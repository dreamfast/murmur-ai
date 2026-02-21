package config

import (
	"fmt"
	"path/filepath"
	"time"
)

// ToolsConfig holds configuration for all tools. Each field is a pointer so
// that absent TOML sections result in nil (tool not configured) rather than a
// zero-value struct (tool configured but disabled). This struct is shared by
// both server and client configurations.
type ToolsConfig struct {
	// SystemInfo configures the system_info tool.
	SystemInfo *SystemInfoConfig `toml:"systeminfo"`
	// Shell configures the Docker-sandboxed shell tool.
	Shell *ShellConfig `toml:"shell"`
	// CodeExec configures the Piston code execution tool.
	CodeExec *CodeExecConfig `toml:"code_exec"`
	// MailRead configures the Thunderbird mbox mail reader tool.
	MailRead *MailReadConfig `toml:"mail_read"`
	// MailSend configures the SMTP mail sending tool.
	MailSend *MailSendConfig `toml:"mail_send"`
	// WebSearch configures the Brave Search API web search tool.
	WebSearch *WebSearchConfig `toml:"web_search"`
	// Git configures the git_ops tool for read-only git operations.
	Git *GitConfig `toml:"git"`
	// RSS configures the rss_read tool for fetching RSS/Atom feeds.
	RSS *RSSConfig `toml:"rss"`
	// DNS configures the dns_check tool for DNS lookups and SSL inspection.
	DNS *DNSConfig `toml:"dns"`
	// ImageGen configures the image_gen tool for ComfyUI image generation.
	ImageGen *ImageGenConfig `toml:"image_gen"`
	// FileOps configures the file_ops tool for read-only file operations.
	FileOps *FileOpsConfig `toml:"file_ops"`
	// HTTP configures the http_request tool for making HTTP requests.
	HTTP *HTTPToolConfig `toml:"http"`
	// SearXNG configures the searxng_search tool for SearXNG-based web search.
	SearXNG *SearXNGConfig `toml:"searxng"`
	// OpenCode configures the opencode tool for interacting with an OpenCode agent.
	OpenCode *OpenCodeConfig `toml:"opencode"`
	// ConfigManage configures the config_manage tool for reading/writing TOML config.
	ConfigManage *ConfigManageConfig `toml:"config_manage"`
	// IRCManage configures the irc_manage tool for IRC channel management.
	IRCManage *IRCManageConfig `toml:"irc_manage"`
}

// SystemInfoConfig configures the system_info tool which provides safe,
// read-only system queries (uptime, disk, memory, cpu, etc.).
type SystemInfoConfig struct {
	// Enabled controls whether the system_info tool is registered.
	Enabled bool `toml:"enabled"`
}

// ShellConfig configures the Docker-sandboxed shell execution tool.
type ShellConfig struct {
	// Enabled controls whether the shell tool is registered.
	Enabled bool `toml:"enabled"`
	// DockerImage is the Docker image to use for command execution.
	// Defaults to "ubuntu:24.04" if empty.
	DockerImage string `toml:"docker_image"`
	// Network enables network access inside the container. Default false.
	Network bool `toml:"network"`
	// MemoryLimit is the Docker memory limit (e.g., "256m").
	MemoryLimit string `toml:"memory_limit"`
	// CPULimit is the Docker CPU limit (e.g., "0.5").
	CPULimit string `toml:"cpu_limit"`
	// Timeout is the maximum execution time (e.g., "30s"). Defaults to "30s".
	Timeout string `toml:"timeout"`
	// Workspace is a host directory to mount read-only at /workspace.
	Workspace string `toml:"workspace"`
	// Whitelist is a list of allowed commands. When non-empty, only commands
	// matching an entry are permitted. Entries may end with * for prefix
	// matching (single trailing argument only).
	Whitelist []string `toml:"whitelist"`
}

// ParseTimeout parses the shell timeout string into a time.Duration.
// Returns a default of 30s if the timeout is not set.
func (s *ShellConfig) ParseTimeout() (time.Duration, error) {
	if s.Timeout == "" {
		return 30 * time.Second, nil
	}
	d, err := time.ParseDuration(s.Timeout)
	if err != nil {
		return 0, fmt.Errorf("ShellConfig.ParseTimeout: %w", err)
	}
	return d, nil
}

// MailReadConfig configures the Thunderbird mbox mail reader tool.
type MailReadConfig struct {
	// Enabled controls whether the mail_read tool is registered.
	Enabled bool `toml:"enabled"`
	// ThunderbirdProfile is the path to the Thunderbird profile directory
	// (e.g., "~/.thunderbird/abc123.default-release"). Required when enabled.
	ThunderbirdProfile string `toml:"thunderbird_profile"`
	// MailDir is the mail directory relative to the profile
	// (e.g., "Mail/pop3.example.com"). Defaults to "Mail/Local Folders".
	MailDir string `toml:"mail_dir"`
}

// MailSendConfig configures the SMTP mail sending tool.
type MailSendConfig struct {
	// Enabled controls whether the mail_send tool is registered.
	Enabled bool `toml:"enabled"`
	// SMTPHost is the SMTP server hostname (e.g., "mail.example.com").
	// Required when enabled.
	SMTPHost string `toml:"smtp_host"`
	// SMTPPort is the SMTP server port. Defaults to 587 (STARTTLS).
	SMTPPort int `toml:"smtp_port"`
	// SMTPUser is the SMTP authentication username.
	SMTPUser string `toml:"smtp_user"`
	// SMTPPass is the SMTP authentication password. Supports "vault:" prefix
	// for vault-based secret resolution.
	SMTPPass string `toml:"smtp_password"`
	// FromAddress is the sender email address. Required when enabled.
	FromAddress string `toml:"from_address"`
	// RequireTLS requires STARTTLS for the SMTP connection. Defaults to true.
	RequireTLS *bool `toml:"require_tls"`
}

// GetRequireTLS returns the RequireTLS value, defaulting to true if not set.
func (m *MailSendConfig) GetRequireTLS() bool {
	if m.RequireTLS == nil {
		return true
	}
	return *m.RequireTLS
}

// WebSearchConfig configures the Brave Search API web search tool.
type WebSearchConfig struct {
	// Enabled controls whether the web_search tool is registered.
	Enabled bool `toml:"enabled"`
	// APIKey is the Brave Search API subscription token. Supports "vault:" prefix.
	// Required when enabled.
	APIKey string `toml:"api_key"`
	// MaxResults is the default number of results to return. Defaults to 5, capped at 20.
	MaxResults int `toml:"max_results"`
}

// GitConfig configures the git_ops tool for read-only git operations
// (log, diff, status, branch, show) on allowed repositories.
type GitConfig struct {
	// Enabled controls whether the git_ops tool is registered.
	Enabled bool `toml:"enabled"`
	// AllowedRepos is a list of absolute paths to git repositories that the
	// tool is permitted to access. Requests for repos not in this list are
	// rejected.
	AllowedRepos []string `toml:"allowed_repos"`
}

// RSSConfig configures the rss_read tool for fetching and parsing RSS 2.0
// and Atom feeds. The tool accepts any http/https URL from the LLM — there
// is no URL allowlist since this is a personal agent tool.
type RSSConfig struct {
	// Enabled controls whether the rss_read tool is registered.
	Enabled bool `toml:"enabled"`
	// MaxItems is the maximum number of items to return per feed.
	// Defaults to 10, capped at 50.
	MaxItems int `toml:"max_items"`
}

// DNSConfig configures the dns_check tool for DNS lookups, SSL certificate
// inspection, and whois expiry checks.
type DNSConfig struct {
	// Enabled controls whether the dns_check tool is registered.
	Enabled bool `toml:"enabled"`
}

// ImageGenConfig configures the image_gen tool for generating images via
// a ComfyUI instance.
type ImageGenConfig struct {
	// Enabled controls whether the image_gen tool is registered.
	Enabled bool `toml:"enabled"`
	// ComfyUIHost is the base URL of the ComfyUI API (e.g., "http://gpu-rig:8188").
	// Required when enabled.
	ComfyUIHost string `toml:"comfyui_host"`
	// OutputDir is the local directory for saving generated images.
	// Required when enabled.
	OutputDir string `toml:"output_dir"`
	// UploadURL is an optional URL to upload images for sharing. If empty,
	// images are only saved locally.
	UploadURL string `toml:"upload_url"`
	// CheckpointName is the model checkpoint filename used in the ComfyUI workflow.
	// Defaults to "sd_xl_base_1.0.safetensors" if empty.
	CheckpointName string `toml:"checkpoint_name"`
}

// FileOpsConfig configures the file_ops tool for read-only file operations
// (read, list, search, stat) on allowed directories.
type FileOpsConfig struct {
	// Enabled controls whether the file_ops tool is registered.
	Enabled bool `toml:"enabled"`
	// AllowedPaths is a list of directories the tool is permitted to access.
	// Requests for paths not under these directories are rejected.
	AllowedPaths []string `toml:"allowed_paths"`
}

// CodeExecConfig configures the Piston-based code execution tool.
type CodeExecConfig struct {
	// Enabled controls whether the code_exec tool is registered.
	Enabled bool `toml:"enabled"`
	// PistonURL is the base URL of the Piston API (e.g., "http://localhost:2000").
	// Required when enabled.
	PistonURL string `toml:"piston_url"`
	// DefaultLang is the default programming language when none is specified.
	DefaultLang string `toml:"default_language"`
	// RunTimeout is the maximum execution time in milliseconds. When 0 (default),
	// Piston uses its own server-side default (PISTON_RUN_TIMEOUT).
	RunTimeout int `toml:"run_timeout"`
	// RunMemoryLimit is the maximum memory in bytes. When 0 (default), Piston
	// uses its own server-side default (PISTON_RUN_MEMORY_LIMIT). Set this only
	// when you need to override Piston's limit.
	RunMemoryLimit int `toml:"run_memory_limit"`
}

// HTTPToolConfig configures the http_request tool for making arbitrary HTTP
// requests. This tool is typically server-only but is part of the unified
// ToolsConfig for consistency.
type HTTPToolConfig struct {
	// Enabled controls whether the http_request tool is registered.
	Enabled bool `toml:"enabled"`
	// Timeout is the maximum request duration (e.g., "30s"). Defaults to "30s".
	Timeout string `toml:"timeout"`
	// MaxResponseBytes is the maximum response body size in bytes.
	// Defaults to 1048576 (1MB).
	MaxResponseBytes int `toml:"max_response_bytes"`
	// AllowedDomains is an optional list of allowed domains (glob patterns).
	// When empty, all domains are allowed. When non-empty, only matching
	// domains are permitted.
	AllowedDomains []string `toml:"allowed_domains"`
	// BlockPrivateIPs blocks requests to private/loopback IP ranges (SSRF protection).
	// Defaults to true.
	BlockPrivateIPs *bool `toml:"block_private_ips"`
}

// GetBlockPrivateIPs returns the BlockPrivateIPs value, defaulting to true if not set.
func (h *HTTPToolConfig) GetBlockPrivateIPs() bool {
	if h.BlockPrivateIPs == nil {
		return true
	}
	return *h.BlockPrivateIPs
}

// ParseTimeout parses the HTTP timeout string into a time.Duration.
// Returns a default of 30s if the timeout is not set.
func (h *HTTPToolConfig) ParseTimeout() (time.Duration, error) {
	if h.Timeout == "" {
		return 30 * time.Second, nil
	}
	d, err := time.ParseDuration(h.Timeout)
	if err != nil {
		return 0, fmt.Errorf("HTTPToolConfig.ParseTimeout: %w", err)
	}
	return d, nil
}

// SearXNGConfig configures the searxng_search tool for querying a self-hosted
// SearXNG instance via its JSON API.
type SearXNGConfig struct {
	// Enabled controls whether the searxng_search tool is registered.
	Enabled bool `toml:"enabled"`
	// URL is the base URL of the SearXNG instance (e.g., "http://localhost:8080").
	// Required when enabled.
	URL string `toml:"url"`
	// MaxResults is the maximum number of results to return. Defaults to 10.
	MaxResults int `toml:"max_results"`
}

// OpenCodeConfig configures the opencode tool for interacting with an
// OpenCode coding agent via its REST+SSE API.
type OpenCodeConfig struct {
	// Enabled controls whether the opencode tool is registered.
	Enabled bool `toml:"enabled"`
	// URL is the base URL of the OpenCode API (e.g., "http://localhost:3000").
	// Required when enabled.
	URL string `toml:"url"`
	// Username is the HTTP Basic Auth username. Optional.
	Username string `toml:"username"`
	// Password is the HTTP Basic Auth password. Supports "vault:" prefix. Optional.
	Password string `toml:"password"`
	// SessionTimeout is the maximum time to wait for a session to complete
	// (e.g., "5m"). Defaults to "5m".
	SessionTimeout string `toml:"session_timeout"`
}

// ConfigManageConfig configures the config_manage tool for reading and
// writing the TOML configuration file at runtime.
type ConfigManageConfig struct {
	// Enabled controls whether the config_manage tool is registered.
	Enabled bool `toml:"enabled"`
	// ConfigPath is the path to the TOML configuration file to manage.
	// Required when enabled.
	ConfigPath string `toml:"config_path"`
}

// IRCManageConfig configures the irc_manage tool for IRC channel management
// operations (join, part, send, topic, list, cross-channel history).
type IRCManageConfig struct {
	// Enabled controls whether the irc_manage tool is registered.
	Enabled bool `toml:"enabled"`
}

// validate checks tool-specific configuration for correctness and applies
// defaults where appropriate.
func (t *ToolsConfig) validate() error {
	if t.Shell != nil && t.Shell.Enabled {
		if t.Shell.DockerImage == "" {
			t.Shell.DockerImage = "ubuntu:24.04"
		}
		if err := validatePositiveDuration(t.Shell.Timeout, "tools.shell.timeout"); err != nil {
			return err
		}
	}
	if t.MailRead != nil && t.MailRead.Enabled {
		if t.MailRead.ThunderbirdProfile == "" {
			return fmt.Errorf("tools.mail_read.thunderbird_profile is required when mail_read is enabled")
		}
		expanded, err := expandHome(t.MailRead.ThunderbirdProfile)
		if err != nil {
			return fmt.Errorf("tools.mail_read.thunderbird_profile: %w", err)
		}
		t.MailRead.ThunderbirdProfile = expanded
		if t.MailRead.MailDir == "" {
			t.MailRead.MailDir = "Mail/Local Folders"
		}
	}
	if t.MailSend != nil && t.MailSend.Enabled {
		if t.MailSend.SMTPHost == "" {
			return fmt.Errorf("tools.mail_send.smtp_host is required when mail_send is enabled")
		}
		if t.MailSend.FromAddress == "" {
			return fmt.Errorf("tools.mail_send.from_address is required when mail_send is enabled")
		}
		if t.MailSend.SMTPPort == 0 {
			t.MailSend.SMTPPort = 587
		}
	}
	if t.CodeExec != nil && t.CodeExec.Enabled {
		if t.CodeExec.PistonURL == "" {
			return fmt.Errorf("tools.code_exec.piston_url is required when code_exec is enabled")
		}
		// RunTimeout and RunMemoryLimit default to 0 (omitted from the
		// Piston request), which lets Piston use its own server-side
		// defaults (PISTON_RUN_TIMEOUT, PISTON_RUN_MEMORY_LIMIT). This
		// avoids mismatches between the config default and Piston's limit.
	}
	if t.WebSearch != nil && t.WebSearch.Enabled {
		if t.WebSearch.APIKey == "" {
			return fmt.Errorf("tools.web_search.api_key is required when web_search is enabled")
		}
		if t.WebSearch.MaxResults == 0 {
			t.WebSearch.MaxResults = 5
		}
		if t.WebSearch.MaxResults > 20 {
			t.WebSearch.MaxResults = 20
		}
	}
	if t.Git != nil && t.Git.Enabled {
		if len(t.Git.AllowedRepos) == 0 {
			return fmt.Errorf("tools.git.allowed_repos is required when git is enabled")
		}
		for i, repo := range t.Git.AllowedRepos {
			if !filepath.IsAbs(repo) {
				return fmt.Errorf("tools.git.allowed_repos[%d] must be an absolute path: %q", i, repo)
			}
		}
	}
	if t.RSS != nil && t.RSS.Enabled {
		if t.RSS.MaxItems == 0 {
			t.RSS.MaxItems = 10
		}
		if t.RSS.MaxItems > 50 {
			t.RSS.MaxItems = 50
		}
	}
	if t.ImageGen != nil && t.ImageGen.Enabled {
		if t.ImageGen.ComfyUIHost == "" {
			return fmt.Errorf("tools.image_gen.comfyui_host is required when image_gen is enabled")
		}
		if t.ImageGen.OutputDir == "" {
			return fmt.Errorf("tools.image_gen.output_dir is required when image_gen is enabled")
		}
	}
	if t.FileOps != nil && t.FileOps.Enabled {
		if len(t.FileOps.AllowedPaths) == 0 {
			return fmt.Errorf("tools.file_ops.allowed_paths is required when file_ops is enabled")
		}
		for i, p := range t.FileOps.AllowedPaths {
			if !filepath.IsAbs(p) {
				return fmt.Errorf("tools.file_ops.allowed_paths[%d] must be an absolute path: %q", i, p)
			}
		}
	}

	// Validate HTTP tool config.
	if t.HTTP != nil && t.HTTP.Enabled {
		if err := validatePositiveDuration(t.HTTP.Timeout, "tools.http.timeout"); err != nil {
			return err
		}
		if t.HTTP.MaxResponseBytes < 0 {
			return fmt.Errorf("tools.http.max_response_bytes must be non-negative, got %d", t.HTTP.MaxResponseBytes)
		}
		if t.HTTP.MaxResponseBytes == 0 {
			t.HTTP.MaxResponseBytes = 1048576 // 1MB
		}
	}

	// Validate SearXNG config.
	if t.SearXNG != nil && t.SearXNG.Enabled {
		if t.SearXNG.URL == "" {
			return fmt.Errorf("tools.searxng.url is required when searxng is enabled")
		}
		if t.SearXNG.MaxResults == 0 {
			t.SearXNG.MaxResults = 10
		}
		if t.SearXNG.MaxResults > 100 {
			t.SearXNG.MaxResults = 100
		}
	}

	// Validate OpenCode config.
	if t.OpenCode != nil && t.OpenCode.Enabled {
		if t.OpenCode.URL == "" {
			return fmt.Errorf("tools.opencode.url is required when opencode is enabled")
		}
		if t.OpenCode.SessionTimeout == "" {
			t.OpenCode.SessionTimeout = "5m"
		}
		if err := validatePositiveDuration(t.OpenCode.SessionTimeout, "tools.opencode.session_timeout"); err != nil {
			return err
		}
	}

	// Validate ConfigManage config.
	if t.ConfigManage != nil && t.ConfigManage.Enabled {
		if t.ConfigManage.ConfigPath == "" {
			return fmt.Errorf("tools.config_manage.config_path is required when config_manage is enabled")
		}
		expanded, err := expandHome(t.ConfigManage.ConfigPath)
		if err != nil {
			return fmt.Errorf("tools.config_manage.config_path: %w", err)
		}
		t.ConfigManage.ConfigPath = expanded
	}

	return nil
}

// HasExecutionTools returns true if any execution-capable tool (shell or
// code_exec) is enabled. Used to warn about missing bus authentication.
func (t *ToolsConfig) HasExecutionTools() bool {
	if t.Shell != nil && t.Shell.Enabled {
		return true
	}
	if t.CodeExec != nil && t.CodeExec.Enabled {
		return true
	}
	return false
}
