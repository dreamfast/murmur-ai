package irc

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestIRCLogHandler_EnabledBeforeConnection(t *testing.T) {
	t.Parallel()

	h := NewIRCLogHandler("#debug", slog.LevelDebug)
	defer h.Close()

	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("handler should be disabled before SetConnection")
	}
}

func TestIRCLogHandler_EnabledAfterConnection(t *testing.T) {
	t.Parallel()

	h := NewIRCLogHandler("#debug", slog.LevelDebug)
	defer h.Close()

	// Simulate setting a connection (we can't create a real one in tests,
	// but SetConnection just stores the pointer and sets enabled=true).
	h.enabled.Store(true)

	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("handler should be enabled after connection is set")
	}
}

func TestIRCLogHandler_LevelFiltering(t *testing.T) {
	t.Parallel()

	h := NewIRCLogHandler("#debug", slog.LevelWarn)
	defer h.Close()
	h.enabled.Store(true)

	tests := []struct {
		name    string
		level   slog.Level
		enabled bool
	}{
		{"debug below warn", slog.LevelDebug, false},
		{"info below warn", slog.LevelInfo, false},
		{"warn at threshold", slog.LevelWarn, true},
		{"error above warn", slog.LevelError, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := h.Enabled(context.Background(), tt.level); got != tt.enabled {
				t.Errorf("Enabled(%v) = %v, want %v", tt.level, got, tt.enabled)
			}
		})
	}
}

func TestIRCLogHandler_SetLevel(t *testing.T) {
	t.Parallel()

	h := NewIRCLogHandler("#debug", slog.LevelInfo)
	defer h.Close()
	h.enabled.Store(true)

	if h.Level() != slog.LevelInfo {
		t.Errorf("initial level = %v, want %v", h.Level(), slog.LevelInfo)
	}

	h.SetLevel(slog.LevelError)
	if h.Level() != slog.LevelError {
		t.Errorf("after SetLevel, level = %v, want %v", h.Level(), slog.LevelError)
	}

	// Debug should now be filtered.
	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("debug should be filtered at error level")
	}
	// Error should pass.
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("error should pass at error level")
	}
}

func TestIRCLogHandler_SetEnabled(t *testing.T) {
	t.Parallel()

	h := NewIRCLogHandler("#debug", slog.LevelDebug)
	defer h.Close()

	h.SetEnabled(true)
	if !h.IsEnabled() {
		t.Error("should be enabled after SetEnabled(true)")
	}

	h.SetEnabled(false)
	if h.IsEnabled() {
		t.Error("should be disabled after SetEnabled(false)")
	}
}

func TestIRCLogHandler_HandleDropsWhenDisabled(t *testing.T) {
	t.Parallel()

	h := NewIRCLogHandler("#debug", slog.LevelDebug)
	defer h.Close()

	// Handler is disabled — Handle should return nil without buffering.
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "test message", 0)
	if err := h.Handle(context.Background(), r); err != nil {
		t.Errorf("Handle returned error: %v", err)
	}

	// Buffer should be empty.
	select {
	case <-h.msgCh:
		t.Error("message should not have been buffered when disabled")
	default:
		// expected
	}
}

func TestIRCLogHandler_HandleBuffersWhenEnabled(t *testing.T) {
	t.Parallel()

	h := NewIRCLogHandler("#debug", slog.LevelDebug)
	defer h.Close()
	h.enabled.Store(true)

	r := slog.NewRecord(time.Now(), slog.LevelInfo, "test message", 0)
	r.AddAttrs(slog.String("key", "value"))
	if err := h.Handle(context.Background(), r); err != nil {
		t.Errorf("Handle returned error: %v", err)
	}

	// Check that the message was buffered.
	select {
	case msg := <-h.msgCh:
		if msg == "" {
			t.Error("buffered message should not be empty")
		}
		// Verify format contains level and message.
		if !strings.Contains(msg, "[INFO]") {
			t.Errorf("message should contain [INFO], got: %s", msg)
		}
		if !strings.Contains(msg, "test message") {
			t.Errorf("message should contain 'test message', got: %s", msg)
		}
		if !strings.Contains(msg, "key=value") {
			t.Errorf("message should contain 'key=value', got: %s", msg)
		}
	default:
		t.Error("expected a buffered message")
	}
}

func TestIRCLogHandler_DropNewestOnFullBuffer(t *testing.T) {
	t.Parallel()

	h := NewIRCLogHandler("#debug", slog.LevelDebug)
	defer h.Close()
	h.enabled.Store(true)

	// Fill the buffer.
	for i := 0; i < ircLogBufSize; i++ {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "fill", 0)
		_ = h.Handle(context.Background(), r)
	}

	// Next message should be silently dropped (not block).
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "overflow", 0)
	done := make(chan struct{})
	go func() {
		_ = h.Handle(context.Background(), r)
		close(done)
	}()

	select {
	case <-done:
		// good — didn't block
	case <-time.After(1 * time.Second):
		t.Fatal("Handle blocked on full buffer — should use drop-newest semantics")
	}
}

func TestMultiHandler_Enabled(t *testing.T) {
	t.Parallel()

	h1 := NewIRCLogHandler("#debug", slog.LevelError)
	defer h1.Close()
	h1.enabled.Store(true)

	h2 := NewIRCLogHandler("#debug", slog.LevelDebug)
	defer h2.Close()
	h2.enabled.Store(true)

	multi := NewMultiHandler(h1, h2)

	// Debug: h1 says no, h2 says yes → multi says yes.
	if !multi.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("multi should be enabled if any child is enabled")
	}

	// Error: both say yes.
	if !multi.Enabled(context.Background(), slog.LevelError) {
		t.Error("multi should be enabled for error level")
	}
}

func TestMultiHandler_Handle(t *testing.T) {
	t.Parallel()

	h1 := NewIRCLogHandler("#debug1", slog.LevelDebug)
	defer h1.Close()
	h1.enabled.Store(true)

	h2 := NewIRCLogHandler("#debug2", slog.LevelDebug)
	defer h2.Close()
	h2.enabled.Store(true)

	multi := NewMultiHandler(h1, h2)

	r := slog.NewRecord(time.Now(), slog.LevelInfo, "multi test", 0)
	if err := multi.Handle(context.Background(), r); err != nil {
		t.Errorf("Handle returned error: %v", err)
	}

	// Both handlers should have the message buffered.
	select {
	case msg := <-h1.msgCh:
		if !strings.Contains(msg, "multi test") {
			t.Errorf("h1 message should contain 'multi test', got: %s", msg)
		}
	default:
		t.Error("h1 should have a buffered message")
	}

	select {
	case msg := <-h2.msgCh:
		if !strings.Contains(msg, "multi test") {
			t.Errorf("h2 message should contain 'multi test', got: %s", msg)
		}
	default:
		t.Error("h2 should have a buffered message")
	}
}

func TestIRCLogHandler_ReconnectPreservesUserDisabled(t *testing.T) {
	t.Parallel()

	h := NewIRCLogHandler("#debug", slog.LevelDebug)
	defer h.Close()

	// Simulate initial connection — handler becomes enabled.
	h.SetConnection(nil) // nil conn is fine for this test; we only check enabled state.
	if !h.IsEnabled() {
		t.Fatal("handler should be enabled after initial SetConnection")
	}

	// User explicitly disables debug output.
	h.SetEnabled(false)
	if h.IsEnabled() {
		t.Fatal("handler should be disabled after SetEnabled(false)")
	}

	// Simulate a reconnect — SetConnection is called again.
	h.SetConnection(nil)
	if h.IsEnabled() {
		t.Error("handler should remain disabled after reconnect when user disabled it")
	}

	// User re-enables debug output.
	h.SetEnabled(true)
	if !h.IsEnabled() {
		t.Fatal("handler should be enabled after SetEnabled(true)")
	}

	// Another reconnect — should stay enabled since user re-enabled.
	h.SetConnection(nil)
	if !h.IsEnabled() {
		t.Error("handler should remain enabled after reconnect when user re-enabled it")
	}
}
