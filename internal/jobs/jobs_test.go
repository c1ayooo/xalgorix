package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/riverqueue/river"
)

type recordingProcessor struct {
	called bool
	args   IndexJobArgs
	err    error
}

func (p *recordingProcessor) ProcessRange(_ context.Context, scanID, scanDir string, from, to uint64) error {
	p.called = true
	p.args = IndexJobArgs{ScanID: scanID, ScanDir: scanDir, FromSeq: from, ToSeq: to}
	return p.err
}

func TestIndexJobArgsValidate(t *testing.T) {
	valid := IndexJobArgs{ScanID: "s", ScanDir: "scans/s", FromSeq: 1, ToSeq: 3}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (IndexJobArgs{ScanID: "s", ScanDir: "scans/s", FromSeq: 2, ToSeq: 1}).Validate(); err == nil {
		t.Fatal("expected inverted range error")
	}
}

func TestIndexWorkerCallsProcessor(t *testing.T) {
	p := &recordingProcessor{}
	w := &indexWorker{processor: p}
	job := &river.Job[IndexJobArgs]{Args: IndexJobArgs{ScanID: "s", ScanDir: "d", FromSeq: 1, ToSeq: 2}}
	if err := w.Work(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if !p.called || p.args != job.Args {
		t.Fatalf("processor not called with job args: %+v", p.args)
	}

	p.err = errors.New("retry")
	if err := w.Work(context.Background(), job); err == nil {
		t.Fatal("expected processor error to propagate for River retry")
	}
}
