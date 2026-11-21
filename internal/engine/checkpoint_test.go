package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileCheckpointStoreAtomicRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileCheckpointStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, ok, err := store.Get(ctx, "scan-1/interrupt"); err != nil || ok {
		t.Fatalf("missing checkpoint = ok=%v err=%v", ok, err)
	}
	if err := store.Set(ctx, "scan-1/interrupt", []byte("first")); err != nil {
		t.Fatal(err)
	}
	payload, ok, err := store.Get(ctx, "scan-1/interrupt")
	if err != nil || !ok || string(payload) != "first" {
		t.Fatalf("round-trip = %q, %v, %v", payload, ok, err)
	}
	if err := store.Set(ctx, "scan-1/interrupt", []byte("second")); err != nil {
		t.Fatal(err)
	}
	payload, _, err = store.Get(ctx, "scan-1/interrupt")
	if err != nil || string(payload) != "second" {
		t.Fatalf("idempotent overwrite = %q, %v", payload, err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "_engine", "checkpoints"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || filepath.Ext(entries[0].Name()) != ".ckpt" {
		t.Fatalf("checkpoint shards = %v", entries)
	}
	if _, ok, err := store.Get(ctx, "../../escape"); err != nil || ok {
		t.Fatalf("path-shaped id = ok=%v err=%v", ok, err)
	}
}

func TestMemoryCheckpointStoreRejectsEmpty(t *testing.T) {
	store := NewMemoryCheckpointStore()
	if err := store.Set(context.Background(), "", []byte("x")); err == nil {
		t.Fatal("empty id accepted")
	}
	if err := store.Set(context.Background(), "id", nil); err == nil {
		t.Fatal("empty payload accepted")
	}
}
