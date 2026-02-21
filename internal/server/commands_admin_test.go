package server

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"murmur/internal/config"
)

// testAdminEnv holds all the components needed for admin command tests.
type testAdminEnv struct {
	handler  *CommandHandler
	pm       *PermissionManager
	pw       *PermissionsWriter
	reloader *pmReloader
	sent     []string
}

// pmReloader is a mock Reloader that actually updates the PermissionManager
// from the permissions file, simulating what Server.Reload() does.
type pmReloader struct {
	pm     *PermissionManager
	pw     *PermissionsWriter
	called bool
	err    error
}

func (r *pmReloader) Reload() error {
	r.called = true
	if r.err != nil {
		return r.err
	}
	cfg, err := r.pw.Read()
	if err != nil {
		return err
	}
	r.pm.Update(cfg)
	return nil
}

// newTestAdminEnv creates a test environment with a permission manager,
// permissions writer, and a command handler that captures sent messages.
// The admin nick is always "admin" with role "admin".
func newTestAdminEnv(t *testing.T) *testAdminEnv {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	path := filepath.Join(dir, "permissions.toml")

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"admin": {Role: "admin", Tools: []string{"*"}, Autonomy: "auto"},
		},
	}

	pm := NewPermissionManager(permCfg, logger)
	pw := NewPermissionsWriter(path, logger)

	// Seed the file with the initial config so reads work.
	if err := pw.WriteUser("admin", config.UserPermissions{Role: "admin", Tools: []string{"*"}, Autonomy: "auto"}); err != nil {
		t.Fatalf("seed admin user: %v", err)
	}

	reloader := &pmReloader{pm: pm, pw: pw}

	env := &testAdminEnv{
		pm:       pm,
		pw:       pw,
		reloader: reloader,
	}

	handler := &CommandHandler{
		reloader:  reloader,
		startTime: time.Now(),
		logger:    logger,
		sendFunc: func(channel, message string) {
			env.sent = append(env.sent, message)
		},
	}
	handler.permissions.Store(pm)
	handler.permWriter.Store(pw)
	env.handler = handler

	return env
}

// lastSent returns the last captured message, or empty string if none.
func (env *testAdminEnv) lastSent() string {
	if len(env.sent) == 0 {
		return ""
	}
	return env.sent[len(env.sent)-1]
}

func TestCmdUser_AdminOnly(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "nobody", "!user list")
	msg := env.lastSent()
	if !strings.Contains(msg, "permission denied") {
		t.Errorf("expected permission denied, got: %s", msg)
	}
}

func TestCmdUser_List(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user list")
	msg := env.lastSent()
	if !strings.Contains(msg, "admin") {
		t.Errorf("expected admin in user list, got: %s", msg)
	}
	if !strings.Contains(msg, "[admin]") {
		t.Errorf("expected [admin] role tag, got: %s", msg)
	}
}

func TestCmdUser_Info(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user info admin")
	msg := env.lastSent()
	if !strings.Contains(msg, "user: admin") {
		t.Errorf("expected 'user: admin', got: %s", msg)
	}
	if !strings.Contains(msg, "role: admin") {
		t.Errorf("expected 'role: admin', got: %s", msg)
	}
}

func TestCmdUser_InfoNotFound(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user info ghost")
	msg := env.lastSent()
	if !strings.Contains(msg, "not found") {
		t.Errorf("expected 'not found', got: %s", msg)
	}
}

func TestCmdUser_Add(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user add bob user")
	msg := env.lastSent()
	if !strings.Contains(msg, "added") {
		t.Errorf("expected 'added', got: %s", msg)
	}

	// Verify the user was written.
	cfg, err := env.pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	bob, ok := cfg.Users["bob"]
	if !ok {
		t.Fatal("expected bob in config")
	}
	if bob.Role != "user" {
		t.Errorf("expected role user, got %q", bob.Role)
	}

	// Verify reload was called.
	if !env.reloader.called {
		t.Error("expected reload to be called")
	}
}

func TestCmdUser_AddDefaultRole(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user add charlie")
	msg := env.lastSent()
	if !strings.Contains(msg, "added") {
		t.Errorf("expected 'added', got: %s", msg)
	}

	cfg, err := env.pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	charlie, ok := cfg.Users["charlie"]
	if !ok {
		t.Fatal("expected charlie in config")
	}
	if charlie.Role != "user" {
		t.Errorf("expected default role 'user', got %q", charlie.Role)
	}
}

func TestCmdUser_AddDuplicate(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user add admin user")
	msg := env.lastSent()
	if !strings.Contains(msg, "already exists") {
		t.Errorf("expected 'already exists', got: %s", msg)
	}
}

func TestCmdUser_Remove(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	// Add a user first.
	env.handler.HandleCommand("#test", "admin", "!user add bob")
	env.sent = nil // clear

	env.handler.HandleCommand("#test", "admin", "!user remove bob")
	msg := env.lastSent()
	if !strings.Contains(msg, "removed") {
		t.Errorf("expected 'removed', got: %s", msg)
	}

	cfg, err := env.pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, ok := cfg.Users["bob"]; ok {
		t.Error("expected bob to be removed")
	}
}

func TestCmdUser_RemoveNotFound(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user remove ghost")
	msg := env.lastSent()
	if !strings.Contains(msg, "error") {
		t.Errorf("expected error, got: %s", msg)
	}
}

func TestCmdUser_SetRole(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user add bob")
	env.sent = nil

	env.handler.HandleCommand("#test", "admin", "!user bob role admin")
	msg := env.lastSent()
	if !strings.Contains(msg, "role updated") {
		t.Errorf("expected 'role updated', got: %s", msg)
	}

	cfg, err := env.pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if cfg.Users["bob"].Role != "admin" {
		t.Errorf("expected role admin, got %q", cfg.Users["bob"].Role)
	}
}

func TestCmdUser_SetTools(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user add bob")
	env.sent = nil

	env.handler.HandleCommand("#test", "admin", "!user bob tools shell,mail_read")
	msg := env.lastSent()
	if !strings.Contains(msg, "tools updated") {
		t.Errorf("expected 'tools updated', got: %s", msg)
	}

	cfg, err := env.pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	tools := cfg.Users["bob"].Tools
	if len(tools) != 2 || tools[0] != "shell" || tools[1] != "mail_read" {
		t.Errorf("expected [shell mail_read], got %v", tools)
	}
}

func TestCmdUser_SetToolsSpaceSeparated(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user add bob")
	env.sent = nil

	env.handler.HandleCommand("#test", "admin", "!user bob tools shell mail_read")
	msg := env.lastSent()
	if !strings.Contains(msg, "tools updated") {
		t.Errorf("expected 'tools updated', got: %s", msg)
	}

	cfg, err := env.pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	tools := cfg.Users["bob"].Tools
	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %v", tools)
	}
}

func TestCmdUser_SetDeny(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user add bob")
	env.sent = nil

	env.handler.HandleCommand("#test", "admin", "!user bob deny code_exec")
	msg := env.lastSent()
	if !strings.Contains(msg, "deny updated") {
		t.Errorf("expected 'deny updated', got: %s", msg)
	}

	cfg, err := env.pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	deny := cfg.Users["bob"].DenyTools
	if len(deny) != 1 || deny[0] != "code_exec" {
		t.Errorf("expected [code_exec], got %v", deny)
	}
}

func TestCmdUser_SetAutonomy(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user add bob")
	env.sent = nil

	env.handler.HandleCommand("#test", "admin", "!user bob autonomy report")
	msg := env.lastSent()
	if !strings.Contains(msg, "autonomy updated") {
		t.Errorf("expected 'autonomy updated', got: %s", msg)
	}

	cfg, err := env.pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if cfg.Users["bob"].Autonomy != "report" {
		t.Errorf("expected autonomy report, got %q", cfg.Users["bob"].Autonomy)
	}
}

func TestCmdUser_SetModel(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user add bob")
	env.sent = nil

	env.handler.HandleCommand("#test", "admin", "!user bob model openrouter,kimi")
	msg := env.lastSent()
	if !strings.Contains(msg, "model updated") {
		t.Errorf("expected 'model updated', got: %s", msg)
	}

	cfg, err := env.pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	models := cfg.Users["bob"].AllowedModels
	if len(models) != 2 {
		t.Errorf("expected 2 models, got %v", models)
	}
}

func TestCmdUser_SetRatelimit(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user add bob")
	env.sent = nil

	env.handler.HandleCommand("#test", "admin", "!user bob ratelimit 50")
	msg := env.lastSent()
	if !strings.Contains(msg, "ratelimit updated") {
		t.Errorf("expected 'ratelimit updated', got: %s", msg)
	}

	cfg, err := env.pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if cfg.Users["bob"].MaxMessagesPerHour != 50 {
		t.Errorf("expected ratelimit 50, got %d", cfg.Users["bob"].MaxMessagesPerHour)
	}
}

func TestCmdUser_SetRatelimitInvalid(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user add bob")
	env.sent = nil

	env.handler.HandleCommand("#test", "admin", "!user bob ratelimit abc")
	msg := env.lastSent()
	if !strings.Contains(msg, "must be a number") {
		t.Errorf("expected 'must be a number', got: %s", msg)
	}
}

func TestCmdUser_SetUnknownField(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user add bob")
	env.sent = nil

	env.handler.HandleCommand("#test", "admin", "!user bob foobar value")
	msg := env.lastSent()
	if !strings.Contains(msg, "unknown field") {
		t.Errorf("expected 'unknown field', got: %s", msg)
	}
}

func TestCmdUser_SetNotFound(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user ghost role admin")
	msg := env.lastSent()
	if !strings.Contains(msg, "not found") {
		t.Errorf("expected 'not found', got: %s", msg)
	}
}

func TestCmdUser_NoArgs(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user")
	msg := env.lastSent()
	if !strings.Contains(msg, "usage:") {
		t.Errorf("expected usage message, got: %s", msg)
	}
}

func TestCmdChannel_AdminOnly(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "nobody", "!channel list")
	msg := env.lastSent()
	if !strings.Contains(msg, "permission denied") {
		t.Errorf("expected permission denied, got: %s", msg)
	}
}

func TestCmdChannel_List(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	// Add a channel first.
	if err := env.pw.WriteChannel("#general", config.ChannelPermissions{Autonomy: "approve"}); err != nil {
		t.Fatalf("WriteChannel: %v", err)
	}
	// Update PM so it sees the new channel.
	cfg, _ := env.pw.Read()
	env.pm.Update(cfg)

	env.handler.HandleCommand("#test", "admin", "!channel list")
	msg := env.lastSent()
	if !strings.Contains(msg, "#general") {
		t.Errorf("expected #general in channel list, got: %s", msg)
	}
}

func TestCmdChannel_ListEmpty(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!channel list")
	msg := env.lastSent()
	if !strings.Contains(msg, "no channels configured") {
		t.Errorf("expected 'no channels configured', got: %s", msg)
	}
}

func TestCmdChannel_Info(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	if err := env.pw.WriteChannel("#general", config.ChannelPermissions{
		Tools:    []string{"shell"},
		Autonomy: "approve",
	}); err != nil {
		t.Fatalf("WriteChannel: %v", err)
	}
	cfg, _ := env.pw.Read()
	env.pm.Update(cfg)

	env.handler.HandleCommand("#test", "admin", "!channel info #general")
	msg := env.lastSent()
	if !strings.Contains(msg, "channel: #general") {
		t.Errorf("expected 'channel: #general', got: %s", msg)
	}
	if !strings.Contains(msg, "shell") {
		t.Errorf("expected 'shell' in tools, got: %s", msg)
	}
}

func TestCmdChannel_InfoNotFound(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!channel info #nonexistent")
	msg := env.lastSent()
	if !strings.Contains(msg, "not configured") {
		t.Errorf("expected 'not configured', got: %s", msg)
	}
}

func TestCmdChannel_SetTools(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!channel #general tools shell,mail_read")
	msg := env.lastSent()
	if !strings.Contains(msg, "tools updated") {
		t.Errorf("expected 'tools updated', got: %s", msg)
	}

	cfg, err := env.pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	ch, ok := cfg.Channels["#general"]
	if !ok {
		t.Fatal("expected #general in config")
	}
	if len(ch.Tools) != 2 {
		t.Errorf("expected 2 tools, got %v", ch.Tools)
	}
}

func TestCmdChannel_SetDeny(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!channel #general deny code_exec")
	msg := env.lastSent()
	if !strings.Contains(msg, "deny updated") {
		t.Errorf("expected 'deny updated', got: %s", msg)
	}

	cfg, err := env.pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(cfg.Channels["#general"].DenyTools) != 1 {
		t.Errorf("expected 1 deny tool, got %v", cfg.Channels["#general"].DenyTools)
	}
}

func TestCmdChannel_SetAutonomy(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!channel #general autonomy report")
	msg := env.lastSent()
	if !strings.Contains(msg, "autonomy updated") {
		t.Errorf("expected 'autonomy updated', got: %s", msg)
	}

	cfg, err := env.pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if cfg.Channels["#general"].Autonomy != "report" {
		t.Errorf("expected autonomy report, got %q", cfg.Channels["#general"].Autonomy)
	}
}

func TestCmdChannel_SetModel(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!channel #general model openrouter,kimi")
	msg := env.lastSent()
	if !strings.Contains(msg, "model updated") {
		t.Errorf("expected 'model updated', got: %s", msg)
	}

	cfg, err := env.pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	models := cfg.Channels["#general"].AllowedModels
	if len(models) != 2 {
		t.Errorf("expected 2 models, got %v", models)
	}
}

func TestCmdChannel_SetUnknownField(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!channel #general foobar value")
	msg := env.lastSent()
	if !strings.Contains(msg, "unknown field") {
		t.Errorf("expected 'unknown field', got: %s", msg)
	}
}

func TestCmdChannel_NoArgs(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!channel")
	msg := env.lastSent()
	if !strings.Contains(msg, "usage:") {
		t.Errorf("expected usage message, got: %s", msg)
	}
}

func TestCmdUser_SetInvalidRole(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user add bob")
	env.sent = nil

	env.handler.HandleCommand("#test", "admin", "!user bob role superadmin")
	msg := env.lastSent()
	if !strings.Contains(msg, "error") {
		t.Errorf("expected error for invalid role, got: %s", msg)
	}
}

func TestCmdUser_SetInvalidAutonomy(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!user add bob")
	env.sent = nil

	env.handler.HandleCommand("#test", "admin", "!user bob autonomy yolo")
	msg := env.lastSent()
	if !strings.Contains(msg, "error") {
		t.Errorf("expected error for invalid autonomy, got: %s", msg)
	}
}

func TestCmdChannel_SetInvalidAutonomy(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!channel #general autonomy yolo")
	msg := env.lastSent()
	if !strings.Contains(msg, "error") {
		t.Errorf("expected error for invalid autonomy, got: %s", msg)
	}
}

func TestCmdHelp_IncludesAdminCommands(t *testing.T) {
	t.Parallel()
	env := newTestAdminEnv(t)

	env.handler.HandleCommand("#test", "admin", "!help")
	msg := env.lastSent()
	if !strings.Contains(msg, "!user") {
		t.Errorf("expected !user in help, got: %s", msg)
	}
	if !strings.Contains(msg, "!channel") {
		t.Errorf("expected !channel in help, got: %s", msg)
	}
}

func TestParseCSV(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  []string
		expect []string
	}{
		{
			name:   "comma separated",
			input:  []string{"shell,mail_read,code_exec"},
			expect: []string{"shell", "mail_read", "code_exec"},
		},
		{
			name:   "space separated",
			input:  []string{"shell", "mail_read", "code_exec"},
			expect: []string{"shell", "mail_read", "code_exec"},
		},
		{
			name:   "mixed",
			input:  []string{"shell,mail_read", "code_exec"},
			expect: []string{"shell", "mail_read", "code_exec"},
		},
		{
			name:   "with spaces around commas",
			input:  []string{"shell, mail_read, code_exec"},
			expect: []string{"shell", "mail_read", "code_exec"},
		},
		{
			name:   "single value",
			input:  []string{"*"},
			expect: []string{"*"},
		},
		{
			name:   "empty values filtered",
			input:  []string{"shell,,mail_read"},
			expect: []string{"shell", "mail_read"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseCSV(tt.input)
			if len(got) != len(tt.expect) {
				t.Fatalf("got %v, want %v", got, tt.expect)
			}
			for i := range got {
				if got[i] != tt.expect[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.expect[i])
				}
			}
		})
	}
}

func TestFormatList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  []string
		expect string
	}{
		{
			name:   "empty",
			input:  nil,
			expect: "(all)",
		},
		{
			name:   "single",
			input:  []string{"shell"},
			expect: "shell",
		},
		{
			name:   "multiple",
			input:  []string{"shell", "mail_read"},
			expect: "shell, mail_read",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatList(tt.input)
			if got != tt.expect {
				t.Errorf("got %q, want %q", got, tt.expect)
			}
		})
	}
}
