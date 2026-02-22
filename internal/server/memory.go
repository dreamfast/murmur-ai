package server

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"murmur/internal/db"
	"murmur/internal/llm"
)

// summaryPrompt is the system prompt used when asking the LLM to summarize
// a conversation. It instructs the model to produce a concise summary that
// preserves key facts, decisions, and context.
const summaryPrompt = `Summarize the following conversation concisely. Preserve key facts, decisions, action items, and important context. The summary will be used as context for future conversations, so include anything that would be important to remember. Be brief but thorough — aim for 2-5 sentences.`

// summaryTimeout is the maximum duration for a summarization LLM call.
// This prevents hung LLM calls from blocking message ingestion indefinitely.
const summaryTimeout = 30 * time.Second

// Memory provides conversation history operations backed by SQLite.
// It wraps a *db.DB and enforces a per-channel message limit via FIFO
// eviction. Optionally, when a summary provider is configured, it
// summarizes older messages before evicting them. Summaries are stored
// in the summaries table and prepended by GetHistory — no synthetic
// messages are inserted into the conversations table.
// All methods are safe for concurrent use (serialized by SQLite).
type Memory struct {
	db              *db.DB
	summaryProvider llm.Provider // nil means summarization disabled
	logger          *slog.Logger

	// lifecycleCtx is the server's lifecycle context. Summarization timeout
	// contexts are derived from this so they are cancelled on shutdown.
	// Set via SetLifecycleContext; defaults to context.Background().
	lifecycleCtx context.Context

	// configMu protects maxHistory and summaryThreshold, which can be
	// updated at runtime via UpdateConfig during hot config reload.
	configMu         sync.RWMutex
	maxHistory       int
	summaryThreshold int

	// summarizeMu prevents concurrent summarization for the same channel.
	// The map key is the channel name. Access to the map itself is guarded
	// by summarizeMapMu.
	summarizeMapMu sync.Mutex
	summarizeMu    map[string]*sync.Mutex
}

// NewMemory creates a new Memory instance. The maxHistory parameter sets the
// maximum number of messages retained per channel; older messages are evicted
// when the limit is exceeded. The summaryThreshold controls when summarization
// is triggered (when message count exceeds this value). The summaryProvider
// is the LLM provider used for summarization — pass nil to disable.
func NewMemory(database *db.DB, maxHistory, summaryThreshold int, summaryProvider llm.Provider, logger *slog.Logger) *Memory {
	return &Memory{
		db:               database,
		maxHistory:       maxHistory,
		summaryThreshold: summaryThreshold,
		summaryProvider:  summaryProvider,
		logger:           logger,
		lifecycleCtx:     context.Background(),
		summarizeMu:      make(map[string]*sync.Mutex),
	}
}

// SetLifecycleContext sets the server lifecycle context used as the parent
// for summarization timeout contexts. This ensures that in-flight
// summarizations are cancelled when the server shuts down.
func (m *Memory) SetLifecycleContext(ctx context.Context) {
	m.lifecycleCtx = ctx
}

// AddMessage inserts a conversation message into the database. After the
// insert, if the message count for the channel exceeds maxHistory, the oldest
// messages are deleted to bring the count back to maxHistory (FIFO eviction).
// Both operations run in a single transaction for atomicity.
//
// If a summary provider is configured and the message count exceeds the
// summary threshold, MaybeSummarize is called after the insert. Summarization
// failures are non-fatal — they are logged but do not prevent the message
// from being stored.
func (m *Memory) AddMessage(channel, role, content, toolName, toolCallID string) error {
	tx, err := m.db.Begin()
	if err != nil {
		return fmt.Errorf("AddMessage: begin tx: %w", err)
	}
	defer func() {
		// Rollback is a no-op if the transaction was already committed.
		_ = tx.Rollback()
	}()

	_, err = tx.Exec(
		`INSERT INTO conversations (channel, role, content, tool_name, tool_call_id) VALUES (?, ?, ?, ?, ?)`,
		channel, role, content, nullIfEmpty(toolName), nullIfEmpty(toolCallID),
	)
	if err != nil {
		return fmt.Errorf("AddMessage: insert: %w", err)
	}

	// FIFO eviction: delete oldest messages if count exceeds maxHistory.
	maxHist := m.loadMaxHistory()
	if maxHist > 0 {
		_, err = tx.Exec(
			`DELETE FROM conversations WHERE channel = ? AND id NOT IN (
				SELECT id FROM conversations WHERE channel = ? ORDER BY id DESC LIMIT ?
			)`,
			channel, channel, maxHist,
		)
		if err != nil {
			return fmt.Errorf("AddMessage: evict: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("AddMessage: commit: %w", err)
	}

	// Attempt summarization after commit. This is non-fatal — if it fails,
	// the message was still stored successfully. A per-channel mutex prevents
	// concurrent summarization attempts for the same channel.
	if m.summaryProvider != nil {
		count, err := m.GetHistoryCount(channel)
		if err != nil {
			m.logger.Error("AddMessage: failed to get history count for summarization",
				"channel", channel,
				"error", err,
			)
			return nil
		}
		if count > m.loadSummaryThreshold() {
			ctx, cancel := context.WithTimeout(m.lifecycleCtx, summaryTimeout)
			defer cancel()
			if err := m.MaybeSummarize(ctx, channel); err != nil {
				m.logger.Error("AddMessage: summarization failed (non-fatal)",
					"channel", channel,
					"error", err,
				)
			}
		}
	}

	return nil
}

// MaybeSummarize summarizes the older half of conversation messages for a
// channel. It:
//  1. Acquires a per-channel mutex to prevent concurrent summarization
//  2. Retrieves all messages for the channel
//  3. Takes the older half and sends them to the summary provider
//  4. Stores the summary in the summaries table
//  5. Deletes the summarized messages from conversations
//
// The summary is NOT inserted into the conversations table — it is only
// stored in the summaries table and prepended by GetHistory. This avoids
// duplicate summaries and prevents recursive summarization of summaries.
//
// If the summary provider is nil, this is a no-op. Errors are returned but
// callers should treat them as non-fatal.
func (m *Memory) MaybeSummarize(ctx context.Context, channel string) error {
	if m.summaryProvider == nil {
		return nil
	}

	// Acquire per-channel mutex to prevent concurrent summarization.
	mu := m.getChannelMu(channel)
	mu.Lock()
	defer mu.Unlock()

	// Get all messages for the channel.
	rows, err := m.db.Query(
		`SELECT id, role, content FROM conversations WHERE channel = ? ORDER BY id ASC`,
		channel,
	)
	if err != nil {
		return fmt.Errorf("MaybeSummarize: query messages: %w", err)
	}
	defer rows.Close()

	type msgRow struct {
		id      int64
		role    string
		content string
	}
	var allMsgs []msgRow
	for rows.Next() {
		var r msgRow
		if err := rows.Scan(&r.id, &r.role, &r.content); err != nil {
			return fmt.Errorf("MaybeSummarize: scan: %w", err)
		}
		allMsgs = append(allMsgs, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("MaybeSummarize: rows iteration: %w", err)
	}

	if len(allMsgs) <= m.loadSummaryThreshold() {
		return nil // below threshold, nothing to do
	}

	// Take the older half for summarization.
	halfIdx := len(allMsgs) / 2
	toSummarize := allMsgs[:halfIdx]
	if len(toSummarize) == 0 {
		return nil
	}

	// Build the conversation text for the LLM.
	var sb strings.Builder
	for _, msg := range toSummarize {
		fmt.Fprintf(&sb, "%s: %s\n", msg.role, msg.content)
	}

	// Call the summary provider.
	req := &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: summaryPrompt},
			{Role: llm.RoleUser, Content: sb.String()},
		},
	}

	resp, err := m.summaryProvider.ChatCompletion(ctx, req)
	if err != nil {
		return fmt.Errorf("MaybeSummarize: LLM call: %w", err)
	}

	summaryText := strings.TrimSpace(resp.Content)
	if summaryText == "" {
		return fmt.Errorf("MaybeSummarize: LLM returned empty summary")
	}

	// Store the summary and delete old messages in a single transaction.
	tx, err := m.db.Begin()
	if err != nil {
		return fmt.Errorf("MaybeSummarize: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Store in summaries table.
	startID := toSummarize[0].id
	endID := toSummarize[len(toSummarize)-1].id
	_, err = tx.Exec(
		`INSERT INTO summaries (channel, summary, messages_start, messages_end) VALUES (?, ?, ?, ?)`,
		channel, summaryText, startID, endID,
	)
	if err != nil {
		return fmt.Errorf("MaybeSummarize: insert summary: %w", err)
	}

	// Delete the summarized messages.
	_, err = tx.Exec(
		`DELETE FROM conversations WHERE channel = ? AND id <= ?`,
		channel, endID,
	)
	if err != nil {
		return fmt.Errorf("MaybeSummarize: delete summarized messages: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("MaybeSummarize: commit: %w", err)
	}

	m.logger.Info("conversation summarized",
		"channel", channel,
		"messages_summarized", len(toSummarize),
		"remaining", len(allMsgs)-halfIdx,
	)

	return nil
}

// getChannelMu returns the per-channel mutex for summarization, creating one
// if it doesn't exist yet.
func (m *Memory) getChannelMu(channel string) *sync.Mutex {
	m.summarizeMapMu.Lock()
	defer m.summarizeMapMu.Unlock()
	mu, ok := m.summarizeMu[channel]
	if !ok {
		mu = &sync.Mutex{}
		m.summarizeMu[channel] = mu
	}
	return mu
}

// GetHistory retrieves the last limit messages for a channel, ordered by
// insertion order (id) ascending (oldest first). If a summary exists for the
// channel, the most recent summary is prepended as a system message. Note
// that the prepended summary does not count toward the limit, so the returned
// slice may contain up to limit+1 messages when a summary is present.
// The returned slice is never nil — an empty channel returns an empty slice.
// Each row is converted to an llm.Message with the appropriate fields set
// based on the role.
func (m *Memory) GetHistory(channel string, limit int) ([]llm.Message, error) {
	// Check for the most recent summary.
	var summaryText sql.NullString
	err := m.db.QueryRow(
		`SELECT summary FROM summaries WHERE channel = ? ORDER BY id DESC LIMIT 1`,
		channel,
	).Scan(&summaryText)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("GetHistory: query summary: %w", err)
	}

	// Use a subquery to get the last N message IDs, then select and order
	// ascending by id. This avoids issues with rowid in derived tables.
	rows, err := m.db.Query(
		`SELECT role, content, tool_name, tool_call_id FROM conversations
		WHERE channel = ? AND id IN (
			SELECT id FROM conversations WHERE channel = ? ORDER BY id DESC LIMIT ?
		) ORDER BY id ASC`,
		channel, channel, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("GetHistory: query: %w", err)
	}
	defer rows.Close()

	messages := make([]llm.Message, 0)

	// Prepend the most recent summary as a system message if available.
	if summaryText.Valid && summaryText.String != "" {
		messages = append(messages, llm.Message{
			Role:    llm.RoleSystem,
			Content: "[Previous conversation summary] " + summaryText.String,
		})
	}

	for rows.Next() {
		var role, content string
		var toolName, toolCallID *string
		if err := rows.Scan(&role, &content, &toolName, &toolCallID); err != nil {
			return nil, fmt.Errorf("GetHistory: scan: %w", err)
		}

		msg := llm.Message{
			Role:    role,
			Content: content,
		}
		if toolName != nil {
			msg.Name = *toolName
		}
		if toolCallID != nil {
			msg.ToolCallID = *toolCallID
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetHistory: rows iteration: %w", err)
	}

	return messages, nil
}

// RecentMessages is a compact representation of a conversation message
// for cross-channel context. It contains only the role and content,
// omitting tool-specific metadata that is irrelevant outside the
// originating channel's agent loop.
type RecentMessage struct {
	Role    string
	Content string
}

// GetRecentMessages retrieves the last limit messages for a channel as
// compact RecentMessage values. Unlike GetHistory, it does not prepend
// summaries or include tool metadata — it is designed for lightweight
// cross-channel context injection into the system prompt. Only user and
// assistant messages are included; tool and system messages are excluded
// since they are either meaningless outside the originating tool call
// sequence or contain internal instructions. Assistant messages that
// contain tool-call envelopes (JSON with "tool_calls") are also filtered
// out since they are internal protocol, not human-readable content.
func (m *Memory) GetRecentMessages(channel string, limit int) ([]RecentMessage, error) {
	// The NOT LIKE filter excludes assistant messages that are tool-call
	// envelopes (serialized JSON starting with {"tool_calls": or [{"id":).
	// These are stored as assistant-role content but are not meaningful
	// text — they are internal protocol for the agent loop.
	rows, err := m.db.Query(
		`SELECT role, content FROM (
			SELECT id, role, content FROM conversations
			WHERE channel = ? AND role IN ('user', 'assistant')
				AND content NOT LIKE '{"tool_calls":%'
				AND content NOT LIKE '[{"id":%'
			ORDER BY id DESC LIMIT ?
		) ORDER BY id ASC`,
		channel, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("GetRecentMessages: query: %w", err)
	}
	defer rows.Close()

	var messages []RecentMessage
	for rows.Next() {
		var rm RecentMessage
		if err := rows.Scan(&rm.Role, &rm.Content); err != nil {
			return nil, fmt.Errorf("GetRecentMessages: scan: %w", err)
		}
		messages = append(messages, rm)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetRecentMessages: rows iteration: %w", err)
	}
	return messages, nil
}

// ClearHistory deletes all conversation messages and summaries for a channel.
// This ensures a complete "forget" — no residual context remains.
func (m *Memory) ClearHistory(channel string) error {
	tx, err := m.db.Begin()
	if err != nil {
		return fmt.Errorf("ClearHistory: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM conversations WHERE channel = ?`, channel); err != nil {
		return fmt.Errorf("ClearHistory: delete conversations: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM summaries WHERE channel = ?`, channel); err != nil {
		return fmt.Errorf("ClearHistory: delete summaries: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ClearHistory: commit: %w", err)
	}
	return nil
}

// UpdateConfig updates the memory's configuration fields. This is called
// during hot config reload for fields that can be safely changed at runtime.
// The maxHistory and summaryThreshold values take effect on the next AddMessage
// call — existing messages are not retroactively evicted.
func (m *Memory) UpdateConfig(maxHistory, summaryThreshold int) {
	m.configMu.Lock()
	defer m.configMu.Unlock()
	m.maxHistory = maxHistory
	m.summaryThreshold = summaryThreshold
}

// loadMaxHistory returns the current maxHistory value under read lock.
func (m *Memory) loadMaxHistory() int {
	m.configMu.RLock()
	defer m.configMu.RUnlock()
	return m.maxHistory
}

// loadSummaryThreshold returns the current summaryThreshold value under read lock.
func (m *Memory) loadSummaryThreshold() int {
	m.configMu.RLock()
	defer m.configMu.RUnlock()
	return m.summaryThreshold
}

// GetHistoryCount returns the number of conversation messages for a channel.
func (m *Memory) GetHistoryCount(channel string) (int, error) {
	var count int
	err := m.db.QueryRow(`SELECT COUNT(*) FROM conversations WHERE channel = ?`, channel).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("GetHistoryCount: %w", err)
	}
	return count, nil
}

// nullIfEmpty returns nil if s is empty, otherwise returns a pointer to s.
// This is used to store optional fields as NULL in SQLite rather than empty
// strings, which allows cleaner queries and distinguishes "not set" from
// "set to empty".
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
