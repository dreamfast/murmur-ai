package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"
)

func newTestApprovalManager(t *testing.T) *ApprovalManager {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewApprovalManager(logger)
}

func TestApprovalManager_RequestAndResolve(t *testing.T) {
	t.Parallel()
	am := newTestApprovalManager(t)

	id, resultCh := am.RequestApproval("#test", "shell", json.RawMessage(`{"cmd":"ls"}`), "client-1")

	if id == "" {
		t.Fatal("expected non-empty approval ID")
	}

	// Resolve the approval in a goroutine (simulates user typing !approve).
	go func() {
		if err := am.Resolve(id, true); err != nil {
			t.Errorf("Resolve: %v", err)
		}
	}()

	select {
	case result := <-resultCh:
		if !result.Approved {
			t.Error("expected approval to be approved")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval result")
	}

	// Verify the pending map is empty after resolution.
	pending := am.GetPending("#test")
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after resolve, got %d", len(pending))
	}
}

func TestApprovalManager_ResolveDeny(t *testing.T) {
	t.Parallel()
	am := newTestApprovalManager(t)

	id, resultCh := am.RequestApproval("#test", "shell", json.RawMessage(`{}`), "client-1")

	go func() {
		if err := am.Resolve(id, false); err != nil {
			t.Errorf("Resolve: %v", err)
		}
	}()

	select {
	case result := <-resultCh:
		if result.Approved {
			t.Error("expected approval to be denied")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval result")
	}
}

func TestApprovalManager_ResolveNotFound(t *testing.T) {
	t.Parallel()
	am := newTestApprovalManager(t)

	err := am.Resolve("nonexistent-id", true)
	if err == nil {
		t.Fatal("expected error for nonexistent approval ID")
	}
}

func TestApprovalManager_Cancel(t *testing.T) {
	t.Parallel()
	am := newTestApprovalManager(t)

	id, resultCh := am.RequestApproval("#test", "shell", json.RawMessage(`{}`), "client-1")

	am.Cancel(id)

	select {
	case result := <-resultCh:
		if result.Approved {
			t.Error("expected cancelled approval to be denied")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancel result")
	}

	// Verify the pending map is empty after cancellation.
	pending := am.GetPending("#test")
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after cancel, got %d", len(pending))
	}
}

func TestApprovalManager_CancelNonexistent(t *testing.T) {
	t.Parallel()
	am := newTestApprovalManager(t)

	// Should not panic or error — silently ignored.
	am.Cancel("nonexistent-id")
}

func TestApprovalManager_GetPending(t *testing.T) {
	t.Parallel()
	am := newTestApprovalManager(t)

	// Create approvals on two different channels.
	am.RequestApproval("#chan1", "shell", json.RawMessage(`{}`), "client-1")
	am.RequestApproval("#chan1", "mail_send", json.RawMessage(`{}`), "client-2")
	am.RequestApproval("#chan2", "dns_check", json.RawMessage(`{}`), "client-3")

	// Get pending for #chan1 — should have 2.
	pending1 := am.GetPending("#chan1")
	if len(pending1) != 2 {
		t.Fatalf("expected 2 pending for #chan1, got %d", len(pending1))
	}

	// Verify ordering (oldest first).
	if pending1[0].ToolName != "shell" {
		t.Errorf("first pending tool = %q, want shell", pending1[0].ToolName)
	}
	if pending1[1].ToolName != "mail_send" {
		t.Errorf("second pending tool = %q, want mail_send", pending1[1].ToolName)
	}

	// Get pending for #chan2 — should have 1.
	pending2 := am.GetPending("#chan2")
	if len(pending2) != 1 {
		t.Fatalf("expected 1 pending for #chan2, got %d", len(pending2))
	}
	if pending2[0].ToolName != "dns_check" {
		t.Errorf("pending tool = %q, want dns_check", pending2[0].ToolName)
	}

	// Get pending for unknown channel — should be empty.
	pending3 := am.GetPending("#unknown")
	if len(pending3) != 0 {
		t.Errorf("expected 0 pending for #unknown, got %d", len(pending3))
	}
}

func TestApprovalManager_GetLatestPending(t *testing.T) {
	t.Parallel()
	am := newTestApprovalManager(t)

	am.RequestApproval("#test", "shell", json.RawMessage(`{}`), "client-1")
	// Small sleep to ensure different timestamps.
	time.Sleep(time.Millisecond)
	am.RequestApproval("#test", "mail_send", json.RawMessage(`{}`), "client-2")

	latest := am.GetLatestPending("#test")
	if latest == nil {
		t.Fatal("expected non-nil latest pending")
	}
	if latest.ToolName != "mail_send" {
		t.Errorf("latest pending tool = %q, want mail_send", latest.ToolName)
	}

	// No pending for unknown channel.
	if got := am.GetLatestPending("#unknown"); got != nil {
		t.Errorf("expected nil for unknown channel, got %+v", got)
	}
}

func TestApprovalManager_Cleanup(t *testing.T) {
	t.Parallel()
	am := newTestApprovalManager(t)

	// Create an approval and manually backdate it.
	id, resultCh := am.RequestApproval("#test", "shell", json.RawMessage(`{}`), "client-1")

	am.mu.Lock()
	am.pending[id].RequestedAt = time.Now().Add(-5 * time.Minute)
	am.mu.Unlock()

	// Cleanup with 1-minute max age — should expire the approval.
	am.Cleanup(1 * time.Minute)

	select {
	case result := <-resultCh:
		if result.Approved {
			t.Error("expected expired approval to be denied")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cleanup result")
	}

	// Verify the pending map is empty.
	pending := am.GetPending("#test")
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after cleanup, got %d", len(pending))
	}
}

func TestApprovalManager_CleanupKeepsFresh(t *testing.T) {
	t.Parallel()
	am := newTestApprovalManager(t)

	// Create a fresh approval (just now).
	am.RequestApproval("#test", "shell", json.RawMessage(`{}`), "client-1")

	// Cleanup with 1-minute max age — should NOT expire the fresh approval.
	am.Cleanup(1 * time.Minute)

	pending := am.GetPending("#test")
	if len(pending) != 1 {
		t.Errorf("expected 1 pending after cleanup (fresh), got %d", len(pending))
	}
}

func TestApprovalManager_UniqueIDs(t *testing.T) {
	t.Parallel()
	am := newTestApprovalManager(t)

	ids := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		id, _ := am.RequestApproval("#test", "shell", json.RawMessage(`{}`), "client-1")
		if _, exists := ids[id]; exists {
			t.Fatalf("duplicate approval ID: %s", id)
		}
		ids[id] = struct{}{}
	}
}
