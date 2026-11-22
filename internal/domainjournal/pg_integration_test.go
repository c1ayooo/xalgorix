package domainjournal_test

// pg_integration_test.go — R6.1 DSN-gated integration: the journal write path
// through the real runstate store (the production Backend implementation),
// the read seam, and the audit projection must agree end to end
// (write → read back → projection consistent, PG_PRIMARY_STORAGE_SPEC §批次
// 切分 R6.1 acceptance). Skipped unless XALGORIX_TEST_DATABASE_URL is set
// (the runstate/infoitem integration test gate; CI gains a PG service with
// R6.1).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/runstate"
)

func integrationStore(t *testing.T) *runstate.Store {
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

func TestJournalBackendPGRoundTrip(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	// Scan/instance ids are unique per run: leftover rows on a shared test
	// database are inert for the assertions below (they filter by scan id)
	// and the runstate store exposes no exported row-delete surface for an
	// external test package to clean up with.
	scanID := fmt.Sprintf("dj-%d", time.Now().UnixNano())
	instanceID := "inst-dj-" + fmt.Sprint(time.Now().UnixNano())

	domainjournal.SetBackend(store)
	t.Cleanup(func() { domainjournal.SetBackend(nil) })

	dir := t.TempDir()
	journalPath := filepath.Join(dir, domainjournal.FileName)
	j, err := domainjournal.Open(journalPath, scanID)
	if err != nil {
		t.Fatal(err)
	}
	j.SetInstanceID(instanceID)

	// ── write: three records, one carrying an InfoItem; no file anywhere ──
	item := &infoitem.Item{
		ID:     "ii-" + scanID,
		ScanID: scanID,
		Nature: infoitem.NatureObserved,
		Coordinates: infoitem.Coordinates{
			Content:    infoitem.Content{Text: "HTTP 200 /admin"},
			Source:     infoitem.Source{Kind: "tool", Name: "http_request"},
			Subject:    infoitem.Subject{Kind: "endpoint", Identifier: "https://example.test/admin"},
			ObservedAt: time.Now().UTC(),
		},
		CreatedAt: time.Now().UTC(),
	}
	rec1, err := j.Append(domainjournal.Record{
		ScanID:       scanID,
		Actor:        domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "hook.ledger_seed"},
		Operation:    domainjournal.OpRecordHypothesis,
		HypothesisID: "H-1",
		Summary:      "recorded H-1",
		After:        &domainjournal.State{Status: "queued", Confidence: 0.5},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec2, err := j.Append(domainjournal.Record{
		ScanID:       scanID,
		Actor:        domainjournal.Actor{Kind: domainjournal.ActorAgent, ID: "agent_1", Source: "ledger_tool"},
		Operation:    domainjournal.OpClaimHypothesis,
		HypothesisID: "H-1",
		After:        &domainjournal.State{Status: "testing", AssignedTo: "agent_1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec3, err := j.Append(domainjournal.Record{
		ScanID:    scanID,
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "trafficproxy"},
		Operation: domainjournal.OpObserveTraffic,
		Summary:   "observed traffic",
		InfoItem:  item,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec1.Seq != 1 || rec2.Seq != 2 || rec3.Seq != 3 {
		t.Fatalf("seqs = %d,%d,%d", rec1.Seq, rec2.Seq, rec3.Seq)
	}
	if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
		t.Fatalf("backend mode must not write the journal file, stat err = %v", err)
	}

	// ── read back through the seam: same records, same order ──
	records, damaged, err := domainjournal.ReadRecordsResolved(ctx, journalPath, scanID)
	if err != nil {
		t.Fatal(err)
	}
	if damaged {
		t.Fatal("round trip must not report damage")
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3", len(records))
	}
	for i, want := range []domainjournal.Record{rec1, rec2, rec3} {
		got := records[i]
		if got.Seq != want.Seq || got.Operation != want.Operation || got.ScanID != scanID {
			t.Fatalf("record %d = %+v, want seq %d %s", i, got, want.Seq, want.Operation)
		}
	}
	if records[2].InfoItem == nil || records[2].InfoItem.ID != item.ID || records[2].InfoItem.SourceSeq != 3 {
		t.Fatalf("info item round trip = %+v", records[2].InfoItem)
	}

	// ── audit projection consistency (charter §5.2): complete claim history,
	// no fabricated gaps ──
	proj := domainjournal.Project(scanID, records)
	if proj.Incomplete || proj.Status != "complete" || proj.LastSeq != 3 {
		t.Fatalf("projection = incomplete:%v status:%q last:%d", proj.Incomplete, proj.Status, proj.LastSeq)
	}
	if len(proj.Claims) != 1 || proj.Claims[0].HypothesisID != "H-1" || proj.Claims[0].Actor.ID != "agent_1" {
		t.Fatalf("claims = %+v", proj.Claims)
	}
	if final, ok := proj.HypothesisFinal["H-1"]; !ok || final.Status != "testing" || final.AssignedTo != "agent_1" {
		t.Fatalf("hypothesis final = %+v", final)
	}

	// ── instance stamp: rows are queryable by the run identity (D11) ──
	instRecords, damaged, err := store.LoadJournalRecordsByInstance(ctx, instanceID)
	if err != nil {
		t.Fatal(err)
	}
	if damaged || len(instRecords) != 3 {
		t.Fatalf("instance rows = %d (damaged=%v), want 3", len(instRecords), damaged)
	}

	// ── source enumeration: the reconciler must see the stream ──
	sources, err := store.LoadJournalSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, src := range sources {
		if src.ScanID == scanID {
			found = true
			if src.LastSeq != 3 {
				t.Fatalf("source last seq = %d, want 3", src.LastSeq)
			}
		}
	}
	if !found {
		t.Fatalf("scan %s missing from journal sources: %+v", scanID, sources)
	}

	// ── continuation: reopening the journal resumes AFTER durable history
	// (a same-scan reopen must not re-use durable seqs — the backend's
	// idempotent upsert would silently rewrite audit history) ──
	reopened, err := domainjournal.Open(journalPath, scanID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.LastSeq() != 3 {
		t.Fatalf("reopened last seq = %d, want 3 (durable seed)", reopened.LastSeq())
	}
	rec4, err := reopened.Append(domainjournal.Record{
		ScanID:       scanID,
		Actor:        domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "hook.set_status"},
		Operation:    domainjournal.OpSetStatus,
		HypothesisID: "H-1",
		After:        &domainjournal.State{Status: "proven", Confidence: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec4.Seq != 4 {
		t.Fatalf("continuation seq = %d, want 4", rec4.Seq)
	}
	after, _, err := domainjournal.ReadRecordsResolved(ctx, journalPath, scanID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 4 {
		t.Fatalf("records after continuation = %d, want 4", len(after))
	}
	projAfter := domainjournal.Project(scanID, after)
	if projAfter.Incomplete || projAfter.LastSeq != 4 {
		t.Fatalf("projection after continuation = incomplete:%v last:%d", projAfter.Incomplete, projAfter.LastSeq)
	}
	if final := projAfter.HypothesisFinal["H-1"]; final.Status != "proven" {
		t.Fatalf("final status after continuation = %q, want proven", final.Status)
	}
}
