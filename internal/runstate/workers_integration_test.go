package runstate

// PG round-trip for the batch worker-history read the task-scoped session rail
// uses (handleTaskWorkers). Skipped unless XALGORIX_TEST_DATABASE_URL is set,
// same gate as the other integration tests.

import (
	"context"
	"testing"
)

func TestListWorkersByInstancesBatch(t *testing.T) {
	store := integrationStore(t)
	ctx := context.Background()
	ids := []string{"batch-workers-a", "batch-workers-b"}
	cleanup := func() {
		_, _ = store.pool.Exec(context.Background(),
			`DELETE FROM run_workers WHERE instance_id = ANY($1)`, ids)
	}
	cleanup()
	t.Cleanup(cleanup)

	for _, rec := range []WorkerRecord{
		{InstanceID: ids[0], ScanID: "scan-a1", AgentID: "sub_1", Lane: "injection", Status: "running"},
		{InstanceID: ids[0], ScanID: "scan-a2", AgentID: "sub_2", Lane: "xss", Status: "completed"},
		{InstanceID: ids[1], ScanID: "scan-b1", AgentID: "sub_3", Lane: "recon", Status: "failed"},
	} {
		if err := store.UpsertWorker(ctx, rec); err != nil {
			t.Fatalf("upsert %s/%s: %v", rec.InstanceID, rec.AgentID, err)
		}
	}

	got, err := store.ListWorkersByInstances(ctx, ids)
	if err != nil {
		t.Fatalf("batch read: %v", err)
	}
	if len(got[ids[0]]) != 2 || len(got[ids[1]]) != 1 {
		t.Fatalf("grouping = %d/%d rows, want 2/1", len(got[ids[0]]), len(got[ids[1]]))
	}
	if got[ids[1]][0].Lane != "recon" || got[ids[1]][0].Status != "failed" {
		t.Fatalf("row fields lost: %#v", got[ids[1]][0])
	}
}
