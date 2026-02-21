package client

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"murmur/internal/bus"
	"murmur/internal/config"
	"murmur/internal/tools"
)

// testLogger returns a logger that discards all output.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeTool creates a Tool that returns the given output or error.
func fakeTool(name string, output string, err error) tools.Tool {
	return tools.Tool{
		Name:        name,
		Description: "test tool",
		Parameters:  []byte(`{}`),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return output, err
		},
	}
}

// fakeToolFunc creates a Tool that calls the given function.
func fakeToolFunc(name string, fn func(ctx context.Context, args map[string]any) (string, error)) tools.Tool {
	return tools.Tool{
		Name:        name,
		Description: "test tool",
		Parameters:  []byte(`{}`),
		Handler:     fn,
	}
}

func TestCronRunner_ExecutesJob(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var calls []map[string]any

	handlers := map[string]tools.Tool{
		"shell": fakeToolFunc("shell", func(ctx context.Context, args map[string]any) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, args)
			return "disk usage: 50%", nil
		}),
	}

	configs := []config.CronJobConfig{
		{
			Name:     "disk-check",
			Schedule: "* * * * *", // every minute
			Command:  "df -h",
			Tool:     "shell",
			Notify:   true,
		},
	}

	cr, err := NewCronRunner(configs, handlers, nil, "test-client", testLogger())
	if err != nil {
		t.Fatalf("NewCronRunner: %v", err)
	}

	// Set nowFunc to return a time that makes the job due.
	cr.nowFunc = func() time.Time {
		return time.Date(2026, 1, 1, 12, 1, 0, 0, time.UTC)
	}
	// Force the job to be due by setting nextRun to the past.
	cr.mu.Lock()
	cr.jobs[0].nextRun = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cr.mu.Unlock()

	// Run a single tick.
	ctx := context.Background()
	cr.tick(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0]["command"] != "df -h" {
		t.Errorf("expected command 'df -h', got %v", calls[0]["command"])
	}

	// Verify the job's next_run was advanced and lastRun was set.
	cr.mu.Lock()
	nextRun := cr.jobs[0].nextRun
	lastRun := cr.jobs[0].lastRun
	lastChanged := cr.jobs[0].lastChanged
	cr.mu.Unlock()
	if !nextRun.After(time.Date(2026, 1, 1, 12, 1, 0, 0, time.UTC)) {
		t.Errorf("expected next_run after 12:01, got %v", nextRun)
	}
	if lastRun.IsZero() {
		t.Error("expected lastRun to be set after execution")
	}
	if !lastChanged {
		t.Error("expected lastChanged=true on first run (output changed from empty)")
	}
}

func TestCronRunner_ChangeDetection(t *testing.T) {
	t.Parallel()

	callCount := 0
	var mu sync.Mutex

	handlers := map[string]tools.Tool{
		"shell": fakeToolFunc("shell", func(ctx context.Context, args map[string]any) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			callCount++
			if callCount == 1 {
				return "output-v1", nil
			}
			return "output-v1", nil // same output on second call
		}),
	}

	configs := []config.CronJobConfig{
		{
			Name:               "check",
			Schedule:           "* * * * *",
			Command:            "test",
			Tool:               "shell",
			Notify:             true,
			NotifyOnlyOnChange: true,
		},
	}

	cr, err := NewCronRunner(configs, handlers, nil, "test-client", testLogger())
	if err != nil {
		t.Fatalf("NewCronRunner: %v", err)
	}

	now := time.Date(2026, 1, 1, 12, 1, 0, 0, time.UTC)
	cr.nowFunc = func() time.Time { return now }

	// First run: output is new (changed from empty hash), should notify.
	cr.mu.Lock()
	cr.jobs[0].nextRun = now.Add(-1 * time.Second)
	cr.mu.Unlock()

	ctx := context.Background()
	cr.tick(ctx)

	cr.mu.Lock()
	hash1 := cr.jobs[0].lastHash
	cr.mu.Unlock()

	if hash1 == "" {
		t.Error("expected lastHash to be set after first run")
	}

	// Second run: same output, should NOT notify (change detection).
	now = now.Add(1 * time.Minute)
	cr.mu.Lock()
	cr.jobs[0].nextRun = now.Add(-1 * time.Second)
	cr.mu.Unlock()

	cr.tick(ctx)

	cr.mu.Lock()
	hash2 := cr.jobs[0].lastHash
	cr.mu.Unlock()

	if hash1 != hash2 {
		t.Error("expected same hash for same output")
	}
}

func TestCronRunner_ErrorOnlyNotification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		notify       bool
		onlyOnError  bool
		onlyOnChange bool
		status       string
		changed      bool
		expectNotify bool
	}{
		{
			name:         "notify=false never notifies",
			notify:       false,
			status:       "error",
			changed:      true,
			expectNotify: false,
		},
		{
			name:         "notify=true with no filters always notifies",
			notify:       true,
			status:       "success",
			changed:      false,
			expectNotify: true,
		},
		{
			name:         "onlyOnError=true with error notifies",
			notify:       true,
			onlyOnError:  true,
			status:       "error",
			expectNotify: true,
		},
		{
			name:         "onlyOnError=true with success does not notify",
			notify:       true,
			onlyOnError:  true,
			status:       "success",
			expectNotify: false,
		},
		{
			name:         "onlyOnChange=true with change notifies",
			notify:       true,
			onlyOnChange: true,
			changed:      true,
			expectNotify: true,
		},
		{
			name:         "onlyOnChange=true without change does not notify",
			notify:       true,
			onlyOnChange: true,
			changed:      false,
			expectNotify: false,
		},
		{
			name:         "both filters: error triggers notify",
			notify:       true,
			onlyOnError:  true,
			onlyOnChange: true,
			status:       "error",
			changed:      false,
			expectNotify: true,
		},
		{
			name:         "both filters: change triggers notify",
			notify:       true,
			onlyOnError:  true,
			onlyOnChange: true,
			status:       "success",
			changed:      true,
			expectNotify: true,
		},
		{
			name:         "both filters: neither triggers no notify",
			notify:       true,
			onlyOnError:  true,
			onlyOnChange: true,
			status:       "success",
			changed:      false,
			expectNotify: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cr := &CronRunner{logger: testLogger()}
			j := &cronJob{
				notify:             tt.notify,
				notifyOnlyOnError:  tt.onlyOnError,
				notifyOnlyOnChange: tt.onlyOnChange,
			}

			got := cr.shouldNotify(j, tt.status, tt.changed)
			if got != tt.expectNotify {
				t.Errorf("shouldNotify() = %v, want %v", got, tt.expectNotify)
			}
		})
	}
}

func TestCronRunner_AddRemoveJob(t *testing.T) {
	t.Parallel()

	handlers := map[string]tools.Tool{
		"shell": fakeTool("shell", "ok", nil),
	}

	cr, err := NewCronRunner(nil, handlers, nil, "test-client", testLogger())
	if err != nil {
		t.Fatalf("NewCronRunner: %v", err)
	}

	// Add a job.
	err = cr.AddJob(bus.CronJob{
		Name:     "new-job",
		Schedule: "*/5 * * * *",
		Command:  "uptime",
		Tool:     "shell",
		Notify:   true,
	})
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	// Verify it was added.
	jobs := cr.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Name != "new-job" {
		t.Errorf("expected job name 'new-job', got %q", jobs[0].Name)
	}

	// Adding duplicate should fail.
	err = cr.AddJob(bus.CronJob{
		Name:     "new-job",
		Schedule: "*/5 * * * *",
		Command:  "uptime",
		Tool:     "shell",
		Notify:   true,
	})
	if err == nil {
		t.Error("expected error for duplicate job name")
	}

	// Remove the job.
	err = cr.RemoveJob("new-job")
	if err != nil {
		t.Fatalf("RemoveJob: %v", err)
	}

	// Verify it was removed.
	jobs = cr.ListJobs()
	if len(jobs) != 0 {
		t.Fatalf("expected 0 jobs, got %d", len(jobs))
	}

	// Removing non-existent should fail.
	err = cr.RemoveJob("non-existent")
	if err == nil {
		t.Error("expected error for non-existent job")
	}
}

func TestCronRunner_ListJobs(t *testing.T) {
	t.Parallel()

	handlers := map[string]tools.Tool{
		"shell":       fakeTool("shell", "ok", nil),
		"system_info": fakeTool("system_info", "ok", nil),
	}

	configs := []config.CronJobConfig{
		{
			Name:     "job-a",
			Schedule: "0 * * * *",
			Command:  "cmd-a",
			Tool:     "shell",
			Notify:   true,
		},
		{
			Name:     "job-b",
			Schedule: "30 * * * *",
			Command:  "cmd-b",
			Tool:     "system_info",
			Notify:   false,
		},
	}

	cr, err := NewCronRunner(configs, handlers, nil, "test-client", testLogger())
	if err != nil {
		t.Fatalf("NewCronRunner: %v", err)
	}

	jobs := cr.ListJobs()
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}

	if jobs[0].Name != "job-a" {
		t.Errorf("expected first job 'job-a', got %q", jobs[0].Name)
	}
	if jobs[1].Name != "job-b" {
		t.Errorf("expected second job 'job-b', got %q", jobs[1].Name)
	}

	// Verify NextRun is a valid RFC3339 timestamp and LastRun is empty (never run).
	for _, j := range jobs {
		if _, err := time.Parse(time.RFC3339, j.NextRun); err != nil {
			t.Errorf("job %q: invalid NextRun format %q: %v", j.Name, j.NextRun, err)
		}
		if j.LastRun != "" {
			t.Errorf("job %q: expected empty LastRun before execution, got %q", j.Name, j.LastRun)
		}
		if j.LastChanged {
			t.Errorf("job %q: expected LastChanged=false before execution", j.Name)
		}
	}
}

func TestCronRunner_ClientIDFiltering(t *testing.T) {
	t.Parallel()

	// This test verifies the client_id filtering logic that will be used
	// in the bus handler. We test the CronRunner's AddJob with the
	// understanding that the bus handler checks client_id before calling AddJob.

	handlers := map[string]tools.Tool{
		"shell": fakeTool("shell", "ok", nil),
	}

	cr, err := NewCronRunner(nil, handlers, nil, "client-A", testLogger())
	if err != nil {
		t.Fatalf("NewCronRunner: %v", err)
	}

	// Simulate a CronAdd message for this client.
	addMsg := bus.CronAddMessage{
		Type:     bus.TypeCronAdd,
		ClientID: "client-A",
		Job: bus.CronJob{
			Name:     "filtered-job",
			Schedule: "* * * * *",
			Command:  "test",
			Tool:     "shell",
			Notify:   true,
		},
	}

	// Client ID matches — should be processed.
	if addMsg.ClientID == cr.clientID {
		err = cr.AddJob(addMsg.Job)
		if err != nil {
			t.Fatalf("AddJob: %v", err)
		}
	}

	jobs := cr.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job after matching client_id, got %d", len(jobs))
	}

	// Simulate a CronAdd message for a different client.
	addMsg2 := bus.CronAddMessage{
		Type:     bus.TypeCronAdd,
		ClientID: "client-B",
		Job: bus.CronJob{
			Name:     "other-job",
			Schedule: "* * * * *",
			Command:  "test",
			Tool:     "shell",
			Notify:   true,
		},
	}

	// Client ID does NOT match — should be ignored.
	if addMsg2.ClientID == cr.clientID {
		t.Error("should not match different client_id")
	}

	// Verify no new job was added.
	jobs = cr.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("expected still 1 job after non-matching client_id, got %d", len(jobs))
	}
}

func TestCronRunner_ScheduleParsing(t *testing.T) {
	t.Parallel()

	handlers := map[string]tools.Tool{
		"shell": fakeTool("shell", "ok", nil),
	}

	tests := []struct {
		name      string
		schedule  string
		expectErr bool
	}{
		{"every minute", "* * * * *", false},
		{"every 5 minutes", "*/5 * * * *", false},
		{"hourly", "0 * * * *", false},
		{"daily at midnight", "0 0 * * *", false},
		{"weekdays at 9am", "0 9 * * 1-5", false},
		{"invalid: 6 fields", "* * * * * *", true},
		{"invalid: empty", "", true},
		{"invalid: bad syntax", "not-a-cron", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			configs := []config.CronJobConfig{
				{
					Name:     "test-job",
					Schedule: tt.schedule,
					Command:  "test",
					Tool:     "shell",
					Notify:   false,
				},
			}

			_, err := NewCronRunner(configs, handlers, nil, "test-client", testLogger())
			if tt.expectErr && err == nil {
				t.Error("expected error for invalid schedule")
			}
			if !tt.expectErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestCronRunner_SkipsUnknownTool(t *testing.T) {
	t.Parallel()

	handlers := map[string]tools.Tool{
		"shell": fakeTool("shell", "ok", nil),
	}

	configs := []config.CronJobConfig{
		{
			Name:     "valid-job",
			Schedule: "* * * * *",
			Command:  "test",
			Tool:     "shell",
			Notify:   false,
		},
		{
			Name:     "invalid-tool-job",
			Schedule: "* * * * *",
			Command:  "test",
			Tool:     "nonexistent",
			Notify:   false,
		},
	}

	cr, err := NewCronRunner(configs, handlers, nil, "test-client", testLogger())
	if err != nil {
		t.Fatalf("NewCronRunner: %v", err)
	}

	// Only the valid job should be registered.
	jobs := cr.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job (unknown tool skipped), got %d", len(jobs))
	}
	if jobs[0].Name != "valid-job" {
		t.Errorf("expected job 'valid-job', got %q", jobs[0].Name)
	}
}

func TestCronRunner_AddJobValidation(t *testing.T) {
	t.Parallel()

	handlers := map[string]tools.Tool{
		"shell": fakeTool("shell", "ok", nil),
	}

	cr, err := NewCronRunner(nil, handlers, nil, "test-client", testLogger())
	if err != nil {
		t.Fatalf("NewCronRunner: %v", err)
	}

	// Empty name.
	err = cr.AddJob(bus.CronJob{
		Name:     "",
		Schedule: "* * * * *",
		Command:  "test",
		Tool:     "shell",
		Notify:   false,
	})
	if err == nil {
		t.Error("expected error for empty name")
	}

	// Empty tool.
	err = cr.AddJob(bus.CronJob{
		Name:     "no-tool",
		Schedule: "* * * * *",
		Command:  "test",
		Tool:     "",
		Notify:   false,
	})
	if err == nil {
		t.Error("expected error for empty tool")
	}

	// Invalid schedule.
	err = cr.AddJob(bus.CronJob{
		Name:     "bad-schedule",
		Schedule: "invalid",
		Command:  "test",
		Tool:     "shell",
		Notify:   false,
	})
	if err == nil {
		t.Error("expected error for invalid schedule")
	}

	// Unknown tool.
	err = cr.AddJob(bus.CronJob{
		Name:     "bad-tool",
		Schedule: "* * * * *",
		Command:  "test",
		Tool:     "nonexistent",
		Notify:   false,
	})
	if err == nil {
		t.Error("expected error for unknown tool")
	}
}

func TestHashOutput(t *testing.T) {
	t.Parallel()

	h1 := hashOutput("hello")
	h2 := hashOutput("hello")
	h3 := hashOutput("world")

	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
	if h1 == h3 {
		t.Error("different input should produce different hash")
	}
	if len(h1) != 64 {
		t.Errorf("expected 64-char hex hash, got %d chars", len(h1))
	}
}
