package runstate

// DSN-gated integration tests (real SQL) for the R6.1/R6.2 shared read and
// append base: event windows across sibling sessions, journal append/read
// round-trips with the damaged-row contract, state-file continuity, and the
// watermark helpers. Skipped unless XALGORIX_TEST_DATABASE_URL is set.
//
// The fixture rows live under the "rs-it-" scan-id prefix; each test deletes
// its prefix first, so re-runs are deterministic on a persistent test DB.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
)

// cleanupScanRows removes every state row of the given scan ids so repeated
// runs on the persistent test database start from a known slate.
func cleanupScanRows(t *testing.T, store *Store, scanIDs ...string) {
	t.Helper()
	for _, id := range scanIDs {
		for _, q := range []string{
			`DELETE FROM run_events WHERE scan_id = $1`,
			`DELETE FROM run_journal WHERE scan_id = $1`,
			`DELETE FROM run_state_files WHERE scan_id = $1`,
			`DELETE FROM run_ledger_snapshots WHERE scan_id = $1`,
			`DELETE FROM run_state_watermarks WHERE scan_id = $1`,
		} {
			if _, err := store.pool.Exec(context.Background(), q, id); err != nil {
				t.Fatalf("cleanup %s: %v", id, err)
			}
		}
	}
}

func eventRow(seq uint64, typ string, at time.Time) EventRow {
	return EventRow{Seq: seq, Type: typ, OccurredAt: at, Payload: []byte(fmt.Sprintf(`{"type":%q,"timestamp":%q}`, typ, at.Format(time.RFC3339)))}
}

func TestEventReadsWindowsAcrossSiblings(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	scanA, scanB, scanC := "rs-it-ev-a", "rs-it-ev-b", "rs-it-ev-c"
	const inst, otherInst = "rs-inst-ev", "rs-inst-ev-other"
	cleanupScanRows(t, store, scanA, scanB, scanC)

	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	// Session A: three events 10:00–10:02.
	aRows := []EventRow{eventRow(1, "message", base), eventRow(2, "tool_call", base.Add(time.Minute)), eventRow(3, "tool_result", base.Add(2*time.Minute))}
	if err := store.PutEvents(ctx, scanA, inst, aRows); err != nil {
		t.Fatalf("put A: %v", err)
	}
	// Sibling session B: two events 11:00–11:01 (a later session).
	bRows := []EventRow{eventRow(1, "message", base.Add(time.Hour)), eventRow(2, "finished", base.Add(time.Hour).Add(time.Minute))}
	if err := store.PutEvents(ctx, scanB, inst, bRows); err != nil {
		t.Fatalf("put B: %v", err)
	}
	// Another run entirely — must stay out of the instance aggregate.
	if err := store.PutEvents(ctx, scanC, otherInst, []EventRow{eventRow(1, "message", base)}); err != nil {
		t.Fatalf("put C: %v", err)
	}

	// Per-scan: durable order + windowing + independent total.
	rows, total, err := store.LoadEventsByScan(ctx, scanA, 0, -1)
	if err != nil || total != 3 || len(rows) != 3 {
		t.Fatalf("full scan read: n=%d total=%d err=%v", len(rows), total, err)
	}
	for i, rec := range rows {
		if rec.ScanID != scanA || rec.Seq != uint64(i+1) || rec.Type != aRows[i].Type {
			t.Fatalf("row %d = %+v", i, rec)
		}
		if !rec.OccurredAt.Equal(aRows[i].OccurredAt) {
			t.Fatalf("row %d occurred_at = %v, want %v", i, rec.OccurredAt, aRows[i].OccurredAt)
		}
	}
	win, total, err := store.LoadEventsByScan(ctx, scanA, 1, 1)
	if err != nil || total != 3 || len(win) != 1 || win[0].Seq != 2 {
		t.Fatalf("window [1,2): %+v total=%d err=%v", win, total, err)
	}
	tail, total, err := store.LoadEventsByScan(ctx, scanA, 2, 1000)
	if err != nil || total != 3 || len(tail) != 1 || tail[0].Seq != 3 {
		t.Fatalf("overhanging window: %+v total=%d err=%v", tail, total, err)
	}
	empty, total, err := store.LoadEventsByScan(ctx, scanA, 9, 5)
	if err != nil || total != 3 || len(empty) != 0 {
		t.Fatalf("offset past end: %+v total=%d err=%v", empty, total, err)
	}

	// Instance aggregate: sessions chronological (A then B), seq within a
	// session, scan attribution on every row, union total.
	rows, total, err = store.LoadEventsByInstance(ctx, inst, 0, -1)
	if err != nil || total != 5 || len(rows) != 5 {
		t.Fatalf("instance union: n=%d total=%d err=%v", len(rows), total, err)
	}
	wantOrder := []struct {
		scan string
		seq  uint64
	}{{scanA, 1}, {scanA, 2}, {scanA, 3}, {scanB, 1}, {scanB, 2}}
	for i, w := range wantOrder {
		if rows[i].ScanID != w.scan || rows[i].Seq != w.seq {
			t.Fatalf("union row %d = %s/%d, want %s/%d", i, rows[i].ScanID, rows[i].Seq, w.scan, w.seq)
		}
	}
	win, total, err = store.LoadEventsByInstance(ctx, inst, 2, 2)
	if err != nil || total != 5 || len(win) != 2 || win[0].Seq != 3 || win[1].ScanID != scanB {
		t.Fatalf("union window [2,4): %+v total=%d err=%v", win, total, err)
	}

	// Other instance + unknown instance are honest empty reads.
	if rows, total, err = store.LoadEventsByInstance(ctx, otherInst, 0, -1); err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("other instance: %+v total=%d err=%v", rows, total, err)
	}
	if rows, total, err = store.LoadEventsByInstance(ctx, "rs-inst-none", 0, -1); err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("unknown instance: %+v total=%d err=%v", rows, total, err)
	}

	// Idempotency: re-put of the same (scan, seq) updates in place, no dup.
	aRows[1].Type = "tool_call_2"
	if err := store.PutEvents(ctx, scanA, inst, aRows); err != nil {
		t.Fatalf("re-put: %v", err)
	}
	if _, total, err = store.LoadEventsByScan(ctx, scanA, 0, -1); err != nil || total != 3 {
		t.Fatalf("after re-put: total=%d err=%v", total, err)
	}
	if rows, _, _ = store.LoadEventsByScan(ctx, scanA, 1, 1); len(rows) != 1 || rows[0].Type != "tool_call_2" {
		t.Fatalf("re-put did not update in place: %+v", rows)
	}
}

func journalRecord(scanID string, seq uint64, at time.Time, op domainjournal.Operation, hypID, summary string) domainjournal.Record {
	return domainjournal.Record{
		ScanID:       scanID,
		Seq:          seq,
		At:           at,
		Actor:        domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "runstate-it"},
		Operation:    op,
		HypothesisID: hypID,
		Summary:      summary,
	}
}

func TestJournalAppendAndReads(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	scanA, scanB := "rs-it-j-a", "rs-it-j-b"
	const inst = "rs-inst-j"
	cleanupScanRows(t, store, scanA, scanB)

	base := time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC)
	recs := []domainjournal.Record{
		journalRecord(scanA, 1, base, domainjournal.OpClaimHypothesis, "H-1", "claim H-1"),
		journalRecord(scanA, 2, base.Add(time.Minute), domainjournal.OpAddEvidence, "H-1", "evidence EV-1"),
		journalRecord(scanA, 3, base.Add(2*time.Minute), domainjournal.OpSetStatus, "H-1", "set status testing"),
	}
	for i, rec := range recs {
		if err := store.AppendJournalRecord(scanA, inst, rec); err != nil {
			t.Fatalf("append %d: %v", i+1, err)
		}
	}

	// Guard rails: seq must be positive and match the row's scan id.
	if err := store.AppendJournalRecord(scanA, inst, journalRecord(scanA, 0, base, domainjournal.OpSetStatus, "H-1", "")); err == nil {
		t.Fatalf("seq 0 accepted")
	}
	if err := store.AppendJournalRecord(scanA, inst, journalRecord("other-scan", 4, base, domainjournal.OpSetStatus, "H-1", "")); err == nil {
		t.Fatalf("scan_id mismatch accepted")
	}

	// Per-scan read: full decode in seq order.
	got, damaged, err := store.LoadJournalRecordsByScan(ctx, scanA)
	if err != nil || damaged {
		t.Fatalf("read scan: n=%d damaged=%v err=%v", len(got), damaged, err)
	}
	if len(got) != 3 {
		t.Fatalf("records = %d, want 3", len(got))
	}
	for i := range got {
		want := recs[i]
		have := got[i]
		if have.Seq != want.Seq || have.Operation != want.Operation || have.HypothesisID != want.HypothesisID ||
			have.Summary != want.Summary || have.Actor.Source != want.Actor.Source {
			t.Fatalf("record %d = %+v, want %+v", i, have, want)
		}
		// PG stores timestamptz at microsecond precision.
		if !have.At.Equal(want.At.Truncate(time.Microsecond)) {
			t.Fatalf("record %d at = %v, want %v", i, have.At, want.At)
		}
	}

	// Idempotency: re-append of the same seq rewrites in place, no duplicate.
	recs[1].Summary = "evidence EV-1 (confirmed)"
	if err := store.AppendJournalRecord(scanA, inst, recs[1]); err != nil {
		t.Fatalf("re-append: %v", err)
	}
	got, damaged, err = store.LoadJournalRecordsByScan(ctx, scanA)
	if err != nil || damaged || len(got) != 3 {
		t.Fatalf("after re-append: n=%d damaged=%v err=%v", len(got), damaged, err)
	}
	if got[1].Summary != "evidence EV-1 (confirmed)" {
		t.Fatalf("re-append did not rewrite the row: %s", got[1].Summary)
	}

	// Sibling session B at a later time; the instance union orders A then B.
	if err := store.AppendJournalRecord(scanB, inst, journalRecord(scanB, 1, base.Add(time.Hour), domainjournal.OpRecordHypothesis, "H-2", "record H-2")); err != nil {
		t.Fatalf("append B: %v", err)
	}
	union, damaged, err := store.LoadJournalRecordsByInstance(ctx, inst)
	if err != nil || damaged {
		t.Fatalf("union: n=%d damaged=%v err=%v", len(union), damaged, err)
	}
	if len(union) != 4 || union[0].ScanID != scanA || union[2].ScanID != scanA || union[3].ScanID != scanB {
		t.Fatalf("union order: %d rows, %+v", len(union), union)
	}

	// Damaged row: skipped, the rest of the history survives, flag surfaces.
	if _, err := store.pool.Exec(ctx,
		`INSERT INTO run_journal (scan_id, instance_id, seq, occurred_at, record) VALUES ($1, $2, 9, $3, 'not-json-on-purpose')`,
		scanA, inst, base.Add(3*time.Minute),
	); err != nil {
		t.Fatalf("inject damaged row: %v", err)
	}
	got, damaged, err = store.LoadJournalRecordsByScan(ctx, scanA)
	if err != nil || !damaged {
		t.Fatalf("damaged flag = %v err = %v", damaged, err)
	}
	if len(got) != 3 {
		t.Fatalf("damaged read kept %d records, want the 3 valid ones", len(got))
	}

	// Unknown instance is an honest empty read.
	if rows, damaged, err := store.LoadJournalRecordsByInstance(ctx, "rs-inst-none"); err != nil || damaged || len(rows) != 0 {
		t.Fatalf("unknown instance: %+v damaged=%v err=%v", rows, damaged, err)
	}
}

func TestStateFileLoadAndContinuity(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	scanA, scanB := "rs-it-sf-a", "rs-it-sf-b"
	const inst = "rs-inst-sf"
	cleanupScanRows(t, store, scanA, scanB)

	if _, ok, err := store.LoadStateFile(ctx, scanA, KindNotes); err != nil || ok {
		t.Fatalf("absent state file: ok=%v err=%v", ok, err)
	}
	if err := store.PutStateFile(ctx, scanA, KindNotes, inst, 1, []byte(`{"k":"v1"}`)); err != nil {
		t.Fatalf("put A: %v", err)
	}
	data, ok, err := store.LoadStateFile(ctx, scanA, KindNotes)
	if err != nil || !ok || string(data) != `{"k":"v1"}` {
		t.Fatalf("round-trip: ok=%v data=%s err=%v", ok, data, err)
	}

	// Upsert rewrites in place.
	if err := store.PutStateFile(ctx, scanA, KindNotes, inst, 1, []byte(`{"k":"v2"}`)); err != nil {
		t.Fatalf("put A v2: %v", err)
	}
	if data, _, _ = store.LoadStateFile(ctx, scanA, KindNotes); string(data) != `{"k":"v2"}` {
		t.Fatalf("upsert: %s", data)
	}

	// Sibling continuity: newest sibling wins, exclude-self never returns
	// its own row.
	if err := store.PutStateFile(ctx, scanB, KindNotes, inst, 1, []byte(`{"k":"sibling"}`)); err != nil {
		t.Fatalf("put B: %v", err)
	}
	data, source, ok, err := store.LoadLatestStateFileForInstance(ctx, inst, scanA, KindNotes)
	if err != nil || !ok || source != scanB || string(data) != `{"k":"sibling"}` {
		t.Fatalf("continuity: ok=%v source=%s data=%s err=%v", ok, source, data, err)
	}
	data, source, ok, err = store.LoadLatestStateFileForInstance(ctx, inst, scanB, KindNotes)
	if err != nil || !ok || source != scanA || string(data) != `{"k":"v2"}` {
		t.Fatalf("continuity excluding newest: ok=%v source=%s data=%s err=%v", ok, source, data, err)
	}
	if _, _, ok, err = store.LoadLatestStateFileForInstance(ctx, inst, scanA, KindCoverage); err != nil || ok {
		t.Fatalf("unknown kind: ok=%v err=%v", ok, err)
	}
	if _, _, ok, err = store.LoadLatestStateFileForInstance(ctx, "rs-inst-none", "", KindNotes); err != nil || ok {
		t.Fatalf("unknown instance: ok=%v err=%v", ok, err)
	}
	if _, _, ok, err = store.LoadLatestStateFileForInstance(ctx, "  ", "", KindNotes); err != nil || ok {
		t.Fatalf("empty instance: ok=%v err=%v", ok, err)
	}
}

func TestWatermarkHelpers(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	const scan = "rs-it-wm"
	cleanupScanRows(t, store, scan)

	// Advance writes an applied row.
	if err := store.AdvanceWatermark(ctx, scan, WatermarkKindEvents, "rs-inst-wm", 5); err != nil {
		t.Fatalf("advance: %v", err)
	}
	var applied int64
	var status, errText string
	if err := store.pool.QueryRow(ctx,
		`SELECT applied_seq, status, error FROM run_state_watermarks WHERE scan_id = $1 AND kind = $2`,
		scan, WatermarkKindEvents,
	).Scan(&applied, &status, &errText); err != nil {
		t.Fatalf("read watermark: %v", err)
	}
	if applied != 5 || status != StatusApplied || errText != "" {
		t.Fatalf("watermark = (%d, %s, %q)", applied, status, errText)
	}

	// Monotonic: a stale advance never pulls the position back.
	if err := store.AdvanceWatermark(ctx, scan, WatermarkKindEvents, "rs-inst-wm", 3); err != nil {
		t.Fatalf("stale advance: %v", err)
	}
	if err := store.pool.QueryRow(ctx,
		`SELECT applied_seq, status FROM run_state_watermarks WHERE scan_id = $1 AND kind = $2`,
		scan, WatermarkKindEvents,
	).Scan(&applied, &status); err != nil {
		t.Fatalf("re-read watermark: %v", err)
	}
	if applied != 5 || status != StatusApplied {
		t.Fatalf("after stale advance = (%d, %s)", applied, status)
	}

	// Failure records status=failed with the attempted seq (ledger precedent).
	if err := store.RecordWatermarkFailure(ctx, scan, WatermarkKindEvents, "rs-inst-wm", 6, errors.New("boom: pg down")); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	if err := store.pool.QueryRow(ctx,
		`SELECT applied_seq, status, error FROM run_state_watermarks WHERE scan_id = $1 AND kind = $2`,
		scan, WatermarkKindEvents,
	).Scan(&applied, &status, &errText); err != nil {
		t.Fatalf("read failure watermark: %v", err)
	}
	if applied != 6 || status != StatusFailed || errText != "boom: pg down" {
		t.Fatalf("failure watermark = (%d, %s, %q)", applied, status, errText)
	}

	// A nil cause records an empty error text.
	if err := store.RecordWatermarkFailure(ctx, scan, WatermarkKindJournal, "rs-inst-wm", 1, nil); err != nil {
		t.Fatalf("record nil-cause failure: %v", err)
	}
	if err := store.pool.QueryRow(ctx,
		`SELECT status, error FROM run_state_watermarks WHERE scan_id = $1 AND kind = $2`,
		scan, WatermarkKindJournal,
	).Scan(&status, &errText); err != nil {
		t.Fatalf("read journal watermark: %v", err)
	}
	if status != StatusFailed || errText != "" {
		t.Fatalf("nil-cause watermark = (%s, %q)", status, errText)
	}

	// The next advance through the failed window clears it.
	if err := store.AdvanceWatermark(ctx, scan, WatermarkKindEvents, "rs-inst-wm", 7); err != nil {
		t.Fatalf("recovery advance: %v", err)
	}
	if err := store.pool.QueryRow(ctx,
		`SELECT applied_seq, status, error FROM run_state_watermarks WHERE scan_id = $1 AND kind = $2`,
		scan, WatermarkKindEvents,
	).Scan(&applied, &status, &errText); err != nil {
		t.Fatalf("read recovered watermark: %v", err)
	}
	if applied != 7 || status != StatusApplied || errText != "" {
		t.Fatalf("recovered watermark = (%d, %s, %q)", applied, status, errText)
	}
}
