package orchestrator

import (
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
