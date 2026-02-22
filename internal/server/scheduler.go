package server

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"murmur/internal/db"

	"github.com/robfig/cron/v3"
)

// TaskRunner is the interface the Scheduler uses to execute scheduled tasks.
// It is satisfied by Agent.RunScheduledTask. Implementations must be safe for
// concurrent use — the scheduler may call RunScheduledTask from multiple
// goroutines simultaneously. The taskID uniquely identifies the task for
// ephemeral context isolation. The createdBy parameter identifies the user who
// created the task for permission filtering; empty means no filtering.
// The provider parameter is an optional LLM provider name override; empty
// means use the normal resolution chain (channel -> global default).
type TaskRunner interface {
	RunScheduledTask(ctx context.Context, taskID int64, channel, taskDescription, createdBy, provider string)
}

// Task type constants for the scheduled_tasks.type column.
const (
	// TaskTypeCron is a recurring task that fires on a cron schedule.
	TaskTypeCron = "cron"
	// TaskTypeOnce is a one-shot task that fires once at run_at and auto-disables.
	TaskTypeOnce = "once"
)

// oneShotCleanupAge is the age after which disabled one-shot tasks are deleted.
const oneShotCleanupAge = 30 * 24 * time.Hour // 30 days

// ScheduledTask represents a row in the scheduled_tasks table.
type ScheduledTask struct {
	ID        int64
	Name      string
	Schedule  string
	Action    string
	Channel   string
	Enabled   bool
	LastRun   sql.NullTime
	NextRun   sql.NullTime
	Type      string       // "cron" or "once"
	RunAt     sql.NullTime // absolute fire time for one-shot tasks
	CreatedBy string       // IRC nick of the user who created the task; empty for legacy tasks
	Provider  string       // LLM provider name override; empty means use channel/global default
}

// Scheduler runs a tick loop that checks for due scheduled tasks and dispatches
// them to the TaskRunner. It uses a semaphore for backpressure — when all
// concurrent slots are occupied, due tasks are skipped until a slot frees up.
type Scheduler struct {
	db            *db.DB
	runner        TaskRunner
	tickInterval  time.Duration
	maxConcurrent int
	semaphore     chan struct{}
	taskWg        sync.WaitGroup // tracks in-flight task goroutines
	logger        *slog.Logger
	cronParser    cron.Parser
}

// NewScheduler creates a new Scheduler. The runner is called to execute each
// due task. tickInterval controls how often the scheduler checks for due tasks.
// maxConcurrent limits the number of tasks that can run simultaneously.
func NewScheduler(
	database *db.DB,
	runner TaskRunner,
	tickInterval time.Duration,
	maxConcurrent int,
	logger *slog.Logger,
) *Scheduler {
	return &Scheduler{
		db:            database,
		runner:        runner,
		tickInterval:  tickInterval,
		maxConcurrent: maxConcurrent,
		semaphore:     make(chan struct{}, maxConcurrent),
		logger:        logger,
		cronParser:    cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
	}
}

// Run starts the scheduler tick loop. It blocks until the context is cancelled.
// On cancellation, it waits for all in-flight task goroutines to finish before
// returning, ensuring no goroutines are leaked and the database is not accessed
// after the caller closes it.
func (s *Scheduler) Run(ctx context.Context) {
	s.logger.Info("scheduler started", "tick_interval", s.tickInterval, "max_concurrent", s.maxConcurrent)
	ticker := time.NewTicker(s.tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("scheduler stopping, waiting for in-flight tasks")
			s.taskWg.Wait()
			s.logger.Info("scheduler stopped")
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// tick checks for due tasks and dispatches them. Before dispatching, each
// task's next_run is advanced to prevent duplicate dispatch on subsequent ticks.
// One-shot tasks are disabled after execution instead of advancing next_run.
// Periodically cleans up old disabled one-shot tasks.
func (s *Scheduler) tick(ctx context.Context) {
	now := time.Now().UTC()
	tasks, err := s.getDueTasks(now)
	if err != nil {
		s.logger.Error("scheduler: failed to get due tasks", "error", err)
		return
	}

	for _, task := range tasks {
		if task.Type == TaskTypeOnce {
			// One-shot task: push next_run far into the future to prevent
			// re-dispatch on the next tick. The task will be disabled after
			// successful execution in executeTask(). We don't disable here
			// because if backpressure drops the task, it would be lost forever.
			farFuture := now.Add(24 * time.Hour)
			if err := s.updateNextRun(task.ID, farFuture); err != nil {
				s.logger.Error("scheduler: failed to defer one-shot task",
					"task_id", task.ID,
					"error", err,
				)
				continue
			}
		} else {
			// Cron task: advance next_run BEFORE dispatching to prevent the
			// same task from being picked up again on the next tick.
			nextRun, err := s.computeNextRun(task.Schedule, now)
			if err != nil {
				s.logger.Error("scheduler: failed to compute next run",
					"task_id", task.ID,
					"schedule", task.Schedule,
					"error", err,
				)
				continue
			}
			if err := s.updateNextRun(task.ID, nextRun); err != nil {
				s.logger.Error("scheduler: failed to advance next_run",
					"task_id", task.ID,
					"error", err,
				)
				continue
			}
		}

		// Try to acquire a semaphore slot (non-blocking).
		select {
		case s.semaphore <- struct{}{}:
			// Slot acquired — dispatch the task.
			s.logger.Info("scheduler: dispatching task",
				"task_id", task.ID,
				"name", task.Name,
				"type", task.Type,
				"channel", task.Channel,
				"created_by", task.CreatedBy,
			)
			s.taskWg.Add(1)
			go s.executeTask(ctx, task)
		default:
			// All slots occupied — skip this task. For cron tasks, next_run
			// has been advanced so it won't fire again until its next
			// scheduled time. For one-shot tasks, next_run was pushed 24h
			// into the future; it will be retried on the next tick after
			// that time (or sooner if we reset it here).
			if task.Type == TaskTypeOnce {
				// Reset next_run to run_at so the one-shot task is retried
				// on the next tick when a slot is available.
				if task.RunAt.Valid {
					_ = s.updateNextRun(task.ID, task.RunAt.Time)
				}
			}
			s.logger.Warn("scheduler: backpressure, skipping task",
				"task_id", task.ID,
				"name", task.Name,
			)
		}
	}

	// Periodic cleanup: delete disabled one-shot tasks older than 30 days.
	s.cleanupOldOneShotTasks(now)
}

// executeTask runs a single scheduled task and updates its last_run.
// For one-shot tasks, the task is disabled after successful execution. If the
// task panics, the next_run is reset to run_at so it can be retried promptly
// instead of waiting 24 hours.
func (s *Scheduler) executeTask(ctx context.Context, task ScheduledTask) {
	defer s.taskWg.Done()
	defer func() { <-s.semaphore }()

	panicked := true
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("scheduler: task panicked",
				"task_id", task.ID,
				"name", task.Name,
				"recover", r,
			)
		}
		// If the task panicked (or RunScheduledTask never returned normally),
		// reset one-shot tasks' next_run to run_at for prompt retry.
		if panicked && task.Type == TaskTypeOnce && task.RunAt.Valid {
			if err := s.updateNextRun(task.ID, task.RunAt.Time); err != nil {
				s.logger.Error("scheduler: failed to reset one-shot task after panic",
					"task_id", task.ID,
					"error", err,
				)
			} else {
				s.logger.Info("scheduler: reset one-shot task for retry after panic",
					"task_id", task.ID,
					"name", task.Name,
				)
			}
		}
	}()

	// Run the task via the agent. The creator's current permissions are used
	// for tool filtering. Empty CreatedBy (legacy tasks) bypasses filtering.
	// The Provider field allows per-task model override; empty uses the default chain.
	s.runner.RunScheduledTask(ctx, task.ID, task.Channel, task.Action, task.CreatedBy, task.Provider)
	panicked = false

	// Update last_run (next_run was already advanced in tick).
	now := time.Now().UTC()
	if err := s.updateLastRun(task.ID, now); err != nil {
		s.logger.Error("scheduler: failed to update last_run",
			"task_id", task.ID,
			"error", err,
		)
	}

	// One-shot tasks auto-disable after successful execution.
	if task.Type == TaskTypeOnce {
		if err := s.disableTask(task.ID); err != nil {
			s.logger.Error("scheduler: failed to disable one-shot task after execution",
				"task_id", task.ID,
				"error", err,
			)
		} else {
			s.logger.Info("scheduler: one-shot task completed and disabled",
				"task_id", task.ID,
				"name", task.Name,
			)
		}
	}
}

// getDueTasks returns enabled tasks whose next_run is at or before the given time.
func (s *Scheduler) getDueTasks(now time.Time) ([]ScheduledTask, error) {
	rows, err := s.db.Query(
		`SELECT id, name, schedule, action, channel, enabled, last_run, next_run, type, run_at, created_by, provider
		 FROM scheduled_tasks
		 WHERE enabled = 1 AND next_run <= ?
		 ORDER BY next_run ASC
		 LIMIT ?`,
		now, s.maxConcurrent,
	)
	if err != nil {
		return nil, fmt.Errorf("getDueTasks: %w", err)
	}
	defer rows.Close()

	var tasks []ScheduledTask
	for rows.Next() {
		var t ScheduledTask
		if err := rows.Scan(&t.ID, &t.Name, &t.Schedule, &t.Action, &t.Channel, &t.Enabled, &t.LastRun, &t.NextRun, &t.Type, &t.RunAt, &t.CreatedBy, &t.Provider); err != nil {
			return nil, fmt.Errorf("getDueTasks: scan: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// updateNextRun advances the next_run for a task. Called before dispatch to
// prevent duplicate execution on subsequent ticks.
func (s *Scheduler) updateNextRun(taskID int64, nextRun time.Time) error {
	_, err := s.db.Exec(
		`UPDATE scheduled_tasks SET next_run = ? WHERE id = ?`,
		nextRun, taskID,
	)
	if err != nil {
		return fmt.Errorf("updateNextRun: %w", err)
	}
	return nil
}

// updateLastRun records when a task was last executed.
func (s *Scheduler) updateLastRun(taskID int64, lastRun time.Time) error {
	_, err := s.db.Exec(
		`UPDATE scheduled_tasks SET last_run = ? WHERE id = ?`,
		lastRun, taskID,
	)
	if err != nil {
		return fmt.Errorf("updateLastRun: %w", err)
	}
	return nil
}

// computeNextRun computes the next run time for a cron schedule after the given time.
func (s *Scheduler) computeNextRun(schedule string, after time.Time) (time.Time, error) {
	sched, err := s.cronParser.Parse(schedule)
	if err != nil {
		return time.Time{}, fmt.Errorf("computeNextRun: parse %q: %w", schedule, err)
	}
	return sched.Next(after), nil
}

// AddTask adds a new scheduled task and computes its initial next_run. The
// createdBy parameter records the IRC nick of the user who created the task;
// their permissions are used when the scheduler fires the task. The provider
// parameter is an optional LLM provider name override; empty means use the
// normal resolution chain.
func (s *Scheduler) AddTask(name, schedule, action, channel, createdBy, provider string) (int64, error) {
	// Validate the cron expression.
	nextRun, err := s.computeNextRun(schedule, time.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("AddTask: %w", err)
	}

	result, err := s.db.Exec(
		`INSERT INTO scheduled_tasks (name, schedule, action, channel, enabled, next_run, created_by, provider)
		 VALUES (?, ?, ?, ?, 1, ?, ?, ?)`,
		name, schedule, action, channel, nextRun, createdBy, provider,
	)
	if err != nil {
		return 0, fmt.Errorf("AddTask: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("AddTask: last insert id: %w", err)
	}
	return id, nil
}

// RemoveTask deletes a scheduled task by ID.
func (s *Scheduler) RemoveTask(id int64) error {
	result, err := s.db.Exec(`DELETE FROM scheduled_tasks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("RemoveTask: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("RemoveTask: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("RemoveTask: task %d not found", id)
	}
	return nil
}

// EnableTask enables a scheduled task and recomputes its next_run. For cron
// tasks, the next run is computed from the cron schedule. For one-shot tasks,
// the run_at time must still be in the future; otherwise the task cannot be
// re-enabled (it has already fired).
func (s *Scheduler) EnableTask(id int64) error {
	// Read the task type and schedule to determine how to compute next_run.
	var taskType, schedule string
	var runAt sql.NullTime
	err := s.db.QueryRow(
		`SELECT type, schedule, run_at FROM scheduled_tasks WHERE id = ?`, id,
	).Scan(&taskType, &schedule, &runAt)
	if err != nil {
		return fmt.Errorf("EnableTask: %w", err)
	}

	var nextRun time.Time
	if taskType == TaskTypeOnce {
		if !runAt.Valid || runAt.Time.Before(time.Now().UTC()) {
			return fmt.Errorf("EnableTask: one-shot task %d has already fired or has no run_at time", id)
		}
		nextRun = runAt.Time
	} else {
		nextRun, err = s.computeNextRun(schedule, time.Now().UTC())
		if err != nil {
			return fmt.Errorf("EnableTask: %w", err)
		}
	}

	result, err := s.db.Exec(
		`UPDATE scheduled_tasks SET enabled = 1, next_run = ? WHERE id = ?`,
		nextRun, id,
	)
	if err != nil {
		return fmt.Errorf("EnableTask: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("EnableTask: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("EnableTask: task %d not found", id)
	}
	return nil
}

// DisableTask disables a scheduled task.
func (s *Scheduler) DisableTask(id int64) error {
	result, err := s.db.Exec(`UPDATE scheduled_tasks SET enabled = 0 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("DisableTask: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("DisableTask: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("DisableTask: task %d not found", id)
	}
	return nil
}

// ListTasks returns all scheduled tasks.
func (s *Scheduler) ListTasks() ([]ScheduledTask, error) {
	rows, err := s.db.Query(
		`SELECT id, name, schedule, action, channel, enabled, last_run, next_run, type, run_at, created_by, provider
		 FROM scheduled_tasks
		 ORDER BY id ASC
		 LIMIT 50`,
	)
	if err != nil {
		return nil, fmt.Errorf("ListTasks: %w", err)
	}
	defer rows.Close()

	var tasks []ScheduledTask
	for rows.Next() {
		var t ScheduledTask
		if err := rows.Scan(&t.ID, &t.Name, &t.Schedule, &t.Action, &t.Channel, &t.Enabled, &t.LastRun, &t.NextRun, &t.Type, &t.RunAt, &t.CreatedBy, &t.Provider); err != nil {
			return nil, fmt.Errorf("ListTasks: scan: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// AddOneShotTask adds a one-shot task that fires once at the given time and
// then auto-disables. The schedule field is left empty since one-shot tasks
// don't use cron expressions. The createdBy parameter records the IRC nick of
// the user who created the task; their permissions are used when the scheduler
// fires the task. The provider parameter is an optional LLM provider name
// override; empty means use the normal resolution chain.
func (s *Scheduler) AddOneShotTask(name string, runAt time.Time, action, channel, createdBy, provider string) (int64, error) {
	if runAt.Before(time.Now().UTC()) {
		return 0, fmt.Errorf("AddOneShotTask: run_at must be in the future")
	}

	result, err := s.db.Exec(
		`INSERT INTO scheduled_tasks (name, schedule, action, channel, enabled, next_run, type, run_at, created_by, provider)
		 VALUES (?, '', ?, ?, 1, ?, ?, ?, ?, ?)`,
		name, action, channel, runAt, TaskTypeOnce, runAt, createdBy, provider,
	)
	if err != nil {
		return 0, fmt.Errorf("AddOneShotTask: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("AddOneShotTask: last insert id: %w", err)
	}
	return id, nil
}

// disableTask sets enabled=0 for a task. This is the unexported version used
// internally by tick() for one-shot tasks, avoiding the rows-affected check
// that DisableTask performs (the task is guaranteed to exist since we just
// queried it).
func (s *Scheduler) disableTask(id int64) error {
	_, err := s.db.Exec(`UPDATE scheduled_tasks SET enabled = 0 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("disableTask: %w", err)
	}
	return nil
}

// cleanupOldOneShotTasks deletes disabled one-shot tasks that are older than
// oneShotCleanupAge. This prevents the scheduled_tasks table from growing
// unboundedly with expired reminders.
func (s *Scheduler) cleanupOldOneShotTasks(now time.Time) {
	cutoff := now.Add(-oneShotCleanupAge)
	result, err := s.db.Exec(
		`DELETE FROM scheduled_tasks WHERE type = ? AND enabled = 0 AND run_at < ?`,
		TaskTypeOnce, cutoff,
	)
	if err != nil {
		s.logger.Error("scheduler: failed to cleanup old one-shot tasks", "error", err)
		return
	}
	n, _ := result.RowsAffected()
	if n > 0 {
		s.logger.Info("scheduler: cleaned up old one-shot tasks", "count", n)
	}
}
