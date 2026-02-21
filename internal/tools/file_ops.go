package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// errMaxSearchResults is a sentinel error used to stop WalkDir when the
// maximum number of search results has been reached.
var errMaxSearchResults = errors.New("max search results reached")

// fileOpsMaxSearchDepth is the maximum directory depth for recursive search.
const fileOpsMaxSearchDepth = 5

// fileOpsMaxSearchResults is the maximum number of search matches returned.
const fileOpsMaxSearchResults = 50

// fileOpsDefaultListLimit is the default number of entries returned by list.
const fileOpsDefaultListLimit = 50

// fileOpsBinaryCheckSize is the number of bytes read to detect binary files.
const fileOpsBinaryCheckSize = 512

// FileOpsToolConfig holds configuration for the file_ops tool.
type FileOpsToolConfig struct {
	// AllowedPaths is a list of directories the tool is permitted to access.
	AllowedPaths []string
}

// NewFileOpsTool creates the file_ops tool for read-only file operations on
// allowed directories. Supported actions: read, list, search, stat.
func NewFileOpsTool(cfg FileOpsToolConfig) Tool {
	return Tool{
		Name:        "file_ops",
		Description: "Read-only file operations on allowed directories. Actions: read (file contents), list (directory listing), search (recursive text search), stat (file metadata).",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {
					"type": "string",
					"enum": ["read", "list", "search", "stat"],
					"description": "The file operation to perform"
				},
				"path": {
					"type": "string",
					"description": "Absolute path to the file or directory"
				},
				"query": {
					"type": "string",
					"description": "Search query string (required for search action)"
				},
				"limit": {
					"type": "integer",
					"description": "Maximum number of entries for list (default 50)"
				}
			},
			"required": ["action", "path"]
		}`),
		Handler: newFileOpsHandler(cfg),
	}
}

// newFileOpsHandler returns a handler function closed over the file_ops config.
func newFileOpsHandler(cfg FileOpsToolConfig) func(ctx context.Context, args map[string]any) (string, error) {
	// Pre-clean and resolve allowed paths for consistent comparison.
	cleaned := make([]string, len(cfg.AllowedPaths))
	for i, p := range cfg.AllowedPaths {
		c := filepath.Clean(p)
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

		path, err := RequireStringArg(args, "path")
		if err != nil {
			return "", err
		}

		// Validate and resolve path to its canonical form.
		resolvedPath, err := resolveAndValidatePath(path, cleaned)
		if err != nil {
			return "", err
		}

		switch action {
		case "read":
			return fileOpsRead(ctx, resolvedPath)
		case "list":
			limit := optionalIntArg(args, "limit", fileOpsDefaultListLimit)
			return fileOpsList(ctx, resolvedPath, limit)
		case "search":
			query := OptionalStringArg(args, "query", "")
			if query == "" {
				return "", fmt.Errorf("query is required for search action")
			}
			return fileOpsSearch(ctx, resolvedPath, query, cleaned)
		case "stat":
			return fileOpsStat(ctx, resolvedPath)
		default:
			return "", fmt.Errorf("unknown action %q, expected one of: read, list, search, stat", action)
		}
	}
}

// resolveAndValidatePath checks that the given path is under one of the allowed
// directories after cleaning and symlink resolution. Returns the canonical
// resolved path for use by callers, preventing TOCTOU races on the original path.
func resolveAndValidatePath(path string, allowedPaths []string) (string, error) {
	// Reject relative paths.
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute: %q", path)
	}

	// Clean the path.
	cleanPath := filepath.Clean(path)

	// Resolve symlinks to get the real path.
	resolvedPath, err := filepath.EvalSymlinks(cleanPath)
	if err != nil {
		// If the path doesn't exist yet, resolve the parent.
		parentDir := filepath.Dir(cleanPath)
		resolvedParent, parentErr := filepath.EvalSymlinks(parentDir)
		if parentErr != nil {
			return "", fmt.Errorf("path not accessible: %w", err)
		}
		resolvedPath = filepath.Join(resolvedParent, filepath.Base(cleanPath))
	}

	// Check if the resolved path is under any allowed directory.
	for _, allowed := range allowedPaths {
		if resolvedPath == allowed || strings.HasPrefix(resolvedPath, allowed+string(filepath.Separator)) {
			return resolvedPath, nil
		}
	}

	return "", fmt.Errorf("path %q is not under any allowed directory", path)
}

// isBinaryFile checks if a file appears to be binary by examining its first
// 512 bytes using http.DetectContentType.
func isBinaryFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, fileOpsBinaryCheckSize)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return false, err
	}
	if n == 0 {
		return false, nil // empty file is not binary
	}

	contentType := http.DetectContentType(buf[:n])
	// Text types start with "text/" or are "application/json", etc.
	if strings.HasPrefix(contentType, "text/") {
		return false, nil
	}
	if contentType == "application/json" || contentType == "application/xml" {
		return false, nil
	}
	return true, nil
}

// fileOpsRead reads a text file and returns its contents, truncated to MaxOutputBytes.
func fileOpsRead(_ context.Context, path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("file_ops read: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("file_ops read: %q is a directory, use list action", path)
	}

	binary, err := isBinaryFile(path)
	if err != nil {
		return "", fmt.Errorf("file_ops read: check binary: %w", err)
	}
	if binary {
		return "", fmt.Errorf("file_ops read: %q appears to be a binary file", path)
	}

	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("file_ops read: %w", err)
	}
	defer f.Close()

	// Read up to MaxOutputBytes.
	data := make([]byte, MaxOutputBytes+1)
	n, err := io.ReadFull(f, data)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", fmt.Errorf("file_ops read: %w", err)
	}

	if n > MaxOutputBytes {
		return string(data[:MaxOutputBytes]) + truncationNotice, nil
	}
	return string(data[:n]), nil
}

// fileOpsList lists directory contents with file sizes and modification times.
func fileOpsList(_ context.Context, path string, limit int) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("file_ops list: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("file_ops list: %q is not a directory, use read or stat action", path)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("file_ops list: %w", err)
	}

	if limit <= 0 {
		limit = fileOpsDefaultListLimit
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Directory: %s (%d entries)\n", path, len(entries))

	count := 0
	for _, entry := range entries {
		if count >= limit {
			fmt.Fprintf(&b, "... and %d more entries\n", len(entries)-count)
			break
		}

		entryInfo, err := entry.Info()
		if err != nil {
			fmt.Fprintf(&b, "  %s (error: %v)\n", entry.Name(), err)
			count++
			continue
		}

		typeStr := "file"
		if entry.IsDir() {
			typeStr = "dir"
		} else if entryInfo.Mode()&fs.ModeSymlink != 0 {
			typeStr = "link"
		}

		fmt.Fprintf(&b, "  %-4s %10d  %s  %s\n",
			typeStr,
			entryInfo.Size(),
			entryInfo.ModTime().Format(time.RFC3339),
			entry.Name(),
		)
		count++
	}

	return TruncateOutput(b.String()), nil
}

// fileOpsSearch performs a recursive text search for query in files under path.
// Each file encountered during the walk is validated against allowedPaths to
// prevent symlink-based escapes. The search uses bufio.Scanner with a default
// 64KB line buffer; lines exceeding this limit are silently skipped.
func fileOpsSearch(ctx context.Context, root, query string, allowedPaths []string) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("file_ops search: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("file_ops search: %q is not a directory", root)
	}

	var results []string
	var skipped int
	lowerQuery := strings.ToLower(query)

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			skipped++
			return nil // skip inaccessible entries but count them
		}

		// Check context cancellation.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Enforce depth limit.
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		depth := strings.Count(rel, string(filepath.Separator))
		if depth > fileOpsMaxSearchDepth {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		// Skip directories.
		if d.IsDir() {
			return nil
		}

		// Validate each file's resolved path against the allowlist to
		// prevent symlink files inside allowed dirs from escaping.
		resolvedFile, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			skipped++
			return nil
		}
		allowed := false
		for _, ap := range allowedPaths {
			if resolvedFile == ap || strings.HasPrefix(resolvedFile, ap+string(filepath.Separator)) {
				allowed = true
				break
			}
		}
		if !allowed {
			skipped++
			return nil
		}

		// Skip binary files.
		binary, bErr := isBinaryFile(resolvedFile)
		if bErr != nil {
			skipped++
			return nil
		}
		if binary {
			return nil
		}

		// Search file contents using the resolved path.
		matches, searchErr := searchFileForQuery(resolvedFile, lowerQuery)
		if searchErr != nil {
			skipped++
			return nil
		}

		for _, m := range matches {
			results = append(results, m)
			if len(results) >= fileOpsMaxSearchResults {
				return errMaxSearchResults
			}
		}

		return nil
	})

	// Check for context cancellation first.
	if ctx.Err() != nil {
		return "", fmt.Errorf("file_ops search: %w", ctx.Err())
	}

	// Ignore the sentinel error for max results.
	if err != nil && !errors.Is(err, errMaxSearchResults) {
		return "", fmt.Errorf("file_ops search: %w", err)
	}

	if len(results) == 0 {
		return fmt.Sprintf("No matches found for %q in %s", query, root), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d matches for %q:\n", len(results), query)
	for _, r := range results {
		fmt.Fprintln(&b, r)
	}
	if skipped > 0 {
		fmt.Fprintf(&b, "\n(%d files/directories skipped due to permissions, symlinks, or read errors)\n", skipped)
	}

	return TruncateOutput(b.String()), nil
}

// searchFileForQuery searches a single file for lines containing the query
// (case-insensitive) and returns formatted match strings.
func searchFileForQuery(path, lowerQuery string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var matches []string
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if strings.Contains(strings.ToLower(line), lowerQuery) {
			// Truncate long lines.
			display := line
			if len(display) > 200 {
				display = display[:200] + "..."
			}
			matches = append(matches, fmt.Sprintf("  %s:%d: %s", path, lineNum, display))
		}
	}

	return matches, scanner.Err()
}

// fileOpsStat returns metadata about a file or directory.
// Note: the path is already resolved via EvalSymlinks, so symlinks are
// transparent. We use os.Stat (not Lstat) for consistency with the resolved path.
func fileOpsStat(_ context.Context, path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("file_ops stat: %w", err)
	}

	typeStr := "file"
	if info.IsDir() {
		typeStr = "directory"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Path: %s\n", path)
	fmt.Fprintf(&b, "Type: %s\n", typeStr)
	fmt.Fprintf(&b, "Size: %d bytes\n", info.Size())
	fmt.Fprintf(&b, "Permissions: %s\n", info.Mode().Perm())
	fmt.Fprintf(&b, "Modified: %s\n", info.ModTime().Format(time.RFC3339))

	return b.String(), nil
}
