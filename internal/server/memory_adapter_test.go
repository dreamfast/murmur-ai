package server

import (
	"io"
	"log/slog"
	"testing"

	"murmur/internal/db"
	"murmur/internal/tools"
)

// newAdapterTestMemory creates an in-memory database, migrates it, and returns
// a Memory instance suitable for adapter tests. The summary provider is nil
// (disabled).
func newAdapterTestMemory(t *testing.T) *Memory {
	t.Helper()

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewMemory(database, 100, 50, nil, logger)
}

func TestNewMemoryAdapter(t *testing.T) {
	t.Parallel()

	mem := newAdapterTestMemory(t)
	adapter := NewMemoryAdapter(mem)

	if adapter == nil {
		t.Fatal("NewMemoryAdapter returned nil")
	}
}

func TestMemoryAdapter_GetHistory_Order(t *testing.T) {
	t.Parallel()

	mem := newAdapterTestMemory(t)
	adapter := NewMemoryAdapter(mem)

	channel := "#test-order"
	messages := []struct {
		role    string
		content string
	}{
		{"user", "first message"},
		{"assistant", "second message"},
		{"user", "third message"},
	}

	for _, m := range messages {
		if err := mem.AddMessage(channel, m.role, m.content, "", ""); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}

	history, err := adapter.GetHistory(channel, 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}

	if len(history) != len(messages) {
		t.Fatalf("expected %d messages, got %d", len(messages), len(history))
	}

	for i, want := range messages {
		got := history[i]
		if got.Role != want.role {
			t.Errorf("message[%d].Role = %q, want %q", i, got.Role, want.role)
		}
		if got.Content != want.content {
			t.Errorf("message[%d].Content = %q, want %q", i, got.Content, want.content)
		}
	}
}

func TestMemoryAdapter_GetHistory_EmptyChannel(t *testing.T) {
	t.Parallel()

	mem := newAdapterTestMemory(t)
	adapter := NewMemoryAdapter(mem)

	history, err := adapter.GetHistory("#empty", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}

	if len(history) != 0 {
		t.Errorf("expected empty slice, got %d messages", len(history))
	}
}

func TestMemoryAdapter_GetHistory_ReturnsMemoryMessages(t *testing.T) {
	t.Parallel()

	mem := newAdapterTestMemory(t)
	adapter := NewMemoryAdapter(mem)

	channel := "#types"
	if err := mem.AddMessage(channel, "user", "hello", "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	history, err := adapter.GetHistory(channel, 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}

	if len(history) == 0 {
		t.Fatal("expected at least one message")
	}

	// Verify the returned type is tools.MemoryMessage.
	var _ []tools.MemoryMessage = history
}

func TestMemoryAdapter_GetHistoryCount(t *testing.T) {
	t.Parallel()

	mem := newAdapterTestMemory(t)
	adapter := NewMemoryAdapter(mem)

	channel := "#count"
	for i := 0; i < 5; i++ {
		if err := mem.AddMessage(channel, "user", "msg", "", ""); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}

	count, err := adapter.GetHistoryCount(channel)
	if err != nil {
		t.Fatalf("GetHistoryCount: %v", err)
	}

	if count != 5 {
		t.Errorf("expected count 5, got %d", count)
	}
}

func TestMemoryAdapter_GetHistoryCount_EmptyChannel(t *testing.T) {
	t.Parallel()

	mem := newAdapterTestMemory(t)
	adapter := NewMemoryAdapter(mem)

	count, err := adapter.GetHistoryCount("#no-messages")
	if err != nil {
		t.Fatalf("GetHistoryCount: %v", err)
	}

	if count != 0 {
		t.Errorf("expected count 0, got %d", count)
	}
}
