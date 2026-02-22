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
	"murmur/internal/llm/llmtest"
	"murmur/internal/tools"
)

// testAgentEnv holds all the components needed for agent tests.
type testAgentEnv struct {
	agent    *Agent
	registry *Registry
	memory   *Memory
	router   *Router
	mock     *llmtest.MockProvider
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

// addTestProvider adds a provider to the agent's atomic provider map. This is
// a test helper that performs the copy-on-write pattern required by the atomic
// pointer.
func addTestProvider(a *Agent, name string, p llm.Provider) {
	providers := a.loadProviders()
	newMap := make(map[string]llm.Provider, len(providers)+1)
	for k, v := range providers {
		newMap[k] = v
	}
	newMap[name] = p
	a.providers.Store(&newMap)
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

	mock := &llmtest.MockProvider{
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

	agent := NewAgent(AgentParams{
		Providers:       providers,
		DefaultProvider: "test-provider",
		Registry:        registry,
		Memory:          memory,
		Router:          router,
		SystemPrompt:    "You are a test assistant.",
		ServerName:      "test-server",
		BusChannel:      "#test-bus",
		MaxHistory:      100,
		ToolTimeout:     2 * time.Second,
		ApprovalTimeout: 2 * time.Second,
		Logger:          logger,
	})
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
	addTestProvider(env.agent, "other-provider", &llmtest.MockProvider{NameVal: "other-provider"})

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

	mock := &llmtest.MockProvider{NameVal: "default-provider"}
	other := &llmtest.MockProvider{NameVal: "other-provider"}
	providers := map[string]llm.Provider{
		"default-provider": mock,
		"other-provider":   other,
	}

	agent := NewAgent(AgentParams{
		Providers:       providers,
		DefaultProvider: "default-provider",
		Registry:        registry,
		Memory:          memory,
		Router:          router,
		SystemPrompt:    "test prompt",
		ServerName:      "test-server",
		BusChannel:      "#test-bus",
		MaxHistory:      100,
		ChannelSettings: channelSettings,
		ToolTimeout:     2 * time.Second,
		ApprovalTimeout: 2 * time.Second,
		Logger:          logger,
	})

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

	mock := &llmtest.MockProvider{NameVal: "default-provider"}
	other := &llmtest.MockProvider{NameVal: "other-provider"}
	providers := map[string]llm.Provider{
		"default-provider": mock,
		"other-provider":   other,
	}

	agent := NewAgent(AgentParams{
		Providers:       providers,
		DefaultProvider: "default-provider",
		Registry:        registry,
		Memory:          memory,
		Router:          router,
		SystemPrompt:    "test prompt",
		ServerName:      "test-server",
		BusChannel:      "#test-bus",
		MaxHistory:      100,
		ChannelSettings: channelSettings,
		ToolTimeout:     2 * time.Second,
		ApprovalTimeout: 2 * time.Second,
		Logger:          logger,
	})

	// No override — should resolve to global default.
	p, err := agent.resolveProvider("#chan1", "user1", nil)
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
	p, err = agent.resolveProvider("#chan1", "user1", nil)
	if err != nil {
		t.Fatalf("resolveProvider: %v", err)
	}
	if p.Name() != "other-provider" {
		t.Errorf("resolveProvider = %q, want %q", p.Name(), "other-provider")
	}

	// Other channel still resolves to default.
	p, err = agent.resolveProvider("#chan2", "user1", nil)
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
	p, err := env.agent.resolveProvider("#test", "user1", nil)
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

	mock := &llmtest.MockProvider{NameVal: "default-provider"}
	providers := map[string]llm.Provider{
		"default-provider": mock,
		// "removed-provider" is NOT in the providers map — simulates config removal.
	}

	agent := NewAgent(AgentParams{
		Providers:       providers,
		DefaultProvider: "default-provider",
		Registry:        registry,
		Memory:          memory,
		Router:          router,
		SystemPrompt:    "test prompt",
		ServerName:      "test-server",
		BusChannel:      "#test-bus",
		MaxHistory:      100,
		ChannelSettings: channelSettings,
		ToolTimeout:     2 * time.Second,
		ApprovalTimeout: 2 * time.Second,
		Logger:          logger,
	})

	// resolveProvider should fall back to global default when override is stale.
	p, err := agent.resolveProvider("#chan1", "user1", nil)
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
	addTestProvider(env.agent, "beta", &llmtest.MockProvider{NameVal: "beta"})
	addTestProvider(env.agent, "alpha", &llmtest.MockProvider{NameVal: "alpha"})

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

	agent := NewAgent(AgentParams{
		Providers:       map[string]llm.Provider{},
		Registry:        registry,
		Memory:          memory,
		Router:          router,
		SystemPrompt:    "test prompt",
		ServerName:      "test-server",
		BusChannel:      "#test-bus",
		MaxHistory:      100,
		ToolTimeout:     2 * time.Second,
		ApprovalTimeout: 2 * time.Second,
		Logger:          logger,
	})

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

	mock := &llmtest.MockProvider{NameVal: "test-provider"}
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

	agent := NewAgent(AgentParams{
		Providers:       providers,
		DefaultProvider: "test-provider",
		ServerTools:     serverTools,
		Registry:        registry,
		Memory:          memory,
		Router:          router,
		SystemPrompt:    "You are a test assistant.",
		ServerName:      "test-server",
		BusChannel:      "#test-bus",
		MaxHistory:      100,
		ToolTimeout:     2 * time.Second,
		ApprovalTimeout: 2 * time.Second,
		Logger:          logger,
	})

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

	mock := &llmtest.MockProvider{NameVal: "test-provider"}
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

	agent := NewAgent(AgentParams{
		Providers:       providers,
		DefaultProvider: "test-provider",
		ServerTools:     serverTools,
		Registry:        registry,
		Memory:          memory,
		Router:          router,
		SystemPrompt:    "You are a test assistant.",
		ServerName:      "test-server",
		BusChannel:      "#test-bus",
		MaxHistory:      100,
		ToolTimeout:     2 * time.Second,
		ApprovalTimeout: 2 * time.Second,
		Logger:          logger,
	})

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

	mock := &llmtest.MockProvider{NameVal: "test-provider"}
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

	agent := NewAgent(AgentParams{
		Providers:       providers,
		DefaultProvider: "test-provider",
		Registry:        registry,
		Memory:          memory,
		Router:          router,
		Approvals:       approvals,
		SystemPrompt:    "You are a test assistant.",
		ServerName:      "test-server",
		BusChannel:      "#test-bus",
		MaxHistory:      100,
		ToolTimeout:     2 * time.Second,
		ApprovalTimeout: 2 * time.Second,
		Logger:          logger,
	})

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

	mock := &llmtest.MockProvider{NameVal: "test-provider"}
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

	agent := NewAgent(AgentParams{
		Providers:       providers,
		DefaultProvider: "test-provider",
		Registry:        registry,
		Memory:          memory,
		Router:          router,
		Approvals:       approvals,
		SystemPrompt:    "You are a test assistant.",
		ServerName:      "test-server",
		BusChannel:      "#test-bus",
		MaxHistory:      100,
		ToolTimeout:     2 * time.Second,
		ApprovalTimeout: 2 * time.Second,
		Logger:          logger,
	})

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

	mock := &llmtest.MockProvider{NameVal: "test-provider"}
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

	agent := NewAgent(AgentParams{
		Providers:       providers,
		DefaultProvider: "test-provider",
		Registry:        registry,
		Memory:          memory,
		Router:          router,
		Approvals:       approvals,
		SystemPrompt:    "You are a test assistant.",
		ServerName:      "test-server",
		BusChannel:      "#test-bus",
		MaxHistory:      100,
		ToolTimeout:     2 * time.Second,
		ApprovalTimeout: 5 * time.Second, // longer approval timeout for this test
		Logger:          logger,
	})

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
	prompt := env.agent.buildSystemPrompt(context.Background(), "#test", env.agent.loadConfig())
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

	mock := &llmtest.MockProvider{NameVal: "default-provider"}
	other := &llmtest.MockProvider{NameVal: "kimi"}
	providers := map[string]llm.Provider{
		"default-provider": mock,
		"kimi":             other,
	}

	agent := NewAgent(AgentParams{
		Providers:       providers,
		DefaultProvider: "default-provider",
		Registry:        registry,
		Memory:          memory,
		Router:          router,
		SystemPrompt:    "You are a test assistant.",
		ServerName:      "test-server",
		BusChannel:      "#test-bus",
		MaxHistory:      100,
		ChannelSettings: channelSettings,
		ToolTimeout:     2 * time.Second,
		ApprovalTimeout: 2 * time.Second,
		Logger:          logger,
	})

	// Set per-channel override.
	if err := agent.SetProvider("#chan1", "kimi"); err != nil {
		t.Fatalf("SetProvider: %v", err)
	}

	// Channel with override should show "channel-specific".
	prompt := agent.buildSystemPrompt(context.Background(), "#chan1", agent.loadConfig())
	if !strings.Contains(prompt, "Active model: kimi (channel-specific)") {
		t.Errorf("prompt should show channel-specific model, got %q", prompt)
	}

	// Channel without override should show "global default".
	prompt = agent.buildSystemPrompt(context.Background(), "#chan2", agent.loadConfig())
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
	addTestProvider(env.agent, "kimi", &llmtest.MockProvider{NameVal: "kimi"})

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

	mock := &llmtest.MockProvider{NameVal: "test-provider"}
	providers := map[string]llm.Provider{"test-provider": mock}

	agent := NewAgent(AgentParams{
		Providers:       providers,
		DefaultProvider: "test-provider",
		Registry:        registry,
		Memory:          memory,
		Router:          router,
		SystemPrompt:    "test prompt",
		ServerName:      "test-server",
		BusChannel:      "#test-bus",
		MaxHistory:      100,
		ToolTimeout:     2 * time.Second,
		ApprovalTimeout: 2 * time.Second,
		Logger:          logger,
	})

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
	err := env.agent.HandleEvent(ctx, "#test", "_system", "backup-script", "backup.completed", "Backup finished", `{"size":"1.2GB"}`)
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

	// Verify only the assistant response was copied to the real channel
	// (event uses ephemeral context — the event system message is NOT in
	// the real channel's history).
	msgs, err := env.memory.GetHistory("#test", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message in real channel memory (copied response), got %d", len(msgs))
	}
	if msgs[0].Role != "assistant" {
		t.Errorf("msg[0].Role = %q, want assistant", msgs[0].Role)
	}
	if msgs[0].Content != "I see the backup completed successfully." {
		t.Errorf("msg[0].Content = %q", msgs[0].Content)
	}

	// Verify the LLM received the event in its ephemeral context.
	if len(env.mock.Calls) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(env.mock.Calls))
	}
	req := env.mock.Calls[0]
	// Messages: system prompt, event (system), so at least 2.
	if len(req.Messages) < 2 {
		t.Fatalf("expected at least 2 messages in LLM request, got %d", len(req.Messages))
	}
	// Verify the event content was in the LLM request.
	foundEvent := false
	for _, msg := range req.Messages {
		if msg.Role == llm.RoleSystem && strings.Contains(msg.Content, "[Event from backup-script]") {
			foundEvent = true
			break
		}
	}
	if !foundEvent {
		t.Error("LLM request should contain the event message")
	}
}

func TestAgent_HandleEvent_NoData(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	env.mock.Responses = []*llm.ChatResponse{
		{Content: "Noted."},
	}

	ctx := context.Background()
	err := env.agent.HandleEvent(ctx, "#test", "_system", "cron", "job.done", "Cron job finished", "")
	if err != nil {
		t.Fatalf("HandleEvent error: %v", err)
	}

	// Verify the LLM received the event without trailing newline.
	if len(env.mock.Calls) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(env.mock.Calls))
	}
	req := env.mock.Calls[0]
	wantContent := "[Event from cron] job.done: Cron job finished"
	foundEvent := false
	for _, msg := range req.Messages {
		if msg.Role == llm.RoleSystem && msg.Content == wantContent {
			foundEvent = true
			break
		}
	}
	if !foundEvent {
		t.Errorf("LLM request should contain event message %q", wantContent)
	}

	// Verify only the assistant response was copied to the real channel.
	msgs, err := env.memory.GetHistory("#test", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message in real channel memory, got %d", len(msgs))
	}
	if msgs[0].Content != "Noted." {
		t.Errorf("msg[0].Content = %q, want %q", msgs[0].Content, "Noted.")
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

	mock := &llmtest.MockProvider{NameVal: "test-provider"}
	providers := map[string]llm.Provider{"test-provider": mock}

	agent := NewAgent(AgentParams{
		Providers:       providers,
		DefaultProvider: "test-provider",
		ServerTools:     serverTools,
		Registry:        registry,
		Memory:          memory,
		Router:          router,
		SystemPrompt:    "test",
		ServerName:      "test-server",
		BusChannel:      "#test-bus",
		MaxHistory:      100,
		ToolTimeout:     2 * time.Second,
		ApprovalTimeout: 2 * time.Second,
		Logger:          logger,
	})

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

	mock := &llmtest.MockProvider{NameVal: "test-provider"}
	providers := map[string]llm.Provider{"test-provider": mock}

	agent := NewAgent(AgentParams{
		Providers:       providers,
		DefaultProvider: "test-provider",
		Registry:        registry,
		Memory:          memory,
		Router:          router,
		SystemPrompt:    "test",
		ServerName:      "test-server",
		BusChannel:      "#test-bus",
		MaxHistory:      100,
		ToolTimeout:     2 * time.Second,
		ApprovalTimeout: 2 * time.Second,
		Logger:          logger,
	})

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

	mock := &llmtest.MockProvider{NameVal: "test-provider"}
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

	agent := NewAgent(AgentParams{
		Providers:           providers,
		DefaultProvider:     "test-provider",
		Registry:            registry,
		Memory:              memory,
		Router:              router,
		Conn:                conn,
		SystemPrompt:        "You are a test assistant.",
		ServerName:          "test-server",
		BusChannel:          "#test-bus",
		MaxHistory:          100,
		CrossChannelContext: 3, // cross-channel context enabled
		ToolTimeout:         2 * time.Second,
		ApprovalTimeout:     2 * time.Second,
		Logger:              logger,
	})

	// Test DM prompt: channel is a nick (no '#' prefix).
	prompt := agent.buildSystemPrompt(context.Background(), "alice", agent.loadConfig())
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
	prompt = agent.buildSystemPrompt(context.Background(), "#general", agent.loadConfig())
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

func TestAgent_UpdateProviders(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Initial state: one provider "test-provider".
	names := env.agent.GetProviderNames()
	if len(names) != 1 || names[0] != "test-provider" {
		t.Fatalf("initial providers = %v, want [test-provider]", names)
	}
	if got := env.agent.GetProvider(); got != "test-provider" {
		t.Fatalf("initial default = %q, want test-provider", got)
	}

	// Swap to a completely new set of providers.
	newMock := &llmtest.MockProvider{NameVal: "new-provider"}
	newProviders := map[string]llm.Provider{
		"new-provider": newMock,
		"extra":        &llmtest.MockProvider{NameVal: "extra"},
	}
	env.agent.UpdateProviders(newProviders, "new-provider", nil)

	// Verify the swap took effect.
	names = env.agent.GetProviderNames()
	if len(names) != 2 {
		t.Fatalf("after swap providers = %v, want 2 entries", names)
	}
	if got := env.agent.GetProvider(); got != "new-provider" {
		t.Errorf("after swap default = %q, want new-provider", got)
	}

	// Verify the old provider is gone.
	if err := env.agent.SetProvider("#test", "test-provider"); err == nil {
		t.Error("expected error for removed provider test-provider")
	}

	// Verify the new provider works.
	if err := env.agent.SetProvider("#test", "extra"); err != nil {
		t.Errorf("SetProvider extra: %v", err)
	}
}

func TestAgent_UpdateConfig(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Verify initial values.
	cfg := env.agent.loadConfig()
	if cfg.verbose {
		t.Error("expected verbose=false initially")
	}
	if cfg.maxHistory != 100 {
		t.Errorf("expected maxHistory=100, got %d", cfg.maxHistory)
	}

	// Update config.
	env.agent.UpdateConfig(true, 50, 5, 30*time.Second, "New system prompt", config.DebugConfig{})

	// Verify updated values.
	cfg = env.agent.loadConfig()
	if !cfg.verbose {
		t.Error("expected verbose=true after update")
	}
	if cfg.maxHistory != 50 {
		t.Errorf("expected maxHistory=50, got %d", cfg.maxHistory)
	}
	if cfg.crossChCtx != 5 {
		t.Errorf("expected crossChCtx=5, got %d", cfg.crossChCtx)
	}
	if cfg.approvalTimeout != 30*time.Second {
		t.Errorf("expected approvalTimeout=30s, got %v", cfg.approvalTimeout)
	}
	if cfg.systemPrompt != "New system prompt" {
		t.Errorf("expected systemPrompt='New system prompt', got %q", cfg.systemPrompt)
	}
}

func TestMemory_UpdateConfig(t *testing.T) {
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

	// Verify initial values.
	if got := memory.loadMaxHistory(); got != 100 {
		t.Errorf("initial maxHistory = %d, want 100", got)
	}
	if got := memory.loadSummaryThreshold(); got != 80 {
		t.Errorf("initial summaryThreshold = %d, want 80", got)
	}

	// Update.
	memory.UpdateConfig(200, 160)

	if got := memory.loadMaxHistory(); got != 200 {
		t.Errorf("updated maxHistory = %d, want 200", got)
	}
	if got := memory.loadSummaryThreshold(); got != 160 {
		t.Errorf("updated summaryThreshold = %d, want 160", got)
	}
}

func TestRunScheduledTask_IsolatedContext(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Simulate prior channel conversation that should NOT be visible to the task.
	if err := env.memory.AddMessage("#test", llm.RoleUser, "alice: What's the weather?", "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := env.memory.AddMessage("#test", llm.RoleAssistant, "It's sunny today!", "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// The LLM should only see the task instruction, not the prior conversation.
	env.mock.Responses = []*llm.ChatResponse{
		{Content: "Task completed: health check passed."},
	}

	ctx := context.Background()
	env.agent.RunScheduledTask(ctx, 42, "#test", "Run health check", "alice", "")

	// Verify the LLM was called.
	if len(env.mock.Calls) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(env.mock.Calls))
	}

	// Verify the LLM request does NOT contain the prior conversation.
	req := env.mock.Calls[0]
	for _, msg := range req.Messages {
		if msg.Role == llm.RoleUser && strings.Contains(msg.Content, "weather") {
			t.Error("task LLM request should NOT contain prior channel conversation about weather")
		}
	}

	// Verify the task instruction IS present.
	foundTask := false
	for _, msg := range req.Messages {
		if msg.Role == llm.RoleSystem && strings.Contains(msg.Content, "[Scheduled Task] Run health check") {
			foundTask = true
			break
		}
	}
	if !foundTask {
		t.Error("task LLM request should contain the scheduled task instruction")
	}

	// Verify the system prompt contains the task mode instruction.
	systemPrompt := req.Messages[0].Content
	if !strings.Contains(systemPrompt, "Task Mode") {
		t.Error("system prompt should contain Task Mode section for scheduled tasks")
	}

	// Verify the response was sent to IRC.
	sent := env.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sent), sent)
	}
	if sent[0] != "Task completed: health check passed." {
		t.Errorf("sent = %q", sent[0])
	}
}

func TestRunScheduledTask_ResponseCopiedToChannel(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	env.mock.Responses = []*llm.ChatResponse{
		{Content: "Health check: all systems operational."},
	}

	ctx := context.Background()
	env.agent.RunScheduledTask(ctx, 99, "#test", "Run health check", "alice", "")

	// Verify the final response was copied to the real channel's history.
	msgs, err := env.memory.GetHistory("#test", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}

	// Should have exactly 1 message: the copied assistant response.
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message in real channel history, got %d", len(msgs))
	}
	if msgs[0].Role != llm.RoleAssistant {
		t.Errorf("msg[0].Role = %q, want assistant", msgs[0].Role)
	}
	if msgs[0].Content != "Health check: all systems operational." {
		t.Errorf("msg[0].Content = %q, want %q", msgs[0].Content, "Health check: all systems operational.")
	}
}

func TestRunScheduledTask_CleanupAfterExecution(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	env.mock.Responses = []*llm.ChatResponse{
		{Content: "Done."},
	}

	ctx := context.Background()
	env.agent.RunScheduledTask(ctx, 7, "#test", "Clean up task", "bob", "")

	// Verify no ephemeral contexts remain by checking all possible keys.
	// Since the ephemeral key includes a sequence number, we scan the
	// conversations table for any rows with a channel starting with "__task:7:#test:".
	var count int
	err := env.memory.db.QueryRow(
		`SELECT COUNT(*) FROM conversations WHERE channel LIKE ?`,
		"__task:7:#test:%",
	).Scan(&count)
	if err != nil {
		t.Fatalf("query ephemeral count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 ephemeral messages after cleanup, got %d", count)
	}
}

func TestHandleEvent_IsolatedContext(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Simulate prior channel conversation.
	if err := env.memory.AddMessage("#test", llm.RoleUser, "alice: Tell me a joke", "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := env.memory.AddMessage("#test", llm.RoleAssistant, "Why did the chicken cross the road?", "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	env.mock.Responses = []*llm.ChatResponse{
		{Content: "Backup completed successfully."},
	}

	ctx := context.Background()
	err := env.agent.HandleEvent(ctx, "#test", "_system", "backup-script", "backup.completed", "Backup finished", `{"size":"1.2GB"}`)
	if err != nil {
		t.Fatalf("HandleEvent error: %v", err)
	}

	// Verify the LLM was called.
	if len(env.mock.Calls) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(env.mock.Calls))
	}

	// Verify the LLM request does NOT contain the prior conversation.
	req := env.mock.Calls[0]
	for _, msg := range req.Messages {
		if msg.Role == llm.RoleUser && strings.Contains(msg.Content, "joke") {
			t.Error("event LLM request should NOT contain prior channel conversation about jokes")
		}
	}

	// Verify the event IS present.
	foundEvent := false
	for _, msg := range req.Messages {
		if msg.Role == llm.RoleSystem && strings.Contains(msg.Content, "[Event from backup-script]") {
			foundEvent = true
			break
		}
	}
	if !foundEvent {
		t.Error("event LLM request should contain the event message")
	}

	// Verify the system prompt contains the task mode instruction.
	systemPrompt := req.Messages[0].Content
	if !strings.Contains(systemPrompt, "Task Mode") {
		t.Error("system prompt should contain Task Mode section for events")
	}

	// Verify the response was copied to the real channel.
	msgs, err := env.memory.GetHistory("#test", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}

	// Should have: prior user msg, prior assistant msg, + copied event response.
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages in real channel history, got %d", len(msgs))
	}
	if msgs[2].Role != llm.RoleAssistant {
		t.Errorf("msg[2].Role = %q, want assistant", msgs[2].Role)
	}
	if msgs[2].Content != "Backup completed successfully." {
		t.Errorf("msg[2].Content = %q, want %q", msgs[2].Content, "Backup completed successfully.")
	}

	// Verify the response was sent to IRC.
	sent := env.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sent), sent)
	}
	if sent[0] != "Backup completed successfully." {
		t.Errorf("sent = %q", sent[0])
	}
}

// --- Provider failover tests ---

func TestProviderFailover_FallsBackOn5xx(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Primary provider returns a 5xx-like error (not permanent, not rate limited).
	primaryMock := &llmtest.MockProvider{
		NameVal: "primary",
		Errors:  []error{fmt.Errorf("server error 500")},
	}
	fallbackMock := &llmtest.MockProvider{
		NameVal: "fallback",
		Responses: []*llm.ChatResponse{
			{Content: "fallback response"},
		},
	}

	providers := map[string]llm.Provider{
		"primary":  primaryMock,
		"fallback": fallbackMock,
	}
	fallbacks := map[string][]string{
		"primary": {"fallback"},
	}
	env.agent.UpdateProviders(providers, "primary", fallbacks)

	ctx := context.Background()
	env.agent.HandleMessage(ctx, "#test", "user1", "hello")

	sent := env.getSent()
	// Should have the failover notice + the fallback response.
	var hasFailoverNotice, hasFallbackResponse bool
	for _, msg := range sent {
		if strings.Contains(msg, "[failover]") && strings.Contains(msg, "primary") && strings.Contains(msg, "fallback") {
			hasFailoverNotice = true
		}
		if msg == "fallback response" {
			hasFallbackResponse = true
		}
	}
	if !hasFailoverNotice {
		t.Errorf("expected failover notice in sent messages: %v", sent)
	}
	if !hasFallbackResponse {
		t.Errorf("expected fallback response in sent messages: %v", sent)
	}
}

func TestProviderFailover_FallsBackOn429(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Primary provider returns a rate-limited error.
	primaryMock := &llmtest.MockProvider{
		NameVal: "primary",
		Errors:  []error{llm.NewRateLimitedError(fmt.Errorf("rate limited"))},
	}
	fallbackMock := &llmtest.MockProvider{
		NameVal: "fallback",
		Responses: []*llm.ChatResponse{
			{Content: "fallback after 429"},
		},
	}

	providers := map[string]llm.Provider{
		"primary":  primaryMock,
		"fallback": fallbackMock,
	}
	fallbacks := map[string][]string{
		"primary": {"fallback"},
	}
	env.agent.UpdateProviders(providers, "primary", fallbacks)

	ctx := context.Background()
	env.agent.HandleMessage(ctx, "#test", "user1", "hello")

	sent := env.getSent()
	var hasFallbackResponse bool
	for _, msg := range sent {
		if msg == "fallback after 429" {
			hasFallbackResponse = true
		}
	}
	if !hasFallbackResponse {
		t.Errorf("expected fallback response after 429: %v", sent)
	}
}

func TestProviderFailover_NoPermanentFallback(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Primary provider returns a permanent error (4xx, not 429).
	primaryMock := &llmtest.MockProvider{
		NameVal: "primary",
		Errors:  []error{llm.NewPermanentError(fmt.Errorf("bad request 400"))},
	}
	fallbackMock := &llmtest.MockProvider{
		NameVal: "fallback",
		Responses: []*llm.ChatResponse{
			{Content: "should not reach"},
		},
	}

	providers := map[string]llm.Provider{
		"primary":  primaryMock,
		"fallback": fallbackMock,
	}
	fallbacks := map[string][]string{
		"primary": {"fallback"},
	}
	env.agent.UpdateProviders(providers, "primary", fallbacks)

	ctx := context.Background()
	env.agent.HandleMessage(ctx, "#test", "user1", "hello")

	// Fallback should NOT have been called.
	if len(fallbackMock.Calls) != 0 {
		t.Errorf("expected 0 fallback calls for permanent error, got %d", len(fallbackMock.Calls))
	}

	// Should have an error message sent.
	sent := env.getSent()
	var hasError bool
	for _, msg := range sent {
		if strings.Contains(msg, "error") || strings.Contains(msg, "Error") {
			hasError = true
		}
	}
	if !hasError {
		t.Errorf("expected error message for permanent failure: %v", sent)
	}
}

func TestProviderFailover_AllFail(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Both providers return retryable errors.
	primaryMock := &llmtest.MockProvider{
		NameVal: "primary",
		Errors:  []error{fmt.Errorf("server error 500")},
	}
	fallbackMock := &llmtest.MockProvider{
		NameVal: "fallback",
		Errors:  []error{fmt.Errorf("server error 502")},
	}

	providers := map[string]llm.Provider{
		"primary":  primaryMock,
		"fallback": fallbackMock,
	}
	fallbacks := map[string][]string{
		"primary": {"fallback"},
	}
	env.agent.UpdateProviders(providers, "primary", fallbacks)

	ctx := context.Background()
	env.agent.HandleMessage(ctx, "#test", "user1", "hello")

	// Both providers should have been called.
	if len(primaryMock.Calls) != 1 {
		t.Errorf("expected 1 primary call, got %d", len(primaryMock.Calls))
	}
	if len(fallbackMock.Calls) != 1 {
		t.Errorf("expected 1 fallback call, got %d", len(fallbackMock.Calls))
	}

	// Should have an error message sent.
	sent := env.getSent()
	var hasError bool
	for _, msg := range sent {
		if strings.Contains(msg, "error") || strings.Contains(msg, "Error") {
			hasError = true
		}
	}
	if !hasError {
		t.Errorf("expected error message when all providers fail: %v", sent)
	}
}

func TestProviderFailover_ContextCancelled(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Primary provider returns an error, but context is already cancelled.
	primaryMock := &llmtest.MockProvider{
		NameVal: "primary",
		Errors:  []error{context.Canceled},
	}
	fallbackMock := &llmtest.MockProvider{
		NameVal: "fallback",
		Responses: []*llm.ChatResponse{
			{Content: "should not reach"},
		},
	}

	providers := map[string]llm.Provider{
		"primary":  primaryMock,
		"fallback": fallbackMock,
	}
	fallbacks := map[string][]string{
		"primary": {"fallback"},
	}
	env.agent.UpdateProviders(providers, "primary", fallbacks)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.
	env.agent.HandleMessage(ctx, "#test", "user1", "hello")

	// Fallback should NOT have been called since context was cancelled.
	if len(fallbackMock.Calls) != 0 {
		t.Errorf("expected 0 fallback calls on context cancellation, got %d", len(fallbackMock.Calls))
	}
}

func TestGetProviderChain_BuildsCorrectChain(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	primary := &llmtest.MockProvider{NameVal: "primary"}
	fb1 := &llmtest.MockProvider{NameVal: "fb1"}
	fb2 := &llmtest.MockProvider{NameVal: "fb2"}

	providers := map[string]llm.Provider{
		"primary": primary,
		"fb1":     fb1,
		"fb2":     fb2,
	}
	fallbacks := map[string][]string{
		"primary": {"fb1", "fb2"},
	}
	env.agent.UpdateProviders(providers, "primary", fallbacks)

	chain := env.agent.getProviderChain(primary, "user1", "#test", nil)
	if len(chain) != 3 {
		t.Fatalf("expected chain length 3, got %d", len(chain))
	}
	if chain[0].Name() != "primary" {
		t.Errorf("chain[0] = %q, want primary", chain[0].Name())
	}
	if chain[1].Name() != "fb1" {
		t.Errorf("chain[1] = %q, want fb1", chain[1].Name())
	}
	if chain[2].Name() != "fb2" {
		t.Errorf("chain[2] = %q, want fb2", chain[2].Name())
	}
}

func TestGetProviderChain_NoFallbacks(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	primary := &llmtest.MockProvider{NameVal: "primary"}
	providers := map[string]llm.Provider{
		"primary": primary,
	}
	env.agent.UpdateProviders(providers, "primary", nil)

	chain := env.agent.getProviderChain(primary, "user1", "#test", nil)
	if len(chain) != 1 {
		t.Fatalf("expected chain length 1, got %d", len(chain))
	}
	if chain[0].Name() != "primary" {
		t.Errorf("chain[0] = %q, want primary", chain[0].Name())
	}
}

func TestGetProviderChain_SkipsMissingFallback(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	primary := &llmtest.MockProvider{NameVal: "primary"}
	fb2 := &llmtest.MockProvider{NameVal: "fb2"}

	providers := map[string]llm.Provider{
		"primary": primary,
		"fb2":     fb2,
	}
	fallbacks := map[string][]string{
		"primary": {"missing", "fb2"},
	}
	env.agent.UpdateProviders(providers, "primary", fallbacks)

	chain := env.agent.getProviderChain(primary, "user1", "#test", nil)
	if len(chain) != 2 {
		t.Fatalf("expected chain length 2 (primary + fb2, missing skipped), got %d", len(chain))
	}
	if chain[1].Name() != "fb2" {
		t.Errorf("chain[1] = %q, want fb2", chain[1].Name())
	}
}

func TestPerTaskModel_UsesSpecifiedProvider(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Create a second mock provider with a distinct name.
	altMock := &llmtest.MockProvider{
		NameVal: "alt-provider",
		Responses: []*llm.ChatResponse{
			{Content: "Alt provider response."},
		},
	}
	addTestProvider(env.agent, "alt-provider", altMock)

	// The default provider should NOT be called.
	env.mock.Responses = []*llm.ChatResponse{
		{Content: "Default provider response."},
	}

	ctx := context.Background()
	env.agent.RunScheduledTask(ctx, 100, "#test", "Run alt task", "alice", "alt-provider")

	// Verify the alt provider was called, not the default.
	if len(altMock.Calls) != 1 {
		t.Fatalf("expected 1 call to alt-provider, got %d", len(altMock.Calls))
	}
	if len(env.mock.Calls) != 0 {
		t.Errorf("expected 0 calls to default provider, got %d", len(env.mock.Calls))
	}

	// Verify the response came from the alt provider.
	sent := env.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sent), sent)
	}
	if sent[0] != "Alt provider response." {
		t.Errorf("sent = %q, want %q", sent[0], "Alt provider response.")
	}
}

func TestPerTaskModel_FallbackOnMissing(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Only the default "test-provider" exists. Specify a non-existent provider.
	env.mock.Responses = []*llm.ChatResponse{
		{Content: "Default fallback response."},
	}

	ctx := context.Background()
	env.agent.RunScheduledTask(ctx, 101, "#test", "Run missing provider task", "alice", "nonexistent-provider")

	// The default provider should be used as fallback.
	if len(env.mock.Calls) != 1 {
		t.Fatalf("expected 1 call to default provider (fallback), got %d", len(env.mock.Calls))
	}

	sent := env.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d: %v", len(sent), sent)
	}
	if sent[0] != "Default fallback response." {
		t.Errorf("sent = %q, want %q", sent[0], "Default fallback response.")
	}
}

func TestResolveProvider_OverrideChain(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Set up channel settings with a per-channel provider.
	channelMock := &llmtest.MockProvider{NameVal: "channel-provider"}
	overrideMock := &llmtest.MockProvider{NameVal: "override-provider"}
	addTestProvider(env.agent, "channel-provider", channelMock)
	addTestProvider(env.agent, "override-provider", overrideMock)

	// Wire up channel settings backed by the same in-memory DB.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cs := NewChannelSettingsStore(env.memory.db, logger)
	env.agent.channelSettings = cs

	// Set per-channel provider.
	if err := cs.SetProvider("#test", "channel-provider"); err != nil {
		t.Fatalf("SetProvider: %v", err)
	}

	tests := []struct {
		name     string
		override string
		wantName string
	}{
		{
			name:     "override wins over channel and default",
			override: "override-provider",
			wantName: "override-provider",
		},
		{
			name:     "channel wins when no override",
			override: "",
			wantName: "channel-provider",
		},
		{
			name:     "missing override falls through to channel",
			override: "nonexistent",
			wantName: "channel-provider",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var overrides []string
			if tt.override != "" {
				overrides = append(overrides, tt.override)
			}
			p, err := env.agent.resolveProvider("#test", "alice", nil, overrides...)
			if err != nil {
				t.Fatalf("resolveProvider: %v", err)
			}
			if p.Name() != tt.wantName {
				t.Errorf("provider = %q, want %q", p.Name(), tt.wantName)
			}
		})
	}

	// Also test: no channel setting, no override → global default.
	if err := cs.SetProvider("#test", ""); err != nil {
		t.Fatalf("SetProvider clear: %v", err)
	}
	p, err := env.agent.resolveProvider("#test", "alice", nil)
	if err != nil {
		t.Fatalf("resolveProvider (default): %v", err)
	}
	if p.Name() != "test-provider" {
		t.Errorf("default provider = %q, want %q", p.Name(), "test-provider")
	}
}

func TestBuildSystemPrompt_TaskProviderOverride(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Add an alt provider.
	altMock := &llmtest.MockProvider{NameVal: "alt-provider"}
	addTestProvider(env.agent, "alt-provider", altMock)

	// With task provider override — should show the overridden model name.
	ctx := context.WithValue(context.Background(), taskModeKey{}, true)
	ctx = context.WithValue(ctx, taskProviderKey{}, "alt-provider")
	prompt := env.agent.buildSystemPrompt(ctx, "#test", env.agent.loadConfig())
	if !strings.Contains(prompt, "Active model: alt-provider (task-specific)") {
		t.Errorf("task override prompt should show alt-provider as task-specific, got:\n%s", prompt)
	}

	// With non-existent task provider override — should fall back to default.
	ctx2 := context.WithValue(context.Background(), taskModeKey{}, true)
	ctx2 = context.WithValue(ctx2, taskProviderKey{}, "nonexistent")
	prompt2 := env.agent.buildSystemPrompt(ctx2, "#test", env.agent.loadConfig())
	if strings.Contains(prompt2, "task-specific") {
		t.Errorf("nonexistent provider override should not show task-specific, got:\n%s", prompt2)
	}
	if !strings.Contains(prompt2, "Active model: test-provider") {
		t.Errorf("nonexistent provider override should fall back to default, got:\n%s", prompt2)
	}

	// With non-existent task provider override + channel provider set —
	// should show channel provider with "channel-specific" scope, not "global default".
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cs := NewChannelSettingsStore(env.memory.db, logger)
	env.agent.channelSettings = cs
	channelMock := &llmtest.MockProvider{NameVal: "channel-model"}
	addTestProvider(env.agent, "channel-model", channelMock)
	if err := cs.SetProvider("#test", "channel-model"); err != nil {
		t.Fatalf("SetProvider: %v", err)
	}

	ctx3 := context.WithValue(context.Background(), taskModeKey{}, true)
	ctx3 = context.WithValue(ctx3, taskProviderKey{}, "nonexistent")
	prompt3 := env.agent.buildSystemPrompt(ctx3, "#test", env.agent.loadConfig())
	if !strings.Contains(prompt3, "Active model: channel-model (channel-specific)") {
		t.Errorf("invalid task override with channel provider should show channel-specific, got:\n%s", prompt3)
	}
}

func TestBuildSystemPrompt_TaskMode(t *testing.T) {
	t.Parallel()
	env := newTestAgentEnv(t)

	// Without task mode — should NOT contain Task Mode section.
	prompt := env.agent.buildSystemPrompt(context.Background(), "#test", env.agent.loadConfig())
	if strings.Contains(prompt, "Task Mode") {
		t.Error("normal prompt should NOT contain Task Mode section")
	}

	// With task mode — should contain Task Mode section.
	ctx := context.WithValue(context.Background(), taskModeKey{}, true)
	prompt = env.agent.buildSystemPrompt(ctx, "#test", env.agent.loadConfig())
	if !strings.Contains(prompt, "Task Mode") {
		t.Error("task mode prompt should contain Task Mode section")
	}
	if !strings.Contains(prompt, "Focus ONLY on the task instruction") {
		t.Error("task mode prompt should contain focus instruction")
	}
}
