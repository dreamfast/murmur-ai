// Package config provides TOML configuration loading and validation for
// both the Murmur server and client.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// ServerConfig holds the complete server configuration loaded from TOML.
type ServerConfig struct {
	Server    ServerSection   `toml:"server"`
	IRC       IRCConfig       `toml:"irc"`
	LLM       LLMConfig       `toml:"llm"`
	Memory    MemoryConfig    `toml:"memory"`
	Scheduler SchedulerConfig `toml:"scheduler"`
	Approval  ApprovalConfig  `toml:"approval"`
	Security  SecurityConfig  `toml:"security"`
	Vault     VaultConfig     `toml:"vault"`
	Tools     ToolsConfig     `toml:"tools"`
	API       APIConfig       `toml:"api"`
}

// APIConfig holds configuration for the REST API server exposed by both
// the murmur server and client. The API accepts external events and provides
// status/health endpoints.
type APIConfig struct {
	// Enabled controls whether the HTTP API server is started.
	Enabled bool `toml:"enabled"`
	// Listen is the address to bind the HTTP server to (e.g., "127.0.0.1:8080").
	// Defaults to "127.0.0.1:8080" for the server and "127.0.0.1:8081" for clients.
	Listen string `toml:"listen"`
	// APIKey is an optional bearer token for authenticating API requests.
	// Supports "vault:" prefix for vault-based secret resolution.
	// When empty, no authentication is required.
	APIKey string `toml:"api_key"`
	// EventRetentionDays is how many days to keep events in the database.
	// Defaults to 30. Set to 0 to disable automatic cleanup.
	EventRetentionDays int `toml:"event_retention_days"`
}

// ServerSection holds general server settings.
type ServerSection struct {
	// DataDir is the base directory for server data files.
	DataDir string `toml:"data_dir"`
	// SystemPromptFile is the path to the system prompt markdown file.
	SystemPromptFile string `toml:"system_prompt_file"`
	// Name is a human-readable name for this server instance, used as a
	// target identifier for tools like shell (e.g., "cloud-vps", "home-server").
	// Defaults to "server" if not set.
	Name string `toml:"name"`
	// Verbose sends status messages to IRC (thinking, tool calls, etc.)
	// so the user can see what the agent is doing in real time.
	Verbose bool `toml:"verbose"`
	// DebugChannel is an IRC channel that receives live slog output for
	// real-time debugging (e.g., "#murmur-debug"). Empty means disabled.
	DebugChannel string `toml:"debug_channel"`
}

// LLMConfig holds multi-provider LLM configuration.
type LLMConfig struct {
	// Default is the name of the default LLM provider.
	Default string `toml:"default"`
	// Providers maps provider names to their configuration.
	Providers map[string]LLMProviderConfig `toml:"providers"`
}

// LLMProviderConfig holds configuration for a single LLM provider.
type LLMProviderConfig struct {
	// APIBase is the base URL for the provider's API (e.g., "https://openrouter.ai/api/v1").
	APIBase string `toml:"api_base"`
	// APIKey is the API key (may use "vault:" prefix for vault references).
	APIKey string `toml:"api_key"`
	// Model is the model identifier (e.g., "anthropic/claude-sonnet-4-5").
	Model string `toml:"model"`
	// MaxTokens is the maximum number of tokens to generate.
	MaxTokens int `toml:"max_tokens"`
	// Temperature controls response randomness (0.0-2.0).
	Temperature float64 `toml:"temperature"`
	// UserAgent is the User-Agent header sent with API requests (optional).
	// Some providers (e.g., Kimi) require a specific User-Agent string.
	UserAgent string `toml:"user_agent"`
	// Reasoning enables reasoning/thinking mode compatibility. When true,
	// the provider is expected to return reasoning_content in responses, and
	// all assistant messages with tool_calls will include reasoning_content
	// (even if empty) when sent back. When false, reasoning_content is
	// stripped from outgoing messages. Required for Kimi's thinking mode.
	Reasoning bool `toml:"reasoning"`
}

// MemoryConfig holds conversation memory settings.
type MemoryConfig struct {
	// DBPath is the path to the SQLite database file.
	DBPath string `toml:"db_path"`
	// MaxHistory is the maximum number of messages to keep per channel.
	MaxHistory int `toml:"max_history"`
	// SummaryModel is the provider name to use for conversation summarization (optional).
	// When empty, summarization is disabled.
	SummaryModel string `toml:"summary_model"`
	// SummaryThreshold is the message count that triggers summarization.
	// When the number of messages in a channel exceeds this threshold,
	// the older half is summarized and stored in the summaries table.
	// The summarized messages are deleted from conversations.
	// Defaults to 80% of MaxHistory. Set to 0 to use the default.
	SummaryThreshold int `toml:"summary_threshold"`
	// CrossChannelContext is the number of recent messages from other joined
	// channels to include in the system prompt. This gives the LLM awareness
	// of activity in other channels (e.g., news posted to #news can be
	// referenced from #murmur). Set to -1 to disable. Defaults to 10.
	CrossChannelContext int `toml:"cross_channel_context"`
}

// VaultConfig holds secrets vault settings.
type VaultConfig struct {
	// Enabled controls whether the vault is active.
	Enabled bool `toml:"enabled"`
	// DBPath is the path to the vault SQLite database file.
	DBPath string `toml:"db_path"`
	// PassphraseEnv is the environment variable name containing the vault passphrase.
	PassphraseEnv string `toml:"passphrase_env"`
}

// IRCConfig holds IRC connection settings shared by both server and client.
type IRCConfig struct {
	// Server is the IRC server hostname.
	Server string `toml:"server"`
	// Port is the IRC server port.
	Port int `toml:"port"`
	// TLS enables TLS for the IRC connection.
	TLS bool `toml:"tls"`
	// Nick is the IRC nickname.
	Nick string `toml:"nick"`
	// User is the IRC username (ident).
	User string `toml:"user"`
	// Realname is the IRC realname field.
	Realname string `toml:"realname"`
	// Password is the IRC server password (optional).
	Password string `toml:"password"`
	// NickServPassword is the NickServ IDENTIFY password (optional).
	NickServPassword string `toml:"nickserv_password"`
	// OperUser is the IRC operator username for the OPER command (optional).
	// When set along with OperPassword, the bot sends OPER on connect to gain
	// IRC operator privileges (set topics, kick users, etc.).
	OperUser string `toml:"oper_user"`
	// OperPassword is the IRC operator password for the OPER command (optional).
	// Supports "vault:" prefix for vault-based secret resolution.
	OperPassword string `toml:"oper_password"`
	// MaxLineLen is the maximum IRC line length in bytes supported by the
	// server. Standard IRC uses 512; some servers (e.g., Ergo) support
	// larger values. The bus protocol uses this to size message chunks.
	// Defaults to 512 if not set.
	MaxLineLen int `toml:"max_line_len"`
	// Channels holds channel configuration (server only).
	Channels ChannelsConfig `toml:"channels"`
	// BusChannel is the bus channel name (client only).
	BusChannel string `toml:"bus_channel"`
}

// ChannelsConfig holds IRC channel names for the server.
type ChannelsConfig struct {
	// Main is the user interaction channel.
	Main string `toml:"main"`
	// Bus is the internal server-client communication channel.
	Bus string `toml:"bus"`
}

// SchedulerConfig holds scheduler and heartbeat settings.
type SchedulerConfig struct {
	// Enabled controls whether the scheduler runs.
	Enabled bool `toml:"enabled"`
	// HeartbeatInterval is how often to check scheduled tasks (e.g., "5m").
	HeartbeatInterval string `toml:"heartbeat_interval"`
	// ClientTimeout is how long before a client is marked offline (e.g., "2m").
	ClientTimeout string `toml:"client_timeout"`
	// TickInterval is how often the task scheduler checks for due tasks (e.g., "30s").
	// Defaults to "30s". This determines the minimum granularity for scheduled tasks.
	TickInterval string `toml:"tick_interval"`
	// MaxConcurrent is the maximum number of scheduled tasks that can run
	// simultaneously. Defaults to 3. When all slots are occupied, due tasks
	// are skipped until a slot frees up.
	MaxConcurrent int `toml:"max_concurrent"`
}

// ApprovalConfig holds settings for the tool call approval flow.
type ApprovalConfig struct {
	// Timeout is how long to wait for user approval before auto-denying (e.g., "2m").
	// Defaults to "2m".
	Timeout string `toml:"timeout"`
}

// SecurityConfig holds security-related settings.
type SecurityConfig struct {
	// AllowedUsers is the list of IRC nicks allowed to interact with the agent.
	AllowedUsers []string `toml:"allowed_users"`
	// RequireNickServ requires users to be identified with NickServ before
	// their messages are processed by the agent. Defaults to true when
	// permissions.toml has [users] entries. Commands still work without
	// identification.
	RequireNickServ bool `toml:"require_nickserv"`
	// NickServCacheTTL is how long to cache NickServ identification results.
	// Defaults to "5m". Set to "0" to disable caching.
	NickServCacheTTL string `toml:"nickserv_cache_ttl"`
	// BusKey is a shared secret for bus message authentication (optional, Phase 2).
	BusKey string `toml:"bus_key"`
	// PermissionsFile is the path to the permissions TOML file that defines
	// user and channel permission rules. Defaults to <data_dir>/permissions.toml.
	// The file is machine-managed by !user/!channel commands and the
	// permissions_manage tool. Manual edits are supported and applied on reload.
	PermissionsFile string `toml:"permissions_file"`
}

// ParsedSchedulerConfig holds parsed duration values from SchedulerConfig.
type ParsedSchedulerConfig struct {
	HeartbeatInterval time.Duration
	ClientTimeout     time.Duration
	TickInterval      time.Duration
	MaxConcurrent     int
}

// LoadServerConfig reads and parses a server TOML configuration file.
func LoadServerConfig(path string) (*ServerConfig, error) {
	expanded, err := expandHome(path)
	if err != nil {
		return nil, fmt.Errorf("LoadServerConfig: %w", err)
	}

	data, err := os.ReadFile(expanded)
	if err != nil {
		return nil, fmt.Errorf("LoadServerConfig: %w", err)
	}

	var cfg ServerConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("LoadServerConfig: %w", err)
	}

	// Default data_dir to ~/.murmur if not set.
	if cfg.Server.DataDir == "" {
		cfg.Server.DataDir = "~/.murmur"
	}
	cfg.Server.DataDir, err = expandHome(cfg.Server.DataDir)
	if err != nil {
		return nil, fmt.Errorf("LoadServerConfig: expanding data_dir: %w", err)
	}

	// Default server name.
	if cfg.Server.Name == "" {
		cfg.Server.Name = "server"
	}

	// Expand ~ in system_prompt_file.
	if cfg.Server.SystemPromptFile != "" {
		cfg.Server.SystemPromptFile, err = expandHome(cfg.Server.SystemPromptFile)
		if err != nil {
			return nil, fmt.Errorf("LoadServerConfig: expanding system_prompt_file: %w", err)
		}
	}

	// Default memory.db_path to data_dir/memory.db.
	if cfg.Memory.DBPath == "" {
		cfg.Memory.DBPath = filepath.Join(cfg.Server.DataDir, "memory.db")
	} else {
		cfg.Memory.DBPath, err = expandHome(cfg.Memory.DBPath)
		if err != nil {
			return nil, fmt.Errorf("LoadServerConfig: expanding memory.db_path: %w", err)
		}
	}

	// Default memory.max_history to 100.
	if cfg.Memory.MaxHistory == 0 {
		cfg.Memory.MaxHistory = 100
	}

	// Default cross_channel_context to 10 messages per other channel.
	// Use -1 in config to explicitly disable (0 means "use default").
	if cfg.Memory.CrossChannelContext == 0 {
		cfg.Memory.CrossChannelContext = 10
	} else if cfg.Memory.CrossChannelContext < 0 {
		cfg.Memory.CrossChannelContext = 0
	}

	// Default summary_threshold to 80% of max_history.
	if cfg.Memory.SummaryThreshold == 0 {
		cfg.Memory.SummaryThreshold = cfg.Memory.MaxHistory * 80 / 100
	}
	// Validate summary_threshold bounds.
	if cfg.Memory.SummaryThreshold < 0 {
		return nil, fmt.Errorf("LoadServerConfig: memory.summary_threshold must be non-negative, got %d", cfg.Memory.SummaryThreshold)
	}
	if cfg.Memory.SummaryThreshold >= cfg.Memory.MaxHistory {
		return nil, fmt.Errorf("LoadServerConfig: memory.summary_threshold (%d) must be less than memory.max_history (%d)", cfg.Memory.SummaryThreshold, cfg.Memory.MaxHistory)
	}

	// Default vault.db_path to data_dir/vault.db when vault is enabled.
	if cfg.Vault.Enabled && cfg.Vault.DBPath == "" {
		cfg.Vault.DBPath = filepath.Join(cfg.Server.DataDir, "vault.db")
	} else if cfg.Vault.DBPath != "" {
		cfg.Vault.DBPath, err = expandHome(cfg.Vault.DBPath)
		if err != nil {
			return nil, fmt.Errorf("LoadServerConfig: expanding vault.db_path: %w", err)
		}
	}

	// Default permissions file path to data_dir/permissions.toml.
	if cfg.Security.PermissionsFile == "" {
		cfg.Security.PermissionsFile = filepath.Join(cfg.Server.DataDir, "permissions.toml")
	} else {
		cfg.Security.PermissionsFile, err = expandHome(cfg.Security.PermissionsFile)
		if err != nil {
			return nil, fmt.Errorf("LoadServerConfig: expanding security.permissions_file: %w", err)
		}
	}

	// Default API listen address.
	if cfg.API.Enabled && cfg.API.Listen == "" {
		cfg.API.Listen = "127.0.0.1:8080"
	}
	// Default event retention to 30 days.
	if cfg.API.EventRetentionDays == 0 {
		cfg.API.EventRetentionDays = 30
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("LoadServerConfig: %w", err)
	}

	return &cfg, nil
}

// Validate checks that all required server configuration fields are present
// and valid.
func (c *ServerConfig) Validate() error {
	if c.IRC.Server == "" {
		return fmt.Errorf("irc.server is required")
	}
	if c.IRC.Nick == "" {
		return fmt.Errorf("irc.nick is required")
	}
	if c.IRC.Port == 0 {
		c.IRC.Port = 6697
	}
	if c.IRC.MaxLineLen == 0 {
		c.IRC.MaxLineLen = 512
	}
	if c.IRC.MaxLineLen < 512 {
		return fmt.Errorf("irc.max_line_len must be at least 512")
	}
	if c.IRC.Channels.Main == "" {
		return fmt.Errorf("irc.channels.main is required")
	}
	if c.IRC.Channels.Bus == "" {
		return fmt.Errorf("irc.channels.bus is required")
	}
	if err := validatePositiveDuration(c.Scheduler.HeartbeatInterval, "scheduler.heartbeat_interval"); err != nil {
		return err
	}
	if err := validatePositiveDuration(c.Scheduler.ClientTimeout, "scheduler.client_timeout"); err != nil {
		return err
	}
	if err := validatePositiveDuration(c.Scheduler.TickInterval, "scheduler.tick_interval"); err != nil {
		return err
	}
	if err := validatePositiveDuration(c.Approval.Timeout, "approval.timeout"); err != nil {
		return err
	}

	// Validate LLM config: if a default is specified, it must exist in providers.
	if c.LLM.Default != "" && len(c.LLM.Providers) > 0 {
		if _, ok := c.LLM.Providers[c.LLM.Default]; !ok {
			return fmt.Errorf("llm.default %q not found in llm.providers", c.LLM.Default)
		}
	}

	// Validate tool configurations.
	if err := c.Tools.validate(); err != nil {
		return err
	}

	return nil
}

// ParseScheduler parses the duration strings in SchedulerConfig into
// time.Duration values, applying defaults where needed.
func (c *ServerConfig) ParseScheduler() (ParsedSchedulerConfig, error) {
	var parsed ParsedSchedulerConfig
	var err error

	if c.Scheduler.HeartbeatInterval != "" {
		parsed.HeartbeatInterval, err = time.ParseDuration(c.Scheduler.HeartbeatInterval)
		if err != nil {
			return parsed, fmt.Errorf("ParseScheduler: heartbeat_interval: %w", err)
		}
	} else {
		parsed.HeartbeatInterval = 5 * time.Minute
	}

	if c.Scheduler.ClientTimeout != "" {
		parsed.ClientTimeout, err = time.ParseDuration(c.Scheduler.ClientTimeout)
		if err != nil {
			return parsed, fmt.Errorf("ParseScheduler: client_timeout: %w", err)
		}
	} else {
		parsed.ClientTimeout = 2 * time.Minute
	}

	if c.Scheduler.TickInterval != "" {
		parsed.TickInterval, err = time.ParseDuration(c.Scheduler.TickInterval)
		if err != nil {
			return parsed, fmt.Errorf("ParseScheduler: tick_interval: %w", err)
		}
	} else {
		parsed.TickInterval = 30 * time.Second
	}

	parsed.MaxConcurrent = c.Scheduler.MaxConcurrent
	if parsed.MaxConcurrent <= 0 {
		parsed.MaxConcurrent = 3
	}

	return parsed, nil
}

// ParseApprovalTimeout parses the approval timeout duration string, applying
// a default of 2 minutes if not set.
func (c *ServerConfig) ParseApprovalTimeout() (time.Duration, error) {
	if c.Approval.Timeout == "" {
		return 2 * time.Minute, nil
	}
	d, err := time.ParseDuration(c.Approval.Timeout)
	if err != nil {
		return 0, fmt.Errorf("ParseApprovalTimeout: %w", err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("ParseApprovalTimeout: must be positive, got %s", c.Approval.Timeout)
	}
	return d, nil
}

// validatePositiveDuration checks that a duration string, if non-empty, parses
// to a positive value. Zero and negative durations are rejected because they
// would cause panics in time.NewTicker.
func validatePositiveDuration(s, field string) error {
	if s == "" {
		return nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	if d <= 0 {
		return fmt.Errorf("%s: must be positive, got %s", field, s)
	}
	return nil
}

// expandHome replaces a leading ~ with the user's home directory.
func expandHome(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expandHome: %w", err)
	}
	return filepath.Join(home, path[1:]), nil
}
