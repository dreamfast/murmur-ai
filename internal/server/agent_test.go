package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"murmur/internal/bus"
	"murmur/internal/config"
	"murmur/internal/db"
	"murmur/internal/irc"
	"murmur/internal/llm"
	"murmur/internal/tools"
)

// testAgentEnv holds all the components needed for agent tests.
type testAgentEnv struct {
	agent    *Agent
	registry *Registry
	memory   *Memory
	router   *Router
	mock     *llm.MockProvider
	sent     []string // captured IRC messages
	mu       sync.Mutex
}

// appendSent safely appends a message to the sent slice.
func (env *testAgentEnv) appendSent(channel, message string) {
	env.mu.Lock()
	defer env.mu.Unlock()
	env.sent = append(env.sent, message)
}

// getSent safely returns a copy of the sent slice.
func (env *testAgentEnv) getSent() []string {
	env.mu.Lock()
	defer env.mu.Unlock()
	result := make([]string, len(env.sent))
	copy(result, env.sent)
	return result
}

// newTestAgentEnv creates a test environment with a mock LLM provider and
// a router that can have responses injected directly.
func newTestAgentEnv(t *testing.T) *testAgentEnv {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Set up in-memory database.
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	memory := NewMemory(database, 100, 80, nil, logger)
	registry := NewRegistry(2*time.Minute, logger)

	// Register a test client with a "shell" tool.
	registry.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "test-client",
		Hostname: "test-host",
		Tools: []bus.ToolDef{
			{Name: "shell", Description: "Run commands", Parameters: json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string"}}}`)},
		},
	})

	// Create a sender with nil connection — we inject responses directly.
	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)

	mock := &llm.MockProvider{
		NameVal: "test-provider",
	}

	providers := map[string]llm.Provider{
		"test-provider": mock,
	}

	env := &testAgentEnv{
		registry: registry,
		memory:   memory,
		router:   router,
		mock:     mock,
	}

	agent := NewAgent(
		providers,
		"test-provider",
		nil, // no server-side tools by default
		registry,
		memory,
		router,
		nil, // no approval manager by default
		nil, // no IRC connection in tests
		"You are a test assistant.",
		"test-server",
		"#test-bus",
		100,
		0,             // cross-channel context disabled in tests
		nil,           // no channel settings by default
		2*time.Second, // short timeout for tests
		2*time.Second, // short approval timeout for tests
		false,         // verbose off in tests
		logger,
	)
	agent.sendFunc = env.appendSent
	env.agent = agent

	return env
}

func TestAgent_TextOnlyResponse(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	env.mock.Responses = []*llm.ChatResponse{
		{Content: "Hello! How can I help?"},
	}

	ctx := context.Background()
	env.agent.HandleMessage(ctx, "#test", "user1", "hello")

	sent := env.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sent), sent)
	}
	if sent[0] != "Hello! How can I help?" {
		t.Errorf("sent = %q, want %q", sent[0], "Hello! How can I help?")
	}

	// Verify the mock was called once.
	if len(env.mock.Calls) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(env.mock.Calls))
	}

	// Verify the request included system prompt and user message.
	req := env.mock.Calls[0]
	if len(req.Messages) < 2 {
		t.Fatalf("expected at least 2 messages in request, got %d", len(req.Messages))
	}
	if req.Messages[0].Role != llm.RoleSystem {
		t.Errorf("first message role = %q, want %q", req.Messages[0].Role, llm.RoleSystem)
	}
	if !strings.HasPrefix(req.Messages[0].Content, "You are a test assistant.") {
		t.Errorf("system prompt should start with base prompt, got %q", req.Messages[0].Content)
	}
	if !strings.Contains(req.Messages[0].Content, "Active model: test-provider") {
		t.Errorf("system prompt should contain active model, got %q", req.Messages[0].Content)
	}

	// Verify messages were stored in memory.
	msgs, err := env.memory.GetHistory("#test", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages in memory, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("msg[0].Role = %q, want %q", msgs[0].Role, "user")
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("msg[1].Role = %q, want %q", msgs[1].Role, "assistant")
	}
}

func TestAgent_ToolCallFlow(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// First LLM call returns a tool call, second returns text.
	env.mock.Responses = []*llm.ChatResponse{
		{
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call-1",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "shell",
						Arguments: `{"cmd":"ls"}`,
					},
				},
			},
		},
		{Content: "The directory contains: file1.txt, file2.txt"},
	}

	// Override tool routing to return a canned result.
	env.agent.routeToolFunc = func(_ context.Context, toolName string, _ json.RawMessage) (string, error) {
		if toolName == "shell" {
			return "file1.txt\nfile2.txt", nil
		}
		return "", fmt.Errorf("unknown tool: %s", toolName)
	}

	ctx := context.Background()
	env.agent.HandleMessage(ctx, "#test", "user1", "list files")

	sent := env.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sent), sent)
	}
	if sent[0] != "The directory contains: file1.txt, file2.txt" {
		t.Errorf("sent = %q", sent[0])
	}

	// Verify the mock was called twice (tool call + final text).
	if len(env.mock.Calls) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(env.mock.Calls))
	}

	// Verify the second LLM call received reconstructed tool_calls on the
	// assistant message (not raw JSON content). This is critical for
	// OpenAI-compatible API compliance.
	secondCall := env.mock.Calls[1]
	foundAssistantWithToolCalls := false
	for _, msg := range secondCall.Messages {
		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
			foundAssistantWithToolCalls = true
			if msg.ToolCalls[0].Function.Name != "shell" {
				t.Errorf("reconstructed tool call name = %q, want shell", msg.ToolCalls[0].Function.Name)
			}
			if msg.Content != "" {
				t.Errorf("assistant message with tool_calls should have empty content, got %q", msg.Content)
			}
			break
		}
	}
	if !foundAssistantWithToolCalls {
		t.Error("second LLM call should contain an assistant message with reconstructed tool_calls")
	}

	// Verify memory contains: user, assistant (tool calls), tool result, assistant (text).
	msgs, err := env.memory.GetHistory("#test", 20)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages in memory, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("msg[0].Role = %q, want user", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("msg[1].Role = %q, want assistant", msgs[1].Role)
	}
	if msgs[2].Role != "tool" {
		t.Errorf("msg[2].Role = %q, want tool", msgs[2].Role)
	}
	if msgs[2].Name != "shell" {
		t.Errorf("msg[2].Name = %q, want shell", msgs[2].Name)
	}
	if msgs[2].ToolCallID != "call-1" {
		t.Errorf("msg[2].ToolCallID = %q, want call-1", msgs[2].ToolCallID)
	}
	if msgs[3].Role != "assistant" {
		t.Errorf("msg[3].Role = %q, want assistant", msgs[3].Role)
	}
}

func TestAgent_MultipleToolCalls(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// LLM returns two tool calls in one response.
	env.mock.Responses = []*llm.ChatResponse{
		{
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call-1",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "shell",
						Arguments: `{"cmd":"uptime"}`,
					},
				},
				{
					ID:   "call-2",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "dns_lookup",
						Arguments: `{"domain":"example.com"}`,
					},
				},
			},
		},
		{Content: "System is up and DNS resolves."},
	}

	// Override tool routing to return canned results.
	var toolCallCount atomic.Int32
	env.agent.routeToolFunc = func(_ context.Context, toolName string, _ json.RawMessage) (string, error) {
		n := toolCallCount.Add(1)
		return fmt.Sprintf("result-%s-%d", toolName, n), nil
	}

	ctx := context.Background()
	env.agent.HandleMessage(ctx, "#test", "user1", "check system")

	sent := env.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sent), sent)
	}
	if sent[0] != "System is up and DNS resolves." {
		t.Errorf("sent = %q", sent[0])
	}

	// Verify 2 LLM calls.
	if len(env.mock.Calls) != 2 {
		t.Errorf("expected 2 LLM calls, got %d", len(env.mock.Calls))
	}

	// Verify both tool calls were routed.
	if toolCallCount.Load() != 2 {
		t.Errorf("expected 2 tool calls, got %d", toolCallCount.Load())
	}
}

func TestAgent_MaxIterationsCap(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Mock always returns a tool call — should hit the iteration cap.
	env.mock.Responses = []*llm.ChatResponse{
		{
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call-loop",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "shell",
						Arguments: `{"cmd":"echo loop"}`,
					},
				},
			},
		},
	}

	// Override tool routing to always succeed.
	env.agent.routeToolFunc = func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
		return "loop result", nil
	}

	ctx := context.Background()
	env.agent.HandleMessage(ctx, "#test", "user1", "loop forever")

	sent := env.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sent), sent)
	}
	if sent[0] != "I've reached the maximum number of tool calls for this message. Please try again." {
		t.Errorf("sent = %q", sent[0])
	}

	// Verify the mock was called exactly maxIterations times.
	if len(env.mock.Calls) != maxIterations {
		t.Errorf("expected %d LLM calls, got %d", maxIterations, len(env.mock.Calls))
	}
}

func TestAgent_LLMError(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	env.mock.Errors = []error{errors.New("API rate limit exceeded")}

	ctx := context.Background()
	env.agent.HandleMessage(ctx, "#test", "user1", "hello")

	sent := env.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sent), sent)
	}
	if sent[0] != "error: LLM call failed: API rate limit exceeded" {
		t.Errorf("sent = %q", sent[0])
	}
}

func TestAgent_ToolRoutingError(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// LLM requests a tool that will fail.
	env.mock.Responses = []*llm.ChatResponse{
		{
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call-bad",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "broken_tool",
						Arguments: `{}`,
					},
				},
			},
		},
		{Content: "I couldn't find that tool, sorry."},
	}

	// Override tool routing to return an error.
	env.agent.routeToolFunc = func(_ context.Context, toolName string, _ json.RawMessage) (string, error) {
		return "", fmt.Errorf("tool %q not available, no online client provides it", toolName)
	}

	ctx := context.Background()
	env.agent.HandleMessage(ctx, "#test", "user1", "use bad tool")

	sent := env.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sent), sent)
	}
	// The LLM should have received the error and produced a text response.
	if sent[0] != "I couldn't find that tool, sorry." {
		t.Errorf("sent = %q", sent[0])
	}

	// Verify the error was stored as a tool result in memory.
	msgs, err := env.memory.GetHistory("#test", 20)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	// user, assistant (tool call), tool (error), assistant (text)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
	if msgs[2].Role != "tool" {
		t.Errorf("msg[2].Role = %q, want tool", msgs[2].Role)
	}
	if len(msgs[2].Content) < 6 || msgs[2].Content[:6] != "error:" {
		t.Errorf("msg[2].Content = %q, expected error prefix", msgs[2].Content)
	}
}

func TestAgent_SetProvider(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Add a second provider.
	env.agent.providers["other-provider"] = &llm.MockProvider{NameVal: "other-provider"}

	// Switch to the other provider for a channel.
	if err := env.agent.SetProvider("#test", "other-provider"); err != nil {
		t.Fatalf("SetProvider: %v", err)
	}

	// Global default should remain unchanged (channelSettings is nil,
	// so the override is not persisted, but no error occurs).
	if got := env.agent.GetProvider(); got != "test-provider" {
		t.Errorf("GetProvider (global) = %q, want %q", got, "test-provider")
	}

	// Switch to a nonexistent provider.
	if err := env.agent.SetProvider("#test", "nonexistent"); err == nil {
		t.Error("expected error for nonexistent provider")
	}

	// Reset to default should succeed.
	if err := env.agent.SetProvider("#test", "default"); err != nil {
		t.Fatalf("SetProvider default: %v", err)
	}
}

func TestAgent_SetProvider_PerChannel(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	memory := NewMemory(database, 100, 80, nil, logger)
	registry := NewRegistry(2*time.Minute, logger)
	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)
	channelSettings := NewChannelSettingsStore(database, logger)

	mock := &llm.MockProvider{NameVal: "default-provider"}
	other := &llm.MockProvider{NameVal: "other-provider"}
	providers := map[string]llm.Provider{
		"default-provider": mock,
		"other-provider":   other,
	}

	agent := NewAgent(
		providers,
		"default-provider",
		nil,
		registry,
		memory,
		router,
		nil,
		nil,
		"test prompt",
		"test-server",
		"#test-bus",
		100,
		0,
		channelSettings,
		2*time.Second,
		2*time.Second,
		false,
		logger,
	)

	// Initially, channel should use global default.
	if got := agent.GetProviderForChannel("#chan1"); got != "default-provider" {
		t.Errorf("GetProviderForChannel = %q, want %q", got, "default-provider")
	}

	// Set per-channel override.
	if err := agent.SetProvider("#chan1", "other-provider"); err != nil {
		t.Fatalf("SetProvider: %v", err)
	}
	if got := agent.GetProviderForChannel("#chan1"); got != "other-provider" {
		t.Errorf("GetProviderForChannel = %q, want %q", got, "other-provider")
	}

	// Other channels still use global default.
	if got := agent.GetProviderForChannel("#chan2"); got != "default-provider" {
		t.Errorf("GetProviderForChannel #chan2 = %q, want %q", got, "default-provider")
	}

	// Global default unchanged.
	if got := agent.GetProvider(); got != "default-provider" {
		t.Errorf("GetProvider (global) = %q, want %q", got, "default-provider")
	}

	// Reset to default.
	if err := agent.SetProvider("#chan1", "default"); err != nil {
		t.Fatalf("SetProvider default: %v", err)
	}
	if got := agent.GetProviderForChannel("#chan1"); got != "default-provider" {
		t.Errorf("GetProviderForChannel after reset = %q, want %q", got, "default-provider")
	}
}

func TestAgent_ResolveProvider(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	memory := NewMemory(database, 100, 80, nil, logger)
	registry := NewRegistry(2*time.Minute, logger)
	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)
	channelSettings := NewChannelSettingsStore(database, logger)

	mock := &llm.MockProvider{NameVal: "default-provider"}
	other := &llm.MockProvider{NameVal: "other-provider"}
	providers := map[string]llm.Provider{
		"default-provider": mock,
		"other-provider":   other,
	}

	agent := NewAgent(
		providers,
		"default-provider",
		nil,
		registry,
		memory,
		router,
		nil,
		nil,
		"test prompt",
		"test-server",
		"#test-bus",
		100,
		0,
		channelSettings,
		2*time.Second,
		2*time.Second,
		false,
		logger,
	)

	// No override — should resolve to global default.
	p, err := agent.resolveProvider("#chan1")
	if err != nil {
		t.Fatalf("resolveProvider: %v", err)
	}
	if p.Name() != "default-provider" {
		t.Errorf("resolveProvider = %q, want %q", p.Name(), "default-provider")
	}

	// Set per-channel override.
	if err := agent.SetProvider("#chan1", "other-provider"); err != nil {
		t.Fatalf("SetProvider: %v", err)
	}
	p, err = agent.resolveProvider("#chan1")
	if err != nil {
		t.Fatalf("resolveProvider: %v", err)
	}
	if p.Name() != "other-provider" {
		t.Errorf("resolveProvider = %q, want %q", p.Name(), "other-provider")
	}

	// Other channel still resolves to default.
	p, err = agent.resolveProvider("#chan2")
	if err != nil {
		t.Fatalf("resolveProvider: %v", err)
	}
	if p.Name() != "default-provider" {
		t.Errorf("resolveProvider #chan2 = %q, want %q", p.Name(), "default-provider")
	}
}

func TestAgent_ResolveProvider_NilChannelSettings(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// channelSettings is nil in default test env — should fall back to global.
	p, err := env.agent.resolveProvider("#test")
	if err != nil {
		t.Fatalf("resolveProvider: %v", err)
	}
	if p.Name() != "test-provider" {
		t.Errorf("resolveProvider = %q, want %q", p.Name(), "test-provider")
	}
}

func TestAgent_ResolveProvider_StaleOverride(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	memory := NewMemory(database, 100, 80, nil, logger)
	registry := NewRegistry(2*time.Minute, logger)
	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)
	channelSettings := NewChannelSettingsStore(database, logger)

	// Set a channel override to a provider that will be "removed" from config.
	if err := channelSettings.SetProvider("#chan1", "removed-provider"); err != nil {
		t.Fatalf("SetProvider: %v", err)
	}

	mock := &llm.MockProvider{NameVal: "default-provider"}
	providers := map[string]llm.Provider{
		"default-provider": mock,
		// "removed-provider" is NOT in the providers map — simulates config removal.
	}

	agent := NewAgent(
		providers,
		"default-provider",
		nil,
		registry,
		memory,
		router,
		nil,
		nil,
		"test prompt",
		"test-server",
		"#test-bus",
		100,
		0,
		channelSettings,
		2*time.Second,
		2*time.Second,
		false,
		logger,
	)

	// resolveProvider should fall back to global default when override is stale.
	p, err := agent.resolveProvider("#chan1")
	if err != nil {
		t.Fatalf("resolveProvider: %v", err)
	}
	if p.Name() != "default-provider" {
		t.Errorf("resolveProvider = %q, want %q (stale override should fall back)", p.Name(), "default-provider")
	}

	// GetProviderForChannel should also fall back to global default.
	if got := agent.GetProviderForChannel("#chan1"); got != "default-provider" {
		t.Errorf("GetProviderForChannel = %q, want %q (stale override should fall back)", got, "default-provider")
	}
}

func TestAgent_GetProviderNames(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Add more providers.
	env.agent.providers["beta"] = &llm.MockProvider{NameVal: "beta"}
	env.agent.providers["alpha"] = &llm.MockProvider{NameVal: "alpha"}

	names := env.agent.GetProviderNames()
	if len(names) != 3 {
		t.Fatalf("expected 3 provider names, got %d: %v", len(names), names)
	}
	// Should be sorted alphabetically.
	if names[0] != "alpha" || names[1] != "beta" || names[2] != "test-provider" {
		t.Errorf("names = %v, want [alpha beta test-provider]", names)
	}
}

func TestAgent_PerChannelLocking(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	env.mock.Responses = []*llm.ChatResponse{
		{Content: "response"},
	}

	ctx := context.Background()

	// Process messages on two different channels sequentially.
	env.agent.HandleMessage(ctx, "#chan1", "user1", "hello from chan1")
	env.agent.HandleMessage(ctx, "#chan2", "user2", "hello from chan2")

	// Verify both channels have messages in memory (channel isolation).
	for _, ch := range []string{"#chan1", "#chan2"} {
		msgs, err := env.memory.GetHistory(ch, 10)
		if err != nil {
			t.Fatalf("GetHistory %s: %v", ch, err)
		}
		if len(msgs) < 2 {
			t.Errorf("channel %s: expected at least 2 messages, got %d", ch, len(msgs))
		}
	}

	// Verify per-channel locks were created.
	env.agent.chanMu.Lock()
	lockCount := len(env.agent.chanLocks)
	_, hasChan1 := env.agent.chanLocks["#chan1"]
	_, hasChan2 := env.agent.chanLocks["#chan2"]
	env.agent.chanMu.Unlock()

	if lockCount != 2 {
		t.Errorf("expected 2 channel locks, got %d", lockCount)
	}
	if !hasChan1 {
		t.Error("expected lock for #chan1")
	}
	if !hasChan2 {
		t.Error("expected lock for #chan2")
	}

	// Verify that same channel reuses the same lock.
	lock1a := env.agent.getChannelLock("#chan1")
	lock1b := env.agent.getChannelLock("#chan1")
	if lock1a != lock1b {
		t.Error("expected same lock for same channel")
	}
}

func TestAgent_ContextCancellation(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Mock always returns a tool call. The routeToolFunc succeeds, the loop
	// continues, and the context check on the next iteration stops the loop.
	env.mock.Responses = []*llm.ChatResponse{
		{
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call-cancel",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "shell",
						Arguments: `{}`,
					},
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Override tool routing to succeed, but cancel the context after the
	// first tool call so the next iteration's context check fires.
	var callCount atomic.Int32
	env.agent.routeToolFunc = func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
		if callCount.Add(1) >= 1 {
			cancel()
		}
		return "tool result", nil
	}

	// HandleMessage should return without blocking forever.
	done := make(chan struct{})
	go func() {
		env.agent.HandleMessage(ctx, "#test", "user1", "do something slow")
		close(done)
	}()

	select {
	case <-done:
		// Good — HandleMessage returned.
	case <-time.After(5 * time.Second):
		t.Fatal("HandleMessage did not return after context cancellation")
	}
}

func TestAgent_ModelSwitcherInterface(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Verify that Agent implements ModelSwitcher.
	var _ ModelSwitcher = env.agent
}

func TestAgent_NoProviders(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	memory := NewMemory(database, 100, 80, nil, logger)
	registry := NewRegistry(2*time.Minute, logger)
	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)

	agent := NewAgent(
		map[string]llm.Provider{},
		"",
		nil, // no server-side tools
		registry,
		memory,
		router,
		nil, // no approval manager
		nil, // no IRC connection
		"test prompt",
		"test-server",
		"#test-bus",
		100,
		0,   // cross-channel context disabled in tests
		nil, // no channel settings
		2*time.Second,
		2*time.Second,
		false,
		logger,
	)

	var sent []string
	agent.sendFunc = func(channel, message string) {
		sent = append(sent, message)
	}

	ctx := context.Background()
	agent.HandleMessage(ctx, "#test", "user1", "hello")

	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sent), sent)
	}
	if sent[0] != "error: no LLM provider available" {
		t.Errorf("sent = %q", sent[0])
	}
}

func TestLoadSystemPrompt_FromFile(t *testing.T) {
	t.Parallel()

	// Create a temp file with a custom prompt.
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(path, []byte("Custom system prompt"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	prompt, err := LoadSystemPrompt(path)
	if err != nil {
		t.Fatalf("LoadSystemPrompt: %v", err)
	}
	if prompt != "Custom system prompt" {
		t.Errorf("prompt = %q, want %q", prompt, "Custom system prompt")
	}
}

func TestLoadSystemPrompt_EmptyPath(t *testing.T) {
	t.Parallel()

	prompt, err := LoadSystemPrompt("")
	if err != nil {
		t.Fatalf("LoadSystemPrompt: %v", err)
	}
	if prompt != defaultSystemPrompt {
		t.Errorf("prompt = %q, want default", prompt)
	}
}

func TestLoadSystemPrompt_MissingFile(t *testing.T) {
	t.Parallel()

	prompt, err := LoadSystemPrompt("/nonexistent/path/prompt.txt")
	if err != nil {
		t.Fatalf("LoadSystemPrompt: %v", err)
	}
	if prompt != defaultSystemPrompt {
		t.Errorf("prompt = %q, want default for missing file", prompt)
	}
}

func TestLoadSystemPrompt_EmptyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	prompt, err := LoadSystemPrompt(path)
	if err != nil {
		t.Fatalf("LoadSystemPrompt: %v", err)
	}
	if prompt != defaultSystemPrompt {
		t.Errorf("prompt = %q, want default for empty file", prompt)
	}
}

func TestAgent_EmptyLLMResponse(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// LLM returns empty response (no content, no tool calls).
	env.mock.Responses = []*llm.ChatResponse{
		{Content: "", ToolCalls: nil},
	}

	ctx := context.Background()
	env.agent.HandleMessage(ctx, "#test", "user1", "hello")

	sent := env.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sent), sent)
	}
	if sent[0] != "I received an empty response from the LLM. Please try again." {
		t.Errorf("sent = %q", sent[0])
	}
}

func TestAgent_ToolsPassedToLLM(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	env.mock.Responses = []*llm.ChatResponse{
		{Content: "ok"},
	}

	ctx := context.Background()
	env.agent.HandleMessage(ctx, "#test", "user1", "hello")

	// Verify tools were passed to the LLM.
	if len(env.mock.Calls) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(env.mock.Calls))
	}
	req := env.mock.Calls[0]
	if len(req.Tools) == 0 {
		t.Error("expected tools to be passed to LLM, got none")
	}

	// Find the shell tool.
	found := false
	for _, tool := range req.Tools {
		if tool.Function.Name == "shell" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'shell' tool in LLM request")
	}
}

func TestReconstructToolCalls_RegularContent(t *testing.T) {
	t.Parallel()

	msg := llm.Message{Role: llm.RoleAssistant, Content: "Hello, how can I help?"}
	result := reconstructToolCalls(msg)
	if result.Content != "Hello, how can I help?" {
		t.Errorf("content = %q, want original text", result.Content)
	}
	if len(result.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(result.ToolCalls))
	}
}

func TestReconstructToolCalls_ToolCallsJSON(t *testing.T) {
	t.Parallel()

	toolCallsJSON := `[{"id":"call-1","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\"ls\"}"}}]`
	msg := llm.Message{Role: llm.RoleAssistant, Content: toolCallsJSON}
	result := reconstructToolCalls(msg)

	if result.Content != "" {
		t.Errorf("content should be empty after reconstruction, got %q", result.Content)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].ID != "call-1" {
		t.Errorf("tool call ID = %q, want %q", result.ToolCalls[0].ID, "call-1")
	}
	if result.ToolCalls[0].Function.Name != "shell" {
		t.Errorf("tool call name = %q, want %q", result.ToolCalls[0].Function.Name, "shell")
	}
}

func TestReconstructToolCalls_NonAssistantRole(t *testing.T) {
	t.Parallel()

	msg := llm.Message{Role: llm.RoleUser, Content: "some content"}
	result := reconstructToolCalls(msg)
	if result.Content != "some content" {
		t.Errorf("non-assistant message should be unchanged")
	}
}

func TestReconstructToolCalls_EmptyContent(t *testing.T) {
	t.Parallel()

	msg := llm.Message{Role: llm.RoleAssistant, Content: ""}
	result := reconstructToolCalls(msg)
	if result.Content != "" {
		t.Errorf("empty content should remain empty")
	}
}

func TestReconstructToolCalls_InvalidJSON(t *testing.T) {
	t.Parallel()

	msg := llm.Message{Role: llm.RoleAssistant, Content: "not json at all"}
	result := reconstructToolCalls(msg)
	if result.Content != "not json at all" {
		t.Errorf("invalid JSON content should be unchanged")
	}
}

func TestReconstructToolCalls_EmptyArray(t *testing.T) {
	t.Parallel()

	msg := llm.Message{Role: llm.RoleAssistant, Content: "[]"}
	result := reconstructToolCalls(msg)
	// Empty array should not be treated as tool calls.
	if result.Content != "[]" {
		t.Errorf("empty array should be unchanged, got content=%q", result.Content)
	}
}

func TestReconstructToolCalls_MalformedToolCall(t *testing.T) {
	t.Parallel()

	// Valid JSON array but not tool calls (missing required fields).
	msg := llm.Message{Role: llm.RoleAssistant, Content: `[{"foo":"bar"}]`}
	result := reconstructToolCalls(msg)
	// Should be treated as regular content since it doesn't look like tool calls.
	if result.Content != `[{"foo":"bar"}]` {
		t.Errorf("malformed tool call should be unchanged, got content=%q", result.Content)
	}
}

func TestReconstructToolCalls_EnvelopeFormat(t *testing.T) {
	t.Parallel()

	// New envelope format with reasoning_content.
	envelopeJSON := `{"tool_calls":[{"id":"call-1","type":"function","function":{"name":"shell","arguments":"{}"}}],"reasoning_content":"I need to run a shell command"}`
	msg := llm.Message{Role: llm.RoleAssistant, Content: envelopeJSON}
	result := reconstructToolCalls(msg)

	if result.Content != "" {
		t.Errorf("content should be empty, got %q", result.Content)
	}
	if result.ReasoningContent != "I need to run a shell command" {
		t.Errorf("reasoning_content = %q, want %q", result.ReasoningContent, "I need to run a shell command")
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Function.Name != "shell" {
		t.Errorf("tool call name = %q, want %q", result.ToolCalls[0].Function.Name, "shell")
	}
}

func TestReconstructToolCalls_EnvelopeEmptyReasoning(t *testing.T) {
	t.Parallel()

	// Envelope format with empty reasoning_content (from non-reasoning provider).
	envelopeJSON := `{"tool_calls":[{"id":"call-1","type":"function","function":{"name":"dns_check","arguments":"{}"}}]}`
	msg := llm.Message{Role: llm.RoleAssistant, Content: envelopeJSON}
	result := reconstructToolCalls(msg)

	if result.Content != "" {
		t.Errorf("content should be empty, got %q", result.Content)
	}
	if result.ReasoningContent != "" {
		t.Errorf("reasoning_content should be empty, got %q", result.ReasoningContent)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
}

func TestReconstructToolCalls_LegacyBareArray(t *testing.T) {
	t.Parallel()

	// Legacy format: bare tool calls array (pre-reasoning support).
	legacyJSON := `[{"id":"call-1","type":"function","function":{"name":"shell","arguments":"{}"}}]`
	msg := llm.Message{Role: llm.RoleAssistant, Content: legacyJSON}
	result := reconstructToolCalls(msg)

	if result.Content != "" {
		t.Errorf("content should be empty, got %q", result.Content)
	}
	if result.ReasoningContent != "" {
		t.Errorf("reasoning_content should be empty for legacy format, got %q", result.ReasoningContent)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
}

func TestAgent_ServerToolExecution(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	memory := NewMemory(database, 100, 80, nil, logger)
	registry := NewRegistry(2*time.Minute, logger)
	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)

	// Create a server-side tool registry with a "note_get" tool.
	serverTools := NewToolRegistry()
	if err := serverTools.Register(tools.Tool{
		Name:        "note_get",
		Description: "Get a note by key",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}`),
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			key, _ := args["key"].(string)
			return "note value for " + key, nil
		},
	}); err != nil {
		t.Fatalf("Register server tool: %v", err)
	}

	mock := &llm.MockProvider{NameVal: "test-provider"}
	providers := map[string]llm.Provider{"test-provider": mock}

	// LLM calls the server-side tool, then returns text.
	mock.Responses = []*llm.ChatResponse{
		{
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call-note",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "note_get",
						Arguments: `{"key":"todo"}`,
					},
				},
			},
		},
		{Content: "Your note says: note value for todo"},
	}

	agent := NewAgent(
		providers,
		"test-provider",
		serverTools,
		registry,
		memory,
		router,
		nil, // no approval manager
		nil, // no IRC connection
		"You are a test assistant.",
		"test-server",
		"#test-bus",
		100,
		0,   // cross-channel context disabled in tests
		nil, // no channel settings
		2*time.Second,
		2*time.Second,
		false,
		logger,
	)

	var sent []string
	var sentMu sync.Mutex
	agent.sendFunc = func(_, message string) {
		sentMu.Lock()
		sent = append(sent, message)
		sentMu.Unlock()
	}

	// Do NOT set routeToolFunc — let the real routing logic run so it
	// hits the server tool path.

	ctx := context.Background()
	agent.HandleMessage(ctx, "#test", "user1", "get my todo note")

	sentMu.Lock()
	sentCopy := make([]string, len(sent))
	copy(sentCopy, sent)
	sentMu.Unlock()

	if len(sentCopy) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sentCopy), sentCopy)
	}
	if sentCopy[0] != "Your note says: note value for todo" {
		t.Errorf("sent = %q", sentCopy[0])
	}

	// Verify the tool result was stored in memory.
	msgs, err := memory.GetHistory("#test", 20)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	// user, assistant (tool call), tool (result), assistant (text)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages in memory, got %d", len(msgs))
	}
	if msgs[2].Role != "tool" {
		t.Errorf("msg[2].Role = %q, want tool", msgs[2].Role)
	}
	if msgs[2].Content != "note value for todo" {
		t.Errorf("msg[2].Content = %q, want %q", msgs[2].Content, "note value for todo")
	}
}

func TestAgent_ServerToolPriority(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	memory := NewMemory(database, 100, 80, nil, logger)
	registry := NewRegistry(2*time.Minute, logger)

	// Register a CLIENT tool named "overlap_tool".
	registry.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "test-client",
		Hostname: "test-host",
		Tools: []bus.ToolDef{
			{Name: "overlap_tool", Description: "Client version", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
	})

	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)

	// Register a SERVER tool with the same name.
	serverTools := NewToolRegistry()
	if err := serverTools.Register(tools.Tool{
		Name:        "overlap_tool",
		Description: "Server version",
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "server handled", nil
		},
	}); err != nil {
		t.Fatalf("Register server tool: %v", err)
	}

	mock := &llm.MockProvider{NameVal: "test-provider"}
	providers := map[string]llm.Provider{"test-provider": mock}

	mock.Responses = []*llm.ChatResponse{
		{
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call-overlap",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "overlap_tool",
						Arguments: `{}`,
					},
				},
			},
		},
		{Content: "done"},
	}

	agent := NewAgent(
		providers,
		"test-provider",
		serverTools,
		registry,
		memory,
		router,
		nil, // no approval manager
		nil, // no IRC connection
		"You are a test assistant.",
		"test-server",
		"#test-bus",
		100,
		0,   // cross-channel context disabled in tests
		nil, // no channel settings
		2*time.Second,
		2*time.Second,
		false,
		logger,
	)

	var sent []string
	var sentMu sync.Mutex
	agent.sendFunc = func(_, message string) {
		sentMu.Lock()
		sent = append(sent, message)
		sentMu.Unlock()
	}

	ctx := context.Background()
	agent.HandleMessage(ctx, "#test", "user1", "use overlap tool")

	// Verify the server tool was called (not the client tool).
	msgs, err := memory.GetHistory("#test", 20)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	// user, assistant (tool call), tool (result), assistant (text)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
	if msgs[2].Content != "server handled" {
		t.Errorf("tool result = %q, want %q (server tool should take priority)", msgs[2].Content, "server handled")
	}
}

func TestAgent_ApprovalFlow_Auto(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	memory := NewMemory(database, 100, 80, nil, logger)
	registry := NewRegistry(2*time.Minute, logger)

	// Register a client with "auto" autonomy.
	registry.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "auto-client",
		Hostname: "test-host",
		Autonomy: "auto",
		Tools: []bus.ToolDef{
			{Name: "shell", Description: "Run commands", Parameters: json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string"}}}`)},
		},
	})

	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)
	approvals := NewApprovalManager(logger)

	mock := &llm.MockProvider{NameVal: "test-provider"}
	providers := map[string]llm.Provider{"test-provider": mock}

	mock.Responses = []*llm.ChatResponse{
		{
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call-auto",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "shell",
						Arguments: `{"cmd":"ls"}`,
					},
				},
			},
		},
		{Content: "Files listed."},
	}

	agent := NewAgent(
		providers,
		"test-provider",
		nil, // no server-side tools
		registry,
		memory,
		router,
		approvals,
		nil, // no IRC connection
		"You are a test assistant.",
		"test-server",
		"#test-bus",
		100,
		0,   // cross-channel context disabled in tests
		nil, // no channel settings
		2*time.Second,
		2*time.Second,
		false,
		logger,
	)

	// Override tool routing to return a canned result (simulates bus routing).
	agent.routeToolFunc = func(_ context.Context, toolName string, _ json.RawMessage) (string, error) {
		return "file1.txt", nil
	}

	var sent []string
	var sentMu sync.Mutex
	agent.sendFunc = func(_, message string) {
		sentMu.Lock()
		sent = append(sent, message)
		sentMu.Unlock()
	}

	ctx := context.Background()
	agent.HandleMessage(ctx, "#test", "user1", "list files")

	sentMu.Lock()
	sentCopy := make([]string, len(sent))
	copy(sentCopy, sent)
	sentMu.Unlock()

	// Auto autonomy — tool should execute immediately, no approval message.
	if len(sentCopy) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sentCopy), sentCopy)
	}
	if sentCopy[0] != "Files listed." {
		t.Errorf("sent = %q, want %q", sentCopy[0], "Files listed.")
	}

	// No pending approvals should exist.
	pending := approvals.GetPending("#test")
	if len(pending) != 0 {
		t.Errorf("expected 0 pending approvals, got %d", len(pending))
	}
}

func TestAgent_ApprovalFlow_Report(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	memory := NewMemory(database, 100, 80, nil, logger)
	registry := NewRegistry(2*time.Minute, logger)

	// Register a client with "report" autonomy.
	registry.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "report-client",
		Hostname: "test-host",
		Autonomy: "report",
		Tools: []bus.ToolDef{
			{Name: "shell", Description: "Run commands", Parameters: json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string"}}}`)},
		},
	})

	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)
	approvals := NewApprovalManager(logger)

	mock := &llm.MockProvider{NameVal: "test-provider"}
	providers := map[string]llm.Provider{"test-provider": mock}

	// LLM tries to call the tool, gets error, then responds with text.
	mock.Responses = []*llm.ChatResponse{
		{
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call-report",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "shell",
						Arguments: `{"cmd":"ls"}`,
					},
				},
			},
		},
		{Content: "I can't execute that tool."},
	}

	agent := NewAgent(
		providers,
		"test-provider",
		nil, // no server-side tools
		registry,
		memory,
		router,
		approvals,
		nil, // no IRC connection
		"You are a test assistant.",
		"test-server",
		"#test-bus",
		100,
		0,   // cross-channel context disabled in tests
		nil, // no channel settings
		2*time.Second,
		2*time.Second,
		false,
		logger,
	)

	// Do NOT set routeToolFunc — let the real routing logic run so it
	// hits the approval gate. The tool call should be rejected before
	// reaching the router.

	var sent []string
	var sentMu sync.Mutex
	agent.sendFunc = func(_, message string) {
		sentMu.Lock()
		sent = append(sent, message)
		sentMu.Unlock()
	}

	ctx := context.Background()
	agent.HandleMessage(ctx, "#test", "user1", "list files")

	sentMu.Lock()
	sentCopy := make([]string, len(sent))
	copy(sentCopy, sent)
	sentMu.Unlock()

	// Report autonomy — tool should be rejected, LLM gets error and responds.
	if len(sentCopy) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sentCopy), sentCopy)
	}
	if sentCopy[0] != "I can't execute that tool." {
		t.Errorf("sent = %q, want %q", sentCopy[0], "I can't execute that tool.")
	}

	// Verify the error was stored as a tool result in memory.
	msgs, err := memory.GetHistory("#test", 20)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	// user, assistant (tool call), tool (error), assistant (text)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
	if msgs[2].Role != "tool" {
		t.Errorf("msg[2].Role = %q, want tool", msgs[2].Role)
	}
	if len(msgs[2].Content) < 6 || msgs[2].Content[:6] != "error:" {
		t.Errorf("msg[2].Content = %q, expected error prefix", msgs[2].Content)
	}
}

func TestAgent_ApprovalFlow_Approve(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	memory := NewMemory(database, 100, 80, nil, logger)
	registry := NewRegistry(2*time.Minute, logger)

	// Register a client with "approve" autonomy.
	registry.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "approve-client",
		Hostname: "test-host",
		Autonomy: "approve",
		Tools: []bus.ToolDef{
			{Name: "shell", Description: "Run commands", Parameters: json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string"}}}`)},
		},
	})

	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)
	approvals := NewApprovalManager(logger)

	mock := &llm.MockProvider{NameVal: "test-provider"}
	providers := map[string]llm.Provider{"test-provider": mock}

	mock.Responses = []*llm.ChatResponse{
		{
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call-approve",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "shell",
						Arguments: `{"cmd":"ls"}`,
					},
				},
			},
		},
		{Content: "Files listed after approval."},
	}

	agent := NewAgent(
		providers,
		"test-provider",
		nil, // no server-side tools
		registry,
		memory,
		router,
		approvals,
		nil, // no IRC connection
		"You are a test assistant.",
		"test-server",
		"#test-bus",
		100,
		0,   // cross-channel context disabled in tests
		nil, // no channel settings
		2*time.Second,
		5*time.Second, // longer approval timeout for this test
		false,
		logger,
	)

	// Override tool routing to return a canned result.
	agent.routeToolFunc = func(_ context.Context, toolName string, _ json.RawMessage) (string, error) {
		return "file1.txt", nil
	}

	var sent []string
	var sentMu sync.Mutex
	agent.sendFunc = func(_, message string) {
		sentMu.Lock()
		sent = append(sent, message)
		sentMu.Unlock()
	}

	// Run HandleMessage in a goroutine since it will block waiting for approval.
	done := make(chan struct{})
	go func() {
		ctx := context.Background()
		agent.HandleMessage(ctx, "#test", "user1", "list files")
		close(done)
	}()

	// Wait briefly for the approval request to be created.
	time.Sleep(100 * time.Millisecond)

	// Verify an approval request was sent to IRC.
	sentMu.Lock()
	sentCopy := make([]string, len(sent))
	copy(sentCopy, sent)
	sentMu.Unlock()

	if len(sentCopy) < 1 {
		t.Fatal("expected at least 1 sent message (approval request)")
	}

	// Find and resolve the pending approval.
	pending := approvals.GetPending("#test")
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(pending))
	}

	if err := approvals.Resolve(pending[0].ID, true); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Wait for HandleMessage to complete.
	select {
	case <-done:
		// Good.
	case <-time.After(5 * time.Second):
		t.Fatal("HandleMessage did not return after approval")
	}

	// Verify the final response was sent.
	sentMu.Lock()
	sentCopy = make([]string, len(sent))
	copy(sentCopy, sent)
	sentMu.Unlock()

	// Should have: approval request message + final LLM response.
	if len(sentCopy) != 2 {
		t.Fatalf("expected 2 sent messages, got %d: %v", len(sentCopy), sentCopy)
	}
	if sentCopy[1] != "Files listed after approval." {
		t.Errorf("sent[1] = %q, want %q", sentCopy[1], "Files listed after approval.")
	}
}

func TestAgent_BuildSystemPrompt_ActiveModel(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Default: global default provider, no per-channel override.
	prompt := env.agent.buildSystemPrompt("#test")
	if !strings.Contains(prompt, "Active model: test-provider (global default)") {
		t.Errorf("prompt should show global default model, got %q", prompt)
	}
}

func TestAgent_BuildSystemPrompt_ChannelSpecificModel(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	memory := NewMemory(database, 100, 80, nil, logger)
	registry := NewRegistry(2*time.Minute, logger)
	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)
	channelSettings := NewChannelSettingsStore(database, logger)

	mock := &llm.MockProvider{NameVal: "default-provider"}
	other := &llm.MockProvider{NameVal: "kimi"}
	providers := map[string]llm.Provider{
		"default-provider": mock,
		"kimi":             other,
	}

	agent := NewAgent(
		providers,
		"default-provider",
		nil,
		registry,
		memory,
		router,
		nil,
		nil,
		"You are a test assistant.",
		"test-server",
		"#test-bus",
		100,
		0,
		channelSettings,
		2*time.Second,
		2*time.Second,
		false,
		logger,
	)

	// Set per-channel override.
	if err := agent.SetProvider("#chan1", "kimi"); err != nil {
		t.Fatalf("SetProvider: %v", err)
	}

	// Channel with override should show "channel-specific".
	prompt := agent.buildSystemPrompt("#chan1")
	if !strings.Contains(prompt, "Active model: kimi (channel-specific)") {
		t.Errorf("prompt should show channel-specific model, got %q", prompt)
	}

	// Channel without override should show "global default".
	prompt = agent.buildSystemPrompt("#chan2")
	if !strings.Contains(prompt, "Active model: default-provider (global default)") {
		t.Errorf("prompt should show global default model for #chan2, got %q", prompt)
	}
}

func TestAgent_SyncChannelTopic_NilConn(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// conn is nil in default test env — syncChannelTopic should be a no-op.
	env.agent.syncChannelTopic("#test")

	// Verify no topics were tracked (no-op path).
	env.agent.topicMu.Lock()
	topicCount := len(env.agent.lastTopics)
	env.agent.topicMu.Unlock()

	if topicCount != 0 {
		t.Errorf("expected 0 tracked topics with nil conn, got %d", topicCount)
	}
}

func TestAgent_SyncAllTopics_NilConn(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// conn is nil — SyncAllTopics should be a no-op without panicking.
	env.agent.SyncAllTopics()
}

func TestAgent_SetProvider_SyncsTopicOnNilConn(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Add a second provider.
	env.agent.providers["kimi"] = &llm.MockProvider{NameVal: "kimi"}

	// SetProvider should succeed and call syncChannelTopic (which is a no-op
	// because conn is nil). No panic should occur.
	if err := env.agent.SetProvider("#test", "kimi"); err != nil {
		t.Fatalf("SetProvider: %v", err)
	}

	// Reset to default should also not panic.
	if err := env.agent.SetProvider("#test", "default"); err != nil {
		t.Fatalf("SetProvider default: %v", err)
	}
}

func TestAgent_SyncChannelTopic_SkipsBusChannel(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	memory := NewMemory(database, 100, 80, nil, logger)
	registry := NewRegistry(2*time.Minute, logger)
	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)

	mock := &llm.MockProvider{NameVal: "test-provider"}
	providers := map[string]llm.Provider{"test-provider": mock}

	agent := NewAgent(
		providers,
		"test-provider",
		nil,
		registry,
		memory,
		router,
		nil,
		nil, // no IRC connection
		"test prompt",
		"test-server",
		"#test-bus",
		100,
		0,
		nil,
		2*time.Second,
		2*time.Second,
		false,
		logger,
	)

	// Even though conn is nil (so it's a no-op anyway), verify the bus
	// channel guard works by checking that lastTopics is not updated.
	agent.syncChannelTopic("#test-bus")

	agent.topicMu.Lock()
	_, hasBus := agent.lastTopics["#test-bus"]
	agent.topicMu.Unlock()

	if hasBus {
		t.Error("syncChannelTopic should skip the bus channel")
	}
}

func TestAgent_HandleEvent(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	env.mock.Responses = []*llm.ChatResponse{
		{Content: "I see the backup completed successfully."},
	}

	ctx := context.Background()
	err := env.agent.HandleEvent(ctx, "#test", "backup-script", "backup.completed", "Backup finished", `{"size":"1.2GB"}`)
	if err != nil {
		t.Fatalf("HandleEvent error: %v", err)
	}

	sent := env.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sent), sent)
	}
	if sent[0] != "I see the backup completed successfully." {
		t.Errorf("sent = %q", sent[0])
	}

	// Verify the event was stored as a system message in memory.
	msgs, err := env.memory.GetHistory("#test", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages in memory, got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("msg[0].Role = %q, want system", msgs[0].Role)
	}
	wantContent := "[Event from backup-script] backup.completed: Backup finished\n{\"size\":\"1.2GB\"}"
	if msgs[0].Content != wantContent {
		t.Errorf("msg[0].Content = %q, want %q", msgs[0].Content, wantContent)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("msg[1].Role = %q, want assistant", msgs[1].Role)
	}

	// Verify the LLM received the event in context.
	if len(env.mock.Calls) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(env.mock.Calls))
	}
	req := env.mock.Calls[0]
	// Messages: system prompt, event (system), so at least 2.
	if len(req.Messages) < 2 {
		t.Fatalf("expected at least 2 messages in LLM request, got %d", len(req.Messages))
	}
}

func TestAgent_HandleEvent_NoData(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	env.mock.Responses = []*llm.ChatResponse{
		{Content: "Noted."},
	}

	ctx := context.Background()
	err := env.agent.HandleEvent(ctx, "#test", "cron", "job.done", "Cron job finished", "")
	if err != nil {
		t.Fatalf("HandleEvent error: %v", err)
	}

	// Verify the event was stored without trailing newline.
	msgs, err := env.memory.GetHistory("#test", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) < 1 {
		t.Fatal("expected at least 1 message in memory")
	}
	wantContent := "[Event from cron] job.done: Cron job finished"
	if msgs[0].Content != wantContent {
		t.Errorf("msg[0].Content = %q, want %q", msgs[0].Content, wantContent)
	}
}

func TestAgent_ExecuteTool_ServerTool(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	memory := NewMemory(database, 100, 80, nil, logger)
	registry := NewRegistry(2*time.Minute, logger)
	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)

	// Register a server-side tool.
	serverTools := NewToolRegistry()
	if err := serverTools.Register(tools.Tool{
		Name:        "note_get",
		Description: "Get a note",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}}}`),
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			key, _ := args["key"].(string)
			return "value for " + key, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	mock := &llm.MockProvider{NameVal: "test-provider"}
	providers := map[string]llm.Provider{"test-provider": mock}

	agent := NewAgent(
		providers, "test-provider", serverTools, registry, memory, router,
		nil, nil, "test", "test-server", "#test-bus", 100, 0, nil,
		2*time.Second, 2*time.Second, false, logger,
	)

	ctx := context.Background()
	result, err := agent.ExecuteTool(ctx, "note_get", map[string]any{"key": "todo"})
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if result != "value for todo" {
		t.Errorf("result = %q, want %q", result, "value for todo")
	}
}

func TestAgent_ExecuteTool_BusTool(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	memory := NewMemory(database, 100, 80, nil, logger)
	registry := NewRegistry(2*time.Minute, logger)
	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)

	mock := &llm.MockProvider{NameVal: "test-provider"}
	providers := map[string]llm.Provider{"test-provider": mock}

	agent := NewAgent(
		providers, "test-provider", nil, registry, memory, router,
		nil, nil, "test", "test-server", "#test-bus", 100, 0, nil,
		2*time.Second, 2*time.Second, false, logger,
	)

	// Override routeToolFunc to simulate bus routing.
	agent.routeToolFunc = func(_ context.Context, toolName string, args json.RawMessage) (string, error) {
		return fmt.Sprintf("bus result for %s", toolName), nil
	}

	ctx := context.Background()
	result, err := agent.ExecuteTool(ctx, "dns_lookup", map[string]any{"domain": "example.com"})
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if result != "bus result for dns_lookup" {
		t.Errorf("result = %q, want %q", result, "bus result for dns_lookup")
	}
}

func TestAgent_ExecuteTool_ImplementsInterface(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Verify that Agent implements ToolExecutor.
	var _ ToolExecutor = env.agent
}

func TestAgent_ToolFailureCircuitBreaker(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// LLM keeps calling the same failing tool. After maxConsecutiveToolFailures
	// (2), the tool should be removed from the available tools list and the
	// LLM should receive a text-only response on the next iteration.
	failingToolCall := &llm.ChatResponse{
		ToolCalls: []llm.ToolCall{
			{
				ID:   "call-fail",
				Type: "function",
				Function: llm.FunctionCall{
					Name:      "broken_tool",
					Arguments: `{}`,
				},
			},
		},
	}

	// Queue: 2 failing tool calls, then a text response (the LLM should
	// stop calling the tool after seeing the circuit breaker message).
	env.mock.Responses = []*llm.ChatResponse{
		failingToolCall,
		failingToolCall,
		{Content: "I was unable to use that tool. Here's what I know instead."},
	}

	// Override tool routing to always fail.
	env.agent.routeToolFunc = func(_ context.Context, toolName string, _ json.RawMessage) (string, error) {
		return "", fmt.Errorf("tool %q: connection refused", toolName)
	}

	ctx := context.Background()
	env.agent.HandleMessage(ctx, "#test", "user1", "use broken tool")

	sent := env.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sent), sent)
	}
	if sent[0] != "I was unable to use that tool. Here's what I know instead." {
		t.Errorf("sent = %q", sent[0])
	}

	// Verify the LLM was called 3 times: 2 with the failing tool, 1 without.
	if len(env.mock.Calls) != 3 {
		t.Fatalf("expected 3 LLM calls, got %d", len(env.mock.Calls))
	}

	// Verify the 3rd LLM call does NOT include "broken_tool" in its tools.
	thirdCall := env.mock.Calls[2]
	for _, tool := range thirdCall.Tools {
		if tool.Function.Name == "broken_tool" {
			t.Error("broken_tool should have been removed from tools after circuit breaker triggered")
		}
	}

	// Verify the circuit breaker message was appended to the tool result.
	msgs, err := env.memory.GetHistory("#test", 20)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	// Find the second tool error result (the one with the circuit breaker hint).
	var foundCircuitBreaker bool
	for _, msg := range msgs {
		if msg.Role == "tool" && strings.Contains(msg.Content, "[SYSTEM:") && strings.Contains(msg.Content, "unavailable") {
			foundCircuitBreaker = true
			break
		}
	}
	if !foundCircuitBreaker {
		t.Error("expected circuit breaker hint in tool result message")
	}
}

func TestAgent_ToolFailureCircuitBreaker_ResetOnSuccess(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Tool fails once, then succeeds. The failure counter should reset.
	callCount := 0
	env.mock.Responses = []*llm.ChatResponse{
		// First call: tool fails.
		{
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call-1",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "flaky_tool",
						Arguments: `{}`,
					},
				},
			},
		},
		// Second call: tool succeeds.
		{
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call-2",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "flaky_tool",
						Arguments: `{}`,
					},
				},
			},
		},
		{Content: "Got it."},
	}

	env.agent.routeToolFunc = func(_ context.Context, toolName string, _ json.RawMessage) (string, error) {
		callCount++
		if callCount == 1 {
			return "", fmt.Errorf("temporary failure")
		}
		return "success", nil
	}

	ctx := context.Background()
	env.agent.HandleMessage(ctx, "#test", "user1", "use flaky tool")

	sent := env.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sent), sent)
	}
	if sent[0] != "Got it." {
		t.Errorf("sent = %q", sent[0])
	}

	// Verify the tool was called twice (not circuit-broken).
	if callCount != 2 {
		t.Errorf("expected 2 tool calls, got %d", callCount)
	}

	// Verify the LLM was called 3 times and the tool was still available
	// in the 2nd call (failure count reset after success).
	if len(env.mock.Calls) != 3 {
		t.Fatalf("expected 3 LLM calls, got %d", len(env.mock.Calls))
	}
}

func TestAgent_BuildSystemPrompt_DM(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	memory := NewMemory(database, 100, 80, nil, logger)
	registry := NewRegistry(2*time.Minute, logger)
	sender := bus.NewSender(nil, "#murmur-bus", "", 0, logger)
	router := NewRouter(registry, sender, logger)

	mock := &llm.MockProvider{NameVal: "test-provider"}
	providers := map[string]llm.Provider{"test-provider": mock}

	// Create a real IRC connection config to test IsChannel.
	conn, err := irc.NewConnection(config.IRCConfig{
		Server: "localhost",
		Port:   6667,
		Nick:   "murmur",
	}, []string{"#murmur"}, logger)
	if err != nil {
		t.Fatalf("NewConnection: %v", err)
	}

	agent := NewAgent(
		providers,
		"test-provider",
		nil,
		registry,
		memory,
		router,
		nil,
		conn,
		"You are a test assistant.",
		"test-server",
		"#test-bus",
		100,
		3, // cross-channel context enabled
		nil,
		2*time.Second,
		2*time.Second,
		false,
		logger,
	)

	// Test DM prompt: channel is a nick (no '#' prefix).
	prompt := agent.buildSystemPrompt("alice")
	if !strings.Contains(prompt, "private conversation (DM) with alice") {
		t.Errorf("DM prompt should mention private conversation, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "Current channel:") {
		t.Error("DM prompt should NOT contain 'Current channel:'")
	}
	if strings.Contains(prompt, "Other Channel Activity") {
		t.Error("DM prompt should NOT contain cross-channel context")
	}

	// Test channel prompt: channel starts with '#'.
	prompt = agent.buildSystemPrompt("#general")
	if !strings.Contains(prompt, "Current channel: #general") {
		t.Errorf("channel prompt should contain 'Current channel:', got:\n%s", prompt)
	}
	if strings.Contains(prompt, "private conversation") {
		t.Error("channel prompt should NOT mention private conversation")
	}
}

func TestAgent_HandleMessage_DM(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	env.mock.Responses = []*llm.ChatResponse{
		{Content: "Hello from DM!"},
	}

	ctx := context.Background()
	// Simulate a DM: channel is the user's nick (already swapped by handler).
	env.agent.HandleMessage(ctx, "alice", "alice", "hello in DM")

	sent := env.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sent), sent)
	}
	if sent[0] != "Hello from DM!" {
		t.Errorf("sent = %q, want %q", sent[0], "Hello from DM!")
	}

	// Verify messages are stored under the user's nick as channel key.
	msgs, err := env.memory.GetHistory("alice", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages in memory for DM, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("msg[0].Role = %q, want user", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("msg[1].Role = %q, want assistant", msgs[1].Role)
	}
}
