package scanctx

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestEgressDisabledIsNoop(t *testing.T) {
	b := NewEgressBudget(EgressPolicy{}, nil)
	tk, err := b.Acquire(context.Background(), "a.test")
	if err != nil || tk == nil {
		t.Fatalf("disabled budget acquire = %v %v", tk, err)
	}
	tk.Done(200, 0)
	if snap := b.Snapshot(); snap.Requests != 0 {
		t.Fatalf("disabled budget counted %d", snap.Requests)
	}
}

func TestEgressTotalBudget(t *testing.T) {
	b := NewEgressBudget(EgressPolicy{TotalBudget: 1}, nil)
	tk, err := b.Acquire(context.Background(), "a.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Acquire(context.Background(), "a.test"); !errors.Is(err, ErrEgressBudgetExhausted) {
		t.Fatalf("second acquire = %v, want budget exhausted", err)
	}
	tk.Done(200, 0)
	if _, err := b.Acquire(context.Background(), "a.test"); !errors.Is(err, ErrEgressBudgetExhausted) {
		t.Fatalf("budget must stay exhausted after release, got %v", err)
	}
}

func TestEgressGlobalConcurrencyIsShared(t *testing.T) {
	b := NewEgressBudget(EgressPolicy{MaxConcurrency: 1}, nil)
	tk, err := b.Acquire(context.Background(), "a.test")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		t2, err := b.Acquire(ctx, "b.test")
		if err == nil {
			t2.Done(200, 0)
		}
		done <- err
	}()
	<-started
	time.Sleep(20 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("second acquire should block on the shared slot, got %v", err)
	default:
	}
	tk.Done(200, 0)
	if err := <-done; err != nil {
		t.Fatalf("second acquire after release: %v", err)
	}
}

func TestEgressPauseAndStop(t *testing.T) {
	b := NewEgressBudget(EgressPolicy{MaxConcurrency: 2}, nil)
	b.Pause(50*time.Millisecond, "target asked")
	if _, err := b.Acquire(context.Background(), "a.test"); !errors.Is(err, ErrEgressPaused) {
		t.Fatalf("paused acquire = %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	tk, err := b.Acquire(context.Background(), "a.test")
	if err != nil {
		t.Fatalf("acquire after pause: %v", err)
	}
	tk.Done(200, 0)
	b.Stop("operator stop")
	if _, err := b.Acquire(context.Background(), "a.test"); !errors.Is(err, ErrEgressStopped) {
		t.Fatalf("stopped acquire = %v", err)
	}
}

func TestEgress429PausesOriginOnly(t *testing.T) {
	b := NewEgressBudget(EgressPolicy{MaxConcurrency: 4}, nil)
	tk, _ := b.Acquire(context.Background(), "a.test")
	tk.Done(429, 200*time.Millisecond)
	if _, err := b.Acquire(context.Background(), "a.test"); !errors.Is(err, ErrEgressPaused) {
		t.Fatalf("429 origin should pause: %v", err)
	}
	other, err := b.Acquire(context.Background(), "b.test")
	if err != nil {
		t.Fatalf("another origin must not inherit the pause: %v", err)
	}
	other.Done(200, 0)
	if snap := b.Snapshot(); snap.StatusClasses["4xx"] != 1 || snap.StatusClasses["2xx"] != 1 {
		t.Fatalf("status classes = %+v", snap.StatusClasses)
	}
	time.Sleep(220 * time.Millisecond)
	if tk, err := b.Acquire(context.Background(), "a.test"); err != nil {
		t.Fatalf("pause should expire: %v", err)
	} else {
		tk.Done(200, 0)
	}
}

func TestEgressSnapshotCounts(t *testing.T) {
	b := NewEgressBudget(EgressPolicy{MaxConcurrency: 4}, nil)
	tk, _ := b.Acquire(context.Background(), "a.test")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tx, err := b.Acquire(context.Background(), "a.test")
		if err == nil {
			time.Sleep(10 * time.Millisecond)
			tx.Done(200, 0)
		}
	}()
	time.Sleep(5 * time.Millisecond)
	snap := b.Snapshot()
	if snap.Requests < 1 || snap.Active < 1 || snap.PerHost["a.test"] < 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	tk.Done(200, 0)
	wg.Wait()
	if snap := b.Snapshot(); snap.Active != 0 {
		t.Fatalf("active after release = %d", snap.Active)
	}
}

func TestPauseHostOnlyAffectsThatOrigin(t *testing.T) {
	b := NewEgressBudget(EgressPolicy{MaxConcurrency: 2}, nil)
	b.PauseHost("a.test", 100*time.Millisecond, "hook 429")
	if _, err := b.Acquire(context.Background(), "a.test"); !errors.Is(err, ErrEgressPaused) {
		t.Fatalf("paused host acquire = %v", err)
	}
	tk, err := b.Acquire(context.Background(), "b.test")
	if err != nil {
		t.Fatalf("other host must proceed: %v", err)
	}
	tk.Done(200, 0)
}
