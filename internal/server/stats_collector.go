package server

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"murmur/internal/db"
)

const (
	// statsChannelSize is the buffer size for the stats recording channel.
	// A large buffer absorbs bursts without blocking the agent loop.
	statsChannelSize = 256

	// statsBatchSize is the maximum number of records to flush in a single
	// database transaction. Batching amortizes transaction overhead.
	statsBatchSize = 50

	// statsFlushInterval is the maximum time between flushes. Records are
	// flushed when either the batch size is reached or this interval elapses,
	// whichever comes first.
	statsFlushInterval = 5 * time.Second

	// statsFlushTimeout is the maximum duration for a single batch flush
	// (transaction begin + inserts + commit).
	statsFlushTimeout = 10 * time.Second

	// statsShutdownTimeout is the maximum time to wait for the background
	// goroutine to drain remaining records during graceful shutdown.
	statsShutdownTimeout = 10 * time.Second
)

// StatsCollector asynchronously writes usage statistics to the database.
// It uses a buffered channel and a background goroutine that batches inserts
// into single transactions. Record() is non-blocking — if the channel is full,
// the record is dropped and a counter is incremented.
type StatsCollector struct {
	database *db.DB
	ch       chan *db.UsageStat
	dropped  atomic.Int64
	wg       sync.WaitGroup
	logger   *slog.Logger
}

// NewStatsCollector creates a new StatsCollector and starts its background
// flush goroutine. The goroutine runs until ctx is cancelled, at which point
// it drains remaining records with a timeout. The caller should call Wait()
// after context cancellation to ensure all records are flushed.
// Both database and logger must be non-nil.
func NewStatsCollector(ctx context.Context, database *db.DB, logger *slog.Logger) *StatsCollector {
	if logger == nil {
		logger = slog.Default()
	}
	sc := &StatsCollector{
		database: database,
		ch:       make(chan *db.UsageStat, statsChannelSize),
		logger:   logger,
	}

	sc.wg.Add(1)
	go sc.run(ctx)

	return sc
}

// Record enqueues a usage stat for asynchronous writing. It is non-blocking:
// if the internal buffer is full, the record is dropped and the dropped counter
// is incremented. Record is safe to call from multiple goroutines. It is a
// no-op if sc is nil, allowing callers to skip nil checks.
func (sc *StatsCollector) Record(s *db.UsageStat) {
	if sc == nil || s == nil {
		return
	}

	select {
	case sc.ch <- s:
	default:
		dropped := sc.dropped.Add(1)
		if dropped%100 == 1 {
			sc.logger.Warn("stats collector: record dropped (buffer full)",
				"total_dropped", dropped,
			)
		}
	}
}

// Dropped returns the total number of records dropped due to a full buffer.
func (sc *StatsCollector) Dropped() int64 {
	if sc == nil {
		return 0
	}
	return sc.dropped.Load()
}

// Wait blocks until the background goroutine has finished, including the
// post-cancellation drain. Call this after the context passed to
// NewStatsCollector has been cancelled.
func (sc *StatsCollector) Wait() {
	if sc == nil {
		return
	}
	sc.wg.Wait()
}

// run is the background goroutine that batches and flushes records. It runs
// until ctx is cancelled, then drains remaining records with a timeout.
func (sc *StatsCollector) run(ctx context.Context) {
	defer sc.wg.Done()

	ticker := time.NewTicker(statsFlushInterval)
	defer ticker.Stop()

	batch := make([]*db.UsageStat, 0, statsBatchSize)

	for {
		select {
		case s := <-sc.ch:
			batch = append(batch, s)
			if len(batch) >= statsBatchSize {
				sc.flush(batch)
				batch = batch[:0]
			}

		case <-ticker.C:
			if len(batch) > 0 {
				sc.flush(batch)
				batch = batch[:0]
			}
			// Periodic observability: log queue depth and dropped count.
			queueLen := len(sc.ch)
			dropped := sc.dropped.Load()
			if queueLen > 0 || dropped > 0 {
				sc.logger.Debug("stats collector status",
					"queue_depth", queueLen,
					"total_dropped", dropped,
				)
			}

		case <-ctx.Done():
			// Flush any remaining batch items.
			if len(batch) > 0 {
				sc.flush(batch)
			}
			// Drain the channel with a timeout.
			sc.drain()
			return
		}
	}
}

// flush writes a batch of records to the database in a single transaction.
func (sc *StatsCollector) flush(batch []*db.UsageStat) {
	if len(batch) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), statsFlushTimeout)
	defer cancel()

	tx, err := sc.database.BeginTx(ctx, nil)
	if err != nil {
		sc.logger.Error("stats collector: begin transaction", "error", err)
		return
	}

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO usage_stats (channel, nick, provider, model, prompt_tokens, completion_tokens,
		 total_tokens, tool_calls_count, tool_details, latency_ms, iteration, request_type, status, error_message)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		sc.logger.Error("stats collector: prepare statement", "error", err)
		_ = tx.Rollback()
		return
	}
	defer stmt.Close()

	for _, s := range batch {
		_, err := stmt.ExecContext(ctx,
			s.Channel, s.Nick, s.Provider, s.Model,
			s.PromptTokens, s.CompletionTokens, s.TotalTokens,
			s.ToolCallsCount, s.ToolDetails, s.LatencyMs,
			s.Iteration, s.RequestType, s.Status, s.ErrorMessage,
		)
		if err != nil {
			sc.logger.Error("stats collector: insert stat", "error", err)
			// Continue with remaining records — don't abort the whole batch
			// for a single bad record.
		}
	}

	if err := tx.Commit(); err != nil {
		sc.logger.Error("stats collector: commit transaction", "error", err)
	}
}

// drainQuietPeriod is the duration to wait for late-arriving records after
// the channel appears empty during shutdown drain. This covers the race
// window where a concurrent Record() call is mid-send.
const drainQuietPeriod = 50 * time.Millisecond

// drain reads remaining records from the channel after context cancellation,
// flushing them in batches. It waits for a short quiet period after the
// channel appears empty to catch late-arriving records from concurrent
// Record() calls. It respects statsShutdownTimeout as an absolute deadline.
func (sc *StatsCollector) drain() {
	deadline := time.After(statsShutdownTimeout)
	batch := make([]*db.UsageStat, 0, statsBatchSize)

	for {
		select {
		case s := <-sc.ch:
			batch = append(batch, s)
			if len(batch) >= statsBatchSize {
				sc.flush(batch)
				batch = batch[:0]
			}

		case <-deadline:
			if len(batch) > 0 {
				sc.flush(batch)
			}
			remaining := len(sc.ch)
			if remaining > 0 {
				sc.logger.Warn("stats collector: shutdown timeout, records lost",
					"lost", remaining,
				)
			}
			return

		case <-time.After(drainQuietPeriod):
			// Channel has been empty for the quiet period — flush and exit.
			if len(batch) > 0 {
				sc.flush(batch)
			}
			return
		}
	}
}
