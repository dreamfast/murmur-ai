package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// gitCommandTimeout is the maximum time allowed for a single git command.
const gitCommandTimeout = 30 * time.Second

// allowedGitArgs defines the per-action allowlist of safe git flags.
// Only flags matching these patterns are permitted. This is an allowlist
// approach — anything not explicitly listed is rejected.
var allowedGitArgs = map[string][]string{
	"log": {
		"--oneline",
		"--stat",
		"--shortstat",
		"--name-only",
		"--name-status",
		"--graph",
		"--all",
		"--reverse",
		"--first-parent",
		"--no-merges",
		"--merges",
		"--abbrev-commit",
		"--pretty=*", // --pretty=oneline, --pretty=short, etc.
		"--format=*", // --format=%H %s etc. (validated separately for safety)
		"--since=*",  // --since=2024-01-01
		"--until=*",  // --until=2024-12-31
		"--author=*", // --author=name
		"--grep=*",   // --grep=pattern
		"-n",         // -n <count> (separate form)
		"-*[0-9]",    // -10, -20, etc.
	},
	"diff": {
		"--staged",
		"--cached",
		"--stat",
		"--shortstat",
		"--name-only",
		"--name-status",
		"--numstat",
		"--no-color",
		"--color=*",
		"--word-diff",
		"--word-diff=*",
		"--unified=*", // -U<n>
		"--diff-filter=*",
		"--ignore-space-change",
		"--ignore-all-space",
		"--ignore-blank-lines",
		"head",
		"head~*",
	},
	"show": {
		"--stat",
		"--shortstat",
		"--name-only",
		"--name-status",
		"--format=*",
		"--pretty=*",
		"--no-patch",
		"--oneline",
		"--abbrev-commit",
	},
}

// commitHashRe matches git commit hashes (short or full).
var commitHashRe = regexp.MustCompile(`^[0-9a-fA-F]{4,40}$`)

// GitOpsToolConfig holds configuration for the git_ops tool.
type GitOpsToolConfig struct {
	// AllowedRepos is a list of absolute paths to git repositories that the
	// tool is permitted to access.
	AllowedRepos []string
}

// NewGitOpsTool creates the git_ops tool for read-only git operations on
// allowed repositories. Supported actions: log, diff, status, branch, show.
func NewGitOpsTool(cfg GitOpsToolConfig) Tool {
	return Tool{
		Name:        "git_ops",
		Description: "Read-only git operations on allowed repositories. Actions: log (recent commits), diff (uncommitted changes), status (working tree status), branch (list branches), show (commit details).",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": ["log", "diff", "status", "branch", "show"],
					"description": "The git operation to perform"
				},
				"repo": {
					"type": "string",
					"description": "Absolute path to the git repository"
				},
				"args": {
					"type": "string",
					"description": "Optional extra arguments (e.g., '--oneline -10', '--staged', a commit hash). Only safe read-only flags are allowed."
				}
			},
			"required": ["action", "repo"]
		}`),
		Handler: newGitOpsHandler(cfg),
	}
}

// newGitOpsHandler returns a handler function closed over the git_ops config.
func newGitOpsHandler(cfg GitOpsToolConfig) func(ctx context.Context, args map[string]any) (string, error) {
	// Pre-clean and resolve allowed repos for consistent comparison.
	cleaned := make([]string, len(cfg.AllowedRepos))
	for i, r := range cfg.AllowedRepos {
		c := filepath.Clean(r)
		// Resolve symlinks at init time for performance.
		if resolved, err := filepath.EvalSymlinks(c); err == nil {
			c = resolved
		}
		cleaned[i] = c
	}

	return func(ctx context.Context, args map[string]any) (string, error) {
		action, err := RequireStringArg(args, "action")
		if err != nil {
			return "", err
		}

		repo, err := RequireStringArg(args, "repo")
		if err != nil {
			return "", err
		}

		extraArgs := OptionalStringArg(args, "args", "")

		// Validate extra args against the per-action allowlist.
		if extraArgs != "" {
			if err := validateGitArgs(action, splitArgs(extraArgs)); err != nil {
				return "", err
			}
		}

		// Validate repo path.
		if err := validateRepoPath(repo, cleaned); err != nil {
			return "", err
		}

		gitCtx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
		defer cancel()

		return executeGitAction(gitCtx, action, repo, extraArgs)
	}
}

// validateGitArgs checks that all provided arguments are in the per-action
// allowlist. This is an allowlist approach — only explicitly permitted flags
// are accepted. Commit hashes are always allowed for log and show actions.
func validateGitArgs(action string, args []string) error {
	allowed, ok := allowedGitArgs[action]
	if !ok {
		// Actions without an allowlist (status, branch) don't accept args.
		if len(args) > 0 {
			return fmt.Errorf("action %q does not accept extra arguments", action)
		}
		return nil
	}

	for _, arg := range args {
		if isAllowedArg(arg, allowed, action) {
			continue
		}
		return fmt.Errorf("argument %q is not allowed for git %s", arg, action)
	}
	return nil
}

// isAllowedArg checks if a single argument matches the allowlist for an action.
func isAllowedArg(arg string, allowed []string, action string) bool {
	// Commit hashes are always allowed for log and show.
	if (action == "log" || action == "show") && commitHashRe.MatchString(arg) {
		return true
	}

	// Numeric count args like -10, -20 for log.
	if action == "log" && len(arg) >= 2 && arg[0] == '-' && isDigits(arg[1:]) {
		return true
	}

	lower := strings.ToLower(arg)

	for _, pattern := range allowed {
		if strings.HasSuffix(pattern, "=*") {
			// Wildcard pattern: --flag=<anything>
			prefix := strings.TrimSuffix(pattern, "*")
			if strings.HasPrefix(lower, prefix) {
				// Safety: reject format directives containing %g (GPG triggers).
				if strings.HasPrefix(lower, "--format=") || strings.HasPrefix(lower, "--pretty=") {
					value := lower[strings.Index(lower, "=")+1:]
					if strings.Contains(value, "%g") {
						return false
					}
				}
				return true
			}
		} else if strings.HasSuffix(pattern, "*") {
			// Prefix wildcard: HEAD~*, -n etc.
			prefix := strings.TrimSuffix(pattern, "*")
			if strings.HasPrefix(lower, prefix) {
				return true
			}
		} else {
			// Exact match.
			if lower == pattern {
				return true
			}
		}
	}

	return false
}

// isDigits returns true if s consists entirely of ASCII digits.
func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

// validateRepoPath checks that the repo path is allowed and safe.
// It rejects path traversal, requires absolute paths, resolves symlinks,
// and checks against the allowed repos list.
func validateRepoPath(repo string, allowedRepos []string) error {
	// Reject path traversal.
	if strings.Contains(repo, "..") {
		return fmt.Errorf("repo path must not contain '..': %q", repo)
	}

	// Require absolute path.
	if !filepath.IsAbs(repo) {
		return fmt.Errorf("repo path must be absolute: %q", repo)
	}

	cleanRepo := filepath.Clean(repo)

	// Resolve symlinks to prevent symlink-based escapes.
	if resolved, err := filepath.EvalSymlinks(cleanRepo); err == nil {
		cleanRepo = resolved
	}

	// Check against allowed repos (exact match or subdirectory).
	// Allowed repos are pre-resolved at init time.
	for _, allowed := range allowedRepos {
		if cleanRepo == allowed || strings.HasPrefix(cleanRepo, allowed+string(filepath.Separator)) {
			return nil
		}
	}

	return fmt.Errorf("repo %q is not in the allowed repos list", repo)
}

// executeGitAction dispatches to the appropriate git command based on action.
func executeGitAction(ctx context.Context, action, repo, extraArgs string) (string, error) {
	switch action {
	case "log":
		return gitLog(ctx, repo, extraArgs)
	case "diff":
		return gitDiff(ctx, repo, extraArgs)
	case "status":
		return gitStatus(ctx, repo)
	case "branch":
		return gitBranch(ctx, repo)
	case "show":
		return gitShow(ctx, repo, extraArgs)
	default:
		return "", fmt.Errorf("unknown git action %q", action)
	}
}

// gitLog runs git log with optional extra arguments.
func gitLog(ctx context.Context, repo, extraArgs string) (string, error) {
	args := []string{"-C", repo, "log"}
	if extraArgs != "" {
		args = append(args, splitArgs(extraArgs)...)
	} else {
		args = append(args, "--oneline", "-20")
	}
	return RunCommand(ctx, "git", args...)
}

// gitDiff runs git diff with optional extra arguments.
func gitDiff(ctx context.Context, repo, extraArgs string) (string, error) {
	args := []string{"-C", repo, "diff"}
	if extraArgs != "" {
		args = append(args, splitArgs(extraArgs)...)
	}
	return RunCommand(ctx, "git", args...)
}

// gitStatus runs git status --short.
func gitStatus(ctx context.Context, repo string) (string, error) {
	return RunCommand(ctx, "git", "-C", repo, "status", "--short")
}

// gitBranch runs git branch -a.
func gitBranch(ctx context.Context, repo string) (string, error) {
	return RunCommand(ctx, "git", "-C", repo, "branch", "-a")
}

// gitShow runs git show with optional extra arguments.
func gitShow(ctx context.Context, repo, extraArgs string) (string, error) {
	args := []string{"-C", repo, "show"}
	if extraArgs != "" {
		args = append(args, splitArgs(extraArgs)...)
	} else {
		args = append(args, "--stat")
	}
	return RunCommand(ctx, "git", args...)
}

// splitArgs splits a space-separated argument string into individual arguments.
// This is a simple split — it does not handle quoting.
func splitArgs(s string) []string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil
	}
	return fields
}
