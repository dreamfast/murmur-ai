package db

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestInsertUsageStat(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	s := &UsageStat{
		Channel:          "#test",
		Nick:             "user1",
		Provider:         "openrouter",
		Model:            "claude-sonnet-4-5",
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		ToolCallsCount:   2,
		ToolDetails:      `[{"name":"shell","duration_ms":500,"status":"ok"}]`,
		LatencyMs:        1200,
		Iteration:        0,
		RequestType:      "chat",
		Status:           "ok",
	}

	id, err := db.InsertUsageStat(ctx, s)
	if err != nil {
		t.Fatalf("InsertUsageStat error: %v", err)
	}
	if id <= 0 {
		t.Errorf("expected positive id, got %d", id)
	}
}

func TestInsertUsageStat_WithError(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	errMsg := "rate limit exceeded"
	s := &UsageStat{
		Channel:      "#test",
		Nick:         "user1",
		Provider:     "openrouter",
		Model:        "claude-sonnet-4-5",
		LatencyMs:    500,
		RequestType:  "chat",
		Status:       "error",
		ErrorMessage: &errMsg,
	}

	id, err := db.InsertUsageStat(ctx, s)
	if err != nil {
		t.Fatalf("InsertUsageStat error: %v", err)
	}
	if id <= 0 {
		t.Errorf("expected positive id, got %d", id)
	}

	// Verify the error was stored.
	stats, total, err := db.ListStats(ctx, StatsQuery{})
	if err != nil {
		t.Fatalf("ListStats error: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected 1 stat, got %d", total)
	}
	if stats[0].Status != "error" {
		t.Errorf("expected status 'error', got %q", stats[0].Status)
	}
	if stats[0].ErrorMessage == nil || *stats[0].ErrorMessage != errMsg {
		t.Errorf("expected error message %q, got %v", errMsg, stats[0].ErrorMessage)
	}
}

func TestListStats_Filters(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Insert stats with different attributes.
	entries := []struct {
		channel     string
		nick        string
		provider    string
		requestType string
	}{
		{"#general", "alice", "openrouter", "chat"},
		{"#general", "bob", "kimi", "chat"},
		{"#dev", "alice", "openrouter", "task"},
		{"#dev", "bob", "kimi", "event"},
		{"#general", "alice", "openrouter", "summary"},
	}

	for i, e := range entries {
		s := &UsageStat{
			Channel:      e.channel,
			Nick:         e.nick,
			Provider:     e.provider,
			PromptTokens: (i + 1) * 10,
			TotalTokens:  (i + 1) * 15,
			LatencyMs:    int64((i + 1) * 100),
			RequestType:  e.requestType,
			Status:       "ok",
			ToolDetails:  "[]",
		}
		if _, err := db.InsertUsageStat(ctx, s); err != nil {
			t.Fatalf("InsertUsageStat %d error: %v", i, err)
		}
	}

	tests := []struct {
		name    string
		query   StatsQuery
		wantLen int
	}{
		{
			name:    "all stats",
			query:   StatsQuery{},
			wantLen: 5,
		},
		{
			name:    "filter by channel",
			query:   StatsQuery{Channel: "#general"},
			wantLen: 3,
		},
		{
			name:    "filter by nick",
			query:   StatsQuery{Nick: "alice"},
			wantLen: 3,
		},
		{
			name:    "filter by provider",
			query:   StatsQuery{Provider: "kimi"},
			wantLen: 2,
		},
		{
			name:    "filter by request type",
			query:   StatsQuery{RequestType: "chat"},
			wantLen: 2,
		},
		{
			name:    "combined filters",
			query:   StatsQuery{Channel: "#general", Nick: "alice"},
			wantLen: 2,
		},
		{
			name:    "no matches",
			query:   StatsQuery{Channel: "#nonexistent"},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats, total, err := db.ListStats(ctx, tt.query)
			if err != nil {
				t.Fatalf("ListStats error: %v", err)
			}
			if total != tt.wantLen {
				t.Errorf("expected total %d, got %d", tt.wantLen, total)
			}
			if len(stats) != tt.wantLen {
				t.Errorf("expected %d stats, got %d", tt.wantLen, len(stats))
			}
		})
	}
}

func TestListStats_Pagination(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Insert 10 stats.
	for i := 0; i < 10; i++ {
		s := &UsageStat{
			Channel:     "#test",
			Nick:        "user1",
			Provider:    "openrouter",
			TotalTokens: (i + 1) * 10,
			RequestType: "chat",
			Status:      "ok",
			ToolDetails: "[]",
		}
		if _, err := db.InsertUsageStat(ctx, s); err != nil {
			t.Fatalf("InsertUsageStat %d error: %v", i, err)
		}
	}

	// Page 1: limit 3, offset 0.
	stats, total, err := db.ListStats(ctx, StatsQuery{Limit: 3, Offset: 0})
	if err != nil {
		t.Fatalf("ListStats page 1 error: %v", err)
	}
	if total != 10 {
		t.Errorf("expected total 10, got %d", total)
	}
	if len(stats) != 3 {
		t.Errorf("expected 3 stats, got %d", len(stats))
	}

	// Page 2: limit 3, offset 3.
	stats2, total2, err := db.ListStats(ctx, StatsQuery{Limit: 3, Offset: 3})
	if err != nil {
		t.Fatalf("ListStats page 2 error: %v", err)
	}
	if total2 != 10 {
		t.Errorf("expected total 10, got %d", total2)
	}
	if len(stats2) != 3 {
		t.Errorf("expected 3 stats, got %d", len(stats2))
	}

	// Verify no overlap between pages.
	if len(stats) > 0 && len(stats2) > 0 && stats[0].ID == stats2[0].ID {
		t.Error("page 1 and page 2 returned the same first row")
	}

	// Limit cap: requesting > 200 should be capped.
	_, total3, err := db.ListStats(ctx, StatsQuery{Limit: 999})
	if err != nil {
		t.Fatalf("ListStats limit cap error: %v", err)
	}
	if total3 != 10 {
		t.Errorf("expected total 10, got %d", total3)
	}
}

func TestListStats_TimeRange(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Insert a stat with a known old timestamp.
	_, err := db.ExecContext(ctx,
		`INSERT INTO usage_stats (channel, nick, provider, prompt_tokens, total_tokens,
		 request_type, status, tool_details, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"#test", "user1", "openrouter", 100, 150, "chat", "ok", "[]",
		time.Now().Add(-48*time.Hour).UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		t.Fatalf("insert old stat error: %v", err)
	}

	// Insert a recent stat.
	s := &UsageStat{
		Channel:     "#test",
		Nick:        "user1",
		Provider:    "openrouter",
		TotalTokens: 200,
		RequestType: "chat",
		Status:      "ok",
		ToolDetails: "[]",
	}
	if _, err := db.InsertUsageStat(ctx, s); err != nil {
		t.Fatalf("InsertUsageStat error: %v", err)
	}

	// Query with From = 24 hours ago should only return the recent stat.
	from := time.Now().Add(-24 * time.Hour)
	stats, total, err := db.ListStats(ctx, StatsQuery{From: from})
	if err != nil {
		t.Fatalf("ListStats error: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 stat within time range, got %d", total)
	}
	if len(stats) != 1 {
		t.Errorf("expected 1 stat, got %d", len(stats))
	}
}

func TestAggregateStats_Daily(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Insert stats across 3 different days.
	days := []time.Duration{0, -24 * time.Hour, -48 * time.Hour}
	for i, offset := range days {
		ts := time.Now().Add(offset).UTC().Format("2006-01-02 15:04:05")
		_, err := db.ExecContext(ctx,
			`INSERT INTO usage_stats (channel, nick, provider, prompt_tokens, completion_tokens,
			 total_tokens, tool_calls_count, latency_ms, request_type, status, tool_details, timestamp)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"#test", "user1", "openrouter",
			(i+1)*100, (i+1)*50, (i+1)*150, i, int64((i+1)*1000),
			"chat", "ok", "[]", ts,
		)
		if err != nil {
			t.Fatalf("insert stat %d error: %v", i, err)
		}
	}

	results, err := db.AggregateStats(ctx, StatsQuery{}, "day")
	if err != nil {
		t.Fatalf("AggregateStats error: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 daily buckets, got %d", len(results))
	}

	// Verify each bucket has exactly 1 request.
	for _, r := range results {
		if r.TotalRequests != 1 {
			t.Errorf("bucket %s: expected 1 request, got %d", r.Period, r.TotalRequests)
		}
	}
}

func TestAggregateStats_Hourly(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Insert 3 stats in the same hour.
	now := time.Now().UTC()
	ts := now.Format("2006-01-02 15:04:05")
	for i := 0; i < 3; i++ {
		_, err := db.ExecContext(ctx,
			`INSERT INTO usage_stats (channel, nick, provider, prompt_tokens, total_tokens,
			 latency_ms, request_type, status, tool_details, timestamp)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"#test", "user1", "openrouter", 100, 150, 1000, "chat", "ok", "[]", ts,
		)
		if err != nil {
			t.Fatalf("insert stat %d error: %v", i, err)
		}
	}

	results, err := db.AggregateStats(ctx, StatsQuery{}, "hour")
	if err != nil {
		t.Fatalf("AggregateStats error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 hourly bucket, got %d", len(results))
	}
	if results[0].TotalRequests != 3 {
		t.Errorf("expected 3 requests in bucket, got %d", results[0].TotalRequests)
	}
	if results[0].TotalPromptTokens != 300 {
		t.Errorf("expected 300 prompt tokens, got %d", results[0].TotalPromptTokens)
	}
}

func TestAggregateStats_InvalidPeriod(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	_, err := db.AggregateStats(ctx, StatsQuery{}, "invalid")
	if err == nil {
		t.Error("expected error for invalid period")
	}
}

func TestTopTools(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Insert stats with tool_details.
	details1, _ := json.Marshal([]ToolDetail{
		{Name: "shell", DurationMs: 500, Status: "ok"},
		{Name: "web_search", DurationMs: 1200, Status: "ok"},
	})
	details2, _ := json.Marshal([]ToolDetail{
		{Name: "shell", DurationMs: 300, Status: "ok"},
		{Name: "shell", DurationMs: 800, Status: "error"},
	})
	details3, _ := json.Marshal([]ToolDetail{
		{Name: "web_search", DurationMs: 900, Status: "ok"},
	})

	for i, d := range []string{string(details1), string(details2), string(details3)} {
		s := &UsageStat{
			Channel:        "#test",
			Nick:           "user1",
			Provider:       "openrouter",
			ToolCallsCount: 2,
			ToolDetails:    d,
			RequestType:    "chat",
			Status:         "ok",
		}
		if i == 2 {
			s.ToolCallsCount = 1
		}
		if _, err := db.InsertUsageStat(ctx, s); err != nil {
			t.Fatalf("InsertUsageStat %d error: %v", i, err)
		}
	}

	results, err := db.TopTools(ctx, StatsQuery{})
	if err != nil {
		t.Fatalf("TopTools error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(results))
	}

	// shell should be first (3 calls) then web_search (2 calls).
	if results[0].Name != "shell" {
		t.Errorf("expected first tool 'shell', got %q", results[0].Name)
	}
	if results[0].Count != 3 {
		t.Errorf("expected shell count 3, got %d", results[0].Count)
	}
	if results[0].ErrorCount != 1 {
		t.Errorf("expected shell error count 1, got %d", results[0].ErrorCount)
	}
	if results[1].Name != "web_search" {
		t.Errorf("expected second tool 'web_search', got %q", results[1].Name)
	}
	if results[1].Count != 2 {
		t.Errorf("expected web_search count 2, got %d", results[1].Count)
	}
}

func TestProviderBreakdown(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Insert stats for different providers.
	providers := []struct {
		name   string
		tokens int
		count  int
	}{
		{"openrouter", 100, 3},
		{"kimi", 200, 2},
		{"ollama", 50, 1},
	}

	for _, p := range providers {
		for i := 0; i < p.count; i++ {
			s := &UsageStat{
				Channel:     "#test",
				Nick:        "user1",
				Provider:    p.name,
				TotalTokens: p.tokens,
				LatencyMs:   1000,
				RequestType: "chat",
				Status:      "ok",
				ToolDetails: "[]",
			}
			if _, err := db.InsertUsageStat(ctx, s); err != nil {
				t.Fatalf("InsertUsageStat error: %v", err)
			}
		}
	}

	results, err := db.ProviderBreakdown(ctx, StatsQuery{})
	if err != nil {
		t.Fatalf("ProviderBreakdown error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(results))
	}

	// Should be ordered by total_requests DESC: openrouter(3), kimi(2), ollama(1).
	if results[0].Provider != "openrouter" {
		t.Errorf("expected first provider 'openrouter', got %q", results[0].Provider)
	}
	if results[0].TotalRequests != 3 {
		t.Errorf("expected 3 requests for openrouter, got %d", results[0].TotalRequests)
	}
	if results[1].Provider != "kimi" {
		t.Errorf("expected second provider 'kimi', got %q", results[1].Provider)
	}
}

func TestGetStatsSummary(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Insert some stats.
	for i := 0; i < 5; i++ {
		status := "ok"
		var errMsg *string
		if i == 4 {
			status = "error"
			msg := "timeout"
			errMsg = &msg
		}
		s := &UsageStat{
			Channel:        "#test",
			Nick:           "user1",
			Provider:       "openrouter",
			TotalTokens:    100,
			ToolCallsCount: 1,
			LatencyMs:      1000,
			RequestType:    "chat",
			Status:         status,
			ErrorMessage:   errMsg,
			ToolDetails:    "[]",
		}
		if _, err := db.InsertUsageStat(ctx, s); err != nil {
			t.Fatalf("InsertUsageStat %d error: %v", i, err)
		}
	}

	summary, err := db.GetStatsSummary(ctx, StatsQuery{})
	if err != nil {
		t.Fatalf("GetStatsSummary error: %v", err)
	}
	if summary.TotalRequests != 5 {
		t.Errorf("expected 5 total requests, got %d", summary.TotalRequests)
	}
	if summary.TotalTokens != 500 {
		t.Errorf("expected 500 total tokens, got %d", summary.TotalTokens)
	}
	if summary.TotalToolCalls != 5 {
		t.Errorf("expected 5 total tool calls, got %d", summary.TotalToolCalls)
	}
	if summary.ErrorCount != 1 {
		t.Errorf("expected 1 error, got %d", summary.ErrorCount)
	}
	if summary.TopProvider != "openrouter" {
		t.Errorf("expected top provider 'openrouter', got %q", summary.TopProvider)
	}
	if summary.TopChannel != "#test" {
		t.Errorf("expected top channel '#test', got %q", summary.TopChannel)
	}
}

func TestGetStatsSummary_Empty(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	summary, err := db.GetStatsSummary(ctx, StatsQuery{})
	if err != nil {
		t.Fatalf("GetStatsSummary error: %v", err)
	}
	if summary.TotalRequests != 0 {
		t.Errorf("expected 0 total requests, got %d", summary.TotalRequests)
	}
	if summary.TotalTokens != 0 {
		t.Errorf("expected 0 total tokens, got %d", summary.TotalTokens)
	}
}

func TestCleanupStats(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Insert an old stat.
	_, err := db.ExecContext(ctx,
		`INSERT INTO usage_stats (channel, nick, provider, request_type, status, tool_details, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"#test", "user1", "openrouter", "chat", "ok", "[]",
		time.Now().Add(-120*24*time.Hour).UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		t.Fatalf("insert old stat error: %v", err)
	}

	// Insert a recent stat.
	s := &UsageStat{
		Channel:     "#test",
		Nick:        "user1",
		Provider:    "openrouter",
		RequestType: "chat",
		Status:      "ok",
		ToolDetails: "[]",
	}
	if _, err := db.InsertUsageStat(ctx, s); err != nil {
		t.Fatalf("InsertUsageStat error: %v", err)
	}

	// Cleanup stats older than 90 days.
	deleted, err := db.CleanupStats(ctx, 90)
	if err != nil {
		t.Fatalf("CleanupStats error: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	// Verify only the recent stat remains.
	stats, total, err := db.ListStats(ctx, StatsQuery{})
	if err != nil {
		t.Fatalf("ListStats error: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 remaining stat, got %d", total)
	}
	if len(stats) != 1 {
		t.Errorf("expected 1 stat, got %d", len(stats))
	}
}

func TestCleanupStats_InvalidRetention(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	tests := []struct {
		name string
		days int
	}{
		{"zero", 0},
		{"negative", -5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := db.CleanupStats(ctx, tt.days)
			if err == nil {
				t.Error("expected error for invalid retentionDays")
			}
		})
	}
}

func TestAggregateStats_WithFilters(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Insert stats for two providers.
	for i := 0; i < 4; i++ {
		provider := "openrouter"
		if i >= 2 {
			provider = "kimi"
		}
		s := &UsageStat{
			Channel:     "#test",
			Nick:        "user1",
			Provider:    provider,
			TotalTokens: 100,
			RequestType: "chat",
			Status:      "ok",
			ToolDetails: "[]",
		}
		if _, err := db.InsertUsageStat(ctx, s); err != nil {
			t.Fatalf("InsertUsageStat %d error: %v", i, err)
		}
	}

	// Aggregate only openrouter stats.
	results, err := db.AggregateStats(ctx, StatsQuery{Provider: "openrouter"}, "day")
	if err != nil {
		t.Fatalf("AggregateStats error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 daily bucket, got %d", len(results))
	}
	if results[0].TotalRequests != 2 {
		t.Errorf("expected 2 requests, got %d", results[0].TotalRequests)
	}
}

func TestTopTools_Empty(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	results, err := db.TopTools(ctx, StatsQuery{})
	if err != nil {
		t.Fatalf("TopTools error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 tools, got %d", len(results))
	}
}

func TestListStats_OrderDescending(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Insert stats with different timestamps.
	for i := 0; i < 3; i++ {
		ts := time.Now().Add(time.Duration(-i) * time.Hour).UTC().Format("2006-01-02 15:04:05")
		_, err := db.ExecContext(ctx,
			`INSERT INTO usage_stats (channel, nick, provider, total_tokens,
			 request_type, status, tool_details, timestamp)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			"#test", "user1", "openrouter", (i+1)*100, "chat", "ok", "[]", ts,
		)
		if err != nil {
			t.Fatalf("insert stat %d error: %v", i, err)
		}
	}

	stats, _, err := db.ListStats(ctx, StatsQuery{})
	if err != nil {
		t.Fatalf("ListStats error: %v", err)
	}
	if len(stats) < 2 {
		t.Fatalf("expected at least 2 stats, got %d", len(stats))
	}

	// Most recent should be first (highest tokens = 100, inserted with offset 0).
	if stats[0].TotalTokens != 100 {
		t.Errorf("expected most recent stat first (100 tokens), got %d", stats[0].TotalTokens)
	}
}

func TestInsertUsageStat_InvalidRequestType(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	s := &UsageStat{
		Channel:     "#test",
		Nick:        "user1",
		Provider:    "openrouter",
		RequestType: "invalid",
		Status:      "ok",
		ToolDetails: "[]",
	}

	_, err := db.InsertUsageStat(ctx, s)
	if err == nil {
		t.Error("expected error for invalid request_type")
	}
}

func TestInsertUsageStat_InvalidStatus(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	s := &UsageStat{
		Channel:     "#test",
		Nick:        "user1",
		Provider:    "openrouter",
		RequestType: "chat",
		Status:      "invalid",
		ToolDetails: "[]",
	}

	_, err := db.InsertUsageStat(ctx, s)
	if err == nil {
		t.Error("expected error for invalid status")
	}
}

func TestChannelCaseInsensitive(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Insert with mixed case.
	s := &UsageStat{
		Channel:     "#Test",
		Nick:        "user1",
		Provider:    "openrouter",
		RequestType: "chat",
		Status:      "ok",
		ToolDetails: "[]",
	}
	if _, err := db.InsertUsageStat(ctx, s); err != nil {
		t.Fatalf("InsertUsageStat error: %v", err)
	}

	// Query with lowercase should match.
	stats, total, err := db.ListStats(ctx, StatsQuery{Channel: "#test"})
	if err != nil {
		t.Fatalf("ListStats error: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 stat with case-insensitive channel match, got %d", total)
	}
	if len(stats) != 1 {
		t.Errorf("expected 1 stat, got %d", len(stats))
	}
}

func TestNickCaseInsensitive(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	// Insert with mixed case nick.
	s := &UsageStat{
		Channel:     "#test",
		Nick:        "Alice",
		Provider:    "openrouter",
		RequestType: "chat",
		Status:      "ok",
		ToolDetails: "[]",
	}
	if _, err := db.InsertUsageStat(ctx, s); err != nil {
		t.Fatalf("InsertUsageStat error: %v", err)
	}

	// Query with lowercase should match.
	stats, total, err := db.ListStats(ctx, StatsQuery{Nick: "alice"})
	if err != nil {
		t.Fatalf("ListStats error: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 stat with case-insensitive nick match, got %d", total)
	}
	if len(stats) != 1 {
		t.Errorf("expected 1 stat, got %d", len(stats))
	}
}

func TestInsertUsageStat_Nil(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()

	_, err := db.InsertUsageStat(ctx, nil)
	if err == nil {
		t.Error("expected error for nil stat")
	}
}

func TestPeriodToStrftime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		period  string
		wantErr bool
	}{
		{"hour", false},
		{"day", false},
		{"week", false},
		{"month", false},
		{"year", true},
		{"", true},
		{"invalid", true},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("period=%s", tt.period), func(t *testing.T) {
			_, err := periodToStrftime(tt.period)
			if (err != nil) != tt.wantErr {
				t.Errorf("periodToStrftime(%q) error = %v, wantErr %v", tt.period, err, tt.wantErr)
			}
		})
	}
}

func TestSortToolUsageStats(t *testing.T) {
	t.Parallel()

	stats := []ToolUsageStat{
		{Name: "web_search", Count: 2},
		{Name: "shell", Count: 5},
		{Name: "code_exec", Count: 5},
		{Name: "dns_lookup", Count: 1},
	}

	sortToolUsageStats(stats)

	// Should be sorted by count DESC, then name ASC.
	expected := []struct {
		name  string
		count int
	}{
		{"code_exec", 5},
		{"shell", 5},
		{"web_search", 2},
		{"dns_lookup", 1},
	}

	for i, e := range expected {
		if stats[i].Name != e.name || stats[i].Count != e.count {
			t.Errorf("index %d: expected {%s, %d}, got {%s, %d}",
				i, e.name, e.count, stats[i].Name, stats[i].Count)
		}
	}
}
