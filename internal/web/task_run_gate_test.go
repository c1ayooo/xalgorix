package web

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

// TestTaskRunGateProjection pins the 用户裁决 rule on the task API: a task owns
// one run, so once a dispatch consumed the task's runnable content a plain
// re-run must not be offered (content_changed_since_run=false). Editing that
// content flips it back; renaming the task does not. The completion reading
// distinguishes 已完成 (full) from 未达成意图 (partial).
func TestTaskRunGateProjection(t *testing.T) {
	s := newPlatformTestServer(t)
	store := s.currentPlatformStore()
	ctx := context.Background()

	project, err := store.CreateProject(ctx, "Gate Project", platform.Scope{Targets: []string{"gate.example"}})
	if err != nil {
		t.Fatal(err)
	}
	rr := doJSON(t, s, http.MethodPost, "/api/projects/"+project.ID+"/tasks",
		`{"name":"Gate review","goal":"Assess gate.example","instruction":"Focus auth"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST task code = %d; body=%s", rr.Code, rr.Body.String())
	}
	created := decodeBody(t, rr)
	taskID := created["id"].(string)

	// Never run → runnable.
	taskMap := getTaskMap(t, s, taskID)
	if taskMap["content_changed_since_run"] != true {
		t.Fatalf("never-run task content_changed_since_run = %v, want true", taskMap["content_changed_since_run"])
	}

	// Mount a settled run with a full completion.
	writeScanFixture(t, s.dataDir, "gate.example", "2026-06-03", ScanRecord{
		ID: "gate-scan-1", InstanceID: "gate-inst-1", Target: "gate.example",
		StartedAt:  time.Now().Add(-time.Hour).Format(time.RFC3339),
		Status:     "finished",
		Completion: "full",
		ScanMode:   "single",
	})
	if _, err := store.CreateRun(ctx, taskID, platform.RunMount{InstanceID: "gate-inst-1", Mode: "single", Trigger: "manual"}); err != nil {
		t.Fatal(err)
	}

	// A run exists but its consumed revision was never recorded → stay runnable
	// (fail open; history imported before the marker existed must not lock).
	taskMap = getTaskMap(t, s, taskID)
	if taskMap["content_changed_since_run"] != true {
		t.Fatalf("unrecorded revision content_changed_since_run = %v, want true", taskMap["content_changed_since_run"])
	}
	if taskMap["last_run_completion"] != "full" {
		t.Fatalf("last_run_completion = %v, want full", taskMap["last_run_completion"])
	}

	// Record the dispatched revision → the exact content is consumed.
	tasks, err := store.ListTasks(ctx, project.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListTasks = %v, %v", tasks, err)
	}
	if err := store.SetTaskRunRevision(ctx, taskID, platform.TaskContentRevision(tasks[0])); err != nil {
		t.Fatal(err)
	}
	taskMap = getTaskMap(t, s, taskID)
	if taskMap["content_changed_since_run"] != false {
		t.Fatalf("consumed content content_changed_since_run = %v, want false", taskMap["content_changed_since_run"])
	}

	// Renaming is not new work: the gate stays closed.
	rr = doJSON(t, s, http.MethodPatch, "/api/tasks/"+taskID, `{"name":"Renamed"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH name code = %d; body=%s", rr.Code, rr.Body.String())
	}
	taskMap = getTaskMap(t, s, taskID)
	if taskMap["content_changed_since_run"] != false {
		t.Fatalf("rename content_changed_since_run = %v, want false", taskMap["content_changed_since_run"])
	}

	// Editing the goal is new work: the gate reopens.
	rr = doJSON(t, s, http.MethodPatch, "/api/tasks/"+taskID, `{"goal":"Assess the admin edge"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH goal code = %d; body=%s", rr.Code, rr.Body.String())
	}
	taskMap = getTaskMap(t, s, taskID)
	if taskMap["content_changed_since_run"] != true {
		t.Fatalf("edited goal content_changed_since_run = %v, want true", taskMap["content_changed_since_run"])
	}
}

// TestTaskRunGatePartialCompletion covers the 未达成意图 reading: a bounded stop
// persists completion=partial, and the task API must project it verbatim
// instead of inventing a "failed" status.
func TestTaskRunGatePartialCompletion(t *testing.T) {
	s := newPlatformTestServer(t)
	store := s.currentPlatformStore()
	ctx := context.Background()

	project, err := store.CreateProject(ctx, "Unmet Project", platform.Scope{Targets: []string{"unmet.example"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "Unmet review", Goal: "Assess unmet.example"})
	if err != nil {
		t.Fatal(err)
	}
	writeScanFixture(t, s.dataDir, "unmet.example", "2026-06-04", ScanRecord{
		ID: "unmet-scan-1", InstanceID: "unmet-inst-1", Target: "unmet.example",
		StartedAt:  time.Now().Add(-time.Hour).Format(time.RFC3339),
		Status:     "stopped",
		Completion: "partial",
		StopReason: "budget_exhausted",
		ScanMode:   "single",
	})
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "unmet-inst-1", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskRunRevision(ctx, task.ID, platform.TaskContentRevision(*task)); err != nil {
		t.Fatal(err)
	}
	taskMap := getTaskMap(t, s, task.ID)
	if taskMap["content_changed_since_run"] != false {
		t.Fatalf("content_changed_since_run = %v, want false", taskMap["content_changed_since_run"])
	}
	if taskMap["last_run_completion"] != "partial" {
		t.Fatalf("last_run_completion = %v, want partial", taskMap["last_run_completion"])
	}
	if taskMap["last_run_status"] != "stopped" {
		t.Fatalf("last_run_status = %v, want stopped", taskMap["last_run_status"])
	}
}

func getTaskMap(t *testing.T, s *Server, taskID string) map[string]any {
	t.Helper()
	rr := doJSON(t, s, http.MethodGet, "/api/tasks/"+taskID, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET task code = %d; body=%s", rr.Code, rr.Body.String())
	}
	return decodeBody(t, rr)
}
