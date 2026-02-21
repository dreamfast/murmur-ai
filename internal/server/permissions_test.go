package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"murmur/internal/bus"
	"murmur/internal/config"
)

// testTools returns a slice of bus.ToolDef for testing.
func testTools() []bus.ToolDef {
	return []bus.ToolDef{
		{Name: "shell", Description: "Run commands", Parameters: json.RawMessage(`{}`)},
		{Name: "note_get", Description: "Get a note", Parameters: json.RawMessage(`{}`)},
		{Name: "note_set", Description: "Set a note", Parameters: json.RawMessage(`{}`)},
		{Name: "dns_lookup", Description: "DNS lookup", Parameters: json.RawMessage(`{}`)},
		{Name: "web_search", Description: "Search the web", Parameters: json.RawMessage(`{}`)},
	}
}

// testToolNames returns the names of the test tools.
func testToolNames() []string {
	tools := testTools()
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestFilterTools_NilManager(t *testing.T) {
	t.Parallel()

	tools := testTools()
	var pm *PermissionManager

	// Nil manager should return all tools unchanged.
	result := pm.FilterTools(tools, "user1", "#test", []string{"model1"})
	if len(result) != len(tools) {
		t.Errorf("nil PM: expected %d tools, got %d", len(tools), len(result))
	}
}

func TestFilterTools_SystemNick(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"default": {Tools: []string{"shell"}},
		},
	}
	pm := NewPermissionManager(permCfg, testLogger())

	tools := testTools()
	// System nicks (starting with _) bypass filtering.
	result := pm.FilterTools(tools, "_system", "#test", []string{"model1"})
	if len(result) != len(tools) {
		t.Errorf("system nick: expected %d tools, got %d", len(tools), len(result))
	}

	result = pm.FilterTools(tools, "_scheduler", "#test", []string{"model1"})
	if len(result) != len(tools) {
		t.Errorf("_scheduler nick: expected %d tools, got %d", len(tools), len(result))
	}
}

func TestFilterTools_AdminGetsAll(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"admin1": {Role: "admin", Tools: []string{"*"}},
		},
	}
	pm := NewPermissionManager(permCfg, testLogger())

	tools := testTools()
	result := pm.FilterTools(tools, "admin1", "#test", []string{"model1"})
	if len(result) != len(tools) {
		t.Errorf("admin: expected %d tools, got %d", len(tools), len(result))
	}
}

func TestFilterTools_UserGetsIntersection(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"user1": {Tools: []string{"shell", "note_*"}},
		},
		Channels: map[string]config.ChannelPermissions{
			"#test": {Tools: []string{"shell", "dns_lookup", "note_get"}},
		},
	}
	pm := NewPermissionManager(permCfg, testLogger())

	tools := testTools()
	result := pm.FilterTools(tools, "user1", "#test", []string{"model1"})

	// User allows: shell, note_get, note_set (via note_*)
	// Channel allows: shell, dns_lookup, note_get
	// Intersection: shell, note_get
	allowed := make(map[string]bool)
	for _, t := range result {
		allowed[t.Name] = true
	}

	if !allowed["shell"] {
		t.Error("expected shell in intersection")
	}
	if !allowed["note_get"] {
		t.Error("expected note_get in intersection")
	}
	if allowed["note_set"] {
		t.Error("note_set should NOT be in intersection (channel doesn't allow it)")
	}
	if allowed["dns_lookup"] {
		t.Error("dns_lookup should NOT be in intersection (user doesn't allow it)")
	}
	if allowed["web_search"] {
		t.Error("web_search should NOT be in intersection")
	}
}

func TestFilterTools_DenyListWins(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"user1": {
				Tools:     []string{"*"},
				DenyTools: []string{"shell"},
			},
		},
	}
	pm := NewPermissionManager(permCfg, testLogger())

	tools := testTools()
	result := pm.FilterTools(tools, "user1", "#test", []string{"model1"})

	allowed := make(map[string]bool)
	for _, td := range result {
		allowed[td.Name] = true
	}

	if allowed["shell"] {
		t.Error("shell should be denied by deny_tools")
	}
	if !allowed["note_get"] {
		t.Error("note_get should be allowed")
	}
	if !allowed["dns_lookup"] {
		t.Error("dns_lookup should be allowed")
	}
}

func TestIsAdmin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		nick     string
		permCfg  *config.PermissionsConfig
		wantBool bool
	}{
		{
			name:     "nil manager",
			nick:     "anyone",
			permCfg:  nil,
			wantBool: false,
		},
		{
			name: "admin user",
			nick: "admin1",
			permCfg: &config.PermissionsConfig{
				Users: map[string]config.UserPermissions{
					"admin1": {Role: "admin"},
				},
			},
			wantBool: true,
		},
		{
			name: "regular user",
			nick: "user1",
			permCfg: &config.PermissionsConfig{
				Users: map[string]config.UserPermissions{
					"user1": {Role: "user"},
				},
			},
			wantBool: false,
		},
		{
			name: "case insensitive",
			nick: "Admin1",
			permCfg: &config.PermissionsConfig{
				Users: map[string]config.UserPermissions{
					"admin1": {Role: "admin"},
				},
			},
			wantBool: true,
		},
		{
			name: "unknown user falls back to default",
			nick: "stranger",
			permCfg: &config.PermissionsConfig{
				Users: map[string]config.UserPermissions{
					"default": {Role: "user"},
				},
			},
			wantBool: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var pm *PermissionManager
			if tt.permCfg != nil {
				pm = NewPermissionManager(tt.permCfg, testLogger())
			}
			got := pm.IsAdmin(tt.nick)
			if got != tt.wantBool {
				t.Errorf("IsAdmin(%q) = %v, want %v", tt.nick, got, tt.wantBool)
			}
		})
	}
}

func TestCheckRateLimit(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"limited": {MaxMessagesPerHour: 3},
			"admin1":  {Role: "admin", MaxMessagesPerHour: -1},
		},
	}
	pm := NewPermissionManager(permCfg, testLogger())

	// First 3 calls should be allowed.
	for i := 0; i < 3; i++ {
		if !pm.CheckRateLimit("limited") {
			t.Errorf("call %d should be allowed", i+1)
		}
	}

	// 4th call should be denied.
	if pm.CheckRateLimit("limited") {
		t.Error("4th call should be rate limited")
	}

	// Admin with -1 should always be allowed.
	for i := 0; i < 100; i++ {
		if !pm.CheckRateLimit("admin1") {
			t.Errorf("admin call %d should be allowed (unlimited)", i+1)
		}
	}
}

func TestCheckRateLimit_NilManager(t *testing.T) {
	t.Parallel()

	var pm *PermissionManager
	if !pm.CheckRateLimit("anyone") {
		t.Error("nil PM should always allow")
	}
}

func TestCheckRateLimit_DefaultFallback(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"default": {MaxMessagesPerHour: 2},
			"user1":   {}, // MaxMessagesPerHour = 0 (not set), should fall back to default
		},
	}
	pm := NewPermissionManager(permCfg, testLogger())

	// user1 should inherit the default rate limit of 2.
	if !pm.CheckRateLimit("user1") {
		t.Error("call 1 should be allowed")
	}
	if !pm.CheckRateLimit("user1") {
		t.Error("call 2 should be allowed")
	}
	if pm.CheckRateLimit("user1") {
		t.Error("call 3 should be rate limited (default limit is 2)")
	}
}

func TestCheckRateLimit_Cleanup(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"user1": {MaxMessagesPerHour: 5},
		},
	}
	pm := NewPermissionManager(permCfg, testLogger())

	// Add some rate limit entries.
	for i := 0; i < 3; i++ {
		pm.CheckRateLimit("user1")
	}

	// Verify entries exist.
	pm.rateMu.Lock()
	if len(pm.rateHits["user1"]) != 3 {
		t.Errorf("expected 3 rate hits, got %d", len(pm.rateHits["user1"]))
	}
	pm.rateMu.Unlock()

	// Run cleanup — entries are recent so they should NOT be removed.
	pm.cleanupRateLimits()

	pm.rateMu.Lock()
	if len(pm.rateHits["user1"]) != 3 {
		t.Errorf("after cleanup: expected 3 rate hits (recent), got %d", len(pm.rateHits["user1"]))
	}
	pm.rateMu.Unlock()

	// Manually set entries to be old (> 1 hour ago).
	pm.rateMu.Lock()
	oldTime := time.Now().Add(-2 * time.Hour)
	pm.rateHits["user1"] = []time.Time{oldTime, oldTime, oldTime}
	pm.rateMu.Unlock()

	// Run cleanup — old entries should be removed.
	pm.cleanupRateLimits()

	pm.rateMu.Lock()
	if len(pm.rateHits["user1"]) != 0 {
		t.Errorf("after cleanup of old entries: expected 0 rate hits, got %d", len(pm.rateHits["user1"]))
	}
	// The key itself should be deleted when all entries are removed.
	if _, exists := pm.rateHits["user1"]; exists {
		t.Error("expected user1 key to be deleted after all entries cleaned up")
	}
	pm.rateMu.Unlock()
}

func TestIsModelAllowed(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"user1": {AllowedModels: []string{"gpt-4", "claude"}},
		},
	}
	pm := NewPermissionManager(permCfg, testLogger())

	allTools := testToolNames()
	allModels := []string{"gpt-4", "claude", "llama"}

	if !pm.IsModelAllowed("user1", "#test", "gpt-4", allTools, allModels) {
		t.Error("gpt-4 should be allowed for user1")
	}
	if !pm.IsModelAllowed("user1", "#test", "claude", allTools, allModels) {
		t.Error("claude should be allowed for user1")
	}
	if pm.IsModelAllowed("user1", "#test", "llama", allTools, allModels) {
		t.Error("llama should NOT be allowed for user1")
	}
}

func TestIsModelAllowed_NilManager(t *testing.T) {
	t.Parallel()

	var pm *PermissionManager
	if !pm.IsModelAllowed("anyone", "#test", "any-model", nil, nil) {
		t.Error("nil PM should allow all models")
	}
}

func TestIsModelAllowed_SystemNick(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"default": {AllowedModels: []string{"gpt-4"}},
		},
	}
	pm := NewPermissionManager(permCfg, testLogger())

	// System nicks bypass model checks.
	if !pm.IsModelAllowed("_system", "#test", "llama", nil, []string{"gpt-4", "llama"}) {
		t.Error("system nick should bypass model checks")
	}
}

func TestGetUserByAPIKey(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"user1": {APIKey: "key-abc-123"},
			"user2": {APIKey: "key-def-456"},
			"user3": {}, // no API key
		},
	}
	pm := NewPermissionManager(permCfg, testLogger())

	tests := []struct {
		name     string
		apiKey   string
		wantNick string
	}{
		{"valid key user1", "key-abc-123", "user1"},
		{"valid key user2", "key-def-456", "user2"},
		{"invalid key", "key-wrong", ""},
		{"empty key", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := pm.GetUserByAPIKey(tt.apiKey)
			if got != tt.wantNick {
				t.Errorf("GetUserByAPIKey(%q) = %q, want %q", tt.apiKey, got, tt.wantNick)
			}
		})
	}
}

func TestGetUserByAPIKey_NilManager(t *testing.T) {
	t.Parallel()

	var pm *PermissionManager
	if got := pm.GetUserByAPIKey("any-key"); got != "" {
		t.Errorf("nil PM: GetUserByAPIKey = %q, want empty", got)
	}
}

func TestPermissionCache(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"user1": {Tools: []string{"shell", "dns_lookup"}},
		},
	}
	pm := NewPermissionManager(permCfg, testLogger())

	allTools := testToolNames()
	allModels := []string{"model1"}

	// First call populates cache.
	ep1 := pm.GetEffective("user1", "#test", allTools, allModels)

	// Second call should return cached result.
	ep2 := pm.GetEffective("user1", "#test", allTools, allModels)

	if len(ep1.Tools) != len(ep2.Tools) {
		t.Errorf("cached result differs: %d vs %d tools", len(ep1.Tools), len(ep2.Tools))
	}

	// Verify cache has an entry.
	pm.cacheMu.RLock()
	cacheLen := len(pm.cache)
	pm.cacheMu.RUnlock()
	if cacheLen != 1 {
		t.Errorf("expected 1 cache entry, got %d", cacheLen)
	}

	// Invalidate cache.
	pm.InvalidateCache()

	pm.cacheMu.RLock()
	cacheLen = len(pm.cache)
	pm.cacheMu.RUnlock()
	if cacheLen != 0 {
		t.Errorf("expected 0 cache entries after invalidation, got %d", cacheLen)
	}
}

func TestPermissionCache_InvalidatedOnToolContentChange(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"user1": {Tools: []string{"*"}},
		},
	}
	pm := NewPermissionManager(permCfg, testLogger())

	allModels := []string{"model1"}

	// First call with 3 tools.
	ep1 := pm.GetEffective("user1", "#test", []string{"a", "b", "c"}, allModels)
	if len(ep1.Tools) != 3 {
		t.Errorf("expected 3 tools, got %d", len(ep1.Tools))
	}

	// Second call with different tools — cache should miss because tool content changed.
	ep2 := pm.GetEffective("user1", "#test", []string{"a", "b", "c", "d", "e"}, allModels)
	if len(ep2.Tools) != 5 {
		t.Errorf("expected 5 tools after cache miss, got %d", len(ep2.Tools))
	}
}

func TestUpdate(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"user1": {Role: "user"},
		},
	}
	pm := NewPermissionManager(permCfg, testLogger())

	if pm.IsAdmin("user1") {
		t.Error("user1 should not be admin initially")
	}

	// Update config to make user1 admin.
	newCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"user1": {Role: "admin"},
		},
	}
	pm.Update(newCfg)

	if !pm.IsAdmin("user1") {
		t.Error("user1 should be admin after update")
	}

	// Verify cache was invalidated.
	pm.cacheMu.RLock()
	cacheLen := len(pm.cache)
	pm.cacheMu.RUnlock()
	if cacheLen != 0 {
		t.Errorf("expected 0 cache entries after update, got %d", cacheLen)
	}
}

func TestUpdate_NilConfig(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"user1": {Role: "admin"},
		},
	}
	pm := NewPermissionManager(permCfg, testLogger())

	// Update with nil should reset to empty config.
	pm.Update(nil)

	if pm.IsAdmin("user1") {
		t.Error("user1 should not be admin after nil update")
	}
}

func TestConfig(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"user1": {Role: "admin"},
		},
	}
	pm := NewPermissionManager(permCfg, testLogger())

	cfg := pm.Config()
	if len(cfg.Users) != 1 {
		t.Errorf("expected 1 user in config, got %d", len(cfg.Users))
	}
}

func TestConfig_NilManager(t *testing.T) {
	t.Parallel()

	var pm *PermissionManager
	cfg := pm.Config()
	if cfg == nil {
		t.Fatal("nil PM should return non-nil empty config")
	}
	if len(cfg.Users) != 0 {
		t.Errorf("nil PM config should have 0 users, got %d", len(cfg.Users))
	}
}

func TestStartCleanup_StopsOnCancel(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{}
	pm := NewPermissionManager(permCfg, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	pm.StartCleanup(ctx)

	// Cancel should stop the goroutine without blocking.
	cancel()

	// Give the goroutine a moment to exit.
	time.Sleep(50 * time.Millisecond)
}

func TestNewPermissionManager_NilConfig(t *testing.T) {
	t.Parallel()

	pm := NewPermissionManager(nil, testLogger())
	if pm == nil {
		t.Fatal("expected non-nil PM with nil config")
	}

	// Should not panic and should return empty results.
	if pm.IsAdmin("anyone") {
		t.Error("nil config should not have admins")
	}
}

func TestGetEffective_RateLimitDefault(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"default": {MaxMessagesPerHour: 10},
			"user1":   {}, // rate limit = 0 (not set)
		},
	}
	pm := NewPermissionManager(permCfg, testLogger())

	ep := pm.GetEffective("user1", "#test", testToolNames(), []string{"model1"})
	if ep.RateLimit != 10 {
		t.Errorf("expected rate limit 10 (from default), got %d", ep.RateLimit)
	}
}

func TestGetEffective_ExplicitRateLimit(t *testing.T) {
	t.Parallel()

	permCfg := &config.PermissionsConfig{
		Users: map[string]config.UserPermissions{
			"default": {MaxMessagesPerHour: 10},
			"user1":   {MaxMessagesPerHour: 50},
		},
	}
	pm := NewPermissionManager(permCfg, testLogger())

	ep := pm.GetEffective("user1", "#test", testToolNames(), []string{"model1"})
	if ep.RateLimit != 50 {
		t.Errorf("expected rate limit 50 (explicit), got %d", ep.RateLimit)
	}
}
