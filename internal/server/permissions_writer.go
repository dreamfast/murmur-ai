package server

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"murmur/internal/config"
	"murmur/internal/db"
)

// PermissionsStore provides DB-backed read-modify operations for user and
// channel permissions. After each mutation it refreshes the in-memory
// PermissionManager cache by reloading from the database. A mutex serializes
// concurrent writes to prevent interleaving.
//
// This replaces the former PermissionsWriter which used TOML file-based
// read-modify-write with atomic file renames.
type PermissionsStore struct {
	mu     sync.Mutex
	db     *db.DB
	pm     *PermissionManager
	logger *slog.Logger
}

// NewPermissionsStore creates a new PermissionsStore backed by the given database.
// The PermissionManager is refreshed after each mutation.
func NewPermissionsStore(database *db.DB, pm *PermissionManager, logger *slog.Logger) *PermissionsStore {
	return &PermissionsStore{
		db:     database,
		pm:     pm,
		logger: logger,
	}
}

// Read loads the current permissions from the database and returns a
// PermissionsConfig. The caller receives a fresh copy safe to inspect.
func (ps *PermissionsStore) Read() (*config.PermissionsConfig, error) {
	cfg, err := config.LoadPermissionsFromDB(ps.db)
	if err != nil {
		return nil, fmt.Errorf("PermissionsStore.Read: %w", err)
	}
	return cfg, nil
}

// WriteUser adds or updates a user entry in the database and refreshes the
// in-memory permission cache. The nick is stored as-is (preserving case).
func (ps *PermissionsStore) WriteUser(nick string, user config.UserPermissions) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	row := userPermissionsToRow(nick, user)

	// Try update first; if not found, create.
	err := ps.db.UpdateUser(row)
	if err != nil {
		if errors.Is(err, db.ErrUserNotFound) {
			if err := ps.db.CreateUser(row); err != nil {
				return fmt.Errorf("PermissionsStore.WriteUser: %w", err)
			}
		} else {
			return fmt.Errorf("PermissionsStore.WriteUser: %w", err)
		}
	}

	if err := ps.refreshCache(); err != nil {
		return fmt.Errorf("PermissionsStore.WriteUser: %w", err)
	}

	ps.logger.Info("permissions: wrote user", "nick", nick)
	return nil
}

// RemoveUser deletes a user entry from the database and refreshes the
// in-memory permission cache. Returns an error if the user does not exist.
func (ps *PermissionsStore) RemoveUser(nick string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if err := ps.db.DeleteUser(nick); err != nil {
		return fmt.Errorf("PermissionsStore.RemoveUser: %w", err)
	}

	if err := ps.refreshCache(); err != nil {
		return fmt.Errorf("PermissionsStore.RemoveUser: %w", err)
	}

	ps.logger.Info("permissions: removed user", "nick", nick)
	return nil
}

// WriteChannel adds or updates a channel permission entry in the database
// and refreshes the in-memory permission cache.
func (ps *PermissionsStore) WriteChannel(channel string, ch config.ChannelPermissions) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	row := channelPermissionsToRow(channel, ch)
	if err := ps.db.SetChannelPermission(row); err != nil {
		return fmt.Errorf("PermissionsStore.WriteChannel: %w", err)
	}

	if err := ps.refreshCache(); err != nil {
		return fmt.Errorf("PermissionsStore.WriteChannel: %w", err)
	}

	ps.logger.Info("permissions: wrote channel", "channel", channel)
	return nil
}

// RemoveChannel deletes a channel permission entry from the database and
// refreshes the in-memory permission cache. Returns an error if the channel
// does not exist.
func (ps *PermissionsStore) RemoveChannel(channel string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if err := ps.db.DeleteChannelPermission(channel); err != nil {
		return fmt.Errorf("PermissionsStore.RemoveChannel: %w", err)
	}

	if err := ps.refreshCache(); err != nil {
		return fmt.Errorf("PermissionsStore.RemoveChannel: %w", err)
	}

	ps.logger.Info("permissions: removed channel", "channel", channel)
	return nil
}

// UserExists checks if a user exists in the database (case-insensitive).
func (ps *PermissionsStore) UserExists(nick string) (bool, error) {
	_, err := ps.db.GetUser(nick)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("PermissionsStore.UserExists: %w", err)
	}
	return true, nil
}

// refreshCache reloads permissions from the database and updates the
// in-memory PermissionManager. Must be called with ps.mu held.
func (ps *PermissionsStore) refreshCache() error {
	cfg, err := config.LoadPermissionsFromDB(ps.db)
	if err != nil {
		return fmt.Errorf("refreshCache: %w", err)
	}
	ps.pm.Update(cfg)
	return nil
}

// userPermissionsToRow converts config.UserPermissions to a db.UserRow.
func userPermissionsToRow(nick string, u config.UserPermissions) *db.UserRow {
	return &db.UserRow{
		Nick:               nick,
		Role:               u.Role,
		Tools:              db.StringSlice(u.Tools),
		DenyTools:          db.StringSlice(u.DenyTools),
		Autonomy:           u.Autonomy,
		AllowedModels:      db.StringSlice(u.AllowedModels),
		DenyModels:         db.StringSlice(u.DenyModels),
		MaxMessagesPerHour: u.MaxMessagesPerHour,
		APIKey:             u.APIKey,
	}
}

// channelPermissionsToRow converts config.ChannelPermissions to a db.ChannelPermissionRow.
func channelPermissionsToRow(channel string, ch config.ChannelPermissions) *db.ChannelPermissionRow {
	return &db.ChannelPermissionRow{
		Channel:       channel,
		Tools:         db.StringSlice(ch.Tools),
		DenyTools:     db.StringSlice(ch.DenyTools),
		Autonomy:      ch.Autonomy,
		AllowedModels: db.StringSlice(ch.AllowedModels),
	}
}
