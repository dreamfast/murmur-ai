package dashboard

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"murmur/internal/config"
	"murmur/web"
)

const (
	// loginRateLimit is the maximum login attempts per IP per minute.
	loginRateLimit = 5
	// loginRateWindow is the sliding window for login rate limiting.
	loginRateWindow = time.Minute
)

// loginRequest is the JSON body for POST /dashboard/login.
type loginRequest struct {
	Nick     string `json:"nick"`
	Password string `json:"password"`
}

// loginResponse is the JSON body returned from POST /dashboard/login.
type loginResponse struct {
	OK    bool   `json:"ok"`
	Nick  string `json:"nick,omitempty"`
	Error string `json:"error,omitempty"`
}

// Handler serves the dashboard HTTP endpoints and static files.
type Handler struct {
	sessions *SessionStore
	cfg      config.DashboardConfig
	ircCfg   config.IRCConfig
	logger   *slog.Logger

	// rateMu protects loginAttempts for per-IP rate limiting.
	rateMu        sync.Mutex
	loginAttempts map[string][]time.Time
}

// NewHandler creates a dashboard HTTP handler.
func NewHandler(sessions *SessionStore, cfg config.DashboardConfig, ircCfg config.IRCConfig, logger *slog.Logger) *Handler {
	return &Handler{
		sessions:      sessions,
		cfg:           cfg,
		ircCfg:        ircCfg,
		logger:        logger.With("component", "dashboard"),
		loginAttempts: make(map[string][]time.Time),
	}
}

// ServeHTTP implements http.Handler and routes requests to the appropriate
// endpoint. Security headers are set on every response.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Set security headers on all responses.
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' ws: wss:; style-src 'self' 'unsafe-inline'; img-src 'self' data:")

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/dashboard/login":
		h.handleLogin(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/dashboard/logout":
		h.handleLogout(w, r)
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

	// Credential verification is deferred to the IRC connection phase.
	// When the WebSocket connects, the bridge creates an IRC connection
	// and sends NickServ IDENTIFY with the stored password. If auth fails,
	// the IRC server or NickServ will reject the connection and the bridge
	// reports the error to the client. This avoids blocking the login
	// endpoint on a full IRC handshake.
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
	h.jsonResponse(w, http.StatusOK, loginResponse{OK: true, Nick: req.Nick})
}

// handleLogout destroys the current session.
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess := h.sessions.GetFromRequest(r)
	if sess != nil {
		h.sessions.Destroy(sess.ID)
	}
	// Clear the cookie regardless.
	SetCookie(w, r, "", -1)
	h.jsonResponse(w, http.StatusOK, loginResponse{OK: true})
}

// handleWebSocket upgrades the connection and starts the IRC bridge.
func (h *Handler) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	sess := h.sessions.GetFromRequest(r)
	if sess == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
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

	// Determine channels to join — use the server's main channel.
	var channels []string
	if h.ircCfg.Channels.Main != "" {
		channels = append(channels, h.ircCfg.Channels.Main)
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
