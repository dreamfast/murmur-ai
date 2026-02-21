package server

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsIdentified_Cached(t *testing.T) {
	t.Parallel()

	var whoisCalls atomic.Int32
	whois := func(nick string) (string, error) {
		whoisCalls.Add(1)
		return "myaccount", nil
	}

	v := NewNickServVerifier(whois, 5*time.Minute, testLogger())

	// First call should perform WHOIS.
	if !v.IsIdentified("User1") {
		t.Error("expected User1 to be identified")
	}
	if whoisCalls.Load() != 1 {
		t.Errorf("expected 1 WHOIS call, got %d", whoisCalls.Load())
	}

	// Second call should use cache (case-insensitive).
	if !v.IsIdentified("user1") {
		t.Error("expected user1 to be identified (cached)")
	}
	if whoisCalls.Load() != 1 {
		t.Errorf("expected still 1 WHOIS call (cached), got %d", whoisCalls.Load())
	}
}

func TestIsIdentified_Expired(t *testing.T) {
	t.Parallel()

	var whoisCalls atomic.Int32
	whois := func(nick string) (string, error) {
		whoisCalls.Add(1)
		return "myaccount", nil
	}

	// Use a very short TTL so the cache expires immediately.
	v := NewNickServVerifier(whois, 1*time.Millisecond, testLogger())

	if !v.IsIdentified("user1") {
		t.Error("expected user1 to be identified")
	}
	if whoisCalls.Load() != 1 {
		t.Errorf("expected 1 WHOIS call, got %d", whoisCalls.Load())
	}

	// Wait for cache to expire.
	time.Sleep(5 * time.Millisecond)

	// Should perform another WHOIS.
	if !v.IsIdentified("user1") {
		t.Error("expected user1 to be identified after cache expiry")
	}
	if whoisCalls.Load() != 2 {
		t.Errorf("expected 2 WHOIS calls after expiry, got %d", whoisCalls.Load())
	}
}

func TestIsIdentified_NotIdentified(t *testing.T) {
	t.Parallel()

	whois := func(nick string) (string, error) {
		return "", nil // empty account = not identified
	}

	v := NewNickServVerifier(whois, 5*time.Minute, testLogger())

	if v.IsIdentified("stranger") {
		t.Error("expected stranger to NOT be identified")
	}
}

func TestIsIdentified_WhoisError(t *testing.T) {
	t.Parallel()

	whois := func(nick string) (string, error) {
		return "", fmt.Errorf("connection timeout")
	}

	v := NewNickServVerifier(whois, 5*time.Minute, testLogger())

	// Errors should fail-closed (deny access).
	if v.IsIdentified("user1") {
		t.Error("expected fail-closed on WHOIS error")
	}
}

func TestIsIdentified_NoCaching(t *testing.T) {
	t.Parallel()

	var whoisCalls atomic.Int32
	whois := func(nick string) (string, error) {
		whoisCalls.Add(1)
		return "myaccount", nil
	}

	// TTL = 0 disables caching.
	v := NewNickServVerifier(whois, 0, testLogger())

	v.IsIdentified("user1")
	v.IsIdentified("user1")
	v.IsIdentified("user1")

	if whoisCalls.Load() != 3 {
		t.Errorf("expected 3 WHOIS calls (no caching), got %d", whoisCalls.Load())
	}
}

func TestInvalidateCache(t *testing.T) {
	t.Parallel()

	var whoisCalls atomic.Int32
	whois := func(nick string) (string, error) {
		whoisCalls.Add(1)
		return "myaccount", nil
	}

	v := NewNickServVerifier(whois, 5*time.Minute, testLogger())

	// Populate cache.
	v.IsIdentified("user1")
	v.IsIdentified("user2")
	if whoisCalls.Load() != 2 {
		t.Fatalf("expected 2 WHOIS calls, got %d", whoisCalls.Load())
	}

	// Invalidate specific nick.
	v.InvalidateCache("user1")

	// user1 should require a new WHOIS, user2 should still be cached.
	v.IsIdentified("user1")
	v.IsIdentified("user2")
	if whoisCalls.Load() != 3 {
		t.Errorf("expected 3 WHOIS calls after invalidating user1, got %d", whoisCalls.Load())
	}
}

func TestInvalidateCache_All(t *testing.T) {
	t.Parallel()

	var whoisCalls atomic.Int32
	whois := func(nick string) (string, error) {
		whoisCalls.Add(1)
		return "myaccount", nil
	}

	v := NewNickServVerifier(whois, 5*time.Minute, testLogger())

	// Populate cache.
	v.IsIdentified("user1")
	v.IsIdentified("user2")

	// Invalidate all.
	v.InvalidateCache("")

	// Both should require new WHOIS.
	v.IsIdentified("user1")
	v.IsIdentified("user2")
	if whoisCalls.Load() != 4 {
		t.Errorf("expected 4 WHOIS calls after full invalidation, got %d", whoisCalls.Load())
	}
}

func TestIsIdentified_CaseInsensitive(t *testing.T) {
	t.Parallel()

	var whoisCalls atomic.Int32
	whois := func(nick string) (string, error) {
		whoisCalls.Add(1)
		return "myaccount", nil
	}

	v := NewNickServVerifier(whois, 5*time.Minute, testLogger())

	// Different cases should share the same cache entry.
	v.IsIdentified("User1")
	v.IsIdentified("USER1")
	v.IsIdentified("user1")

	if whoisCalls.Load() != 1 {
		t.Errorf("expected 1 WHOIS call (case-insensitive cache), got %d", whoisCalls.Load())
	}
}

func TestIsIdentified_Singleflight(t *testing.T) {
	t.Parallel()

	var whoisCalls atomic.Int32
	// Slow WHOIS to ensure concurrent callers overlap.
	whois := func(nick string) (string, error) {
		whoisCalls.Add(1)
		time.Sleep(50 * time.Millisecond)
		return "myaccount", nil
	}

	// Disable caching so singleflight is the only dedup mechanism.
	v := NewNickServVerifier(whois, 0, testLogger())

	// Launch 5 concurrent lookups for the same nick.
	var wg sync.WaitGroup
	results := make([]bool, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = v.IsIdentified("user1")
		}(i)
	}
	wg.Wait()

	// All should return true.
	for i, r := range results {
		if !r {
			t.Errorf("goroutine %d: expected identified=true", i)
		}
	}

	// Only 1 WHOIS call should have been made (singleflight dedup).
	if whoisCalls.Load() != 1 {
		t.Errorf("expected 1 WHOIS call (singleflight), got %d", whoisCalls.Load())
	}
}
