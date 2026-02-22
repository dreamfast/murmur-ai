// Package pathutil provides shared path validation utilities for Murmur tools.
// It centralises the common pattern of resolving symlinks and checking that a
// path falls within a set of allowed directories.
package pathutil

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidateContainment checks that path is under one of the allowed directories
// after cleaning and symlink resolution. It returns the canonical resolved path
// for use by callers, preventing TOCTOU races on the original path.
//
// The path must be absolute. If the path does not exist, the parent directory
// is resolved instead and the base name is appended.
func ValidateContainment(path string, allowedPaths []string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute: %q", path)
	}

	resolved, err := ResolveAndClean(path)
	if err != nil {
		return "", err
	}

	if !isUnderAny(resolved, allowedPaths) {
		return "", fmt.Errorf("path %q is not under any allowed directory", path)
	}

	return resolved, nil
}

// ResolveAndClean cleans the path and resolves symlinks. If the path does not
// exist, the parent directory is resolved and the base name is appended. If
// neither the path nor its parent can be resolved, the cleaned path is returned
// as a best-effort fallback.
func ResolveAndClean(path string) (string, error) {
	cleanPath := filepath.Clean(path)

	resolved, err := filepath.EvalSymlinks(cleanPath)
	if err != nil {
		// Path doesn't exist — resolve the parent directory instead.
		parentDir := filepath.Dir(cleanPath)
		resolvedParent, parentErr := filepath.EvalSymlinks(parentDir)
		if parentErr != nil {
			// Neither path nor parent exist — fall back to cleaned path.
			return cleanPath, nil
		}
		resolved = filepath.Join(resolvedParent, filepath.Base(cleanPath))
	}

	return resolved, nil
}

// IsUnderAny reports whether resolved is equal to or a subdirectory of any
// path in allowedPaths.
func IsUnderAny(resolved string, allowedPaths []string) bool {
	return isUnderAny(resolved, allowedPaths)
}

// isUnderAny is the internal implementation of IsUnderAny.
func isUnderAny(resolved string, allowedPaths []string) bool {
	for _, allowed := range allowedPaths {
		if resolved == allowed || strings.HasPrefix(resolved, allowed+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// CleanAndResolveAll cleans and resolves symlinks for a list of paths.
// Paths that cannot be resolved are cleaned but kept as-is.
func CleanAndResolveAll(paths []string) []string {
	cleaned := make([]string, len(paths))
	for i, p := range paths {
		resolved, _ := ResolveAndClean(p)
		cleaned[i] = resolved
	}
	return cleaned
}
