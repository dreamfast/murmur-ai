package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"murmur/internal/bus"
)

// Router manages pending tool requests and routes responses from clients
// back to the agent loop. It maintains a concurrent-safe map of pending
// request channels keyed by request ID.
type Router struct {
	registry *Registry
	sender   *bus.Sender
	logger   *slog.Logger

	mu      sync.Mutex
	pending map[string]chan *bus.ToolResponseMessage
}

// NewRouter creates a new tool request router.
func NewRouter(registry *Registry, sender *bus.Sender, logger *slog.Logger) *Router {
	return &Router{
		registry: registry,
		sender:   sender,
		logger:   logger,
		pending:  make(map[string]chan *bus.ToolResponseMessage),
	}
}

// RouteToolCall sends a tool execution request to the appropriate client and
// waits for the response. It returns the tool result string or an error if
// the tool is unavailable, the request times out, or the context is cancelled.
// The pending entry is always cleaned up on return.
func (r *Router) RouteToolCall(ctx context.Context, toolName string, arguments json.RawMessage, timeout time.Duration) (string, error) {
	// Find the client that provides this tool.
	_, ok := r.registry.GetToolProvider(toolName)
	if !ok {
		return "", fmt.Errorf("RouteToolCall: tool %q not available, no online client provides it", toolName)
	}

	// Generate a unique request ID.
	reqID, err := generateRequestID()
	if err != nil {
		return "", fmt.Errorf("RouteToolCall: generate request ID: %w", err)
	}

	// Create a buffered response channel (cap 1 so the sender never blocks).
	respCh := make(chan *bus.ToolResponseMessage, 1)

	// Register the pending request.
	r.mu.Lock()
	r.pending[reqID] = respCh
	r.mu.Unlock()

	// Always clean up the pending entry on return.
	defer func() {
		r.mu.Lock()
		delete(r.pending, reqID)
		r.mu.Unlock()
	}()

	// Send the tool request to the client via the bus.
	if err := r.sender.SendToolRequest(reqID, toolName, arguments); err != nil {
		return "", fmt.Errorf("RouteToolCall: send request: %w", err)
	}

	r.logger.Debug("tool request sent",
		"request_id", reqID,
		"tool", toolName,
	)

	// Wait for the response with a timeout.
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case msg := <-respCh:
		if msg.Status == "error" {
			return "", fmt.Errorf("tool %q returned error: %s", toolName, msg.Result)
		}
		return msg.Result, nil
	case <-timeoutCtx.Done():
		if ctx.Err() != nil {
			return "", fmt.Errorf("RouteToolCall: context cancelled while waiting for tool %q: %w", toolName, ctx.Err())
		}
		return "", fmt.Errorf("RouteToolCall: timeout waiting for tool %q (request_id=%s)", toolName, reqID)
	}
}

// HandleToolResponse dispatches a tool response to the waiting RouteToolCall
// goroutine. If no pending request matches the response's RequestID, the
// response is logged and dropped (stale or duplicate). A nil msg is ignored.
func (r *Router) HandleToolResponse(nick string, msg *bus.ToolResponseMessage) {
	if msg == nil {
		return
	}

	r.mu.Lock()
	ch, ok := r.pending[msg.RequestID]
	delete(r.pending, msg.RequestID)
	r.mu.Unlock()

	if !ok {
		r.logger.Warn("received tool response for unknown request",
			"request_id", msg.RequestID,
			"nick", nick,
			"status", msg.Status,
		)
		return
	}

	// Non-blocking send — channel is buffered with cap 1.
	ch <- msg
}

// generateRequestID returns a unique request ID in the format "req-<16 hex chars>".
func generateRequestID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "req-" + hex.EncodeToString(b), nil
}
