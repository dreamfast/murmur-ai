package server

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"murmur/internal/bus"
)

// newTestRouter creates a Router with a registry containing one online client
// that provides the "shell" tool. The sender is nil (tests that need to
// exercise the full send path must handle the nil-conn panic or use
// goroutine-based response injection).
func newTestRouter(t *testing.T) (*Router, *Registry) {
	t.Helper()
	logger := newTestLogger()
	registry := NewRegistry(2*time.Minute, logger)

	registry.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "test-client",
		Hostname: "test-host",
		Tools: []bus.ToolDef{
			{Name: "shell", Description: "Run commands", Parameters: json.RawMessage(`{}`)},
		},
	})

	// Create a sender with nil connection — we'll inject responses directly
	// via HandleToolResponse for tests that need the happy path.
	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)
	return router, registry
}

func TestRouter_UnknownTool(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	ctx := context.Background()
	_, err := router.RouteToolCall(ctx, "nonexistent", json.RawMessage(`{}`), 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected error for unknown tool, got nil")
	}
	if got := err.Error(); got != `RouteToolCall: tool "nonexistent" not available, no online client provides it` {
		t.Errorf("error = %q", got)
	}
}

func TestRouter_HandleToolResponse_Delivers(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	// Manually register a pending request (simulating what RouteToolCall does).
	respCh := make(chan *bus.ToolResponseMessage, 1)
	router.mu.Lock()
	router.pending["req-test-123"] = respCh
	router.mu.Unlock()

	// Deliver the response.
	router.HandleToolResponse("test-client", &bus.ToolResponseMessage{
		Type:      bus.TypeToolResponse,
		RequestID: "req-test-123",
		Status:    "success",
		Result:    "hello world",
	})

	// The response should be on the channel.
	select {
	case msg := <-respCh:
		if msg.Result != "hello world" {
			t.Errorf("result = %q, want %q", msg.Result, "hello world")
		}
		if msg.Status != "success" {
			t.Errorf("status = %q, want %q", msg.Status, "success")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for response on channel")
	}

	// Pending entry should be cleaned up.
	router.mu.Lock()
	_, exists := router.pending["req-test-123"]
	router.mu.Unlock()
	if exists {
		t.Error("expected pending entry to be cleaned up after HandleToolResponse")
	}
}

func TestRouter_HandleToolResponse_StaleRequest(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	// Deliver a response for a non-existent request — should not panic.
	router.HandleToolResponse("test-client", &bus.ToolResponseMessage{
		Type:      bus.TypeToolResponse,
		RequestID: "req-stale-999",
		Status:    "success",
		Result:    "stale result",
	})
	// If we get here without panic, the test passes.
}

func TestRouter_Timeout(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	// Manually register a pending request that will never get a response.
	respCh := make(chan *bus.ToolResponseMessage, 1)
	reqID := "req-timeout-test"

	router.mu.Lock()
	router.pending[reqID] = respCh
	router.mu.Unlock()

	// Simulate what RouteToolCall does after sending: wait with timeout.
	ctx := context.Background()
	timeoutCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	select {
	case <-respCh:
		t.Fatal("expected timeout, got response")
	case <-timeoutCtx.Done():
		// Expected — timeout occurred.
	}

	// Clean up (as RouteToolCall would).
	router.mu.Lock()
	delete(router.pending, reqID)
	router.mu.Unlock()

	// Verify pending entry is cleaned up.
	router.mu.Lock()
	_, exists := router.pending[reqID]
	router.mu.Unlock()
	if exists {
		t.Error("expected pending entry to be cleaned up after timeout")
	}
}

func TestRouter_ContextCancellation(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	// Manually register a pending request.
	respCh := make(chan *bus.ToolResponseMessage, 1)
	reqID := "req-cancel-test"

	router.mu.Lock()
	router.pending[reqID] = respCh
	router.mu.Unlock()

	// Cancel the context immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, 5*time.Second)
	defer timeoutCancel()

	select {
	case <-respCh:
		t.Fatal("expected cancellation, got response")
	case <-timeoutCtx.Done():
		// Expected — context was cancelled.
	}

	// Clean up.
	router.mu.Lock()
	delete(router.pending, reqID)
	router.mu.Unlock()
}

func TestRouter_ConcurrentRequests(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	const numRequests = 10
	var wg sync.WaitGroup

	// Register multiple pending requests.
	channels := make(map[string]chan *bus.ToolResponseMessage)
	for i := 0; i < numRequests; i++ {
		reqID := "req-concurrent-" + string(rune('a'+i))
		ch := make(chan *bus.ToolResponseMessage, 1)
		channels[reqID] = ch

		router.mu.Lock()
		router.pending[reqID] = ch
		router.mu.Unlock()
	}

	// Deliver responses concurrently.
	for reqID := range channels {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			router.HandleToolResponse("test-client", &bus.ToolResponseMessage{
				Type:      bus.TypeToolResponse,
				RequestID: id,
				Status:    "success",
				Result:    "result-" + id,
			})
		}(reqID)
	}

	wg.Wait()

	// Verify all responses were delivered.
	for reqID, ch := range channels {
		select {
		case msg := <-ch:
			expected := "result-" + reqID
			if msg.Result != expected {
				t.Errorf("reqID=%s: result = %q, want %q", reqID, msg.Result, expected)
			}
		default:
			t.Errorf("reqID=%s: no response received", reqID)
		}
	}

	// Verify all pending entries are cleaned up.
	router.mu.Lock()
	remaining := len(router.pending)
	router.mu.Unlock()
	if remaining != 0 {
		t.Errorf("expected 0 pending entries, got %d", remaining)
	}
}

func TestRouter_ErrorToolResponse(t *testing.T) {
	t.Parallel()
	router, _ := newTestRouter(t)

	// Register a pending request.
	respCh := make(chan *bus.ToolResponseMessage, 1)
	router.mu.Lock()
	router.pending["req-error-test"] = respCh
	router.mu.Unlock()

	// Deliver an error response.
	router.HandleToolResponse("test-client", &bus.ToolResponseMessage{
		Type:      bus.TypeToolResponse,
		RequestID: "req-error-test",
		Status:    "error",
		Result:    "command not found",
	})

	select {
	case msg := <-respCh:
		if msg.Status != "error" {
			t.Errorf("status = %q, want %q", msg.Status, "error")
		}
		if msg.Result != "command not found" {
			t.Errorf("result = %q, want %q", msg.Result, "command not found")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for error response")
	}
}

func TestGenerateRequestID(t *testing.T) {
	t.Parallel()

	id, err := generateRequestID()
	if err != nil {
		t.Fatalf("generateRequestID: %v", err)
	}

	// Should start with "req-" prefix.
	if len(id) < 4 || id[:4] != "req-" {
		t.Errorf("id = %q, want prefix 'req-'", id)
	}

	// Should be "req-" + 16 hex chars = 20 chars total.
	if len(id) != 20 {
		t.Errorf("len(id) = %d, want 20", len(id))
	}

	// Generate another — should be different.
	id2, err := generateRequestID()
	if err != nil {
		t.Fatalf("generateRequestID: %v", err)
	}
	if id == id2 {
		t.Error("two generated IDs should be different")
	}
}
