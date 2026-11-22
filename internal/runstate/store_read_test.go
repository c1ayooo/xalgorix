package runstate

// Pure unit tests for the R6.1/R6.2 shared read base: window-argument
// semantics, the journal payload decode, and the watermark stream naming.
// SQL behavior (ordering, windows, totals) is pinned by the DSN-gated
// integration tests in reads_integration_test.go.

import (
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
)

func TestClampWindow(t *testing.T) {
	cases := []struct {
		name                  string
		offset, limit         int
		wantOffset, wantLimit int
	}{
		{"negative offset clamps to 0", -5, 10, 0, 10},
		{"negative limit is the unbounded sentinel", 3, -1, 3, 0},
		{"zero limit is unbounded", 3, 0, 3, 0},
		{"positive passthrough", 7, 42, 7, 42},
		{"zero offset passthrough", 0, 1, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			off, lim := clampWindow(tc.offset, tc.limit)
			if off != tc.wantOffset || lim != tc.wantLimit {
				t.Fatalf("clampWindow(%d, %d) = (%d, %d), want (%d, %d)",
					tc.offset, tc.limit, off, lim, tc.wantOffset, tc.wantLimit)
			}
		})
	}
}

func TestLimitArg(t *testing.T) {
	if got := limitArg(25); got != 25 {
		t.Fatalf("limitArg(25) = %v, want 25", got)
	}
	if got := limitArg(0); got != nil {
		t.Fatalf("limitArg(0) = %v, want nil (LIMIT NULL = unbounded)", got)
	}
	if got := limitArg(-3); got != nil {
		t.Fatalf("limitArg(-3) = %v, want nil (unbounded sentinel)", got)
	}
}

func TestStateWatermarkKind(t *testing.T) {
	if got := StateWatermarkKind(KindNotes); got != "state:notes" {
		t.Fatalf("StateWatermarkKind(notes) = %q", got)
	}
	if got := StateWatermarkKind(KindTokens); got != "state:tokens" {
		t.Fatalf("StateWatermarkKind(tokens) = %q", got)
	}
}

func TestDecodeJournalRecord(t *testing.T) {
	at := time.Date(2026, 5, 1, 18, 5, 30, 0, time.UTC)
	payload := `{"scan_id":"scan-x","seq":3,"at":"` + at.Format(time.RFC3339) +
		`","actor":{"kind":"engine","source":"test"},"operation":"claim_hypothesis","hypothesis_id":"H-1","summary":"claim H-1"}`
	rec, err := decodeJournalRecord([]byte(payload))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rec.ScanID != "scan-x" || rec.Seq != 3 || rec.Operation != domainjournal.OpClaimHypothesis ||
		rec.HypothesisID != "H-1" || rec.Summary != "claim H-1" || rec.Actor.Kind != domainjournal.ActorEngine {
		t.Fatalf("decoded record = %+v", rec)
	}
	if !rec.At.Equal(at) {
		t.Fatalf("decoded at = %v, want %v", rec.At, at)
	}

	if _, err := decodeJournalRecord([]byte("not-json-on-purpose")); err == nil {
		t.Fatalf("damaged payload decoded without error")
	}
}
