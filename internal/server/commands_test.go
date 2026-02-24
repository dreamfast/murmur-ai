package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"murmur/internal/bus"
	"murmur/internal/db"
	"murmur/internal/tools"
)

// mockModelSwitcher implements ModelSwitcher for testing.
type mockModelSwitcher struct {
	active          string
	providers       []string
	channelOverride map[string]string // per-channel overrides
}

func (m *mockModelSwitcher) SetProvider(channel, name string) error {
	if name == "default" || name == "" {
		if m.channelOverride != nil {
			delete(m.channelOverride, channel)
		}
		return nil
	}
	for _, p := range m.providers {
		if p == name {
			if m.channelOverride == nil {
				m.channelOverride = make(map[string]string)
			}
			m.channelOverride[channel] = name
			return nil
		}
	}
	return fmt.Errorf("provider %q not found", name)
}

func (m *mockModelSwitcher) GetProvider() string {
	return m.active
}

func (m *mockModelSwitcher) GetProviderForChannel(channel string) string {
	if m.channelOverride != nil {
		if name, ok := m.channelOverride[channel]; ok {
			return name
		}
	}
	return m.active
}

func (m *mockModelSwitcher) GetProviderNames() []string {
	return m.providers
}

// testCommandEnv holds all the components needed for command handler tests.
type testCommandEnv struct {
	handler  *CommandHandler
	registry *Registry
	memory   *Memory
	model    *mockModelSwitcher
	sent     []string // captured messages
}

// newTestCommandEnv creates a test environment with a command handler that
// captures sent messages instead of sending them over IRC.
func newTestCommandEnv(t *testing.T, allowedUsers []string) *testCommandEnv {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry := NewRegistry(2*time.Minute, logger)

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	memory := NewMemory(database, 100, 80, nil, logger)
	model := &mockModelSwitcher{
		active:    "openrouter",
		providers: []string{"openrouter", "ollama", "kimi"},
	}

	env := &testCommandEnv{
		registry: registry,
		memory:   memory,
		model:    model,
	}

	handler := &CommandHandler{
		registry:  registry,
		memory:    memory,
		notes:     nil,
		scheduler: nil,
		conn:      nil,
		model:     model,
		startTime: time.Now().Add(-5 * time.Minute),
		logger:    logger,
		sendFunc: func(channel, message string) {
			env.sent = append(env.sent, message)
		},
	}
	if allowedUsers != nil {
		handler.allowedUsers.Store(&allowedUsers)
	}
	env.handler = handler

	return env
}

// lastSent returns the last captured message, or empty string if none.
func (env *testCommandEnv) lastSent() string {
	if len(env.sent) == 0 {
		return ""
	}
	return env.sent[len(env.sent)-1]
}

func TestCommandHandler_NonCommand(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	handled := env.handler.HandleCommand("#test", "user1", "hello world")
	if handled {
		t.Error("expected non-command message to return false")
	}
}

func TestCommandHandler_Status(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	if err := env.memory.AddMessage("#test", "user", "hello", "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := env.memory.AddMessage("#test", "assistant", "hi", "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	env.registry.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "laptop",
		Hostname: "thinkpad",
		Tools:    []bus.ToolDef{{Name: "shell", Description: "Run commands", Parameters: json.RawMessage(`{}`)}},
	})

	handled := env.handler.HandleCommand("#test", "user1", "!status")
	if !handled {
		t.Fatal("expected !status to be handled")
	}

	msg := env.lastSent()
	if !strings.Contains(msg, "clients: 1") {
		t.Errorf("expected clients: 1 in status, got: %s", msg)
	}
	if !strings.Contains(msg, "model: openrouter") {
		t.Errorf("expected model: openrouter in status, got: %s", msg)
	}
	if !strings.Contains(msg, "messages: 2") {
		t.Errorf("expected messages: 2 in status, got: %s", msg)
	}
}

func TestCommandHandler_Clients(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	env.registry.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "laptop",
		Hostname: "thinkpad",
		Tools:    []bus.ToolDef{{Name: "shell", Description: "Run commands"}},
	})

	env.handler.HandleCommand("#test", "user1", "!clients")
	msg := env.lastSent()
	if !strings.Contains(msg, "laptop") {
		t.Errorf("expected 'laptop' in clients output, got: %s", msg)
	}
	if !strings.Contains(msg, "thinkpad") {
		t.Errorf("expected 'thinkpad' in clients output, got: %s", msg)
	}
}

func TestCommandHandler_ClientsEmpty(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	env.handler.HandleCommand("#test", "user1", "!clients")
	msg := env.lastSent()
	if msg != "no clients connected" {
		t.Errorf("expected 'no clients connected', got: %s", msg)
	}
}

func TestCommandHandler_Tools_ClientOnly(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	env.registry.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "laptop",
		Hostname: "thinkpad",
		Tools: []bus.ToolDef{
			{Name: "shell", Description: "Run commands"},
			{Name: "mail_read", Description: "Read email"},
		},
	})

	env.handler.HandleCommand("#test", "user1", "!tools")
	msg := env.lastSent()
	if !strings.Contains(msg, "shell") {
		t.Errorf("expected 'shell' in tools output, got: %s", msg)
	}
	if !strings.Contains(msg, "mail_read") {
		t.Errorf("expected 'mail_read' in tools output, got: %s", msg)
	}
	if !strings.Contains(msg, "(client)") {
		t.Errorf("expected '(client)' source label, got: %s", msg)
	}
	if !strings.Contains(msg, "available tools (2)") {
		t.Errorf("expected tool count in header, got: %s", msg)
	}
}

func TestCommandHandler_Tools_ServerAndClient(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	// Add server tools.
	st := NewToolRegistry()
	if err := st.Register(tools.Tool{
		Name:        "code_exec",
		Description: "Execute code",
		Parameters:  json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("register server tool: %v", err)
	}
	env.handler.serverTools = st

	// Add client tools.
	env.registry.Register(&bus.RegisterMessage{
		Type:     bus.TypeRegister,
		ClientID: "laptop",
		Hostname: "thinkpad",
		Tools: []bus.ToolDef{
			{Name: "shell", Description: "Run commands"},
		},
	})

	env.handler.HandleCommand("#test", "user1", "!tools")
	msg := env.lastSent()
	if !strings.Contains(msg, "code_exec") {
		t.Errorf("expected server tool 'code_exec' in output, got: %s", msg)
	}
	if !strings.Contains(msg, "(server)") {
		t.Errorf("expected '(server)' source label, got: %s", msg)
	}
	if !strings.Contains(msg, "shell") {
		t.Errorf("expected client tool 'shell' in output, got: %s", msg)
	}
	if !strings.Contains(msg, "(client)") {
		t.Errorf("expected '(client)' source label, got: %s", msg)
	}
	if !strings.Contains(msg, "available tools (2)") {
		t.Errorf("expected tool count in header, got: %s", msg)
	}
}

func TestCommandHandler_Tools_ServerOnly(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	// Add server tools only (simulates server-only mode with no clients).
	st := NewToolRegistry()
	if err := st.Register(tools.Tool{
		Name:        "code_exec",
		Description: "Execute code",
		Parameters:  json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("register server tool: %v", err)
	}
	if err := st.Register(tools.Tool{
		Name:        "http_request",
		Description: "Make HTTP requests",
		Parameters:  json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("register server tool: %v", err)
	}
	env.handler.serverTools = st

	env.handler.HandleCommand("#test", "user1", "!tools")
	msg := env.lastSent()
	if !strings.Contains(msg, "code_exec") {
		t.Errorf("expected 'code_exec' in output, got: %s", msg)
	}
	if !strings.Contains(msg, "http_request") {
		t.Errorf("expected 'http_request' in output, got: %s", msg)
	}
	if !strings.Contains(msg, "(server)") {
		t.Errorf("expected '(server)' source label, got: %s", msg)
	}
	if !strings.Contains(msg, "available tools (2)") {
		t.Errorf("expected tool count in header, got: %s", msg)
	}
}

func TestCommandHandler_ToolsEmpty(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	env.handler.HandleCommand("#test", "user1", "!tools")
	msg := env.lastSent()
	if msg != "no tools available" {
		t.Errorf("expected 'no tools available', got: %s", msg)
	}
}

func TestCommandHandler_ModelShow(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	env.handler.HandleCommand("#test", "user1", "!model")
	msg := env.lastSent()
	if !strings.Contains(msg, "channel model: openrouter") {
		t.Errorf("expected channel model in output, got: %s", msg)
	}
	if !strings.Contains(msg, "global default") {
		t.Errorf("expected 'global default' in output, got: %s", msg)
	}
	if !strings.Contains(msg, "ollama") {
		t.Errorf("expected available providers in output, got: %s", msg)
	}
}

func TestCommandHandler_ModelSwitch(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	env.handler.HandleCommand("#test", "user1", "!model ollama")
	msg := env.lastSent()
	if !strings.Contains(msg, "switched channel model to: ollama") {
		t.Errorf("expected switch confirmation, got: %s", msg)
	}
	if env.model.GetProviderForChannel("#test") != "ollama" {
		t.Errorf("model not switched for channel: got %s", env.model.GetProviderForChannel("#test"))
	}
}

func TestCommandHandler_ModelSwitchInvalid(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	env.handler.HandleCommand("#test", "user1", "!model nonexistent")
	msg := env.lastSent()
	if !strings.Contains(msg, "failed to switch model") {
		t.Errorf("expected error message, got: %s", msg)
	}
}

func TestCommandHandler_ModelDefault(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	// Set a per-channel override first.
	env.handler.HandleCommand("#test", "user1", "!model kimi")
	if env.model.GetProviderForChannel("#test") != "kimi" {
		t.Fatalf("expected kimi, got %s", env.model.GetProviderForChannel("#test"))
	}

	// Reset to default.
	env.handler.HandleCommand("#test", "user1", "!model default")
	msg := env.lastSent()
	if !strings.Contains(msg, "reset to global default model") {
		t.Errorf("expected reset confirmation, got: %s", msg)
	}
	if env.model.GetProviderForChannel("#test") != "openrouter" {
		t.Errorf("expected global default after reset, got %s", env.model.GetProviderForChannel("#test"))
	}
}

func TestCommandHandler_ModelShowChannelSpecific(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	// Set a per-channel override.
	env.handler.HandleCommand("#test", "user1", "!model kimi")

	// Show model — should indicate channel-specific.
	env.handler.HandleCommand("#test", "user1", "!model")
	msg := env.lastSent()
	if !strings.Contains(msg, "channel model: kimi") {
		t.Errorf("expected channel model: kimi, got: %s", msg)
	}
	if !strings.Contains(msg, "channel-specific") {
		t.Errorf("expected 'channel-specific' scope, got: %s", msg)
	}
	if !strings.Contains(msg, "global default: openrouter") {
		t.Errorf("expected global default: openrouter, got: %s", msg)
	}
}

func TestCommandHandler_History(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	if err := env.memory.AddMessage("#test", "user", "hello", "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := env.memory.AddMessage("#test", "assistant", "hi there", "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	env.handler.HandleCommand("#test", "user1", "!history")
	msg := env.lastSent()
	if !strings.Contains(msg, "[user] hello") {
		t.Errorf("expected user message in history, got: %s", msg)
	}
	if !strings.Contains(msg, "[assistant] hi there") {
		t.Errorf("expected assistant message in history, got: %s", msg)
	}
}

func TestCommandHandler_HistoryWithLimit(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	for i := 0; i < 5; i++ {
		if err := env.memory.AddMessage("#test", "user", fmt.Sprintf("msg%d", i), "", ""); err != nil {
			t.Fatalf("AddMessage %d: %v", i, err)
		}
	}

	env.handler.HandleCommand("#test", "user1", "!history 2")
	msg := env.lastSent()
	if !strings.Contains(msg, "msg3") || !strings.Contains(msg, "msg4") {
		t.Errorf("expected last 2 messages, got: %s", msg)
	}
}

func TestCommandHandler_HistoryEmpty(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	env.handler.HandleCommand("#test", "user1", "!history")
	msg := env.lastSent()
	if msg != "no conversation history" {
		t.Errorf("expected 'no conversation history', got: %s", msg)
	}
}

func TestCommandHandler_Forget(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	if err := env.memory.AddMessage("#test", "user", "hello", "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := env.memory.AddMessage("#test", "assistant", "hi", "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	env.handler.HandleCommand("#test", "user1", "!forget")
	msg := env.lastSent()
	if msg != "conversation history cleared" {
		t.Errorf("expected confirmation, got: %s", msg)
	}

	count, _ := env.memory.GetHistoryCount("#test")
	if count != 0 {
		t.Errorf("expected 0 messages after forget, got %d", count)
	}
}

func TestCommandHandler_Help(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	env.handler.HandleCommand("#test", "user1", "!help")
	msg := env.lastSent()
	if !strings.Contains(msg, "!status") {
		t.Errorf("expected !status in help, got: %s", msg)
	}
	if !strings.Contains(msg, "!forget") {
		t.Errorf("expected !forget in help, got: %s", msg)
	}
}

func TestCommandHandler_UnknownCommand(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	handled := env.handler.HandleCommand("#test", "user1", "!foobar")
	if !handled {
		t.Error("expected unknown command to be handled (consumed)")
	}
	msg := env.lastSent()
	if !strings.Contains(msg, "unknown command") {
		t.Errorf("expected 'unknown command' message, got: %s", msg)
	}
}

func TestCommandHandler_Unauthorized(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, []string{"admin", "owner"})

	handled := env.handler.HandleCommand("#test", "hacker", "!status")
	if !handled {
		t.Error("expected unauthorized command to be handled (consumed)")
	}
	msg := env.lastSent()
	if !strings.Contains(msg, "unauthorized") {
		t.Errorf("expected 'unauthorized' message, got: %s", msg)
	}
}

func TestCommandHandler_AuthorizedUser(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, []string{"admin", "owner"})

	handled := env.handler.HandleCommand("#test", "admin", "!help")
	if !handled {
		t.Error("expected authorized command to be handled")
	}
	msg := env.lastSent()
	if !strings.Contains(msg, "!status") {
		t.Errorf("expected help output for authorized user, got: %s", msg)
	}
}

func TestCommandHandler_NoModel(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)
	env.handler.model = nil

	env.handler.HandleCommand("#test", "user1", "!model")
	msg := env.lastSent()
	if msg != "no LLM providers configured" {
		t.Errorf("expected 'no LLM providers configured', got: %s", msg)
	}
}

func TestCommandHandler_EmptyAllowedUsers(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil) // nil = no restrictions

	handled := env.handler.HandleCommand("#test", "anyone", "!help")
	if !handled {
		t.Error("expected command to be handled with no user restrictions")
	}
	msg := env.lastSent()
	if !strings.Contains(msg, "!status") {
		t.Errorf("expected help output, got: %s", msg)
	}
}

func TestCommandHandler_ApproveNoPending(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env.handler.approvals = NewApprovalManager(logger)

	env.handler.HandleCommand("#test", "user1", "!approve")
	msg := env.lastSent()
	if msg != "no pending approvals" {
		t.Errorf("expected 'no pending approvals', got: %s", msg)
	}
}

func TestCommandHandler_ApproveResolves(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	am := NewApprovalManager(logger)
	env.handler.approvals = am

	_, resultCh := am.RequestApproval("#test", "shell", json.RawMessage(`{"cmd":"ls"}`), "client-1")

	env.handler.HandleCommand("#test", "user1", "!approve")
	msg := env.lastSent()
	if !strings.Contains(msg, "approved: shell") {
		t.Errorf("expected 'approved: shell', got: %s", msg)
	}

	// Verify the result channel received approval.
	select {
	case result := <-resultCh:
		if !result.Approved {
			t.Error("expected approval to be approved")
		}
	default:
		t.Error("expected result on channel")
	}
}

func TestCommandHandler_DenyResolves(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	am := NewApprovalManager(logger)
	env.handler.approvals = am

	_, resultCh := am.RequestApproval("#test", "shell", json.RawMessage(`{"cmd":"rm -rf /"}`), "client-1")

	env.handler.HandleCommand("#test", "user1", "!deny")
	msg := env.lastSent()
	if !strings.Contains(msg, "denied: shell") {
		t.Errorf("expected 'denied: shell', got: %s", msg)
	}

	// Verify the result channel received denial.
	select {
	case result := <-resultCh:
		if result.Approved {
			t.Error("expected approval to be denied")
		}
	default:
		t.Error("expected result on channel")
	}
}

func TestCommandHandler_PendingList(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	am := NewApprovalManager(logger)
	env.handler.approvals = am

	am.RequestApproval("#test", "shell", json.RawMessage(`{"cmd":"ls"}`), "client-1")
	am.RequestApproval("#test", "mail_send", json.RawMessage(`{"to":"user@example.com"}`), "client-2")

	env.handler.HandleCommand("#test", "user1", "!pending")
	msg := env.lastSent()
	if !strings.Contains(msg, "pending approvals (2)") {
		t.Errorf("expected 'pending approvals (2)', got: %s", msg)
	}
	if !strings.Contains(msg, "shell") {
		t.Errorf("expected 'shell' in pending list, got: %s", msg)
	}
	if !strings.Contains(msg, "mail_send") {
		t.Errorf("expected 'mail_send' in pending list, got: %s", msg)
	}
}

func TestCommandHandler_ApproveNoManager(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)
	// approvals is nil by default

	env.handler.HandleCommand("#test", "user1", "!approve")
	msg := env.lastSent()
	if msg != "approval flow not configured" {
		t.Errorf("expected 'approval flow not configured', got: %s", msg)
	}
}

func TestCommandHandler_HelpIncludesApproval(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	env.handler.HandleCommand("#test", "user1", "!help")
	msg := env.lastSent()
	if !strings.Contains(msg, "!approve") {
		t.Errorf("expected !approve in help, got: %s", msg)
	}
	if !strings.Contains(msg, "!deny") {
		t.Errorf("expected !deny in help, got: %s", msg)
	}
	if !strings.Contains(msg, "!pending") {
		t.Errorf("expected !pending in help, got: %s", msg)
	}
	if !strings.Contains(msg, "!reload") {
		t.Errorf("expected !reload in help, got: %s", msg)
	}
}

// mockReloader implements Reloader for testing.
type mockReloader struct {
	called bool
	err    error
}

func (m *mockReloader) Reload() error {
	m.called = true
	return m.err
}

func TestCommandHandler_Reload(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	reloader := &mockReloader{}
	env.handler.reloader = reloader

	env.handler.HandleCommand("#test", "admin", "!reload")

	if !reloader.called {
		t.Error("expected Reload to be called")
	}
	msg := env.lastSent()
	if msg != "config reloaded successfully" {
		t.Errorf("expected success message, got: %s", msg)
	}
}

func TestCommandHandler_ReloadError(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	reloader := &mockReloader{err: fmt.Errorf("bad config")}
	env.handler.reloader = reloader

	env.handler.HandleCommand("#test", "admin", "!reload")

	msg := env.lastSent()
	if !strings.Contains(msg, "reload failed") {
		t.Errorf("expected 'reload failed' message, got: %s", msg)
	}
	if !strings.Contains(msg, "bad config") {
		t.Errorf("expected error detail in message, got: %s", msg)
	}
}

func TestCommandHandler_ReloadNilReloader(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	// reloader is nil by default.
	env.handler.HandleCommand("#test", "admin", "!reload")

	msg := env.lastSent()
	if msg != "hot reload not available" {
		t.Errorf("expected 'hot reload not available', got: %s", msg)
	}
}

// mockAgentStopper implements AgentStopper for testing.
type mockAgentStopper struct {
	active bool // whether StopChannel should report a loop was running
	called bool
}

func (m *mockAgentStopper) StopChannel(channel string) bool {
	m.called = true
	return m.active
}

func TestCommandHandler_Stop_ActiveLoop(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	stopper := &mockAgentStopper{active: true}
	env.handler.stopper = stopper

	handled := env.handler.HandleCommand("#test", "user1", "!stop")
	if !handled {
		t.Fatal("expected !stop to be handled")
	}
	if !stopper.called {
		t.Error("expected StopChannel to be called")
	}
	msg := env.lastSent()
	if !strings.Contains(msg, "stopping agent loop") {
		t.Errorf("expected 'stopping agent loop' message, got: %s", msg)
	}
}

func TestCommandHandler_Stop_NoActiveLoop(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	stopper := &mockAgentStopper{active: false}
	env.handler.stopper = stopper

	handled := env.handler.HandleCommand("#test", "user1", "!stop")
	if !handled {
		t.Fatal("expected !stop to be handled")
	}
	if !stopper.called {
		t.Error("expected StopChannel to be called")
	}
	msg := env.lastSent()
	if msg != "no active agent loop on this channel" {
		t.Errorf("expected 'no active agent loop on this channel', got: %s", msg)
	}
}

func TestCommandHandler_Stop_NilStopper(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)
	// stopper is nil by default

	handled := env.handler.HandleCommand("#test", "user1", "!stop")
	if !handled {
		t.Fatal("expected !stop to be handled")
	}
	msg := env.lastSent()
	if msg != "stop not available" {
		t.Errorf("expected 'stop not available', got: %s", msg)
	}
}

func TestCommandHandler_HelpIncludesStop(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, nil)

	env.handler.HandleCommand("#test", "user1", "!help")
	msg := env.lastSent()
	if !strings.Contains(msg, "!stop") {
		t.Errorf("expected !stop in help, got: %s", msg)
	}
}

func TestCommandHandler_UpdateAllowedUsers(t *testing.T) {
	t.Parallel()
	env := newTestCommandEnv(t, []string{"admin"})

	// Initially, "admin" is allowed.
	env.handler.HandleCommand("#test", "admin", "!status")
	if len(env.sent) != 1 {
		t.Fatalf("expected 1 message, got %d", len(env.sent))
	}

	// "newuser" is not allowed.
	env.handler.HandleCommand("#test", "newuser", "!status")
	if len(env.sent) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(env.sent))
	}
	if env.sent[1] != "unauthorized: you are not in the allowed users list" {
		t.Errorf("expected unauthorized message, got: %s", env.sent[1])
	}

	// Update allowed users to include "newuser".
	env.handler.UpdateAllowedUsers([]string{"admin", "newuser"})

	// Now "newuser" should be allowed.
	env.handler.HandleCommand("#test", "newuser", "!status")
	if len(env.sent) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(env.sent))
	}
	// Should get a status response, not unauthorized.
	if strings.Contains(env.sent[2], "unauthorized") {
		t.Errorf("expected authorized response, got: %s", env.sent[2])
	}
}
