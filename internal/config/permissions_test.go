package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestMatchesToolPattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		pattern  string
		toolName string
		want     bool
	}{
		{"exact match", "shell", "shell", true},
		{"exact no match", "shell", "web_search", false},
		{"wildcard matches all", "*", "anything", true},
		{"wildcard matches empty-ish", "*", "x", true},
		{"prefix glob matches", "note_*", "note_add", true},
		{"prefix glob matches exact prefix", "note_*", "note_", true},
		{"prefix glob no match", "note_*", "shell", false},
		{"prefix glob no match partial", "note_*", "not_a_note", false},
		{"empty pattern no match", "", "shell", false},
		{"empty tool no match", "shell", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := MatchesToolPattern(tt.pattern, tt.toolName)
			if got != tt.want {
				t.Errorf("MatchesToolPattern(%q, %q) = %v, want %v", tt.pattern, tt.toolName, got, tt.want)
			}
		})
	}
}

func TestMostRestrictiveAutonomy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b string
		want string
	}{
		{"both empty", "", "", ""},
		{"a empty b report", "", "report", "report"},
		{"a report b empty", "report", "", "report"},
		{"a empty b approve", "", "approve", "approve"},
		{"a auto b empty", "auto", "", "auto"},
		{"report vs approve", "report", "approve", "report"},
		{"approve vs report", "approve", "report", "report"},
		{"report vs auto", "report", "auto", "report"},
		{"auto vs report", "auto", "report", "report"},
		{"approve vs auto", "approve", "auto", "approve"},
		{"auto vs approve", "auto", "approve", "approve"},
		{"same report", "report", "report", "report"},
		{"same approve", "approve", "approve", "approve"},
		{"same auto", "auto", "auto", "auto"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := MostRestrictiveAutonomy(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("MostRestrictiveAutonomy(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestResolveEffectivePermissions(t *testing.T) {
	t.Parallel()

	allTools := []string{"shell", "web_search", "rss_read", "note_add", "note_delete", "code_exec", "file_ops"}
	allModels := []string{"openrouter", "ollama", "kimi"}

	tests := []struct {
		name      string
		user      UserPermissions
		channel   ChannelPermissions
		wantTools []string
		wantAuto  string
		wantModel []string
		wantAdmin bool
	}{
		{
			name:      "no restrictions either side",
			user:      UserPermissions{},
			channel:   ChannelPermissions{},
			wantTools: allTools,
			wantAuto:  "",
			wantModel: allModels,
		},
		{
			name:      "user wildcard, channel subset",
			user:      UserPermissions{Tools: []string{"*"}},
			channel:   ChannelPermissions{Tools: []string{"web_search", "rss_read"}},
			wantTools: []string{"rss_read", "web_search"},
			wantAuto:  "",
			wantModel: allModels,
		},
		{
			name:      "user subset, channel wildcard",
			user:      UserPermissions{Tools: []string{"shell", "web_search"}},
			channel:   ChannelPermissions{Tools: []string{"*"}},
			wantTools: []string{"shell", "web_search"},
			wantAuto:  "",
			wantModel: allModels,
		},
		{
			name:      "intersection of both",
			user:      UserPermissions{Tools: []string{"shell", "web_search", "rss_read"}},
			channel:   ChannelPermissions{Tools: []string{"web_search", "rss_read", "code_exec"}},
			wantTools: []string{"rss_read", "web_search"},
			wantAuto:  "",
			wantModel: allModels,
		},
		{
			name:      "user deny overrides allow",
			user:      UserPermissions{Tools: []string{"*"}, DenyTools: []string{"shell"}},
			channel:   ChannelPermissions{},
			wantTools: []string{"code_exec", "file_ops", "note_add", "note_delete", "rss_read", "web_search"},
			wantAuto:  "",
			wantModel: allModels,
		},
		{
			name:      "channel deny overrides allow",
			user:      UserPermissions{Tools: []string{"*"}},
			channel:   ChannelPermissions{Tools: []string{"*"}, DenyTools: []string{"shell", "code_exec"}},
			wantTools: []string{"file_ops", "note_add", "note_delete", "rss_read", "web_search"},
			wantAuto:  "",
			wantModel: allModels,
		},
		{
			name:      "prefix glob expansion",
			user:      UserPermissions{Tools: []string{"note_*", "web_search"}},
			channel:   ChannelPermissions{},
			wantTools: []string{"note_add", "note_delete", "web_search"},
			wantAuto:  "",
			wantModel: allModels,
		},
		{
			name:      "autonomy most restrictive",
			user:      UserPermissions{Autonomy: "auto"},
			channel:   ChannelPermissions{Autonomy: "approve"},
			wantTools: allTools,
			wantAuto:  "approve",
			wantModel: allModels,
		},
		{
			name:      "model intersection",
			user:      UserPermissions{AllowedModels: []string{"openrouter", "ollama"}},
			channel:   ChannelPermissions{AllowedModels: []string{"ollama", "kimi"}},
			wantTools: allTools,
			wantAuto:  "",
			wantModel: []string{"ollama"},
		},
		{
			name:      "model deny",
			user:      UserPermissions{AllowedModels: []string{"*"}, DenyModels: []string{"kimi"}},
			channel:   ChannelPermissions{},
			wantTools: allTools,
			wantAuto:  "",
			wantModel: []string{"ollama", "openrouter"},
		},
		{
			name:      "admin flag",
			user:      UserPermissions{Role: "admin", Tools: []string{"*"}},
			channel:   ChannelPermissions{},
			wantTools: allTools,
			wantAuto:  "",
			wantModel: allModels,
			wantAdmin: true,
		},
		{
			name:      "rate limit passthrough",
			user:      UserPermissions{MaxMessagesPerHour: 60},
			channel:   ChannelPermissions{},
			wantTools: allTools,
			wantAuto:  "",
			wantModel: allModels,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ep := ResolveEffectivePermissions(tt.user, tt.channel, allTools, allModels)

			// Sort expected for comparison.
			wantTools := make([]string, len(tt.wantTools))
			copy(wantTools, tt.wantTools)
			sort.Strings(wantTools)

			if !slicesEqual(ep.Tools, wantTools) {
				t.Errorf("Tools = %v, want %v", ep.Tools, wantTools)
			}
			if ep.Autonomy != tt.wantAuto {
				t.Errorf("Autonomy = %q, want %q", ep.Autonomy, tt.wantAuto)
			}

			wantModels := make([]string, len(tt.wantModel))
			copy(wantModels, tt.wantModel)
			sort.Strings(wantModels)

			if !slicesEqual(ep.Models, wantModels) {
				t.Errorf("Models = %v, want %v", ep.Models, wantModels)
			}
			if ep.IsAdmin != tt.wantAdmin {
				t.Errorf("IsAdmin = %v, want %v", ep.IsAdmin, tt.wantAdmin)
			}
			if tt.user.MaxMessagesPerHour != 0 && ep.RateLimit != tt.user.MaxMessagesPerHour {
				t.Errorf("RateLimit = %d, want %d", ep.RateLimit, tt.user.MaxMessagesPerHour)
			}
		})
	}
}

func TestResolveEffectivePermissions_AdminWildcardChannelDeny(t *testing.T) {
	t.Parallel()

	allTools := []string{"shell", "web_search", "rss_read", "code_exec"}
	allModels := []string{"openrouter"}

	user := UserPermissions{Role: "admin", Tools: []string{"*"}}
	channel := ChannelPermissions{DenyTools: []string{"shell"}}

	ep := ResolveEffectivePermissions(user, channel, allTools, allModels)

	// Admin with wildcard tools, but channel denies shell.
	for _, tool := range ep.Tools {
		if tool == "shell" {
			t.Error("shell should be denied by channel deny_tools even for admin")
		}
	}
	if !ep.IsAdmin {
		t.Error("IsAdmin should be true")
	}
	if len(ep.Tools) != 3 {
		t.Errorf("expected 3 tools, got %d: %v", len(ep.Tools), ep.Tools)
	}
}

func TestLoadPermissionsConfig_Valid(t *testing.T) {
	t.Parallel()

	content := `
[users.default]
role = "user"
tools = ["web_search", "rss_read"]
autonomy = "approve"
allowed_models = ["*"]
max_messages_per_hour = 30

[users.alice]
role = "admin"
tools = ["*"]
autonomy = "auto"
allowed_models = ["*"]
max_messages_per_hour = -1
api_key = "whk_alice_test123"

[users.bob]
role = "user"
tools = ["web_search", "rss_read", "note_*", "shell"]
deny_tools = ["file_ops"]
autonomy = "approve"
allowed_models = ["ollama"]
deny_models = ["openrouter"]
max_messages_per_hour = 60

[channels."#murmur"]
tools = ["web_search", "rss_read", "note_*"]

[channels."#dev"]
tools = ["*"]

[channels."#family"]
tools = ["web_search", "rss_read"]
deny_tools = ["shell", "code_exec", "file_ops"]
allowed_models = ["ollama"]
`
	path := writeTempFile(t, "permissions.toml", content)

	cfg, err := LoadPermissionsConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Users) != 3 {
		t.Fatalf("Users count = %d, want 3", len(cfg.Users))
	}
	if len(cfg.Channels) != 3 {
		t.Fatalf("Channels count = %d, want 3", len(cfg.Channels))
	}

	alice := cfg.Users["alice"]
	if alice.Role != "admin" {
		t.Errorf("alice.Role = %q, want %q", alice.Role, "admin")
	}
	if alice.APIKey != "whk_alice_test123" {
		t.Errorf("alice.APIKey = %q, want %q", alice.APIKey, "whk_alice_test123")
	}
	if alice.MaxMessagesPerHour != -1 {
		t.Errorf("alice.MaxMessagesPerHour = %d, want -1", alice.MaxMessagesPerHour)
	}

	bob := cfg.Users["bob"]
	if len(bob.DenyTools) != 1 || bob.DenyTools[0] != "file_ops" {
		t.Errorf("bob.DenyTools = %v, want [file_ops]", bob.DenyTools)
	}
	if len(bob.DenyModels) != 1 || bob.DenyModels[0] != "openrouter" {
		t.Errorf("bob.DenyModels = %v, want [openrouter]", bob.DenyModels)
	}

	family := cfg.Channels["#family"]
	if len(family.DenyTools) != 3 {
		t.Errorf("#family.DenyTools count = %d, want 3", len(family.DenyTools))
	}
}

func TestLoadPermissionsConfig_MissingFile(t *testing.T) {
	t.Parallel()

	cfg, err := LoadPermissionsConfig("/nonexistent/path/permissions.toml")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config for missing file")
	}
	if len(cfg.Users) != 0 {
		t.Errorf("Users count = %d, want 0", len(cfg.Users))
	}
	if len(cfg.Channels) != 0 {
		t.Errorf("Channels count = %d, want 0", len(cfg.Channels))
	}
}

func TestLoadPermissionsConfig_EmptyPath(t *testing.T) {
	t.Parallel()

	cfg, err := LoadPermissionsConfig("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config for empty path")
	}
}

func TestLoadPermissionsConfig_InvalidTOML(t *testing.T) {
	t.Parallel()

	content := `this is not valid toml [[[`
	path := writeTempFile(t, "bad.toml", content)

	_, err := LoadPermissionsConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid TOML, got nil")
	}
}

func TestLoadPermissionsConfig_InvalidRole(t *testing.T) {
	t.Parallel()

	content := `
[users.alice]
role = "superadmin"
`
	path := writeTempFile(t, "permissions.toml", content)

	_, err := LoadPermissionsConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid role, got nil")
	}
}

func TestLoadPermissionsConfig_InvalidAutonomy(t *testing.T) {
	t.Parallel()

	content := `
[users.alice]
autonomy = "yolo"
`
	path := writeTempFile(t, "permissions.toml", content)

	_, err := LoadPermissionsConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid autonomy, got nil")
	}
}

func TestLoadPermissionsConfig_InvalidChannelAutonomy(t *testing.T) {
	t.Parallel()

	content := `
[channels."#test"]
autonomy = "invalid"
`
	path := writeTempFile(t, "permissions.toml", content)

	_, err := LoadPermissionsConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid channel autonomy, got nil")
	}
}

func TestValidate_CaseDuplicateUsers(t *testing.T) {
	t.Parallel()

	cfg := &PermissionsConfig{
		Users: map[string]UserPermissions{
			"Alice": {Role: "admin"},
			"alice": {Role: "user"},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for case-duplicate users, got nil")
	}
	if !strings.Contains(err.Error(), "case-insensitive collision") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidate_CaseDuplicateChannels(t *testing.T) {
	t.Parallel()

	cfg := &PermissionsConfig{
		Channels: map[string]ChannelPermissions{
			"#Dev": {Tools: []string{"*"}},
			"#dev": {Tools: []string{"shell"}},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for case-duplicate channels, got nil")
	}
	if !strings.Contains(err.Error(), "case-insensitive collision") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidate_DuplicateAPIKey(t *testing.T) {
	t.Parallel()

	cfg := &PermissionsConfig{
		Users: map[string]UserPermissions{
			"alice": {Role: "admin", APIKey: "whk_shared_key"},
			"bob":   {Role: "user", APIKey: "whk_shared_key"},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for duplicate API key, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate api_key") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidate_InvalidRateLimit(t *testing.T) {
	t.Parallel()

	cfg := &PermissionsConfig{
		Users: map[string]UserPermissions{
			"alice": {MaxMessagesPerHour: -2},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid rate limit, got nil")
	}
	if !strings.Contains(err.Error(), "max_messages_per_hour must be >= -1") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestPermissionsConfig_GetUser_DefaultFallback(t *testing.T) {
	t.Parallel()

	cfg := &PermissionsConfig{
		Users: map[string]UserPermissions{
			"default": {
				Role:     "user",
				Tools:    []string{"web_search"},
				Autonomy: "approve",
			},
			"alice": {
				Role:     "admin",
				Tools:    []string{"*"},
				Autonomy: "auto",
			},
		},
	}

	// Known user.
	alice := cfg.GetUser("alice")
	if alice.Role != "admin" {
		t.Errorf("alice.Role = %q, want %q", alice.Role, "admin")
	}

	// Unknown user falls back to default.
	unknown := cfg.GetUser("charlie")
	if unknown.Role != "user" {
		t.Errorf("charlie.Role = %q, want %q (default)", unknown.Role, "user")
	}
	if len(unknown.Tools) != 1 || unknown.Tools[0] != "web_search" {
		t.Errorf("charlie.Tools = %v, want [web_search] (default)", unknown.Tools)
	}
}

func TestPermissionsConfig_GetUser_CaseInsensitive(t *testing.T) {
	t.Parallel()

	cfg := &PermissionsConfig{
		Users: map[string]UserPermissions{
			"Alice": {Role: "admin"},
		},
	}

	u := cfg.GetUser("alice")
	if u.Role != "admin" {
		t.Errorf("case-insensitive lookup failed: Role = %q, want %q", u.Role, "admin")
	}

	u2 := cfg.GetUser("ALICE")
	if u2.Role != "admin" {
		t.Errorf("case-insensitive lookup failed: Role = %q, want %q", u2.Role, "admin")
	}
}

func TestPermissionsConfig_GetUser_NilConfig(t *testing.T) {
	t.Parallel()

	var cfg *PermissionsConfig
	u := cfg.GetUser("anyone")
	if u.Role != "" {
		t.Errorf("nil config should return zero-value, got Role = %q", u.Role)
	}
}

func TestPermissionsConfig_GetChannel(t *testing.T) {
	t.Parallel()

	cfg := &PermissionsConfig{
		Channels: map[string]ChannelPermissions{
			"#dev": {Tools: []string{"*"}},
		},
	}

	ch := cfg.GetChannel("#dev")
	if len(ch.Tools) != 1 || ch.Tools[0] != "*" {
		t.Errorf("#dev.Tools = %v, want [*]", ch.Tools)
	}

	// Unknown channel returns zero-value.
	unknown := cfg.GetChannel("#unknown")
	if len(unknown.Tools) != 0 {
		t.Errorf("#unknown.Tools = %v, want empty", unknown.Tools)
	}
}

func TestPermissionsConfig_GetChannel_CaseInsensitive(t *testing.T) {
	t.Parallel()

	cfg := &PermissionsConfig{
		Channels: map[string]ChannelPermissions{
			"#Dev": {Tools: []string{"shell"}},
		},
	}

	ch := cfg.GetChannel("#dev")
	if len(ch.Tools) != 1 || ch.Tools[0] != "shell" {
		t.Errorf("case-insensitive lookup failed: Tools = %v, want [shell]", ch.Tools)
	}
}

func TestPermissionsConfig_HasUsers(t *testing.T) {
	t.Parallel()

	empty := &PermissionsConfig{}
	if empty.HasUsers() {
		t.Error("empty config should not have users")
	}

	withUsers := &PermissionsConfig{
		Users: map[string]UserPermissions{"alice": {}},
	}
	if !withUsers.HasUsers() {
		t.Error("config with users should have users")
	}
}

func TestEmptyPermissionsConfig_NoRestrictions(t *testing.T) {
	t.Parallel()

	allTools := []string{"shell", "web_search", "rss_read"}
	allModels := []string{"openrouter", "ollama"}

	// Empty user + empty channel = no restrictions.
	ep := ResolveEffectivePermissions(UserPermissions{}, ChannelPermissions{}, allTools, allModels)

	wantTools := make([]string, len(allTools))
	copy(wantTools, allTools)
	sort.Strings(wantTools)
	sort.Strings(ep.Tools)
	if !slicesEqual(ep.Tools, wantTools) {
		t.Errorf("Tools = %v, want %v (no restrictions)", ep.Tools, wantTools)
	}
	wantModels := make([]string, len(allModels))
	copy(wantModels, allModels)
	sort.Strings(wantModels)
	sort.Strings(ep.Models)
	if !slicesEqual(ep.Models, wantModels) {
		t.Errorf("Models = %v, want %v (no restrictions)", ep.Models, wantModels)
	}
	if ep.IsAdmin {
		t.Error("IsAdmin should be false for empty user")
	}
}

func TestLoadServerConfig_PermissionsFileDefault(t *testing.T) {
	t.Parallel()

	content := `
[server]
data_dir = "/tmp/murmur-test"

[irc]
server = "irc.example.com"
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"
`
	path := writeTempFile(t, "server.toml", content)

	cfg, err := LoadServerConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := filepath.Join("/tmp/murmur-test", "permissions.toml")
	if cfg.Security.PermissionsFile != want {
		t.Errorf("Security.PermissionsFile = %q, want %q", cfg.Security.PermissionsFile, want)
	}
}

func TestLoadServerConfig_PermissionsFileCustom(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[security]
permissions_file = "/etc/murmur/permissions.toml"
`
	path := writeTempFile(t, "server.toml", content)

	cfg, err := LoadServerConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Security.PermissionsFile != "/etc/murmur/permissions.toml" {
		t.Errorf("Security.PermissionsFile = %q, want %q", cfg.Security.PermissionsFile, "/etc/murmur/permissions.toml")
	}
}

func TestUserAPIKeyParsing(t *testing.T) {
	t.Parallel()

	content := `
[users.alice]
role = "admin"
api_key = "whk_alice_abc123"

[users.bob]
role = "user"
`
	path := writeTempFile(t, "permissions.toml", content)

	cfg, err := LoadPermissionsConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Users["alice"].APIKey != "whk_alice_abc123" {
		t.Errorf("alice.APIKey = %q, want %q", cfg.Users["alice"].APIKey, "whk_alice_abc123")
	}
	if cfg.Users["bob"].APIKey != "" {
		t.Errorf("bob.APIKey = %q, want empty", cfg.Users["bob"].APIKey)
	}
}

func TestLoadPermissionsConfig_ExpandsHome(t *testing.T) {
	t.Parallel()

	// Create a temp file and use its path — we can't test ~ expansion
	// without knowing the home dir, but we can test that a valid absolute
	// path works.
	content := `
[users.default]
role = "user"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "permissions.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	cfg, err := LoadPermissionsConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Users["default"].Role != "user" {
		t.Errorf("default.Role = %q, want %q", cfg.Users["default"].Role, "user")
	}
}

// slicesEqual compares two string slices for equality.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
