package server

import (
	"log/slog"
	"sync"
	"testing"
	"time"
)

// flushed records a single flush event from the debouncer.
type flushed struct {
	channel string
	nick    string
	message string
}

// testDebouncer creates a debouncer with a controllable timer for testing.
// It returns the debouncer, a channel that receives flush events, and a
// channel that receives fire-control channels (one per afterFunc call).
// Sending on a fire-control channel triggers the corresponding timer callback.
func testDebouncer(t *testing.T, window time.Duration) (*messageDebouncer, chan flushed, chan chan struct{}) {
	t.Helper()

	flushCh := make(chan flushed, 10)
	// fireCh receives a channel for each afterFunc call. Sending on that
	// inner channel triggers the callback.
	fireCh := make(chan chan struct{}, 10)

	flushFn := func(channel, nick, message string) {
		flushCh <- flushed{channel: channel, nick: nick, message: message}
	}

	d := newMessageDebouncer(window, flushFn, slog.Default())

	// Replace the afterFunc factory with one we control.
	d.afterFunc = func(_ time.Duration, f func()) *time.Timer {
		fire := make(chan struct{}, 1)
		fireCh <- fire

		// Create a stopped timer (we don't use its channel).
		timer := time.NewTimer(24 * time.Hour)

		// Start a goroutine that calls f when we signal.
		go func() {
			<-fire
			f()
		}()

		return timer
	}

	return d, flushCh, fireCh
}

func TestDebouncer_Disabled(t *testing.T) {
	t.Parallel()

	flushCh := make(chan flushed, 10)
	d := newMessageDebouncer(0, func(ch, nick, msg string) {
		flushCh <- flushed{ch, nick, msg}
	}, slog.Default())
	defer d.Close()

	d.Add("#test", "alice", "line 1")
	d.Add("#test", "alice", "line 2")

	// Both should flush immediately as separate messages.
	f1 := <-flushCh
	f2 := <-flushCh

	if f1.message != "line 1" {
		t.Errorf("expected 'line 1', got %q", f1.message)
	}
	if f2.message != "line 2" {
		t.Errorf("expected 'line 2', got %q", f2.message)
	}
}

func TestDebouncer_DisabledAddAfterClose(t *testing.T) {
	t.Parallel()

	flushCh := make(chan flushed, 10)
	d := newMessageDebouncer(0, func(ch, nick, msg string) {
		flushCh <- flushed{ch, nick, msg}
	}, slog.Default())

	d.Close()
	d.Add("#test", "alice", "should be dropped")

	// Give a moment for any unexpected flush.
	time.Sleep(50 * time.Millisecond)
	select {
	case f := <-flushCh:
		t.Errorf("expected no flush after close in disabled mode, got %+v", f)
	default:
		// Good — no flush.
	}
}

func TestDebouncer_SingleMessage(t *testing.T) {
	t.Parallel()

	d, flushCh, fireCh := testDebouncer(t, 2*time.Second)
	defer d.Close()

	d.Add("#test", "alice", "hello")

	// Fire the timer.
	fire := <-fireCh
	fire <- struct{}{}

	f := <-flushCh
	if f.channel != "#test" {
		t.Errorf("expected channel '#test', got %q", f.channel)
	}
	if f.nick != "alice" {
		t.Errorf("expected nick 'alice', got %q", f.nick)
	}
	if f.message != "hello" {
		t.Errorf("expected 'hello', got %q", f.message)
	}
}

func TestDebouncer_MultiLineConcat(t *testing.T) {
	t.Parallel()

	d, flushCh, fireCh := testDebouncer(t, 2*time.Second)
	defer d.Close()

	d.Add("#test", "alice", "line 1")
	// First timer created — drain it (it will be stopped when we add line 2).
	<-fireCh

	d.Add("#test", "alice", "line 2")
	// Second timer created.
	<-fireCh

	d.Add("#test", "alice", "line 3")
	// Third timer created.
	fire := <-fireCh
	fire <- struct{}{}

	f := <-flushCh
	if f.message != "line 1\nline 2\nline 3" {
		t.Errorf("expected concatenated lines, got %q", f.message)
	}
}

func TestDebouncer_DifferentNicks(t *testing.T) {
	t.Parallel()

	d, flushCh, fireCh := testDebouncer(t, 2*time.Second)
	defer d.Close()

	d.Add("#test", "alice", "hello from alice")
	fireAlice := <-fireCh

	d.Add("#test", "bob", "hello from bob")
	fireBob := <-fireCh

	// Fire both.
	fireAlice <- struct{}{}
	fireBob <- struct{}{}

	results := make(map[string]string)
	for i := 0; i < 2; i++ {
		f := <-flushCh
		results[f.nick] = f.message
	}

	if results["alice"] != "hello from alice" {
		t.Errorf("alice: expected 'hello from alice', got %q", results["alice"])
	}
	if results["bob"] != "hello from bob" {
		t.Errorf("bob: expected 'hello from bob', got %q", results["bob"])
	}
}

func TestDebouncer_DifferentChannels(t *testing.T) {
	t.Parallel()

	d, flushCh, fireCh := testDebouncer(t, 2*time.Second)
	defer d.Close()

	d.Add("#chan1", "alice", "msg in chan1")
	fireChan1 := <-fireCh

	d.Add("#chan2", "alice", "msg in chan2")
	fireChan2 := <-fireCh

	fireChan1 <- struct{}{}
	fireChan2 <- struct{}{}

	results := make(map[string]string)
	for i := 0; i < 2; i++ {
		f := <-flushCh
		results[f.channel] = f.message
	}

	if results["#chan1"] != "msg in chan1" {
		t.Errorf("chan1: expected 'msg in chan1', got %q", results["#chan1"])
	}
	if results["#chan2"] != "msg in chan2" {
		t.Errorf("chan2: expected 'msg in chan2', got %q", results["#chan2"])
	}
}

func TestDebouncer_MaxLines(t *testing.T) {
	t.Parallel()

	d, flushCh, fireCh := testDebouncer(t, 2*time.Second)
	defer d.Close()

	// Send exactly debounceMaxLines messages. The batch should flush
	// immediately on the last one without waiting for the timer.
	for i := 0; i < debounceMaxLines-1; i++ {
		d.Add("#test", "alice", "line")
		<-fireCh // drain timer creation
	}

	// The last line triggers immediate flush — no new timer created.
	d.Add("#test", "alice", "last line")

	f := <-flushCh
	// Should be debounceMaxLines lines joined by \n.
	lines := 1
	for _, c := range f.message {
		if c == '\n' {
			lines++
		}
	}
	if lines != debounceMaxLines {
		t.Errorf("expected %d lines, got %d", debounceMaxLines, lines)
	}
}

func TestDebouncer_MaxLinesThenNewBatch(t *testing.T) {
	t.Parallel()

	d, flushCh, fireCh := testDebouncer(t, 2*time.Second)
	defer d.Close()

	// Fill first batch to max.
	for i := 0; i < debounceMaxLines-1; i++ {
		d.Add("#test", "alice", "batch1")
		<-fireCh
	}
	d.Add("#test", "alice", "batch1-last")

	f1 := <-flushCh
	if f1.nick != "alice" {
		t.Errorf("expected nick 'alice', got %q", f1.nick)
	}

	// New message starts a fresh batch.
	d.Add("#test", "alice", "batch2-first")
	fire := <-fireCh
	fire <- struct{}{}

	f2 := <-flushCh
	if f2.message != "batch2-first" {
		t.Errorf("expected 'batch2-first', got %q", f2.message)
	}
}

func TestDebouncer_CloseFlushes(t *testing.T) {
	t.Parallel()

	d, flushCh, fireCh := testDebouncer(t, 2*time.Second)

	d.Add("#test", "alice", "pending line 1")
	<-fireCh
	d.Add("#test", "alice", "pending line 2")
	<-fireCh

	// Close should flush the pending batch.
	d.Close()

	f := <-flushCh
	if f.message != "pending line 1\npending line 2" {
		t.Errorf("expected concatenated pending lines, got %q", f.message)
	}
}

func TestDebouncer_CloseIdempotent(t *testing.T) {
	t.Parallel()

	d, _, _ := testDebouncer(t, 2*time.Second)
	d.Close()
	d.Close() // should not panic
}

func TestDebouncer_AddAfterClose(t *testing.T) {
	t.Parallel()

	flushCh := make(chan flushed, 10)
	d := newMessageDebouncer(2*time.Second, func(ch, nick, msg string) {
		flushCh <- flushed{ch, nick, msg}
	}, slog.Default())

	d.Close()
	d.Add("#test", "alice", "should be dropped")

	// Give a moment for any unexpected flush.
	time.Sleep(50 * time.Millisecond)
	select {
	case f := <-flushCh:
		t.Errorf("expected no flush after close, got %+v", f)
	default:
		// Good — no flush.
	}
}

func TestDebouncer_StaleTimerAfterClose(t *testing.T) {
	t.Parallel()

	// Verify that a timer callback that fires after Close() does not
	// double-flush. The batch should already be flushed by Close().
	d, flushCh, fireCh := testDebouncer(t, 2*time.Second)

	d.Add("#test", "alice", "will be flushed by close")
	staleTimer := <-fireCh

	// Close flushes the batch.
	d.Close()
	f := <-flushCh
	if f.message != "will be flushed by close" {
		t.Errorf("expected close flush, got %q", f.message)
	}

	// Now fire the stale timer — it should be a no-op since the batch
	// was already removed from the map by Close().
	staleTimer <- struct{}{}

	// Give a moment for any unexpected flush.
	time.Sleep(50 * time.Millisecond)
	select {
	case f2 := <-flushCh:
		t.Errorf("expected no double flush from stale timer, got %+v", f2)
	default:
		// Good — stale timer was a no-op.
	}
}

func TestDebouncer_ConcurrentAdds(t *testing.T) {
	t.Parallel()

	// Use real timers with a short window for this concurrency test.
	flushCh := make(chan flushed, 100)
	d := newMessageDebouncer(50*time.Millisecond, func(ch, nick, msg string) {
		flushCh <- flushed{ch, nick, msg}
	}, slog.Default())
	defer d.Close()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Add("#test", "alice", "concurrent")
		}()
	}
	wg.Wait()

	// Wait for all flushes (may be multiple batches due to timing).
	time.Sleep(200 * time.Millisecond)

	totalLines := 0
	for {
		select {
		case f := <-flushCh:
			// Count lines in this flush.
			for _, c := range f.message {
				if c == '\n' {
					totalLines++
				}
			}
			totalLines++ // last line has no trailing \n
		default:
			goto done
		}
	}
done:
	if totalLines != 20 {
		t.Errorf("expected 20 total lines across all flushes, got %d", totalLines)
	}
}
