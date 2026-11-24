package orchestrator

// failure.go — the worker-settlement failure taxonomy (R8.4).
//
// Settlement is a small deterministic table, deliberately separate from the
// pool loop so it can be tested without goroutines or a graph. The classes are
// stable strings for logs/audit; the returned state is the graph transition.

import "github.com/c1ayooo/xalgorix/v4/internal/graph"

// FailureClass is the stable worker-settlement class.
type FailureClass string

const (
	FailureNone           FailureClass = ""
	FailureWorkerTimeout  FailureClass = "worker_timeout"
	FailureWorkerMissing  FailureClass = "worker_missing"
	FailureWorkerError    FailureClass = "worker_error"
	FailureWorkerRejected FailureClass = "worker_rejected"
)

// ClassifyWorkerStatus maps a worker result status to the graph state it
// settles into. "completed" is handled by the caller (transactional conclude),
// never by this table.
func ClassifyWorkerStatus(status, errText string) (graph.State, FailureClass) {
	switch status {
	case "timeout":
		return graph.StateExhausted, FailureWorkerTimeout
	case "missing":
		// The worker record disappeared (restart/GC): the honest move is to
		// release the claim so a fresh worker can retry.
		return graph.StateOpen, FailureWorkerMissing
	case "rejected":
		return graph.StateBlocked, FailureWorkerRejected
	default:
		return graph.StateBlocked, FailureWorkerError
	}
}
