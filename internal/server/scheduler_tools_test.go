package server

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"murmur/internal/db"
)

func TestParseReminderTime_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantErr   bool
		checkTime func(t *testing.T, got time.Time)
	}{
		{
			name:  "+2h returns ~2 hours from now",
			input: "+2h",
			checkTime: func(t *testing.T, got time.Time) {
				t.Helper()
				expected := time.Now().UTC().Add(2 * time.Hour)
				diff := got.Sub(expected)
				if diff < -5*time.Second || diff > 5*time.Second {
					t.Errorf("expected ~%v, got %v (diff %v)", expected, got, diff)
				}
			},
		},
		{
			name:  "+30m returns ~30 minutes from now",
			input: "+30m",
			checkTime: func(t *testing.T, got time.Time) {
				t.Helper()
				expected := time.Now().UTC().Add(30 * time.Minute)
				diff := got.Sub(expected)
				if diff < -5*time.Second || diff > 5*time.Second {
					t.Errorf("expected ~%v, got %v (diff %v)", expected, got, diff)
				}
			},
		},
		{
			name:  "+1d returns ~1 day from now",
			input: "+1d",
			checkTime: func(t *testing.T, got time.Time) {
				t.Helper()
				expected := time.Now().UTC().AddDate(0, 0, 1)
				diff := got.Sub(expected)
				if diff < -5*time.Second || diff > 5*time.Second {
					t.Errorf("expected ~%v, got %v (diff %v)", expected, got, diff)
				}
			},
		},
		{
			name:  "ISO 8601 with timezone",
			input: "2026-02-22T15:00:00Z",
			checkTime: func(t *testing.T, got time.Time) {
				t.Helper()
				want := time.Date(2026, 2, 22, 15, 0, 0, 0, time.UTC)
				if !got.Equal(want) {
					t.Errorf("got %v, want %v", got, want)
				}
			},
		},
		{
			name:  "ISO 8601 without timezone assumes UTC",
			input: "2026-02-22T15:00:00",
			checkTime: func(t *testing.T, got time.Time) {
				t.Helper()
				want := time.Date(2026, 2, 22, 15, 0, 0, 0, time.UTC)
				if !got.Equal(want) {
					t.Errorf("got %v, want %v", got, want)
				}
			},
		},
		{
			name:  "date-only returns midnight UTC",
			input: "2026-02-22",
			checkTime: func(t *testing.T, got time.Time) {
				t.Helper()
				want := time.Date(2026, 2, 22, 0, 0, 0, 0, time.UTC)
				if !got.Equal(want) {
					t.Errorf("got %v, want %v", got, want)
				}
			},
		},
		{
			name:    "+0h rejected (duration must be positive)",
			input:   "+0h",
			wantErr: true,
		},
		{
			name:    "invalid format returns error",
			input:   "invalid",
			wantErr: true,
		},
		{
			name:    "empty string returns error",
			input:   "",
			wantErr: true,
		},
		{
			name:  "whitespace trimmed around +2h",
			input: " +2h ",
			checkTime: func(t *testing.T, got time.Time) {
				t.Helper()
				expected := time.Now().UTC().Add(2 * time.Hour)
				diff := got.Sub(expected)
				if diff < -5*time.Second || diff > 5*time.Second {
					t.Errorf("expected ~%v, got %v (diff %v)", expected, got, diff)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseReminderTime(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for input %q, got time %v", tt.input, got)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tt.input, err)
			}

			if tt.checkTime != nil {
				tt.checkTime(t, got)
			}
		})
	}
}

func TestRegisterSchedulerTools_NilScheduler(t *testing.T) {
	t.Parallel()

	registry := NewToolRegistry()
	err := RegisterSchedulerTools(registry, nil, "#test")
	if err != nil {
		t.Fatalf("expected nil error for nil scheduler, got: %v", err)
	}

	// No tools should be registered.
	names := registry.Names()
	if len(names) != 0 {
		t.Errorf("expected 0 tools registered, got %d: %v", len(names), names)
	}
}

func TestRegisterSchedulerTools_RegistersSixTools(t *testing.T) {
	t.Parallel()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) error: %v", err)
	}
	defer database.Close()

	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate error: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scheduler := NewScheduler(database, nil, 1*time.Minute, 2, logger)

	registry := NewToolRegistry()
	err = RegisterSchedulerTools(registry, scheduler, "#murmur")
	if err != nil {
		t.Fatalf("RegisterSchedulerTools error: %v", err)
	}

	expectedTools := []string{
		"task_add",
		"reminder_add",
		"task_list",
		"task_remove",
		"task_enable",
		"task_disable",
	}

	names := registry.Names()
	if len(names) != len(expectedTools) {
		t.Fatalf("expected %d tools registered, got %d: %v", len(expectedTools), len(names), names)
	}

	for _, name := range expectedTools {
		if !registry.HasTool(name) {
			t.Errorf("expected tool %q to be registered", name)
		}
	}
}
