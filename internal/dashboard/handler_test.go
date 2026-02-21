package dashboard

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"murmur/internal/config"
)

func testHandler(t *testing.T) (*Handler, *SessionStore) {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := NewSessionStore(time.Hour, logger)

	cfg := config.DashboardConfig{
		Enabled:        true,
		Listen:         "127.0.0.1:8082",
		SessionTimeout: "1h",
	}
	ircCfg := config.IRCConfig{
		Server: "localhost",
		Port:   6667,
		Channels: config.ChannelsConfig{
			Main: "#murmur",
		},
	}

	h := NewHandler(store, cfg, ircCfg, logger)
	return h, store
}

func TestLoginRateLimit(t *testing.T) {
	t.Parallel()

	h, _ := testHandler(t)

	body := loginRequest{Nick: "ratelimited", Password: "pass"}
	data, _ := json.Marshal(body)

	// Make loginRateLimit requests — all should succeed.
	for i := 0; i < loginRateLimit; i++ {
		req := httptest.NewRequest(http.MethodPost, "/dashboard/login", bytes.NewReader(data))
		req.RemoteAddr = "192.168.1.1:12345"
		w := httptest.NewRecorder()
		h.handleLogin(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want %d", i+1, w.Code, http.StatusOK)
		}
	}

	// Next request should be rate limited.
	req := httptest.NewRequest(http.MethodPost, "/dashboard/login", bytes.NewReader(data))
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()
	h.handleLogin(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("rate limited request: status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}

	// Different IP should not be rate limited.
	req2 := httptest.NewRequest(http.MethodPost, "/dashboard/login", bytes.NewReader(data))
	req2.RemoteAddr = "10.0.0.1:12345"
	w2 := httptest.NewRecorder()
	h.handleLogin(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("different IP: status = %d, want %d", w2.Code, http.StatusOK)
	}
}

func TestLoginValidation(t *testing.T) {
	t.Parallel()

	h, _ := testHandler(t)

	tests := []struct {
		name       string
		body       interface{}
		wantStatus int
		wantError  string
	}{
		{
			name:       "empty nick",
			body:       loginRequest{Nick: "", Password: "pass"},
			wantStatus: http.StatusBadRequest,
			wantError:  "nick and password are required",
		},
		{
			name:       "empty password",
			body:       loginRequest{Nick: "user", Password: ""},
			wantStatus: http.StatusBadRequest,
			wantError:  "nick and password are required",
		},
		{
			name:       "invalid json",
			body:       "not json",
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid request body",
		},
		{
			name:       "valid credentials",
			body:       loginRequest{Nick: "validuser", Password: "validpass"},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var data []byte
			switch v := tt.body.(type) {
			case string:
				data = []byte(v)
			default:
				data, _ = json.Marshal(v)
			}

			req := httptest.NewRequest(http.MethodPost, "/dashboard/login", bytes.NewReader(data))
			w := httptest.NewRecorder()
			h.handleLogin(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}

			if tt.wantError != "" {
				var resp loginResponse
				if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if resp.Error != tt.wantError {
					t.Errorf("error = %q, want %q", resp.Error, tt.wantError)
				}
			}
		})
	}
}

func TestLoginCreatesSession(t *testing.T) {
	t.Parallel()

	h, store := testHandler(t)

	body, _ := json.Marshal(loginRequest{Nick: "sessionuser", Password: "pass"})
	req := httptest.NewRequest(http.MethodPost, "/dashboard/login", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.handleLogin(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Check response.
	var resp loginResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Error("response OK = false, want true")
	}
	if resp.Nick != "sessionuser" {
		t.Errorf("Nick = %q, want %q", resp.Nick, "sessionuser")
	}

	// Check cookie was set.
	cookies := w.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("session cookie not set")
	}

	// Verify session exists in store.
	sess := store.Get(sessionCookie.Value)
	if sess == nil {
		t.Fatal("session not found in store")
	}
	if sess.Nick != "sessionuser" {
		t.Errorf("session Nick = %q, want %q", sess.Nick, "sessionuser")
	}
	if sess.Password != "pass" {
		t.Errorf("session Password = %q, want %q", sess.Password, "pass")
	}
}

func TestLogout(t *testing.T) {
	t.Parallel()

	h, store := testHandler(t)

	// Create a session first.
	sess, _ := store.Create("logoutuser")

	// Logout with valid session cookie.
	req := httptest.NewRequest(http.MethodPost, "/dashboard/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	w := httptest.NewRecorder()
	h.handleLogout(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Session should be destroyed.
	if store.Get(sess.ID) != nil {
		t.Error("session should be destroyed after logout")
	}

	// Cookie should be cleared (MaxAge = -1).
	cookies := w.Result().Cookies()
	for _, c := range cookies {
		if c.Name == sessionCookieName && c.MaxAge != -1 {
			t.Errorf("cookie MaxAge = %d, want -1", c.MaxAge)
		}
	}
}

func TestWebSocketRequiresSession(t *testing.T) {
	t.Parallel()

	h, _ := testHandler(t)

	// Request without session cookie should get 401.
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	w := httptest.NewRecorder()
	h.handleWebSocket(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestStaticFileServing(t *testing.T) {
	t.Parallel()

	h, _ := testHandler(t)

	// Request for root should serve index.html (or the SPA fallback).
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.handleStatic(w, req)

	// Should get a response (either 200 with content or 200 with .gitkeep).
	// The exact content depends on whether the frontend was built.
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()

	h, _ := testHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	tests := []struct {
		header string
		want   string
	}{
		{"X-Frame-Options", "DENY"},
		{"X-Content-Type-Options", "nosniff"},
		{"Content-Security-Policy", "default-src 'self'; connect-src 'self' ws: wss:; style-src 'self' 'unsafe-inline'; img-src 'self' data:"},
	}

	for _, tt := range tests {
		got := w.Header().Get(tt.header)
		if got != tt.want {
			t.Errorf("%s = %q, want %q", tt.header, got, tt.want)
		}
	}
}

func TestRouting(t *testing.T) {
	t.Parallel()

	h, _ := testHandler(t)

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{"login", http.MethodPost, "/dashboard/login", http.StatusBadRequest}, // no body
		{"logout", http.MethodPost, "/dashboard/logout", http.StatusOK},
		{"websocket no session", http.MethodGet, "/ws", http.StatusUnauthorized},
		{"static root", http.MethodGet, "/", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(tt.method, tt.path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}
