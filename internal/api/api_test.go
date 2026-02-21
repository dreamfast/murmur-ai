package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJSONResponse_Success(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		data   any
	}{
		{
			name:   "200 with map data",
			status: http.StatusOK,
			data:   map[string]string{"key": "value"},
		},
		{
			name:   "201 with string data",
			status: http.StatusCreated,
			data:   "created",
		},
		{
			name:   "202 accepted",
			status: http.StatusAccepted,
			data:   map[string]int64{"id": 42},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			JSONResponse(w, tt.status, tt.data)

			if w.Code != tt.status {
				t.Errorf("expected status %d, got %d", tt.status, w.Code)
			}

			ct := w.Header().Get("Content-Type")
			if ct != "application/json" {
				t.Errorf("expected Content-Type application/json, got %q", ct)
			}

			var resp Response
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if !resp.OK {
				t.Error("expected ok=true")
			}
			if resp.Error != "" {
				t.Errorf("expected empty error, got %q", resp.Error)
			}
		})
	}
}

func TestJSONResponse_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		data      any
		wantError string
	}{
		{
			name:      "400 bad request",
			status:    http.StatusBadRequest,
			data:      "missing required field",
			wantError: "missing required field",
		},
		{
			name:      "401 unauthorized",
			status:    http.StatusUnauthorized,
			data:      "invalid api key",
			wantError: "invalid api key",
		},
		{
			name:      "500 with non-string data",
			status:    http.StatusInternalServerError,
			data:      42,
			wantError: "internal server error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			JSONResponse(w, tt.status, tt.data)

			if w.Code != tt.status {
				t.Errorf("expected status %d, got %d", tt.status, w.Code)
			}

			var resp Response
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if resp.OK {
				t.Error("expected ok=false")
			}
			if resp.Error != tt.wantError {
				t.Errorf("expected error %q, got %q", tt.wantError, resp.Error)
			}
		})
	}
}

func TestJSONResponse_NilData(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	JSONResponse(w, http.StatusOK, nil)

	var resp Response
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !resp.OK {
		t.Error("expected ok=true")
	}
}

func TestAPIKeyMiddleware_ValidKey(t *testing.T) {
	t.Parallel()

	handler := APIKeyMiddleware("test-secret")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAPIKeyMiddleware_CaseInsensitiveScheme(t *testing.T) {
	t.Parallel()

	handler := APIKeyMiddleware("test-secret")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// RFC 7235: auth-scheme is case-insensitive.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "bearer test-secret")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for lowercase 'bearer', got %d", w.Code)
	}
}

func TestAPIKeyMiddleware_InvalidKey(t *testing.T) {
	t.Parallel()

	handler := APIKeyMiddleware("test-secret")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}

	var resp Response
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.OK {
		t.Error("expected ok=false")
	}
}

func TestAPIKeyMiddleware_MissingHeader(t *testing.T) {
	t.Parallel()

	handler := APIKeyMiddleware("test-secret")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAPIKeyMiddleware_EmptyKeyBypass(t *testing.T) {
	t.Parallel()

	handler := APIKeyMiddleware("")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// No Authorization header — should still pass through.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (bypass), got %d", w.Code)
	}
}

func TestRecoverMiddleware(t *testing.T) {
	t.Parallel()

	// Handler that panics.
	panicking := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("test panic")
	})

	handler := RecoverMiddleware(panicking)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	// Should not panic — middleware recovers.
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}

	var resp Response
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.OK {
		t.Error("expected ok=false")
	}
	if resp.Error != "internal server error" {
		t.Errorf("expected error 'internal server error', got %q", resp.Error)
	}
}

func TestRecoverMiddleware_NoPanic(t *testing.T) {
	t.Parallel()

	// Handler that does not panic.
	normal := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := RecoverMiddleware(normal)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAPIKeyMiddlewareWithUserKeys_PerUserKey(t *testing.T) {
	t.Parallel()

	resolver := func(key string) string {
		if key == "user-key-alice" {
			return "alice"
		}
		return ""
	}

	var capturedNick string
	handler := APIKeyMiddlewareWithUserKeys("global-key", resolver)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedNick = AuthNick(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer user-key-alice")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if capturedNick != "alice" {
		t.Errorf("expected nick 'alice', got %q", capturedNick)
	}
}

func TestAPIKeyMiddlewareWithUserKeys_GlobalKeyFallback(t *testing.T) {
	t.Parallel()

	resolver := func(key string) string { return "" } // no per-user match

	var capturedNick string
	handler := APIKeyMiddlewareWithUserKeys("global-key", resolver)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedNick = AuthNick(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer global-key")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if capturedNick != "" {
		t.Errorf("expected empty nick for global key, got %q", capturedNick)
	}
}

func TestAPIKeyMiddlewareWithUserKeys_InvalidKey(t *testing.T) {
	t.Parallel()

	resolver := func(key string) string { return "" }

	handler := APIKeyMiddlewareWithUserKeys("global-key", resolver)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAPIKeyMiddlewareWithUserKeys_NilResolver(t *testing.T) {
	t.Parallel()

	// With nil resolver, should behave like the original middleware.
	handler := APIKeyMiddlewareWithUserKeys("global-key", nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer global-key")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestAuthNick_EmptyContext(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	nick := AuthNick(req.Context())
	if nick != "" {
		t.Errorf("expected empty nick, got %q", nick)
	}
}

func TestNewHTTPServer(t *testing.T) {
	t.Parallel()

	logger := slog.Default()
	mux := http.NewServeMux()
	srv := NewHTTPServer("127.0.0.1:0", mux, logger)

	if srv.Addr != "127.0.0.1:0" {
		t.Errorf("expected addr 127.0.0.1:0, got %q", srv.Addr)
	}
	if srv.ReadTimeout == 0 {
		t.Error("expected non-zero ReadTimeout")
	}
	if srv.WriteTimeout == 0 {
		t.Error("expected non-zero WriteTimeout")
	}
	if srv.IdleTimeout == 0 {
		t.Error("expected non-zero IdleTimeout")
	}
	if srv.Handler != mux {
		t.Error("expected handler to be the provided mux")
	}
}
