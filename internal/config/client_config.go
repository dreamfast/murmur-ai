package config

import (
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// AutonomyLevel represents the autonomy level for a client.
type AutonomyLevel string

const (
	// AutonomyReport means the client can only run read-only/safe commands.
	AutonomyReport AutonomyLevel = "report"
	// AutonomyApprove means the client proposes actions and waits for user approval.
	AutonomyApprove AutonomyLevel = "approve"
	// AutonomyAuto means the client executes actions automatically.
	AutonomyAuto AutonomyLevel = "auto"
)

// ClientConfig holds the complete client configuration loaded from TOML.
type ClientConfig struct {
	Client    ClientSection        `toml:"client"`
	IRC       IRCConfig            `toml:"irc"`
	Heartbeat HeartbeatConfig      `toml:"heartbeat"`
	Security  ClientSecurityConfig `toml:"security"`
	Vault     VaultConfig          `toml:"vault"`
	Tools     ToolsConfig          `toml:"tools"`
	Cron      []CronJobConfig      `toml:"cron"`
	API       APIConfig            `toml:"api"`
}

// CronJobConfig defines a client-side cron job loaded from TOML configuration.
type CronJobConfig struct {
	// Name is a unique identifier for this cron job.
	Name string `toml:"name"`
	// Schedule is a standard 5-field cron expression (minute hour dom month dow). All times UTC.
	Schedule string `toml:"schedule"`
	// Command is the argument string passed to the tool handler.
	Command string `toml:"command"`
	// Tool is the name of the tool to execute (must be enabled in [tools]).
	Tool string `toml:"tool"`
	// Notify controls whether results are sent to the server via bus.
	Notify bool `toml:"notify"`
	// NotifyOnlyOnChange sends notifications only when output changes from the previous run.
	NotifyOnlyOnChange bool `toml:"notify_only_on_change"`
	// NotifyOnlyOnError sends notifications only when the tool returns an error.
	NotifyOnlyOnError bool `toml:"notify_only_on_error"`
}

// ClientSecurityConfig holds security-related settings for the client.
type ClientSecurityConfig struct {
	// BusKey is a shared secret for bus message authentication (HMAC-SHA256).
	// Must match the server's security.bus_key. Optional — when empty, messages
	// are sent unsigned and received without verification.
	BusKey string `toml:"bus_key"`
}

// ClientSection holds general client settings.
type ClientSection struct {
	// ID is the unique client identifier (e.g., "laptop-home").
	ID string `toml:"id"`
	// Hostname is the machine hostname for display purposes.
	Hostname string `toml:"hostname"`
	// Autonomy is the autonomy level: "report", "approve", or "auto".
	Autonomy string `toml:"autonomy"`
}

// HeartbeatConfig holds heartbeat timing settings.
type HeartbeatConfig struct {
	// Interval is how often to send heartbeats (e.g., "30s").
	Interval string `toml:"interval"`
}

// LoadClientConfig reads and parses a client TOML configuration file.
func LoadClientConfig(path string) (*ClientConfig, error) {
	expanded, err := expandHome(path)
	if err != nil {
		return nil, fmt.Errorf("LoadClientConfig: %w", err)
	}

	data, err := os.ReadFile(expanded)
	if err != nil {
		return nil, fmt.Errorf("LoadClientConfig: %w", err)
	}

	var cfg ClientConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("LoadClientConfig: %w", err)
	}

	// Default vault.db_path to ~/.murmur/vault.db when vault is enabled.
	if cfg.Vault.Enabled && cfg.Vault.DBPath == "" {
		defaultPath, err := expandHome("~/.murmur/vault.db")
		if err != nil {
			return nil, fmt.Errorf("LoadClientConfig: expanding default vault.db_path: %w", err)
		}
		cfg.Vault.DBPath = defaultPath
	} else if cfg.Vault.DBPath != "" {
		cfg.Vault.DBPath, err = expandHome(cfg.Vault.DBPath)
		if err != nil {
			return nil, fmt.Errorf("LoadClientConfig: expanding vault.db_path: %w", err)
		}
	}

	// Default API listen address for client.
	if cfg.API.Enabled && cfg.API.Listen == "" {
		cfg.API.Listen = "127.0.0.1:8081"
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("LoadClientConfig: %w", err)
	}

	return &cfg, nil
}

// Validate checks that all required client configuration fields are present
// and valid.
func (c *ClientConfig) Validate() error {
	if c.Client.ID == "" {
		return fmt.Errorf("client.id is required")
	}
	if err := c.IRC.Validate(); err != nil {
		return err
	}
	if c.IRC.BusChannel == "" {
		return fmt.Errorf("irc.bus_channel is required")
	}
	if c.Client.Autonomy == "" {
		c.Client.Autonomy = string(AutonomyReport)
	}
	switch AutonomyLevel(c.Client.Autonomy) {
	case AutonomyReport, AutonomyApprove, AutonomyAuto:
		// valid
	default:
		return fmt.Errorf("client.autonomy must be one of: report, approve, auto")
	}
	if err := validatePositiveDuration(c.Heartbeat.Interval, "heartbeat.interval"); err != nil {
		return err
	}

	// Validate tool configurations.
	if err := c.Tools.validate(); err != nil {
		return err
	}

	// Validate cron job configurations.
	cronNames := make(map[string]struct{}, len(c.Cron))
	for i, job := range c.Cron {
		if job.Name == "" {
			return fmt.Errorf("cron[%d].name is required", i)
		}
		if _, dup := cronNames[job.Name]; dup {
			return fmt.Errorf("cron[%d].name %q is a duplicate", i, job.Name)
		}
		cronNames[job.Name] = struct{}{}
		if job.Schedule == "" {
			return fmt.Errorf("cron[%d].schedule is required", i)
		}
		if job.Tool == "" {
			return fmt.Errorf("cron[%d].tool is required", i)
		}
	}

	return nil
}

// ParseHeartbeatInterval parses the heartbeat interval string into a
// time.Duration, applying a default of 30s if not set.
func (c *ClientConfig) ParseHeartbeatInterval() (time.Duration, error) {
	d, err := parseDurationDefault(c.Heartbeat.Interval, 30*time.Second)
	if err != nil {
		return 0, fmt.Errorf("ParseHeartbeatInterval: %w", err)
	}
	return d, nil
}
