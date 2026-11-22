package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/platform"
	"github.com/xalgord/xalgorix/v4/internal/tools/intelligence"
)

// intelTestTask creates project+task and mounts one run on a scan dir that
// carries a real record_intelligence snapshot.
func intelTestTask(t *testing.T, s *Server, instanceID string, record func(dir string)) (taskID string, store platform.Repository) {
	t.Helper()
	rr := doJSON(t, s, http.MethodPost, "/api/projects",
		`{"name":"Intel Proj","scope":{"targets":["intel.example"]}}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST /api/projects code = %d body=%s", rr.Code, rr.Body.String())
	}
	var proj struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &proj)

	rr = doJSON(t, s, http.MethodPost, "/api/projects/"+proj.ID+"/tasks",
		`{"name":"Intel Task","goal":"find things"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST tasks code = %d body=%s", rr.Code, rr.Body.String())
	}
	var task struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &task)

	dir := filepath.Join(s.dataDir, instanceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := &ScanRecord{
		ID:         instanceID,
		InstanceID: instanceID,
		Target:     "https://intel.example",
		Status:     "finished",
		StartedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	s.saveScanRecordTo(rec, dir)

	// Mount through the server's own store instance: a second FileStore on
	// the same path would hold a stale in-memory snapshot the server never
	// sees.
	store = s.currentPlatformStore()
	if _, err := store.CreateRun(t.Context(), task.ID, platform.RunMount{InstanceID: instanceID, Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	if record != nil {
		record(dir)
	}
	return task.ID, store
}

func intelRecordInto(t *testing.T, dir string, args map[string]string) string {
	t.Helper()
	ctxID := "intel-web-" + filepath.Base(dir)
	t.Cleanup(func() { intelligence.CleanupContext(ctxID) })
	intelligence.SetPersistPathForContext(ctxID, dir)
	res, err := intelligence.RecordForContext(ctxID, args)
	if err != nil || res.Error != "" {
		t.Fatalf("fixture record: err=%v res=%+v", err, res)
	}
	fp, _ := res.Metadata["intel_fingerprint"].(string)
	return fp
}

// The aggregation walks run scan dirs, dedups by fingerprint, zero-fills kind
// counts, and exposes the triage overlay as a separate read-time projection.
func TestTaskIntelligenceAggregatesRunsAndTriages(t *testing.T) {
	s := newPlatformTestServer(t)

	fp := ""
	taskID, _ := intelTestTask(t, s, "intel-inst-1", func(dir string) {
		fp = intelRecordInto(t, dir, map[string]string{
			"kind":       "credential",
			"title":      "Admin panel accepts default creds",
			"content":    "admin:admin accepted at /app/admin/login",
			"endpoint":   "https://intel.example/app/admin",
			"confidence": "0.9",
		})
	})

	rr := doJSON(t, s, http.MethodGet, "/api/tasks/"+taskID+"/intelligence", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET intelligence code = %d body=%s", rr.Code, rr.Body.String())
	}
	var view struct {
		Items []struct {
			Fingerprint string   `json:"fingerprint"`
			Kind        string   `json:"kind"`
			ScanIDs     []string `json:"scan_ids"`
			Triage      *struct {
				Status string `json:"status"`
			} `json:"triage,omitempty"`
		} `json:"items"`
		Counts map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Items) != 1 || view.Items[0].Fingerprint != fp {
		t.Fatalf("items = %+v, want exactly one item with fp %s", view.Items, fp)
	}
	if view.Items[0].Kind != "credential" || view.Counts["credential"] != 1 {
		t.Fatalf("counts = %+v, want credential:1", view.Counts)
	}
	if len(view.Items[0].ScanIDs) != 1 || view.Items[0].ScanIDs[0] != "intel-inst-1" {
		t.Fatalf("scan_ids = %+v, want [intel-inst-1]", view.Items[0].ScanIDs)
	}
	if view.Items[0].Triage != nil {
		t.Fatalf("untiraged item must carry no triage, got %+v", view.Items[0].Triage)
	}

	// PATCH triage → confirmed; overlay appears on the next read.
	rr = doJSON(t, s, http.MethodPatch, "/api/tasks/"+taskID+"/intelligence/"+fp, `{"status":"confirmed"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH triage code = %d body=%s", rr.Code, rr.Body.String())
	}
	rr = doJSON(t, s, http.MethodGet, "/api/tasks/"+taskID+"/intelligence", "")
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Items[0].Triage == nil || view.Items[0].Triage.Status != "confirmed" {
		t.Fatalf("triage overlay = %+v, want confirmed", view.Items[0].Triage)
	}

	// Re-triage moves the label; invalid vocabulary is refused.
	rr = doJSON(t, s, http.MethodPatch, "/api/tasks/"+taskID+"/intelligence/"+fp, `{"status":"dismissed"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("re-triage code = %d body=%s", rr.Code, rr.Body.String())
	}
	rr = doJSON(t, s, http.MethodPatch, "/api/tasks/"+taskID+"/intelligence/"+fp, `{"status":"yolo"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid status code = %d, want 400", rr.Code)
	}
}

// The same observation recorded by a second run aggregates under one item
// with both scan ids; a damaged snapshot is counted, never folded into clean.
func TestTaskIntelligenceCrossRunDedupAndDamage(t *testing.T) {
	s := newPlatformTestServer(t)

	firstFP := ""
	taskID, _ := intelTestTask(t, s, "intel-inst-a", func(dir string) {
		firstFP = intelRecordInto(t, dir, map[string]string{
			"kind": "lead", "title": "Sequential invoice ids", "content": "invoice ids are sequential",
		})
	})
	// Second run, same observation (same normalized content+endpoint-less).
	secondFP := ""
	dir2 := filepath.Join(s.dataDir, "intel-inst-b")
	if err := os.MkdirAll(dir2, 0o700); err != nil {
		t.Fatal(err)
	}
	rec2 := &ScanRecord{
		ID:         "intel-inst-b",
		InstanceID: "intel-inst-b",
		Target:     "https://intel.example",
		Status:     "finished",
		StartedAt:  time.Now().UTC().Add(time.Minute).Format(time.RFC3339),
	}
	s.saveScanRecordTo(rec2, dir2)
	if _, err := s.currentPlatformStore().CreateRun(t.Context(), taskID, platform.RunMount{InstanceID: "intel-inst-b", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	secondFP = intelRecordInto(t, dir2, map[string]string{
		"kind": "lead", "title": "Sequential invoice ids (again)", "content": "invoice   ids ARE sequential",
	})
	if secondFP != firstFP {
		t.Fatalf("fixture fingerprints must match across runs: %s vs %s", secondFP, firstFP)
	}
	// Third run with a corrupt snapshot — must surface as damaged_runs, not
	// as an absence of data.
	dir3 := filepath.Join(s.dataDir, "intel-inst-c")
	if err := os.MkdirAll(dir3, 0o700); err != nil {
		t.Fatal(err)
	}
	rec3 := &ScanRecord{ID: "intel-inst-c", InstanceID: "intel-inst-c", Target: "https://intel.example", Status: "finished", StartedAt: time.Now().UTC().Format(time.RFC3339)}
	s.saveScanRecordTo(rec3, dir3)
	if _, err := s.currentPlatformStore().CreateRun(t.Context(), taskID, platform.RunMount{InstanceID: "intel-inst-c", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir3, "intelligence.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}

	rr := doJSON(t, s, http.MethodGet, "/api/tasks/"+taskID+"/intelligence", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET intelligence code = %d body=%s", rr.Code, rr.Body.String())
	}
	var view struct {
		Items []struct {
			Fingerprint string   `json:"fingerprint"`
			ScanIDs     []string `json:"scan_ids"`
		} `json:"items"`
		DamagedRuns int `json:"damaged_runs"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Items) != 1 || len(view.Items[0].ScanIDs) != 2 {
		t.Fatalf("cross-run dedup failed: items=%+v, want one item with two scan ids", view.Items)
	}
	if view.DamagedRuns != 1 {
		t.Fatalf("damaged_runs = %d, want 1", view.DamagedRuns)
	}
}

// Unknown task reads as an honest empty aggregation — the same semantics as
// GET /api/tasks/{id}/findings (subresource aggregation never fabricates a
// 404 from "no runs yet"; existence is the task detail endpoint's concern).
func TestTaskIntelligenceUnknownTaskIsEmpty(t *testing.T) {
	s := newPlatformTestServer(t)
	rr := doJSON(t, s, http.MethodGet, "/api/tasks/does-not-exist/intelligence", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("unknown task code = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	var view struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Items) != 0 {
		t.Fatalf("unknown task must aggregate to zero items, got %d", len(view.Items))
	}
}
