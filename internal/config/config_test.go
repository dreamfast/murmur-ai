package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadServerConfig_Valid(t *testing.T) {
	t.Parallel()

	content := `
[server]
data_dir = "/tmp/murmur"

[irc]
server = "irc.example.com"
port = 6697
tls = true
nick = "murmur"
user = "murmur"
realname = "Murmur Agent"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[scheduler]
enabled = true
heartbeat_interval = "5m"
client_timeout = "2m"

[security]
allowed_users = ["admin"]
require_nickserv = true
`
	path := writeTempFile(t, "server.toml", content)

	cfg, err := LoadServerConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Server.DataDir != "/tmp/murmur" {
		t.Errorf("DataDir = %q, want %q", cfg.Server.DataDir, "/tmp/murmur")
	}
	if cfg.IRC.Server != "irc.example.com" {
		t.Errorf("IRC.Server = %q, want %q", cfg.IRC.Server, "irc.example.com")
	}
	if cfg.IRC.Port != 6697 {
		t.Errorf("IRC.Port = %d, want %d", cfg.IRC.Port, 6697)
	}
	if !cfg.IRC.TLS {
		t.Error("IRC.TLS = false, want true")
	}
	if cfg.IRC.Nick != "murmur" {
		t.Errorf("IRC.Nick = %q, want %q", cfg.IRC.Nick, "murmur")
	}
	if cfg.IRC.Channels.Main != "#murmur" {
		t.Errorf("Channels.Main = %q, want %q", cfg.IRC.Channels.Main, "#murmur")
	}
	if cfg.IRC.Channels.Bus != "#murmur-bus" {
		t.Errorf("Channels.Bus = %q, want %q", cfg.IRC.Channels.Bus, "#murmur-bus")
	}
	if !cfg.Scheduler.Enabled {
		t.Error("Scheduler.Enabled = false, want true")
	}
	if len(cfg.Security.AllowedUsers) != 1 || cfg.Security.AllowedUsers[0] != "admin" {
		t.Errorf("Security.AllowedUsers = %v, want [admin]", cfg.Security.AllowedUsers)
	}
	if !cfg.Security.RequireNickServ {
		t.Error("Security.RequireNickServ = false, want true")
	}
}

func TestLoadServerConfig_MissingIRCServer(t *testing.T) {
	t.Parallel()

	content := `
[irc]
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"
`
	path := writeTempFile(t, "server.toml", content)

	_, err := LoadServerConfig(path)
	if err == nil {
		t.Fatal("expected error for missing irc.server, got nil")
	}
}

func TestLoadServerConfig_MissingNick(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"
`
	path := writeTempFile(t, "server.toml", content)

	_, err := LoadServerConfig(path)
	if err == nil {
		t.Fatal("expected error for missing irc.nick, got nil")
	}
}

func TestLoadServerConfig_MissingChannels(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"
nick = "murmur"
`
	path := writeTempFile(t, "server.toml", content)

	_, err := LoadServerConfig(path)
	if err == nil {
		t.Fatal("expected error for missing channels, got nil")
	}
}

func TestLoadServerConfig_InvalidDuration(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[scheduler]
heartbeat_interval = "not-a-duration"
`
	path := writeTempFile(t, "server.toml", content)

	_, err := LoadServerConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid duration, got nil")
	}
}

func TestLoadServerConfig_DefaultPort(t *testing.T) {
	t.Parallel()

	content := `
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
	if cfg.IRC.Port != 6697 {
		t.Errorf("IRC.Port = %d, want default 6697", cfg.IRC.Port)
	}
}

func TestServerConfig_ParseScheduler(t *testing.T) {
	t.Parallel()

	cfg := &ServerConfig{
		Scheduler: SchedulerConfig{
			HeartbeatInterval: "10m",
			ClientTimeout:     "3m",
		},
	}

	parsed, err := cfg.ParseScheduler()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.HeartbeatInterval.Minutes() != 10 {
		t.Errorf("HeartbeatInterval = %v, want 10m", parsed.HeartbeatInterval)
	}
	if parsed.ClientTimeout.Minutes() != 3 {
		t.Errorf("ClientTimeout = %v, want 3m", parsed.ClientTimeout)
	}
}

func TestServerConfig_ParseScheduler_Defaults(t *testing.T) {
	t.Parallel()

	cfg := &ServerConfig{}

	parsed, err := cfg.ParseScheduler()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.HeartbeatInterval.Minutes() != 5 {
		t.Errorf("HeartbeatInterval = %v, want default 5m", parsed.HeartbeatInterval)
	}
	if parsed.ClientTimeout.Minutes() != 2 {
		t.Errorf("ClientTimeout = %v, want default 2m", parsed.ClientTimeout)
	}
}

func TestLoadClientConfig_Valid(t *testing.T) {
	t.Parallel()

	content := `
[client]
id = "laptop-home"
hostname = "thinkpad"
autonomy = "report"

[irc]
server = "irc.example.com"
port = 6697
tls = true
nick = "murmur-laptop"
bus_channel = "#murmur-bus"

[heartbeat]
interval = "30s"
`
	path := writeTempFile(t, "client.toml", content)

	cfg, err := LoadClientConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Client.ID != "laptop-home" {
		t.Errorf("Client.ID = %q, want %q", cfg.Client.ID, "laptop-home")
	}
	if cfg.Client.Hostname != "thinkpad" {
		t.Errorf("Client.Hostname = %q, want %q", cfg.Client.Hostname, "thinkpad")
	}
	if cfg.Client.Autonomy != "report" {
		t.Errorf("Client.Autonomy = %q, want %q", cfg.Client.Autonomy, "report")
	}
	if cfg.IRC.Server != "irc.example.com" {
		t.Errorf("IRC.Server = %q, want %q", cfg.IRC.Server, "irc.example.com")
	}
	if cfg.IRC.BusChannel != "#murmur-bus" {
		t.Errorf("IRC.BusChannel = %q, want %q", cfg.IRC.BusChannel, "#murmur-bus")
	}
	if cfg.Heartbeat.Interval != "30s" {
		t.Errorf("Heartbeat.Interval = %q, want %q", cfg.Heartbeat.Interval, "30s")
	}
}

func TestLoadClientConfig_MissingClientID(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"
nick = "murmur-laptop"
bus_channel = "#murmur-bus"
`
	path := writeTempFile(t, "client.toml", content)

	_, err := LoadClientConfig(path)
	if err == nil {
		t.Fatal("expected error for missing client.id, got nil")
	}
}

func TestLoadClientConfig_InvalidAutonomy(t *testing.T) {
	t.Parallel()

	content := `
[client]
id = "test"
autonomy = "yolo"

[irc]
server = "irc.example.com"
nick = "murmur-test"
bus_channel = "#murmur-bus"
`
	path := writeTempFile(t, "client.toml", content)

	_, err := LoadClientConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid autonomy, got nil")
	}
}

func TestLoadClientConfig_DefaultAutonomy(t *testing.T) {
	t.Parallel()

	content := `
[client]
id = "test"

[irc]
server = "irc.example.com"
nick = "murmur-test"
bus_channel = "#murmur-bus"
`
	path := writeTempFile(t, "client.toml", content)

	cfg, err := LoadClientConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Client.Autonomy != "report" {
		t.Errorf("Client.Autonomy = %q, want default %q", cfg.Client.Autonomy, "report")
	}
}

func TestLoadClientConfig_InvalidHeartbeatInterval(t *testing.T) {
	t.Parallel()

	content := `
[client]
id = "test"

[irc]
server = "irc.example.com"
nick = "murmur-test"
bus_channel = "#murmur-bus"

[heartbeat]
interval = "bad"
`
	path := writeTempFile(t, "client.toml", content)

	_, err := LoadClientConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid heartbeat interval, got nil")
	}
}

func TestClientConfig_ParseHeartbeatInterval(t *testing.T) {
	t.Parallel()

	cfg := &ClientConfig{
		Heartbeat: HeartbeatConfig{Interval: "45s"},
	}

	d, err := cfg.ParseHeartbeatInterval()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Seconds() != 45 {
		t.Errorf("ParseHeartbeatInterval = %v, want 45s", d)
	}
}

func TestClientConfig_ParseHeartbeatInterval_Default(t *testing.T) {
	t.Parallel()

	cfg := &ClientConfig{}

	d, err := cfg.ParseHeartbeatInterval()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Seconds() != 30 {
		t.Errorf("ParseHeartbeatInterval = %v, want default 30s", d)
	}
}

func TestExpandHome(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot get home dir: %v", err)
	}

	tests := []struct {
		name string
		path string
		want string
	}{
		{"with tilde", "~/.murmur", filepath.Join(home, ".murmur")},
		{"without tilde", "/etc/murmur", "/etc/murmur"},
		{"tilde only", "~", home},
		{"relative", "relative/path", "relative/path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := expandHome(tt.path)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("expandHome(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestLoadServerConfig_FileNotFound(t *testing.T) {
	t.Parallel()

	_, err := LoadServerConfig("/nonexistent/path/server.toml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadClientConfig_FileNotFound(t *testing.T) {
	t.Parallel()

	_, err := LoadClientConfig("/nonexistent/path/client.toml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadServerConfig_ZeroDuration(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[scheduler]
heartbeat_interval = "0s"
`
	path := writeTempFile(t, "server.toml", content)

	_, err := LoadServerConfig(path)
	if err == nil {
		t.Fatal("expected error for zero duration, got nil")
	}
}

func TestLoadServerConfig_NegativeDuration(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[scheduler]
client_timeout = "-5m"
`
	path := writeTempFile(t, "server.toml", content)

	_, err := LoadServerConfig(path)
	if err == nil {
		t.Fatal("expected error for negative duration, got nil")
	}
}

func TestLoadClientConfig_ZeroHeartbeat(t *testing.T) {
	t.Parallel()

	content := `
[client]
id = "test"

[irc]
server = "irc.example.com"
nick = "murmur-test"
bus_channel = "#murmur-bus"

[heartbeat]
interval = "0s"
`
	path := writeTempFile(t, "client.toml", content)

	_, err := LoadClientConfig(path)
	if err == nil {
		t.Fatal("expected error for zero heartbeat interval, got nil")
	}
}

func TestLoadServerConfig_DefaultDataDir(t *testing.T) {
	t.Parallel()

	content := `
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
	if cfg.Server.DataDir == "" {
		t.Error("DataDir should default to expanded ~/.murmur, got empty")
	}
}

func TestLoadServerConfig_WithLLMProviders(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[llm]
default = "openrouter"

[llm.providers.openrouter]
api_base = "https://openrouter.ai/api/v1"
api_key = "sk-test"
model = "anthropic/claude-sonnet-4-5"
max_tokens = 8192
temperature = 0.7

[llm.providers.ollama]
api_base = "http://localhost:11434/v1"
api_key = "dummy"
model = "llama3.1:70b"
max_tokens = 4096
temperature = 0.5
`
	path := writeTempFile(t, "server.toml", content)

	cfg, err := LoadServerConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.LLM.Default != "openrouter" {
		t.Errorf("LLM.Default = %q, want %q", cfg.LLM.Default, "openrouter")
	}
	if len(cfg.LLM.Providers) != 2 {
		t.Fatalf("LLM.Providers count = %d, want 2", len(cfg.LLM.Providers))
	}

	or := cfg.LLM.Providers["openrouter"]
	if or.APIBase != "https://openrouter.ai/api/v1" {
		t.Errorf("openrouter.APIBase = %q", or.APIBase)
	}
	if or.APIKey != "sk-test" {
		t.Errorf("openrouter.APIKey = %q", or.APIKey)
	}
	if or.Model != "anthropic/claude-sonnet-4-5" {
		t.Errorf("openrouter.Model = %q", or.Model)
	}
	if or.MaxTokens != 8192 {
		t.Errorf("openrouter.MaxTokens = %d", or.MaxTokens)
	}
	if or.Temperature != 0.7 {
		t.Errorf("openrouter.Temperature = %f", or.Temperature)
	}

	ol := cfg.LLM.Providers["ollama"]
	if ol.Model != "llama3.1:70b" {
		t.Errorf("ollama.Model = %q", ol.Model)
	}
}

func TestLoadServerConfig_LLMDefaultNotInProviders(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[llm]
default = "nonexistent"

[llm.providers.openrouter]
api_base = "https://openrouter.ai/api/v1"
api_key = "sk-test"
model = "test"
`
	path := writeTempFile(t, "server.toml", content)

	_, err := LoadServerConfig(path)
	if err == nil {
		t.Fatal("expected error for default provider not in providers map, got nil")
	}
}

func TestLoadServerConfig_MemoryDefaults(t *testing.T) {
	t.Parallel()

	content := `
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

	// Memory.DBPath should default to DataDir/memory.db.
	if cfg.Memory.DBPath == "" {
		t.Error("Memory.DBPath should not be empty")
	}
	if cfg.Memory.MaxHistory != 100 {
		t.Errorf("Memory.MaxHistory = %d, want default 100", cfg.Memory.MaxHistory)
	}
}

func TestLoadServerConfig_MemoryCustomPath(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[memory]
db_path = "/custom/path/memory.db"
max_history = 200
`
	path := writeTempFile(t, "server.toml", content)

	cfg, err := LoadServerConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Memory.DBPath != "/custom/path/memory.db" {
		t.Errorf("Memory.DBPath = %q, want %q", cfg.Memory.DBPath, "/custom/path/memory.db")
	}
	if cfg.Memory.MaxHistory != 200 {
		t.Errorf("Memory.MaxHistory = %d, want 200", cfg.Memory.MaxHistory)
	}
}

func TestLoadServerConfig_VaultConfig(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[vault]
enabled = true
passphrase_env = "MY_VAULT_PASS"
`
	path := writeTempFile(t, "server.toml", content)

	cfg, err := LoadServerConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !cfg.Vault.Enabled {
		t.Error("Vault.Enabled = false, want true")
	}
	if cfg.Vault.DBPath == "" {
		t.Error("Vault.DBPath should default to DataDir/vault.db when enabled")
	}
	if cfg.Vault.PassphraseEnv != "MY_VAULT_PASS" {
		t.Errorf("Vault.PassphraseEnv = %q, want %q", cfg.Vault.PassphraseEnv, "MY_VAULT_PASS")
	}
}

func TestLoadServerConfig_SystemPromptFile(t *testing.T) {
	t.Parallel()

	content := `
[server]
system_prompt_file = "/etc/murmur/prompt.md"

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

	if cfg.Server.SystemPromptFile != "/etc/murmur/prompt.md" {
		t.Errorf("Server.SystemPromptFile = %q, want %q", cfg.Server.SystemPromptFile, "/etc/murmur/prompt.md")
	}
}

func TestLoadServerConfig_EmptyLLMIsValid(t *testing.T) {
	t.Parallel()

	content := `
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

	if cfg.LLM.Default != "" {
		t.Errorf("LLM.Default = %q, want empty", cfg.LLM.Default)
	}
	if len(cfg.LLM.Providers) != 0 {
		t.Errorf("LLM.Providers count = %d, want 0", len(cfg.LLM.Providers))
	}
}

func TestLoadClientConfig_WithSystemInfoTool(t *testing.T) {
	t.Parallel()

	content := `
[client]
id = "test"

[irc]
server = "irc.example.com"
nick = "murmur-test"
bus_channel = "#murmur-bus"

[tools.systeminfo]
enabled = true
`
	path := writeTempFile(t, "client.toml", content)

	cfg, err := LoadClientConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Tools.SystemInfo == nil {
		t.Fatal("Tools.SystemInfo is nil, want non-nil")
	}
	if !cfg.Tools.SystemInfo.Enabled {
		t.Error("Tools.SystemInfo.Enabled = false, want true")
	}
}

func TestLoadClientConfig_WithShellTool(t *testing.T) {
	t.Parallel()

	content := `
[client]
id = "test"

[irc]
server = "irc.example.com"
nick = "murmur-test"
bus_channel = "#murmur-bus"

[tools.shell]
enabled = true
docker_image = "alpine:latest"
network = true
memory_limit = "512m"
cpu_limit = "1.0"
timeout = "60s"
workspace = "/home/user/work"
whitelist = ["df -h", "uptime", "systemctl status *"]
`
	path := writeTempFile(t, "client.toml", content)

	cfg, err := LoadClientConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Tools.Shell == nil {
		t.Fatal("Tools.Shell is nil, want non-nil")
	}
	s := cfg.Tools.Shell
	if !s.Enabled {
		t.Error("Shell.Enabled = false, want true")
	}
	if s.DockerImage != "alpine:latest" {
		t.Errorf("Shell.DockerImage = %q, want %q", s.DockerImage, "alpine:latest")
	}
	if !s.Network {
		t.Error("Shell.Network = false, want true")
	}
	if s.MemoryLimit != "512m" {
		t.Errorf("Shell.MemoryLimit = %q, want %q", s.MemoryLimit, "512m")
	}
	if s.CPULimit != "1.0" {
		t.Errorf("Shell.CPULimit = %q, want %q", s.CPULimit, "1.0")
	}
	if s.Timeout != "60s" {
		t.Errorf("Shell.Timeout = %q, want %q", s.Timeout, "60s")
	}
	if s.Workspace != "/home/user/work" {
		t.Errorf("Shell.Workspace = %q, want %q", s.Workspace, "/home/user/work")
	}
	if len(s.Whitelist) != 3 {
		t.Fatalf("Shell.Whitelist length = %d, want 3", len(s.Whitelist))
	}
	if s.Whitelist[0] != "df -h" {
		t.Errorf("Shell.Whitelist[0] = %q, want %q", s.Whitelist[0], "df -h")
	}
}

func TestLoadClientConfig_WithCodeExecTool(t *testing.T) {
	t.Parallel()

	content := `
[client]
id = "test"

[irc]
server = "irc.example.com"
nick = "murmur-test"
bus_channel = "#murmur-bus"

[tools.code_exec]
enabled = true
piston_url = "http://localhost:2000"
default_language = "python"
run_timeout = 15000
run_memory_limit = 134217728
`
	path := writeTempFile(t, "client.toml", content)

	cfg, err := LoadClientConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Tools.CodeExec == nil {
		t.Fatal("Tools.CodeExec is nil, want non-nil")
	}
	ce := cfg.Tools.CodeExec
	if !ce.Enabled {
		t.Error("CodeExec.Enabled = false, want true")
	}
	if ce.PistonURL != "http://localhost:2000" {
		t.Errorf("CodeExec.PistonURL = %q, want %q", ce.PistonURL, "http://localhost:2000")
	}
	if ce.DefaultLang != "python" {
		t.Errorf("CodeExec.DefaultLang = %q, want %q", ce.DefaultLang, "python")
	}
	if ce.RunTimeout != 15000 {
		t.Errorf("CodeExec.RunTimeout = %d, want 15000", ce.RunTimeout)
	}
	if ce.RunMemoryLimit != 134217728 {
		t.Errorf("CodeExec.RunMemoryLimit = %d, want 134217728", ce.RunMemoryLimit)
	}
}

func TestLoadClientConfig_ShellDefaults(t *testing.T) {
	t.Parallel()

	content := `
[client]
id = "test"

[irc]
server = "irc.example.com"
nick = "murmur-test"
bus_channel = "#murmur-bus"

[tools.shell]
enabled = true
`
	path := writeTempFile(t, "client.toml", content)

	cfg, err := LoadClientConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Tools.Shell == nil {
		t.Fatal("Tools.Shell is nil, want non-nil")
	}
	if cfg.Tools.Shell.DockerImage != "ubuntu:24.04" {
		t.Errorf("Shell.DockerImage = %q, want default %q", cfg.Tools.Shell.DockerImage, "ubuntu:24.04")
	}
}

func TestLoadClientConfig_CodeExecMissingURL(t *testing.T) {
	t.Parallel()

	content := `
[client]
id = "test"

[irc]
server = "irc.example.com"
nick = "murmur-test"
bus_channel = "#murmur-bus"

[tools.code_exec]
enabled = true
`
	path := writeTempFile(t, "client.toml", content)

	_, err := LoadClientConfig(path)
	if err == nil {
		t.Fatal("expected error for missing piston_url, got nil")
	}
}

func TestLoadClientConfig_CodeExecDefaults(t *testing.T) {
	t.Parallel()

	content := `
[client]
id = "test"

[irc]
server = "irc.example.com"
nick = "murmur-test"
bus_channel = "#murmur-bus"

[tools.code_exec]
enabled = true
piston_url = "http://localhost:2000"
`
	path := writeTempFile(t, "client.toml", content)

	cfg, err := LoadClientConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ce := cfg.Tools.CodeExec
	if ce.RunTimeout != 0 {
		t.Errorf("CodeExec.RunTimeout = %d, want default 0 (use Piston server default)", ce.RunTimeout)
	}
	if ce.RunMemoryLimit != 0 {
		t.Errorf("CodeExec.RunMemoryLimit = %d, want default 0 (use Piston server default)", ce.RunMemoryLimit)
	}
}

func TestLoadClientConfig_NoToolsIsValid(t *testing.T) {
	t.Parallel()

	content := `
[client]
id = "test"

[irc]
server = "irc.example.com"
nick = "murmur-test"
bus_channel = "#murmur-bus"
`
	path := writeTempFile(t, "client.toml", content)

	cfg, err := LoadClientConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Tools.SystemInfo != nil {
		t.Error("Tools.SystemInfo should be nil when not configured")
	}
	if cfg.Tools.Shell != nil {
		t.Error("Tools.Shell should be nil when not configured")
	}
	if cfg.Tools.CodeExec != nil {
		t.Error("Tools.CodeExec should be nil when not configured")
	}
}

func TestShellConfig_ParseTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		timeout string
		want    float64 // seconds
		wantErr bool
	}{
		{"explicit", "45s", 45, false},
		{"default", "", 30, false},
		{"minutes", "2m", 120, false},
		{"invalid", "bad", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &ShellConfig{Timeout: tt.timeout}
			d, err := cfg.ParseTimeout()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d.Seconds() != tt.want {
				t.Errorf("ParseTimeout() = %v, want %vs", d, tt.want)
			}
		})
	}
}

func TestLoadClientConfig_ShellInvalidTimeout(t *testing.T) {
	t.Parallel()

	content := `
[client]
id = "test"

[irc]
server = "irc.example.com"
nick = "murmur-test"
bus_channel = "#murmur-bus"

[tools.shell]
enabled = true
timeout = "not-a-duration"
`
	path := writeTempFile(t, "client.toml", content)

	_, err := LoadClientConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid shell timeout, got nil")
	}
}

func TestToolsConfig_HasExecutionTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  ToolsConfig
		want bool
	}{
		{"empty", ToolsConfig{}, false},
		{"systeminfo only", ToolsConfig{SystemInfo: &SystemInfoConfig{Enabled: true}}, false},
		{"shell enabled", ToolsConfig{Shell: &ShellConfig{Enabled: true}}, true},
		{"code_exec enabled", ToolsConfig{CodeExec: &CodeExecConfig{Enabled: true}}, true},
		{"shell disabled", ToolsConfig{Shell: &ShellConfig{Enabled: false}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.cfg.HasExecutionTools(); got != tt.want {
				t.Errorf("HasExecutionTools() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadServerConfig_UnifiedTools(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[tools.shell]
enabled = true
docker_image = "alpine:latest"
timeout = "60s"

[tools.web_search]
enabled = true
api_key = "sk-brave-test"
max_results = 10

[tools.rss]
enabled = true
max_items = 20

[tools.dns]
enabled = true

[tools.code_exec]
enabled = true
piston_url = "http://localhost:2000"
`
	path := writeTempFile(t, "server.toml", content)

	cfg, err := LoadServerConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify all tools parsed into the unified ToolsConfig.
	if cfg.Tools.Shell == nil || !cfg.Tools.Shell.Enabled {
		t.Error("Tools.Shell should be enabled")
	}
	if cfg.Tools.Shell.DockerImage != "alpine:latest" {
		t.Errorf("Tools.Shell.DockerImage = %q, want %q", cfg.Tools.Shell.DockerImage, "alpine:latest")
	}
	if cfg.Tools.WebSearch == nil || !cfg.Tools.WebSearch.Enabled {
		t.Error("Tools.WebSearch should be enabled")
	}
	if cfg.Tools.WebSearch.APIKey != "sk-brave-test" {
		t.Errorf("Tools.WebSearch.APIKey = %q, want %q", cfg.Tools.WebSearch.APIKey, "sk-brave-test")
	}
	if cfg.Tools.RSS == nil || !cfg.Tools.RSS.Enabled {
		t.Error("Tools.RSS should be enabled")
	}
	if cfg.Tools.RSS.MaxItems != 20 {
		t.Errorf("Tools.RSS.MaxItems = %d, want 20", cfg.Tools.RSS.MaxItems)
	}
	if cfg.Tools.DNS == nil || !cfg.Tools.DNS.Enabled {
		t.Error("Tools.DNS should be enabled")
	}
	if cfg.Tools.CodeExec == nil || !cfg.Tools.CodeExec.Enabled {
		t.Error("Tools.CodeExec should be enabled")
	}
}

func TestLoadServerConfig_WithAPIConfig(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[api]
enabled = true
listen = "0.0.0.0:9090"
api_key = "vault:api-key"
event_retention_days = 7
`
	path := writeTempFile(t, "server.toml", content)

	cfg, err := LoadServerConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !cfg.API.Enabled {
		t.Error("API.Enabled = false, want true")
	}
	if cfg.API.Listen != "0.0.0.0:9090" {
		t.Errorf("API.Listen = %q, want %q", cfg.API.Listen, "0.0.0.0:9090")
	}
	if cfg.API.APIKey != "vault:api-key" {
		t.Errorf("API.APIKey = %q, want %q", cfg.API.APIKey, "vault:api-key")
	}
	if cfg.API.EventRetentionDays != 7 {
		t.Errorf("API.EventRetentionDays = %d, want 7", cfg.API.EventRetentionDays)
	}
}

func TestToolsConfig_Validate_SearXNG(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     ToolsConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid",
			cfg: ToolsConfig{
				SearXNG: &SearXNGConfig{Enabled: true, URL: "http://localhost:8080"},
			},
		},
		{
			name: "disabled is always valid",
			cfg: ToolsConfig{
				SearXNG: &SearXNGConfig{Enabled: false},
			},
		},
		{
			name: "missing URL",
			cfg: ToolsConfig{
				SearXNG: &SearXNGConfig{Enabled: true},
			},
			wantErr: true,
			errMsg:  "tools.searxng.url is required when searxng is enabled",
		},
		{
			name: "defaults max_results",
			cfg: ToolsConfig{
				SearXNG: &SearXNGConfig{Enabled: true, URL: "http://localhost:8080"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("error = %q, want %q", err.Error(), tt.errMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Check defaults were applied.
			if tt.cfg.SearXNG != nil && tt.cfg.SearXNG.Enabled && tt.cfg.SearXNG.MaxResults != 10 {
				t.Errorf("SearXNG.MaxResults = %d, want default 10", tt.cfg.SearXNG.MaxResults)
			}
		})
	}
}

func TestToolsConfig_Validate_SearXNG_MaxResultsCap(t *testing.T) {
	t.Parallel()

	cfg := ToolsConfig{
		SearXNG: &SearXNGConfig{Enabled: true, URL: "http://localhost:8080", MaxResults: 200},
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.SearXNG.MaxResults != 100 {
		t.Errorf("SearXNG.MaxResults = %d, want capped 100", cfg.SearXNG.MaxResults)
	}
}

func TestToolsConfig_Validate_OpenCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     ToolsConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid",
			cfg: ToolsConfig{
				OpenCode: &OpenCodeConfig{Enabled: true, URL: "http://localhost:3000"},
			},
		},
		{
			name: "disabled is always valid",
			cfg: ToolsConfig{
				OpenCode: &OpenCodeConfig{Enabled: false},
			},
		},
		{
			name: "missing URL",
			cfg: ToolsConfig{
				OpenCode: &OpenCodeConfig{Enabled: true},
			},
			wantErr: true,
			errMsg:  "tools.opencode.url is required when opencode is enabled",
		},
		{
			name: "invalid session timeout",
			cfg: ToolsConfig{
				OpenCode: &OpenCodeConfig{Enabled: true, URL: "http://localhost:3000", SessionTimeout: "bad"},
			},
			wantErr: true,
		},
		{
			name: "defaults session timeout",
			cfg: ToolsConfig{
				OpenCode: &OpenCodeConfig{Enabled: true, URL: "http://localhost:3000"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("error = %q, want %q", err.Error(), tt.errMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Check defaults were applied.
			if tt.cfg.OpenCode != nil && tt.cfg.OpenCode.Enabled && tt.cfg.OpenCode.SessionTimeout != "5m" {
				t.Errorf("OpenCode.SessionTimeout = %q, want default %q", tt.cfg.OpenCode.SessionTimeout, "5m")
			}
		})
	}
}

func TestToolsConfig_Validate_HTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     ToolsConfig
		wantErr bool
	}{
		{
			name: "valid",
			cfg: ToolsConfig{
				HTTP: &HTTPToolConfig{Enabled: true, Timeout: "15s"},
			},
		},
		{
			name: "disabled is always valid",
			cfg: ToolsConfig{
				HTTP: &HTTPToolConfig{Enabled: false},
			},
		},
		{
			name: "defaults max_response_bytes",
			cfg: ToolsConfig{
				HTTP: &HTTPToolConfig{Enabled: true},
			},
		},
		{
			name: "invalid timeout",
			cfg: ToolsConfig{
				HTTP: &HTTPToolConfig{Enabled: true, Timeout: "bad"},
			},
			wantErr: true,
		},
		{
			name: "negative max_response_bytes",
			cfg: ToolsConfig{
				HTTP: &HTTPToolConfig{Enabled: true, MaxResponseBytes: -1},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Check defaults were applied.
			if tt.cfg.HTTP != nil && tt.cfg.HTTP.Enabled && tt.cfg.HTTP.MaxResponseBytes != 1048576 {
				t.Errorf("HTTP.MaxResponseBytes = %d, want default 1048576", tt.cfg.HTTP.MaxResponseBytes)
			}
		})
	}
}

func TestToolsConfig_Validate_ConfigManage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     ToolsConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid",
			cfg: ToolsConfig{
				ConfigManage: &ConfigManageConfig{Enabled: true, ConfigPath: "/etc/murmur/server.toml"},
			},
		},
		{
			name: "disabled is always valid",
			cfg: ToolsConfig{
				ConfigManage: &ConfigManageConfig{Enabled: false},
			},
		},
		{
			name: "missing config_path",
			cfg: ToolsConfig{
				ConfigManage: &ConfigManageConfig{Enabled: true},
			},
			wantErr: true,
			errMsg:  "tools.config_manage.config_path is required when config_manage is enabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("error = %q, want %q", err.Error(), tt.errMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestToolsConfig_Validate_ConfigManage_ExpandsHome(t *testing.T) {
	t.Parallel()

	cfg := ToolsConfig{
		ConfigManage: &ConfigManageConfig{Enabled: true, ConfigPath: "~/.murmur/server.toml"},
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ConfigManage.ConfigPath == "~/.murmur/server.toml" {
		t.Error("ConfigManage.ConfigPath should have expanded ~ to absolute path")
	}
	if cfg.ConfigManage.ConfigPath == "" {
		t.Error("ConfigManage.ConfigPath should not be empty after expansion")
	}
}

func TestHTTPToolConfig_GetBlockPrivateIPs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  HTTPToolConfig
		want bool
	}{
		{"nil defaults to true", HTTPToolConfig{}, true},
		{"explicit true", HTTPToolConfig{BlockPrivateIPs: boolPtr(true)}, true},
		{"explicit false", HTTPToolConfig{BlockPrivateIPs: boolPtr(false)}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.cfg.GetBlockPrivateIPs(); got != tt.want {
				t.Errorf("GetBlockPrivateIPs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHTTPToolConfig_ParseTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		timeout string
		want    float64 // seconds
		wantErr bool
	}{
		{"explicit", "15s", 15, false},
		{"default", "", 30, false},
		{"minutes", "2m", 120, false},
		{"invalid", "bad", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &HTTPToolConfig{Timeout: tt.timeout}
			d, err := cfg.ParseTimeout()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d.Seconds() != tt.want {
				t.Errorf("ParseTimeout() = %v, want %vs", d, tt.want)
			}
		})
	}
}

func TestToolsConfig_Validate_IRCManage(t *testing.T) {
	t.Parallel()

	// IRCManage has no required fields beyond Enabled, so it should always pass.
	cfg := ToolsConfig{
		IRCManage: &IRCManageConfig{Enabled: true},
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadServerConfig_ServerToolsValidation(t *testing.T) {
	t.Parallel()

	// Verify that server config validates tool configs (e.g., missing piston_url).
	content := `
[irc]
server = "irc.example.com"
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[tools.code_exec]
enabled = true
`
	path := writeTempFile(t, "server.toml", content)

	_, err := LoadServerConfig(path)
	if err == nil {
		t.Fatal("expected error for missing piston_url in server config, got nil")
	}
}

func TestLoadServerConfig_APIDefaults(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[api]
enabled = true
`
	path := writeTempFile(t, "server.toml", content)

	cfg, err := LoadServerConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.API.Listen != "127.0.0.1:8080" {
		t.Errorf("API.Listen = %q, want default %q", cfg.API.Listen, "127.0.0.1:8080")
	}
	if cfg.API.EventRetentionDays != 30 {
		t.Errorf("API.EventRetentionDays = %d, want default 30", cfg.API.EventRetentionDays)
	}
}

func TestLoadServerConfig_APIDisabledNoDefaults(t *testing.T) {
	t.Parallel()

	content := `
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

	if cfg.API.Enabled {
		t.Error("API.Enabled = true, want false")
	}
	// Listen should remain empty when API is disabled.
	if cfg.API.Listen != "" {
		t.Errorf("API.Listen = %q, want empty when disabled", cfg.API.Listen)
	}
}

func boolPtr(b bool) *bool {
	return &b
}

func TestLoadClientConfig_WithAPIConfig(t *testing.T) {
	t.Parallel()

	content := `
[client]
id = "test"

[irc]
server = "irc.example.com"
nick = "murmur-test"
bus_channel = "#murmur-bus"

[api]
enabled = true
listen = "0.0.0.0:9091"
api_key = "secret-key"
`
	path := writeTempFile(t, "client.toml", content)

	cfg, err := LoadClientConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !cfg.API.Enabled {
		t.Error("API.Enabled = false, want true")
	}
	if cfg.API.Listen != "0.0.0.0:9091" {
		t.Errorf("API.Listen = %q, want %q", cfg.API.Listen, "0.0.0.0:9091")
	}
	if cfg.API.APIKey != "secret-key" {
		t.Errorf("API.APIKey = %q, want %q", cfg.API.APIKey, "secret-key")
	}
}

func TestLoadClientConfig_APIDefaults(t *testing.T) {
	t.Parallel()

	content := `
[client]
id = "test"

[irc]
server = "irc.example.com"
nick = "murmur-test"
bus_channel = "#murmur-bus"

[api]
enabled = true
`
	path := writeTempFile(t, "client.toml", content)

	cfg, err := LoadClientConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.API.Listen != "127.0.0.1:8081" {
		t.Errorf("API.Listen = %q, want default %q", cfg.API.Listen, "127.0.0.1:8081")
	}
}

func TestLoadServerConfig_WithHTTPToolConfig(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[tools.http]
enabled = true
timeout = "15s"
max_response_bytes = 524288
allowed_domains = ["api.example.com", "*.internal.net"]
block_private_ips = false
`
	path := writeTempFile(t, "server.toml", content)

	cfg, err := LoadServerConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Tools.HTTP == nil {
		t.Fatal("Tools.HTTP is nil, want non-nil")
	}
	h := cfg.Tools.HTTP
	if !h.Enabled {
		t.Error("HTTP.Enabled = false, want true")
	}
	if h.Timeout != "15s" {
		t.Errorf("HTTP.Timeout = %q, want %q", h.Timeout, "15s")
	}
	if h.MaxResponseBytes != 524288 {
		t.Errorf("HTTP.MaxResponseBytes = %d, want 524288", h.MaxResponseBytes)
	}
	if len(h.AllowedDomains) != 2 {
		t.Fatalf("HTTP.AllowedDomains length = %d, want 2", len(h.AllowedDomains))
	}
	if h.AllowedDomains[0] != "api.example.com" {
		t.Errorf("HTTP.AllowedDomains[0] = %q, want %q", h.AllowedDomains[0], "api.example.com")
	}
	if h.GetBlockPrivateIPs() {
		t.Error("HTTP.GetBlockPrivateIPs() = true, want false")
	}
}

func TestHTTPToolConfig_BlockPrivateIPsDefault(t *testing.T) {
	t.Parallel()

	// When BlockPrivateIPs is nil (not set in TOML), default to true.
	cfg := &HTTPToolConfig{}
	if !cfg.GetBlockPrivateIPs() {
		t.Error("GetBlockPrivateIPs() = false, want default true")
	}
}

func TestLoadServerConfig_HTTPToolInvalidTimeout(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[tools.http]
enabled = true
timeout = "not-a-duration"
`
	path := writeTempFile(t, "server.toml", content)

	_, err := LoadServerConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid HTTP tool timeout, got nil")
	}
}

func TestDebugConfigParsing(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[debug]
enabled = true
channel = "#murmur-debug"
log_level = "info"
log_tool_calls = true
log_llm_requests = true
log_bus_protocol = false
log_permissions = true
`
	path := writeTempFile(t, "server.toml", content)

	cfg, err := LoadServerConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !cfg.Debug.Enabled {
		t.Error("Debug.Enabled = false, want true")
	}
	if cfg.Debug.Channel != "#murmur-debug" {
		t.Errorf("Debug.Channel = %q, want %q", cfg.Debug.Channel, "#murmur-debug")
	}
	if cfg.Debug.LogLevel != "info" {
		t.Errorf("Debug.LogLevel = %q, want %q", cfg.Debug.LogLevel, "info")
	}
	if !cfg.Debug.LogToolCalls {
		t.Error("Debug.LogToolCalls = false, want true")
	}
	if !cfg.Debug.LogLLMRequests {
		t.Error("Debug.LogLLMRequests = false, want true")
	}
	if cfg.Debug.LogBusProtocol {
		t.Error("Debug.LogBusProtocol = true, want false")
	}
	if !cfg.Debug.LogPermissions {
		t.Error("Debug.LogPermissions = false, want true")
	}

	level := cfg.Debug.ParseDebugLevel()
	if level != slog.LevelInfo {
		t.Errorf("ParseDebugLevel() = %v, want %v", level, slog.LevelInfo)
	}
}

func TestDebugConfigDefaults(t *testing.T) {
	t.Parallel()

	content := `
[irc]
server = "irc.example.com"
nick = "murmur"

[irc.channels]
main = "#murmur"
bus = "#murmur-bus"

[debug]
channel = "#debug"
`
	path := writeTempFile(t, "server.toml", content)

	cfg, err := LoadServerConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Channel is set but enabled is not explicitly set — TOML zero value is
	// false. Users must explicitly set enabled = true in the new [debug] section.
	// (Backward compat from server.debug_channel sets it automatically.)
	if cfg.Debug.Enabled {
		t.Error("Debug.Enabled should be false when not explicitly set in [debug] section")
	}
	// LogLevel should default to "debug".
	if cfg.Debug.LogLevel != "debug" {
		t.Errorf("Debug.LogLevel = %q, want default %q", cfg.Debug.LogLevel, "debug")
	}
	level := cfg.Debug.ParseDebugLevel()
	if level != slog.LevelDebug {
		t.Errorf("ParseDebugLevel() = %v, want %v", level, slog.LevelDebug)
	}
	// Log categories should default to false (TOML zero value).
	if cfg.Debug.LogToolCalls {
		t.Error("Debug.LogToolCalls should default to false")
	}
	if cfg.Debug.LogLLMRequests {
		t.Error("Debug.LogLLMRequests should default to false")
	}
}

func TestDebugChannelBackwardCompat(t *testing.T) {
	t.Parallel()

	content := `
[server]
debug_channel = "#old-debug"

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

	// Old server.debug_channel should populate [debug] section.
	if cfg.Debug.Channel != "#old-debug" {
		t.Errorf("Debug.Channel = %q, want %q (from server.debug_channel)", cfg.Debug.Channel, "#old-debug")
	}
	if !cfg.Debug.Enabled {
		t.Error("Debug.Enabled should be true from backward compat")
	}
	// Backward compat should enable all log categories.
	if !cfg.Debug.LogToolCalls {
		t.Error("Debug.LogToolCalls should be true from backward compat")
	}
	if !cfg.Debug.LogLLMRequests {
		t.Error("Debug.LogLLMRequests should be true from backward compat")
	}
	if !cfg.Debug.LogBusProtocol {
		t.Error("Debug.LogBusProtocol should be true from backward compat")
	}
	if !cfg.Debug.LogPermissions {
		t.Error("Debug.LogPermissions should be true from backward compat")
	}
}

func TestDebugConfigParseDebugLevel_AllLevels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		level string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"DEBUG", slog.LevelDebug},
		{"INFO", slog.LevelInfo},
		{"", slog.LevelDebug},
		{"unknown", slog.LevelDebug},
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			t.Parallel()
			dc := &DebugConfig{LogLevel: tt.level}
			got := dc.ParseDebugLevel()
			if got != tt.want {
				t.Errorf("ParseDebugLevel(%q) = %v, want %v", tt.level, got, tt.want)
			}
		})
	}
}

func TestParseDurationDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		s          string
		defaultVal time.Duration
		want       time.Duration
		wantErr    bool
	}{
		{"empty returns default", "", 5 * time.Minute, 5 * time.Minute, false},
		{"valid duration", "30s", time.Minute, 30 * time.Second, false},
		{"minutes", "2m", time.Second, 2 * time.Minute, false},
		{"invalid", "bad", time.Second, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseDurationDefault(tt.s, tt.defaultVal)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseDurationDefault(%q, %v) = %v, want %v", tt.s, tt.defaultVal, got, tt.want)
			}
		})
	}
}

func TestIRCConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     IRCConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid",
			cfg:     IRCConfig{Server: "irc.example.com", Nick: "bot", Port: 6667},
			wantErr: false,
		},
		{
			name:    "missing server",
			cfg:     IRCConfig{Nick: "bot"},
			wantErr: true,
			errMsg:  "irc.server is required",
		},
		{
			name:    "missing nick",
			cfg:     IRCConfig{Server: "irc.example.com"},
			wantErr: true,
			errMsg:  "irc.nick is required",
		},
		{
			name:    "default port",
			cfg:     IRCConfig{Server: "irc.example.com", Nick: "bot"},
			wantErr: false,
		},
		{
			name:    "default max_line_len",
			cfg:     IRCConfig{Server: "irc.example.com", Nick: "bot"},
			wantErr: false,
		},
		{
			name:    "max_line_len too small",
			cfg:     IRCConfig{Server: "irc.example.com", Nick: "bot", MaxLineLen: 256},
			wantErr: true,
			errMsg:  "irc.max_line_len must be at least 512",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("error = %q, want %q", err.Error(), tt.errMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	// Verify defaults are applied.
	cfg := IRCConfig{Server: "irc.example.com", Nick: "bot"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 6697 {
		t.Errorf("Port = %d, want default 6697", cfg.Port)
	}
	if cfg.MaxLineLen != 512 {
		t.Errorf("MaxLineLen = %d, want default 512", cfg.MaxLineLen)
	}
}

// writeTempFile creates a temporary file with the given content and returns its path.
func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	return path
}
