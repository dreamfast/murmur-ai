package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"murmur/internal/bus"
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRegistry(2*time.Minute, logger)
}

func TestRegistry_AutonomyStored(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t)

	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "client-1",
		Hostname: "host-1",
		Autonomy: "approve",
		Tools: []bus.ToolDef{
			{Name: "shell", Description: "Run commands", Parameters: json.RawMessage(`{}`)},
		},
	})

	autonomy := r.GetClientAutonomy("client-1")
	if autonomy != "approve" {
		t.Errorf("GetClientAutonomy = %q, want %q", autonomy, "approve")
	}

	// Verify it's also in the ClientInfo.
	info, ok := r.GetClient("client-1")
	if !ok {
		t.Fatal("expected client to be found")
	}
	if info.Autonomy != "approve" {
		t.Errorf("ClientInfo.Autonomy = %q, want %q", info.Autonomy, "approve")
	}
}

func TestRegistry_AutonomyDefaultAuto(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t)

	// Register without setting autonomy — should default to "auto".
	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "client-2",
		Hostname: "host-2",
		Tools: []bus.ToolDef{
			{Name: "shell", Description: "Run commands", Parameters: json.RawMessage(`{}`)},
		},
	})

	autonomy := r.GetClientAutonomy("client-2")
	if autonomy != "auto" {
		t.Errorf("GetClientAutonomy = %q, want %q", autonomy, "auto")
	}
}

func TestRegistry_AutonomyReport(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t)

	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "client-3",
		Hostname: "host-3",
		Autonomy: "report",
		Tools:    []bus.ToolDef{},
	})

	autonomy := r.GetClientAutonomy("client-3")
	if autonomy != "report" {
		t.Errorf("GetClientAutonomy = %q, want %q", autonomy, "report")
	}
}

func TestRegistry_AutonomyInvalidDefaultsToApprove(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t)

	// Invalid autonomy value should be coerced to "approve" (fail-closed).
	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "client-4",
		Hostname: "host-4",
		Autonomy: "INVALID",
		Tools:    []bus.ToolDef{},
	})

	autonomy := r.GetClientAutonomy("client-4")
	if autonomy != "approve" {
		t.Errorf("GetClientAutonomy = %q, want %q for invalid input", autonomy, "approve")
	}
}

func TestRegistry_AutonomyUnknownClient(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t)

	// Unknown client should return "auto" as safe default.
	autonomy := r.GetClientAutonomy("nonexistent")
	if autonomy != "auto" {
		t.Errorf("GetClientAutonomy = %q, want %q for unknown client", autonomy, "auto")
	}
}

func TestRegistry_AutonomyUpdatedOnReRegister(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(t)

	// Register with "auto".
	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "client-5",
		Hostname: "host-5",
		Autonomy: "auto",
		Tools:    []bus.ToolDef{},
	})

	if got := r.GetClientAutonomy("client-5"); got != "auto" {
		t.Fatalf("initial autonomy = %q, want %q", got, "auto")
	}

	// Re-register with "approve".
	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "client-5",
		Hostname: "host-5",
		Autonomy: "approve",
		Tools:    []bus.ToolDef{},
	})

	if got := r.GetClientAutonomy("client-5"); got != "approve" {
		t.Errorf("updated autonomy = %q, want %q", got, "approve")
	}
}
