package db

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// StringSlice is a []string that marshals to/from a JSON array TEXT column
// in SQLite. An empty slice is stored as "[]".
type StringSlice []string

// Scan implements the sql.Scanner interface for reading JSON TEXT from SQLite.
func (s *StringSlice) Scan(src any) error {
	if src == nil {
		*s = nil
		return nil
	}

	var raw string
	switch v := src.(type) {
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		return fmt.Errorf("StringSlice.Scan: unsupported type %T", src)
	}

	var result []string
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return fmt.Errorf("StringSlice.Scan: %w", err)
	}
	*s = result
	return nil
}

// Value implements the driver.Valuer interface for writing JSON TEXT to SQLite.
func (s StringSlice) Value() (driver.Value, error) {
	if s == nil {
		return "[]", nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("StringSlice.Value: %w", err)
	}
	return string(b), nil
}

// UserRow represents a row in the users table.
type UserRow struct {
	// Nick is the IRC nickname (primary key, case-insensitive).
	Nick string `json:"nick"`
	// Role is either "admin" or "user".
	Role string `json:"role"`
	// Tools is a JSON array of allowed tool patterns (e.g. ["*"], ["shell", "web_search"]).
	Tools StringSlice `json:"tools"`
	// DenyTools is a JSON array of denied tool patterns.
	DenyTools StringSlice `json:"deny_tools"`
	// Autonomy is the autonomy level: "report", "approve", "auto", or "" (inherit).
	Autonomy string `json:"autonomy"`
	// AllowedModels is a JSON array of allowed model patterns.
	AllowedModels StringSlice `json:"allowed_models"`
	// DenyModels is a JSON array of denied model patterns.
	DenyModels StringSlice `json:"deny_models"`
	// MaxMessagesPerHour is the rate limit (0 = unlimited).
	MaxMessagesPerHour int `json:"max_messages_per_hour"`
	// APIKey is an optional API key for REST authentication.
	APIKey string `json:"api_key,omitempty"`
	// NickServAccount is the NickServ account name for authentication.
	NickServAccount string `json:"nickserv_account,omitempty"`
	// Created is the timestamp when the user was created.
	Created time.Time `json:"created"`
	// Updated is the timestamp when the user was last modified.
	Updated time.Time `json:"updated"`
}

// ChannelPermissionRow represents a row in the channel_permissions table.
type ChannelPermissionRow struct {
	// Channel is the IRC channel name (primary key, case-insensitive).
	Channel string `json:"channel"`
	// Tools is a JSON array of allowed tool patterns for this channel.
	Tools StringSlice `json:"tools"`
	// DenyTools is a JSON array of denied tool patterns for this channel.
	DenyTools StringSlice `json:"deny_tools"`
	// Autonomy is the autonomy level override for this channel ("" = inherit).
	Autonomy string `json:"autonomy"`
	// AllowedModels is a JSON array of allowed model patterns for this channel.
	AllowedModels StringSlice `json:"allowed_models"`
}

// scanUser scans a single user row from the given scanner (Row or Rows).
func scanUser(scan func(dest ...any) error) (*UserRow, error) {
	var u UserRow
	var tools, denyTools, allowedModels, denyModels string

	err := scan(
		&u.Nick, &u.Role,
		&tools, &denyTools,
		&u.Autonomy,
		&allowedModels, &denyModels,
		&u.MaxMessagesPerHour,
		&u.APIKey, &u.NickServAccount,
		&u.Created, &u.Updated,
	)
	if err != nil {
		return nil, err
	}

	if err := u.Tools.Scan(tools); err != nil {
		return nil, fmt.Errorf("scanUser: tools: %w", err)
	}
	if err := u.DenyTools.Scan(denyTools); err != nil {
		return nil, fmt.Errorf("scanUser: deny_tools: %w", err)
	}
	if err := u.AllowedModels.Scan(allowedModels); err != nil {
		return nil, fmt.Errorf("scanUser: allowed_models: %w", err)
	}
	if err := u.DenyModels.Scan(denyModels); err != nil {
		return nil, fmt.Errorf("scanUser: deny_models: %w", err)
	}

	return &u, nil
}

// userColumns is the column list used in SELECT queries for the users table.
const userColumns = `nick, role, tools, deny_tools, autonomy, allowed_models, deny_models,
	max_messages_per_hour, api_key, nickserv_account, created, updated`

// GetUser retrieves a user by nick. Returns sql.ErrNoRows if not found.
func (db *DB) GetUser(nick string) (*UserRow, error) {
	row := db.QueryRow(
		`SELECT `+userColumns+` FROM users WHERE nick = ?`, nick,
	)
	u, err := scanUser(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("GetUser: %w", err)
	}
	return u, nil
}

// GetDefaultUser retrieves the "default" user row, which serves as the
// fallback when no specific user entry exists. Returns sql.ErrNoRows if
// no default user has been configured.
func (db *DB) GetDefaultUser() (*UserRow, error) {
	u, err := db.GetUser("default")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("GetDefaultUser: %w", err)
	}
	return u, nil
}

// GetUserByAPIKey retrieves a user by their API key. Returns sql.ErrNoRows
// if no user with that key exists. Empty API keys are never matched.
func (db *DB) GetUserByAPIKey(apiKey string) (*UserRow, error) {
	if apiKey == "" {
		return nil, sql.ErrNoRows
	}
	row := db.QueryRow(
		`SELECT `+userColumns+` FROM users WHERE api_key = ?`, apiKey,
	)
	u, err := scanUser(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("GetUserByAPIKey: %w", err)
	}
	return u, nil
}

// ListUsers returns all users ordered by nick.
func (db *DB) ListUsers() ([]UserRow, error) {
	rows, err := db.Query(`SELECT ` + userColumns + ` FROM users ORDER BY nick`)
	if err != nil {
		return nil, fmt.Errorf("ListUsers: %w", err)
	}
	defer rows.Close()

	var users []UserRow
	for rows.Next() {
		u, err := scanUser(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("ListUsers: scan row: %w", err)
		}
		users = append(users, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListUsers: iterate rows: %w", err)
	}
	return users, nil
}

// UserCount returns the total number of users in the database.
func (db *DB) UserCount() (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("UserCount: %w", err)
	}
	return count, nil
}

// marshalUserSlices marshals the four JSON array fields of a UserRow for
// database storage. Returns driver.Value results suitable for sql.Exec.
func marshalUserSlices(u *UserRow) (tools, denyTools, allowedModels, denyModels driver.Value, err error) {
	tools, err = u.Tools.Value()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("marshal tools: %w", err)
	}
	denyTools, err = u.DenyTools.Value()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("marshal deny_tools: %w", err)
	}
	allowedModels, err = u.AllowedModels.Value()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("marshal allowed_models: %w", err)
	}
	denyModels, err = u.DenyModels.Value()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("marshal deny_models: %w", err)
	}
	return tools, denyTools, allowedModels, denyModels, nil
}

// CreateUser inserts a new user into the database. Returns an error if a
// user with the same nick already exists.
func (db *DB) CreateUser(u *UserRow) error {
	tools, denyTools, allowedModels, denyModels, err := marshalUserSlices(u)
	if err != nil {
		return fmt.Errorf("CreateUser: %w", err)
	}

	_, err = db.Exec(
		`INSERT INTO users (nick, role, tools, deny_tools, autonomy, allowed_models,
			deny_models, max_messages_per_hour, api_key, nickserv_account)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.Nick, u.Role, tools, denyTools, u.Autonomy,
		allowedModels, denyModels, u.MaxMessagesPerHour,
		u.APIKey, u.NickServAccount,
	)
	if err != nil {
		return fmt.Errorf("CreateUser: %w", err)
	}
	return nil
}

// UpdateUser updates an existing user's fields. The nick field identifies
// which user to update. Returns an error if the user does not exist.
func (db *DB) UpdateUser(u *UserRow) error {
	tools, denyTools, allowedModels, denyModels, err := marshalUserSlices(u)
	if err != nil {
		return fmt.Errorf("UpdateUser: %w", err)
	}

	result, err := db.Exec(
		`UPDATE users SET role = ?, tools = ?, deny_tools = ?, autonomy = ?,
			allowed_models = ?, deny_models = ?, max_messages_per_hour = ?,
			api_key = ?, nickserv_account = ?, updated = CURRENT_TIMESTAMP
		 WHERE nick = ?`,
		u.Role, tools, denyTools, u.Autonomy,
		allowedModels, denyModels, u.MaxMessagesPerHour,
		u.APIKey, u.NickServAccount, u.Nick,
	)
	if err != nil {
		return fmt.Errorf("UpdateUser: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("UpdateUser: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("UpdateUser: user %q not found", u.Nick)
	}
	return nil
}

// DeleteUser deletes a user by nick. Returns an error if the user does not exist.
func (db *DB) DeleteUser(nick string) error {
	result, err := db.Exec(`DELETE FROM users WHERE nick = ?`, nick)
	if err != nil {
		return fmt.Errorf("DeleteUser: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("DeleteUser: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("DeleteUser: user %q not found", nick)
	}
	return nil
}

// GetChannelPermission retrieves channel permissions by channel name.
// Returns sql.ErrNoRows if not found.
func (db *DB) GetChannelPermission(channel string) (*ChannelPermissionRow, error) {
	var cp ChannelPermissionRow
	var tools, denyTools, allowedModels string

	err := db.QueryRow(
		`SELECT channel, tools, deny_tools, autonomy, allowed_models
		 FROM channel_permissions WHERE channel = ?`, channel,
	).Scan(&cp.Channel, &tools, &denyTools, &cp.Autonomy, &allowedModels)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("GetChannelPermission: %w", err)
	}

	if err := cp.Tools.Scan(tools); err != nil {
		return nil, fmt.Errorf("GetChannelPermission: tools: %w", err)
	}
	if err := cp.DenyTools.Scan(denyTools); err != nil {
		return nil, fmt.Errorf("GetChannelPermission: deny_tools: %w", err)
	}
	if err := cp.AllowedModels.Scan(allowedModels); err != nil {
		return nil, fmt.Errorf("GetChannelPermission: allowed_models: %w", err)
	}

	return &cp, nil
}

// ListChannelPermissions returns all channel permissions ordered by channel name.
func (db *DB) ListChannelPermissions() ([]ChannelPermissionRow, error) {
	rows, err := db.Query(
		`SELECT channel, tools, deny_tools, autonomy, allowed_models
		 FROM channel_permissions ORDER BY channel`,
	)
	if err != nil {
		return nil, fmt.Errorf("ListChannelPermissions: %w", err)
	}
	defer rows.Close()

	var perms []ChannelPermissionRow
	for rows.Next() {
		var cp ChannelPermissionRow
		var tools, denyTools, allowedModels string

		if err := rows.Scan(&cp.Channel, &tools, &denyTools, &cp.Autonomy, &allowedModels); err != nil {
			return nil, fmt.Errorf("ListChannelPermissions: scan row: %w", err)
		}

		if err := cp.Tools.Scan(tools); err != nil {
			return nil, fmt.Errorf("ListChannelPermissions: tools: %w", err)
		}
		if err := cp.DenyTools.Scan(denyTools); err != nil {
			return nil, fmt.Errorf("ListChannelPermissions: deny_tools: %w", err)
		}
		if err := cp.AllowedModels.Scan(allowedModels); err != nil {
			return nil, fmt.Errorf("ListChannelPermissions: allowed_models: %w", err)
		}

		perms = append(perms, cp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListChannelPermissions: iterate rows: %w", err)
	}
	return perms, nil
}

// SetChannelPermission creates or updates channel permissions (upsert).
func (db *DB) SetChannelPermission(cp *ChannelPermissionRow) error {
	tools, err := cp.Tools.Value()
	if err != nil {
		return fmt.Errorf("SetChannelPermission: marshal tools: %w", err)
	}
	var denyTools, allowedModels driver.Value
	denyTools, err = cp.DenyTools.Value()
	if err != nil {
		return fmt.Errorf("SetChannelPermission: marshal deny_tools: %w", err)
	}
	allowedModels, err = cp.AllowedModels.Value()
	if err != nil {
		return fmt.Errorf("SetChannelPermission: marshal allowed_models: %w", err)
	}

	_, err = db.Exec(
		`INSERT INTO channel_permissions (channel, tools, deny_tools, autonomy, allowed_models)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(channel) DO UPDATE SET
			tools = excluded.tools,
			deny_tools = excluded.deny_tools,
			autonomy = excluded.autonomy,
			allowed_models = excluded.allowed_models`,
		cp.Channel, tools, denyTools, cp.Autonomy, allowedModels,
	)
	if err != nil {
		return fmt.Errorf("SetChannelPermission: %w", err)
	}
	return nil
}

// DeleteChannelPermission deletes channel permissions by channel name.
// Returns an error if the channel does not exist.
func (db *DB) DeleteChannelPermission(channel string) error {
	result, err := db.Exec(`DELETE FROM channel_permissions WHERE channel = ?`, channel)
	if err != nil {
		return fmt.Errorf("DeleteChannelPermission: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("DeleteChannelPermission: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("DeleteChannelPermission: channel %q not found", channel)
	}
	return nil
}

// GetMetadata retrieves a metadata value by key. Returns sql.ErrNoRows if
// the key does not exist.
func (db *DB) GetMetadata(key string) (string, error) {
	var value string
	err := db.QueryRow(`SELECT value FROM metadata WHERE key = ?`, key).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		return "", fmt.Errorf("GetMetadata: %w", err)
	}
	return value, nil
}

// SetMetadata creates or updates a metadata key-value pair (upsert).
func (db *DB) SetMetadata(key, value string) error {
	_, err := db.Exec(
		`INSERT INTO metadata (key, value, updated)
		 VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated = excluded.updated`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("SetMetadata: %w", err)
	}
	return nil
}
