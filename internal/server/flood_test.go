package server

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func newTestFloodGuard() *floodGuard {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return newFloodGuard(logger)
}

func TestFloodGuard_AllowUnderLimit(t *testing.T) {
	t.Parallel()
	fg := newTestFloodGuard()

	// Should allow up to floodMaxPerWindow messages.
	for i := 0; i < floodMaxPerWindow; i++ {
		if !fg.allow("alice") {
			t.Fatalf("message %d should be allowed", i)
		}
	}
}

func TestFloodGuard_BlockOverLimit(t *testing.T) {
	t.Parallel()
	fg := newTestFloodGuard()

	// Exhaust the limit.
	for i := 0; i < floodMaxPerWindow; i++ {
		fg.allow("bob")
	}

	// Next message should be blocked.
	if fg.allow("bob") {
		t.Fatal("message should be blocked after exceeding rate limit")
	}
}

func TestFloodGuard_DifferentNicksIndependent(t *testing.T) {
	t.Parallel()
	fg := newTestFloodGuard()

	// Exhaust alice's limit.
	for i := 0; i < floodMaxPerWindow; i++ {
		fg.allow("alice")
	}

	// Bob should still be allowed.
	if !fg.allow("bob") {
		t.Fatal("bob should not be affected by alice's rate limit")
	}
}

func TestFloodGuard_CooldownExpires(t *testing.T) {
	t.Parallel()
	fg := newTestFloodGuard()

	// Trigger cooldown by exceeding the limit.
	for i := 0; i < floodMaxPerWindow; i++ {
		fg.allow("carol")
	}
	if fg.allow("carol") {
		t.Fatal("should be in cooldown")
	}

	// Manually expire the cooldown for testing.
	fg.mu.Lock()
	fg.nicks["carol"].cooldownAt = time.Now().Add(-1 * time.Second)
	fg.mu.Unlock()

	if !fg.allow("carol") {
		t.Fatal("should be allowed after cooldown expires")
	}
}

func TestFloodGuard_EnqueueAndProcess(t *testing.T) {
	t.Parallel()
	fg := newTestFloodGuard()

	var mu sync.Mutex
	var processed []string

	handler := func(m pendingMsg) {
		mu.Lock()
		processed = append(processed, m.nick)
		mu.Unlock()
	}

	msg := pendingMsg{channel: "#test", nick: "dave", message: "hello"}
	if !fg.enqueue(msg, handler) {
		t.Fatal("enqueue should succeed")
	}

	// Give the worker goroutine time to process.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(processed) != 1 || processed[0] != "dave" {
		t.Fatalf("expected [dave], got %v", processed)
	}
}

func TestFloodGuard_QueueFull(t *testing.T) {
	t.Parallel()
	fg := newTestFloodGuard()

	// Use a blocking handler so messages pile up in the queue.
	block := make(chan struct{})
	handler := func(m pendingMsg) {
		<-block // block until released
	}

	// Fill the queue: 1 message is being processed (blocking), then
	// channelQueueSize more fill the buffer.
	msg := pendingMsg{channel: "#test", nick: "eve", message: "msg"}
	fg.enqueue(msg, handler) // this one starts processing (blocks)
	time.Sleep(10 * time.Millisecond)

	for i := 0; i < channelQueueSize; i++ {
		if !fg.enqueue(pendingMsg{channel: "#test", nick: "eve", message: "fill"}, handler) {
			t.Fatalf("enqueue %d should succeed (buffer not full yet)", i)
		}
	}

	// Next enqueue should fail — queue is full.
	if fg.enqueue(pendingMsg{channel: "#test", nick: "eve", message: "overflow"}, handler) {
		t.Fatal("enqueue should fail when queue is full")
	}

	close(block) // unblock the handler
}

func TestFloodGuard_Flush(t *testing.T) {
	t.Parallel()
	fg := newTestFloodGuard()

	// Use a blocking handler so messages pile up.
	block := make(chan struct{})
	handler := func(m pendingMsg) {
		<-block
	}

	msg := pendingMsg{channel: "#test", nick: "frank", message: "msg"}
	fg.enqueue(msg, handler) // starts processing (blocks)
	time.Sleep(10 * time.Millisecond)

	// Fill the buffer.
	for i := 0; i < channelQueueSize; i++ {
		fg.enqueue(pendingMsg{channel: "#test", nick: "frank", message: "queued"}, handler)
	}

	// Flush should drain the queued messages.
	dropped := fg.flush("#test")
	if dropped != channelQueueSize {
		t.Fatalf("expected %d dropped, got %d", channelQueueSize, dropped)
	}

	// Flush again should return 0.
	if fg.flush("#test") != 0 {
		t.Fatal("second flush should return 0")
	}

	close(block)
}

func TestFloodGuard_FlushUnknownChannel(t *testing.T) {
	t.Parallel()
	fg := newTestFloodGuard()

	if fg.flush("#nonexistent") != 0 {
		t.Fatal("flush of unknown channel should return 0")
	}
}

func TestFloodGuard_Close(t *testing.T) {
	t.Parallel()
	fg := newTestFloodGuard()

	done := make(chan struct{})
	handler := func(m pendingMsg) {
		close(done)
	}

	// Enqueue a message so a worker goroutine is created.
	msg := pendingMsg{channel: "#test", nick: "grace", message: "hello"}
	if !fg.enqueue(msg, handler) {
		t.Fatal("enqueue should succeed before close")
	}

	// Wait for the worker to process the message.
	<-done

	// Close the flood guard.
	fg.Close()

	// Enqueue after close should return false (not panic).
	if fg.enqueue(pendingMsg{channel: "#test", nick: "grace", message: "after"}, handler) {
		t.Fatal("enqueue should fail after close")
	}

	// Flush after close should return 0.
	if fg.flush("#test") != 0 {
		t.Fatal("flush after close should return 0")
	}
}

func TestFloodGuard_CloseIdempotent(t *testing.T) {
	t.Parallel()
	fg := newTestFloodGuard()

	// Close without any channels should not panic.
	fg.Close()
	fg.Close() // second close should also be safe
}
