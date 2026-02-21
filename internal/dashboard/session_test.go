package dashboard

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSessionCreateRetrieve(t *testing.T) {
	t.Parallel()

	store := NewSessionStore(time.Hour, testLogger())

	sess, err := store.Create("testuser")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if sess.Nick != "testuser" {
		t.Errorf("Nick = %q, want %q", sess.Nick, "testuser")
	}
	if sess.ID == "" {
		t.Error("session ID is empty")
	}
	if len(sess.ID) != sessionIDLen*2 { // hex encoding doubles length
		t.Errorf("session ID length = %d, want %d", len(sess.ID), sessionIDLen*2)
	}
	if sess.SigningKey == "" {
		t.Error("SigningKey is empty, want non-empty hex string")
	}
	if len(sess.SigningKey) != signingKeyLen*2 {
		t.Errorf("SigningKey length = %d, want %d", len(sess.SigningKey), signingKeyLen*2)
	}

	// Retrieve by ID.
	got := store.Get(sess.ID)
	if got == nil {
		t.Fatal("Get returned nil for valid session")
	}
	if got.Nick != "testuser" {
		t.Errorf("Get Nick = %q, want %q", got.Nick, "testuser")
	}

	// Unknown ID returns nil.
	if store.Get("nonexistent") != nil {
		t.Error("Get returned non-nil for unknown session ID")
	}
}

func TestSessionExpiry(t *testing.T) {
	t.Parallel()

	// Use a very short timeout.
	store := NewSessionStore(10*time.Millisecond, testLogger())

	sess, err := store.Create("expiring")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Should be retrievable immediately.
	if store.Get(sess.ID) == nil {
		t.Fatal("session should be retrievable immediately after creation")
	}

	// Wait for expiry.
	time.Sleep(20 * time.Millisecond)

	// Should be expired now.
	if store.Get(sess.ID) != nil {
		t.Error("session should have expired")
	}

	// Count should be 0 after expiry-triggered cleanup.
	if store.Count() != 0 {
		t.Errorf("Count = %d, want 0", store.Count())
	}
}

func TestSessionDestroy(t *testing.T) {
	t.Parallel()

	store := NewSessionStore(time.Hour, testLogger())

	sess, err := store.Create("destroyme")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	store.Destroy(sess.ID)

	if store.Get(sess.ID) != nil {
		t.Error("session should be nil after Destroy")
	}
	if store.Count() != 0 {
		t.Errorf("Count = %d, want 0", store.Count())
	}

	// Destroying a nonexistent session should not panic.
	store.Destroy("nonexistent")
}

func TestSessionGetFromRequest(t *testing.T) {
	t.Parallel()

	store := NewSessionStore(time.Hour, testLogger())

	sess, err := store.Create("cookieuser")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Request with valid cookie.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})

	got := store.GetFromRequest(req)
	if got == nil {
		t.Fatal("GetFromRequest returned nil for valid cookie")
	}
	if got.Nick != "cookieuser" {
		t.Errorf("Nick = %q, want %q", got.Nick, "cookieuser")
	}

	// Request without cookie.
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	if store.GetFromRequest(req2) != nil {
		t.Error("GetFromRequest should return nil without cookie")
	}

	// Request with invalid cookie.
	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	req3.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "bogus"})
	if store.GetFromRequest(req3) != nil {
		t.Error("GetFromRequest should return nil for invalid cookie")
	}
}

func TestSessionCount(t *testing.T) {
	t.Parallel()

	store := NewSessionStore(time.Hour, testLogger())

	if store.Count() != 0 {
		t.Errorf("initial Count = %d, want 0", store.Count())
	}

	s1, _ := store.Create("user1")
	s2, _ := store.Create("user2")

	if store.Count() != 2 {
		t.Errorf("Count = %d, want 2", store.Count())
	}

	store.Destroy(s1.ID)
	if store.Count() != 1 {
		t.Errorf("Count = %d, want 1", store.Count())
	}

	store.Destroy(s2.ID)
	if store.Count() != 0 {
		t.Errorf("Count = %d, want 0", store.Count())
	}
}

func TestSetCookie(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	SetCookie(w, r, "test-session-id", 3600)

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}

	c := cookies[0]
	if c.Name != sessionCookieName {
		t.Errorf("cookie name = %q, want %q", c.Name, sessionCookieName)
	}
	if c.Value != "test-session-id" {
		t.Errorf("cookie value = %q, want %q", c.Value, "test-session-id")
	}
	if !c.HttpOnly {
		t.Error("cookie should be HttpOnly")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", c.SameSite)
	}
	if c.MaxAge != 3600 {
		t.Errorf("MaxAge = %d, want 3600", c.MaxAge)
	}
}

func TestSessionCleanup(t *testing.T) {
	t.Parallel()

	store := NewSessionStore(10*time.Millisecond, testLogger())

	_, err := store.Create("cleanup1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = store.Create("cleanup2")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if store.Count() != 2 {
		t.Fatalf("Count = %d, want 2", store.Count())
	}

	// Wait for sessions to expire.
	time.Sleep(20 * time.Millisecond)

	// Manually trigger eviction.
	store.evictExpired()

	if store.Count() != 0 {
		t.Errorf("Count after eviction = %d, want 0", store.Count())
	}
}
