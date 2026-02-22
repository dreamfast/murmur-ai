package server

import (
	"io"
	"log/slog"
	"sync"
	"testing"

	"murmur/internal/config"
	"murmur/internal/db"
)

func newTestPermissionsStore(t *testing.T) *PermissionsStore {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	permCfg := &config.PermissionsConfig{
		Users:    make(map[string]config.UserPermissions),
		Channels: make(map[string]config.ChannelPermissions),
	}
	pm := NewPermissionManager(permCfg, logger)
	return NewPermissionsStore(database, pm, logger)
}

func TestPermissionsStore_RoundTrip(t *testing.T) {
	t.Parallel()
	ps := newTestPermissionsStore(t)

	// Write a user.
	err := ps.WriteUser("alice", config.UserPermissions{
		Role:     "admin",
		Tools:    []string{"*"},
		Autonomy: "auto",
	})
	if err != nil {
		t.Fatalf("WriteUser: %v", err)
	}

	// Write a channel.
	err = ps.WriteChannel("#general", config.ChannelPermissions{
		Tools:    []string{"shell", "mail_*"},
		Autonomy: "approve",
	})
	if err != nil {
		t.Fatalf("WriteChannel: %v", err)
	}

	// Read back.
	cfg, err := ps.Read()
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

func TestPermissionsStore_WriteUser_Update(t *testing.T) {
	t.Parallel()
	ps := newTestPermissionsStore(t)

	// Write initial user.
	if err := ps.WriteUser("bob", config.UserPermissions{Role: "user", Autonomy: "approve"}); err != nil {
		t.Fatalf("WriteUser: %v", err)
	}

	// Update the same user.
	if err := ps.WriteUser("bob", config.UserPermissions{Role: "admin", Autonomy: "auto"}); err != nil {
		t.Fatalf("WriteUser update: %v", err)
	}

	cfg, err := ps.Read()
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

func TestPermissionsStore_RemoveUser(t *testing.T) {
	t.Parallel()
	ps := newTestPermissionsStore(t)

	if err := ps.WriteUser("alice", config.UserPermissions{Role: "admin"}); err != nil {
		t.Fatalf("WriteUser: %v", err)
	}
	if err := ps.WriteUser("bob", config.UserPermissions{Role: "user"}); err != nil {
		t.Fatalf("WriteUser: %v", err)
	}

	if err := ps.RemoveUser("alice"); err != nil {
		t.Fatalf("RemoveUser: %v", err)
	}

	cfg, err := ps.Read()
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

func TestPermissionsStore_RemoveUser_NotFound(t *testing.T) {
	t.Parallel()
	ps := newTestPermissionsStore(t)

	err := ps.RemoveUser("ghost")
	if err == nil {
		t.Fatal("expected error for removing non-existent user")
	}
}

func TestPermissionsStore_RemoveChannel(t *testing.T) {
	t.Parallel()
	ps := newTestPermissionsStore(t)

	if err := ps.WriteChannel("#general", config.ChannelPermissions{Autonomy: "approve"}); err != nil {
		t.Fatalf("WriteChannel: %v", err)
	}

	if err := ps.RemoveChannel("#general"); err != nil {
		t.Fatalf("RemoveChannel: %v", err)
	}

	cfg, err := ps.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(cfg.Channels) != 0 {
		t.Errorf("expected 0 channels after remove, got %d", len(cfg.Channels))
	}
}

func TestPermissionsStore_RemoveChannel_NotFound(t *testing.T) {
	t.Parallel()
	ps := newTestPermissionsStore(t)

	err := ps.RemoveChannel("#nonexistent")
	if err == nil {
		t.Fatal("expected error for removing non-existent channel")
	}
}

func TestPermissionsStore_ConcurrentWrites(t *testing.T) {
	t.Parallel()
	ps := newTestPermissionsStore(t)

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			nick := "user" + string(rune('a'+i))
			errs[i] = ps.WriteUser(nick, config.UserPermissions{Role: "user"})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	cfg, err := ps.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(cfg.Users) != n {
		t.Errorf("expected %d users, got %d", n, len(cfg.Users))
	}
}

func TestPermissionsStore_AllFieldsPreserved(t *testing.T) {
	t.Parallel()
	ps := newTestPermissionsStore(t)

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
	if err := ps.WriteUser("alice", user); err != nil {
		t.Fatalf("WriteUser: %v", err)
	}

	cfg, err := ps.Read()
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

func TestPermissionsStore_CacheRefresh(t *testing.T) {
	t.Parallel()
	ps := newTestPermissionsStore(t)

	// Write a user.
	if err := ps.WriteUser("alice", config.UserPermissions{Role: "admin"}); err != nil {
		t.Fatalf("WriteUser: %v", err)
	}

	// The PM should now see alice as admin (cache was refreshed).
	if !ps.pm.IsAdmin("alice") {
		t.Error("expected alice to be admin after WriteUser (cache should be refreshed)")
	}

	// Remove alice.
	if err := ps.RemoveUser("alice"); err != nil {
		t.Fatalf("RemoveUser: %v", err)
	}

	// The PM should no longer see alice as admin.
	if ps.pm.IsAdmin("alice") {
		t.Error("expected alice to not be admin after RemoveUser (cache should be refreshed)")
	}
}

func TestPermissionsStore_UserExists(t *testing.T) {
	t.Parallel()
	ps := newTestPermissionsStore(t)

	exists, err := ps.UserExists("alice")
	if err != nil {
		t.Fatalf("UserExists: %v", err)
	}
	if exists {
		t.Error("expected alice to not exist")
	}

	if err := ps.WriteUser("alice", config.UserPermissions{Role: "admin"}); err != nil {
		t.Fatalf("WriteUser: %v", err)
	}

	exists, err = ps.UserExists("alice")
	if err != nil {
		t.Fatalf("UserExists: %v", err)
	}
	if !exists {
		t.Error("expected alice to exist")
	}

	// Case-insensitive.
	exists, err = ps.UserExists("ALICE")
	if err != nil {
		t.Fatalf("UserExists(ALICE): %v", err)
	}
	if !exists {
		t.Error("expected ALICE to exist (case-insensitive)")
	}
}

func TestPermissionsStore_ReadEmpty(t *testing.T) {
	t.Parallel()
	ps := newTestPermissionsStore(t)

	cfg, err := ps.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(cfg.Users) != 0 {
		t.Errorf("expected 0 users from empty DB, got %d", len(cfg.Users))
	}
	if len(cfg.Channels) != 0 {
		t.Errorf("expected 0 channels from empty DB, got %d", len(cfg.Channels))
	}
}
