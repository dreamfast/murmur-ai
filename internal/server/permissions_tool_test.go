package server

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"murmur/internal/config"
)

// testPermToolEnv holds all the components needed for permissions tool tests.
type testPermToolEnv struct {
	pm       *PermissionManager
	pw       *PermissionsWriter
	reloader *pmReloader
	logger   *slog.Logger
}

// newTestPermToolEnv creates a test environment with a permission manager,
// permissions writer, and a mock reloader that updates the PM from the file.
// The admin nick is always "admin" with role "admin".
func newTestPermToolEnv(t *testing.T) *testPermToolEnv {
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

	return &testPermToolEnv{
		pm:       pm,
		pw:       pw,
		reloader: reloader,
		logger:   logger,
	}
}

// adminCtx returns a context with the admin nick set.
func adminCtx() context.Context {
	return context.WithValue(context.Background(), requestNickKey{}, "admin")
}

// userCtx returns a context with a non-admin nick set.
func userCtx(nick string) context.Context {
	return context.WithValue(context.Background(), requestNickKey{}, nick)
}

func TestPermissionsManage_NonAdminRejected(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	args := map[string]any{"action": "list_users"}

	// No nick in context.
	_, err := handlePermissionsManage(context.Background(), args, env.pw, env.pm, env.reloader, env.logger)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("expected permission denied with no nick, got: %v", err)
	}

	// Non-admin nick in context.
	ctx := userCtx("regularuser")
	_, err = handlePermissionsManage(ctx, args, env.pw, env.pm, env.reloader, env.logger)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("expected permission denied for non-admin, got: %v", err)
	}
}

func TestPermissionsManage_ListUsers(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{"action": "list_users"}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "admin") {
		t.Errorf("expected result to contain 'admin', got: %s", result)
	}
}

func TestPermissionsManage_ListUsers_SingleUser(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Create a PM with only the admin user to verify the list format with
	// a minimal config. The admin user must exist for the admin check to pass.
	pm := NewPermissionManager(&config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"admin": {Role: "admin"},
		},
	}, logger)

	dir := t.TempDir()
	pw := NewPermissionsWriter(filepath.Join(dir, "permissions.toml"), logger)
	reloader := &pmReloader{pm: pm, pw: pw}

	result, err := handlePermissionsManage(adminCtx(), map[string]any{"action": "list_users"}, pw, pm, reloader, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "admin") {
		t.Errorf("expected result to contain 'admin', got: %s", result)
	}
}

func TestPermissionsManage_GetUser(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "get_user",
		"nick":   "admin",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "admin") {
		t.Errorf("expected result to contain 'admin', got: %s", result)
	}
	if !strings.Contains(result, "Role: admin") {
		t.Errorf("expected result to contain 'Role: admin', got: %s", result)
	}
}

func TestPermissionsManage_GetUser_NotFound(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "get_user",
		"nick":   "nobody",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "not found") {
		t.Errorf("expected 'not found' message, got: %s", result)
	}
}

func TestPermissionsManage_GetUser_MissingNick(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	_, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "get_user",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err == nil || !strings.Contains(err.Error(), "missing required argument") {
		t.Errorf("expected missing argument error, got: %v", err)
	}
}

func TestPermissionsManage_AddUser(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "add_user",
		"nick":   "alice",
		"role":   "user",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "alice") || !strings.Contains(result, "added") {
		t.Errorf("expected add confirmation, got: %s", result)
	}

	// Verify the user was actually added.
	cfg := env.pm.Config()
	if _, ok := cfg.Users["alice"]; !ok {
		t.Error("expected user 'alice' to exist after add")
	}
}

func TestPermissionsManage_AddUser_DefaultRole(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "add_user",
		"nick":   "bob",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, `"user"`) {
		t.Errorf("expected default role 'user' in result, got: %s", result)
	}

	cfg := env.pm.Config()
	bob, ok := cfg.Users["bob"]
	if !ok {
		t.Fatal("expected user 'bob' to exist")
	}
	if bob.Role != "user" {
		t.Errorf("expected role 'user', got %q", bob.Role)
	}
}

func TestPermissionsManage_AddUser_AlreadyExists(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "add_user",
		"nick":   "admin",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "already exists") {
		t.Errorf("expected 'already exists' message, got: %s", result)
	}
}

func TestPermissionsManage_RemoveUser(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	// Add a user first.
	_, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "add_user",
		"nick":   "charlie",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("add user: %v", err)
	}

	// Remove the user.
	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "remove_user",
		"nick":   "charlie",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "removed") {
		t.Errorf("expected 'removed' message, got: %s", result)
	}

	// Verify the user was removed.
	cfg := env.pm.Config()
	if _, ok := cfg.Users["charlie"]; ok {
		t.Error("expected user 'charlie' to be removed")
	}
}

func TestPermissionsManage_RemoveUser_NotFound(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	_, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "remove_user",
		"nick":   "nobody",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestPermissionsManage_SetUserTools(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "set_user_tools",
		"nick":   "admin",
		"value":  "shell,mail_read",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "tools updated") {
		t.Errorf("expected 'tools updated' message, got: %s", result)
	}

	cfg := env.pm.Config()
	admin := cfg.Users["admin"]
	if len(admin.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %v", admin.Tools)
	}
	if admin.Tools[0] != "shell" || admin.Tools[1] != "mail_read" {
		t.Errorf("expected [shell, mail_read], got %v", admin.Tools)
	}
}

func TestPermissionsManage_SetUserDeny(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "set_user_deny",
		"nick":   "admin",
		"value":  "shell",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "deny updated") {
		t.Errorf("expected 'deny updated' message, got: %s", result)
	}

	cfg := env.pm.Config()
	admin := cfg.Users["admin"]
	if len(admin.DenyTools) != 1 || admin.DenyTools[0] != "shell" {
		t.Errorf("expected deny_tools [shell], got %v", admin.DenyTools)
	}
}

func TestPermissionsManage_SetUserRole(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	// Add a regular user first.
	_, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "add_user",
		"nick":   "dave",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("add user: %v", err)
	}

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "set_user_role",
		"nick":   "dave",
		"value":  "admin",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "role updated") {
		t.Errorf("expected 'role updated' message, got: %s", result)
	}

	cfg := env.pm.Config()
	dave := cfg.Users["dave"]
	if dave.Role != "admin" {
		t.Errorf("expected role 'admin', got %q", dave.Role)
	}
}

func TestPermissionsManage_SetUserAutonomy(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "set_user_autonomy",
		"nick":   "admin",
		"value":  "approve",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "autonomy updated") {
		t.Errorf("expected 'autonomy updated' message, got: %s", result)
	}

	cfg := env.pm.Config()
	admin := cfg.Users["admin"]
	if admin.Autonomy != "approve" {
		t.Errorf("expected autonomy 'approve', got %q", admin.Autonomy)
	}
}

func TestPermissionsManage_SetUserModel(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "set_user_model",
		"nick":   "admin",
		"value":  "openai,anthropic",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "model updated") {
		t.Errorf("expected 'model updated' message, got: %s", result)
	}

	cfg := env.pm.Config()
	admin := cfg.Users["admin"]
	if len(admin.AllowedModels) != 2 {
		t.Fatalf("expected 2 models, got %v", admin.AllowedModels)
	}
}

func TestPermissionsManage_SetUserRatelimit(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "set_user_ratelimit",
		"nick":   "admin",
		"value":  "100",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "ratelimit updated") {
		t.Errorf("expected 'ratelimit updated' message, got: %s", result)
	}

	cfg := env.pm.Config()
	admin := cfg.Users["admin"]
	if admin.MaxMessagesPerHour != 100 {
		t.Errorf("expected ratelimit 100, got %d", admin.MaxMessagesPerHour)
	}
}

func TestPermissionsManage_SetUserRatelimit_Invalid(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "set_user_ratelimit",
		"nick":   "admin",
		"value":  "notanumber",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "must be a number") {
		t.Errorf("expected 'must be a number' message, got: %s", result)
	}
}

func TestPermissionsManage_SetUserField_UserNotFound(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "set_user_tools",
		"nick":   "nobody",
		"value":  "shell",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "not found") {
		t.Errorf("expected 'not found' message, got: %s", result)
	}
}

func TestPermissionsManage_ListChannels(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	// No channels initially.
	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "list_channels",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "No channels") {
		t.Errorf("expected 'No channels' message, got: %s", result)
	}
}

func TestPermissionsManage_SetChannelTools(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	// Set tools on a channel (creates it implicitly via WriteChannel).
	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action":  "set_channel_tools",
		"channel": "#general",
		"value":   "shell,note_*",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "tools updated") {
		t.Errorf("expected 'tools updated' message, got: %s", result)
	}

	// Verify the channel was created.
	cfg := env.pm.Config()
	ch, ok := cfg.Channels["#general"]
	if !ok {
		t.Fatal("expected channel '#general' to exist")
	}
	if len(ch.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %v", ch.Tools)
	}
}

func TestPermissionsManage_SetChannelDeny(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action":  "set_channel_deny",
		"channel": "#general",
		"value":   "shell",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "deny updated") {
		t.Errorf("expected 'deny updated' message, got: %s", result)
	}
}

func TestPermissionsManage_SetChannelAutonomy(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action":  "set_channel_autonomy",
		"channel": "#general",
		"value":   "report",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "autonomy updated") {
		t.Errorf("expected 'autonomy updated' message, got: %s", result)
	}

	cfg := env.pm.Config()
	ch := cfg.Channels["#general"]
	if ch.Autonomy != "report" {
		t.Errorf("expected autonomy 'report', got %q", ch.Autonomy)
	}
}

func TestPermissionsManage_SetChannelModel(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action":  "set_channel_model",
		"channel": "#general",
		"value":   "openai",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "model updated") {
		t.Errorf("expected 'model updated' message, got: %s", result)
	}

	cfg := env.pm.Config()
	ch := cfg.Channels["#general"]
	if len(ch.AllowedModels) != 1 || ch.AllowedModels[0] != "openai" {
		t.Errorf("expected models [openai], got %v", ch.AllowedModels)
	}
}

func TestPermissionsManage_GetChannel(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	// Create a channel first.
	_, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action":  "set_channel_autonomy",
		"channel": "#test",
		"value":   "approve",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action":  "get_channel",
		"channel": "#test",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "#test") {
		t.Errorf("expected result to contain '#test', got: %s", result)
	}
	if !strings.Contains(result, "approve") {
		t.Errorf("expected result to contain 'approve', got: %s", result)
	}
}

func TestPermissionsManage_GetChannel_NotFound(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action":  "get_channel",
		"channel": "#nonexistent",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "not configured") {
		t.Errorf("expected 'not configured' message, got: %s", result)
	}
}

func TestPermissionsManage_UnknownAction(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	_, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "do_something_weird",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("expected 'unknown action' error, got: %v", err)
	}
}

func TestPermissionsManage_MissingAction(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	_, err := handlePermissionsManage(adminCtx(), map[string]any{}, env.pw, env.pm, env.reloader, env.logger)
	if err == nil || !strings.Contains(err.Error(), "missing required argument") {
		t.Errorf("expected 'missing required argument' error, got: %v", err)
	}
}

func TestPermissionsManage_RegisterTool(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	registry := NewToolRegistry()
	err := RegisterPermissionsTool(registry, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("RegisterPermissionsTool: %v", err)
	}

	// Verify the tool was registered.
	tool, ok := registry.Get("permissions_manage")
	if !ok {
		t.Fatal("expected permissions_manage tool to be registered")
	}
	if tool.Name != "permissions_manage" {
		t.Errorf("expected name 'permissions_manage', got %q", tool.Name)
	}
}

func TestPermissionsManage_RegisterTool_Duplicate(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	registry := NewToolRegistry()
	if err := RegisterPermissionsTool(registry, env.pw, env.pm, env.reloader, env.logger); err != nil {
		t.Fatalf("first register: %v", err)
	}

	// Second registration should fail.
	err := RegisterPermissionsTool(registry, env.pw, env.pm, env.reloader, env.logger)
	if err == nil {
		t.Error("expected error on duplicate registration")
	}
}

func TestPermissionsManage_SetUserRole_EmptyValue(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "set_user_role",
		"nick":   "admin",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Value is required") {
		t.Errorf("expected 'Value is required' message, got: %s", result)
	}
}

func TestPermissionsManage_SetChannelAutonomy_EmptyValue(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	result, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action":  "set_channel_autonomy",
		"channel": "#test",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Value is required") {
		t.Errorf("expected 'Value is required' message, got: %s", result)
	}
}

func TestPermissionsManage_SetChannelTools_MissingChannel(t *testing.T) {
	t.Parallel()
	env := newTestPermToolEnv(t)

	_, err := handlePermissionsManage(adminCtx(), map[string]any{
		"action": "set_channel_tools",
		"value":  "shell",
	}, env.pw, env.pm, env.reloader, env.logger)
	if err == nil || !strings.Contains(err.Error(), "missing required argument") {
		t.Errorf("expected 'missing required argument' error, got: %v", err)
	}
}
