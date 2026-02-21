package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFileOps_Read(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(filePath, []byte("hello world\nsecond line\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewFileOpsTool(FileOpsToolConfig{
		AllowedPaths: []string{dir},
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "read",
		"path":   filePath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "hello world") {
		t.Errorf("expected file content in result, got: %s", result)
	}
	if !strings.Contains(result, "second line") {
		t.Errorf("expected second line in result, got: %s", result)
	}
}

func TestFileOps_ReadBinary(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "binary.dat")
	// Write binary data (null bytes make it non-text).
	data := make([]byte, 256)
	for i := range data {
		data[i] = byte(i)
	}
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewFileOpsTool(FileOpsToolConfig{
		AllowedPaths: []string{dir},
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "read",
		"path":   filePath,
	})
	if err == nil {
		t.Fatal("expected error for binary file")
	}
	if !strings.Contains(err.Error(), "binary") {
		t.Errorf("expected binary error, got: %v", err)
	}
}

func TestFileOps_ReadDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	tool := NewFileOpsTool(FileOpsToolConfig{
		AllowedPaths: []string{dir},
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "read",
		"path":   dir,
	})
	if err == nil {
		t.Fatal("expected error for directory")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("expected directory error, got: %v", err)
	}
}

func TestFileOps_List(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create some files and a subdirectory.
	if err := os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("content1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file2.txt"), []byte("content2"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}

	tool := NewFileOpsTool(FileOpsToolConfig{
		AllowedPaths: []string{dir},
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "list",
		"path":   dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "file1.txt") {
		t.Errorf("expected file1.txt in result, got: %s", result)
	}
	if !strings.Contains(result, "file2.txt") {
		t.Errorf("expected file2.txt in result, got: %s", result)
	}
	if !strings.Contains(result, "subdir") {
		t.Errorf("expected subdir in result, got: %s", result)
	}
	if !strings.Contains(result, "3 entries") {
		t.Errorf("expected '3 entries' in result, got: %s", result)
	}
}

func TestFileOps_ListLimit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create more files than the limit.
	for i := 0; i < 5; i++ {
		name := filepath.Join(dir, strings.Repeat("a", i+1)+".txt")
		if err := os.WriteFile(name, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	tool := NewFileOpsTool(FileOpsToolConfig{
		AllowedPaths: []string{dir},
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "list",
		"path":   dir,
		"limit":  float64(2),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "more entries") {
		t.Errorf("expected truncation notice in result, got: %s", result)
	}
}

func TestFileOps_Search(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create files with searchable content.
	if err := os.WriteFile(filepath.Join(dir, "match.txt"), []byte("hello world\nfoo bar\nhello again\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nomatch.txt"), []byte("nothing here\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Create a subdirectory with a matching file.
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "deep.txt"), []byte("hello deep\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewFileOpsTool(FileOpsToolConfig{
		AllowedPaths: []string{dir},
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "search",
		"path":   dir,
		"query":  "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "match.txt:1") {
		t.Errorf("expected match.txt:1 in result, got: %s", result)
	}
	if !strings.Contains(result, "match.txt:3") {
		t.Errorf("expected match.txt:3 in result, got: %s", result)
	}
	if !strings.Contains(result, "deep.txt:1") {
		t.Errorf("expected deep.txt:1 in result, got: %s", result)
	}
	// Should report 3 matches.
	if !strings.Contains(result, "3 matches") {
		t.Errorf("expected '3 matches' in result, got: %s", result)
	}
}

func TestFileOps_SearchCaseInsensitive(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("Hello World\nHELLO AGAIN\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewFileOpsTool(FileOpsToolConfig{
		AllowedPaths: []string{dir},
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "search",
		"path":   dir,
		"query":  "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "2 matches") {
		t.Errorf("expected 2 matches (case-insensitive), got: %s", result)
	}
}

func TestFileOps_SearchNoQuery(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	tool := NewFileOpsTool(FileOpsToolConfig{
		AllowedPaths: []string{dir},
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "search",
		"path":   dir,
	})
	if err == nil {
		t.Fatal("expected error for missing query")
	}
	if !strings.Contains(err.Error(), "query is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFileOps_Stat(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewFileOpsTool(FileOpsToolConfig{
		AllowedPaths: []string{dir},
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "stat",
		"path":   filePath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "Type: file") {
		t.Errorf("expected 'Type: file' in result, got: %s", result)
	}
	if !strings.Contains(result, "Size: 5 bytes") {
		t.Errorf("expected 'Size: 5 bytes' in result, got: %s", result)
	}
	if !strings.Contains(result, "Permissions:") {
		t.Errorf("expected permissions in result, got: %s", result)
	}
}

func TestFileOps_StatDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	tool := NewFileOpsTool(FileOpsToolConfig{
		AllowedPaths: []string{dir},
	})

	result, err := tool.Handler(context.Background(), map[string]any{
		"action": "stat",
		"path":   dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "Type: directory") {
		t.Errorf("expected 'Type: directory' in result, got: %s", result)
	}
}

func TestFileOps_PathNotAllowed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	otherDir := t.TempDir()
	filePath := filepath.Join(otherDir, "secret.txt")
	if err := os.WriteFile(filePath, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewFileOpsTool(FileOpsToolConfig{
		AllowedPaths: []string{dir},
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "read",
		"path":   filePath,
	})
	if err == nil {
		t.Fatal("expected error for path outside allowed dirs")
	}
	if !strings.Contains(err.Error(), "not under any allowed directory") {
		t.Errorf("expected 'not under any allowed directory' error, got: %v", err)
	}
}

func TestFileOps_PathTraversal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	tool := NewFileOpsTool(FileOpsToolConfig{
		AllowedPaths: []string{dir},
	})

	// Try to escape using ".." — after Clean the path resolves to a location
	// outside the allowed dir, which is caught by the prefix check.
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "read",
		"path":   filepath.Join(dir, "..", "etc", "passwd"),
	})
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestFileOps_Symlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink test not supported on Windows")
	}
	t.Parallel()

	allowedDir := t.TempDir()
	secretDir := t.TempDir()
	secretFile := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("secret data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a symlink inside the allowed dir that points outside.
	symlinkPath := filepath.Join(allowedDir, "escape")
	if err := os.Symlink(secretDir, symlinkPath); err != nil {
		t.Fatal(err)
	}

	tool := NewFileOpsTool(FileOpsToolConfig{
		AllowedPaths: []string{allowedDir},
	})

	// Try to read through the symlink.
	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "read",
		"path":   filepath.Join(symlinkPath, "secret.txt"),
	})
	if err == nil {
		t.Fatal("expected error for symlink escaping allowed dir")
	}
	if !strings.Contains(err.Error(), "not under any allowed directory") {
		t.Errorf("expected 'not under any allowed directory' error, got: %v", err)
	}
}

func TestFileOps_InvalidAction(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	tool := NewFileOpsTool(FileOpsToolConfig{
		AllowedPaths: []string{dir},
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "delete",
		"path":   dir,
	})
	if err == nil {
		t.Fatal("expected error for invalid action")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("expected 'unknown action' error, got: %v", err)
	}
}

func TestFileOps_RelativePath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	tool := NewFileOpsTool(FileOpsToolConfig{
		AllowedPaths: []string{dir},
	})

	_, err := tool.Handler(context.Background(), map[string]any{
		"action": "read",
		"path":   "relative/path.txt",
	})
	if err == nil {
		t.Fatal("expected error for relative path")
	}
	if !strings.Contains(err.Error(), "must be absolute") {
		t.Errorf("expected 'must be absolute' error, got: %v", err)
	}
}

func TestResolveAndValidatePath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(filePath, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "valid file", path: filePath, wantErr: false},
		{name: "valid dir", path: dir, wantErr: false},
		{name: "relative path", path: "foo/bar", wantErr: true},
		{name: "outside allowed", path: "/etc/passwd", wantErr: true},
	}

	// Resolve dir for comparison (temp dirs may be symlinks on some OS).
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := resolveAndValidatePath(tt.path, []string{resolvedDir})
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestIsBinaryFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Text file.
	textPath := filepath.Join(dir, "text.txt")
	if err := os.WriteFile(textPath, []byte("hello world\nthis is text\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Binary file.
	binPath := filepath.Join(dir, "binary.dat")
	binData := make([]byte, 256)
	for i := range binData {
		binData[i] = byte(i)
	}
	if err := os.WriteFile(binPath, binData, 0644); err != nil {
		t.Fatal(err)
	}

	// Empty file.
	emptyPath := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(emptyPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		path       string
		wantBinary bool
	}{
		{name: "text file", path: textPath, wantBinary: false},
		{name: "binary file", path: binPath, wantBinary: true},
		{name: "empty file", path: emptyPath, wantBinary: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := isBinaryFile(tt.path)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.wantBinary {
				t.Errorf("isBinaryFile(%q) = %v, want %v", tt.path, got, tt.wantBinary)
			}
		})
	}
}
