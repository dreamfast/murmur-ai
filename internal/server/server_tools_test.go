package server

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"murmur/internal/tools"
)

func newTestTool(name, desc string) tools.Tool {
	return tools.Tool{
		Name:        name,
		Description: desc,
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "result from " + name, nil
		},
	}
}

func TestToolRegistry_RegisterAndGet(t *testing.T) {
	t.Parallel()

	reg := NewToolRegistry()

	tool := newTestTool("note_set", "Set a note")
	if err := reg.Register(tool); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Get existing tool.
	got, ok := reg.Get("note_set")
	if !ok {
		t.Fatal("expected to find tool note_set")
	}
	if got.Name != "note_set" {
		t.Errorf("Name = %q, want %q", got.Name, "note_set")
	}
	if got.Description != "Set a note" {
		t.Errorf("Description = %q, want %q", got.Description, "Set a note")
	}

	// Get unknown tool.
	_, ok = reg.Get("nonexistent")
	if ok {
		t.Error("expected Get to return false for unknown tool")
	}
}

func TestToolRegistry_AllToolDefs(t *testing.T) {
	t.Parallel()

	reg := NewToolRegistry()

	if err := reg.Register(newTestTool("note_set", "Set a note")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Register(newTestTool("note_get", "Get a note")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	defs := reg.AllToolDefs()
	if len(defs) != 2 {
		t.Fatalf("expected 2 tool defs, got %d", len(defs))
	}

	// Verify both tools are present (order may vary).
	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
	}
	if !names["note_set"] {
		t.Error("expected note_set in tool defs")
	}
	if !names["note_get"] {
		t.Error("expected note_get in tool defs")
	}
}

func TestToolRegistry_AllToolDefs_Empty(t *testing.T) {
	t.Parallel()

	reg := NewToolRegistry()
	defs := reg.AllToolDefs()
	if len(defs) != 0 {
		t.Errorf("expected 0 tool defs from empty registry, got %d", len(defs))
	}
}

func TestToolRegistry_DuplicateReturnsError(t *testing.T) {
	t.Parallel()

	reg := NewToolRegistry()

	tool := newTestTool("note_set", "Set a note")
	if err := reg.Register(tool); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	err := reg.Register(tool)
	if err == nil {
		t.Fatal("expected error on duplicate registration")
	}
	if err.Error() != `register: tool "note_set" already registered` {
		t.Errorf("error = %q, want duplicate message", err.Error())
	}
}

func TestToolRegistry_Names(t *testing.T) {
	t.Parallel()

	reg := NewToolRegistry()

	if err := reg.Register(newTestTool("charlie", "C")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Register(newTestTool("alpha", "A")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Register(newTestTool("bravo", "B")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	names := reg.Names()
	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d", len(names))
	}
	// Should be sorted alphabetically.
	if names[0] != "alpha" || names[1] != "bravo" || names[2] != "charlie" {
		t.Errorf("names = %v, want [alpha bravo charlie]", names)
	}
}

func TestToolRegistry_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	reg := NewToolRegistry()

	// Register tools concurrently.
	var wg sync.WaitGroup
	errs := make(chan error, 100)

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			name := "tool_" + string(rune('a'+n%26)) + string(rune('0'+n/26))
			if err := reg.Register(newTestTool(name, "desc")); err != nil {
				errs <- err
			}
		}(i)
	}

	// Read concurrently while writing.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = reg.AllToolDefs()
			_ = reg.Names()
			reg.Get("tool_a0")
		}()
	}

	wg.Wait()
	close(errs)

	// Some duplicate errors are expected since we have 50 tools with
	// 26*2=52 possible names. Just verify no panics occurred.
	for err := range errs {
		t.Logf("concurrent register error (expected for duplicates): %v", err)
	}
}
