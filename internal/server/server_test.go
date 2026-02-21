package server

import (
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"murmur/internal/bus"
	"murmur/internal/config"
	"murmur/internal/tools"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestRegistry_Register(t *testing.T) {
	t.Parallel()

	r := NewRegistry(2*time.Minute, newTestLogger())

	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "laptop",
		Hostname: "thinkpad",
		Tools: []bus.ToolDef{
			{Name: "shell", Description: "Run commands", Parameters: json.RawMessage(`{}`)},
		},
	})

	clients := r.GetOnlineClients()
	if len(clients) != 1 {
		t.Fatalf("expected 1 online client, got %d", len(clients))
	}
	if clients[0].ClientID != "laptop" {
		t.Errorf("ClientID = %q, want %q", clients[0].ClientID, "laptop")
	}
	if clients[0].Hostname != "thinkpad" {
		t.Errorf("Hostname = %q, want %q", clients[0].Hostname, "thinkpad")
	}
	if len(clients[0].Tools) != 1 {
		t.Errorf("len(Tools) = %d, want 1", len(clients[0].Tools))
	}
	if clients[0].Status != "online" {
		t.Errorf("Status = %q, want %q", clients[0].Status, "online")
	}
}

func TestRegistry_Deregister(t *testing.T) {
	t.Parallel()

	r := NewRegistry(2*time.Minute, newTestLogger())

	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "laptop",
		Hostname: "thinkpad",
	})

	r.Deregister("laptop")

	clients := r.GetOnlineClients()
	if len(clients) != 0 {
		t.Fatalf("expected 0 online clients after deregister, got %d", len(clients))
	}

	// Client should still exist but be offline.
	info, ok := r.GetClient("laptop")
	if !ok {
		t.Fatal("expected client to still exist after deregister")
	}
	if info.Status != "offline" {
		t.Errorf("Status = %q, want %q", info.Status, "offline")
	}
}

func TestRegistry_Heartbeat(t *testing.T) {
	t.Parallel()

	r := NewRegistry(2*time.Minute, newTestLogger())

	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "laptop",
		Hostname: "thinkpad",
	})

	// Wait a tiny bit so heartbeat time is different from registration.
	time.Sleep(10 * time.Millisecond)

	r.Heartbeat(&bus.HeartbeatMessage{
		Type:     bus.TypeHeartbeat,
		ClientID: "laptop",
		Uptime:   100,
		Load:     bus.LoadInfo{CPU: 25.0, Memory: 50.0},
	})

	info, ok := r.GetClient("laptop")
	if !ok {
		t.Fatal("expected client to exist")
	}
	if info.Load.CPU != 25.0 {
		t.Errorf("Load.CPU = %f, want 25.0", info.Load.CPU)
	}
	if info.Load.Memory != 50.0 {
		t.Errorf("Load.Memory = %f, want 50.0", info.Load.Memory)
	}
}

func TestRegistry_HeartbeatTimeout(t *testing.T) {
	t.Parallel()

	// Use a very short timeout for testing.
	r := NewRegistry(50*time.Millisecond, newTestLogger())

	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "laptop",
		Hostname: "thinkpad",
	})

	// Wait for the timeout to expire.
	time.Sleep(100 * time.Millisecond)

	r.checkTimeouts()

	info, ok := r.GetClient("laptop")
	if !ok {
		t.Fatal("expected client to exist")
	}
	if info.Status != "offline" {
		t.Errorf("Status = %q, want %q after timeout", info.Status, "offline")
	}
}

func TestRegistry_HeartbeatRevivesOffline(t *testing.T) {
	t.Parallel()

	r := NewRegistry(50*time.Millisecond, newTestLogger())

	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "laptop",
		Hostname: "thinkpad",
	})

	// Force offline.
	r.Deregister("laptop")

	// Heartbeat should bring it back online.
	r.Heartbeat(&bus.HeartbeatMessage{
		Type:     bus.TypeHeartbeat,
		ClientID: "laptop",
		Uptime:   100,
	})

	info, _ := r.GetClient("laptop")
	if info.Status != "online" {
		t.Errorf("Status = %q, want %q after heartbeat", info.Status, "online")
	}
}

func TestRegistry_GetToolProvider(t *testing.T) {
	t.Parallel()

	r := NewRegistry(2*time.Minute, newTestLogger())

	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "vps1",
		Hostname: "nyc",
		Tools: []bus.ToolDef{
			{Name: "shell", Description: "Run commands"},
			{Name: "apt_check", Description: "Check updates"},
		},
	})

	provider, ok := r.GetToolProvider("shell")
	if !ok {
		t.Fatal("expected to find provider for 'shell'")
	}
	if provider.ClientID != "vps1" {
		t.Errorf("provider ClientID = %q, want %q", provider.ClientID, "vps1")
	}

	_, ok = r.GetToolProvider("nonexistent")
	if ok {
		t.Error("expected no provider for 'nonexistent'")
	}
}

func TestRegistry_GetToolProvider_MultipleClients(t *testing.T) {
	t.Parallel()

	r := NewRegistry(2*time.Minute, newTestLogger())

	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "vps1",
		Hostname: "nyc",
		Tools:    []bus.ToolDef{{Name: "shell", Description: "Run commands"}},
	})

	// Small delay so vps2 has a more recent heartbeat.
	time.Sleep(10 * time.Millisecond)

	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "vps2",
		Hostname: "lon",
		Tools:    []bus.ToolDef{{Name: "shell", Description: "Run commands"}},
	})

	provider, ok := r.GetToolProvider("shell")
	if !ok {
		t.Fatal("expected to find provider for 'shell'")
	}
	// vps2 should be preferred (more recent heartbeat).
	if provider.ClientID != "vps2" {
		t.Errorf("provider ClientID = %q, want %q (most recent heartbeat)", provider.ClientID, "vps2")
	}
}

func TestRegistry_GetToolProvider_OfflineSkipped(t *testing.T) {
	t.Parallel()

	r := NewRegistry(2*time.Minute, newTestLogger())

	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "vps1",
		Hostname: "nyc",
		Tools:    []bus.ToolDef{{Name: "shell", Description: "Run commands"}},
	})

	r.Deregister("vps1")

	_, ok := r.GetToolProvider("shell")
	if ok {
		t.Error("expected no provider when client is offline")
	}
}

func TestRegistry_AllTools(t *testing.T) {
	t.Parallel()

	r := NewRegistry(2*time.Minute, newTestLogger())

	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "vps1",
		Hostname: "nyc",
		Tools: []bus.ToolDef{
			{Name: "shell", Description: "Run commands"},
			{Name: "apt_check", Description: "Check updates"},
		},
	})

	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "laptop",
		Hostname: "thinkpad",
		Tools: []bus.ToolDef{
			{Name: "mail_read", Description: "Read email"},
		},
	})

	tools := r.AllTools()
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}

	names := make(map[string]bool)
	for _, t := range tools {
		names[t.Name] = true
	}
	for _, expected := range []string{"shell", "apt_check", "mail_read"} {
		if !names[expected] {
			t.Errorf("missing tool %q in AllTools result", expected)
		}
	}
}

func TestRegistry_AllTools_DeduplicatesSameTool(t *testing.T) {
	t.Parallel()

	r := NewRegistry(2*time.Minute, newTestLogger())

	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "vps1",
		Hostname: "nyc",
		Tools:    []bus.ToolDef{{Name: "shell", Description: "Run commands v1"}},
	})

	time.Sleep(10 * time.Millisecond)

	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "vps2",
		Hostname: "lon",
		Tools:    []bus.ToolDef{{Name: "shell", Description: "Run commands v2"}},
	})

	tools := r.AllTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 deduplicated tool, got %d", len(tools))
	}
	// Should prefer vps2's version (more recent heartbeat).
	if tools[0].Description != "Run commands v2" {
		t.Errorf("Description = %q, want %q (from most recent client)", tools[0].Description, "Run commands v2")
	}
}

func TestRegistry_ReRegister(t *testing.T) {
	t.Parallel()

	r := NewRegistry(2*time.Minute, newTestLogger())

	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "laptop",
		Hostname: "thinkpad",
		Tools:    []bus.ToolDef{{Name: "shell", Description: "Run commands"}},
	})

	// Re-register with different tools.
	r.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "laptop",
		Hostname: "thinkpad-v2",
		Tools: []bus.ToolDef{
			{Name: "mail_read", Description: "Read email"},
			{Name: "mail_send", Description: "Send email"},
		},
	})

	info, ok := r.GetClient("laptop")
	if !ok {
		t.Fatal("expected client to exist")
	}
	if info.Hostname != "thinkpad-v2" {
		t.Errorf("Hostname = %q, want %q", info.Hostname, "thinkpad-v2")
	}
	if len(info.Tools) != 2 {
		t.Errorf("len(Tools) = %d, want 2", len(info.Tools))
	}
}

func TestRegistry_GetClient_NotFound(t *testing.T) {
	t.Parallel()

	r := NewRegistry(2*time.Minute, newTestLogger())

	_, ok := r.GetClient("nonexistent")
	if ok {
		t.Error("expected GetClient to return false for nonexistent client")
	}
}

func TestRegistry_HeartbeatUnknownClient(t *testing.T) {
	t.Parallel()

	r := NewRegistry(2*time.Minute, newTestLogger())

	// Should not panic — just log a warning.
	r.Heartbeat(&bus.HeartbeatMessage{
		Type:     bus.TypeHeartbeat,
		ClientID: "unknown",
		Uptime:   100,
	})

	_, ok := r.GetClient("unknown")
	if ok {
		t.Error("expected unknown client to not be created by heartbeat")
	}
}

// TestBuildToolsIntoRegistry verifies that tools built from the unified
// ToolsConfig can be registered into the server's ToolRegistry. This tests
// the integration pattern used in server.New.
func TestBuildToolsIntoRegistry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       *config.ToolsConfig
		wantNames []string
	}{
		{
			name:      "empty config",
			cfg:       &config.ToolsConfig{},
			wantNames: nil,
		},
		{
			name: "system_info and dns",
			cfg: &config.ToolsConfig{
				SystemInfo: &config.SystemInfoConfig{Enabled: true},
				DNS:        &config.DNSConfig{Enabled: true},
			},
			wantNames: []string{"system_info", "dns_check"},
		},
		{
			name: "code_exec and rss",
			cfg: &config.ToolsConfig{
				CodeExec: &config.CodeExecConfig{
					Enabled:   true,
					PistonURL: "http://localhost:2000",
				},
				RSS: &config.RSSConfig{Enabled: true},
			},
			wantNames: []string{"code_exec", "rss_read"},
		},
		{
			name: "disabled tools skipped",
			cfg: &config.ToolsConfig{
				SystemInfo: &config.SystemInfoConfig{Enabled: false},
				Shell:      &config.ShellConfig{Enabled: false},
			},
			wantNames: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger := newTestLogger()
			reg := NewToolRegistry()

			builtTools, err := tools.BuildTools(tools.BuildToolsOpts{
				Config: tt.cfg,
				Logger: logger,
			})
			if err != nil {
				t.Fatalf("BuildTools: %v", err)
			}

			for _, tool := range builtTools {
				if err := reg.Register(tool); err != nil {
					t.Fatalf("Register(%q): %v", tool.Name, err)
				}
			}

			names := reg.Names()
			if len(names) != len(tt.wantNames) {
				t.Fatalf("registered %d tools, want %d: got %v", len(names), len(tt.wantNames), names)
			}

			registered := make(map[string]bool)
			for _, n := range names {
				registered[n] = true
			}
			for _, want := range tt.wantNames {
				if !registered[want] {
					t.Errorf("missing tool %q in registry, got %v", want, names)
				}
			}
		})
	}
}

// TestBuildToolsIntoRegistry_NoDuplicateWithNotes verifies that tools built
// from config don't conflict with server-only note tools when both are
// registered in the same ToolRegistry.
func TestBuildToolsIntoRegistry_NoDuplicateWithNotes(t *testing.T) {
	t.Parallel()

	reg := NewToolRegistry()

	// Register a note tool manually (simulating RegisterNoteTools).
	noteTool := tools.Tool{
		Name:        "note_set",
		Description: "Set a note",
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
	}
	if err := reg.Register(noteTool); err != nil {
		t.Fatalf("Register note_set: %v", err)
	}

	// Build tools from config — these should not conflict with note tools.
	cfg := &config.ToolsConfig{
		SystemInfo: &config.SystemInfoConfig{Enabled: true},
		DNS:        &config.DNSConfig{Enabled: true},
	}
	builtTools, err := tools.BuildTools(tools.BuildToolsOpts{
		Config: cfg,
		Logger: newTestLogger(),
	})
	if err != nil {
		t.Fatalf("BuildTools: %v", err)
	}

	for _, tool := range builtTools {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("Register(%q): %v", tool.Name, err)
		}
	}

	// Should have note_set + system_info + dns_check = 3 tools.
	names := reg.Names()
	if len(names) != 3 {
		t.Fatalf("expected 3 tools, got %d: %v", len(names), names)
	}
}
