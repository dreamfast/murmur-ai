package tools

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"murmur/internal/config"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestBuildTools_NilConfig(t *testing.T) {
	t.Parallel()

	tools, err := BuildTools(BuildToolsOpts{
		Config: nil,
		Logger: newTestLogger(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("BuildTools(nil config) returned %d tools, want 0", len(tools))
	}
}

func TestBuildTools_EmptyConfig(t *testing.T) {
	t.Parallel()

	cfg := &config.ToolsConfig{}
	tools, err := BuildTools(BuildToolsOpts{
		Config: cfg,
		Logger: newTestLogger(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("BuildTools(empty) returned %d tools, want 0", len(tools))
	}
}

func TestBuildTools_SystemInfoOnly(t *testing.T) {
	t.Parallel()

	cfg := &config.ToolsConfig{
		SystemInfo: &config.SystemInfoConfig{Enabled: true},
	}
	tools, err := BuildTools(BuildToolsOpts{
		Config: cfg,
		Logger: newTestLogger(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("BuildTools returned %d tools, want 1", len(tools))
	}
	if tools[0].Name != "system_info" {
		t.Errorf("tools[0].Name = %q, want %q", tools[0].Name, "system_info")
	}
}

func TestBuildTools_DisabledToolsSkipped(t *testing.T) {
	t.Parallel()

	cfg := &config.ToolsConfig{
		SystemInfo: &config.SystemInfoConfig{Enabled: false},
		Shell:      &config.ShellConfig{Enabled: false},
	}
	tools, err := BuildTools(BuildToolsOpts{
		Config: cfg,
		Logger: newTestLogger(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("BuildTools with disabled tools returned %d tools, want 0", len(tools))
	}
}

func TestBuildTools_AllEnabled(t *testing.T) {
	t.Parallel()

	cfg := &config.ToolsConfig{
		SystemInfo: &config.SystemInfoConfig{Enabled: true},
		Shell:      &config.ShellConfig{Enabled: true, DockerImage: "ubuntu:24.04"},
		CodeExec:   &config.CodeExecConfig{Enabled: true, PistonURL: "http://localhost:2000"},
	}
	tools, err := BuildTools(BuildToolsOpts{
		Config: cfg,
		Logger: newTestLogger(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("BuildTools returned %d tools, want 3", len(tools))
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"system_info", "shell", "code_exec"} {
		if !names[want] {
			t.Errorf("missing tool %q in result", want)
		}
	}
}

func TestBuildTools_OptsStruct(t *testing.T) {
	t.Parallel()

	// Verify that BuildToolsOpts works with all optional fields nil.
	cfg := &config.ToolsConfig{
		SystemInfo: &config.SystemInfoConfig{Enabled: true},
	}
	tools, err := BuildTools(BuildToolsOpts{
		Config:     cfg,
		Logger:     newTestLogger(),
		Resolver:   nil,
		IRCManager: nil,
		Memory:     nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("BuildTools returned %d tools, want 1", len(tools))
	}
}

func TestToBusToolDefs(t *testing.T) {
	t.Parallel()

	tools := []Tool{
		{
			Name:        "test_tool",
			Description: "A test tool",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		},
		{
			Name:        "another_tool",
			Description: "Another test tool",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
	}

	defs := ToBusToolDefs(tools)
	if len(defs) != 2 {
		t.Fatalf("ToBusToolDefs returned %d defs, want 2", len(defs))
	}
	if defs[0].Name != "test_tool" {
		t.Errorf("defs[0].Name = %q, want %q", defs[0].Name, "test_tool")
	}
	if defs[0].Description != "A test tool" {
		t.Errorf("defs[0].Description = %q, want %q", defs[0].Description, "A test tool")
	}
	if string(defs[1].Parameters) != `{"type":"object","properties":{}}` {
		t.Errorf("defs[1].Parameters = %s", string(defs[1].Parameters))
	}
}

func TestToBusToolDefs_Empty(t *testing.T) {
	t.Parallel()

	defs := ToBusToolDefs(nil)
	if len(defs) != 0 {
		t.Errorf("ToBusToolDefs(nil) returned %d defs, want 0", len(defs))
	}
}
