package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// UsageStat represents a single LLM API call record in the usage_stats table.
type UsageStat struct {
	ID               int64     `json:"id"`
	Timestamp        time.Time `json:"timestamp"`
	Channel          string    `json:"channel"`
	Nick             string    `json:"nick"`
	Provider         string    `json:"provider"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	ToolCallsCount   int       `json:"tool_calls_count"`
	ToolDetails      string    `json:"tool_details"`
	LatencyMs        int64     `json:"latency_ms"`
	Iteration        int       `json:"iteration"`
	RequestType      string    `json:"request_type"`
	Status           string    `json:"status"`
	ErrorMessage     *string   `json:"error_message,omitempty"`
}

// ToolDetail describes a single tool invocation within an LLM request.
// Stored as a JSON array in the tool_details column. Only safe metadata
// is captured — no arguments or output bodies.
type ToolDetail struct {
	Name       string `json:"name"`
	DurationMs int64  `json:"duration_ms"`
	Status     string `json:"status"` // "ok" or "error"
}

// StatsQuery specifies filters for querying usage statistics.
type StatsQuery struct {
	Channel     string
	Nick        string
	Provider    string
	RequestType string
	From        time.Time
	To          time.Time
	Limit       int
	Offset      int
}

// StatsAggregation holds a single time-bucket aggregation of usage statistics.
type StatsAggregation struct {
	Period                string  `json:"period"`
	TotalRequests         int     `json:"total_requests"`
	TotalPromptTokens     int64   `json:"total_prompt_tokens"`
	TotalCompletionTokens int64   `json:"total_completion_tokens"`
	TotalTokens           int64   `json:"total_tokens"`
	TotalToolCalls        int     `json:"total_tool_calls"`
	AvgLatencyMs          float64 `json:"avg_latency_ms"`
	ErrorCount            int     `json:"error_count"`
}

// ToolUsageStat holds aggregated tool usage data.
type ToolUsageStat struct {
	Name          string  `json:"name"`
	Count         int     `json:"count"`
	AvgDurationMs float64 `json:"avg_duration_ms"`
	ErrorCount    int     `json:"error_count"`
}

// ProviderStat holds aggregated per-provider statistics.
type ProviderStat struct {
	Provider              string  `json:"provider"`
	TotalRequests         int     `json:"total_requests"`
	TotalPromptTokens     int64   `json:"total_prompt_tokens"`
	TotalCompletionTokens int64   `json:"total_completion_tokens"`
	TotalTokens           int64   `json:"total_tokens"`
	TotalToolCalls        int     `json:"total_tool_calls"`
	AvgLatencyMs          float64 `json:"avg_latency_ms"`
	ErrorCount            int     `json:"error_count"`
}

// StatsSummary holds high-level summary statistics for the dashboard header.
type StatsSummary struct {
	TotalRequests  int     `json:"total_requests"`
	TotalTokens    int64   `json:"total_tokens"`
	TotalToolCalls int     `json:"total_tool_calls"`
	AvgLatencyMs   float64 `json:"avg_latency_ms"`
	ErrorCount     int     `json:"error_count"`
	TopProvider    string  `json:"top_provider"`
	TopChannel     string  `json:"top_channel"`
}

// InsertUsageStat stores a new usage statistics record. Returns the new row ID.
func (db *DB) InsertUsageStat(ctx context.Context, s *UsageStat) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("InsertUsageStat: stat must not be nil")
	}
	result, err := db.ExecContext(ctx,
		`INSERT INTO usage_stats (channel, nick, provider, model, prompt_tokens, completion_tokens,
		 total_tokens, tool_calls_count, tool_details, latency_ms, iteration, request_type, status, error_message)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.Channel, s.Nick, s.Provider, s.Model,
		s.PromptTokens, s.CompletionTokens, s.TotalTokens,
		s.ToolCallsCount, s.ToolDetails, s.LatencyMs,
		s.Iteration, s.RequestType, s.Status, s.ErrorMessage,
	)
	if err != nil {
		return 0, fmt.Errorf("InsertUsageStat: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("InsertUsageStat: last insert id: %w", err)
	}

	return id, nil
}

// ListStats returns usage statistics matching the query, ordered by timestamp
// descending (most recent first). Returns the matching rows and the total count
// of rows matching the filter (for pagination).
func (db *DB) ListStats(ctx context.Context, q StatsQuery) ([]UsageStat, int, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 200 {
		q.Limit = 200
	}

	where, args := buildStatsWhere(q)

	// Count total matching rows for pagination.
	countQuery := "SELECT COUNT(*) FROM usage_stats" + where
	var total int
	if err := db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("ListStats: count: %w", err)
	}

	// Fetch the page.
	selectQuery := `SELECT id, timestamp, channel, nick, provider, model,
		prompt_tokens, completion_tokens, total_tokens, tool_calls_count,
		tool_details, latency_ms, iteration, request_type, status, error_message
		FROM usage_stats` + where + ` ORDER BY timestamp DESC, id DESC LIMIT ? OFFSET ?`
	pageArgs := append(args, q.Limit, q.Offset)

	rows, err := db.QueryContext(ctx, selectQuery, pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("ListStats: %w", err)
	}
	defer rows.Close()

	var stats []UsageStat
	for rows.Next() {
		s, err := scanUsageStat(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("ListStats: scan: %w", err)
		}
		stats = append(stats, s)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("ListStats: rows: %w", err)
	}

	return stats, total, nil
}

// AggregateStats returns usage statistics aggregated into time buckets.
// Valid period values: "hour", "day", "week", "month". Results are ordered
// by period ascending (oldest first) for chart rendering.
func (db *DB) AggregateStats(ctx context.Context, q StatsQuery, period string) ([]StatsAggregation, error) {
	format, err := periodToStrftime(period)
	if err != nil {
		return nil, fmt.Errorf("AggregateStats: %w", err)
	}

	where, args := buildStatsWhere(q)

	query := fmt.Sprintf(`SELECT strftime('%s', timestamp) as period,
		COUNT(*) as total_requests,
		COALESCE(SUM(prompt_tokens), 0) as total_prompt_tokens,
		COALESCE(SUM(completion_tokens), 0) as total_completion_tokens,
		COALESCE(SUM(total_tokens), 0) as total_tokens,
		COALESCE(SUM(tool_calls_count), 0) as total_tool_calls,
		COALESCE(AVG(latency_ms), 0) as avg_latency_ms,
		COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0) as error_count
		FROM usage_stats%s
		GROUP BY period
		ORDER BY period ASC`, format, where)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("AggregateStats: %w", err)
	}
	defer rows.Close()

	var results []StatsAggregation
	for rows.Next() {
		var a StatsAggregation
		if err := rows.Scan(&a.Period, &a.TotalRequests, &a.TotalPromptTokens,
			&a.TotalCompletionTokens, &a.TotalTokens, &a.TotalToolCalls,
			&a.AvgLatencyMs, &a.ErrorCount); err != nil {
			return nil, fmt.Errorf("AggregateStats: scan: %w", err)
		}
		results = append(results, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("AggregateStats: rows: %w", err)
	}

	return results, nil
}

// TopTools returns the most-used tools by parsing the tool_details JSON column
// and aggregating across matching rows. Results are ordered by count descending.
func (db *DB) TopTools(ctx context.Context, q StatsQuery) ([]ToolUsageStat, error) {
	where, args := buildStatsWhere(q)

	// Fetch tool_details from all matching rows that have tool calls.
	var query string
	if where == "" {
		query = `SELECT tool_details FROM usage_stats WHERE tool_calls_count > 0`
	} else {
		query = `SELECT tool_details FROM usage_stats` + where + ` AND tool_calls_count > 0`
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("TopTools: %w", err)
	}
	defer rows.Close()

	type toolAgg struct {
		totalDuration int64
		count         int
		errorCount    int
	}
	agg := make(map[string]*toolAgg)

	for rows.Next() {
		var detailsJSON string
		if err := rows.Scan(&detailsJSON); err != nil {
			return nil, fmt.Errorf("TopTools: scan: %w", err)
		}

		var details []ToolDetail
		if err := json.Unmarshal([]byte(detailsJSON), &details); err != nil {
			slog.Warn("TopTools: skipping malformed tool_details JSON", "error", err)
			continue
		}

		for _, d := range details {
			a, ok := agg[d.Name]
			if !ok {
				a = &toolAgg{}
				agg[d.Name] = a
			}
			a.count++
			a.totalDuration += d.DurationMs
			if d.Status == "error" {
				a.errorCount++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("TopTools: rows: %w", err)
	}

	// Convert to sorted slice.
	results := make([]ToolUsageStat, 0, len(agg))
	for name, a := range agg {
		var avgDur float64
		if a.count > 0 {
			avgDur = float64(a.totalDuration) / float64(a.count)
		}
		results = append(results, ToolUsageStat{
			Name:          name,
			Count:         a.count,
			AvgDurationMs: avgDur,
			ErrorCount:    a.errorCount,
		})
	}

	// Sort by count descending.
	sortToolUsageStats(results)

	return results, nil
}

// ProviderBreakdown returns aggregated statistics grouped by LLM provider.
// Results are ordered by total requests descending.
func (db *DB) ProviderBreakdown(ctx context.Context, q StatsQuery) ([]ProviderStat, error) {
	where, args := buildStatsWhere(q)

	query := `SELECT provider,
		COUNT(*) as total_requests,
		COALESCE(SUM(prompt_tokens), 0) as total_prompt_tokens,
		COALESCE(SUM(completion_tokens), 0) as total_completion_tokens,
		COALESCE(SUM(total_tokens), 0) as total_tokens,
		COALESCE(SUM(tool_calls_count), 0) as total_tool_calls,
		COALESCE(AVG(latency_ms), 0) as avg_latency_ms,
		COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0) as error_count
		FROM usage_stats` + where + `
		GROUP BY provider
		ORDER BY total_requests DESC`

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ProviderBreakdown: %w", err)
	}
	defer rows.Close()

	var results []ProviderStat
	for rows.Next() {
		var p ProviderStat
		if err := rows.Scan(&p.Provider, &p.TotalRequests, &p.TotalPromptTokens,
			&p.TotalCompletionTokens, &p.TotalTokens, &p.TotalToolCalls,
			&p.AvgLatencyMs, &p.ErrorCount); err != nil {
			return nil, fmt.Errorf("ProviderBreakdown: scan: %w", err)
		}
		results = append(results, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ProviderBreakdown: rows: %w", err)
	}

	return results, nil
}

// GetStatsSummary returns a high-level summary of usage statistics matching
// the query. Used for the dashboard header cards.
func (db *DB) GetStatsSummary(ctx context.Context, q StatsQuery) (*StatsSummary, error) {
	where, args := buildStatsWhere(q)

	query := `SELECT
		COUNT(*) as total_requests,
		COALESCE(SUM(total_tokens), 0) as total_tokens,
		COALESCE(SUM(tool_calls_count), 0) as total_tool_calls,
		COALESCE(AVG(latency_ms), 0) as avg_latency_ms,
		COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0) as error_count
		FROM usage_stats` + where

	var s StatsSummary
	if err := db.QueryRowContext(ctx, query, args...).Scan(
		&s.TotalRequests, &s.TotalTokens, &s.TotalToolCalls,
		&s.AvgLatencyMs, &s.ErrorCount,
	); err != nil {
		return nil, fmt.Errorf("GetStatsSummary: %w", err)
	}

	// Top provider by request count.
	topProvQuery := `SELECT provider FROM usage_stats` + where + `
		GROUP BY provider ORDER BY COUNT(*) DESC LIMIT 1`
	if err := db.QueryRowContext(ctx, topProvQuery, args...).Scan(&s.TopProvider); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("GetStatsSummary: top provider: %w", err)
	}

	// Top channel by request count.
	topChanQuery := `SELECT channel FROM usage_stats` + where + `
		GROUP BY channel ORDER BY COUNT(*) DESC LIMIT 1`
	if err := db.QueryRowContext(ctx, topChanQuery, args...).Scan(&s.TopChannel); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("GetStatsSummary: top channel: %w", err)
	}

	return &s, nil
}

// CleanupStats deletes usage statistics older than the given number of days.
// retentionDays must be positive. Returns the number of deleted rows.
func (db *DB) CleanupStats(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, fmt.Errorf("CleanupStats: retentionDays must be positive, got %d", retentionDays)
	}

	result, err := db.ExecContext(ctx,
		`DELETE FROM usage_stats WHERE timestamp < datetime('now', ?)`,
		fmt.Sprintf("-%d days", retentionDays),
	)
	if err != nil {
		return 0, fmt.Errorf("CleanupStats: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("CleanupStats: rows affected: %w", err)
	}

	return n, nil
}

// buildStatsWhere constructs a WHERE clause and args from a StatsQuery.
// Returns the clause (including leading " WHERE") and the positional args.
// If no filters are set, returns an empty string and nil args.
func buildStatsWhere(q StatsQuery) (string, []any) {
	var conditions []string
	var args []any

	if q.Channel != "" {
		conditions = append(conditions, "channel = ?")
		args = append(args, q.Channel)
	}
	if q.Nick != "" {
		conditions = append(conditions, "nick = ?")
		args = append(args, q.Nick)
	}
	if q.Provider != "" {
		conditions = append(conditions, "provider = ?")
		args = append(args, q.Provider)
	}
	if q.RequestType != "" {
		conditions = append(conditions, "request_type = ?")
		args = append(args, q.RequestType)
	}
	if !q.From.IsZero() {
		conditions = append(conditions, "timestamp >= ?")
		args = append(args, q.From.UTC().Format("2006-01-02 15:04:05"))
	}
	if !q.To.IsZero() {
		conditions = append(conditions, "timestamp <= ?")
		args = append(args, q.To.UTC().Format("2006-01-02 15:04:05"))
	}

	if len(conditions) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

// strftimeFormats maps period names to SQLite strftime format strings.
// This is a closed whitelist — only these values are safe to interpolate
// into SQL queries. Never add user-supplied values to this map.
var strftimeFormats = map[string]string{
	"hour":  "%Y-%m-%d %H:00",
	"day":   "%Y-%m-%d",
	"week":  "%Y-W%W", // SQLite %W gives Monday-based week 00-53 (not ISO 8601).
	"month": "%Y-%m",
}

// periodToStrftime converts a human-readable period name to a SQLite strftime
// format string for GROUP BY aggregation. Only whitelisted period values are
// accepted; all others return an error.
func periodToStrftime(period string) (string, error) {
	format, ok := strftimeFormats[period]
	if !ok {
		return "", fmt.Errorf("invalid period %q: must be hour, day, week, or month", period)
	}
	return format, nil
}

// sortToolUsageStats sorts tool usage stats by count descending, then name ascending.
func sortToolUsageStats(stats []ToolUsageStat) {
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Count != stats[j].Count {
			return stats[i].Count > stats[j].Count
		}
		return stats[i].Name < stats[j].Name
	})
}

// scanUsageStat scans a single usage_stats row from a *sql.Rows cursor.
func scanUsageStat(rows *sql.Rows) (UsageStat, error) {
	var s UsageStat
	var errMsg sql.NullString

	if err := rows.Scan(&s.ID, &s.Timestamp, &s.Channel, &s.Nick, &s.Provider,
		&s.Model, &s.PromptTokens, &s.CompletionTokens, &s.TotalTokens,
		&s.ToolCallsCount, &s.ToolDetails, &s.LatencyMs, &s.Iteration,
		&s.RequestType, &s.Status, &errMsg); err != nil {
		return UsageStat{}, err
	}

	if errMsg.Valid {
		s.ErrorMessage = &errMsg.String
	}

	return s, nil
}
