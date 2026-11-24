package orchestrator

// failure.go — the worker-settlement failure taxonomy (R8.4).
//
// Settlement is a small deterministic table, deliberately separate from the
// pool loop so it can be tested without goroutines or a graph. The classes are
// stable strings for logs/audit; the returned state is the graph transition.

import (
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
)

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

// isCancelLike reports whether the worker's terminal status is the expected
// consequence of an engine cancel, rather than an independent failure. A
// reclaim reason may only replace the generic settlement reason in that case;
// otherwise the real failure class is preserved next to the reclaim cause.
func isCancelLike(status, errText string) bool {
	if status == "timeout" {
		return true
	}
	text := strings.ToLower(errText)
	for _, marker := range []string{"parent scan stopped", "context canceled", "context deadline exceeded", "killed"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// settleReasonAfterReclaim composes the audit reason for a reclaimed worker and
// reports whether the worker exit is confirmed. A non-cancel failure keeps its
// class next to the reclaim cause instead of being overwritten; a timeout status
// means the worker had not closed its done channel when the run path gave up.
func settleReasonAfterReclaim(reclaimed, status, errText string, class FailureClass) (reason string, exitConfirmed bool) {
	reason = reclaimed
	if !isCancelLike(status, errText) && strings.TrimSpace(errText) != "" {
		reason += "; worker reported " + string(class) + ": " + errText
	}
	if status == "timeout" {
		return reason + "; exit=unconfirmed", false
	}
	return reason + "; exit=confirmed", true
}
