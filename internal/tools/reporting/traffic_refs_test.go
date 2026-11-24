package reporting

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// A finding reported for a run with a matching MITM-archived exchange must
// persist the exact (session, seq) evidence ref at confirmation time, so the
// report UI renders the wire packet the finding was proven with rather than a
// later endpoint heuristic.
func TestReportBindsTrafficEvidenceRefs(t *testing.T) {
	contextID := "rep-traffic-evidence-refs"
	dir := t.TempDir()
	CleanupContext(contextID)
	defer CleanupContext(contextID)

	sc := scanctx.New(contextID, dir)
	scanctx.Activate(sc)
	defer scanctx.Deactivate(contextID)

	archive := scanctx.NewTrafficArchive(dir)
	ex := scanctx.TrafficExchange{
		At:          time.Now().UTC(),
		Method:      "GET",
		URL:         "https://example.com/login?id=1",
		Status:      200,
		Channel:     "bearer",
		RequestRaw:  "GET /login?id=1 HTTP/1.1\r\nHost: example.com\r\n\r\n",
		ResponseRaw: "HTTP/1.1 200 OK\r\n\r\nadmin:secret",
	}
	if _, err := archive.Append(&ex); err != nil {
		t.Fatalf("seed archive: %v", err)
	}

	res, err := reportVulnWithContextID(contextID, validReportArgs())
	if err != nil || res.Error != "" {
		t.Fatalf("report failed: %+v %v", res, err)
	}
	vulns := GetVulnerabilitiesForContext(contextID)
	if len(vulns) != 1 {
		t.Fatalf("stored findings = %d, want 1", len(vulns))
	}
	refs := vulns[0].TrafficRefs
	if len(refs) != 1 {
		t.Fatalf("traffic refs = %+v, want one pinned exchange", refs)
	}
	if refs[0].Session != filepath.Base(dir) || refs[0].Seq != 1 {
		t.Fatalf("ref identity = %+v, want session %q seq 1", refs[0], filepath.Base(dir))
	}
}
