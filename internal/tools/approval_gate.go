package tools

// ApprovalGate is the HITL approval hook consulted by Registry.Execute as
// the LAST step before tool.Execute — after the circuit breaker, tool
// lookup and required-parameter validation — so a breaker-OPEN or malformed
// call never burns an approval slot (REDESIGN §5.2 R2, the "第 0.5 环"
// layered beside the six-ring chain documented in AGENTS.md). The web layer
// installs one implementation per scan registry (SetApprovalGate); a
// registry without a gate behaves exactly as before.
//
// Contract:
//   - BeforeExecute blocks until the operator decides, the deadline elapses
//     (timeout = rejection, D5), or the scan context is cancelled. It NEVER
//     returns a Go error: a verdict is an expected, operator-facing outcome.
//   - A denial is delivered as Result{Output: denial text, Success: false},
//     before tool.Execute runs and with an empty Error — an expected
//     rejection is not a tool failure and must not feed RecordFailure (the
//     failure semantics are pinned by registry_test.go; AGENTS.md 已知脆弱点).
//   - Implementations must not mutate req.Args and must not retain the map
//     beyond string extraction (the registry hands a private copy).
type ApprovalGate interface {
	BeforeExecute(req ApprovalRequest) ApprovalVerdict
}

// ApprovalRequest is the material a gate needs to classify one call. The
// registry already carries the ScanContext ID; the web adapter carries the
// instance binding (REDESIGN §5.2 R2).
type ApprovalRequest struct {
	ToolName      string
	Args          map[string]string
	ScanContextID string
}

// ApprovalVerdict is the gate outcome. Allowed=true executes; Allowed=false
// short-circuits Execute with Output as the tool result the model sees
// (status + reason + suggested alternative path, styled after the existing
// ⛔ block messages).
type ApprovalVerdict struct {
	Allowed bool
	Output  string
}

// SetApprovalGate installs the gate consulted by Execute. nil (the default)
// keeps legacy behavior byte-for-byte.
func (r *Registry) SetApprovalGate(g ApprovalGate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.approvalGate = g
}

// ApprovalGate returns the gate installed with SetApprovalGate, or nil.
func (r *Registry) ApprovalGate() ApprovalGate {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.approvalGate
}
