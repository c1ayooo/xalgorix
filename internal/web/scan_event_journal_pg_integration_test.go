package web

// DSN-gated integration test for the R6.1 events slice: the journal write
// path lands in run_events and the run-scoped readers aggregate TWO SIBLING
// SESSIONS of one run (the D11 union the panels must not regress on).
// Skipped unless XALGORIX_TEST_DATABASE_URL is set (the same gate as the
// internal/runstate integration tests; CI gains a PG service with R6.1).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/runstate"
)

func webEventIntegrationStore(t *testing.T) *runstate.Store {
	t.Helper()
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set")
	}
	store, err := runstate.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open runstate store: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func TestRunEventsSiblingAggregationAcrossSessions(t *testing.T) {
	store := webEventIntegrationStore(t)
	ctx := context.Background()

	// Unique ids per run: the integration database is shared.
	stamp := time.Now().UnixNano()
	instance := fmt.Sprintf("r61ev-run-%d", stamp)
	scanOld := fmt.Sprintf("r61ev-%d-old", stamp)
	scanNew := fmt.Sprintf("r61ev-%d-new", stamp)
	t.Cleanup(func() {
		_ = store.DeleteEventsByScan(context.Background(), scanOld)
		_ = store.DeleteEventsByScan(context.Background(), scanNew)
	})

	// Two sibling session directories of one run. The journal identity is
	// the directory basename (the importer's scan_id), so session dirs are
	// named by their scan id — the shape the engine itself produces.
	parent := t.TempDir()
	dirOld := writeSiblingDirForJournal(t, parent, scanOld, scanOld, instance)
	dirNew := writeSiblingDirForJournal(t, parent, scanNew, scanNew, instance)
	oldStart := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	newStart := oldStart.Add(time.Hour)

	// Session OLD goes through the real journal write path: prepare seeds the
	// (empty) durable stream, appends land in run_events.
	SetEventStateBackend(store)
	t.Cleanup(func() { SetEventStateBackend(nil) })
	recOld := &ScanRecord{ID: scanOld, InstanceID: instance, EventsJournal: true}
	if err := prepareScanEventJournal(dirOld, recOld, false); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	evOld1 := WSEvent{Type: "message", Content: "old-1", Timestamp: oldStart.Format(time.RFC3339)}
	evOld2 := WSEvent{Type: "tool_result", Output: "old-2", Timestamp: oldStart.Add(time.Second).Format(time.RFC3339)}
	if err := appendScanEventJournal(dirOld, evOld1); err != nil {
		t.Fatalf("append old-1: %v", err)
	}
	if err := appendScanEventJournal(dirOld, evOld2); err != nil {
		t.Fatalf("append old-2: %v", err)
	}

	// Session NEW is seeded directly through the store (the shape a sibling
	// session or the importer produces) — same instance id, different scan.
	rows := []runstate.EventRow{
		{Seq: 1, Type: "message", OccurredAt: newStart, Payload: mustJSON(t, WSEvent{Type: "message", Content: "new-1", Timestamp: newStart.Format(time.RFC3339)})},
		{Seq: 2, Type: "message", OccurredAt: newStart.Add(time.Second), Payload: mustJSON(t, WSEvent{Type: "message", Content: "new-2", Timestamp: newStart.Add(time.Second).Format(time.RFC3339)})},
	}
	if err := store.PutEvents(ctx, scanNew, instance, rows); err != nil {
		t.Fatalf("seed sibling session: %v", err)
	}

	// The run-scoped window aggregates BOTH sessions, oldest session first,
	// with offset/limit indexing into the union.
	window, total, ok := loadRunEventsWindow([]string{dirOld, dirNew}, 0, 10)
	if !ok || total != 4 || len(window) != 4 {
		t.Fatalf("sibling union wrong: ok=%t total=%d len=%d", ok, total, len(window))
	}
	contents := make([]string, 0, len(window))
	for _, ev := range window {
		contents = append(contents, firstTranscriptContent(ev))
	}
	if strings.Join(contents, ",") != "old-1,old-2,new-1,new-2" {
		t.Fatalf("sibling order wrong: %v", contents)
	}
	if page, total, ok := loadRunEventsWindow([]string{dirOld, dirNew}, 1, 2); !ok || total != 4 || len(page) != 2 || firstTranscriptContent(page[0]) != "old-2" || firstTranscriptContent(page[1]) != "new-1" {
		t.Fatalf("sibling paging wrong: ok=%t total=%d %+v", ok, total, page)
	}

	// The session-scoped window stays scoped to its own scan_id.
	if evs, total, ok := loadScanEventsWindow(dirOld, 0, 10); !ok || total != 2 || len(evs) != 2 || firstTranscriptContent(evs[1]) != "old-2" {
		t.Fatalf("session window wrong: ok=%t total=%d", ok, total)
	}

	// The walk route (detail tail / stop rewrite / finding upgrades) reads
	// the durable rows of its session.
	var walked []string
	if _, _, err := walkEventJournal(dirOld, func(_ int, ev WSEvent) { walked = append(walked, firstTranscriptContent(ev)) }); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if strings.Join(walked, ",") != "old-1,old-2" {
		t.Fatalf("walk order wrong: %v", walked)
	}

	// The chat transcript folds the same instance union into one timeline:
	// message turns render as Text, tool results as Output. The union total
	// also proves the rows carry the instance stamp the aggregate keys on.
	seen := map[string]struct{}{}
	var items []transcriptItem
	if !(&Server{}).appendRunStateTranscript(store, instance, seen, &items) {
		t.Fatal("transcript did not fold the durable union")
	}
	if len(items) != 4 || items[0].Text != "old-1" || items[1].Output != "old-2" || items[2].Text != "new-1" || items[3].Text != "new-2" {
		t.Fatalf("transcript union wrong: %+v", items)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// firstTranscriptContent returns the content-ish field of an event for order
// assertions across message/tool_result shapes.
func firstTranscriptContent(ev WSEvent) string {
	if ev.Content != "" {
		return ev.Content
	}
	return ev.Output
}
