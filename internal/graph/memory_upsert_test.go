package graph

import (
	"context"
	"sync"
	"testing"
)

// TestMemoryStore_UpsertNodeConcurrentFindOrInsert pins the in-memory
// authority to the same uniqueness the PG store gets from its
// (scan_id, kind, unit_key) constraint: concurrent projections of one unit
// must converge on one node id.
func TestMemoryStore_UpsertNodeConcurrentFindOrInsert(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	const workers = 64
	ids := make(chan int64, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			node, err := store.UpsertNode(ctx, NodeInput{
				ScanID: "scan-concurrent", Kind: KindGoal, UnitKey: "GOAL", State: StateOpen,
			})
			if err != nil {
				t.Errorf("upsert: %v", err)
				return
			}
			ids <- node.ID
		}()
	}
	wg.Wait()
	close(ids)

	unique := map[int64]bool{}
	for id := range ids {
		unique[id] = true
	}
	if len(unique) != 1 {
		t.Fatalf("concurrent upsert produced %d node ids: %+v", len(unique), unique)
	}
	nodes, err := store.ListNodes(ctx, "scan-concurrent", KindGoal)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].UnitKey != "GOAL" {
		t.Fatalf("goal nodes = %+v", nodes)
	}
}
