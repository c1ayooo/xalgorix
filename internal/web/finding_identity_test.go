package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/platform"
)

// TestFindingIdentityKeyIsSharedAndSeverityIndependent pins the single identity
// contract that made the live feed and the findings list disagree before:
// record de-dup (vulnSummaryKey), live broadcast de-dup and every aggregation
// projection (dedupFindingKey) must produce the same key, and a re-rated finding
// (severity changed by the report gate) must stay the same finding.
func TestFindingIdentityKeyIsSharedAndSeverityIndependent(t *testing.T) {
	high := VulnSummary{
		ID: "v1", Title: "Auth Bypass", Severity: "high", Endpoint: "/admin",
		Target: "acme.example", Method: "GET", CVE: "CVE-2024-1",
	}
	critical := high
	critical.Severity = "Critical"

	if dedupFindingKey("acme.example", high) != dedupFindingKey("acme.example", critical) {
		t.Fatal("severity must not be part of the finding identity")
	}
	if vulnSummaryKey(high) != dedupFindingKey("acme.example", high) {
		t.Fatal("record de-dup and aggregation keys drifted apart")
	}

	// The key must not depend on which caller produced it (record target vs the
	// finding's own target) or on cosmetic whitespace/case.
	if dedupFindingKey("other.example", high) != dedupFindingKey("acme.example", high) {
		t.Fatal("key depends on caller-supplied target instead of the finding's target")
	}
	spaced := high
	spaced.Title = "  auth   BYPASS "
	if dedupFindingKey("acme.example", spaced) != dedupFindingKey("acme.example", high) {
		t.Fatal("key is not normalized (case/whitespace)")
	}

	// method/cve stay part of identity: they already drive the verified-upgrade
	// replacement rule (internal/tools/reporting/upgrade_identity.go).
	post := high
	post.Method = "POST"
	if dedupFindingKey("acme.example", post) == dedupFindingKey("acme.example", high) {
		t.Fatal("method must stay part of the finding identity")
	}
	noCVE := high
	noCVE.CVE = ""
	if dedupFindingKey("acme.example", noCVE) == dedupFindingKey("acme.example", high) {
		t.Fatal("cve must stay part of the finding identity")
	}
}

// TestFindingsListMergesReratedFinding pins the user-visible consequence: the
// same finding reported twice with different severities collapses into the
// newest row, while a finding that differs by method survives as its own row.
func TestFindingsListMergesReratedFinding(t *testing.T) {
	s := newPlatformTestServer(t)

	base := VulnSummary{
		Title: "Auth Bypass", Endpoint: "/admin", Target: "rated.example", Method: "GET",
	}
	older := base
	older.ID, older.Severity = "v-old", "high"
	newer := base
	newer.ID, newer.Severity = "v-new", "critical"
	byMethod := base
	byMethod.ID, byMethod.Severity, byMethod.Method = "v-post", "medium", "POST"

	writeScanFixture(t, s.dataDir, "rated.example", "2026-05-01", ScanRecord{
		ID: "rated-old", InstanceID: "rated-inst", Target: "rated.example",
		Status: "finished", StartedAt: "2026-05-01T10:00:00Z",
		Vulns: []VulnSummary{older},
	})
	writeScanFixture(t, s.dataDir, "rated.example", "2026-05-01", ScanRecord{
		ID: "rated-new", InstanceID: "rated-inst", Target: "rated.example",
		Status: "finished", StartedAt: "2026-05-01T12:00:00Z",
		Vulns: []VulnSummary{newer},
	})
	writeScanFixture(t, s.dataDir, "rated.example", "2026-05-01", ScanRecord{
		ID: "rated-post", InstanceID: "rated-inst", Target: "rated.example",
		Status: "finished", StartedAt: "2026-05-01T11:00:00Z",
		Vulns: []VulnSummary{byMethod},
	})

	rr := doJSON(t, s, http.MethodGet, "/api/findings", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/findings = %d; body=%s", rr.Code, rr.Body.String())
	}
	var rows []flatFinding
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode findings: %v; body=%s", err, rr.Body.String())
	}
	if len(rows) != 2 {
		t.Fatalf("findings rows = %d, want 2 (re-rated merged, method kept); rows=%+v", len(rows), rows)
	}
	for _, row := range rows {
		if row.Method == "GET" {
			if row.Severity != "critical" || row.ScanID != "rated-new" {
				t.Fatalf("re-rated row did not keep the newest finding: %+v", row)
			}
		}
	}
}

// TestTaskFindingCountMatchesDedupedList pins that the task detail header count
// (finding_count) and the task findings list are derived from the same identity
// key — the mismatch users saw between "N findings" and the rendered rows.
func TestTaskFindingCountMatchesDedupedList(t *testing.T) {
	s := newPlatformTestServer(t)
	ctx := context.Background()
	store := s.currentPlatformStore()
	project, err := store.CreateProject(ctx, "Consistent", platform.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "Consistency"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "cons-inst", Mode: "single"}); err != nil {
		t.Fatal(err)
	}

	base := VulnSummary{Title: "Open Redirect", Endpoint: "/go", Target: "cons.example", Method: "GET"}
	first := base
	first.ID, first.Severity = "v1", "medium"
	rerated := base
	rerated.ID, rerated.Severity = "v2", "high"
	otherMethod := base
	otherMethod.ID, otherMethod.Severity, otherMethod.Method = "v3", "medium", "POST"

	writeScanFixture(t, s.dataDir, "cons.example", "2026-05-02", ScanRecord{
		ID: "cons-old", InstanceID: "cons-inst", Target: "cons.example",
		Status: "finished", StartedAt: "2026-05-02T09:00:00Z",
		Vulns: []VulnSummary{first},
	})
	writeScanFixture(t, s.dataDir, "cons.example", "2026-05-02", ScanRecord{
		ID: "cons-new", InstanceID: "cons-inst", Target: "cons.example",
		Status: "finished", StartedAt: "2026-05-02T11:00:00Z",
		Vulns: []VulnSummary{rerated, otherMethod},
	})

	rr := doJSON(t, s, http.MethodGet, "/api/tasks/"+task.ID+"/findings", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET task findings = %d; body=%s", rr.Code, rr.Body.String())
	}
	var findings []flatFinding
	if err := json.Unmarshal(rr.Body.Bytes(), &findings); err != nil {
		t.Fatalf("decode task findings: %v; body=%s", err, rr.Body.String())
	}
	if len(findings) != 2 {
		t.Fatalf("task findings = %d, want 2 (re-rated merged); rows=%+v", len(findings), findings)
	}

	rr = doJSON(t, s, http.MethodGet, "/api/projects/"+project.ID+"/tasks", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET project tasks = %d; body=%s", rr.Code, rr.Body.String())
	}
	var envelope struct {
		Tasks []platform.Task `json:"tasks"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode tasks: %v; body=%s", err, rr.Body.String())
	}
	var got *platform.Task
	for i := range envelope.Tasks {
		if envelope.Tasks[i].ID == task.ID {
			got = &envelope.Tasks[i]
		}
	}
	if got == nil {
		t.Fatalf("task %s missing from project tasks: %+v", task.ID, envelope.Tasks)
	}
	if got.FindingCount != len(findings) {
		t.Fatalf("finding_count = %d, findings list = %d (must share one identity key)", got.FindingCount, len(findings))
	}
}
