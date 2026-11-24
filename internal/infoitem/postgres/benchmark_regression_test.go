package postgres

// Cross-scan retrieval reference benchmark (charter §10.1).
//
// Fixed reference profile:
//   - local single-instance PostgreSQL;
//   - 100,000 InfoItems, 300,000 relation edges;
//   - 16 concurrent read-only queries;
//   - at most 100 rows per query;
//   - SQL query + result assembly only (no LLM coordinate completion);
//   - warm-up excluded; p50/p95/p99 reported by the benchmark.
//
// Run with:
//
//	XALGORIX_TEST_DATABASE_URL=postgres://... \
//	  go test ./internal/infoitem/postgres -run '^$' -bench RegionQuery100k -benchtime=200x
//
// It is opt-in (no DSN = skip), so the default test suite stays database-free.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
)

const (
	benchmarkItems = 100000
	benchmarkEdges = 300000
	benchmarkBatch = 10000
)

func BenchmarkRegionQuery100k(b *testing.B) {
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		b.Skip("XALGORIX_TEST_DATABASE_URL not set (charter §10.1 reference benchmark)")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		b.Fatalf("migrate: %v", err)
	}
	store := New(pool)

	scanID := fmt.Sprintf("bench-region-%d", time.Now().UnixNano())
	b.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM info_item_edges WHERE scan_id = $1`, scanID)
		_, _ = store.pool.Exec(cleanupCtx, `DELETE FROM info_items WHERE scan_id = $1`, scanID)
	})

	b.StopTimer()
	if err := seedBenchmark(ctx, store, scanID); err != nil {
		b.Fatalf("seed: %v", err)
	}
	// Warm the cache and prove the filter shape before measurement.
	if _, err := store.Region(ctx, infoitem.RegionQuery{
		ScanIDs: []string{scanID}, Subject: benchSubject(0), Limit: 100,
	}); err != nil {
		b.Fatalf("warm-up region query: %v", err)
	}
	b.StartTimer()

	const concurrency = 16
	perWorker := b.N / concurrency
	if perWorker < 1 {
		perWorker = 1
	}
	latencies := make([][]time.Duration, concurrency)
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for w := 0; w < concurrency; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				start := time.Now()
				_, err := store.Region(ctx, infoitem.RegionQuery{
					ScanIDs: []string{scanID}, Subject: benchSubject(w*perWorker + i), Limit: 100,
				})
				if err != nil {
					b.Errorf("region query: %v", err)
					return
				}
				latencies[w] = append(latencies[w], time.Since(start))
			}
		}(w)
	}
	wg.Wait()

	all := make([]time.Duration, 0, concurrency*perWorker)
	for _, group := range latencies {
		all = append(all, group...)
	}
	if len(all) == 0 {
		b.Fatal("no samples")
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	report := func(name string, d time.Duration) {
		b.ReportMetric(float64(d.Microseconds())/1000, name)
	}
	b.ReportMetric(float64(len(all)), "samples")
	report("p50_ms", percentile(all, 0.50))
	report("p95_ms", percentile(all, 0.95))
	report("p99_ms", percentile(all, 0.99))
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func benchSubject(i int) string {
	return fmt.Sprintf("/bench/%03d", i%1000)
}

func seedBenchmark(ctx context.Context, store *Store, scanID string) error {
	now := time.Now().UTC()
	for start := 0; start < benchmarkItems; start += benchmarkBatch {
		rows := make([][]any, 0, benchmarkBatch)
		for i := start; i < start+benchmarkBatch && i < benchmarkItems; i++ {
			subject := benchSubject(i)
			content, _ := json.Marshal(map[string]string{"text": fmt.Sprintf("bench observation %d %s", i, subject)})
			coordinates, _ := json.Marshal(map[string]any{
				"content":     map[string]string{"text": "bench"},
				"subject":     map[string]string{"kind": "endpoint", "identifier": subject},
				"purpose":     "bench",
				"observed_at": now,
			})
			source, _ := json.Marshal(map[string]string{"kind": "bench"})
			subjectJSON, _ := json.Marshal(map[string]string{"kind": "endpoint", "identifier": subject})
			contextJSON, _ := json.Marshal(map[string]string{"bench": "1"})
			rows = append(rows, []any{
				fmt.Sprintf("ii-%08d", i), scanID, int64(i + 1), "observed",
				content, coordinates, source, subjectJSON, "bench", contextJSON,
				[]byte("[]"), []byte("[]"), 0.5, "complete", nil, nil, now, now,
			})
		}
		if _, err := store.pool.CopyFrom(ctx, pgx.Identifier{"info_items"}, []string{
			"id", "scan_id", "source_seq", "nature", "content", "coordinates", "source",
			"subject", "purpose", "context", "relation_ids", "supports", "confidence",
			"coordinate_status", "valid_from", "valid_until", "observed_at", "created_at",
		}, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
	}
	for start := 0; start < benchmarkEdges; start += benchmarkBatch {
		rows := make([][]any, 0, benchmarkBatch)
		for i := start; i < start+benchmarkBatch && i < benchmarkEdges; i++ {
			from := i % 1000
			to := (i + 1) % 1000
			if to == from {
				to = (i + 2) % 1000
			}
			rows = append(rows, []any{
				scanID, int64(i + 1),
				fmt.Sprintf("ii-%08d", from), fmt.Sprintf("ii-%08d", to),
				"same_subject", 1.0, now,
			})
		}
		if _, err := store.pool.CopyFrom(ctx, pgx.Identifier{"info_item_edges"}, []string{
			"scan_id", "source_seq", "from_id", "to_id", "type", "weight", "created_at",
		}, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
	}
	return nil
}
