package llm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// H/M-8 regression: a failed persistence write must not leave the in-memory
// map ahead of disk.
func TestKeyStoreSetRollsBackOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	ks, err := NewKeyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := ks.Set(ctx, ProviderKey{ProviderID: "openai", APIKey: "sk-a"}); err != nil {
		t.Fatal(err)
	}
	// Force the rename target to be a directory so the atomic save fails.
	path := filepath.Join(dir, "llm_keys.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ks.Set(ctx, ProviderKey{ProviderID: "anthropic", APIKey: "sk-b"}); err == nil {
		t.Fatal("Set must surface the persistence failure")
	}
	if _, ok := ks.Get("anthropic"); ok {
		t.Fatal("failed Set leaked into memory")
	}
	if got, ok := ks.Get("openai"); !ok || got.APIKey != "sk-a" {
		t.Fatalf("existing key damaged by failed Set: %+v, %v", got, ok)
	}
}
