// Package dashboard provides a web-based IRC chat interface for Murmur.
// It bridges WebSocket connections to per-user IRC sessions, allowing
// browser clients to interact with IRC channels through the dashboard.
package dashboard

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	// sessionIDLen is the byte length of session IDs (32 bytes = 64 hex chars).
	sessionIDLen = 32
	// sessionCookieName is the HTTP cookie name for dashboard sessions.
	sessionCookieName = "murmur_session"
	// cleanupInterval is how often the session store evicts expired sessions.
	cleanupInterval = 5 * time.Minute
)

// Session represents an authenticated dashboard user session. Each session
// owns an IRC bridge connection that relays messages between the browser
// and the IRC server.
type Session struct {
	ID        string
	Nick      string
	Password  string // NickServ password, kept in memory only
	CreatedAt time.Time
	LastSeen  time.Time
	Bridge    *Bridge // nil until WebSocket connects
}

// SessionStore manages in-memory dashboard sessions with automatic expiry.
// Sessions are identified by cryptographically random IDs stored in
// HttpOnly cookies.
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
	timeout  time.Duration
	logger   *slog.Logger
}

// NewSessionStore creates a session store with the given session timeout.
func NewSessionStore(timeout time.Duration, logger *slog.Logger) *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*Session),
		timeout:  timeout,
		logger:   logger,
	}
}

// Create generates a new session for the given nick and returns it.
// The session ID is a cryptographically random hex string.
func (s *SessionStore) Create(nick string) (*Session, error) {
	id, err := generateSessionID()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	sess := &Session{
		ID:        id,
		Nick:      nick,
		CreatedAt: now,
		LastSeen:  now,
	}

	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()

	s.logger.Info("session created", "nick", nick, "session_id", id[:8]+"...")
	return sess, nil
}

// Get retrieves a session by ID and updates its last-seen time.
// Returns nil if the session does not exist or has expired.
func (s *SessionStore) Get(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[id]
	if !ok {
		return nil
	}

	if time.Since(sess.LastSeen) > s.timeout {
		s.destroyLocked(sess)
		return nil
	}

	sess.LastSeen = time.Now()
	return sess
}

// Destroy removes a session and closes its IRC bridge if active.
func (s *SessionStore) Destroy(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sess, ok := s.sessions[id]; ok {
		s.destroyLocked(sess)
	}
}

// destroyLocked removes a session while holding the lock.
func (s *SessionStore) destroyLocked(sess *Session) {
	if sess.Bridge != nil {
		sess.Bridge.Close()
	}
	delete(s.sessions, sess.ID)
	s.logger.Info("session destroyed", "nick", sess.Nick, "session_id", sess.ID[:8]+"...")
}

// GetFromRequest extracts the session from an HTTP request cookie.
// Returns nil if no valid session cookie is present.
func (s *SessionStore) GetFromRequest(r *http.Request) *Session {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	return s.Get(cookie.Value)
}

// SetCookie writes the session cookie to the HTTP response with secure
// defaults: HttpOnly, SameSite=Strict, and Secure when behind TLS.
func SetCookie(w http.ResponseWriter, r *http.Request, sessionID string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
}

// StartCleanup runs a background goroutine that periodically evicts expired
// sessions. It stops when the done channel is closed.
func (s *SessionStore) StartCleanup(done <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				s.closeAll()
				return
			case <-ticker.C:
				s.evictExpired()
			}
		}
	}()
}

// evictExpired removes all sessions that have exceeded the timeout.
func (s *SessionStore) evictExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, sess := range s.sessions {
		if time.Since(sess.LastSeen) > s.timeout {
			s.destroyLocked(sess)
		}
	}
}

// closeAll destroys all sessions. Called during shutdown.
func (s *SessionStore) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, sess := range s.sessions {
		if sess.Bridge != nil {
			sess.Bridge.Close()
		}
	}
	s.sessions = make(map[string]*Session)
	s.logger.Info("all dashboard sessions closed")
}

// AttachBridge sets the bridge on a session under the store lock.
func (s *SessionStore) AttachBridge(id string, bridge *Bridge) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[id]; ok {
		sess.Bridge = bridge
	}
}

// DetachBridge clears the bridge on a session under the store lock.
func (s *SessionStore) DetachBridge(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[id]; ok {
		sess.Bridge = nil
	}
}

// Count returns the number of active sessions.
func (s *SessionStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// generateSessionID creates a cryptographically random session ID.
func generateSessionID() (string, error) {
	b := make([]byte, sessionIDLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
