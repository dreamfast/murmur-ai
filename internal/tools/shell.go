package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// shellMetachars is the set of shell metacharacters that are rejected when
// a whitelist is active. This prevents command chaining and injection.
var shellMetachars = []string{";", "&&", "||", "|", "`", "$(", ">", "<", "\n", "&"}

// ShellToolConfig holds runtime configuration for the Docker-sandboxed
// shell execution tool.
type ShellToolConfig struct {
	// DockerImage is the Docker image to use (e.g., "ubuntu:24.04").
	DockerImage string
	// Network enables network access inside the container.
	Network bool
	// MemoryLimit is the Docker memory limit (e.g., "256m").
	MemoryLimit string
	// CPULimit is the Docker CPU limit (e.g., "0.5").
	CPULimit string
	// Timeout is the maximum execution time.
	Timeout time.Duration
	// Workspace is a host directory to mount read-only at /workspace.
	Workspace string
	// Whitelist is a list of allowed commands. Empty means allow all.
	Whitelist []string
}

// NewShellTool creates a Docker-sandboxed shell execution tool.
// Commands are executed inside ephemeral containers with security hardening
// including --cap-drop=ALL, --security-opt=no-new-privileges, --read-only,
// and --network=none (unless explicitly enabled).
func NewShellTool(cfg ShellToolConfig) Tool {
	return Tool{
		Name:        "shell",
		Description: "Execute a shell command inside a Docker container. Use the 'target' parameter to choose which host to run on.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {
					"type": "string",
					"description": "The shell command to execute inside a Docker container"
				},
				"target": {
					"type": "string",
					"description": "The name of the host to run the command on. Omit to run on the server."
				}
			},
			"required": ["command"]
		}`),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return handleShell(ctx, cfg, args)
		},
	}
}

// handleShell extracts the command, validates it against the whitelist,
// builds Docker arguments, and executes the command in a sandboxed container.
func handleShell(ctx context.Context, cfg ShellToolConfig, args map[string]any) (string, error) {
	command, err := RequireStringArg(args, "command")
	if err != nil {
		return "", err
	}

	// Whitelist validation when whitelist is non-empty.
	if len(cfg.Whitelist) > 0 {
		if ContainsShellMetachars(command) {
			return "", fmt.Errorf("command contains disallowed shell metacharacters")
		}
		if !IsAllowed(command, cfg.Whitelist) {
			return "", fmt.Errorf("command %q is not in the whitelist", command)
		}
	}

	dockerArgs := BuildDockerArgs(cfg, command)

	// Apply timeout if configured.
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	output, err := RunCommand(ctx, "docker", dockerArgs...)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("shell: command timed out or cancelled: %w", ctx.Err())
		}
		// Return output alongside error for non-zero exit codes.
		if output != "" {
			return output, nil
		}
		return "", fmt.Errorf("shell: %w", err)
	}

	return output, nil
}

// ContainsShellMetachars returns true if the command contains any shell
// metacharacters that could be used for command injection. Checked when
// a whitelist is active.
func ContainsShellMetachars(command string) bool {
	for _, mc := range shellMetachars {
		if strings.Contains(command, mc) {
			return true
		}
	}
	return false
}

// IsAllowed checks whether a command is permitted by the whitelist.
// Matching rules:
//   - Exact match: the command equals a whitelist entry exactly
//   - Glob suffix: a whitelist entry ending with "*" matches if the command
//     starts with the prefix and the remainder (the argument) contains no
//     whitespace (i.e., only a single argument is allowed after the prefix)
//
// Returns true if the whitelist is empty (allow all).
func IsAllowed(command string, whitelist []string) bool {
	if len(whitelist) == 0 {
		return true
	}

	for _, entry := range whitelist {
		if command == entry {
			return true
		}
		// Glob suffix matching: "ls *" matches "ls -la" but not "ls -la /tmp".
		if strings.HasSuffix(entry, "*") {
			prefix := strings.TrimSuffix(entry, "*")
			if strings.HasPrefix(command, prefix) {
				remainder := command[len(prefix):]
				// The remainder must be a single argument (no whitespace).
				// Check for spaces, tabs, and other whitespace to prevent
				// bypass via alternative shell separators.
				if !strings.ContainsAny(remainder, " \t\n\r\v\f") {
					return true
				}
			}
		}
	}

	return false
}

// BuildDockerArgs constructs the Docker CLI arguments for running a command
// in a sandboxed container. Security hardening flags are always applied:
// --rm, --cap-drop=ALL, --security-opt=no-new-privileges, --read-only.
func BuildDockerArgs(cfg ShellToolConfig, command string) []string {
	args := []string{
		"run",
		"--rm",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--read-only",
	}

	if !cfg.Network {
		args = append(args, "--network=none")
	}

	if cfg.MemoryLimit != "" {
		args = append(args, "--memory="+cfg.MemoryLimit)
	}

	if cfg.CPULimit != "" {
		args = append(args, "--cpus="+cfg.CPULimit)
	}

	if cfg.Workspace != "" {
		args = append(args, "-v", cfg.Workspace+":/workspace:ro")
	}

	args = append(args, cfg.DockerImage, "bash", "-c", command)

	return args
}
