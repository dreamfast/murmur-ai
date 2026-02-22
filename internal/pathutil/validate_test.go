package pathutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateContainment_HappyPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	subdir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(subdir, "test.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ValidateContainment(file, []string{dir})
	if err != nil {
		t.Fatalf("ValidateContainment() error: %v", err)
	}

	// Resolve file to handle any symlinks in the temp dir path.
	resolvedFile, _ := filepath.EvalSymlinks(file)

	if got != resolvedFile {
		t.Errorf("ValidateContainment() = %q, want %q", got, resolvedFile)
	}
}

func TestValidateContainment_Traversal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Try to escape via ..
	escapePath := filepath.Join(dir, "..", "etc", "passwd")

	_, err := ValidateContainment(escapePath, []string{dir})
	if err == nil {
		t.Error("ValidateContainment() should reject path traversal")
	}
}

func TestValidateContainment_RelativePath(t *testing.T) {
	t.Parallel()

	_, err := ValidateContainment("relative/path", []string{"/tmp"})
	if err == nil {
		t.Error("ValidateContainment() should reject relative paths")
	}
}

func TestValidateContainment_NotAllowed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	otherDir := t.TempDir()

	file := filepath.Join(otherDir, "test.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ValidateContainment(file, []string{dir})
	if err == nil {
		t.Error("ValidateContainment() should reject paths outside allowed dirs")
	}
}

func TestValidateContainment_MultipleAllowed(t *testing.T) {
	t.Parallel()

	dir1 := t.TempDir()
	dir2 := t.TempDir()

	file := filepath.Join(dir2, "test.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ValidateContainment(file, []string{dir1, dir2})
	if err != nil {
		t.Fatalf("ValidateContainment() should accept path under second allowed dir: %v", err)
	}
}

func TestValidateContainment_ExactMatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// The directory itself should be allowed.
	_, err := ValidateContainment(dir, []string{dir})
	if err != nil {
		t.Fatalf("ValidateContainment() should accept exact match: %v", err)
	}
}

func TestValidateContainment_NonexistentFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	nonexistent := filepath.Join(dir, "does-not-exist.txt")

	got, err := ValidateContainment(nonexistent, []string{dir})
	if err != nil {
		t.Fatalf("ValidateContainment() should accept nonexistent file in allowed dir: %v", err)
	}

	resolvedDir, _ := filepath.EvalSymlinks(dir)
	expected := filepath.Join(resolvedDir, "does-not-exist.txt")
	if got != expected {
		t.Errorf("ValidateContainment() = %q, want %q", got, expected)
	}
}

func TestValidateContainment_Symlink(t *testing.T) {
	t.Parallel()

	allowedDir := t.TempDir()
	outsideDir := t.TempDir()

	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a symlink inside allowed dir pointing outside.
	symlink := filepath.Join(allowedDir, "escape")
	if err := os.Symlink(outsideFile, symlink); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	_, err := ValidateContainment(symlink, []string{allowedDir})
	if err == nil {
		t.Error("ValidateContainment() should reject symlinks pointing outside allowed dirs")
	}
}

func TestResolveAndClean(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveAndClean(file)
	if err != nil {
		t.Fatalf("ResolveAndClean() error: %v", err)
	}

	resolvedFile, _ := filepath.EvalSymlinks(file)
	if got != resolvedFile {
		t.Errorf("ResolveAndClean() = %q, want %q", got, resolvedFile)
	}
}

func TestCleanAndResolveAll(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	paths := []string{dir, "/nonexistent/path"}

	result := CleanAndResolveAll(paths)
	if len(result) != 2 {
		t.Fatalf("CleanAndResolveAll() returned %d paths, want 2", len(result))
	}

	resolvedDir, _ := filepath.EvalSymlinks(dir)
	if result[0] != resolvedDir {
		t.Errorf("CleanAndResolveAll()[0] = %q, want %q", result[0], resolvedDir)
	}

	// Nonexistent path should be cleaned but not resolved.
	if result[1] != "/nonexistent/path" {
		t.Errorf("CleanAndResolveAll()[1] = %q, want %q", result[1], "/nonexistent/path")
	}
}

func TestIsUnderAny(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resolved string
		allowed  []string
		want     bool
	}{
		{"exact match", "/home/user", []string{"/home/user"}, true},
		{"subdirectory", "/home/user/docs/file.txt", []string{"/home/user"}, true},
		{"not under", "/etc/passwd", []string{"/home/user"}, false},
		{"prefix attack", "/home/username", []string{"/home/user"}, false},
		{"empty allowed", "/home/user", nil, false},
		{"multiple allowed", "/opt/data/file", []string{"/home", "/opt/data"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsUnderAny(tt.resolved, tt.allowed); got != tt.want {
				t.Errorf("IsUnderAny(%q, %v) = %v, want %v", tt.resolved, tt.allowed, got, tt.want)
			}
		})
	}
}
