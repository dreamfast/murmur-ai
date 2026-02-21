package server

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"murmur/internal/config"

	"github.com/BurntSushi/toml"
)

// PermissionsWriter provides safe, serialized read-modify-write access to the
// permissions TOML file. All mutations follow the same pattern: read the file,
// apply the change in memory, validate the result, then atomically write it
// back (temp file + rename). A mutex serializes concurrent writes.
type PermissionsWriter struct {
	mu     sync.Mutex
	path   string
	logger *slog.Logger
}

// NewPermissionsWriter creates a new PermissionsWriter for the given file path.
// The file does not need to exist — Read will return an empty config if missing.
func NewPermissionsWriter(path string, logger *slog.Logger) *PermissionsWriter {
	return &PermissionsWriter{
		path:   path,
		logger: logger,
	}
}

// Path returns the file path this writer manages.
func (pw *PermissionsWriter) Path() string {
	return pw.path
}

// Read loads and parses the permissions file. Returns an empty config if the
// file does not exist. The caller receives a fresh copy safe to modify.
func (pw *PermissionsWriter) Read() (*config.PermissionsConfig, error) {
	cfg, err := config.LoadPermissionsConfig(pw.path)
	if err != nil {
		return nil, fmt.Errorf("PermissionsWriter.Read: %w", err)
	}
	return cfg, nil
}

// WriteUser adds or replaces a user entry, validates the result, and
// atomically writes the file. The nick is stored as-is (preserving case).
func (pw *PermissionsWriter) WriteUser(nick string, user config.UserPermissions) error {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	cfg, err := pw.Read()
	if err != nil {
		return fmt.Errorf("PermissionsWriter.WriteUser: %w", err)
	}

	if cfg.Users == nil {
		cfg.Users = make(map[string]config.UserPermissions)
	}
	cfg.Users[nick] = user

	if err := pw.validateAndWrite(cfg); err != nil {
		return fmt.Errorf("PermissionsWriter.WriteUser: %w", err)
	}

	pw.logger.Info("permissions: wrote user", "nick", nick)
	return nil
}

// RemoveUser deletes a user entry, validates the result, and atomically
// writes the file. Returns an error if the user does not exist.
func (pw *PermissionsWriter) RemoveUser(nick string) error {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	cfg, err := pw.Read()
	if err != nil {
		return fmt.Errorf("PermissionsWriter.RemoveUser: %w", err)
	}

	if cfg.Users == nil {
		return fmt.Errorf("PermissionsWriter.RemoveUser: user %q not found", nick)
	}

	// Case-insensitive lookup to find the actual key.
	found := pw.findUserKey(cfg, nick)
	if found == "" {
		return fmt.Errorf("PermissionsWriter.RemoveUser: user %q not found", nick)
	}
	delete(cfg.Users, found)

	if err := pw.validateAndWrite(cfg); err != nil {
		return fmt.Errorf("PermissionsWriter.RemoveUser: %w", err)
	}

	pw.logger.Info("permissions: removed user", "nick", nick)
	return nil
}

// WriteChannel adds or replaces a channel entry, validates the result, and
// atomically writes the file. The channel name is stored as-is.
func (pw *PermissionsWriter) WriteChannel(channel string, ch config.ChannelPermissions) error {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	cfg, err := pw.Read()
	if err != nil {
		return fmt.Errorf("PermissionsWriter.WriteChannel: %w", err)
	}

	if cfg.Channels == nil {
		cfg.Channels = make(map[string]config.ChannelPermissions)
	}
	cfg.Channels[channel] = ch

	if err := pw.validateAndWrite(cfg); err != nil {
		return fmt.Errorf("PermissionsWriter.WriteChannel: %w", err)
	}

	pw.logger.Info("permissions: wrote channel", "channel", channel)
	return nil
}

// RemoveChannel deletes a channel entry, validates the result, and atomically
// writes the file. Returns an error if the channel does not exist.
func (pw *PermissionsWriter) RemoveChannel(channel string) error {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	cfg, err := pw.Read()
	if err != nil {
		return fmt.Errorf("PermissionsWriter.RemoveChannel: %w", err)
	}

	if cfg.Channels == nil {
		return fmt.Errorf("PermissionsWriter.RemoveChannel: channel %q not found", channel)
	}

	// Case-insensitive lookup to find the actual key.
	found := pw.findChannelKey(cfg, channel)
	if found == "" {
		return fmt.Errorf("PermissionsWriter.RemoveChannel: channel %q not found", channel)
	}
	delete(cfg.Channels, found)

	if err := pw.validateAndWrite(cfg); err != nil {
		return fmt.Errorf("PermissionsWriter.RemoveChannel: %w", err)
	}

	pw.logger.Info("permissions: removed channel", "channel", channel)
	return nil
}

// validateAndWrite validates the config and atomically writes it to disk.
// Must be called with pw.mu held.
func (pw *PermissionsWriter) validateAndWrite(cfg *config.PermissionsConfig) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	// Ensure the parent directory exists.
	dir := filepath.Dir(pw.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	// Atomic write: write to a temp file in the same directory, then rename.
	// Same-directory temp file ensures rename is atomic (same filesystem).
	tmp, err := os.CreateTemp(dir, ".permissions-*.toml.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp: %w", err)
	}

	if err := os.Rename(tmpName, pw.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// findUserKey returns the actual map key for a nick (case-insensitive).
// Returns empty string if not found.
func (pw *PermissionsWriter) findUserKey(cfg *config.PermissionsConfig, nick string) string {
	for k := range cfg.Users {
		if strings.EqualFold(k, nick) {
			return k
		}
	}
	return ""
}

// findChannelKey returns the actual map key for a channel (case-insensitive).
// Returns empty string if not found.
func (pw *PermissionsWriter) findChannelKey(cfg *config.PermissionsConfig, channel string) string {
	for k := range cfg.Channels {
		if strings.EqualFold(k, channel) {
			return k
		}
	}
	return ""
}
