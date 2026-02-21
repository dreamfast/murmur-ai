package server

import (
	"context"
	"crypto/subtle"
	"hash/fnv"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"murmur/internal/bus"
	"murmur/internal/config"
)

// adminOnlyTools lists tool names that should only be visible to admin users.
// FilterTools removes these from non-admin users' tool lists regardless of
// their configured tool permissions.
var adminOnlyTools = map[string]struct{}{
	"permissions_manage": {},
}

// PermissionManager enforces user and channel permissions by filtering tools,
// checking model access, and applying rate limits. It wraps a PermissionsConfig
// and provides caching for resolved effective permissions.
type PermissionManager struct {
	mu     sync.RWMutex
	cfg    *config.PermissionsConfig
	logger *slog.Logger

	// logPermissions controls whether permission_filter log messages are emitted.
	// Updated via SetLogPermissions during hot reload. Uses atomic.Bool for
	// lock-free concurrent access from FilterTools (read) and Reload (write).
	logPermissions atomic.Bool

	// cacheMu protects the effective permissions cache.
	cacheMu sync.RWMutex
	cache   map[string]cachedPermissions

	// rateMu protects the rate limit sliding window.
	rateMu   sync.Mutex
	rateHits map[string][]time.Time
}

// cachedPermissions stores a resolved EffectivePermissions with content hashes
// of the tool/model lists that were used to compute it. The cache is invalidated
// when the config changes or when the available tools/models change (even if
// the count stays the same).
type cachedPermissions struct {
	perms     config.EffectivePermissions
	toolHash  uint64 // FNV hash of sorted tool names at computation time
	modelHash uint64 // FNV hash of sorted model names at computation time
}

// NewPermissionManager creates a new PermissionManager. If permCfg is nil,
// an empty config is used (no restrictions).
func NewPermissionManager(permCfg *config.PermissionsConfig, logger *slog.Logger) *PermissionManager {
	if permCfg == nil {
		permCfg = &config.PermissionsConfig{}
	}
	return &PermissionManager{
		cfg:      permCfg,
		logger:   logger,
		cache:    make(map[string]cachedPermissions),
		rateHits: make(map[string][]time.Time),
	}
}

// GetEffective returns the resolved effective permissions for a user in a
// channel. Results are cached and invalidated when the config changes or
// the available tool/model names change.
func (pm *PermissionManager) GetEffective(nick, channel string, allToolNames, allModelNames []string) config.EffectivePermissions {
	key := strings.ToLower(nick) + "\x00" + strings.ToLower(channel)
	th := hashNames(allToolNames)
	mh := hashNames(allModelNames)

	// Check cache.
	pm.cacheMu.RLock()
	if cached, ok := pm.cache[key]; ok {
		if cached.toolHash == th && cached.modelHash == mh {
			pm.cacheMu.RUnlock()
			return cached.perms
		}
	}
	pm.cacheMu.RUnlock()

	// Compute and cache.
	pm.mu.RLock()
	user := pm.cfg.GetUser(nick)
	ch := pm.cfg.GetChannel(channel)
	pm.mu.RUnlock()

	ep := config.ResolveEffectivePermissions(user, ch, allToolNames, allModelNames)

	// Resolve rate limit default: if user's rate limit is 0 ("not set"),
	// use the default user's rate limit.
	if ep.RateLimit == 0 {
		pm.mu.RLock()
		def := pm.cfg.GetUser("default")
		pm.mu.RUnlock()
		ep.RateLimit = def.MaxMessagesPerHour
	}

	pm.cacheMu.Lock()
	pm.cache[key] = cachedPermissions{
		perms:     ep,
		toolHash:  th,
		modelHash: mh,
	}
	pm.cacheMu.Unlock()

	return ep
}

// hashNames computes a stable FNV-1a hash of a sorted copy of the names slice.
// This is used for cache invalidation — if the set of available tools or models
// changes (even if the count stays the same), the hash will differ.
func hashNames(names []string) uint64 {
	sorted := make([]string, len(names))
	copy(sorted, names)
	sort.Strings(sorted)

	h := fnv.New64a()
	for _, name := range sorted {
		h.Write([]byte(name))
		h.Write([]byte{0}) // separator to avoid "ab"+"c" == "a"+"bc"
	}
	return h.Sum64()
}

// FilterTools returns only the tools that the given user is allowed to use
// in the given channel. If pm is nil, all tools are returned unchanged.
func (pm *PermissionManager) FilterTools(tools []bus.ToolDef, nick, channel string, allModelNames []string) []bus.ToolDef {
	if pm == nil {
		return tools
	}

	// System nicks (starting with _) and empty nicks (legacy scheduled tasks
	// without a creator) bypass filtering.
	if nick == "" || strings.HasPrefix(nick, "_") {
		return tools
	}

	allToolNames := make([]string, len(tools))
	for i, t := range tools {
		allToolNames[i] = t.Name
	}

	ep := pm.GetEffective(nick, channel, allToolNames, allModelNames)

	allowed := make(map[string]struct{}, len(ep.Tools))
	for _, t := range ep.Tools {
		allowed[t] = struct{}{}
	}

	// Remove admin-only tools for non-admin users. These tools should never
	// be visible to regular users regardless of their tool permissions.
	if !ep.IsAdmin {
		for name := range adminOnlyTools {
			delete(allowed, name)
		}
	}

	filtered := make([]bus.ToolDef, 0, len(ep.Tools))
	for _, t := range tools {
		if _, ok := allowed[t.Name]; ok {
			filtered = append(filtered, t)
		}
	}

	if len(filtered) != len(tools) && pm.logPermissions.Load() {
		denied := make([]string, 0, len(tools)-len(filtered))
		for _, t := range tools {
			if _, ok := allowed[t.Name]; !ok {
				denied = append(denied, t.Name)
			}
		}
		pm.logger.Info("permission_filter",
			"nick", nick,
			"channel", channel,
			"total", len(tools),
			"allowed", len(filtered),
			"denied_tools", denied,
		)
	}

	return filtered
}

// IsAdmin returns true if the user has the admin role. Returns false if pm is nil.
func (pm *PermissionManager) IsAdmin(nick string) bool {
	if pm == nil {
		return false
	}
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	user := pm.cfg.GetUser(nick)
	return strings.EqualFold(user.Role, "admin")
}

// CheckRateLimit returns true if the user is within their rate limit.
// Returns true (allowed) if pm is nil or the user has no rate limit.
func (pm *PermissionManager) CheckRateLimit(nick string) bool {
	if pm == nil {
		return true
	}

	pm.mu.RLock()
	user := pm.cfg.GetUser(nick)
	limit := user.MaxMessagesPerHour
	// Resolve default if not set.
	if limit == 0 {
		def := pm.cfg.GetUser("default")
		limit = def.MaxMessagesPerHour
	}
	pm.mu.RUnlock()

	// -1 or 0 (after default resolution) means unlimited.
	if limit <= 0 {
		return true
	}

	now := time.Now()
	cutoff := now.Add(-1 * time.Hour)
	lowerNick := strings.ToLower(nick)

	pm.rateMu.Lock()
	defer pm.rateMu.Unlock()

	// Prune old entries.
	hits := pm.rateHits[lowerNick]
	pruned := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}

	if len(pruned) >= limit {
		pm.rateHits[lowerNick] = pruned
		return false
	}

	pm.rateHits[lowerNick] = append(pruned, now)
	return true
}

// IsModelAllowed returns true if the user is allowed to use the given model
// in the given channel. Returns true if pm is nil.
func (pm *PermissionManager) IsModelAllowed(nick, channel, modelName string, allToolNames, allModelNames []string) bool {
	if pm == nil {
		return true
	}

	// System nicks and empty nicks bypass model checks, matching FilterTools behavior.
	if nick == "" || strings.HasPrefix(nick, "_") {
		return true
	}

	ep := pm.GetEffective(nick, channel, allToolNames, allModelNames)
	for _, m := range ep.Models {
		if m == modelName {
			return true
		}
	}
	return false
}

// GetUserByAPIKey returns the nick associated with the given API key, or
// empty string if not found. Returns empty string if pm is nil. Uses
// constant-time comparison to prevent timing attacks on API key values.
func (pm *PermissionManager) GetUserByAPIKey(apiKey string) string {
	if pm == nil || apiKey == "" {
		return ""
	}

	pm.mu.RLock()
	defer pm.mu.RUnlock()

	for nick, user := range pm.cfg.Users {
		if user.APIKey != "" && subtle.ConstantTimeCompare([]byte(user.APIKey), []byte(apiKey)) == 1 {
			return nick
		}
	}
	return ""
}

// Update replaces the permissions config and invalidates all caches.
func (pm *PermissionManager) Update(permCfg *config.PermissionsConfig) {
	if permCfg == nil {
		permCfg = &config.PermissionsConfig{}
	}

	pm.mu.Lock()
	pm.cfg = permCfg
	pm.mu.Unlock()

	pm.InvalidateCache()
}

// InvalidateCache clears the effective permissions cache.
func (pm *PermissionManager) InvalidateCache() {
	pm.cacheMu.Lock()
	pm.cache = make(map[string]cachedPermissions)
	pm.cacheMu.Unlock()
}

// StartCleanup runs a background goroutine that periodically evicts stale
// rate limit entries. It stops when the context is cancelled.
func (pm *PermissionManager) StartCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pm.cleanupRateLimits()
			}
		}
	}()
}

// cleanupRateLimits removes rate limit entries older than 1 hour.
func (pm *PermissionManager) cleanupRateLimits() {
	cutoff := time.Now().Add(-1 * time.Hour)

	pm.rateMu.Lock()
	defer pm.rateMu.Unlock()

	for nick, hits := range pm.rateHits {
		pruned := hits[:0]
		for _, t := range hits {
			if t.After(cutoff) {
				pruned = append(pruned, t)
			}
		}
		if len(pruned) == 0 {
			delete(pm.rateHits, nick)
		} else {
			pm.rateHits[nick] = pruned
		}
	}
}

// SetLogPermissions controls whether FilterTools emits permission_filter log
// messages. This is called during hot reload to sync with the debug config.
func (pm *PermissionManager) SetLogPermissions(enabled bool) {
	pm.logPermissions.Store(enabled)
}

// Config returns the current permissions config. This is used by other
// components that need to inspect the raw config (e.g., admin commands).
// The returned pointer is the internal config — callers must NOT modify it.
func (pm *PermissionManager) Config() *config.PermissionsConfig {
	if pm == nil {
		return &config.PermissionsConfig{}
	}
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.cfg
}
