package server

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"murmur/internal/config"
)

func newTestPermissionsWriter(t *testing.T) (*PermissionsWriter, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "permissions.toml")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewPermissionsWriter(path, logger), path
}

func TestPermissionsWriter_RoundTrip(t *testing.T) {
	t.Parallel()
	pw, _ := newTestPermissionsWriter(t)

	// Write a user.
	err := pw.WriteUser("alice", config.UserPermissions{
		Role:     "admin",
		Tools:    []string{"*"},
		Autonomy: "auto",
	})
	if err != nil {
		t.Fatalf("WriteUser: %v", err)
	}

	// Write a channel.
	err = pw.WriteChannel("#general", config.ChannelPermissions{
		Tools:    []string{"shell", "mail_*"},
		Autonomy: "approve",
	})
	if err != nil {
		t.Fatalf("WriteChannel: %v", err)
	}

	// Read back.
	cfg, err := pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(cfg.Users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(cfg.Users))
	}
	alice, ok := cfg.Users["alice"]
	if !ok {
		t.Fatal("expected user 'alice'")
	}
	if alice.Role != "admin" {
		t.Errorf("expected role admin, got %q", alice.Role)
	}
	if alice.Autonomy != "auto" {
		t.Errorf("expected autonomy auto, got %q", alice.Autonomy)
	}
	if len(alice.Tools) != 1 || alice.Tools[0] != "*" {
		t.Errorf("expected tools [*], got %v", alice.Tools)
	}

	if len(cfg.Channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(cfg.Channels))
	}
	ch, ok := cfg.Channels["#general"]
	if !ok {
		t.Fatal("expected channel '#general'")
	}
	if ch.Autonomy != "approve" {
		t.Errorf("expected autonomy approve, got %q", ch.Autonomy)
	}
	if len(ch.Tools) != 2 {
		t.Errorf("expected 2 tools, got %v", ch.Tools)
	}
}

func TestPermissionsWriter_WriteUser_Update(t *testing.T) {
	t.Parallel()
	pw, _ := newTestPermissionsWriter(t)

	// Write initial user.
	if err := pw.WriteUser("bob", config.UserPermissions{Role: "user", Autonomy: "approve"}); err != nil {
		t.Fatalf("WriteUser: %v", err)
	}

	// Update the same user.
	if err := pw.WriteUser("bob", config.UserPermissions{Role: "admin", Autonomy: "auto"}); err != nil {
		t.Fatalf("WriteUser update: %v", err)
	}

	cfg, err := pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(cfg.Users) != 1 {
		t.Fatalf("expected 1 user after update, got %d", len(cfg.Users))
	}
	if cfg.Users["bob"].Role != "admin" {
		t.Errorf("expected role admin after update, got %q", cfg.Users["bob"].Role)
	}
}

func TestPermissionsWriter_RemoveUser(t *testing.T) {
	t.Parallel()
	pw, _ := newTestPermissionsWriter(t)

	if err := pw.WriteUser("alice", config.UserPermissions{Role: "admin"}); err != nil {
		t.Fatalf("WriteUser: %v", err)
	}
	if err := pw.WriteUser("bob", config.UserPermissions{Role: "user"}); err != nil {
		t.Fatalf("WriteUser: %v", err)
	}

	if err := pw.RemoveUser("alice"); err != nil {
		t.Fatalf("RemoveUser: %v", err)
	}

	cfg, err := pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(cfg.Users) != 1 {
		t.Fatalf("expected 1 user after remove, got %d", len(cfg.Users))
	}
	if _, ok := cfg.Users["bob"]; !ok {
		t.Error("expected bob to remain")
	}
}

func TestPermissionsWriter_RemoveUser_CaseInsensitive(t *testing.T) {
	t.Parallel()
	pw, _ := newTestPermissionsWriter(t)

	if err := pw.WriteUser("Alice", config.UserPermissions{Role: "admin"}); err != nil {
		t.Fatalf("WriteUser: %v", err)
	}

	// Remove with different case.
	if err := pw.RemoveUser("alice"); err != nil {
		t.Fatalf("RemoveUser: %v", err)
	}

	cfg, err := pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(cfg.Users) != 0 {
		t.Errorf("expected 0 users after case-insensitive remove, got %d", len(cfg.Users))
	}
}

func TestPermissionsWriter_RemoveUser_NotFound(t *testing.T) {
	t.Parallel()
	pw, _ := newTestPermissionsWriter(t)

	err := pw.RemoveUser("ghost")
	if err == nil {
		t.Fatal("expected error for removing non-existent user")
	}
}

func TestPermissionsWriter_RemoveChannel(t *testing.T) {
	t.Parallel()
	pw, _ := newTestPermissionsWriter(t)

	if err := pw.WriteChannel("#general", config.ChannelPermissions{Autonomy: "approve"}); err != nil {
		t.Fatalf("WriteChannel: %v", err)
	}

	if err := pw.RemoveChannel("#general"); err != nil {
		t.Fatalf("RemoveChannel: %v", err)
	}

	cfg, err := pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(cfg.Channels) != 0 {
		t.Errorf("expected 0 channels after remove, got %d", len(cfg.Channels))
	}
}

func TestPermissionsWriter_RemoveChannel_NotFound(t *testing.T) {
	t.Parallel()
	pw, _ := newTestPermissionsWriter(t)

	err := pw.RemoveChannel("#nonexistent")
	if err == nil {
		t.Fatal("expected error for removing non-existent channel")
	}
}

func TestPermissionsWriter_ValidationOnWrite(t *testing.T) {
	t.Parallel()
	pw, _ := newTestPermissionsWriter(t)

	// Invalid role should fail validation.
	err := pw.WriteUser("alice", config.UserPermissions{Role: "superadmin"})
	if err == nil {
		t.Fatal("expected validation error for invalid role")
	}

	// Invalid autonomy should fail validation.
	err = pw.WriteUser("alice", config.UserPermissions{Autonomy: "yolo"})
	if err == nil {
		t.Fatal("expected validation error for invalid autonomy")
	}

	// Invalid rate limit should fail validation.
	err = pw.WriteUser("alice", config.UserPermissions{MaxMessagesPerHour: -5})
	if err == nil {
		t.Fatal("expected validation error for invalid rate limit")
	}

	// Invalid channel autonomy should fail validation.
	err = pw.WriteChannel("#test", config.ChannelPermissions{Autonomy: "invalid"})
	if err == nil {
		t.Fatal("expected validation error for invalid channel autonomy")
	}
}

func TestPermissionsWriter_ConcurrentWrites(t *testing.T) {
	t.Parallel()
	pw, _ := newTestPermissionsWriter(t)

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			nick := "user" + string(rune('a'+i))
			errs[i] = pw.WriteUser(nick, config.UserPermissions{Role: "user"})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	cfg, err := pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(cfg.Users) != n {
		t.Errorf("expected %d users, got %d", n, len(cfg.Users))
	}
}

func TestPermissionsWriter_AtomicWrite(t *testing.T) {
	t.Parallel()
	pw, path := newTestPermissionsWriter(t)

	// Write a valid user first.
	if err := pw.WriteUser("alice", config.UserPermissions{Role: "admin"}); err != nil {
		t.Fatalf("WriteUser: %v", err)
	}

	// Attempt to write an invalid user — should fail validation.
	err := pw.WriteUser("bob", config.UserPermissions{Role: "invalid"})
	if err == nil {
		t.Fatal("expected validation error")
	}

	// The file should still contain only alice (the failed write should not
	// have corrupted the file).
	cfg, err := pw.Read()
	if err != nil {
		t.Fatalf("Read after failed write: %v", err)
	}
	if len(cfg.Users) != 1 {
		t.Errorf("expected 1 user after failed write, got %d", len(cfg.Users))
	}
	if _, ok := cfg.Users["alice"]; !ok {
		t.Error("expected alice to still exist after failed write")
	}

	// Verify no temp files were left behind.
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestPermissionsWriter_ReadMissingFile(t *testing.T) {
	t.Parallel()
	pw, _ := newTestPermissionsWriter(t)

	cfg, err := pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(cfg.Users) != 0 {
		t.Errorf("expected 0 users from missing file, got %d", len(cfg.Users))
	}
	if len(cfg.Channels) != 0 {
		t.Errorf("expected 0 channels from missing file, got %d", len(cfg.Channels))
	}
}

func TestPermissionsWriter_DuplicateAPIKey(t *testing.T) {
	t.Parallel()
	pw, _ := newTestPermissionsWriter(t)

	if err := pw.WriteUser("alice", config.UserPermissions{APIKey: "key123"}); err != nil {
		t.Fatalf("WriteUser alice: %v", err)
	}

	// Writing bob with the same API key should fail validation.
	err := pw.WriteUser("bob", config.UserPermissions{APIKey: "key123"})
	if err == nil {
		t.Fatal("expected validation error for duplicate API key")
	}
}

func TestPermissionsWriter_AllFieldsPreserved(t *testing.T) {
	t.Parallel()
	pw, _ := newTestPermissionsWriter(t)

	user := config.UserPermissions{
		Role:               "admin",
		Tools:              []string{"shell", "mail_*"},
		DenyTools:          []string{"code_exec"},
		Autonomy:           "auto",
		AllowedModels:      []string{"openrouter", "kimi"},
		DenyModels:         []string{"ollama"},
		MaxMessagesPerHour: 100,
		APIKey:             "secret-key",
	}
	if err := pw.WriteUser("alice", user); err != nil {
		t.Fatalf("WriteUser: %v", err)
	}

	cfg, err := pw.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	got := cfg.Users["alice"]
	if got.Role != user.Role {
		t.Errorf("role: got %q, want %q", got.Role, user.Role)
	}
	if len(got.Tools) != len(user.Tools) {
		t.Errorf("tools: got %v, want %v", got.Tools, user.Tools)
	}
	if len(got.DenyTools) != len(user.DenyTools) {
		t.Errorf("deny_tools: got %v, want %v", got.DenyTools, user.DenyTools)
	}
	if got.Autonomy != user.Autonomy {
		t.Errorf("autonomy: got %q, want %q", got.Autonomy, user.Autonomy)
	}
	if len(got.AllowedModels) != len(user.AllowedModels) {
		t.Errorf("allowed_models: got %v, want %v", got.AllowedModels, user.AllowedModels)
	}
	if len(got.DenyModels) != len(user.DenyModels) {
		t.Errorf("deny_models: got %v, want %v", got.DenyModels, user.DenyModels)
	}
	if got.MaxMessagesPerHour != user.MaxMessagesPerHour {
		t.Errorf("max_messages_per_hour: got %d, want %d", got.MaxMessagesPerHour, user.MaxMessagesPerHour)
	}
	if got.APIKey != user.APIKey {
		t.Errorf("api_key: got %q, want %q", got.APIKey, user.APIKey)
	}
}
