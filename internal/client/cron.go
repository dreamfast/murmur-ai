package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"murmur/internal/bus"
	"murmur/internal/config"
	"murmur/internal/tools"

	"github.com/robfig/cron/v3"
)

// cronJob is the internal representation of a scheduled cron job, including
// runtime state for change detection and scheduling.
type cronJob struct {
	name               string
	schedule           string
	command            string
	tool               string
	notify             bool
	notifyOnlyOnChange bool
	notifyOnlyOnError  bool
	parsedSchedule     cron.Schedule // parsed once, reused on each tick

	// Runtime state.
	nextRun     time.Time
	lastRun     time.Time // zero value means never run
	lastHash    string    // SHA256 of last output for change detection
	lastStatus  string    // "success" or "error"
	lastChanged bool      // whether the last run's output differed from the previous
}

// CronRunner executes client-side cron jobs on a 1-minute ticker. Jobs are
// executed via the client's tool handlers and results are optionally sent to
// the server via the bus protocol. Change detection uses SHA256 hashing of
// tool output.
type CronRunner struct {
	mu           sync.Mutex
	jobs         []*cronJob
	toolHandlers map[string]tools.Tool
	sender       *bus.Sender
	clientID     string
	logger       *slog.Logger
	cronParser   cron.Parser

	// nowFunc is used for time in tests. Defaults to time.Now().UTC.
	nowFunc func() time.Time
}

// NewCronRunner creates a new CronRunner from the given cron job configs. It
// validates each job's cron schedule and resolves the initial next_run time.
// Jobs referencing tools not present in toolHandlers are skipped with a warning.
func NewCronRunner(
	configs []config.CronJobConfig,
	toolHandlers map[string]tools.Tool,
	sender *bus.Sender,
	clientID string,
	logger *slog.Logger,
) (*CronRunner, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	now := time.Now().UTC()

	cr := &CronRunner{
		toolHandlers: toolHandlers,
		sender:       sender,
		clientID:     clientID,
		logger:       logger,
		cronParser:   parser,
		nowFunc:      func() time.Time { return time.Now().UTC() },
	}

	for _, cfg := range configs {
		// Validate that the referenced tool exists.
		if _, ok := toolHandlers[cfg.Tool]; !ok {
			logger.Warn("cron job references unknown tool, skipping",
				"job", cfg.Name,
				"tool", cfg.Tool,
			)
			continue
		}

		// Parse and validate the cron schedule.
		sched, err := parser.Parse(cfg.Schedule)
		if err != nil {
			return nil, fmt.Errorf("NewCronRunner: job %q: invalid schedule %q: %w", cfg.Name, cfg.Schedule, err)
		}

		j := &cronJob{
			name:               cfg.Name,
			schedule:           cfg.Schedule,
			command:            cfg.Command,
			tool:               cfg.Tool,
			notify:             cfg.Notify,
			notifyOnlyOnChange: cfg.NotifyOnlyOnChange,
			notifyOnlyOnError:  cfg.NotifyOnlyOnError,
			parsedSchedule:     sched,
			nextRun:            sched.Next(now),
		}
		cr.jobs = append(cr.jobs, j)
		logger.Info("registered cron job",
			"name", cfg.Name,
			"schedule", cfg.Schedule,
			"tool", cfg.Tool,
			"next_run", j.nextRun,
		)
	}

	return cr, nil
}

// Run starts the cron ticker loop. It checks for due jobs every minute and
// executes them. It blocks until the context is cancelled.
func (cr *CronRunner) Run(ctx context.Context) {
	cr.logger.Info("cron runner started", "jobs", len(cr.jobs))
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			cr.logger.Info("cron runner stopped")
			return
		case <-ticker.C:
			cr.tick(ctx)
		}
	}
}

// tick checks for due jobs and executes them sequentially.
func (cr *CronRunner) tick(ctx context.Context) {
	cr.mu.Lock()
	now := cr.nowFunc()
	var due []*cronJob
	for _, j := range cr.jobs {
		if !j.nextRun.After(now) {
			due = append(due, j)
		}
	}
	cr.mu.Unlock()

	for _, j := range due {
		cr.executeJob(ctx, j)
	}
}

// executeJob runs a single cron job, handles change detection, and sends
// notifications if appropriate.
func (cr *CronRunner) executeJob(ctx context.Context, j *cronJob) {
	cr.logger.Info("executing cron job", "name", j.name, "tool", j.tool)

	tool, ok := cr.toolHandlers[j.tool]
	if !ok {
		cr.logger.Error("cron job tool not found", "name", j.name, "tool", j.tool)
		return
	}

	// Build arguments for the tool handler.
	args := map[string]any{
		"command": j.command,
	}

	// Execute the tool with a 2-minute timeout.
	execCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	start := time.Now()
	result, err := tool.Handler(execCtx, args)
	duration := time.Since(start)

	status := "success"
	output := result
	if err != nil {
		status = "error"
		output = err.Error()
	}

	// Compute SHA256 hash for change detection.
	hash := hashOutput(output)
	changed := hash != j.lastHash

	cr.logger.Info("cron job completed",
		"name", j.name,
		"status", status,
		"duration", duration,
		"changed", changed,
	)

	// Update runtime state under lock.
	cr.mu.Lock()
	now := cr.nowFunc()
	j.lastHash = hash
	j.lastStatus = status
	j.lastChanged = changed
	j.lastRun = now

	// Advance next_run using the pre-parsed schedule.
	j.nextRun = j.parsedSchedule.Next(now)
	cr.mu.Unlock()

	// Determine whether to notify.
	if !cr.shouldNotify(j, status, changed) {
		return
	}

	// Send notification via bus.
	cr.sendResult(j.name, status, output, changed)
}

// shouldNotify determines whether a cron job result should be sent to the
// server based on the job's notification settings.
//
// Precedence rules:
//   - Notify=false: never notify (overrides everything)
//   - Notify=true + both false: always notify (backward compat)
//   - NotifyOnlyOnError=true: only on error
//   - NotifyOnlyOnChange=true: only on change
//   - Both true: notify on error OR change
func (cr *CronRunner) shouldNotify(j *cronJob, status string, changed bool) bool {
	if !j.notify {
		return false
	}

	// If neither filter is set, always notify.
	if !j.notifyOnlyOnChange && !j.notifyOnlyOnError {
		return true
	}

	// Notify if error filter matches.
	if j.notifyOnlyOnError && status == "error" {
		return true
	}

	// Notify if change filter matches.
	if j.notifyOnlyOnChange && changed {
		return true
	}

	return false
}

// sendResult sends a CronResultMessage to the server via the bus.
func (cr *CronRunner) sendResult(jobName, status, output string, changed bool) {
	if cr.sender == nil {
		cr.logger.Debug("cron result not sent: no bus sender configured",
			"job", jobName,
			"status", status,
		)
		return
	}

	// Truncate output to avoid bus message explosion.
	if len(output) > tools.MaxOutputBytes {
		output = output[:tools.MaxOutputBytes] + "\n... [output truncated]"
	}

	msg := &bus.CronResultMessage{
		Type:      bus.TypeCronResult,
		ClientID:  cr.clientID,
		JobName:   jobName,
		Status:    status,
		Output:    output,
		Changed:   changed,
		Timestamp: cr.nowFunc().Format(time.RFC3339),
	}

	if err := cr.sender.Send(msg); err != nil {
		cr.logger.Error("failed to send cron result",
			"job", jobName,
			"error", err,
		)
	}
}

// AddJob adds a new cron job at runtime (e.g., via bus CronAdd message).
// Returns an error if required fields are missing, the schedule is invalid,
// or the tool is not available.
func (cr *CronRunner) AddJob(job bus.CronJob) error {
	if job.Name == "" {
		return fmt.Errorf("AddJob: name is required")
	}
	if job.Tool == "" {
		return fmt.Errorf("AddJob: tool is required")
	}

	sched, err := cr.cronParser.Parse(job.Schedule)
	if err != nil {
		return fmt.Errorf("AddJob: invalid schedule %q: %w", job.Schedule, err)
	}

	if _, ok := cr.toolHandlers[job.Tool]; !ok {
		return fmt.Errorf("AddJob: unknown tool %q", job.Tool)
	}

	j := &cronJob{
		name:               job.Name,
		schedule:           job.Schedule,
		command:            job.Command,
		tool:               job.Tool,
		notify:             job.Notify,
		notifyOnlyOnChange: job.NotifyOnlyOnChange,
		notifyOnlyOnError:  job.NotifyOnlyOnError,
		parsedSchedule:     sched,
		nextRun:            sched.Next(cr.nowFunc()),
	}

	cr.mu.Lock()
	defer cr.mu.Unlock()

	// Check for duplicate name.
	for _, existing := range cr.jobs {
		if existing.name == job.Name {
			return fmt.Errorf("AddJob: job %q already exists", job.Name)
		}
	}

	cr.jobs = append(cr.jobs, j)
	cr.logger.Info("added cron job via bus",
		"name", job.Name,
		"schedule", job.Schedule,
		"tool", job.Tool,
	)
	return nil
}

// RemoveJob removes a cron job by name. Returns an error if the job is not found.
func (cr *CronRunner) RemoveJob(name string) error {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	for i, j := range cr.jobs {
		if j.name == name {
			cr.jobs = append(cr.jobs[:i], cr.jobs[i+1:]...)
			cr.logger.Info("removed cron job", "name", name)
			return nil
		}
	}
	return fmt.Errorf("RemoveJob: job %q not found", name)
}

// ListJobs returns information about all registered cron jobs.
func (cr *CronRunner) ListJobs() []bus.CronJobInfo {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	infos := make([]bus.CronJobInfo, len(cr.jobs))
	for i, j := range cr.jobs {
		var lastRun string
		if !j.lastRun.IsZero() {
			lastRun = j.lastRun.Format(time.RFC3339)
		}
		infos[i] = bus.CronJobInfo{
			Name:        j.name,
			Schedule:    j.schedule,
			LastRun:     lastRun,
			NextRun:     j.nextRun.Format(time.RFC3339),
			LastStatus:  j.lastStatus,
			LastChanged: j.lastChanged,
		}
	}
	return infos
}

// hashOutput computes the SHA256 hex digest of the given string.
func hashOutput(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
