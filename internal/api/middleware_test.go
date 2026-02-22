package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRecoverMiddleware_ContentType(t *testing.T) {
	t.Parallel()

	panicking := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("content type test")
	})

	// Use a non-nil logger to exercise the explicit-logger path.
	logger := slog.Default()
	handler := RecoverMiddleware(logger, panicking)

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	// Verify the body is valid JSON with ok=false.
	var resp Response
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if resp.OK {
		t.Error("expected ok=false in panic recovery response")
	}
}

func TestAPIKeyMiddleware_MalformedAuthHeader(t *testing.T) {
	t.Parallel()

	logger := slog.Default()
	handler := APIKeyMiddleware("secret", logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "NotBearer secret")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for malformed auth scheme, got %d", w.Code)
	}

	var resp Response
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.OK {
		t.Error("expected ok=false")
	}
}

func TestAPIKeyMiddleware_EmptyToken(t *testing.T) {
	t.Parallel()

	logger := slog.Default()
	handler := APIKeyMiddleware("secret", logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// "Bearer " with a trailing space but no actual token.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer ")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for empty token, got %d", w.Code)
	}

	var resp Response
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.OK {
		t.Error("expected ok=false")
	}
}
