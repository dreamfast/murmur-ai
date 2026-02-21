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

func (m *mockTaskRunner) RunScheduledTask(_ context.Context, channel, description string) {
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
func newTestScheduler(t *testing.T, runner TaskRunner) (*Scheduler, *db.DB) {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
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

	// Wait briefly for the goroutine to execute.
	time.Sleep(100 * time.Millisecond)

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

func (r *blockingTaskRunner) RunScheduledTask(_ context.Context, _, _ string) {
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
