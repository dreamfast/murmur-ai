package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"murmur/internal/db"
)

func newTestNotesStore(t *testing.T) *NotesStore {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewNotesStore(database, logger)
}

func TestNotesStore_SetAndGet(t *testing.T) {
	t.Parallel()
	store := newTestNotesStore(t)

	if err := store.Set("greeting", "hello world"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	value, err := store.Get("greeting")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if value != "hello world" {
		t.Errorf("Get = %q, want %q", value, "hello world")
	}
}

func TestNotesStore_GetNotFound(t *testing.T) {
	t.Parallel()
	store := newTestNotesStore(t)

	_, err := store.Get("nonexistent")
	if !errors.Is(err, ErrNoteNotFound) {
		t.Errorf("Get nonexistent: got %v, want ErrNoteNotFound", err)
	}
}

func TestNotesStore_Update(t *testing.T) {
	t.Parallel()
	store := newTestNotesStore(t)

	if err := store.Set("key1", "value1"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Update the same key.
	if err := store.Set("key1", "value2"); err != nil {
		t.Fatalf("Set update: %v", err)
	}

	value, err := store.Get("key1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if value != "value2" {
		t.Errorf("Get after update = %q, want %q", value, "value2")
	}

	// Verify only one entry exists.
	entries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("List count = %d, want 1", len(entries))
	}
}

func TestNotesStore_List(t *testing.T) {
	t.Parallel()
	store := newTestNotesStore(t)

	// Empty list.
	entries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("List empty = %d entries, want 0", len(entries))
	}

	// Add some notes.
	for _, kv := range []struct{ k, v string }{
		{"beta", "second"},
		{"alpha", "first"},
		{"gamma", "third"},
	} {
		if err := store.Set(kv.k, kv.v); err != nil {
			t.Fatalf("Set %q: %v", kv.k, err)
		}
	}

	entries, err = store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("List count = %d, want 3", len(entries))
	}

	// Verify alphabetical order.
	if entries[0].Key != "alpha" || entries[1].Key != "beta" || entries[2].Key != "gamma" {
		t.Errorf("List order: %s, %s, %s", entries[0].Key, entries[1].Key, entries[2].Key)
	}
}

func TestNotesStore_Delete(t *testing.T) {
	t.Parallel()
	store := newTestNotesStore(t)

	if err := store.Set("to-delete", "value"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := store.Delete("to-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := store.Get("to-delete")
	if !errors.Is(err, ErrNoteNotFound) {
		t.Errorf("Get after delete: got %v, want ErrNoteNotFound", err)
	}

	// Deleting a nonexistent key should not error.
	if err := store.Delete("nonexistent"); err != nil {
		t.Errorf("Delete nonexistent: %v", err)
	}
}

func TestNotesStore_Search(t *testing.T) {
	t.Parallel()
	store := newTestNotesStore(t)

	for _, kv := range []struct{ k, v string }{
		{"server-ip", "192.168.1.1"},
		{"database-host", "db.example.com"},
		{"api-key", "secret123"},
	} {
		if err := store.Set(kv.k, kv.v); err != nil {
			t.Fatalf("Set %q: %v", kv.k, err)
		}
	}

	// Search by key.
	entries, err := store.Search("server")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("Search 'server' count = %d, want 1", len(entries))
	}
	if entries[0].Key != "server-ip" {
		t.Errorf("Search result key = %q, want %q", entries[0].Key, "server-ip")
	}

	// Search by value.
	entries, err = store.Search("example.com")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("Search 'example.com' count = %d, want 1", len(entries))
	}

	// Search with no results.
	entries, err = store.Search("nonexistent")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Search 'nonexistent' count = %d, want 0", len(entries))
	}
}

func TestNotesCommand_List(t *testing.T) {
	t.Parallel()

	env := newTestCommandEnv(t, nil)
	store := newTestNotesStore(t)
	env.handler.notes = store

	if err := store.Set("key1", "value1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Set("key2", "value2"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	env.handler.HandleCommand("#test", "admin", "!notes")

	msg := env.lastSent()
	if !strings.Contains(msg, "key1") || !strings.Contains(msg, "key2") {
		t.Errorf("expected both keys in output, got %q", msg)
	}
}

func TestNotesCommand_GetSet(t *testing.T) {
	t.Parallel()

	env := newTestCommandEnv(t, nil)
	store := newTestNotesStore(t)
	env.handler.notes = store

	// Set a note.
	env.handler.HandleCommand("#test", "admin", "!notes set mykey hello world")
	msg := env.lastSent()
	if !strings.Contains(msg, "saved") {
		t.Errorf("expected 'saved' in response, got %q", msg)
	}

	// Get the note.
	env.handler.HandleCommand("#test", "admin", "!notes get mykey")
	msg = env.lastSent()
	if !strings.Contains(msg, "hello world") {
		t.Errorf("expected 'hello world' in response, got %q", msg)
	}

	// Delete the note.
	env.handler.HandleCommand("#test", "admin", "!notes delete mykey")
	msg = env.lastSent()
	if !strings.Contains(msg, "deleted") {
		t.Errorf("expected 'deleted' in response, got %q", msg)
	}

	// Get should now return not found.
	env.handler.HandleCommand("#test", "admin", "!notes get mykey")
	msg = env.lastSent()
	if !strings.Contains(msg, "not found") {
		t.Errorf("expected 'not found' in response, got %q", msg)
	}
}

func TestNotesStore_EmptyKey(t *testing.T) {
	t.Parallel()
	store := newTestNotesStore(t)

	err := store.Set("", "value")
	if err == nil {
		t.Error("expected error for empty key")
	}

	err = store.Set("   ", "value")
	if err == nil {
		t.Error("expected error for whitespace-only key")
	}
}

func TestNotesCommand_Search(t *testing.T) {
	t.Parallel()

	env := newTestCommandEnv(t, nil)
	store := newTestNotesStore(t)
	env.handler.notes = store

	if err := store.Set("server-ip", "192.168.1.1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Set("database-host", "db.example.com"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	env.handler.HandleCommand("#test", "admin", "!notes search server")
	msg := env.lastSent()
	if !strings.Contains(msg, "server-ip") {
		t.Errorf("expected 'server-ip' in search results, got %q", msg)
	}
	if strings.Contains(msg, "database-host") {
		t.Errorf("unexpected 'database-host' in search results for 'server', got %q", msg)
	}
}

func TestRegisterNoteTools(t *testing.T) {
	t.Parallel()

	store := newTestNotesStore(t)
	registry := NewToolRegistry()

	if err := RegisterNoteTools(registry, store); err != nil {
		t.Fatalf("RegisterNoteTools: %v", err)
	}

	names := registry.Names()
	expected := []string{"note_delete", "note_get", "note_list", "note_search", "note_set"}
	if len(names) != len(expected) {
		t.Fatalf("registered %d tools, want %d", len(names), len(expected))
	}
	for i, name := range names {
		if name != expected[i] {
			t.Errorf("tool[%d] = %q, want %q", i, name, expected[i])
		}
	}
}

func TestNoteTools_SetAndGet(t *testing.T) {
	t.Parallel()

	store := newTestNotesStore(t)
	registry := NewToolRegistry()
	if err := RegisterNoteTools(registry, store); err != nil {
		t.Fatalf("RegisterNoteTools: %v", err)
	}

	// Set via tool.
	setTool, ok := registry.Get("note_set")
	if !ok {
		t.Fatal("note_set tool not found")
	}
	result, err := setTool.Handler(context.Background(), map[string]any{
		"key":   "test-key",
		"value": "test-value",
	})
	if err != nil {
		t.Fatalf("note_set: %v", err)
	}
	if !strings.Contains(result, "saved") {
		t.Errorf("note_set result = %q, expected 'saved'", result)
	}

	// Get via tool.
	getTool, ok := registry.Get("note_get")
	if !ok {
		t.Fatal("note_get tool not found")
	}
	result, err = getTool.Handler(context.Background(), map[string]any{
		"key": "test-key",
	})
	if err != nil {
		t.Fatalf("note_get: %v", err)
	}
	if !strings.Contains(result, "test-value") {
		t.Errorf("note_get result = %q, expected 'test-value'", result)
	}
}
