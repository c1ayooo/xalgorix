package jobs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"
)

func TestToolboxJobKindsAreStable(t *testing.T) {
	cases := []struct {
		args river.JobArgs
		kind string
	}{
		{GCJobArgs{InstanceID: "i"}, KindToolboxGC},
		{ReapIdleJobArgs{InstanceID: "i"}, KindToolboxReapIdle},
		{AdoptJobArgs{InstanceID: "i"}, KindToolboxAdopt},
		{PrefetchImageJobArgs{Image: "img"}, KindToolboxPrefetch},
		{SelftestJobArgs{Image: "img"}, KindToolboxSelftest},
		{ReconcileProjectJobArgs{ProjectID: "p"}, KindToolboxReconcile},
		{ImageGCJobArgs{KeepDigests: 3}, KindToolboxImageGC},
	}
	for _, tc := range cases {
		if got := tc.args.Kind(); got != tc.kind {
			t.Errorf("%T.Kind() = %q, want %q", tc.args, got, tc.kind)
		}
	}
	if ToolboxQueue == DefaultQueue {
		t.Fatal("toolbox queue must be isolated from the cold-path queue")
	}
}

func TestToolboxJobValidation(t *testing.T) {
	bad := []river.JobArgs{
		GCJobArgs{},
		ReapIdleJobArgs{InstanceID: "  "},
		AdoptJobArgs{},
		PrefetchImageJobArgs{},
		SelftestJobArgs{},
		ReconcileProjectJobArgs{},
		ImageGCJobArgs{},
	}
	for _, args := range bad {
		validator, ok := args.(interface{ Validate() error })
		if !ok {
			t.Fatalf("%T has no Validate", args)
		}
		if err := validator.Validate(); err == nil {
			t.Errorf("%T accepted empty args", args)
		}
	}
}

type fakeHandlers struct {
	gcCalls        int
	adoptCalls     int
	reconcileCalls int
	reapCalls      int
	reapTTL        time.Duration
	reapMaxLife    time.Duration
	prefetched     string
}

func (f *fakeHandlers) ReconcileOnce(context.Context) error {
	f.reconcileCalls++
	return nil
}

func (f *fakeHandlers) ReclaimOrphans(context.Context) error {
	f.gcCalls++
	return nil
}

func (f *fakeHandlers) Adopt(context.Context) error {
	f.adoptCalls++
	return nil
}

func (f *fakeHandlers) PrefetchImage(_ context.Context, image, _ string) error {
	f.prefetched = image
	return nil
}

func (f *fakeHandlers) Selftest(context.Context, string, string) (int, int, string, error) {
	return 0, 0, "", nil
}

func (f *fakeHandlers) ImageGC(context.Context, int) error { return nil }

func (f *fakeHandlers) ReapIdle(_ context.Context, ttl, maxLife time.Duration) error {
	f.reapCalls++
	f.reapTTL = ttl
	f.reapMaxLife = maxLife
	return nil
}

func TestGCWorkerSweepsThenReconciles(t *testing.T) {
	f := &fakeHandlers{}
	w := &gcWorker{handlers: f}
	if err := w.Work(context.Background(), &river.Job[GCJobArgs]{Args: GCJobArgs{InstanceID: "inst"}}); err != nil {
		t.Fatal(err)
	}
	if f.gcCalls != 1 || f.reconcileCalls != 1 {
		t.Fatalf("gc=%d reconcile=%d", f.gcCalls, f.reconcileCalls)
	}
}

func TestGCWorkerDryRunTouchesNothing(t *testing.T) {
	f := &fakeHandlers{}
	w := &gcWorker{handlers: f}
	if err := w.Work(context.Background(), &river.Job[GCJobArgs]{Args: GCJobArgs{InstanceID: "inst", DryRun: true}}); err != nil {
		t.Fatal(err)
	}
	if f.gcCalls != 0 || f.reconcileCalls != 0 {
		t.Fatalf("dry-run must be a no-op: gc=%d reconcile=%d", f.gcCalls, f.reconcileCalls)
	}
}

func TestWorkersRequireHandlers(t *testing.T) {
	if err := (&gcWorker{}).Work(context.Background(), &river.Job[GCJobArgs]{Args: GCJobArgs{InstanceID: "i"}}); err == nil {
		t.Fatal("gc worker must fail without handlers")
	}
}

func TestValidationErrorIsReadable(t *testing.T) {
	err := ImageGCJobArgs{KeepDigests: 0}.Validate()
	if err == nil || !strings.Contains(err.Error(), "keep_digests") {
		t.Fatalf("err = %v", err)
	}
}

func TestReapIdleJobArgsValidateMaxLife(t *testing.T) {
	if err := (ReapIdleJobArgs{InstanceID: "i", MaxLifeSeconds: -1}).Validate(); err == nil {
		t.Fatal("negative max_life_seconds must be rejected")
	} else if !strings.Contains(err.Error(), "max_life_seconds") {
		t.Fatalf("err = %v", err)
	}
	for _, secs := range []int{0, 1, 21600} {
		if err := (ReapIdleJobArgs{InstanceID: "i", MaxLifeSeconds: secs}).Validate(); err != nil {
			t.Errorf("max_life_seconds=%d rejected: %v", secs, err)
		}
	}
}

func TestReapIdleWorkerPassesBothDurations(t *testing.T) {
	f := &fakeHandlers{}
	w := &reapIdleWorker{handlers: f}
	job := &river.Job[ReapIdleJobArgs]{Args: ReapIdleJobArgs{
		InstanceID: "inst", IdleTTLSeconds: 600, MaxLifeSeconds: 7200,
	}}
	if err := w.Work(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if f.reapCalls != 1 {
		t.Fatalf("reap calls = %d, want 1", f.reapCalls)
	}
	if f.reapTTL != 600*time.Second {
		t.Fatalf("ttl = %s, want 600s", f.reapTTL)
	}
	if f.reapMaxLife != 7200*time.Second {
		t.Fatalf("maxLife = %s, want 7200s", f.reapMaxLife)
	}
}

func TestReapIdleWorkerZeroMaxLifePassesThrough(t *testing.T) {
	f := &fakeHandlers{}
	w := &reapIdleWorker{handlers: f}
	job := &river.Job[ReapIdleJobArgs]{Args: ReapIdleJobArgs{InstanceID: "inst", IdleTTLSeconds: 60}}
	if err := w.Work(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	// 0 is the "use the runtime default" signal; the worker must not invent one.
	if f.reapMaxLife != 0 {
		t.Fatalf("maxLife = %s, want 0 (runtime default)", f.reapMaxLife)
	}
}
