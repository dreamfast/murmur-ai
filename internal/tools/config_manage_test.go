package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testTOMLConfig = `[server]
data_dir = "~/.murmur"

[irc]
server = "irc.example.com"
port = 6697
tls = true
nick = "murmur"
password = ""
nickserv_password = ""

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[llm]
default = "openrouter-claude"

[llm.providers.openrouter-claude]
api_base = "https://openrouter.ai/api/v1"
api_key = "vault:openrouter-key"
model = "anthropic/claude-sonnet-4-5"
max_tokens = 8192
temperature = 0.7

[memory]
db_path = "~/.murmur/memory.db"
max_history = 100

[security]
allowed_users = ["yournick"]
require_nickserv = true
bus_key = ""

[vault]
enabled = false
db_path = "~/.murmur/vault.db"

[tools.shell]
enabled = true
docker_image = "ubuntu:24.04"
timeout = "30s"
`

// writeTestConfig writes a test TOML config to a temp file and returns the path.
func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	return path
}

func TestConfigManage_Read(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, testTOMLConfig)
	cfg := ConfigManageToolConfig{ConfigPath: path}
	tool := NewConfigManageTool(cfg)

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "read",
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// Should contain config content.
	if !strings.Contains(result, "[server]") {
		t.Error("expected [server] section in output")
	}
	if !strings.Contains(result, "[irc]") {
		t.Error("expected [irc] section in output")
	}

	// Vault values should be masked.
	if strings.Contains(result, "openrouter-key") {
		t.Error("expected vault value to be masked, but found 'openrouter-key'")
	}
	if !strings.Contains(result, "vault:****") {
		t.Error("expected masked vault value 'vault:****'")
	}
}

func TestConfigManage_ReadSection(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, testTOMLConfig)

	result, err := handleConfigManage(context.Background(), map[string]any{
		"action":  "read_section",
		"section": "irc",
	}, path, nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "[irc]") {
		t.Error("expected [irc] header in output")
	}
	if !strings.Contains(result, "irc.example.com") {
		t.Error("expected server value in output")
	}
}

func TestConfigManage_ReadSectionNested(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, testTOMLConfig)

	result, err := handleConfigManage(context.Background(), map[string]any{
		"action":  "read_section",
		"section": "irc.channels",
	}, path, nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "#murmur") {
		t.Error("expected channel value in output")
	}
}

func TestConfigManage_ReadSectionNotFound(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, testTOMLConfig)

	_, err := handleConfigManage(context.Background(), map[string]any{
		"action":  "read_section",
		"section": "nonexistent",
	}, path, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent section")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got %q", err.Error())
	}
}

func TestConfigManage_Set(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, testTOMLConfig)

	// Set a string value.
	result, err := handleConfigManage(context.Background(), map[string]any{
		"action": "set",
		"key":    "irc.nick",
		"value":  "newbot",
	}, path, nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	if !strings.Contains(result, "Set irc.nick") {
		t.Error("expected confirmation message")
	}
	if !strings.Contains(result, "restart is required") {
		t.Error("expected restart hint")
	}

	// Verify the file was updated.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), `nick = "newbot"`) {
		t.Errorf("expected nick to be updated, got:\n%s", string(data))
	}
}

func TestConfigManage_SetBoolean(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, testTOMLConfig)

	_, err := handleConfigManage(context.Background(), map[string]any{
		"action": "set",
		"key":    "irc.tls",
		"value":  "false",
	}, path, nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "tls = false") {
		t.Errorf("expected tls to be set to false, got:\n%s", string(data))
	}
}

func TestConfigManage_SetInteger(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, testTOMLConfig)

	_, err := handleConfigManage(context.Background(), map[string]any{
		"action": "set",
		"key":    "irc.port",
		"value":  "6667",
	}, path, nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "port = 6667") {
		t.Errorf("expected port to be set to 6667, got:\n%s", string(data))
	}
}

func TestConfigManage_SetDenied_Security(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, testTOMLConfig)

	tests := []struct {
		name string
		key  string
	}{
		{"security.allowed_users", "security.allowed_users"},
		{"security.bus_key", "security.bus_key"},
		{"vault.enabled", "vault.enabled"},
		{"vault.db_path", "vault.db_path"},
		{"irc.password", "irc.password"},
		{"irc.nickserv_password", "irc.nickserv_password"},
		{"llm.providers.openrouter.api_key", "llm.providers.openrouter.api_key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := handleConfigManage(context.Background(), map[string]any{
				"action": "set",
				"key":    tt.key,
				"value":  "hacked",
			}, path, nil)
			if err == nil {
				t.Fatalf("expected error for denied key %q", tt.key)
			}
			if !strings.Contains(err.Error(), "protected") {
				t.Errorf("expected 'protected' in error for key %q, got %q", tt.key, err.Error())
			}
		})
	}
}

func TestConfigManage_SetDenied_VaultPrefix(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, testTOMLConfig)

	_, err := handleConfigManage(context.Background(), map[string]any{
		"action": "set",
		"key":    "irc.nick",
		"value":  "vault:stolen-secret",
	}, path, nil)
	if err == nil {
		t.Fatal("expected error for vault: prefixed value")
	}
	if !strings.Contains(err.Error(), "vault:") {
		t.Errorf("expected 'vault:' in error, got %q", err.Error())
	}
}

func TestConfigManage_SetKeyNotFound(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, testTOMLConfig)

	_, err := handleConfigManage(context.Background(), map[string]any{
		"action": "set",
		"key":    "irc.nonexistent_key",
		"value":  "test",
	}, path, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent key")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got %q", err.Error())
	}
}

func TestConfigManage_MaskVault(t *testing.T) {
	t.Parallel()

	input := `api_key = "vault:my-secret-key"
other = "normal"
another = "vault:another-secret"`

	masked := maskVaultValues(input)

	if strings.Contains(masked, "my-secret-key") {
		t.Error("expected vault value to be masked")
	}
	if strings.Contains(masked, "another-secret") {
		t.Error("expected second vault value to be masked")
	}
	if !strings.Contains(masked, "vault:****") {
		t.Error("expected masked vault value")
	}
	if !strings.Contains(masked, "normal") {
		t.Error("expected non-vault value to be preserved")
	}
}

func TestConfigManage_ListSections(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, testTOMLConfig)

	result, err := handleConfigManage(context.Background(), map[string]any{
		"action": "list_sections",
	}, path, nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	for _, section := range []string{"server", "irc", "llm", "memory", "security", "vault", "tools"} {
		if !strings.Contains(result, section) {
			t.Errorf("expected section %q in output", section)
		}
	}
}

func TestConfigManage_AtomicWrite(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, testTOMLConfig)

	// Get original content.
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}

	// Set a value.
	_, err = handleConfigManage(context.Background(), map[string]any{
		"action": "set",
		"key":    "memory.max_history",
		"value":  "200",
	}, path, nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// Verify the file was updated.
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read updated: %v", err)
	}

	if !strings.Contains(string(updated), "max_history = 200") {
		t.Error("expected max_history to be updated to 200")
	}

	// Verify the rest of the file is intact.
	if !strings.Contains(string(updated), "[server]") {
		t.Error("expected [server] section to be preserved")
	}

	// Verify no temp files left behind.
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", entry.Name())
		}
	}

	// Verify original and updated are different.
	if string(original) == string(updated) {
		t.Error("expected config to be changed")
	}
}

func TestConfigManage_UnknownAction(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, testTOMLConfig)

	_, err := handleConfigManage(context.Background(), map[string]any{
		"action": "invalid",
	}, path, nil)
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("expected 'unknown action' in error, got %q", err.Error())
	}
}

func TestConfigManage_MissingAction(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, testTOMLConfig)
	cfg := ConfigManageToolConfig{ConfigPath: path}
	tool := NewConfigManageTool(cfg)

	_, err := tool.Handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing action")
	}
	if !strings.Contains(err.Error(), "missing required argument") {
		t.Errorf("expected 'missing required argument' error, got %q", err.Error())
	}
}

func TestConfigManage_IsConfigKeyDenied(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key    string
		denied bool
	}{
		{"security.allowed_users", true},
		{"security.bus_key", true},
		{"vault.enabled", true},
		{"vault.db_path", true},
		{"irc.password", true},
		{"irc.nickserv_password", true},
		{"llm.providers.openrouter.api_key", true},
		{"llm.providers.kimi.api_key", true},
		{"irc.nick", false},
		{"irc.server", false},
		{"memory.max_history", false},
		{"tools.shell.enabled", false},
		{"llm.default", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()
			got := isConfigKeyDenied(tt.key)
			if got != tt.denied {
				t.Errorf("isConfigKeyDenied(%q) = %v, want %v", tt.key, got, tt.denied)
			}
		})
	}
}

func TestConfigManage_SetInNestedSection(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, testTOMLConfig)

	_, err := handleConfigManage(context.Background(), map[string]any{
		"action": "set",
		"key":    "tools.shell.enabled",
		"value":  "false",
	}, path, nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "enabled = false") {
		t.Errorf("expected enabled to be set to false in [tools.shell], got:\n%s", string(data))
	}
}

func TestConfigManage_SetIgnoresCommentedSections(t *testing.T) {
	t.Parallel()

	// Config with a commented section header that matches.
	config := `# See [irc] for IRC settings
[server]
data_dir = "~/.murmur"

[irc]
server = "irc.example.com"
nick = "murmur"
`
	path := writeTestConfig(t, config)

	_, err := handleConfigManage(context.Background(), map[string]any{
		"action": "set",
		"key":    "irc.nick",
		"value":  "newbot",
	}, path, nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `nick = "newbot"`) {
		t.Errorf("expected nick to be updated, got:\n%s", content)
	}
	// The comment should still be there.
	if !strings.Contains(content, "# See [irc]") {
		t.Error("expected comment to be preserved")
	}
}

func TestConfigManage_ReadSectionFullyQualifiedHeaders(t *testing.T) {
	t.Parallel()

	path := writeTestConfig(t, testTOMLConfig)

	result, err := handleConfigManage(context.Background(), map[string]any{
		"action":  "read_section",
		"section": "irc",
	}, path, nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// Nested channels section should have fully qualified header.
	if !strings.Contains(result, "[irc.channels]") {
		t.Errorf("expected fully qualified [irc.channels] header, got:\n%s", result)
	}
}

func TestConfigManage_CommentsPreserved(t *testing.T) {
	t.Parallel()

	config := `[server]
data_dir = "~/.murmur"
# This is a comment
system_prompt_file = "~/.murmur/system_prompt.md"
`
	path := writeTestConfig(t, config)

	_, err := handleConfigManage(context.Background(), map[string]any{
		"action": "set",
		"key":    "server.data_dir",
		"value":  "/new/path",
	}, path, nil)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "# This is a comment") {
		t.Error("expected comment to be preserved")
	}
	if !strings.Contains(content, `data_dir = "/new/path"`) {
		t.Errorf("expected data_dir to be updated, got:\n%s", content)
	}
}

func TestConfigManage_FormatTOMLSetValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected string
	}{
		{"true", "true"},
		{"false", "false"},
		{"42", "42"},
		{"-10", "-10"},
		{"3.14", "3.14"},
		{"hello", `"hello"`},
		{"hello world", `"hello world"`},
		{"", `""`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := formatTOMLSetValue(tt.input)
			if got != tt.expected {
				t.Errorf("formatTOMLSetValue(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
