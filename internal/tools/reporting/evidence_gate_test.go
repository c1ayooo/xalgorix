package reporting

import (
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// seedTrafficArchive appends one exchange and returns its sequence number.
func seedTrafficArchive(t *testing.T, dir string) int64 {
	t.Helper()
	archive := scanctx.NewTrafficArchive(dir)
	ex := scanctx.TrafficExchange{
		At:          time.Now().UTC(),
		Method:      "GET",
		URL:         "https://example.com/login?id=1",
		Status:      200,
		Channel:     "bearer",
		RequestRaw:  "GET /login?id=1 HTTP/1.1\r\nHost: example.com\r\n\r\n",
		ResponseRaw: "HTTP/1.1 200 OK\r\n\r\nerror in your SQL syntax near '1''",
	}
	seq, err := archive.Append(&ex)
	if err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	return seq
}

func TestEvidenceGateRejectsReportWithoutRefs(t *testing.T) {
	contextID := "rep-evidence-missing"
	dir := t.TempDir()
	CleanupContext(contextID)
	defer CleanupContext(contextID)

	sc := scanctx.New(contextID, dir)
	sc.RequireEvidenceRefs = true
	scanctx.Activate(sc)
	defer scanctx.Deactivate(contextID)
	seedTrafficArchive(t, dir)

	res, err := reportVulnWithContextID(contextID, validReportArgs())
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected transport error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "no resolvable wire evidence") {
		t.Fatalf("expected the evidence gate to reject, got:\n%s", res.Output)
	}
	if got := res.Metadata["report_outcome"]; got != "validation_rejected" {
		t.Fatalf("report_outcome = %v", got)
	}
	if got := getStoreByID(contextID).vulns; len(got) != 0 {
		t.Fatalf("rejected report must not persist a finding, count=%d", len(got))
	}
}

func TestEvidenceGateAcceptsExplicitRefAndBindsModelSource(t *testing.T) {
	contextID := "rep-evidence-explicit"
	dir := t.TempDir()
	CleanupContext(contextID)
	defer CleanupContext(contextID)

	sc := scanctx.New(contextID, dir)
	sc.RequireEvidenceRefs = true
	scanctx.Activate(sc)
	defer scanctx.Deactivate(contextID)
	seq := seedTrafficArchive(t, dir)

	args := validReportArgs()
	args["traffic_refs"] = "1" // sequence numbers start at 1 in a fresh archive
	_ = seq
	res, err := reportVulnWithContextID(contextID, args)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected transport error: %s", res.Error)
	}
	if strings.Contains(res.Output, "REJECTED") {
		t.Fatalf("explicit evidence ref should be accepted, got:\n%s", res.Output)
	}
	vulns := getStoreByID(contextID).vulns
	if len(vulns) != 1 {
		t.Fatalf("expected one finding, got %d", len(vulns))
	}
	if vulns[0].TrafficRefsSource != "model" {
		t.Fatalf("traffic refs source = %q, want model", vulns[0].TrafficRefsSource)
	}
	if len(vulns[0].TrafficRefs) != 1 || vulns[0].TrafficRefs[0].Seq != 1 {
		t.Fatalf("unexpected traffic refs: %+v", vulns[0].TrafficRefs)
	}
}

func TestEvidenceGateRejectsUnresolvableRef(t *testing.T) {
	contextID := "rep-evidence-badref"
	dir := t.TempDir()
	CleanupContext(contextID)
	defer CleanupContext(contextID)

	sc := scanctx.New(contextID, dir)
	sc.RequireEvidenceRefs = true
	scanctx.Activate(sc)
	defer scanctx.Deactivate(contextID)
	seedTrafficArchive(t, dir)

	args := validReportArgs()
	args["traffic_refs"] = "999"
	res, _ := reportVulnWithContextID(contextID, args)
	if !strings.Contains(res.Output, "none of the supplied evidence references resolved") {
		t.Fatalf("expected rejection for an unresolvable ref, got:\n%s", res.Output)
	}
}

func TestEvidenceGateDisabledByDefaultForSyntheticContexts(t *testing.T) {
	contextID := "rep-evidence-default-off"
	dir := t.TempDir()
	CleanupContext(contextID)
	defer CleanupContext(contextID)

	sc := scanctx.New(contextID, dir)
	scanctx.Activate(sc)
	defer scanctx.Deactivate(contextID)

	res, _ := reportVulnWithContextID(contextID, validReportArgs())
	if strings.Contains(res.Output, "no resolvable wire evidence") {
		t.Fatalf("synthetic contexts must keep the legacy behavior, got:\n%s", res.Output)
	}
}

// TestEvidenceGateDegradesWhenCaptureEmpty pins the fail-open edge: a run whose
// MITM archive holds zero records (capture off, or the runtime bypassed the
// proxy) cannot ever satisfy the reference requirement, so blocking would lose
// every real finding. The gate accepts and records the gap instead.
func TestEvidenceGateDegradesWhenCaptureEmpty(t *testing.T) {
	contextID := "rep-evidence-no-capture"
	dir := t.TempDir()
	CleanupContext(contextID)
	defer CleanupContext(contextID)

	sc := scanctx.New(contextID, dir)
	sc.RequireEvidenceRefs = true
	scanctx.Activate(sc)
	defer scanctx.Deactivate(contextID)

	res, err := reportVulnWithContextID(contextID, validReportArgs())
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected transport error: %s", res.Error)
	}
	if strings.Contains(res.Output, "REJECTED") {
		t.Fatalf("empty capture plane must not reject findings, got:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "没有任何 MITM 流量记录") {
		t.Fatalf("finding must carry the capture-unavailable note, got:\n%s", res.Output)
	}
	if got := len(getStoreByID(contextID).vulns); got != 1 {
		t.Fatalf("finding not persisted (count=%d)", got)
	}
}
