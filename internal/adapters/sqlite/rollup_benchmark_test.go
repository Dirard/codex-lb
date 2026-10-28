package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"codex-lb/internal/application"
)

// recordingRollupStore records every fold job of the service it serves.
type recordingRollupStore struct {
	*Store
	held, call []time.Duration
	rows       []int
}

func (r *recordingRollupStore) FoldUsageRollups(ctx context.Context, maxRows, maxJournal int) (application.UsageRollupFold, error) {
	started := time.Now()
	fold, err := r.Store.FoldUsageRollups(ctx, maxRows, maxJournal)
	r.call, r.held, r.rows = append(r.call, time.Since(started)), append(r.held, fold.Held), append(r.rows, fold.Rows)
	return fold, err
}

func durationQuantile(values []time.Duration, q float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return sorted[min(len(sorted)-1, int(q*float64(len(sorted))))]
}

// BenchmarkUsageRollupBackfill folds a synthetic production-size database
// (742 k events; design §3.5, §6.7) from an empty rollup state: fixed batch
// sizes, and the service with its adaptive batches and pauses. held is the
// fold job inside the write transaction, lock the whole call including BEGIN
// IMMEDIATE and COMMIT, which bounds how long the write lock is held per
// batch. Run with -benchtime=1x.
func BenchmarkUsageRollupBackfill(b *testing.B) {
	ctx := context.Background()
	store, err := Open(filepath.Join(b.TempDir(), "backfill.sqlite3"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	events := syntheticUsageEvents()
	if err := insertSyntheticUsageEvents(ctx, store.db, 0, events, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		b.Fatal(err)
	}
	report := func(b *testing.B, recorder *recordingRollupStore, total time.Duration) {
		b.ReportMetric(float64(durationQuantile(recorder.held, 0.5).Microseconds())/1000, "held-p50-ms")
		b.ReportMetric(float64(durationQuantile(recorder.held, 0.99).Microseconds())/1000, "held-p99-ms")
		b.ReportMetric(float64(durationQuantile(recorder.call, 0.5).Microseconds())/1000, "lock-p50-ms")
		b.ReportMetric(float64(durationQuantile(recorder.call, 0.99).Microseconds())/1000, "lock-p99-ms")
		var lock time.Duration
		for _, d := range recorder.call {
			lock += d
		}
		b.ReportMetric(lock.Seconds(), "lock-total-s")
		b.ReportMetric(total.Seconds(), "backfill-s")
		b.ReportMetric(float64(len(recorder.call)), "batches")
		b.ReportMetric(float64(events)/total.Seconds(), "events/s")
	}
	reset := func(b *testing.B) {
		b.StopTimer()
		if err := store.RebuildUsageRollups(ctx); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
	for _, batch := range []int{1000, 2000, 5000} {
		b.Run(fmt.Sprintf("batch=%d", batch), func(b *testing.B) {
			for range b.N {
				reset(b)
				recorder := &recordingRollupStore{Store: store}
				started := time.Now()
				for {
					fold, err := recorder.FoldUsageRollups(ctx, batch, batch)
					if err != nil || fold.Rebuild != "" {
						b.Fatalf("fold %+v %v", fold, err)
					}
					if !fold.Backlog {
						break
					}
				}
				report(b, recorder, time.Since(started))
			}
		})
	}
	b.Run("service", func(b *testing.B) {
		for range b.N {
			reset(b)
			recorder := &recordingRollupStore{Store: store}
			service := application.NewUsageRollupService(recorder, nil, nil)
			service.Checkpoint = func(ctx context.Context) error { _, err := store.CheckpointWAL(ctx); return err }
			started := time.Now()
			if err := service.Run(ctx); err != nil {
				b.Fatal(err)
			}
			total := time.Since(started)
			folded := 0
			for _, rows := range recorder.rows {
				folded += rows
			}
			if folded != events {
				b.Fatalf("service folded %d of %d events", folded, events)
			}
			report(b, recorder, total)
			b.ReportMetric(float64(recorder.rows[len(recorder.rows)/2]), "batch-rows-p50")
		}
	})
}
