package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

func taskWorkerCheckpoint(agents string) string {
	return `{"version":1,"counter":2,"delegated":2,"agents":[` + agents + `]}`
}

// TestTaskWorkersProjection_ListsHistoricalWorkers pins the task-scoped rail
// contract: every run that delegated keeps its completed/failed workers visible
// after output stops, and runs that never delegated are omitted instead of
// rendering an empty worker section.
func TestTaskWorkersProjection_ListsHistoricalWorkers(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Workers Project", platform.Scope{Targets: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "Workers Task", Goal: "g"})
	if err != nil {
		t.Fatal(err)
	}

	// Run with two delegated workers, both terminal, in the durable checkpoint.
	withWorkers := "inst-workers-1"
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: withWorkers, Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	dir := ledgerTestScan(t, s, withWorkers)
	checkpoint := taskWorkerCheckpoint(`
		{"id":"sub_1_1","name":"injection-serverside","task":"test sqli","targets":["https://example.com"],"status":"completed","started_at":"2026-01-01T00:00:00Z","completed_at":"2026-01-01T00:10:00Z"},
		{"id":"sub_2_2","name":"client-source","task":"test xss","targets":["https://example.com"],"status":"failed","started_at":"2026-01-01T00:00:05Z","completed_at":"2026-01-01T00:08:00Z","error":"budget exhausted"}
	`)
	if err := os.WriteFile(filepath.Join(dir, "execution-graph.json"), []byte(checkpoint), 0o600); err != nil {
		t.Fatal(err)
	}

	// Run that never delegated: no checkpoint, no workers.
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "inst-single-2", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	ledgerTestScan(t, s, "inst-single-2")

	// Route-level: /api/tasks/{id}/workers must dispatch from handleTaskDetail.
	rr := projectionGet(t, s.handleTaskDetail, "/api/tasks/"+task.ID+"/workers")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Items []taskWorkerRunView `json:"items"`
		Total int                 `json:"total"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != 2 {
		t.Fatalf("total runs = %d, want 2", out.Total)
	}
	if len(out.Items) != 1 {
		t.Fatalf("items = %d, want only the delegating run: %#v", len(out.Items), out.Items)
	}
	item := out.Items[0]
	if item.InstanceID != withWorkers {
		t.Fatalf("instance = %q, want %q", item.InstanceID, withWorkers)
	}
	if !item.Workers.Available {
		t.Fatal("checkpoint projection must be available")
	}
	if len(item.Workers.Agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(item.Workers.Agents))
	}
	statuses := map[string]string{}
	for _, worker := range item.Workers.Agents {
		statuses[worker.AgentID] = worker.Status
	}
	if statuses["sub_1_1"] != "completed" || statuses["sub_2_2"] != "failed" {
		t.Fatalf("terminal statuses lost: %#v", statuses)
	}
}

func TestTaskWorkersProjection_RejectsWrites(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/x/workers", nil)
	s.handleTaskWorkers(rr, req, store, "x")
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}
