package config

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"murmur/internal/db"
)

// PermissionsConfig is the top-level structure of permissions.toml.
// It defines user-level and channel-level permission rules that control
// which tools, models, and autonomy levels are available.
type PermissionsConfig struct {
	// Users maps IRC nicks to their permission rules.
	// The special key "default" defines fallback permissions for unknown users.
	Users map[string]UserPermissions `toml:"users"`
	// Channels maps channel names to their permission rules.
	Channels map[string]ChannelPermissions `toml:"channels"`
}

// UserPermissions defines what a specific IRC user can do.
type UserPermissions struct {
	// Role is "admin" or "user". Admins can use !user/!channel commands
	// and the permissions_manage tool. Defaults to "user".
	Role string `toml:"role"`
	// Tools is the list of allowed tool names. Use "*" for all tools.
	// Supports prefix globs like "note_*".
	Tools []string `toml:"tools"`
	// DenyTools is the list of explicitly denied tool names.
	// Deny always wins over allow.
	DenyTools []string `toml:"deny_tools"`
	// Autonomy is the autonomy level: "report", "approve", or "auto".
	// Defaults to "approve".
	Autonomy string `toml:"autonomy"`
	// AllowedModels is the list of allowed LLM provider names. Use "*" for all.
	AllowedModels []string `toml:"allowed_models"`
	// DenyModels is the list of explicitly denied LLM provider names.
	DenyModels []string `toml:"deny_models"`
	// MaxMessagesPerHour is the per-user rate limit. -1 means unlimited.
	// 0 means "not set" — the PermissionManager resolves this to the
	// [users.default] value at enforcement time.
	MaxMessagesPerHour int `toml:"max_messages_per_hour"`
	// APIKey is an optional per-user API key for webhook/REST authentication.
	// Events received with this key are processed with this user's permissions.
	APIKey string `toml:"api_key"`
}

// ChannelPermissions defines tool and model restrictions for a channel.
type ChannelPermissions struct {
	// Tools is the list of allowed tool names for this channel.
	// Use "*" for all tools. Supports prefix globs like "note_*".
	Tools []string `toml:"tools"`
	// DenyTools is the list of explicitly denied tool names.
	// Deny always wins over allow.
	DenyTools []string `toml:"deny_tools"`
	// Autonomy overrides the autonomy level for this channel.
	// The effective autonomy is the most restrictive of user and channel.
	Autonomy string `toml:"autonomy"`
	// AllowedModels is the list of allowed LLM provider names for this channel.
	AllowedModels []string `toml:"allowed_models"`
}

// EffectivePermissions is the computed result of intersecting user and
// channel permissions. It contains fully resolved names (no patterns).
type EffectivePermissions struct {
	// Tools is the resolved list of allowed tool names.
	Tools []string
	// Autonomy is the most restrictive autonomy level.
	Autonomy string
	// Models is the resolved list of allowed model names.
	Models []string
	// IsAdmin is true if the user has role="admin".
	IsAdmin bool
	// RateLimit is the max messages per hour (-1 = unlimited).
	RateLimit int
}

// LoadPermissionsConfig reads and parses a permissions TOML file.
// Returns an empty config (not an error) if the file does not exist.
func LoadPermissionsConfig(path string) (*PermissionsConfig, error) {
	if path == "" {
		return &PermissionsConfig{}, nil
	}

	expanded, err := expandHome(path)
	if err != nil {
		return nil, fmt.Errorf("LoadPermissionsConfig: %w", err)
	}

	data, err := os.ReadFile(expanded)
	if err != nil {
		if os.IsNotExist(err) {
			return &PermissionsConfig{}, nil
		}
		return nil, fmt.Errorf("LoadPermissionsConfig: %w", err)
	}

	var cfg PermissionsConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("LoadPermissionsConfig: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("LoadPermissionsConfig: %w", err)
	}

	return &cfg, nil
}

// Validate checks that all permission entries have valid field values.
// It also detects case-duplicate keys (e.g. "Alice" and "alice") which
// would cause non-deterministic lookups.
func (c *PermissionsConfig) Validate() error {
	// Check for case-duplicate user keys and API key collisions.
	seenUsers := make(map[string]string, len(c.Users))
	seenAPIKeys := make(map[string]string)
	for nick, u := range c.Users {
		lower := strings.ToLower(nick)
		if existing, ok := seenUsers[lower]; ok {
			return fmt.Errorf("duplicate user entries %q and %q (case-insensitive collision)", existing, nick)
		}
		seenUsers[lower] = nick

		if u.Role != "" && u.Role != "admin" && u.Role != "user" {
			return fmt.Errorf("users.%s.role must be \"admin\" or \"user\", got %q", nick, u.Role)
		}
		if u.Autonomy != "" && !isValidAutonomy(u.Autonomy) {
			return fmt.Errorf("users.%s.autonomy must be \"report\", \"approve\", or \"auto\", got %q", nick, u.Autonomy)
		}
		if u.MaxMessagesPerHour < -1 {
			return fmt.Errorf("users.%s.max_messages_per_hour must be >= -1, got %d", nick, u.MaxMessagesPerHour)
		}
		if u.APIKey != "" {
			if existing, ok := seenAPIKeys[u.APIKey]; ok {
				return fmt.Errorf("duplicate api_key between users %q and %q", existing, nick)
			}
			seenAPIKeys[u.APIKey] = nick
		}
	}

	// Check for case-duplicate channel keys.
	seenChannels := make(map[string]string, len(c.Channels))
	for ch, cp := range c.Channels {
		lower := strings.ToLower(ch)
		if existing, ok := seenChannels[lower]; ok {
			return fmt.Errorf("duplicate channel entries %q and %q (case-insensitive collision)", existing, ch)
		}
		seenChannels[lower] = ch

		if cp.Autonomy != "" && !isValidAutonomy(cp.Autonomy) {
			return fmt.Errorf("channels.%s.autonomy must be \"report\", \"approve\", or \"auto\", got %q", ch, cp.Autonomy)
		}
	}
	return nil
}

// HasUsers returns true if any user entries are defined (including "default").
func (c *PermissionsConfig) HasUsers() bool {
	return len(c.Users) > 0
}

// GetUser returns the permissions for a nick, falling back to [users.default]
// if the nick is not explicitly configured. If neither exists, returns a
// zero-value UserPermissions.
func (c *PermissionsConfig) GetUser(nick string) UserPermissions {
	if c == nil || len(c.Users) == 0 {
		return UserPermissions{}
	}

	// Case-insensitive nick lookup.
	lower := strings.ToLower(nick)
	for k, v := range c.Users {
		if strings.ToLower(k) == lower {
			return v
		}
	}

	// Fall back to default (case-insensitive).
	for k, v := range c.Users {
		if strings.ToLower(k) == "default" {
			return v
		}
	}
	return UserPermissions{}
}

// GetChannel returns the permissions for a channel. If the channel is not
// configured, returns a zero-value ChannelPermissions (no restrictions from
// the channel side).
func (c *PermissionsConfig) GetChannel(channel string) ChannelPermissions {
	if c == nil || len(c.Channels) == 0 {
		return ChannelPermissions{}
	}

	// Case-insensitive channel lookup.
	lower := strings.ToLower(channel)
	for k, v := range c.Channels {
		if strings.ToLower(k) == lower {
			return v
		}
	}
	return ChannelPermissions{}
}

// ResolveEffectivePermissions computes the effective permissions for a user
// in a channel by intersecting their allow lists and subtracting deny lists.
//
// The resolution rules are:
//   - Tools: (expanded_user_tools ∩ expanded_channel_tools) - user_deny - channel_deny
//   - Autonomy: most restrictive of user and channel
//   - Models: (expanded_user_models ∩ expanded_channel_models) - user_deny_models
//   - If either side has no tools/models configured (empty list), it is treated
//     as "no restriction from that side" (equivalent to "*").
func ResolveEffectivePermissions(
	user UserPermissions,
	channel ChannelPermissions,
	allToolNames []string,
	allModelNames []string,
) EffectivePermissions {
	ep := EffectivePermissions{
		IsAdmin:   strings.EqualFold(user.Role, "admin"),
		RateLimit: user.MaxMessagesPerHour,
	}

	// Resolve tools.
	userTools := expandPatterns(user.Tools, allToolNames)
	channelTools := expandPatterns(channel.Tools, allToolNames)
	ep.Tools = intersect(userTools, channelTools)
	ep.Tools = subtract(ep.Tools, expandDenyPatterns(user.DenyTools, allToolNames))
	ep.Tools = subtract(ep.Tools, expandDenyPatterns(channel.DenyTools, allToolNames))
	sort.Strings(ep.Tools)

	// Resolve autonomy.
	ep.Autonomy = MostRestrictiveAutonomy(user.Autonomy, channel.Autonomy)

	// Resolve models.
	userModels := expandPatterns(user.AllowedModels, allModelNames)
	channelModels := expandPatterns(channel.AllowedModels, allModelNames)
	ep.Models = intersect(userModels, channelModels)
	ep.Models = subtract(ep.Models, expandExact(user.DenyModels))
	sort.Strings(ep.Models)

	return ep
}

// MatchesToolPattern checks if a pattern matches a tool name.
// Supported patterns:
//   - "*" matches any tool name
//   - "prefix*" matches tool names starting with "prefix" (any trailing wildcard)
//   - exact string matches the tool name exactly
func MatchesToolPattern(pattern, toolName string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		prefix := pattern[:len(pattern)-1]
		return strings.HasPrefix(toolName, prefix)
	}
	return pattern == toolName
}

// MostRestrictiveAutonomy returns the more restrictive of two autonomy levels.
// The ordering from most to least restrictive is: report > approve > auto.
// Empty strings are treated as "no restriction" (least restrictive).
func MostRestrictiveAutonomy(a, b string) string {
	rank := func(s string) int {
		switch s {
		case "report":
			return 3
		case "approve":
			return 2
		case "auto":
			return 1
		default:
			return 0 // empty or unknown = no restriction
		}
	}

	ra, rb := rank(a), rank(b)
	if ra >= rb {
		if ra == 0 {
			return "" // both empty
		}
		return a
	}
	return b
}

// LoadPermissionsFromDB queries the users and channel_permissions tables and
// builds a PermissionsConfig. This is the DB-backed replacement for
// LoadPermissionsConfig (which reads from a TOML file). The returned config
// is compatible with all downstream code (PermissionManager, FilterTools, etc.).
func LoadPermissionsFromDB(database *db.DB) (*PermissionsConfig, error) {
	users, err := database.ListUsers()
	if err != nil {
		return nil, fmt.Errorf("LoadPermissionsFromDB: list users: %w", err)
	}

	cfg := &PermissionsConfig{
		Users:    make(map[string]UserPermissions, len(users)),
		Channels: make(map[string]ChannelPermissions),
	}

	for _, u := range users {
		cfg.Users[u.Nick] = UserPermissions{
			Role:               u.Role,
			Tools:              []string(u.Tools),
			DenyTools:          []string(u.DenyTools),
			Autonomy:           u.Autonomy,
			AllowedModels:      []string(u.AllowedModels),
			DenyModels:         []string(u.DenyModels),
			MaxMessagesPerHour: u.MaxMessagesPerHour,
			APIKey:             u.APIKey,
		}
	}

	channels, err := database.ListChannelPermissions()
	if err != nil {
		return nil, fmt.Errorf("LoadPermissionsFromDB: list channels: %w", err)
	}

	for _, cp := range channels {
		cfg.Channels[cp.Channel] = ChannelPermissions{
			Tools:         []string(cp.Tools),
			DenyTools:     []string(cp.DenyTools),
			Autonomy:      cp.Autonomy,
			AllowedModels: []string(cp.AllowedModels),
		}
	}

	return cfg, nil
}

// ImportPermissionsToDB imports a PermissionsConfig (typically loaded from TOML)
// into the database. This is used for the one-time migration from
// permissions.toml to SQLite. Existing users in the DB are skipped (no
// overwrite). Channels are upserted (created or updated). Returns the number
// of users and channels imported.
func ImportPermissionsToDB(database *db.DB, cfg *PermissionsConfig) (usersImported, channelsImported int, err error) {
	for nick, u := range cfg.Users {
		// Skip users that already exist in the DB.
		if _, err := database.GetUser(nick); err == nil {
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return usersImported, channelsImported, fmt.Errorf("ImportPermissionsToDB: check user %q: %w", nick, err)
		}

		row := &db.UserRow{
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
		if err := database.CreateUser(row); err != nil {
			return usersImported, channelsImported, fmt.Errorf("ImportPermissionsToDB: create user %q: %w", nick, err)
		}
		usersImported++
	}

	for channel, cp := range cfg.Channels {
		row := &db.ChannelPermissionRow{
			Channel:       channel,
			Tools:         db.StringSlice(cp.Tools),
			DenyTools:     db.StringSlice(cp.DenyTools),
			Autonomy:      cp.Autonomy,
			AllowedModels: db.StringSlice(cp.AllowedModels),
		}
		if err := database.SetChannelPermission(row); err != nil {
			return usersImported, channelsImported, fmt.Errorf("ImportPermissionsToDB: set channel %q: %w", channel, err)
		}
		channelsImported++
	}

	return usersImported, channelsImported, nil
}

// isValidAutonomy checks if a string is a valid autonomy level.
func isValidAutonomy(s string) bool {
	return s == "report" || s == "approve" || s == "auto"
}

// expandPatterns expands a list of allow patterns against all names.
// If patterns is empty, returns all names (no restriction from this side).
// If patterns contains "*", returns all names.
// Otherwise, each pattern is matched against all names.
func expandPatterns(patterns []string, allNames []string) []string {
	if len(patterns) == 0 {
		// No restriction — return all names.
		result := make([]string, len(allNames))
		copy(result, allNames)
		return result
	}
	return matchPatterns(patterns, allNames)
}

// expandDenyPatterns expands a list of deny patterns against all names.
// Unlike expandPatterns, an empty list means "deny nothing" (returns nil).
func expandDenyPatterns(patterns []string, allNames []string) []string {
	if len(patterns) == 0 {
		return nil
	}
	return matchPatterns(patterns, allNames)
}

// matchPatterns expands a non-empty list of patterns against all names.
// If patterns contains "*", returns all names.
// Otherwise, each pattern is matched against all names using MatchesToolPattern.
func matchPatterns(patterns []string, allNames []string) []string {
	seen := make(map[string]struct{})
	for _, pattern := range patterns {
		if pattern == "*" {
			result := make([]string, len(allNames))
			copy(result, allNames)
			return result
		}
		for _, name := range allNames {
			if MatchesToolPattern(pattern, name) {
				seen[name] = struct{}{}
			}
		}
	}

	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	return result
}

// expandExact returns a copy of the input slice (no pattern expansion).
// Used for deny_models which don't support glob patterns.
func expandExact(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	result := make([]string, len(names))
	copy(result, names)
	return result
}

// intersect returns elements present in both a and b.
// Note: "empty = no restriction" semantics are handled by expandPatterns
// (which returns all names when patterns is empty), not by this function.
func intersect(a, b []string) []string {
	set := make(map[string]struct{}, len(a))
	for _, s := range a {
		set[s] = struct{}{}
	}

	result := make([]string, 0, len(b))
	for _, s := range b {
		if _, ok := set[s]; ok {
			result = append(result, s)
		}
	}
	return result
}

// subtract returns elements in a that are not in b.
func subtract(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	deny := make(map[string]struct{}, len(b))
	for _, s := range b {
		deny[s] = struct{}{}
	}

	result := make([]string, 0, len(a))
	for _, s := range a {
		if _, ok := deny[s]; !ok {
			result = append(result, s)
		}
	}
	return result
}
