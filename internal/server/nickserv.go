package server

import (
	"log/slog"
	"strings"
	"sync"
	"time"
)

// defaultNickServCacheTTL is the default cache duration for NickServ
// identification results.
const defaultNickServCacheTTL = 5 * time.Minute

// WhoisFunc is the function signature for performing a WHOIS lookup.
// It returns the account name the nick is logged in as, or empty if not
// identified. This is typically irc.Connection.Whois but is abstracted
// for testing.
type WhoisFunc func(nick string) (account string, err error)

// NickServVerifier checks whether IRC users are identified with NickServ
// by performing WHOIS lookups and caching the results. Concurrent lookups
// for the same nick are deduplicated (singleflight pattern). It is safe
// for concurrent use.
type NickServVerifier struct {
	whois    WhoisFunc
	cacheTTL time.Duration
	logger   *slog.Logger

	mu    sync.RWMutex
	cache map[string]cachedIdentity

	// inflight tracks in-progress WHOIS lookups to prevent thundering herd.
	// Key is lowercased nick. When a lookup is in progress, subsequent
	// callers wait on the channel instead of issuing duplicate WHOIS commands.
	inflightMu sync.Mutex
	inflight   map[string]*inflightLookup
}

// cachedIdentity stores a cached NickServ identification result.
type cachedIdentity struct {
	account   string    // NickServ account name; empty if not identified
	expiresAt time.Time // when this cache entry expires
}

// inflightLookup represents an in-progress WHOIS lookup.
type inflightLookup struct {
	done    chan struct{} // closed when the lookup completes
	account string        // result (valid after done is closed)
	err     error         // error (valid after done is closed)
}

// NewNickServVerifier creates a new NickServVerifier. The whois function is
// called to perform WHOIS lookups when the cache misses. If cacheTTL is zero,
// caching is disabled and every check performs a WHOIS lookup.
func NewNickServVerifier(whois WhoisFunc, cacheTTL time.Duration, logger *slog.Logger) *NickServVerifier {
	return &NickServVerifier{
		whois:    whois,
		cacheTTL: cacheTTL,
		logger:   logger,
		cache:    make(map[string]cachedIdentity),
		inflight: make(map[string]*inflightLookup),
	}
}

// IsIdentified returns true if the given nick is identified with NickServ.
// It checks the cache first, then performs a WHOIS lookup if needed.
// Concurrent lookups for the same nick are deduplicated.
// Returns false on WHOIS errors (fail-closed for security).
func (v *NickServVerifier) IsIdentified(nick string) bool {
	lowerNick := strings.ToLower(nick)

	// Check cache.
	if v.cacheTTL > 0 {
		v.mu.RLock()
		if cached, ok := v.cache[lowerNick]; ok && time.Now().Before(cached.expiresAt) {
			v.mu.RUnlock()
			return cached.account != ""
		}
		v.mu.RUnlock()
	}

	// Check for in-flight lookup (singleflight dedup).
	v.inflightMu.Lock()
	if fl, ok := v.inflight[lowerNick]; ok {
		v.inflightMu.Unlock()
		// Wait for the in-flight lookup to complete.
		<-fl.done
		if fl.err != nil {
			return false
		}
		return fl.account != ""
	}

	// No in-flight lookup — start one.
	fl := &inflightLookup{done: make(chan struct{})}
	v.inflight[lowerNick] = fl
	v.inflightMu.Unlock()

	// Perform WHOIS lookup.
	account, err := v.whois(nick)
	fl.account = account
	fl.err = err
	close(fl.done)

	// Remove from in-flight map.
	v.inflightMu.Lock()
	delete(v.inflight, lowerNick)
	v.inflightMu.Unlock()

	if err != nil {
		v.logger.Warn("WHOIS lookup failed, denying access (fail-closed)",
			"nick", nick,
			"error", err,
		)
		return false
	}

	// Cache the result.
	if v.cacheTTL > 0 {
		v.mu.Lock()
		v.cache[lowerNick] = cachedIdentity{
			account:   account,
			expiresAt: time.Now().Add(v.cacheTTL),
		}
		v.mu.Unlock()
	}

	identified := account != ""
	if !identified {
		v.logger.Debug("nick not identified with NickServ",
			"nick", nick,
		)
	}
	return identified
}

// InvalidateCache removes a specific nick from the cache, or clears the
// entire cache if nick is empty.
func (v *NickServVerifier) InvalidateCache(nick string) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if nick == "" {
		clear(v.cache)
		return
	}
	delete(v.cache, strings.ToLower(nick))
}
