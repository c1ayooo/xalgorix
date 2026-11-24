package domainjournal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
)

func testRecord(scanID string) Record {
	return Record{
		ScanID:       scanID,
		Actor:        Actor{Kind: ActorEngine, ID: "coordinator", Source: "test"},
		Operation:    OpRecordHypothesis,
		HypothesisID: "H-1",
		Summary:      "recorded",
		After:        &State{Status: "queued", Confidence: 0.5},
	}
}

func TestJournalAppendReopenAndRepairTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	j, err := Open(path, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	first, err := j.Append(testRecord("scan-1"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Seq != 1 {
		t.Fatalf("first seq = %d, want 1", first.Seq)
	}
	second, err := j.Append(Record{
		Actor:        Actor{Kind: ActorAgent, ID: "specialist-1"},
		Operation:    OpClaimHypothesis,
		HypothesisID: "H-1",
		Summary:      "claimed",
		Before:       &State{Status: "queued"},
		After:        &State{Status: "testing", AssignedTo: "specialist-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Seq != 2 || second.ScanID != "scan-1" {
		t.Fatalf("unexpected second record: %+v", second)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"scan_id":"scan-1","seq":3`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	reopened, err := Open(path, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.LastSeq() != 2 {
		t.Fatalf("last seq after repair = %d, want 2", reopened.LastSeq())
	}
	third, err := reopened.Append(Record{
		Actor:     Actor{Kind: ActorSystem, ID: "test"},
		Operation: OpRecordAttempt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if third.Seq != 3 {
		t.Fatalf("third seq = %d, want 3", third.Seq)
	}
}

func TestJournalRejectsScanMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	j, err := Open(path, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(testRecord("scan-2")); err == nil {
		t.Fatal("expected scan mismatch")
	}
}

func TestJournalAppendsInfoItemWithRecordSeq(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	j, err := Open(path, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	item := infoitem.Item{
		ID:     "ii-1",
		ScanID: "scan-1",
		Nature: infoitem.NatureObserved,
		Coordinates: infoitem.Coordinates{
			Content: infoitem.Content{Text: "HTTP 200"},
			Source:  infoitem.Source{Kind: "tool", Name: "http_request"},
			Subject: infoitem.Subject{Kind: "endpoint", Identifier: "https://example.test/"},
		},
		CreatedAt: time.Now().UTC(),
	}
	rec, err := j.Append(Record{
		ScanID:    "scan-1",
		Actor:     Actor{Kind: ActorAgent, ID: "a-1"},
		Operation: OpInfoItemProjected,
		InfoItem:  &item,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.InfoItem == nil || rec.InfoItem.SourceSeq != rec.Seq {
		t.Fatalf("info item seq not assigned: %+v", rec.InfoItem)
	}
}

func TestProjectReportsGapsDisorderAndClaims(t *testing.T) {
	now := time.Now().UTC()
	records := []Record{
		{
			ScanID: "s", Seq: 3, At: now, Actor: Actor{Kind: ActorEngine}, Operation: OpSetStatus,
			HypothesisID: "H-1", After: &State{Status: "proven"},
		},
		{
			ScanID: "s", Seq: 1, At: now, Actor: Actor{Kind: ActorEngine}, Operation: OpRecordHypothesis,
			HypothesisID: "H-1", After: &State{Status: "queued"},
		},
		{
			ScanID: "s", Seq: 3, At: now, Actor: Actor{Kind: ActorAgent}, Operation: OpClaimHypothesis,
			HypothesisID: "H-1", After: &State{Status: "testing", AssignedTo: "agent-1"},
		},
	}
	p := Project("s", records)
	if !p.Incomplete || !p.Disordered {
		t.Fatalf("expected incomplete/disordered projection: %+v", p)
	}
	if len(p.DuplicateSeqs) != 1 || p.DuplicateSeqs[0] != 3 {
		t.Fatalf("duplicates = %v", p.DuplicateSeqs)
	}
	if len(p.Gaps) != 1 || p.Gaps[0].From != 2 || p.Gaps[0].To != 2 {
		t.Fatalf("gaps = %+v", p.Gaps)
	}
	if len(p.Claims) != 1 || p.Claims[0].HypothesisID != "H-1" {
		t.Fatalf("claims = %+v", p.Claims)
	}
	if got := p.HypothesisFinal["H-1"].Status; got != "testing" {
		t.Fatalf("final status = %q, want testing", got)
	}
}

func TestJournalNotifiesObserverAfterAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	j, err := Open(path, "scan-1")
	if err != nil {
		t.Fatal(err)
	}
	type event struct {
		path, scanID string
		seq          uint64
	}
	ch := make(chan event, 1)
	cancel := RegisterObserver(func(p, scanID string, seq uint64) {
		ch <- event{path: p, scanID: scanID, seq: seq}
	})
	defer cancel()

	if _, err := j.Append(testRecord("scan-1")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-ch:
		if got.path != path || got.scanID != "scan-1" || got.seq != 1 {
			t.Fatalf("unexpected observer event: %+v", got)
		}
	default:
		t.Fatal("observer was not notified")
	}
}

func TestReadRecordsRejectsMalformedCompleteLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	data, err := json.Marshal(testRecord("scan-1"))
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	data = append(data, []byte("not-json\n")...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadRecords(path); err == nil {
		t.Fatal("expected malformed complete line error")
	}
}

func TestProjectCollectsDeviations(t *testing.T) {
	now := time.Now().UTC()
	recs := []Record{
		{ScanID: "s", Seq: 1, At: now, Actor: Actor{Kind: ActorAgent, ID: "a"}, Operation: OpRecordDeviation, IntentID: "T-1", Summary: "target redirected"},
		{ScanID: "s", Seq: 2, At: now, Actor: Actor{Kind: ActorAgent, ID: "a"}, Operation: OpSetStatus, HypothesisID: "H-1", Summary: "testing"},
	}
	p := Project("s", recs)
	if len(p.Deviations) != 1 {
		t.Fatalf("deviations = %+v", p.Deviations)
	}
	d := p.Deviations[0]
	if d.IntentID != "T-1" || d.Summary != "target redirected" || d.Seq != 1 {
		t.Fatalf("deviation = %+v", d)
	}
}
