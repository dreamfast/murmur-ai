package dashboard

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"murmur/internal/config"
	mcrypto "murmur/internal/crypto"
	"murmur/web"
)

const (
	// loginRateLimit is the maximum login attempts per IP per minute.
	loginRateLimit = 5
	// loginRateWindow is the sliding window for login rate limiting.
	loginRateWindow = time.Minute
	// signatureMaxAge is the maximum age of a signed request timestamp.
	// Requests older than this are rejected to prevent replay attacks.
	signatureMaxAge = 30 * time.Second
	// signatureTimestampHeader is the HTTP header carrying the Unix timestamp.
	signatureTimestampHeader = "X-Request-Timestamp"
	// signatureHeader is the HTTP header carrying the HMAC-SHA256 signature.
	signatureHeader = "X-Request-Signature"
)

// loginRequest is the JSON body for POST /dashboard/login.
type loginRequest struct {
	Nick     string `json:"nick"`
	Password string `json:"password"`
}

// loginResponse is the JSON body returned from POST /dashboard/login.
type loginResponse struct {
	OK         bool   `json:"ok"`
	Nick       string `json:"nick,omitempty"`
	SigningKey string `json:"signing_key,omitempty"`
	Error      string `json:"error,omitempty"`
}

// CredentialVerifier is a function that verifies IRC credentials.
// It returns nil if authentication succeeds, or an error describing the failure.
type CredentialVerifier func(ctx context.Context, server string, port int, tls bool, serverPass, nick, password string) error

// Handler serves the dashboard HTTP endpoints and static files.
type Handler struct {
	sessions *SessionStore
	cfg      config.DashboardConfig
	ircCfg   config.IRCConfig
	logger   *slog.Logger
	status   StatusProvider // may be nil if no provider is configured
	verify   CredentialVerifier
	api      adminAPI // admin API dependencies; zero value means admin API is disabled

	// rateMu protects loginAttempts for per-IP rate limiting.
	rateMu        sync.Mutex
	loginAttempts map[string][]time.Time
}

// NewHandler creates a dashboard HTTP handler. The status parameter may be nil
// if no StatusProvider is available; the /dashboard/status endpoint will return
// 503 Service Unavailable in that case. The admin parameter may be nil if the
// admin API should be disabled; admin API endpoints will return 503 in that case.
func NewHandler(sessions *SessionStore, cfg config.DashboardConfig, ircCfg config.IRCConfig, status StatusProvider, admin *AdminDeps, logger *slog.Logger) *Handler {
	h := &Handler{
		sessions:      sessions,
		cfg:           cfg,
		ircCfg:        ircCfg,
		logger:        logger.With("component", "dashboard"),
		status:        status,
		verify:        VerifyIRCCredentials,
		loginAttempts: make(map[string][]time.Time),
	}
	if admin != nil {
		h.api = adminAPI{
			database:  admin.DB,
			admin:     admin.Admin,
			tasks:     admin.Tasks,
			tools:     admin.Tools,
			channels:  admin.Channels,
			providers: admin.Providers,
			reloader:  admin.Reloader,
			stats:     admin.Stats,
		}
	}
	return h
}

// ServeHTTP implements http.Handler and routes requests to the appropriate
// endpoint. Security headers are set on every response. Signed endpoints
// (logout, status, WebSocket) require valid X-Request-Timestamp and
// X-Request-Signature headers to prevent replay attacks.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Set security headers on all responses.
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' ws: wss:; style-src 'self' 'unsafe-inline'; img-src 'self' data:")

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/dashboard/login":
		// Login is unsigned — the client has no signing key yet.
		h.handleLogin(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/dashboard/logout":
		h.handleLogout(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/dashboard/status":
		h.handleStatus(w, r)
	case strings.HasPrefix(r.URL.Path, "/dashboard/api/"):
		h.routeAdminAPI(w, r)
	case r.URL.Path == "/ws":
		h.handleWebSocket(w, r)
	default:
		h.handleStatic(w, r)
	}
}

// handleLogin creates a deferred-auth session for the given IRC credentials.
// Actual NickServ authentication happens when the WebSocket connects and the
// IRC bridge is established.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Rate limit by IP (strip port to prevent bypass via reconnection).
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr // fallback if no port
	}
	if !h.checkLoginRate(ip) {
		h.jsonResponse(w, http.StatusTooManyRequests, loginResponse{
			Error: "too many login attempts, try again later",
		})
		return
	}

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.jsonResponse(w, http.StatusBadRequest, loginResponse{
			Error: "invalid request body",
		})
		return
	}

	if req.Nick == "" || req.Password == "" {
		h.jsonResponse(w, http.StatusBadRequest, loginResponse{
			Error: "nick and password are required",
		})
		return
	}

	// Verify IRC credentials before creating a session. This connects to
	// the IRC server, registers the nick, and sends NickServ IDENTIFY to
	// confirm the password is valid. This prevents invalid sessions from
	// being created and avoids the error-loop on the dashboard.
	if err := h.verify(
		r.Context(),
		h.ircCfg.Server, h.ircCfg.Port, h.ircCfg.TLS,
		h.cfg.ServerPassword, req.Nick, req.Password,
	); err != nil {
		h.logger.Info("login failed", "nick", req.Nick, "error", err)
		h.jsonResponse(w, http.StatusUnauthorized, loginResponse{
			Error: err.Error(),
		})
		return
	}

	sess, err := h.sessions.Create(req.Nick)
	if err != nil {
		h.logger.Error("failed to create session", "error", err)
		h.jsonResponse(w, http.StatusInternalServerError, loginResponse{
			Error: "internal error",
		})
		return
	}

	// Store the password in the session for use when creating the IRC bridge.
	// This is kept in memory only and never persisted.
	sess.Password = req.Password

	SetCookie(w, r, sess.ID, int(h.sessions.timeout.Seconds()))
	h.jsonResponse(w, http.StatusOK, loginResponse{
		OK:         true,
		Nick:       req.Nick,
		SigningKey: sess.SigningKey,
	})
}

// handleLogout destroys the current session. Requires a valid request signature.
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess := h.sessions.GetFromRequest(r)
	if sess != nil {
		if !h.verifySignature(r, sess, "") {
			h.jsonResponse(w, http.StatusForbidden, loginResponse{
				Error: "invalid or expired request signature",
			})
			return
		}
		h.sessions.Destroy(sess.ID)
	}
	// Clear the cookie regardless.
	SetCookie(w, r, "", -1)
	h.jsonResponse(w, http.StatusOK, loginResponse{OK: true})
}

// handleStatus returns server status information as JSON. Requires a valid
// session cookie and signed request. Returns 503 if no StatusProvider is
// configured.
func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	sess := h.sessions.GetFromRequest(r)
	if sess == nil {
		h.jsonResponse(w, http.StatusUnauthorized, loginResponse{
			Error: "unauthorized",
		})
		return
	}

	if !h.verifySignature(r, sess, "") {
		h.jsonResponse(w, http.StatusForbidden, loginResponse{
			Error: "invalid or expired request signature",
		})
		return
	}

	if h.status == nil {
		h.jsonResponse(w, http.StatusServiceUnavailable, loginResponse{
			Error: "status not available",
		})
		return
	}

	info := h.status.GetStatus()
	h.jsonResponse(w, http.StatusOK, info)
}

// handleWebSocket upgrades the connection and starts the IRC bridge.
// Requires a valid session cookie. Signature is verified from query
// parameters (t=timestamp, s=signature) since the browser WebSocket API
// does not support custom HTTP headers.
func (h *Handler) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	sess := h.sessions.GetFromRequest(r)
	if sess == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// WebSocket connections pass signature via query params because
	// the browser WebSocket API cannot set custom HTTP headers.
	q := r.URL.Query()
	tsStr := q.Get("t")
	sig := q.Get("s")
	if tsStr == "" || sig == "" {
		http.Error(w, "missing signature parameters", http.StatusForbidden)
		return
	}
	// Inject into headers so verifySignature can process them uniformly.
	r.Header.Set(signatureTimestampHeader, tsStr)
	r.Header.Set(signatureHeader, sig)
	// Verify against the base path (without query string). The frontend
	// signs over just the path since the signature itself lives in the query.
	// Temporarily strip the query so RequestURI() returns the bare path.
	origRawQuery := r.URL.RawQuery
	r.URL.RawQuery = ""
	sigOK := h.verifySignature(r, sess, "")
	r.URL.RawQuery = origRawQuery
	if !sigOK {
		http.Error(w, "invalid or expired request signature", http.StatusForbidden)
		return
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Allow same-origin only by default. The Vite dev proxy handles
		// cross-origin during development.
		InsecureSkipVerify: false,
	})
	if err != nil {
		h.logger.Error("WebSocket upgrade failed", "error", err)
		return
	}

	// Determine channels to join — use dashboard config, fall back to main channel.
	channels := h.cfg.Channels
	if len(channels) == 0 && h.ircCfg.Channels.Main != "" {
		channels = []string{h.ircCfg.Channels.Main}
	}

	bridgeCfg := BridgeConfig{
		Nick:           sess.Nick,
		Password:       sess.Password,
		IRCServer:      h.ircCfg.Server,
		IRCPort:        h.ircCfg.Port,
		IRCTLS:         h.ircCfg.TLS,
		ServerPassword: h.cfg.ServerPassword,
		Channels:       channels,
	}

	bridge, err := NewBridge(r.Context(), ws, bridgeCfg, h.logger)
	if err != nil {
		h.logger.Error("failed to create bridge", "error", err)
		ws.Close(websocket.StatusInternalError, "bridge creation failed")
		return
	}

	h.sessions.AttachBridge(sess.ID, bridge)

	// Run the bridge (blocks until disconnected).
	bridge.Run()

	// Clean up bridge reference when done.
	h.sessions.DetachBridge(sess.ID)
}

// handleStatic serves the embedded Vue.js frontend files.
func (h *Handler) handleStatic(w http.ResponseWriter, r *http.Request) {
	// Serve from the embedded dist/ directory.
	distFS, err := fs.Sub(web.DistFS, "dist")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Try to serve the requested file. If it doesn't exist, serve
	// index.html for client-side routing (SPA fallback).
	fileServer := http.FileServer(http.FS(distFS))

	// Sanitize the path to prevent traversal attacks.
	clean := path.Clean(r.URL.Path)
	if clean == "/" || clean == "." {
		clean = "index.html"
	} else {
		clean = strings.TrimPrefix(clean, "/")
	}

	// Reject paths that escape the root.
	if strings.HasPrefix(clean, "..") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Check if the file exists in the embedded FS. If not, serve
	// index.html for SPA client-side routing.
	if _, err := fs.Stat(distFS, clean); err != nil {
		r.URL.Path = "/"
	}

	fileServer.ServeHTTP(w, r)
}

// verifySignature checks the X-Request-Timestamp and X-Request-Signature
// headers against the session's signing key. The signature is
// HMAC-SHA256(signingKey, timestamp + method + path + body). Requests with
// timestamps older than signatureMaxAge are rejected to prevent replay.
// The body parameter should be the raw request body for POST requests,
// or empty string for GET/WebSocket upgrades.
func (h *Handler) verifySignature(r *http.Request, sess *Session, body string) bool {
	tsStr := r.Header.Get(signatureTimestampHeader)
	sig := r.Header.Get(signatureHeader)
	if tsStr == "" || sig == "" {
		return false
	}

	// Validate timestamp freshness.
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return false
	}
	now := time.Now().Unix()
	diff := now - ts
	if diff < 0 {
		diff = -diff
	}
	if diff > int64(signatureMaxAge.Seconds()) {
		h.logger.Debug("request signature expired",
			"age_seconds", diff,
			"max_seconds", int64(signatureMaxAge.Seconds()),
		)
		return false
	}

	// Compute expected signature: HMAC-SHA256(key, timestamp+method+path+body).
	// Use RequestURI (path + query string) to match the frontend's signedFetch,
	// which signs over the full path including query parameters.
	// The signing key is hex-encoded; decode it to use as the raw HMAC key.
	keyBytes, err := hex.DecodeString(sess.SigningKey)
	if err != nil {
		return false
	}
	payload := tsStr + r.Method + r.URL.RequestURI() + body
	return mcrypto.VerifyHMAC(string(keyBytes), sig, []byte(payload))
}

// checkLoginRate returns true if the IP has not exceeded the login rate limit.
func (h *Handler) checkLoginRate(ip string) bool {
	h.rateMu.Lock()
	defer h.rateMu.Unlock()

	now := time.Now()
	cutoff := now.Add(-loginRateWindow)

	// Remove expired entries.
	attempts := h.loginAttempts[ip]
	valid := attempts[:0]
	for _, t := range attempts {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= loginRateLimit {
		h.loginAttempts[ip] = valid
		return false
	}

	h.loginAttempts[ip] = append(valid, now)

	// Evict stale IPs to prevent unbounded map growth.
	if len(h.loginAttempts) > 1000 {
		for k, v := range h.loginAttempts {
			if k == ip {
				continue
			}
			allExpired := true
			for _, t := range v {
				if t.After(cutoff) {
					allExpired = false
					break
				}
			}
			if allExpired {
				delete(h.loginAttempts, k)
			}
		}
	}

	return true
}

// jsonResponse writes a JSON response with the given status code.
func (h *Handler) jsonResponse(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.logger.Error("failed to write JSON response", "error", err)
	}
}
