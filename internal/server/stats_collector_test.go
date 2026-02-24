package server

import (
	"context"
	"testing"
	"time"

	"murmur/internal/db"
)

// newTestStatsDB creates an in-memory database with all migrations applied.
func newTestStatsDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open error: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate error: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestStatsCollector_RecordAndFlush(t *testing.T) {
	t.Parallel()
	database := newTestStatsDB(t)
	logger := testLogger()

	ctx, cancel := context.WithCancel(context.Background())
	sc := NewStatsCollector(ctx, database, logger)

	// Record a stat.
	sc.Record(&db.UsageStat{
		Channel:     "#test",
		Nick:        "user1",
		Provider:    "openrouter",
		Model:       "claude-sonnet-4-5",
		TotalTokens: 100,
		RequestType: "chat",
		Status:      "ok",
		ToolDetails: "[]",
	})

	// Cancel and wait for flush.
	cancel()
	sc.Wait()

	// Verify the record was written.
	stats, total, err := database.ListStats(context.Background(), db.StatsQuery{})
	if err != nil {
		t.Fatalf("ListStats error: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 stat, got %d", total)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 stat row, got %d", len(stats))
	}
	if stats[0].Provider != "openrouter" {
		t.Errorf("expected provider 'openrouter', got %q", stats[0].Provider)
	}
	if stats[0].TotalTokens != 100 {
		t.Errorf("expected 100 total tokens, got %d", stats[0].TotalTokens)
	}
}

func TestStatsCollector_BatchFlush(t *testing.T) {
	t.Parallel()
	database := newTestStatsDB(t)
	logger := testLogger()

	ctx, cancel := context.WithCancel(context.Background())
	sc := NewStatsCollector(ctx, database, logger)

	// Record enough stats to trigger a batch flush (statsBatchSize = 50).
	for i := 0; i < 60; i++ {
		sc.Record(&db.UsageStat{
			Channel:     "#test",
			Nick:        "user1",
			Provider:    "openrouter",
			TotalTokens: i + 1,
			RequestType: "chat",
			Status:      "ok",
			ToolDetails: "[]",
		})
	}

	// Cancel and wait for flush.
	cancel()
	sc.Wait()

	// Verify all records were written.
	_, total, err := database.ListStats(context.Background(), db.StatsQuery{Limit: 200})
	if err != nil {
		t.Fatalf("ListStats error: %v", err)
	}
	if total != 60 {
		t.Errorf("expected 60 stats, got %d", total)
	}
}

func TestStatsCollector_TimerFlush(t *testing.T) {
	t.Parallel()
	database := newTestStatsDB(t)
	logger := testLogger()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sc := NewStatsCollector(ctx, database, logger)

	// Record a single stat (below batch threshold).
	sc.Record(&db.UsageStat{
		Channel:     "#test",
		Nick:        "user1",
		Provider:    "openrouter",
		TotalTokens: 42,
		RequestType: "chat",
		Status:      "ok",
		ToolDetails: "[]",
	})

	// Wait for the timer flush (statsFlushInterval = 5s, but we'll poll).
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for timer flush")
		default:
			_, total, err := database.ListStats(context.Background(), db.StatsQuery{})
			if err != nil {
				t.Fatalf("ListStats error: %v", err)
			}
			if total == 1 {
				// Success — timer flush wrote the record.
				cancel()
				sc.Wait()
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func TestStatsCollector_DroppedRecords(t *testing.T) {
	t.Parallel()
	database := newTestStatsDB(t)
	logger := testLogger()

	// Create a collector but don't start it — we'll fill the channel manually.
	// Actually, we need to use the real constructor but block the goroutine.
	// Instead, create a collector with a cancelled context so the goroutine
	// exits immediately, then try to record into the full channel.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	sc := NewStatsCollector(ctx, database, logger)
	sc.Wait() // Wait for goroutine to exit.

	// Now the channel is not being drained. Fill it up.
	for i := 0; i < statsChannelSize+10; i++ {
		sc.Record(&db.UsageStat{
			Channel:     "#test",
			Nick:        "user1",
			Provider:    "openrouter",
			TotalTokens: i,
			RequestType: "chat",
			Status:      "ok",
			ToolDetails: "[]",
		})
	}

	dropped := sc.Dropped()
	if dropped == 0 {
		t.Error("expected some dropped records, got 0")
	}
	// We sent statsChannelSize+10 records. The goroutine may have drained
	// some before exiting, so we can't predict the exact count. But at least
	// some should have been dropped since the goroutine exited.
	t.Logf("dropped %d records (channel size %d, sent %d)", dropped, statsChannelSize, statsChannelSize+10)
}

func TestStatsCollector_NilSafe(t *testing.T) {
	t.Parallel()

	// A nil collector should not panic.
	var sc *StatsCollector
	sc.Record(&db.UsageStat{
		Channel:     "#test",
		RequestType: "chat",
		Status:      "ok",
		ToolDetails: "[]",
	})
	sc.Wait()

	if sc.Dropped() != 0 {
		t.Error("expected 0 dropped for nil collector")
	}
}

func TestStatsCollector_NilStat(t *testing.T) {
	t.Parallel()
	database := newTestStatsDB(t)
	logger := testLogger()

	ctx, cancel := context.WithCancel(context.Background())
	sc := NewStatsCollector(ctx, database, logger)

	// Recording nil should be a no-op.
	sc.Record(nil)

	cancel()
	sc.Wait()

	// Verify nothing was written.
	_, total, err := database.ListStats(context.Background(), db.StatsQuery{})
	if err != nil {
		t.Fatalf("ListStats error: %v", err)
	}
	if total != 0 {
		t.Errorf("expected 0 stats, got %d", total)
	}
}

func TestStatsCollector_GracefulShutdown(t *testing.T) {
	t.Parallel()
	database := newTestStatsDB(t)
	logger := testLogger()

	ctx, cancel := context.WithCancel(context.Background())
	sc := NewStatsCollector(ctx, database, logger)

	// Record several stats.
	for i := 0; i < 10; i++ {
		sc.Record(&db.UsageStat{
			Channel:     "#test",
			Nick:        "user1",
			Provider:    "openrouter",
			TotalTokens: (i + 1) * 10,
			RequestType: "chat",
			Status:      "ok",
			ToolDetails: "[]",
		})
	}

	// Cancel and wait — all records should be flushed during drain.
	cancel()
	sc.Wait()

	_, total, err := database.ListStats(context.Background(), db.StatsQuery{})
	if err != nil {
		t.Fatalf("ListStats error: %v", err)
	}
	if total != 10 {
		t.Errorf("expected 10 stats after graceful shutdown, got %d", total)
	}
}

func TestStatsCollector_ErrorRecords(t *testing.T) {
	t.Parallel()
	database := newTestStatsDB(t)
	logger := testLogger()

	ctx, cancel := context.WithCancel(context.Background())
	sc := NewStatsCollector(ctx, database, logger)

	errMsg := "rate limit exceeded"
	sc.Record(&db.UsageStat{
		Channel:      "#test",
		Nick:         "user1",
		Provider:     "openrouter",
		Model:        "claude-sonnet-4-5",
		LatencyMs:    500,
		RequestType:  "chat",
		Status:       "error",
		ErrorMessage: &errMsg,
		ToolDetails:  "[]",
	})

	cancel()
	sc.Wait()

	stats, total, err := database.ListStats(context.Background(), db.StatsQuery{})
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
