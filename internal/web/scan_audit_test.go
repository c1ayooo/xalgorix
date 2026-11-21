package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/domainjournal"
)

// auditTestScan registers a scan record under the server's data dir so
// resolveScanDirByID can find it, mirroring how production scan dirs are laid
// out (scan.json at <dataDir>/<scanID>/scan.json).
func auditTestScan(t *testing.T, s *Server, id string) string {
	t.Helper()
	dir := filepath.Join(s.dataDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := &ScanRecord{
		ID:        id,
		Target:    "https://example.com",
		Status:    "finished",
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	s.saveScanRecordTo(rec, dir)
	return dir
}

func auditGet(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	switch {
	case filepath.Base(path) == "audit":
		s.handleScanAudit(rr, req)
	case filepath.Base(path) == "coldpath":
		s.handleScanColdPath(rr, req)
	default:
		t.Fatalf("unsupported audit path %q", path)
	}
	return rr
}

func mustAppend(t *testing.T, j *domainjournal.Journal, rec domainjournal.Record) domainjournal.Record {
	t.Helper()
	got, err := j.Append(rec)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// The audit projection must reconstruct the claim history — engine and model
// claims alike — from the durable journal, and report a complete scan as
// complete (charter §5.2: "谁在何时认领哪条 hypothesis" must be recoverable).
func TestScanAuditEndpoint_ReturnsClaimHistory(t *testing.T) {
	s := newTestServer(t, nil)
	id := "audit-claims-1"
	dir := auditTestScan(t, s, id)

	j, err := domainjournal.Open(filepath.Join(dir, domainjournal.FileName), id)
	if err != nil {
		t.Fatal(err)
	}
	mustAppend(t, j, domainjournal.Record{
		Actor:        domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "hook.ledger_seed"},
		Operation:    domainjournal.OpSeedHypothesis,
		HypothesisID: "H-1",
		After:        &domainjournal.State{Status: "queued", Confidence: 0.5},
		Summary:      "seed H-1",
	})
	mustAppend(t, j, domainjournal.Record{
		Actor:        domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "specialist_wave"},
		Operation:    domainjournal.OpClaimHypothesis,
		HypothesisID: "H-1",
		Before:       &domainjournal.State{Status: "queued"},
		After:        &domainjournal.State{Status: "testing", AssignedTo: "specialist-1"},
		Summary:      "engine wave claims H-1",
	})
	mustAppend(t, j, domainjournal.Record{
		Actor:        domainjournal.Actor{Kind: domainjournal.ActorAgent, ID: "agent_2", Source: "ledger_tool"},
		Operation:    domainjournal.OpClaimHypothesis,
		HypothesisID: "H-2",
		Before:       &domainjournal.State{Status: "queued"},
		After:        &domainjournal.State{Status: "testing", AssignedTo: "agent_2"},
		Summary:      "model claims H-2",
	})
	mustAppend(t, j, domainjournal.Record{
		Actor:        domainjournal.Actor{Kind: domainjournal.ActorAgent, ID: "agent_2", Source: "ledger_tool"},
		Operation:    domainjournal.OpSetStatus,
		HypothesisID: "H-1",
		Before:       &domainjournal.State{Status: "testing"},
		After:        &domainjournal.State{Status: "proven", Confidence: 1},
		Summary:      "H-1 proven",
	})

	rr := auditGet(t, s, "/api/scans/"+id+"/audit")
	if rr.Code != http.StatusOK {
		t.Fatalf("audit code = %d body=%s", rr.Code, rr.Body.String())
	}
	var view ScanAuditView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.JournalPresent || view.JournalDamaged {
		t.Fatalf("journal flags = present:%v damaged:%v", view.JournalPresent, view.JournalDamaged)
	}
	if view.Incomplete || view.Projection == nil || view.Projection.Status != "complete" {
		t.Fatalf("projection must be complete, got incomplete=%v status=%q", view.Incomplete, view.Projection.Status)
	}
	if view.Projection.LastSeq != 4 {
		t.Fatalf("last seq = %d, want 4", view.Projection.LastSeq)
	}
	if len(view.Projection.Claims) != 2 {
		t.Fatalf("claims = %+v, want exactly the two claim records", view.Projection.Claims)
	}
	if view.Projection.Claims[0].Actor.Kind != domainjournal.ActorEngine ||
		view.Projection.Claims[1].Actor.Kind != domainjournal.ActorAgent {
		t.Fatalf("claim actors = %+v, want engine then agent", view.Projection.Claims)
	}
	if view.Projection.Claims[1].HypothesisID != "H-2" || view.Projection.Claims[1].Actor.ID != "agent_2" {
		t.Fatalf("model claim = %+v, want H-2 by agent_2", view.Projection.Claims[1])
	}
	if got := view.Projection.HypothesisFinal["H-1"]; got.Status != "proven" {
		t.Fatalf("H-1 final state = %+v, want proven", got)
	}
}

// A sequence gap in the journal file must surface as an explicit incomplete
// projection with the gap range — never as a fabricated complete history.
func TestScanAuditEndpoint_ReportsSeqGapAsIncomplete(t *testing.T) {
	s := newTestServer(t, nil)
	id := "audit-gap-1"
	dir := auditTestScan(t, s, id)

	// Handcraft a journal with seq 1 and 3 (seq 2 lost) — Append can never
	// produce this, so write the JSONL directly.
	gapRecord := func(seq uint64) []byte {
		rec := domainjournal.Record{
			ScanID:       id,
			Seq:          seq,
			At:           time.Now().UTC(),
			Actor:        domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "hook.ledger_seed"},
			Operation:    domainjournal.OpSeedHypothesis,
			HypothesisID: "H-" + string(rune('0'+seq)),
			After:        &domainjournal.State{Status: "queued"},
		}
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		return append(data, '\n')
	}
	var buf []byte
	buf = append(buf, gapRecord(1)...)
	buf = append(buf, gapRecord(3)...)
	if err := os.WriteFile(filepath.Join(dir, domainjournal.FileName), buf, 0o600); err != nil {
		t.Fatal(err)
	}

	rr := auditGet(t, s, "/api/scans/"+id+"/audit")
	if rr.Code != http.StatusOK {
		t.Fatalf("audit code = %d body=%s", rr.Code, rr.Body.String())
	}
	var view ScanAuditView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Incomplete || view.Projection.Status != "incomplete" {
		t.Fatalf("gap must mark the projection incomplete, got %+v", view)
	}
	if len(view.Projection.Gaps) != 1 || view.Projection.Gaps[0].From != 2 || view.Projection.Gaps[0].To != 2 {
		t.Fatalf("gaps = %+v, want [{2 2}]", view.Projection.Gaps)
	}
}

// A scan without a journal file must be reported as incomplete/absent, not as
// an empty-but-complete history (charter §5.2: no fabricated history).
func TestScanAuditEndpoint_MissingJournalIsIncomplete(t *testing.T) {
	s := newTestServer(t, nil)
	id := "audit-absent-1"
	auditTestScan(t, s, id)

	rr := auditGet(t, s, "/api/scans/"+id+"/audit")
	if rr.Code != http.StatusOK {
		t.Fatalf("audit code = %d body=%s", rr.Code, rr.Body.String())
	}
	var view ScanAuditView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.JournalPresent {
		t.Fatal("journal_present must be false")
	}
	if !view.Incomplete || view.Note == "" {
		t.Fatalf("absent journal must be incomplete with a note, got %+v", view)
	}
	if view.Projection != nil {
		t.Fatal("absent journal must not carry a projection")
	}
	if rr := auditGet(t, s, "/api/scans/does-not-exist/audit"); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown scan code = %d, want 404", rr.Code)
	}
}

// Without a cold-path syncer the endpoint reports disabled (charter §6.6:
// report disabled, not failed), while still surfacing the local source
// position.
func TestScanColdPathEndpoint_DisabledWithoutSyncer(t *testing.T) {
	s := newTestServer(t, nil)
	id := "cold-disabled-1"
	dir := auditTestScan(t, s, id)
	j, err := domainjournal.Open(filepath.Join(dir, domainjournal.FileName), id)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		mustAppend(t, j, domainjournal.Record{
			Actor:        domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "hook.ledger_seed"},
			Operation:    domainjournal.OpSeedHypothesis,
			HypothesisID: "H-1",
			After:        &domainjournal.State{Status: "queued"},
		})
	}

	rr := auditGet(t, s, "/api/scans/"+id+"/coldpath")
	if rr.Code != http.StatusOK {
		t.Fatalf("coldpath code = %d body=%s", rr.Code, rr.Body.String())
	}
	var state ColdPathSyncState
	if err := json.Unmarshal(rr.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.ColdPath != "disabled" || state.State != "disabled" {
		t.Fatalf("cold path = %q state = %q, want disabled/disabled", state.ColdPath, state.State)
	}
	if state.SourceSeq != 2 {
		t.Fatalf("source seq = %d, want 2", state.SourceSeq)
	}
}

type stubSyncer struct {
	position ColdPathPosition
	err      error
}

func (s stubSyncer) SyncState(_ context.Context, _ string) (ColdPathPosition, error) {
	return s.position, s.err
}

// The pending/applied/gap/failed state machine is derived in the web layer
// from (source seq, watermark), so it is contract-testable without PostgreSQL
// (charter §6.4: the audit view must distinguish all four).
func TestScanColdPathEndpoint_DerivesSyncStates(t *testing.T) {
	s := newTestServer(t, nil)
	id := "cold-states-1"
	dir := auditTestScan(t, s, id)
	j, err := domainjournal.Open(filepath.Join(dir, domainjournal.FileName), id)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		mustAppend(t, j, domainjournal.Record{
			Actor:        domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: "hook.ledger_seed"},
			Operation:    domainjournal.OpSeedHypothesis,
			HypothesisID: "H-1",
			After:        &domainjournal.State{Status: "queued"},
		})
	}

	cases := []struct {
		name     string
		position ColdPathPosition
		err      error
		want     string
	}{
		{"nothing applied yet", ColdPathPosition{Status: "pending"}, nil, "pending"},
		{"fully applied", ColdPathPosition{AppliedSeq: 3, Status: "applied", UpdatedAt: time.Now().UTC()}, nil, "applied"},
		{"partial contiguous", ColdPathPosition{AppliedSeq: 1, Status: "gap"}, nil, "gap"},
		{"permanent failure", ColdPathPosition{Status: "failed", LastError: "apply boom"}, nil, "failed"},
		{"watermark read error", ColdPathPosition{}, errors.New("pg down"), "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s.SetColdPathSyncer(stubSyncer{position: tc.position, err: tc.err})
			rr := auditGet(t, s, "/api/scans/"+id+"/coldpath")
			if rr.Code != http.StatusOK {
				t.Fatalf("coldpath code = %d body=%s", rr.Code, rr.Body.String())
			}
			var state ColdPathSyncState
			if err := json.Unmarshal(rr.Body.Bytes(), &state); err != nil {
				t.Fatal(err)
			}
			if state.ColdPath != "enabled" {
				t.Fatalf("cold path = %q, want enabled", state.ColdPath)
			}
			if state.State != tc.want {
				t.Fatalf("state = %q, want %q (position=%+v err=%v)", state.State, tc.want, tc.position, tc.err)
			}
			if state.SourceSeq != 3 {
				t.Fatalf("source seq = %d, want 3", state.SourceSeq)
			}
			if tc.name == "permanent failure" && state.LastError != "apply boom" {
				t.Fatalf("last error = %q, want apply boom", state.LastError)
			}
		})
	}
}
