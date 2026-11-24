package runtime

import (
	"context"
	"testing"
)

func TestMemoryDesiredStateListByProjectIDs(t *testing.T) {
	store := NewMemoryDesiredState()
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		if _, err := store.RecordProject(ctx, DesiredProject{
			ProjectID:    id,
			ScanID:       id,
			DesiredState: DesiredReady,
			Status:       ReconcileApplied,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.ListByProjectIDs(ctx, []string{"a", "missing", "c", "a", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (%+v)", len(rows), rows)
	}
	got := map[string]bool{rows[0].ProjectID: true, rows[1].ProjectID: true}
	if !got["a"] || !got["c"] {
		t.Fatalf("unexpected rows: %+v", rows)
	}

	empty, err := store.ListByProjectIDs(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty query = %v, %v", empty, err)
	}
}
