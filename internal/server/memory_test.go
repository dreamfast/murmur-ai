package server

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"murmur/internal/db"
	"murmur/internal/llm"
	"murmur/internal/llm/llmtest"
)

// newTestMemory creates an in-memory database, runs migrations, and returns
// a Memory instance suitable for testing. Summarization is disabled (nil provider).
func newTestMemory(t *testing.T, maxHistory int) *Memory {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewMemory(database, maxHistory, maxHistory*80/100, nil, logger)
}

// newTestMemoryWithSummary creates a Memory instance with a summary provider for testing.
func newTestMemoryWithSummary(t *testing.T, maxHistory, threshold int, provider llm.Provider) (*Memory, *db.DB) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewMemory(database, maxHistory, threshold, provider, logger), database
}

func TestMemory_AddAndGetHistory(t *testing.T) {
	t.Parallel()
	mem := newTestMemory(t, 100)

	if err := mem.AddMessage("#test", "user", "hello", "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := mem.AddMessage("#test", "assistant", "hi there", "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	msgs, err := mem.GetHistory("#test", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Errorf("msg[0] = %+v, want user/hello", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "hi there" {
		t.Errorf("msg[1] = %+v, want assistant/hi there", msgs[1])
	}
}

func TestMemory_HistoryOrdering(t *testing.T) {
	t.Parallel()
	mem := newTestMemory(t, 100)

	for i := 0; i < 5; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		content := string(rune('a' + i))
		if err := mem.AddMessage("#order", role, content, "", ""); err != nil {
			t.Fatalf("AddMessage %d: %v", i, err)
		}
	}

	msgs, err := mem.GetHistory("#order", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 5 {
		t.Fatalf("got %d messages, want 5", len(msgs))
	}
	// Verify oldest first ordering.
	for i, msg := range msgs {
		want := string(rune('a' + i))
		if msg.Content != want {
			t.Errorf("msg[%d].Content = %q, want %q", i, msg.Content, want)
		}
	}
}

func TestMemory_LimitWorks(t *testing.T) {
	t.Parallel()
	mem := newTestMemory(t, 100)

	for i := 0; i < 10; i++ {
		if err := mem.AddMessage("#limit", "user", "msg", "", ""); err != nil {
			t.Fatalf("AddMessage %d: %v", i, err)
		}
	}

	// Request only 3 messages.
	msgs, err := mem.GetHistory("#limit", 3)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 3 {
		t.Errorf("got %d messages, want 3", len(msgs))
	}
}

func TestMemory_FIFOEviction(t *testing.T) {
	t.Parallel()
	mem := newTestMemory(t, 3)

	// Add 5 messages — only the last 3 should remain.
	for i := 1; i <= 5; i++ {
		content := string(rune('0' + i))
		if err := mem.AddMessage("#evict", "user", content, "", ""); err != nil {
			t.Fatalf("AddMessage %d: %v", i, err)
		}
	}

	msgs, err := mem.GetHistory("#evict", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	// Should have messages 3, 4, 5 (oldest evicted).
	if msgs[0].Content != "3" {
		t.Errorf("msg[0].Content = %q, want %q", msgs[0].Content, "3")
	}
	if msgs[1].Content != "4" {
		t.Errorf("msg[1].Content = %q, want %q", msgs[1].Content, "4")
	}
	if msgs[2].Content != "5" {
		t.Errorf("msg[2].Content = %q, want %q", msgs[2].Content, "5")
	}

	// Verify count.
	count, err := mem.GetHistoryCount("#evict")
	if err != nil {
		t.Fatalf("GetHistoryCount: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestMemory_ClearHistory(t *testing.T) {
	t.Parallel()
	mem := newTestMemory(t, 100)

	if err := mem.AddMessage("#clear", "user", "hello", "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := mem.AddMessage("#clear", "assistant", "hi", "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	if err := mem.ClearHistory("#clear"); err != nil {
		t.Fatalf("ClearHistory: %v", err)
	}

	msgs, err := mem.GetHistory("#clear", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("got %d messages after clear, want 0", len(msgs))
	}

	count, err := mem.GetHistoryCount("#clear")
	if err != nil {
		t.Fatalf("GetHistoryCount: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d after clear, want 0", count)
	}
}

func TestMemory_ToolMessages(t *testing.T) {
	t.Parallel()
	mem := newTestMemory(t, 100)

	// Add a tool message with toolName and toolCallID.
	if err := mem.AddMessage("#tools", "tool", "result data", "shell", "call-123"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	msgs, err := mem.GetHistory("#tools", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	msg := msgs[0]
	if msg.Role != "tool" {
		t.Errorf("role = %q, want %q", msg.Role, "tool")
	}
	if msg.Content != "result data" {
		t.Errorf("content = %q, want %q", msg.Content, "result data")
	}
	if msg.Name != "shell" {
		t.Errorf("name = %q, want %q", msg.Name, "shell")
	}
	if msg.ToolCallID != "call-123" {
		t.Errorf("tool_call_id = %q, want %q", msg.ToolCallID, "call-123")
	}
}

func TestMemory_EmptyChannelReturnsEmptySlice(t *testing.T) {
	t.Parallel()
	mem := newTestMemory(t, 100)

	msgs, err := mem.GetHistory("#empty", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if msgs == nil {
		t.Error("expected non-nil empty slice, got nil")
	}
	if len(msgs) != 0 {
		t.Errorf("got %d messages, want 0", len(msgs))
	}
}

func TestMemory_AssistantToolCallsStoredAsContent(t *testing.T) {
	t.Parallel()
	mem := newTestMemory(t, 100)

	// Store an assistant message with tool_calls serialized as JSON content.
	toolCallJSON := `[{"id":"call-1","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\"ls\"}"}}]`
	if err := mem.AddMessage("#toolcalls", "assistant", toolCallJSON, "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	msgs, err := mem.GetHistory("#toolcalls", 10)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Content != toolCallJSON {
		t.Errorf("content mismatch:\ngot:  %s\nwant: %s", msgs[0].Content, toolCallJSON)
	}
}

func TestMemory_ChannelIsolation(t *testing.T) {
	t.Parallel()
	mem := newTestMemory(t, 100)

	if err := mem.AddMessage("#chan1", "user", "msg1", "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := mem.AddMessage("#chan2", "user", "msg2", "", ""); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	msgs1, err := mem.GetHistory("#chan1", 10)
	if err != nil {
		t.Fatalf("GetHistory #chan1: %v", err)
	}
	msgs2, err := mem.GetHistory("#chan2", 10)
	if err != nil {
		t.Fatalf("GetHistory #chan2: %v", err)
	}

	if len(msgs1) != 1 || msgs1[0].Content != "msg1" {
		t.Errorf("#chan1: got %+v, want [msg1]", msgs1)
	}
	if len(msgs2) != 1 || msgs2[0].Content != "msg2" {
		t.Errorf("#chan2: got %+v, want [msg2]", msgs2)
	}
}

func TestMemory_GetHistoryCount(t *testing.T) {
	t.Parallel()
	mem := newTestMemory(t, 100)

	count, err := mem.GetHistoryCount("#count")
	if err != nil {
		t.Fatalf("GetHistoryCount: %v", err)
	}
	if count != 0 {
		t.Errorf("initial count = %d, want 0", count)
	}

	for i := 0; i < 5; i++ {
		if err := mem.AddMessage("#count", "user", "msg", "", ""); err != nil {
			t.Fatalf("AddMessage %d: %v", i, err)
		}
	}

	count, err = mem.GetHistoryCount("#count")
	if err != nil {
		t.Fatalf("GetHistoryCount: %v", err)
	}
	if count != 5 {
		t.Errorf("count = %d, want 5", count)
	}
}

func TestMemory_SummarizationTriggered(t *testing.T) {
	t.Parallel()

	provider := &llmtest.MockProvider{
		NameVal: "test-summary",
		Responses: []*llm.ChatResponse{
			{Content: "This is a summary of the conversation."},
		},
	}

	// threshold=5, maxHistory=20 — summarization triggers when count > 5.
	mem, _ := newTestMemoryWithSummary(t, 20, 5, provider)

	// Add 6 messages to exceed the threshold.
	for i := 0; i < 6; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		content := fmt.Sprintf("message-%d", i)
		if err := mem.AddMessage("#sum", role, content, "", ""); err != nil {
			t.Fatalf("AddMessage %d: %v", i, err)
		}
	}

	// The 6th message should trigger summarization.
	// Verify the provider was called. Safe to read Calls without locking
	// because all AddMessage calls have completed (sequential test).
	if len(provider.Calls) != 1 {
		t.Fatalf("expected 1 LLM call for summarization, got %d", len(provider.Calls))
	}

	// Verify a summary was stored in the summaries table.
	var summaryCount int
	err := mem.db.QueryRow(`SELECT COUNT(*) FROM summaries WHERE channel = ?`, "#sum").Scan(&summaryCount)
	if err != nil {
		t.Fatalf("query summaries: %v", err)
	}
	if summaryCount != 1 {
		t.Errorf("expected 1 summary, got %d", summaryCount)
	}

	// Verify GetHistory returns the summary prepended as first message.
	msgs, err := mem.GetHistory("#sum", 20)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected messages after summarization")
	}

	// First message should be the summary (from GetHistory prepend).
	if msgs[0].Role != "system" {
		t.Errorf("expected first message role=system, got %q", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "summary of the conversation") {
		t.Errorf("expected summary content, got %q", msgs[0].Content)
	}

	// Verify no duplicate summaries — the summary should appear exactly once.
	// The summary is only prepended by GetHistory, not stored as a synthetic
	// message in conversations.
	summaryMsgCount := 0
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "[Previous conversation summary]") {
			summaryMsgCount++
		}
	}
	if summaryMsgCount != 1 {
		t.Errorf("expected exactly 1 summary message in history, got %d", summaryMsgCount)
	}
}

func TestMemory_SummarizationDisabled(t *testing.T) {
	t.Parallel()

	// nil provider — summarization should be a no-op.
	mem := newTestMemory(t, 20)

	// Add many messages — no summarization should occur.
	for i := 0; i < 15; i++ {
		if err := mem.AddMessage("#nosum", "user", fmt.Sprintf("msg-%d", i), "", ""); err != nil {
			t.Fatalf("AddMessage %d: %v", i, err)
		}
	}

	// Verify no summaries were created.
	var summaryCount int
	err := mem.db.QueryRow(`SELECT COUNT(*) FROM summaries WHERE channel = ?`, "#nosum").Scan(&summaryCount)
	if err != nil {
		t.Fatalf("query summaries: %v", err)
	}
	if summaryCount != 0 {
		t.Errorf("expected 0 summaries with nil provider, got %d", summaryCount)
	}

	// All messages should be present.
	msgs, err := mem.GetHistory("#nosum", 20)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 15 {
		t.Errorf("expected 15 messages, got %d", len(msgs))
	}
}

func TestMemory_SummaryIncludedInHistory(t *testing.T) {
	t.Parallel()

	provider := &llmtest.MockProvider{
		NameVal: "test-summary",
		Responses: []*llm.ChatResponse{
			{Content: "Key facts: user asked about Go, assistant explained interfaces."},
		},
	}

	// threshold=3, maxHistory=20.
	mem, _ := newTestMemoryWithSummary(t, 20, 3, provider)

	// Add 4 messages to trigger summarization.
	for i := 0; i < 4; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if err := mem.AddMessage("#hist", role, fmt.Sprintf("msg-%d", i), "", ""); err != nil {
			t.Fatalf("AddMessage %d: %v", i, err)
		}
	}

	// Get history — should include the summary as first message.
	msgs, err := mem.GetHistory("#hist", 20)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}

	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages (summary + remaining), got %d", len(msgs))
	}

	// First message should be the prepended summary.
	if msgs[0].Role != "system" {
		t.Errorf("first message role = %q, want system", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "[Previous conversation summary]") {
		t.Errorf("first message should contain summary prefix, got %q", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, "Go") {
		t.Errorf("first message should contain summary text, got %q", msgs[0].Content)
	}
}

func TestMemory_SummarizationFailureDoesntBlockAdd(t *testing.T) {
	t.Parallel()

	provider := &llmtest.MockProvider{
		NameVal: "test-summary",
		Errors:  []error{fmt.Errorf("LLM unavailable")},
	}

	// threshold=3, maxHistory=20.
	mem, _ := newTestMemoryWithSummary(t, 20, 3, provider)

	// Add 4 messages — summarization will fail but AddMessage should succeed.
	for i := 0; i < 4; i++ {
		if err := mem.AddMessage("#fail", "user", fmt.Sprintf("msg-%d", i), "", ""); err != nil {
			t.Fatalf("AddMessage %d should succeed even with LLM failure: %v", i, err)
		}
	}

	// All messages should still be present (summarization failed, no deletion).
	msgs, err := mem.GetHistory("#fail", 20)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(msgs) != 4 {
		t.Errorf("expected 4 messages (summarization failed), got %d", len(msgs))
	}
}

func TestMemory_SummaryThresholdConfig(t *testing.T) {
	t.Parallel()

	provider := &llmtest.MockProvider{
		NameVal: "test-summary",
		Responses: []*llm.ChatResponse{
			{Content: "Summary text."},
		},
	}

	// threshold=10, maxHistory=20 — should NOT trigger with only 8 messages.
	mem, _ := newTestMemoryWithSummary(t, 20, 10, provider)

	for i := 0; i < 8; i++ {
		if err := mem.AddMessage("#thresh", "user", fmt.Sprintf("msg-%d", i), "", ""); err != nil {
			t.Fatalf("AddMessage %d: %v", i, err)
		}
	}

	// Provider should NOT have been called (below threshold).
	// Safe to read Calls without locking — all AddMessage calls have completed.
	if len(provider.Calls) != 0 {
		t.Errorf("expected 0 LLM calls (below threshold), got %d", len(provider.Calls))
	}

	// Now add 3 more to exceed threshold.
	for i := 8; i < 11; i++ {
		if err := mem.AddMessage("#thresh", "user", fmt.Sprintf("msg-%d", i), "", ""); err != nil {
			t.Fatalf("AddMessage %d: %v", i, err)
		}
	}

	// Provider should have been called now.
	if len(provider.Calls) != 1 {
		t.Errorf("expected 1 LLM call (exceeded threshold), got %d", len(provider.Calls))
	}
}

func TestMemory_ClearHistoryClearsSummaries(t *testing.T) {
	t.Parallel()

	provider := &llmtest.MockProvider{
		NameVal: "test-summary",
		Responses: []*llm.ChatResponse{
			{Content: "Summary to be cleared."},
		},
	}

	// threshold=3, maxHistory=20.
	mem, _ := newTestMemoryWithSummary(t, 20, 3, provider)

	// Add 4 messages to trigger summarization.
	for i := 0; i < 4; i++ {
		if err := mem.AddMessage("#clearsum", "user", fmt.Sprintf("msg-%d", i), "", ""); err != nil {
			t.Fatalf("AddMessage %d: %v", i, err)
		}
	}

	// Verify summary exists.
	msgs, err := mem.GetHistory("#clearsum", 20)
	if err != nil {
		t.Fatalf("GetHistory before clear: %v", err)
	}
	hasSummary := false
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "[Previous conversation summary]") {
			hasSummary = true
			break
		}
	}
	if !hasSummary {
		t.Fatal("expected summary before clear")
	}

	// Clear history — should remove both conversations and summaries.
	if err := mem.ClearHistory("#clearsum"); err != nil {
		t.Fatalf("ClearHistory: %v", err)
	}

	// Verify no messages remain.
	msgs, err = mem.GetHistory("#clearsum", 20)
	if err != nil {
		t.Fatalf("GetHistory after clear: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages after clear, got %d", len(msgs))
	}

	// Verify summaries table is also cleared.
	var summaryCount int
	err = mem.db.QueryRow(`SELECT COUNT(*) FROM summaries WHERE channel = ?`, "#clearsum").Scan(&summaryCount)
	if err != nil {
		t.Fatalf("query summaries: %v", err)
	}
	if summaryCount != 0 {
		t.Errorf("expected 0 summaries after clear, got %d", summaryCount)
	}
}

func TestMemory_MultiCycleSummarization(t *testing.T) {
	t.Parallel()

	provider := &llmtest.MockProvider{
		NameVal: "test-summary",
		Responses: []*llm.ChatResponse{
			{Content: "First cycle summary."},
			{Content: "Second cycle summary."},
		},
	}

	// threshold=4, maxHistory=20 — triggers summarization when count > 4.
	mem, _ := newTestMemoryWithSummary(t, 20, 4, provider)

	// Cycle 1: Add 5 messages to trigger first summarization.
	for i := 0; i < 5; i++ {
		if err := mem.AddMessage("#multi", "user", fmt.Sprintf("cycle1-msg-%d", i), "", ""); err != nil {
			t.Fatalf("Cycle 1 AddMessage %d: %v", i, err)
		}
	}

	if len(provider.Calls) != 1 {
		t.Fatalf("expected 1 LLM call after cycle 1, got %d", len(provider.Calls))
	}

	// Add more messages to trigger second summarization.
	for i := 0; i < 5; i++ {
		if err := mem.AddMessage("#multi", "user", fmt.Sprintf("cycle2-msg-%d", i), "", ""); err != nil {
			t.Fatalf("Cycle 2 AddMessage %d: %v", i, err)
		}
	}

	// Should have triggered a second summarization.
	if len(provider.Calls) < 2 {
		t.Fatalf("expected at least 2 LLM calls after cycle 2, got %d", len(provider.Calls))
	}

	// Verify history is stable — only one summary prepended (the latest).
	msgs, err := mem.GetHistory("#multi", 20)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}

	// First message should be the latest summary.
	if msgs[0].Role != "system" {
		t.Errorf("first message role = %q, want system", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "[Previous conversation summary]") {
		t.Errorf("first message should contain summary prefix, got %q", msgs[0].Content)
	}

	// Verify no duplicate summary messages in history.
	summaryMsgCount := 0
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "[Previous conversation summary]") {
			summaryMsgCount++
		}
	}
	if summaryMsgCount != 1 {
		t.Errorf("expected exactly 1 summary message in history after multi-cycle, got %d", summaryMsgCount)
	}

	// Verify summaries table has 2 entries (one per cycle).
	var summaryCount int
	err = mem.db.QueryRow(`SELECT COUNT(*) FROM summaries WHERE channel = ?`, "#multi").Scan(&summaryCount)
	if err != nil {
		t.Fatalf("query summaries: %v", err)
	}
	if summaryCount < 2 {
		t.Errorf("expected at least 2 summaries in table, got %d", summaryCount)
	}
}

// TestMemory_GetRecentMessages verifies that GetRecentMessages returns compact
// messages excluding tool-role messages, suitable for cross-channel context.
func TestMemory_GetRecentMessages(t *testing.T) {
	m := newTestMemory(t, 100)

	// Populate #news with a mix of roles including tool messages.
	if err := m.AddMessage("#news", "user", "maxx: show me hacker news", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.AddMessage("#news", "assistant", `[{"id":"call-1","type":"function","function":{"name":"web_search","arguments":"{}"}}]`, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.AddMessage("#news", "tool", "search results here", "web_search", "call-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.AddMessage("#news", "assistant", "Here are the top HN stories", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.AddMessage("#news", "user", "maxx: thanks", "", ""); err != nil {
		t.Fatal(err)
	}

	// GetRecentMessages should skip tool-role messages AND assistant
	// tool-call envelopes (JSON with tool_calls or [{"id":...).
	msgs, err := m.GetRecentMessages("#news", 10)
	if err != nil {
		t.Fatal(err)
	}

	// Expect 3 messages: 2 user + 1 text assistant.
	// Filtered out: 1 tool message + 1 assistant tool-call envelope.
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3 (tool and tool-call envelope filtered)", len(msgs))
	}

	// Verify no tool role and no tool-call envelope content.
	for _, msg := range msgs {
		if msg.Role == "tool" {
			t.Errorf("unexpected tool message in GetRecentMessages: %+v", msg)
		}
		if strings.HasPrefix(msg.Content, `[{"id":`) || strings.HasPrefix(msg.Content, `{"tool_calls":`) {
			t.Errorf("unexpected tool-call envelope in GetRecentMessages: %+v", msg)
		}
	}

	// Verify order: oldest first.
	if msgs[0].Content != "maxx: show me hacker news" {
		t.Errorf("first message = %q, want user message", msgs[0].Content)
	}
	if msgs[len(msgs)-1].Content != "maxx: thanks" {
		t.Errorf("last message = %q, want last user message", msgs[len(msgs)-1].Content)
	}

	// Verify limit works.
	limited, err := m.GetRecentMessages("#news", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 {
		t.Fatalf("got %d messages with limit=2, want 2", len(limited))
	}

	// Verify channel isolation — #murmur should have no messages.
	empty, err := m.GetRecentMessages("#murmur", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Errorf("got %d messages for #murmur, want 0", len(empty))
	}
}

// Verify that the llm.Message type is used correctly (compile-time check).
var _ = []llm.Message{}
