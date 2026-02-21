package tools

import (
	"fmt"
	"log/slog"
	"time"

	"murmur/internal/config"
)

// SecretResolver resolves a configuration value that may reference an
// external secret store (e.g., "vault:keyname"). If the value has no special
// prefix, it is returned as-is. Pass nil to disable secret resolution.
type SecretResolver func(value string) (string, error)

// IRCManager provides IRC channel management operations. This interface is
// defined in the tools package (where it's consumed) to avoid coupling tools
// to the irc package. The irc.Connection type satisfies this interface.
type IRCManager interface {
	// Join joins an IRC channel.
	Join(channel string) error
	// Part leaves an IRC channel.
	Part(channel string) error
	// Send sends a message to an IRC channel.
	Send(channel, message string)
	// SetTopic sets the topic for an IRC channel.
	SetTopic(channel, topic string) error
	// Kick removes a user from a channel with an optional reason.
	Kick(channel, user, reason string) error
	// Ban sets +b on a hostmask in a channel.
	Ban(channel, mask string) error
	// Unban removes +b on a hostmask in a channel.
	Unban(channel, mask string) error
	// SetMode sets a channel mode (e.g., "+o", "-v") with optional params.
	SetMode(channel, mode string, params ...string) error
	// Channels returns the list of currently joined channels.
	Channels() []string
}

// MemoryMessage is a simplified conversation message used by the MemoryReader
// interface. It contains only the fields needed for cross-channel history
// reading, avoiding a dependency on the llm package.
type MemoryMessage struct {
	// Role is the message role (e.g., "user", "assistant", "system", "tool").
	Role string
	// Content is the message text content.
	Content string
}

// MemoryReader provides read access to conversation history. This interface
// is defined in the tools package (where it's consumed) to avoid coupling
// tools to the server/memory package. The server.Memory type satisfies this
// interface via an adapter.
type MemoryReader interface {
	// GetHistory retrieves the last limit messages for a channel.
	GetHistory(channel string, limit int) ([]MemoryMessage, error)
	// GetHistoryCount returns the number of messages stored for a channel.
	GetHistoryCount(channel string) (int, error)
}

// ChannelPersister persists channel join/part state so that dynamically
// joined channels survive reconnects. The server.ChannelSettingsStore
// satisfies this interface.
type ChannelPersister interface {
	// SetAutoJoin marks a channel for automatic rejoin on reconnect (true)
	// or clears the auto-join flag (false).
	SetAutoJoin(channel string, autoJoin bool) error
}

// BuildToolsOpts holds the parameters for BuildTools. Using a struct avoids
// long positional argument lists and makes it easy to add new optional
// dependencies without breaking callers.
type BuildToolsOpts struct {
	// Config is the unified tool configuration. Required.
	Config *config.ToolsConfig
	// Logger is used to report which tools are enabled at startup. Required.
	Logger *slog.Logger
	// Resolver resolves secret references (e.g., "vault:" prefixed values).
	// Pass nil to disable secret resolution (when secrets are resolved upfront).
	Resolver SecretResolver
	// IRCManager provides IRC channel management for the irc_manage tool.
	// Pass nil when IRC management is not available (e.g., on clients).
	IRCManager IRCManager
	// Memory provides read access to conversation history for cross-channel
	// operations. Pass nil when memory access is not available.
	Memory MemoryReader
	// BusChannel is the IRC bus channel name (e.g., "#murmur-bus"). Used by
	// the irc_manage tool to prevent parting the bus channel. Required when
	// IRCManage is enabled.
	BusChannel string
	// ChannelPersister persists channel join/part state for auto-rejoin on
	// reconnect. Pass nil to disable persistence (joins/parts are ephemeral).
	ChannelPersister ChannelPersister
}

// BuildTools creates tool instances from the tool configuration. Each enabled
// tool section produces one Tool entry. Tools that are not configured (nil
// pointer) or not enabled are skipped. The logger is used to report which
// tools are enabled at startup.
func BuildTools(opts BuildToolsOpts) ([]Tool, error) {
	cfg := opts.Config
	logger := opts.Logger
	resolver := opts.Resolver

	if cfg == nil {
		return nil, nil
	}

	var result []Tool

	if cfg.SystemInfo != nil && cfg.SystemInfo.Enabled {
		result = append(result, NewSystemInfoTool())
		logger.Info("enabled tool", "name", "system_info")
	}

	if cfg.Shell != nil && cfg.Shell.Enabled {
		timeout, err := cfg.Shell.ParseTimeout()
		if err != nil {
			return nil, fmt.Errorf("BuildTools: shell timeout: %w", err)
		}
		shellCfg := ShellToolConfig{
			DockerImage: cfg.Shell.DockerImage,
			Network:     cfg.Shell.Network,
			MemoryLimit: cfg.Shell.MemoryLimit,
			CPULimit:    cfg.Shell.CPULimit,
			Timeout:     timeout,
			Workspace:   cfg.Shell.Workspace,
			Whitelist:   cfg.Shell.Whitelist,
		}
		result = append(result, NewShellTool(shellCfg))
		logger.Info("enabled tool", "name", "shell")
	}

	if cfg.MailRead != nil && cfg.MailRead.Enabled {
		result = append(result, NewMailReadTool(cfg.MailRead.ThunderbirdProfile, cfg.MailRead.MailDir))
		logger.Info("enabled tool", "name", "mail_read")
	}

	if cfg.MailSend != nil && cfg.MailSend.Enabled {
		smtpPass := cfg.MailSend.SMTPPass
		if resolver != nil && smtpPass != "" {
			resolved, err := resolver(smtpPass)
			if err != nil {
				return nil, fmt.Errorf("BuildTools: mail_send smtp_password: %w", err)
			}
			smtpPass = resolved
		}
		sendCfg := MailSendConfig{
			SMTPHost:    cfg.MailSend.SMTPHost,
			SMTPPort:    cfg.MailSend.SMTPPort,
			SMTPUser:    cfg.MailSend.SMTPUser,
			SMTPPass:    smtpPass,
			FromAddress: cfg.MailSend.FromAddress,
			RequireTLS:  cfg.MailSend.GetRequireTLS(),
		}
		result = append(result, NewMailSendTool(sendCfg, nil))
		logger.Info("enabled tool", "name", "mail_send")
	}

	if cfg.CodeExec != nil && cfg.CodeExec.Enabled {
		execCfg := CodeExecToolConfig{
			PistonURL:      cfg.CodeExec.PistonURL,
			DefaultLang:    cfg.CodeExec.DefaultLang,
			RunTimeout:     cfg.CodeExec.RunTimeout,
			RunMemoryLimit: cfg.CodeExec.RunMemoryLimit,
		}
		result = append(result, NewCodeExecTool(execCfg))
		logger.Info("enabled tool", "name", "code_exec")
	}

	if cfg.WebSearch != nil && cfg.WebSearch.Enabled {
		apiKey := cfg.WebSearch.APIKey
		if resolver != nil && apiKey != "" {
			resolved, err := resolver(apiKey)
			if err != nil {
				return nil, fmt.Errorf("BuildTools: web_search api_key: %w", err)
			}
			apiKey = resolved
		}
		searchCfg := WebSearchToolConfig{
			APIKey:     apiKey,
			MaxResults: cfg.WebSearch.MaxResults,
		}
		result = append(result, NewWebSearchTool(searchCfg, nil))
		logger.Info("enabled tool", "name", "web_search")
	}

	if cfg.Git != nil && cfg.Git.Enabled {
		gitCfg := GitOpsToolConfig{
			AllowedRepos: cfg.Git.AllowedRepos,
		}
		result = append(result, NewGitOpsTool(gitCfg))
		logger.Info("enabled tool", "name", "git_ops")
	}

	if cfg.RSS != nil && cfg.RSS.Enabled {
		rssCfg := RSSToolConfig{
			MaxItems: cfg.RSS.MaxItems,
		}
		result = append(result, NewRSSTool(rssCfg))
		logger.Info("enabled tool", "name", "rss_read")
	}

	if cfg.DNS != nil && cfg.DNS.Enabled {
		result = append(result, NewDNSCheckTool())
		logger.Info("enabled tool", "name", "dns_check")
	}

	if cfg.ImageGen != nil && cfg.ImageGen.Enabled {
		imgCfg := ImageGenToolConfig{
			ComfyUIHost:    cfg.ImageGen.ComfyUIHost,
			OutputDir:      cfg.ImageGen.OutputDir,
			UploadURL:      cfg.ImageGen.UploadURL,
			CheckpointName: cfg.ImageGen.CheckpointName,
		}
		result = append(result, NewImageGenTool(imgCfg))
		logger.Info("enabled tool", "name", "image_gen")
	}

	if cfg.FileOps != nil && cfg.FileOps.Enabled {
		foCfg := FileOpsToolConfig{
			AllowedPaths: cfg.FileOps.AllowedPaths,
		}
		result = append(result, NewFileOpsTool(foCfg))
		logger.Info("enabled tool", "name", "file_ops")
	}

	if cfg.SearXNG != nil && cfg.SearXNG.Enabled {
		searxngCfg := SearXNGToolConfig{
			URL:        cfg.SearXNG.URL,
			MaxResults: cfg.SearXNG.MaxResults,
		}
		result = append(result, NewSearXNGTool(searxngCfg, nil))
		logger.Info("enabled tool", "name", "searxng_search")
	}

	if cfg.OpenCode != nil && cfg.OpenCode.Enabled {
		sessionTimeout := openCodeDefaultTimeout
		if cfg.OpenCode.SessionTimeout != "" {
			d, err := time.ParseDuration(cfg.OpenCode.SessionTimeout)
			if err != nil {
				return nil, fmt.Errorf("BuildTools: opencode session_timeout: %w", err)
			}
			sessionTimeout = d
		}
		password := cfg.OpenCode.Password
		if resolver != nil && password != "" {
			resolved, err := resolver(password)
			if err != nil {
				return nil, fmt.Errorf("BuildTools: opencode password: %w", err)
			}
			password = resolved
		}
		ocCfg := OpenCodeToolConfig{
			URL:            cfg.OpenCode.URL,
			Username:       cfg.OpenCode.Username,
			Password:       password,
			SessionTimeout: sessionTimeout,
		}
		result = append(result, NewOpenCodeTool(ocCfg, nil))
		logger.Info("enabled tool", "name", "opencode")
	}

	if cfg.ConfigManage != nil && cfg.ConfigManage.Enabled {
		cmCfg := ConfigManageToolConfig{
			ConfigPath: cfg.ConfigManage.ConfigPath,
		}
		result = append(result, NewConfigManageTool(cmCfg))
		logger.Info("enabled tool", "name", "config_manage")
	}

	if cfg.IRCManage != nil && cfg.IRCManage.Enabled {
		if opts.IRCManager == nil {
			return nil, fmt.Errorf("BuildTools: irc_manage requires IRCManager but none was provided")
		}
		if opts.BusChannel == "" {
			return nil, fmt.Errorf("BuildTools: irc_manage requires BusChannel but none was provided")
		}
		result = append(result, NewIRCManageTool(opts.IRCManager, opts.Memory, opts.BusChannel, opts.ChannelPersister))
		logger.Info("enabled tool", "name", "irc_manage")
	}

	return result, nil
}
