package dashboard

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// testSignRequest adds valid X-Request-Timestamp and X-Request-Signature
// headers to a request using the session's signing key.
func testSignRequest(t *testing.T, r *http.Request, sess *Session, body string) {
	t.Helper()

	ts := fmt.Sprintf("%d", time.Now().Unix())
	r.Header.Set(signatureTimestampHeader, ts)

	keyBytes, err := hex.DecodeString(sess.SigningKey)
	if err != nil {
		t.Fatalf("decode signing key: %v", err)
	}
	mac := hmac.New(sha256.New, keyBytes)
	mac.Write([]byte(ts + r.Method + r.URL.Path + body))
	sig := hex.EncodeToString(mac.Sum(nil))
	r.Header.Set(signatureHeader, sig)
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
	if resp.SigningKey == "" {
		t.Error("SigningKey is empty, want non-empty hex string")
	}
	if len(resp.SigningKey) != signingKeyLen*2 {
		t.Errorf("SigningKey length = %d, want %d", len(resp.SigningKey), signingKeyLen*2)
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
	if sess.SigningKey != resp.SigningKey {
		t.Errorf("session SigningKey = %q, want %q", sess.SigningKey, resp.SigningKey)
	}
}

func TestLogout(t *testing.T) {
	t.Parallel()

	h, store := testHandler(t)

	// Create a session first.
	sess, _ := store.Create("logoutuser")

	// Logout with valid session cookie and signature.
	req := httptest.NewRequest(http.MethodPost, "/dashboard/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	testSignRequest(t, req, sess, "")
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

func TestLogoutRejectsUnsignedRequest(t *testing.T) {
	t.Parallel()

	h, store := testHandler(t)
	sess, _ := store.Create("unsigneduser")

	// Logout without signature headers should be rejected.
	req := httptest.NewRequest(http.MethodPost, "/dashboard/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	w := httptest.NewRecorder()
	h.handleLogout(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}

	// Session should NOT be destroyed.
	if store.Get(sess.ID) == nil {
		t.Error("session should still exist after unsigned logout attempt")
	}
}

func TestSignatureVerification(t *testing.T) {
	t.Parallel()

	h, store := testHandler(t)
	sess, _ := store.Create("siguser")

	tests := []struct {
		name      string
		timestamp string
		signature string
		wantValid bool
	}{
		{
			name:      "valid signature",
			timestamp: fmt.Sprintf("%d", time.Now().Unix()),
			wantValid: true,
		},
		{
			name:      "missing timestamp",
			timestamp: "",
			signature: "deadbeef",
			wantValid: false,
		},
		{
			name:      "missing signature",
			timestamp: fmt.Sprintf("%d", time.Now().Unix()),
			signature: "",
			wantValid: false,
		},
		{
			name:      "expired timestamp",
			timestamp: fmt.Sprintf("%d", time.Now().Add(-time.Minute).Unix()),
			wantValid: false,
		},
		{
			name:      "future timestamp beyond window",
			timestamp: fmt.Sprintf("%d", time.Now().Add(time.Minute).Unix()),
			wantValid: false,
		},
		{
			name:      "wrong signature",
			timestamp: fmt.Sprintf("%d", time.Now().Unix()),
			signature: "0000000000000000000000000000000000000000000000000000000000000000",
			wantValid: false,
		},
		{
			name:      "invalid timestamp format",
			timestamp: "not-a-number",
			signature: "deadbeef",
			wantValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/dashboard/logout", nil)

			if tt.timestamp != "" {
				req.Header.Set(signatureTimestampHeader, tt.timestamp)
			}

			if tt.signature != "" {
				req.Header.Set(signatureHeader, tt.signature)
			} else if tt.wantValid {
				// Compute valid signature for the "valid" test case.
				keyBytes, _ := hex.DecodeString(sess.SigningKey)
				mac := hmac.New(sha256.New, keyBytes)
				mac.Write([]byte(tt.timestamp + req.Method + req.URL.Path))
				req.Header.Set(signatureHeader, hex.EncodeToString(mac.Sum(nil)))
			}

			got := h.verifySignature(req, sess, "")
			if got != tt.wantValid {
				t.Errorf("verifySignature = %v, want %v", got, tt.wantValid)
			}
		})
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

func TestWebSocketRequiresSignature(t *testing.T) {
	t.Parallel()

	h, store := testHandler(t)
	sess, _ := store.Create("wsuser")

	// Request with session cookie but no signature query params should get 403.
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	w := httptest.NewRecorder()
	h.handleWebSocket(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestWebSocketAcceptsQuerySignature(t *testing.T) {
	t.Parallel()

	h, store := testHandler(t)
	sess, _ := store.Create("wssiguser")

	// Build a signed WebSocket URL with query params.
	ts := fmt.Sprintf("%d", time.Now().Unix())
	keyBytes, _ := hex.DecodeString(sess.SigningKey)
	mac := hmac.New(sha256.New, keyBytes)
	// Signature is computed against the base path "/ws", not the full URL with query.
	mac.Write([]byte(ts + http.MethodGet + "/ws"))
	sig := hex.EncodeToString(mac.Sum(nil))

	url := fmt.Sprintf("/ws?t=%s&s=%s", ts, sig)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	w := httptest.NewRecorder()
	h.handleWebSocket(w, req)

	// The WebSocket upgrade will fail (not a real WS connection in tests),
	// but we should NOT get 401 or 403 — the auth check should pass.
	// websocket.Accept will fail with a non-WS request, returning 200
	// with an error body or similar. The key assertion is: not 401/403.
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Errorf("status = %d, want neither 401 nor 403 (auth should pass)", w.Code)
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
		{"logout no session", http.MethodPost, "/dashboard/logout", http.StatusOK},
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
