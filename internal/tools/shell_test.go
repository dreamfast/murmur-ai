package tools

import (
	"context"
	"strings"
	"testing"
)

func TestContainsShellMetachars(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		command string
		want    bool
	}{
		{"clean command", "ls -la", false},
		{"semicolon", "ls; rm -rf /", true},
		{"double ampersand", "ls && cat /etc/passwd", true},
		{"double pipe", "ls || echo fail", true},
		{"single pipe", "cat file | grep foo", true},
		{"backtick", "echo `whoami`", true},
		{"dollar paren", "echo $(whoami)", true},
		{"redirect out", "echo hello > /tmp/file", true},
		{"redirect in", "cat < /tmp/file", true},
		{"newline", "ls\nrm -rf /", true},
		{"background ampersand", "sleep 10 & rm -rf /", true},
		{"no metachars", "df -h", false},
		{"empty string", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ContainsShellMetachars(tc.command)
			if got != tc.want {
				t.Errorf("ContainsShellMetachars(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

func TestIsAllowed_ExactMatch(t *testing.T) {
	t.Parallel()

	whitelist := []string{"ls -la", "df -h", "uptime"}
	if !IsAllowed("df -h", whitelist) {
		t.Error("expected 'df -h' to be allowed (exact match)")
	}
	if !IsAllowed("uptime", whitelist) {
		t.Error("expected 'uptime' to be allowed (exact match)")
	}
}

func TestIsAllowed_GlobMatch(t *testing.T) {
	t.Parallel()

	whitelist := []string{"ls *", "cat *"}
	if !IsAllowed("ls -la", whitelist) {
		t.Error("expected 'ls -la' to be allowed (glob match)")
	}
	if !IsAllowed("cat /etc/hostname", whitelist) {
		t.Error("expected 'cat /etc/hostname' to be allowed (glob match)")
	}
}

func TestIsAllowed_GlobRejectsMultipleArgs(t *testing.T) {
	t.Parallel()

	whitelist := []string{"ls *"}
	if IsAllowed("ls -la /tmp", whitelist) {
		t.Error("expected 'ls -la /tmp' to be rejected (multiple args after prefix)")
	}
	// Tab-separated args should also be rejected.
	if IsAllowed("ls -la\t/tmp", whitelist) {
		t.Error("expected 'ls -la\\t/tmp' to be rejected (tab-separated args)")
	}
}

func TestIsAllowed_NoMatch(t *testing.T) {
	t.Parallel()

	whitelist := []string{"ls -la", "df -h"}
	if IsAllowed("rm -rf /", whitelist) {
		t.Error("expected 'rm -rf /' to be rejected (no match)")
	}
}

func TestIsAllowed_EmptyWhitelist(t *testing.T) {
	t.Parallel()

	if !IsAllowed("anything goes", nil) {
		t.Error("expected any command to be allowed with nil whitelist")
	}
	if !IsAllowed("rm -rf /", []string{}) {
		t.Error("expected any command to be allowed with empty whitelist")
	}
}

func TestBuildDockerArgs_Defaults(t *testing.T) {
	t.Parallel()

	cfg := ShellToolConfig{
		DockerImage: "ubuntu:24.04",
	}
	args := BuildDockerArgs(cfg, "echo hello")

	// Check required security flags.
	joined := strings.Join(args, " ")
	for _, flag := range []string{"--rm", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--read-only", "--network=none"} {
		if !strings.Contains(joined, flag) {
			t.Errorf("missing flag %q in args: %v", flag, args)
		}
	}

	// Check image and command.
	if args[len(args)-4] != "ubuntu:24.04" {
		t.Errorf("expected image 'ubuntu:24.04', got %q", args[len(args)-4])
	}
	if args[len(args)-3] != "bash" {
		t.Errorf("expected 'bash', got %q", args[len(args)-3])
	}
	if args[len(args)-2] != "-c" {
		t.Errorf("expected '-c', got %q", args[len(args)-2])
	}
	if args[len(args)-1] != "echo hello" {
		t.Errorf("expected 'echo hello', got %q", args[len(args)-1])
	}
}

func TestBuildDockerArgs_WithNetwork(t *testing.T) {
	t.Parallel()

	cfg := ShellToolConfig{
		DockerImage: "ubuntu:24.04",
		Network:     true,
	}
	args := BuildDockerArgs(cfg, "curl example.com")

	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--network=none") {
		t.Error("--network=none should not be present when Network is true")
	}
}

func TestBuildDockerArgs_WithLimits(t *testing.T) {
	t.Parallel()

	cfg := ShellToolConfig{
		DockerImage: "ubuntu:24.04",
		MemoryLimit: "256m",
		CPULimit:    "0.5",
	}
	args := BuildDockerArgs(cfg, "echo test")

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--memory=256m") {
		t.Errorf("missing --memory=256m in args: %v", args)
	}
	if !strings.Contains(joined, "--cpus=0.5") {
		t.Errorf("missing --cpus=0.5 in args: %v", args)
	}
}

func TestBuildDockerArgs_WithWorkspace(t *testing.T) {
	t.Parallel()

	cfg := ShellToolConfig{
		DockerImage: "ubuntu:24.04",
		Workspace:   "/home/user/project",
	}
	args := BuildDockerArgs(cfg, "ls /workspace")

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-v /home/user/project:/workspace:ro") {
		t.Errorf("missing workspace volume mount in args: %v", args)
	}
}

func TestShellTool_Name(t *testing.T) {
	t.Parallel()

	tool := NewShellTool(ShellToolConfig{DockerImage: "ubuntu:24.04"})
	if tool.Name != "shell" {
		t.Errorf("Name = %q, want %q", tool.Name, "shell")
	}
	if tool.Description == "" {
		t.Error("Description should not be empty")
	}
	if tool.Handler == nil {
		t.Error("Handler should not be nil")
	}
}

func TestShellTool_WhitelistRejectMetachars(t *testing.T) {
	t.Parallel()

	cfg := ShellToolConfig{
		DockerImage: "ubuntu:24.04",
		Whitelist:   []string{"ls *", "df -h"},
	}
	tool := NewShellTool(cfg)

	// Command with metacharacters should be rejected even if it matches whitelist.
	_, err := tool.Handler(context.Background(), map[string]any{
		"command": "ls; rm -rf /",
	})
	if err == nil {
		t.Fatal("expected error for command with metacharacters, got nil")
	}
	if !strings.Contains(err.Error(), "metacharacter") {
		t.Errorf("error = %q, want to mention 'metacharacter'", err.Error())
	}
}

func TestShellTool_MissingCommand(t *testing.T) {
	t.Parallel()

	tool := NewShellTool(ShellToolConfig{DockerImage: "ubuntu:24.04"})
	_, err := tool.Handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing command, got nil")
	}
	if !strings.Contains(err.Error(), "command") {
		t.Errorf("error = %q, want to mention 'command'", err.Error())
	}
}
