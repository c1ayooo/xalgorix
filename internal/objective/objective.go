// Package objective evaluates whether a task's operator-stated objective has
// been met, using only facts the scan pipeline already accepted.
//
// It is deliberately a pure function over a Signal snapshot:
//
//   - it never writes back, never mutates findings, never touches verification
//     labels, and never blocks a scan (charter §3.5 / §9 — the report pipeline
//     stays the only exit for finding acceptance);
//   - "not structured" is a first-class answer: without an objective there is
//     no attainment to report, and the UI must say so instead of guessing;
//   - "partially succeeded" does not exist: every criterion is met, unmet,
//     undetermined or manual_pending.
//
// The package owns its own Spec types so it stays free of platform/engine
// dependencies; the web layer converts platform.Objective into a Spec.
package objective

// State is the verdict of one criterion.
type State string

const (
	// StateMet means the criterion is satisfied by accepted evidence.
	StateMet State = "met"
	// StateUnmet means evidence exists but does not satisfy the criterion.
	StateUnmet State = "unmet"
	// StateUndetermined means there is not enough evidence to decide either way.
	StateUndetermined State = "undetermined"
	// StateManualPending means the criterion requires a human decision.
	StateManualPending State = "manual_pending"
)

// Criterion kinds (mirrors platform.CriterionKind*).
const (
	KindFindingClass = "finding_class"
	KindCapability   = "capability"
	KindCoverage     = "coverage"
)

// Verification modes (mirrors platform.CriterionVerify*).
const (
	VerifyDeterministic = "deterministic"
	VerifyVerifier      = "verifier"
	VerifyManual        = "manual"
)

// Verification tags carried by accepted findings (internal/tools/reporting).
const (
	TagVerified         = "verified"
	TagExploitProven    = "exploit-proven"
	TagNeedsManualVerif = "needs-manual-verification"
)

// Spec is the evaluator's view of a task objective.
type Spec struct {
	Kind      string
	Statement string
	Criteria  []SpecCriterion
}

// SpecCriterion is one decidable acceptance line.
type SpecCriterion struct {
	ID         string
	Kind       string
	VulnClass  string
	Capability string
	VerifyBy   string
	Required   bool
}

// FindingSignal is one accepted finding (the report pipeline already
// accepted it — the evaluator only observes).
type FindingSignal struct {
	ID           string
	Title        string
	VulnClass    string
	Severity     string
	CWE          string
	Tag          string
	ExploitProof string
}

// HypothesisSignal is one ledger hypothesis (read-only observation).
type HypothesisSignal struct {
	ID         string
	Statement  string
	Status     string
	Confidence float64
}

// Signal is the evidence snapshot handed to the evaluator.
type Signal struct {
	Findings          []FindingSignal
	Hypotheses        []HypothesisSignal
	CoveragePlanned   int
	CoverageCompleted int
}

// CriterionStatus is the per-criterion verdict with its evidence reference.
type CriterionStatus struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	VulnClass  string `json:"vuln_class,omitempty"`
	Capability string `json:"capability,omitempty"`
	VerifyBy   string `json:"verify_by"`
	Required   bool   `json:"required,omitempty"`
	State      State  `json:"state"`
	Reason     string `json:"reason,omitempty"`
	// EvidenceFindingID / EvidenceTag point at the accepted finding that
	// decided the verdict, so the UI can link back to real evidence.
	EvidenceFindingID string `json:"evidence_finding_id,omitempty"`
	EvidenceTag       string `json:"evidence_tag,omitempty"`
}

// Report is the evaluation result. Attainment is display-only.
type Report struct {
	Structured    bool              `json:"structured"`
	Criteria      []CriterionStatus `json:"criteria"`
	Met           int               `json:"met"`
	Unmet         int               `json:"unmet"`
	Undetermined  int               `json:"undetermined"`
	ManualPending int               `json:"manual_pending"`
	Attainment    float64           `json:"attainment"`
}

// VerificationTagOf picks the verification dimension tag of an accepted
// finding. Exactly one dimension tag is attached by the report pipeline;
// empty means the finding carries no label and can never satisfy a
// criterion. Both consumers (web objective_service, agent goal_gate) must
// map identically, hence this shared helper.
func VerificationTagOf(tags []string) string {
	for _, t := range tags {
		switch t {
		case TagVerified, TagExploitProven, TagNeedsManualVerif:
			return t
		}
	}
	return ""
}
