package agent

import "testing"

func TestSetWorkerConcurrencyStoresClampedValue(t *testing.T) {
	a := &Agent{}
	if err := a.SetWorkerConcurrency(7); err != nil {
		t.Fatal(err)
	}
	if got := a.WorkerPoolSize(); got != 7 {
		t.Fatalf("worker pool size = %d, want 7", got)
	}
	if err := a.SetWorkerConcurrency(99); err != nil {
		t.Fatal(err)
	}
	if got := a.WorkerPoolSize(); got != 10 {
		t.Fatalf("clamped size = %d, want 10", got)
	}
	if err := a.SetWorkerConcurrency(0); err != nil {
		t.Fatal(err)
	}
	if got := a.WorkerPoolSize(); got != 1 {
		t.Fatalf("floor size = %d, want 1", got)
	}
}
