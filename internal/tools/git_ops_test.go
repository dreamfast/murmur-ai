package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTestRepo creates a temporary git repository with an initial commit.
// Returns the repo path. The caller should use t.TempDir() or similar cleanup.
func initTestRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	// Initialize repo with GPG signing disabled (host may have it enabled globally).
	run(t, dir, "git", "init")
	run(t, dir, "git", "config", "user.email", "test@test.com")
	run(t, dir, "git", "config", "user.name", "Test")
	run(t, dir, "git", "config", "commit.gpgsign", "false")

	// Create initial commit.
	testFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test Repo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "initial commit")

	return dir
}

// run executes a command in the given directory and fails the test on error.
func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}

func TestGitOps_Status(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	// Add an untracked file.
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "status",
		"repo":   repo,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "new.txt") {
		t.Errorf("expected status to contain 'new.txt', got: %s", result)
	}
	if !strings.Contains(result, "??") {
		t.Errorf("expected status to show untracked '??', got: %s", result)
	}
}

func TestGitOps_Log(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	// Add a second commit.
	if err := os.WriteFile(filepath.Join(repo, "second.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-m", "second commit")

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "log",
		"repo":   repo,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "initial commit") {
		t.Errorf("expected log to contain 'initial commit', got: %s", result)
	}
	if !strings.Contains(result, "second commit") {
		t.Errorf("expected log to contain 'second commit', got: %s", result)
	}
}

func TestGitOps_LogWithArgs(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	// Add a second commit.
	if err := os.WriteFile(filepath.Join(repo, "second.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-m", "second commit")

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "log",
		"repo":   repo,
		"args":   "--oneline -1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With -1, should only show the latest commit.
	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 log line with -1, got %d: %s", len(lines), result)
	}
	if !strings.Contains(result, "second commit") {
		t.Errorf("expected log to contain 'second commit', got: %s", result)
	}
}

func TestGitOps_Diff(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	// Modify the tracked file.
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Modified\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "diff",
		"repo":   repo,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "Modified") {
		t.Errorf("expected diff to contain 'Modified', got: %s", result)
	}
	if !strings.Contains(result, "README.md") {
		t.Errorf("expected diff to reference 'README.md', got: %s", result)
	}
}

func TestGitOps_DiffStaged(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	// Modify and stage the file.
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Staged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "add", ".")

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "diff",
		"repo":   repo,
		"args":   "--staged",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "Staged") {
		t.Errorf("expected staged diff to contain 'Staged', got: %s", result)
	}
}

func TestGitOps_Branch(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "branch",
		"repo":   repo,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should contain the default branch (master or main).
	if !strings.Contains(result, "master") && !strings.Contains(result, "main") {
		t.Errorf("expected branch output to contain 'master' or 'main', got: %s", result)
	}
}

func TestGitOps_Show(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "show",
		"repo":   repo,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "initial commit") {
		t.Errorf("expected show to contain 'initial commit', got: %s", result)
	}
	if !strings.Contains(result, "README.md") {
		t.Errorf("expected show to reference 'README.md', got: %s", result)
	}
}

func TestGitOps_ShowWithCommitHash(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	// Get the commit hash.
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimSpace(string(out))

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "show",
		"repo":   repo,
		"args":   hash,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "initial commit") {
		t.Errorf("expected show to contain 'initial commit', got: %s", result)
	}
}

func TestGitOps_RepoNotAllowed(t *testing.T) {
	t.Parallel()

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{"/allowed/repo"}})
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "status",
		"repo":   "/not/allowed/repo",
	})
	if err == nil {
		t.Fatal("expected error for repo not in allowed list")
	}
	if !strings.Contains(err.Error(), "not in the allowed repos list") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestGitOps_PathTraversal(t *testing.T) {
	t.Parallel()

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{"/allowed/repo"}})
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "status",
		"repo":   "/allowed/repo/../secret",
	})
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	if !strings.Contains(err.Error(), "..") {
		t.Errorf("expected error about '..', got: %v", err)
	}
}

func TestGitOps_InvalidAction(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "push",
		"repo":   repo,
	})
	if err == nil {
		t.Fatal("expected error for invalid action")
	}
	if !strings.Contains(err.Error(), "unknown git action") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestGitOps_MissingAction(t *testing.T) {
	t.Parallel()

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{"/some/repo"}})
	_, err := tool.Handler(context.Background(), map[string]any{
		"repo": "/some/repo",
	})
	if err == nil {
		t.Fatal("expected error for missing action")
	}
	if !strings.Contains(err.Error(), "missing required argument") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestGitOps_MissingRepo(t *testing.T) {
	t.Parallel()

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{"/some/repo"}})
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "status",
	})
	if err == nil {
		t.Fatal("expected error for missing repo")
	}
	if !strings.Contains(err.Error(), "missing required argument") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestGitOps_SubdirectoryAllowed(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	// Create a subdirectory inside the repo.
	subdir := filepath.Join(repo, "subdir")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	// Allow the parent repo — subdirectory should pass path validation.
	// git -C <subdir> status works because git walks up to find .git.
	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "status",
		"repo":   subdir,
	})
	if err != nil && strings.Contains(err.Error(), "not in the allowed repos list") {
		t.Errorf("subdirectory of allowed repo should pass path validation, got: %v", err)
	}
}

// --- Security tests: blocked flags (allowlist enforcement) ---

func TestGitOps_BlockedFlagOutput(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "diff",
		"repo":   repo,
		"args":   "--output=/tmp/evil.txt",
	})
	if err == nil {
		t.Fatal("expected error for disallowed --output flag")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestGitOps_BlockedFlagNoIndex(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "diff",
		"repo":   repo,
		"args":   "--no-index /etc/passwd /etc/shadow",
	})
	if err == nil {
		t.Fatal("expected error for disallowed --no-index flag")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestGitOps_BlockedFlagWorkTree(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "log",
		"repo":   repo,
		"args":   "--work-tree=/etc",
	})
	if err == nil {
		t.Fatal("expected error for disallowed --work-tree flag")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestGitOps_BlockedFlagExec(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "log",
		"repo":   repo,
		"args":   "--exec rm",
	})
	if err == nil {
		t.Fatal("expected error for disallowed --exec flag")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestGitOps_BlockedFlagGitDir(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "log",
		"repo":   repo,
		"args":   "--git-dir=/etc",
	})
	if err == nil {
		t.Fatal("expected error for disallowed --git-dir flag")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestGitOps_BlockedFlagExtDiff(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "diff",
		"repo":   repo,
		"args":   "--ext-diff",
	})
	if err == nil {
		t.Fatal("expected error for disallowed --ext-diff flag")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestGitOps_BlockedFormatGPG(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "log",
		"repo":   repo,
		"args":   "--format=%GK",
	})
	if err == nil {
		t.Fatal("expected error for GPG format directive")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestGitOps_StatusRejectsArgs(t *testing.T) {
	t.Parallel()

	repo := initTestRepo(t)

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{repo}})
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "status",
		"repo":   repo,
		"args":   "--porcelain",
	})
	if err == nil {
		t.Fatal("expected error: status does not accept extra args")
	}
	if !strings.Contains(err.Error(), "does not accept extra arguments") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestGitOps_SymlinkEscape(t *testing.T) {
	t.Parallel()

	// Create an allowed directory and a target directory outside it.
	allowedDir := t.TempDir()
	outsideDir := t.TempDir()

	// Create a symlink inside the allowed directory pointing outside.
	symlink := filepath.Join(allowedDir, "escape")
	if err := os.Symlink(outsideDir, symlink); err != nil {
		t.Fatal(err)
	}

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{allowedDir}})
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "status",
		"repo":   symlink,
	})
	if err == nil {
		t.Fatal("expected error for symlink escaping allowed repos")
	}
	if !strings.Contains(err.Error(), "not in the allowed repos list") {
		t.Errorf("expected 'not in the allowed repos list' error, got: %v", err)
	}
}

func TestGitOps_RelativePathRejected(t *testing.T) {
	t.Parallel()

	tool := NewGitOpsTool(GitOpsToolConfig{AllowedRepos: []string{"/allowed/repo"}})
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "status",
		"repo":   "relative/path",
	})
	if err == nil {
		t.Fatal("expected error for relative path")
	}
	if !strings.Contains(err.Error(), "must be absolute") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// --- Unit tests for validation functions ---

func TestValidateGitArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		action  string
		args    []string
		wantErr bool
	}{
		{name: "log safe args", action: "log", args: []string{"--oneline", "-10"}, wantErr: false},
		{name: "log with graph", action: "log", args: []string{"--graph", "--all"}, wantErr: false},
		{name: "log with author", action: "log", args: []string{"--author=john"}, wantErr: false},
		{name: "log commit hash", action: "log", args: []string{"abc123f"}, wantErr: false},
		{name: "log format safe", action: "log", args: []string{"--format=%H %s"}, wantErr: false},
		{name: "log format gpg blocked", action: "log", args: []string{"--format=%GK"}, wantErr: true},
		{name: "log pretty gpg blocked", action: "log", args: []string{"--pretty=%G"}, wantErr: true},
		{name: "log exec blocked", action: "log", args: []string{"--exec"}, wantErr: true},
		{name: "log work-tree blocked", action: "log", args: []string{"--work-tree=/etc"}, wantErr: true},
		{name: "log git-dir blocked", action: "log", args: []string{"--git-dir=/etc"}, wantErr: true},
		{name: "log ext-diff blocked", action: "log", args: []string{"--ext-diff"}, wantErr: true},
		{name: "diff staged", action: "diff", args: []string{"--staged"}, wantErr: false},
		{name: "diff cached", action: "diff", args: []string{"--cached"}, wantErr: false},
		{name: "diff stat", action: "diff", args: []string{"--stat"}, wantErr: false},
		{name: "diff HEAD", action: "diff", args: []string{"HEAD"}, wantErr: false},
		{name: "diff HEAD~1", action: "diff", args: []string{"HEAD~1"}, wantErr: false},
		{name: "diff output blocked", action: "diff", args: []string{"--output=/tmp/x"}, wantErr: true},
		{name: "diff no-index blocked", action: "diff", args: []string{"--no-index"}, wantErr: true},
		{name: "diff ext-diff blocked", action: "diff", args: []string{"--ext-diff"}, wantErr: true},
		{name: "show stat", action: "show", args: []string{"--stat"}, wantErr: false},
		{name: "show commit hash", action: "show", args: []string{"abc123f"}, wantErr: false},
		{name: "show no-patch", action: "show", args: []string{"--no-patch"}, wantErr: false},
		{name: "status no args allowed", action: "status", args: []string{}, wantErr: false},
		{name: "status rejects args", action: "status", args: []string{"--porcelain"}, wantErr: true},
		{name: "branch rejects args", action: "branch", args: []string{"--delete"}, wantErr: true},
		{name: "case insensitive", action: "diff", args: []string{"--STAGED"}, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateGitArgs(tt.action, tt.args)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateRepoPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		repo     string
		allowed  []string
		wantErr  bool
		errMatch string
	}{
		{
			name:    "exact match",
			repo:    "/home/user/project",
			allowed: []string{"/home/user/project"},
			wantErr: false,
		},
		{
			name:    "subdirectory match",
			repo:    "/home/user/project/src",
			allowed: []string{"/home/user/project"},
			wantErr: false,
		},
		{
			name:     "not allowed",
			repo:     "/home/user/secret",
			allowed:  []string{"/home/user/project"},
			wantErr:  true,
			errMatch: "not in the allowed repos list",
		},
		{
			name:     "path traversal",
			repo:     "/home/user/project/../secret",
			allowed:  []string{"/home/user/project"},
			wantErr:  true,
			errMatch: "..",
		},
		{
			name:     "prefix attack without separator",
			repo:     "/home/user/project-evil",
			allowed:  []string{"/home/user/project"},
			wantErr:  true,
			errMatch: "not in the allowed repos list",
		},
		{
			name:    "multiple allowed repos",
			repo:    "/opt/repos/b",
			allowed: []string{"/opt/repos/a", "/opt/repos/b"},
			wantErr: false,
		},
		{
			name:     "relative path rejected",
			repo:     "relative/path",
			allowed:  []string{"/home/user/project"},
			wantErr:  true,
			errMatch: "must be absolute",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Clean allowed repos as the handler does.
			cleaned := make([]string, len(tt.allowed))
			for i, r := range tt.allowed {
				cleaned[i] = filepath.Clean(r)
			}
			err := validateRepoPath(tt.repo, cleaned)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errMatch != "" && !strings.Contains(err.Error(), tt.errMatch) {
					t.Errorf("expected error containing %q, got: %v", tt.errMatch, err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestIsAllowedArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		arg     string
		allowed []string
		action  string
		want    bool
	}{
		{name: "exact match", arg: "--oneline", allowed: []string{"--oneline"}, action: "log", want: true},
		{name: "wildcard value", arg: "--since=2024-01-01", allowed: []string{"--since=*"}, action: "log", want: true},
		{name: "prefix wildcard", arg: "HEAD~3", allowed: []string{"head~*"}, action: "diff", want: true},
		{name: "commit hash", arg: "abc123f", allowed: []string{}, action: "log", want: true},
		{name: "numeric count", arg: "-20", allowed: []string{}, action: "log", want: true},
		{name: "not in list", arg: "--ext-diff", allowed: []string{"--oneline"}, action: "log", want: false},
		{name: "gpg format blocked", arg: "--format=%GK", allowed: []string{"--format=*"}, action: "log", want: false},
		{name: "safe format allowed", arg: "--format=%H", allowed: []string{"--format=*"}, action: "log", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isAllowedArg(tt.arg, tt.allowed, tt.action)
			if got != tt.want {
				t.Errorf("isAllowedArg(%q, %v, %q) = %v, want %v", tt.arg, tt.allowed, tt.action, got, tt.want)
			}
		})
	}
}
