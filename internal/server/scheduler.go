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
// goroutines simultaneously.
type TaskRunner interface {
	RunScheduledTask(ctx context.Context, channel, taskDescription string)
}

// ScheduledTask represents a row in the scheduled_tasks table.
type ScheduledTask struct {
	ID       int64
	Name     string
	Schedule string
	Action   string
	Channel  string
	Enabled  bool
	LastRun  sql.NullTime
	NextRun  sql.NullTime
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
func (s *Scheduler) tick(ctx context.Context) {
	now := time.Now().UTC()
	tasks, err := s.getDueTasks(now)
	if err != nil {
		s.logger.Error("scheduler: failed to get due tasks", "error", err)
		return
	}

	for _, task := range tasks {
		// Advance next_run BEFORE dispatching to prevent the same task from
		// being picked up again on the next tick while still running.
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

		// Try to acquire a semaphore slot (non-blocking).
		select {
		case s.semaphore <- struct{}{}:
			// Slot acquired — dispatch the task.
			s.logger.Info("scheduler: dispatching task",
				"task_id", task.ID,
				"name", task.Name,
				"channel", task.Channel,
			)
			s.taskWg.Add(1)
			go s.executeTask(ctx, task)
		default:
			// All slots occupied — skip this task. The next_run has already
			// been advanced, so the task won't fire again until its next
			// scheduled time.
			s.logger.Warn("scheduler: backpressure, skipping task",
				"task_id", task.ID,
				"name", task.Name,
			)
		}
	}
}

// executeTask runs a single scheduled task and updates its last_run.
func (s *Scheduler) executeTask(ctx context.Context, task ScheduledTask) {
	defer s.taskWg.Done()
	defer func() { <-s.semaphore }()
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("scheduler: task panicked",
				"task_id", task.ID,
				"name", task.Name,
				"recover", r,
			)
		}
	}()

	// Run the task via the agent.
	s.runner.RunScheduledTask(ctx, task.Channel, task.Action)

	// Update last_run (next_run was already advanced in tick).
	now := time.Now().UTC()
	if err := s.updateLastRun(task.ID, now); err != nil {
		s.logger.Error("scheduler: failed to update last_run",
			"task_id", task.ID,
			"error", err,
		)
	}
}

// getDueTasks returns enabled tasks whose next_run is at or before the given time.
func (s *Scheduler) getDueTasks(now time.Time) ([]ScheduledTask, error) {
	rows, err := s.db.Query(
		`SELECT id, name, schedule, action, channel, enabled, last_run, next_run
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
		if err := rows.Scan(&t.ID, &t.Name, &t.Schedule, &t.Action, &t.Channel, &t.Enabled, &t.LastRun, &t.NextRun); err != nil {
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

// AddTask adds a new scheduled task and computes its initial next_run.
func (s *Scheduler) AddTask(name, schedule, action, channel string) (int64, error) {
	// Validate the cron expression.
	nextRun, err := s.computeNextRun(schedule, time.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("AddTask: %w", err)
	}

	result, err := s.db.Exec(
		`INSERT INTO scheduled_tasks (name, schedule, action, channel, enabled, next_run)
		 VALUES (?, ?, ?, ?, 1, ?)`,
		name, schedule, action, channel, nextRun,
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

// EnableTask enables a scheduled task and recomputes its next_run.
func (s *Scheduler) EnableTask(id int64) error {
	nextRun, err := s.computeNextRunForTask(id)
	if err != nil {
		return fmt.Errorf("EnableTask: %w", err)
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
		`SELECT id, name, schedule, action, channel, enabled, last_run, next_run
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
		if err := rows.Scan(&t.ID, &t.Name, &t.Schedule, &t.Action, &t.Channel, &t.Enabled, &t.LastRun, &t.NextRun); err != nil {
			return nil, fmt.Errorf("ListTasks: scan: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// computeNextRunForTask reads the schedule for a task and computes its next run.
func (s *Scheduler) computeNextRunForTask(id int64) (time.Time, error) {
	var schedule string
	err := s.db.QueryRow(`SELECT schedule FROM scheduled_tasks WHERE id = ?`, id).Scan(&schedule)
	if err != nil {
		return time.Time{}, fmt.Errorf("computeNextRunForTask: %w", err)
	}
	return s.computeNextRun(schedule, time.Now().UTC())
}
