package server

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"murmur/internal/db"
)

// mockTaskRunner records calls to RunScheduledTask.
type mockTaskRunner struct {
	mu    sync.Mutex
	calls []taskRunCall
}

type taskRunCall struct {
	Channel     string
	Description string
}

func (m *mockTaskRunner) RunScheduledTask(_ context.Context, channel, description, _ string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, taskRunCall{Channel: channel, Description: description})
}

func (m *mockTaskRunner) getCalls() []taskRunCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]taskRunCall, len(m.calls))
	copy(result, m.calls)
	return result
}

// newTestScheduler creates a Scheduler with an in-memory DB for testing.
// MaxOpenConns is set to 1 to ensure all operations use the same in-memory
// database connection (each ":memory:" connection gets its own database).
func newTestScheduler(t *testing.T, runner TaskRunner) (*Scheduler, *db.DB) {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	database.SetMaxOpenConns(1)
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	s := NewScheduler(database, runner, 30*time.Second, 3, logger)
	return s, database
}

func TestScheduler_AddAndListTasks(t *testing.T) {
	t.Parallel()

	runner := &mockTaskRunner{}
	s, _ := newTestScheduler(t, runner)

	id, err := s.AddTask("daily-check", "0 9 * * *", "Check system health", "#murmur")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if id <= 0 {
		t.Errorf("expected positive task ID, got %d", id)
	}

	tasks, err := s.ListTasks()
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Name != "daily-check" {
		t.Errorf("task name = %q, want %q", tasks[0].Name, "daily-check")
	}
	if tasks[0].Schedule != "0 9 * * *" {
		t.Errorf("task schedule = %q, want %q", tasks[0].Schedule, "0 9 * * *")
	}
	if tasks[0].Action != "Check system health" {
		t.Errorf("task action = %q, want %q", tasks[0].Action, "Check system health")
	}
	if tasks[0].Channel != "#murmur" {
		t.Errorf("task channel = %q, want %q", tasks[0].Channel, "#murmur")
	}
	if !tasks[0].Enabled {
		t.Error("task should be enabled by default")
	}
	if !tasks[0].NextRun.Valid {
		t.Error("task should have a next_run time")
	}
}

func TestScheduler_RemoveTask(t *testing.T) {
	t.Parallel()

	runner := &mockTaskRunner{}
	s, _ := newTestScheduler(t, runner)

	id, err := s.AddTask("temp-task", "*/5 * * * *", "Temporary", "#test")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	if err := s.RemoveTask(id); err != nil {
		t.Fatalf("RemoveTask: %v", err)
	}

	tasks, err := s.ListTasks()
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks after removal, got %d", len(tasks))
	}

	// Removing a non-existent task should return an error.
	if err := s.RemoveTask(999); err == nil {
		t.Error("expected error removing non-existent task")
	}
}

func TestScheduler_TickFiresDueTask(t *testing.T) {
	t.Parallel()

	runner := &mockTaskRunner{}
	s, database := newTestScheduler(t, runner)

	// Insert a task with next_run in the past so it's immediately due.
	past := time.Now().UTC().Add(-1 * time.Minute)
	_, err := database.Exec(
		`INSERT INTO scheduled_tasks (name, schedule, action, channel, enabled, next_run)
		 VALUES (?, ?, ?, ?, 1, ?)`,
		"due-task", "*/5 * * * *", "Run health check", "#murmur", past,
	)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}

	// Run a single tick.
	s.tick(context.Background())

	// Wait for all in-flight task goroutines to finish.
	s.taskWg.Wait()

	calls := runner.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 task execution, got %d", len(calls))
	}
	if calls[0].Channel != "#murmur" {
		t.Errorf("channel = %q, want %q", calls[0].Channel, "#murmur")
	}
	if calls[0].Description != "Run health check" {
		t.Errorf("description = %q, want %q", calls[0].Description, "Run health check")
	}
}

func TestScheduler_SkipsDisabledTasks(t *testing.T) {
	t.Parallel()

	runner := &mockTaskRunner{}
	s, _ := newTestScheduler(t, runner)

	id, err := s.AddTask("disabled-task", "*/5 * * * *", "Should not run", "#test")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	// Disable the task.
	if err := s.DisableTask(id); err != nil {
		t.Fatalf("DisableTask: %v", err)
	}

	// Run a tick — the disabled task should not fire.
	s.tick(context.Background())
	time.Sleep(100 * time.Millisecond)

	calls := runner.getCalls()
	if len(calls) != 0 {
		t.Errorf("expected 0 task executions for disabled task, got %d", len(calls))
	}
}

func TestScheduler_BackpressureSkips(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	database.SetMaxOpenConns(1)
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	// Create a runner that blocks until signalled.
	var started atomic.Int32
	unblock := make(chan struct{})
	runner := &blockingTaskRunner{
		started: &started,
		unblock: unblock,
	}

	// maxConcurrent=1 so only one task can run at a time.
	s := NewScheduler(database, runner, 30*time.Second, 1, logger)

	// Insert two tasks that are both due.
	past := time.Now().UTC().Add(-1 * time.Minute)
	for i := 0; i < 2; i++ {
		_, err := database.Exec(
			`INSERT INTO scheduled_tasks (name, schedule, action, channel, enabled, next_run)
			 VALUES (?, ?, ?, ?, 1, ?)`,
			"task", "*/5 * * * *", "action", "#test", past,
		)
		if err != nil {
			t.Fatalf("insert task %d: %v", i, err)
		}
	}

	// First tick: one task starts, second is skipped due to backpressure.
	s.tick(context.Background())

	// Wait for the first task to start.
	deadline := time.After(2 * time.Second)
	for started.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for first task to start")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Second tick while first is still running — should skip.
	s.tick(context.Background())

	// Only 1 task should have started (the second was skipped).
	time.Sleep(50 * time.Millisecond)
	if started.Load() != 1 {
		t.Errorf("expected 1 started task, got %d", started.Load())
	}

	// Unblock the first task.
	close(unblock)
	time.Sleep(50 * time.Millisecond)
}

// blockingTaskRunner blocks until the unblock channel is closed.
type blockingTaskRunner struct {
	started *atomic.Int32
	unblock chan struct{}
}

func (r *blockingTaskRunner) RunScheduledTask(_ context.Context, _, _, _ string) {
	r.started.Add(1)
	<-r.unblock
}

func TestScheduler_NextRunComputed(t *testing.T) {
	t.Parallel()

	runner := &mockTaskRunner{}
	s, _ := newTestScheduler(t, runner)

	// Every 5 minutes.
	now := time.Date(2026, 2, 20, 10, 0, 0, 0, time.UTC)
	next, err := s.computeNextRun("*/5 * * * *", now)
	if err != nil {
		t.Fatalf("computeNextRun: %v", err)
	}
	expected := time.Date(2026, 2, 20, 10, 5, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("next = %v, want %v", next, expected)
	}
}

func TestScheduler_InvalidCronExpression(t *testing.T) {
	t.Parallel()

	runner := &mockTaskRunner{}
	s, _ := newTestScheduler(t, runner)

	_, err := s.AddTask("bad-task", "not-a-cron", "action", "#test")
	if err == nil {
		t.Fatal("expected error for invalid cron expression")
	}
}

func TestScheduler_EnableDisableTask(t *testing.T) {
	t.Parallel()

	runner := &mockTaskRunner{}
	s, _ := newTestScheduler(t, runner)

	id, err := s.AddTask("toggle-task", "0 * * * *", "Hourly check", "#test")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	// Disable.
	if err := s.DisableTask(id); err != nil {
		t.Fatalf("DisableTask: %v", err)
	}
	tasks, _ := s.ListTasks()
	if tasks[0].Enabled {
		t.Error("task should be disabled")
	}

	// Re-enable.
	if err := s.EnableTask(id); err != nil {
		t.Fatalf("EnableTask: %v", err)
	}
	tasks, _ = s.ListTasks()
	if !tasks[0].Enabled {
		t.Error("task should be enabled")
	}
	if !tasks[0].NextRun.Valid {
		t.Error("re-enabled task should have a next_run")
	}
}

func TestScheduler_EnableNonExistent(t *testing.T) {
	t.Parallel()

	runner := &mockTaskRunner{}
	s, _ := newTestScheduler(t, runner)

	if err := s.EnableTask(999); err == nil {
		t.Error("expected error enabling non-existent task")
	}
}

func TestScheduler_DisableNonExistent(t *testing.T) {
	t.Parallel()

	runner := &mockTaskRunner{}
	s, _ := newTestScheduler(t, runner)

	if err := s.DisableTask(999); err == nil {
		t.Error("expected error disabling non-existent task")
	}
}

func TestTaskCommands_List(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	database.SetMaxOpenConns(1)
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	runner := &mockTaskRunner{}
	scheduler := NewScheduler(database, runner, 30*time.Second, 3, logger)

	_, err = scheduler.AddTask("daily-check", "0 9 * * *", "Check health", "#murmur")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	var sent []string
	handler := &CommandHandler{
		scheduler: scheduler,
		logger:    logger,
		sendFunc: func(_, message string) {
			sent = append(sent, message)
		},
	}

	handler.HandleCommand("#test", "admin", "!tasks")
	if len(sent) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sent))
	}
	if !strings.Contains(sent[0], "daily-check") {
		t.Errorf("expected 'daily-check' in output, got: %s", sent[0])
	}
	if !strings.Contains(sent[0], "0 9 * * *") {
		t.Errorf("expected cron expression in output, got: %s", sent[0])
	}
}

func TestTaskCommands_AddAndRemove(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	database.SetMaxOpenConns(1)
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	runner := &mockTaskRunner{}
	scheduler := NewScheduler(database, runner, 30*time.Second, 3, logger)

	var sent []string
	handler := &CommandHandler{
		scheduler: scheduler,
		logger:    logger,
		sendFunc: func(_, message string) {
			sent = append(sent, message)
		},
	}

	// Add a task: !task add 0 9 * * * Check system health
	handler.HandleCommand("#test", "admin", "!task add 0 9 * * * Check system health")
	if len(sent) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sent))
	}
	if !strings.Contains(sent[0], "task #") {
		t.Errorf("expected task confirmation, got: %s", sent[0])
	}

	// Verify task was added.
	tasks, _ := scheduler.ListTasks()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}

	// Remove the task.
	sent = nil
	handler.HandleCommand("#test", "admin", "!task remove 1")
	if len(sent) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sent))
	}
	if !strings.Contains(sent[0], "removed") {
		t.Errorf("expected removal confirmation, got: %s", sent[0])
	}

	tasks, _ = scheduler.ListTasks()
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks after removal, got %d", len(tasks))
	}
}

func TestScheduler_AddOneShotTask(t *testing.T) {
	t.Parallel()

	runner := &mockTaskRunner{}
	s, _ := newTestScheduler(t, runner)

	runAt := time.Now().UTC().Add(2 * time.Hour)
	id, err := s.AddOneShotTask("test-reminder", runAt, "[Reminder] test", "#murmur")
	if err != nil {
		t.Fatalf("AddOneShotTask: %v", err)
	}
	if id <= 0 {
		t.Errorf("expected positive task ID, got %d", id)
	}

	tasks, err := s.ListTasks()
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Type != TaskTypeOnce {
		t.Errorf("task type = %q, want %q", tasks[0].Type, TaskTypeOnce)
	}
	if !tasks[0].RunAt.Valid {
		t.Error("one-shot task should have a run_at time")
	}
	if !tasks[0].RunAt.Time.Equal(runAt) {
		t.Errorf("run_at = %v, want %v", tasks[0].RunAt.Time, runAt)
	}
	if tasks[0].Schedule != "" {
		t.Errorf("one-shot task schedule should be empty, got %q", tasks[0].Schedule)
	}
}

func TestScheduler_AddOneShotTask_PastTime(t *testing.T) {
	t.Parallel()

	runner := &mockTaskRunner{}
	s, _ := newTestScheduler(t, runner)

	past := time.Now().UTC().Add(-1 * time.Hour)
	_, err := s.AddOneShotTask("past-reminder", past, "[Reminder] past", "#murmur")
	if err == nil {
		t.Fatal("expected error for past run_at time")
	}
}

func TestScheduler_OneShotTickDisables(t *testing.T) {
	t.Parallel()

	runner := &mockTaskRunner{}
	s, database := newTestScheduler(t, runner)

	// Insert a one-shot task with next_run in the past so it's immediately due.
	past := time.Now().UTC().Add(-1 * time.Minute)
	_, err := database.Exec(
		`INSERT INTO scheduled_tasks (name, schedule, action, channel, enabled, next_run, type, run_at)
		 VALUES (?, '', ?, ?, 1, ?, 'once', ?)`,
		"due-reminder", "[Reminder] test", "#murmur", past, past,
	)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}

	// Run a single tick.
	s.tick(context.Background())

	// Wait for all in-flight task goroutines to finish before checking DB state.
	s.taskWg.Wait()

	// Verify the task was executed.
	calls := runner.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 task execution, got %d", len(calls))
	}
	if calls[0].Description != "[Reminder] test" {
		t.Errorf("description = %q, want %q", calls[0].Description, "[Reminder] test")
	}

	// Verify the task was disabled after execution.
	tasks, err := s.ListTasks()
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Enabled {
		t.Error("one-shot task should be disabled after execution")
	}
}

func TestScheduler_OneShotPanicResetsNextRun(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	database.SetMaxOpenConns(1)
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	// Use a runner that panics.
	runner := &panickingTaskRunner{}
	s := NewScheduler(database, runner, 30*time.Second, 3, logger)

	// Insert a one-shot task with next_run in the past so it's immediately due.
	past := time.Now().UTC().Add(-1 * time.Minute)
	_, err = database.Exec(
		`INSERT INTO scheduled_tasks (name, schedule, action, channel, enabled, next_run, type, run_at)
		 VALUES (?, '', ?, ?, 1, ?, 'once', ?)`,
		"panic-reminder", "[Reminder] panic test", "#murmur", past, past,
	)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}

	// Run a single tick — the task will panic during execution.
	s.tick(context.Background())

	// Wait for all in-flight task goroutines to finish (panic recovery + next_run reset).
	s.taskWg.Wait()

	// Verify the task is still enabled (not disabled, since it panicked).
	tasks, err := s.ListTasks()
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if !tasks[0].Enabled {
		t.Error("one-shot task should still be enabled after panic (for retry)")
	}
	// Verify next_run was reset to run_at (not left 24h in the future).
	if !tasks[0].NextRun.Valid {
		t.Fatal("task should have a next_run")
	}
	if tasks[0].NextRun.Time.After(time.Now().UTC()) {
		t.Error("next_run should have been reset to run_at (in the past) for prompt retry, not left in the future")
	}
}

// panickingTaskRunner panics when RunScheduledTask is called.
type panickingTaskRunner struct{}

func (r *panickingTaskRunner) RunScheduledTask(_ context.Context, _, _, _ string) {
	panic("simulated task panic")
}

func TestScheduler_EnableOneShotTask(t *testing.T) {
	t.Parallel()

	runner := &mockTaskRunner{}
	s, _ := newTestScheduler(t, runner)

	// Add a one-shot task in the future.
	runAt := time.Now().UTC().Add(2 * time.Hour)
	id, err := s.AddOneShotTask("future-reminder", runAt, "[Reminder] future", "#murmur")
	if err != nil {
		t.Fatalf("AddOneShotTask: %v", err)
	}

	// Disable it.
	if err := s.DisableTask(id); err != nil {
		t.Fatalf("DisableTask: %v", err)
	}

	// Re-enable it — should succeed since run_at is in the future.
	if err := s.EnableTask(id); err != nil {
		t.Fatalf("EnableTask: %v", err)
	}

	tasks, _ := s.ListTasks()
	if !tasks[0].Enabled {
		t.Error("task should be enabled")
	}
}

func TestScheduler_EnableOneShotTask_PastRunAt(t *testing.T) {
	t.Parallel()

	runner := &mockTaskRunner{}
	s, database := newTestScheduler(t, runner)

	// Insert a disabled one-shot task with run_at in the past.
	past := time.Now().UTC().Add(-1 * time.Hour)
	_, err := database.Exec(
		`INSERT INTO scheduled_tasks (name, schedule, action, channel, enabled, next_run, type, run_at)
		 VALUES (?, '', ?, ?, 0, ?, 'once', ?)`,
		"past-reminder", "[Reminder] past", "#murmur", past, past,
	)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}

	// Re-enabling should fail since run_at is in the past.
	if err := s.EnableTask(1); err == nil {
		t.Fatal("expected error enabling one-shot task with past run_at")
	}
}

func TestScheduler_CleanupOldOneShotTasks(t *testing.T) {
	t.Parallel()

	runner := &mockTaskRunner{}
	s, database := newTestScheduler(t, runner)

	// Insert a disabled one-shot task with run_at 31 days ago.
	oldTime := time.Now().UTC().Add(-31 * 24 * time.Hour)
	_, err := database.Exec(
		`INSERT INTO scheduled_tasks (name, schedule, action, channel, enabled, next_run, type, run_at)
		 VALUES (?, '', ?, ?, 0, ?, 'once', ?)`,
		"old-reminder", "[Reminder] old", "#murmur", oldTime, oldTime,
	)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}

	// Insert a disabled one-shot task with run_at 1 day ago (should NOT be cleaned up).
	recentTime := time.Now().UTC().Add(-1 * 24 * time.Hour)
	_, err = database.Exec(
		`INSERT INTO scheduled_tasks (name, schedule, action, channel, enabled, next_run, type, run_at)
		 VALUES (?, '', ?, ?, 0, ?, 'once', ?)`,
		"recent-reminder", "[Reminder] recent", "#murmur", recentTime, recentTime,
	)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}

	// Run cleanup.
	s.cleanupOldOneShotTasks(time.Now().UTC())

	// Verify only the old task was deleted.
	tasks, err := s.ListTasks()
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task after cleanup, got %d", len(tasks))
	}
	if tasks[0].Name != "recent-reminder" {
		t.Errorf("remaining task = %q, want %q", tasks[0].Name, "recent-reminder")
	}
}

func TestParseReminderTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
		check   func(t *testing.T, result time.Time)
	}{
		{
			name:  "relative hours",
			input: "+2h",
			check: func(t *testing.T, result time.Time) {
				t.Helper()
				expected := time.Now().UTC().Add(2 * time.Hour)
				if result.Sub(expected).Abs() > 5*time.Second {
					t.Errorf("result %v not within 5s of expected %v", result, expected)
				}
			},
		},
		{
			name:  "relative minutes",
			input: "+30m",
			check: func(t *testing.T, result time.Time) {
				t.Helper()
				expected := time.Now().UTC().Add(30 * time.Minute)
				if result.Sub(expected).Abs() > 5*time.Second {
					t.Errorf("result %v not within 5s of expected %v", result, expected)
				}
			},
		},
		{
			name:  "relative days",
			input: "+1d",
			check: func(t *testing.T, result time.Time) {
				t.Helper()
				expected := time.Now().UTC().AddDate(0, 0, 1)
				if result.Sub(expected).Abs() > 5*time.Second {
					t.Errorf("result %v not within 5s of expected %v", result, expected)
				}
			},
		},
		{
			name:  "ISO 8601 with timezone",
			input: "2026-02-22T15:00:00Z",
			check: func(t *testing.T, result time.Time) {
				t.Helper()
				expected := time.Date(2026, 2, 22, 15, 0, 0, 0, time.UTC)
				if !result.Equal(expected) {
					t.Errorf("result = %v, want %v", result, expected)
				}
			},
		},
		{
			name:  "ISO 8601 without timezone",
			input: "2026-02-22T15:00:00",
			check: func(t *testing.T, result time.Time) {
				t.Helper()
				expected := time.Date(2026, 2, 22, 15, 0, 0, 0, time.UTC)
				if !result.Equal(expected) {
					t.Errorf("result = %v, want %v", result, expected)
				}
			},
		},
		{
			name:  "date only",
			input: "2026-02-22",
			check: func(t *testing.T, result time.Time) {
				t.Helper()
				expected := time.Date(2026, 2, 22, 0, 0, 0, 0, time.UTC)
				if !result.Equal(expected) {
					t.Errorf("result = %v, want %v", result, expected)
				}
			},
		},
		{
			name:    "invalid format",
			input:   "next tuesday",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := parseReminderTime(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseReminderTime(%q): %v", tt.input, err)
			}
			if tt.check != nil {
				tt.check(t, result)
			}
		})
	}
}

func TestTaskCommands_NoScheduler(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var sent []string
	handler := &CommandHandler{
		scheduler: nil,
		logger:    logger,
		sendFunc: func(_, message string) {
			sent = append(sent, message)
		},
	}

	handler.HandleCommand("#test", "admin", "!tasks")
	if len(sent) != 1 {
		t.Fatalf("expected 1 message, got %d", len(sent))
	}
	if sent[0] != "scheduler not enabled" {
		t.Errorf("expected 'scheduler not enabled', got: %s", sent[0])
	}
}
