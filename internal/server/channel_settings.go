package server

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"murmur/internal/db"
)

// ChannelSettings holds per-channel configuration persisted in SQLite.
// An empty Provider means the channel uses the global default LLM provider.
type ChannelSettings struct {
	Channel     string
	Provider    string // LLM provider name; empty = global default
	AutoJoin    bool   // whether to rejoin this channel on reconnect
	TopicPrefix string // user-defined prefix prepended to the auto-generated topic
}

// ChannelSettingsStore provides CRUD operations for per-channel settings
// backed by the channel_settings SQLite table. All channel names are
// normalized to lowercase before storage and lookup to match IRC's
// case-insensitive channel semantics.
// All methods are safe for concurrent use (serialized by SQLite).
type ChannelSettingsStore struct {
	db     *db.DB
	logger *slog.Logger
}

// NewChannelSettingsStore creates a new store backed by the given database.
func NewChannelSettingsStore(database *db.DB, logger *slog.Logger) *ChannelSettingsStore {
	return &ChannelSettingsStore{db: database, logger: logger}
}

// normalizeChannel lowercases a channel name to match IRC case-insensitivity.
func normalizeChannel(ch string) string {
	return strings.ToLower(ch)
}

// Get returns the settings for a channel, or nil if no settings exist.
func (s *ChannelSettingsStore) Get(channel string) (*ChannelSettings, error) {
	channel = normalizeChannel(channel)

	var cs ChannelSettings
	err := s.db.QueryRow(
		`SELECT channel, provider, auto_join, topic_prefix FROM channel_settings WHERE channel = ?`,
		channel,
	).Scan(&cs.Channel, &cs.Provider, &cs.AutoJoin, &cs.TopicPrefix)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ChannelSettingsStore.Get: %w", err)
	}
	return &cs, nil
}

// Upsert inserts or replaces the full channel settings row. All fields are
// written; use the specific Set* methods to update individual fields without
// overwriting others. The input struct's Channel field is mutated to its
// normalized (lowercase) form.
func (s *ChannelSettingsStore) Upsert(cs *ChannelSettings) error {
	if cs == nil {
		return fmt.Errorf("ChannelSettingsStore.Upsert: settings must not be nil")
	}
	cs.Channel = normalizeChannel(cs.Channel)

	_, err := s.db.Exec(
		`INSERT INTO channel_settings (channel, provider, auto_join, topic_prefix, updated)
		 VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(channel) DO UPDATE SET
		   provider = excluded.provider,
		   auto_join = excluded.auto_join,
		   topic_prefix = excluded.topic_prefix,
		   updated = CURRENT_TIMESTAMP`,
		cs.Channel, cs.Provider, cs.AutoJoin, cs.TopicPrefix,
	)
	if err != nil {
		return fmt.Errorf("ChannelSettingsStore.Upsert: %w", err)
	}
	return nil
}

// SetProvider sets the LLM provider for a channel. An empty provider string
// means the channel should use the global default. Creates the row if it
// doesn't exist.
func (s *ChannelSettingsStore) SetProvider(channel, provider string) error {
	channel = normalizeChannel(channel)

	_, err := s.db.Exec(
		`INSERT INTO channel_settings (channel, provider, updated)
		 VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(channel) DO UPDATE SET
		   provider = excluded.provider,
		   updated = CURRENT_TIMESTAMP`,
		channel, provider,
	)
	if err != nil {
		return fmt.Errorf("ChannelSettingsStore.SetProvider: %w", err)
	}
	return nil
}

// SetAutoJoin sets whether a channel should be automatically rejoined on
// reconnect. Creates the row if it doesn't exist.
func (s *ChannelSettingsStore) SetAutoJoin(channel string, autoJoin bool) error {
	channel = normalizeChannel(channel)

	_, err := s.db.Exec(
		`INSERT INTO channel_settings (channel, auto_join, updated)
		 VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(channel) DO UPDATE SET
		   auto_join = excluded.auto_join,
		   updated = CURRENT_TIMESTAMP`,
		channel, autoJoin,
	)
	if err != nil {
		return fmt.Errorf("ChannelSettingsStore.SetAutoJoin: %w", err)
	}
	return nil
}

// GetProvider returns the LLM provider name for a channel. Returns an empty
// string if no channel-specific provider is set (meaning use global default).
func (s *ChannelSettingsStore) GetProvider(channel string) (string, error) {
	channel = normalizeChannel(channel)

	var provider string
	err := s.db.QueryRow(
		`SELECT provider FROM channel_settings WHERE channel = ?`,
		channel,
	).Scan(&provider)

	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("ChannelSettingsStore.GetProvider: %w", err)
	}
	return provider, nil
}

// GetAutoJoinChannels returns all channel names that have auto_join enabled.
// The returned names are lowercase (normalized).
func (s *ChannelSettingsStore) GetAutoJoinChannels() ([]string, error) {
	rows, err := s.db.Query(`SELECT channel FROM channel_settings WHERE auto_join = 1 ORDER BY channel`)
	if err != nil {
		return nil, fmt.Errorf("ChannelSettingsStore.GetAutoJoinChannels: %w", err)
	}
	defer rows.Close()

	var channels []string
	for rows.Next() {
		var ch string
		if err := rows.Scan(&ch); err != nil {
			return nil, fmt.Errorf("ChannelSettingsStore.GetAutoJoinChannels: scan: %w", err)
		}
		channels = append(channels, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ChannelSettingsStore.GetAutoJoinChannels: rows: %w", err)
	}
	return channels, nil
}
