package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"

	"murmur/internal/bus"
	"murmur/internal/tools"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newTestClient creates a minimal Client for testing tool dispatch without
// requiring an IRC connection.
func newTestClient(toolList []tools.Tool) *Client {
	handlers := make(map[string]tools.Tool, len(toolList))
	for _, t := range toolList {
		handlers[t.Name] = t
	}

	return &Client{
		toolHandlers: handlers,
		toolSem:      make(chan struct{}, maxConcurrentTools),
		logger:       newTestLogger(),
	}
}

func TestRegistrationMessage(t *testing.T) {
	t.Parallel()

	toolDefs := []bus.ToolDef{
		{
			Name:        "shell",
			Description: "Run shell commands",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		},
	}

	msg := &bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "laptop-home",
		Hostname: "thinkpad",
		Tools:    toolDefs,
	}

	data, err := bus.MarshalMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, parsed, err := bus.ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reg, ok := parsed.(*bus.RegisterMessage)
	if !ok {
		t.Fatalf("expected *RegisterMessage, got %T", parsed)
	}
	if reg.ClientID != "laptop-home" {
		t.Errorf("ClientID = %q, want %q", reg.ClientID, "laptop-home")
	}
	if reg.Hostname != "thinkpad" {
		t.Errorf("Hostname = %q, want %q", reg.Hostname, "thinkpad")
	}
	if len(reg.Tools) != 1 {
		t.Fatalf("len(Tools) = %d, want 1", len(reg.Tools))
	}
	if reg.Tools[0].Name != "shell" {
		t.Errorf("Tools[0].Name = %q, want %q", reg.Tools[0].Name, "shell")
	}
}

func TestDeregistrationMessage(t *testing.T) {
	t.Parallel()

	msg := &bus.DeregisterMessage{
		Type:     bus.TypeDeregister,
		ClientID: "laptop-home",
	}

	data, err := bus.MarshalMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, parsed, err := bus.ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dereg, ok := parsed.(*bus.DeregisterMessage)
	if !ok {
		t.Fatalf("expected *DeregisterMessage, got %T", parsed)
	}
	if dereg.ClientID != "laptop-home" {
		t.Errorf("ClientID = %q, want %q", dereg.ClientID, "laptop-home")
	}
}

func TestGetSystemLoad(t *testing.T) {
	t.Parallel()

	load := getSystemLoad()

	// On any platform, values should be non-negative.
	if load.CPU < 0 {
		t.Errorf("CPU = %f, want >= 0", load.CPU)
	}
	if load.Memory < 0 {
		t.Errorf("Memory = %f, want >= 0", load.Memory)
	}
}

func TestGetSystemLoad_Values(t *testing.T) {
	t.Parallel()

	load := getSystemLoad()

	// Memory percentage should be between 0 and 100 (or 0 on non-Linux).
	if load.Memory > 100 {
		t.Errorf("Memory = %f, want <= 100", load.Memory)
	}
}

func TestHandleToolRequest_Dispatch(t *testing.T) {
	t.Parallel()

	var called bool
	var gotArgs map[string]any

	testTool := tools.Tool{
		Name:        "test_tool",
		Description: "A test tool",
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			called = true
			gotArgs = args
			return "test result", nil
		},
	}

	c := newTestClient([]tools.Tool{testTool})

	var responseStatus, responseResult string
	c.sendResponseFunc = func(requestID, status, result string) error {
		responseStatus = status
		responseResult = result
		return nil
	}

	msg := &bus.ToolRequestMessage{
		Type:      bus.TypeToolRequest,
		RequestID: "req-test-1",
		Tool:      "test_tool",
		Arguments: json.RawMessage(`{"key":"value"}`),
	}

	c.handleToolRequest("server", msg)

	if !called {
		t.Fatal("tool handler was not called")
	}
	if gotArgs["key"] != "value" {
		t.Errorf("args[key] = %v, want %q", gotArgs["key"], "value")
	}
	if responseStatus != "success" {
		t.Errorf("response status = %q, want %q", responseStatus, "success")
	}
	if responseResult != "test result" {
		t.Errorf("response result = %q, want %q", responseResult, "test result")
	}
}

func TestHandleToolRequest_UnknownTool(t *testing.T) {
	t.Parallel()

	c := newTestClient(nil)

	var responseSent bool
	c.sendResponseFunc = func(_, _, _ string) error {
		responseSent = true
		return nil
	}

	msg := &bus.ToolRequestMessage{
		Type:      bus.TypeToolRequest,
		RequestID: "req-test-2",
		Tool:      "nonexistent_tool",
		Arguments: json.RawMessage(`{}`),
	}

	c.handleToolRequest("server", msg)

	if responseSent {
		t.Error("response was sent for unknown tool — should be silently ignored")
	}
}

func TestHandleToolRequest_HandlerError(t *testing.T) {
	t.Parallel()

	testTool := tools.Tool{
		Name:        "failing_tool",
		Description: "A tool that fails",
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "", fmt.Errorf("something went wrong")
		},
	}

	c := newTestClient([]tools.Tool{testTool})

	var responseStatus, responseResult string
	c.sendResponseFunc = func(_, status, result string) error {
		responseStatus = status
		responseResult = result
		return nil
	}

	msg := &bus.ToolRequestMessage{
		Type:      bus.TypeToolRequest,
		RequestID: "req-test-3",
		Tool:      "failing_tool",
		Arguments: json.RawMessage(`{}`),
	}

	c.handleToolRequest("server", msg)

	if responseStatus != "error" {
		t.Errorf("response status = %q, want %q", responseStatus, "error")
	}
	if responseResult != "something went wrong" {
		t.Errorf("response result = %q, want %q", responseResult, "something went wrong")
	}
}

func TestHandleToolRequest_ConcurrencyLimit(t *testing.T) {
	t.Parallel()

	// Create a tool that signals when it starts, then blocks until released.
	blockCh := make(chan struct{})
	acquired := make(chan struct{}, 2) // signals when handlers have acquired semaphore
	slowTool := tools.Tool{
		Name:        "slow_tool",
		Description: "A slow tool",
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			acquired <- struct{}{} // signal that we're running
			<-blockCh
			return "done", nil
		},
	}

	c := newTestClient([]tools.Tool{slowTool})
	// Use a small semaphore for testing.
	c.toolSem = make(chan struct{}, 2)

	var mu sync.Mutex
	responses := make(map[string]string) // requestID -> status

	c.sendResponseFunc = func(requestID, status, _ string) error {
		mu.Lock()
		responses[requestID] = status
		mu.Unlock()
		return nil
	}

	// Fill the semaphore with 2 blocking requests.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			msg := &bus.ToolRequestMessage{
				Type:      bus.TypeToolRequest,
				RequestID: fmt.Sprintf("req-block-%d", id),
				Tool:      "slow_tool",
				Arguments: json.RawMessage(`{}`),
			}
			c.handleToolRequest("server", msg)
		}(i)
	}

	// Wait for both handlers to signal they've acquired the semaphore.
	<-acquired
	<-acquired

	// Third request should be rejected — semaphore is full.
	msg := &bus.ToolRequestMessage{
		Type:      bus.TypeToolRequest,
		RequestID: "req-overflow",
		Tool:      "slow_tool",
		Arguments: json.RawMessage(`{}`),
	}
	c.handleToolRequest("server", msg)

	mu.Lock()
	overflowStatus := responses["req-overflow"]
	mu.Unlock()

	if overflowStatus != "error" {
		t.Errorf("overflow request status = %q, want %q", overflowStatus, "error")
	}

	// Release blocked goroutines.
	close(blockCh)
	wg.Wait()
}

func TestHandleToolRequest_PanicRecovery(t *testing.T) {
	t.Parallel()

	panicTool := tools.Tool{
		Name:        "panic_tool",
		Description: "A tool that panics",
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			panic("unexpected panic")
		},
	}

	c := newTestClient([]tools.Tool{panicTool})

	var responseStatus, responseResult string
	c.sendResponseFunc = func(_, status, result string) error {
		responseStatus = status
		responseResult = result
		return nil
	}

	msg := &bus.ToolRequestMessage{
		Type:      bus.TypeToolRequest,
		RequestID: "req-panic",
		Tool:      "panic_tool",
		Arguments: json.RawMessage(`{}`),
	}

	// This should not panic — the handler should recover.
	c.handleToolRequest("server", msg)

	if responseStatus != "error" {
		t.Errorf("response status = %q, want %q", responseStatus, "error")
	}
	if responseResult != "internal error: tool handler panicked" {
		t.Errorf("response result = %q, want panic error message", responseResult)
	}
}

func TestHandleToolRequest_InvalidArguments(t *testing.T) {
	t.Parallel()

	testTool := tools.Tool{
		Name:        "test_tool",
		Description: "A test tool",
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "ok", nil
		},
	}

	c := newTestClient([]tools.Tool{testTool})

	var responseStatus string
	c.sendResponseFunc = func(_, status, _ string) error {
		responseStatus = status
		return nil
	}

	msg := &bus.ToolRequestMessage{
		Type:      bus.TypeToolRequest,
		RequestID: "req-bad-args",
		Tool:      "test_tool",
		Arguments: json.RawMessage(`not valid json`),
	}

	c.handleToolRequest("server", msg)

	if responseStatus != "error" {
		t.Errorf("response status = %q, want %q for invalid JSON args", responseStatus, "error")
	}
}

func TestHandleToolRequest_EmptyArguments(t *testing.T) {
	t.Parallel()

	var gotArgs map[string]any
	testTool := tools.Tool{
		Name:        "test_tool",
		Description: "A test tool",
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			gotArgs = args
			return "ok", nil
		},
	}

	c := newTestClient([]tools.Tool{testTool})
	c.sendResponseFunc = func(_, _, _ string) error { return nil }

	msg := &bus.ToolRequestMessage{
		Type:      bus.TypeToolRequest,
		RequestID: "req-empty-args",
		Tool:      "test_tool",
		Arguments: nil,
	}

	c.handleToolRequest("server", msg)

	if gotArgs == nil {
		t.Fatal("args should be non-nil empty map, got nil")
	}
	if len(gotArgs) != 0 {
		t.Errorf("args should be empty, got %v", gotArgs)
	}
}
