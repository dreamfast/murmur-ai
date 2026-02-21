package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// PendingApproval represents a tool call that is waiting for user approval
// before being routed to the client.
type PendingApproval struct {
	ID          string
	Channel     string
	ToolName    string
	Arguments   json.RawMessage
	ClientID    string
	RequestedAt time.Time
	ResultCh    chan ApprovalResult
}

// ApprovalResult carries the user's decision on a pending approval.
type ApprovalResult struct {
	Approved bool
}

// ApprovalManager tracks pending tool call approvals. When a client's autonomy
// level is "approve", the server holds the tool call and asks the user in IRC.
// The manager is safe for concurrent use.
type ApprovalManager struct {
	mu      sync.Mutex
	pending map[string]*PendingApproval // keyed by approval ID
	logger  *slog.Logger
}

// NewApprovalManager creates a new ApprovalManager.
func NewApprovalManager(logger *slog.Logger) *ApprovalManager {
	return &ApprovalManager{
		pending: make(map[string]*PendingApproval),
		logger:  logger,
	}
}

// RequestApproval creates a pending approval for a tool call and returns the
// approval ID and a channel that will receive the user's decision. The caller
// should select on the returned channel with a timeout.
func (am *ApprovalManager) RequestApproval(channel, toolName string, arguments json.RawMessage, clientID string) (string, <-chan ApprovalResult) {
	id := generateApprovalID()
	resultCh := make(chan ApprovalResult, 1)

	am.mu.Lock()
	defer am.mu.Unlock()

	am.pending[id] = &PendingApproval{
		ID:          id,
		Channel:     channel,
		ToolName:    toolName,
		Arguments:   arguments,
		ClientID:    clientID,
		RequestedAt: time.Now(),
		ResultCh:    resultCh,
	}

	am.logger.Info("approval requested",
		"id", id,
		"channel", channel,
		"tool", toolName,
		"client", clientID,
	)

	return id, resultCh
}

// Resolve resolves a pending approval by sending the decision on its result
// channel. Returns an error if the approval ID is not found.
func (am *ApprovalManager) Resolve(id string, approved bool) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	pa, ok := am.pending[id]
	if !ok {
		return fmt.Errorf("resolve: approval %q not found", id)
	}

	pa.ResultCh <- ApprovalResult{Approved: approved}
	close(pa.ResultCh)
	delete(am.pending, id)

	am.logger.Info("approval resolved",
		"id", id,
		"approved", approved,
		"tool", pa.ToolName,
	)

	return nil
}

// Cancel cancels a pending approval by sending a denial on its result channel.
// This is typically called on timeout. If the approval ID is not found, it is
// silently ignored (it may have already been resolved).
func (am *ApprovalManager) Cancel(id string) {
	am.mu.Lock()
	defer am.mu.Unlock()

	pa, ok := am.pending[id]
	if !ok {
		return
	}

	pa.ResultCh <- ApprovalResult{Approved: false}
	close(pa.ResultCh)
	delete(am.pending, id)

	am.logger.Info("approval cancelled",
		"id", id,
		"tool", pa.ToolName,
	)
}

// GetPending returns all pending approvals for the given channel, ordered by
// request time (oldest first). Returns nil if no approvals are pending.
func (am *ApprovalManager) GetPending(channel string) []*PendingApproval {
	am.mu.Lock()
	defer am.mu.Unlock()

	var result []*PendingApproval
	for _, pa := range am.pending {
		if pa.Channel == channel {
			// Return a copy without the result channel to prevent callers
			// from accidentally sending/closing and corrupting manager state.
			cpy := *pa
			cpy.ResultCh = nil
			result = append(result, &cpy)
		}
	}

	// Sort by request time for deterministic ordering.
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j].RequestedAt.Before(result[j-1].RequestedAt); j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}

	return result
}

// GetLatestPending returns the most recent pending approval for the given
// channel, or nil if none exist.
func (am *ApprovalManager) GetLatestPending(channel string) *PendingApproval {
	am.mu.Lock()
	defer am.mu.Unlock()

	var latest *PendingApproval
	for _, pa := range am.pending {
		if pa.Channel == channel {
			if latest == nil || pa.RequestedAt.After(latest.RequestedAt) {
				latest = pa
			}
		}
	}

	if latest == nil {
		return nil
	}

	// Return a copy without the result channel to prevent callers from
	// accidentally sending/closing and corrupting manager state.
	cpy := *latest
	cpy.ResultCh = nil
	return &cpy
}

// Cleanup removes all pending approvals older than maxAge by cancelling them.
// This should be called periodically to prevent stale approvals from
// accumulating.
func (am *ApprovalManager) Cleanup(maxAge time.Duration) {
	am.mu.Lock()
	defer am.mu.Unlock()

	now := time.Now()
	for id, pa := range am.pending {
		if now.Sub(pa.RequestedAt) > maxAge {
			pa.ResultCh <- ApprovalResult{Approved: false}
			close(pa.ResultCh)
			delete(am.pending, id)

			am.logger.Info("approval expired",
				"id", id,
				"tool", pa.ToolName,
				"age", now.Sub(pa.RequestedAt),
			)
		}
	}
}

// generateApprovalID returns a random 8-byte hex string for use as an
// approval identifier.
func generateApprovalID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID if crypto/rand fails (extremely unlikely).
		return fmt.Sprintf("ap-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
