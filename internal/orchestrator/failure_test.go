package orchestrator

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
)

func TestClassifyWorkerStatus(t *testing.T) {
	cases := []struct {
		status string
		state  graph.State
		class  FailureClass
	}{
		{"timeout", graph.StateExhausted, FailureWorkerTimeout},
		{"missing", graph.StateOpen, FailureWorkerMissing},
		{"rejected", graph.StateBlocked, FailureWorkerRejected},
		{"failed", graph.StateBlocked, FailureWorkerError},
		{"provider_error", graph.StateBlocked, FailureWorkerError},
	}
	for _, c := range cases {
		state, class := ClassifyWorkerStatus(c.status, "detail")
		if state != c.state || class != c.class {
			t.Fatalf("%s => (%s,%s), want (%s,%s)", c.status, state, class, c.state, c.class)
		}
	}
}

func TestSettleReasonAfterReclaim(t *testing.T) {
	const reclaim = "no information-layer progress for 20m"
	cases := []struct {
		name       string
		status     string
		errText    string
		class      FailureClass
		wantPart   string
		wantAbsent string
		confirmed  bool
	}{
		{
			name: "cancel-like keeps only the reclaim cause", status: "failed",
			errText: "killed: stagnant: no progress", class: FailureWorkerError,
			wantPart: reclaim + "; exit=confirmed", wantAbsent: "worker reported", confirmed: true,
		},
		{
			name: "independent failure is preserved", status: "failed",
			errText: "provider 500", class: FailureWorkerError,
			wantPart: "worker reported worker_error: provider 500", confirmed: true,
		},
		{
			name: "timeout is unconfirmed", status: "timeout",
			errText: "context deadline exceeded", class: FailureWorkerTimeout,
			wantPart: "exit=unconfirmed", confirmed: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, confirmed := settleReasonAfterReclaim(reclaim, c.status, c.errText, c.class)
			if confirmed != c.confirmed {
				t.Fatalf("confirmed = %v, want %v (reason %q)", confirmed, c.confirmed, reason)
			}
			if !strings.Contains(reason, c.wantPart) {
				t.Fatalf("reason %q missing %q", reason, c.wantPart)
			}
			if c.wantAbsent != "" && strings.Contains(reason, c.wantAbsent) {
				t.Fatalf("reason %q unexpectedly contains %q", reason, c.wantAbsent)
			}
			if !strings.HasPrefix(reason, reclaim) {
				t.Fatalf("reason %q must start with the reclaim cause", reason)
			}
		})
	}
}
