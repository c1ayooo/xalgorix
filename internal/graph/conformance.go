package graph

// conformance.go — read-only runtime checks that answer one question:
// "is the platform still following the design?"
//
// The checks are intentionally structural. They prove that the authority
// graph is internally consistent and that the projection from the field
// authorities (ledger, reporting, objective) did not silently drop or invent
// nodes. They do NOT prove scan quality, and they never repair state.
//
// See docs/INFO_GRAPH_CONTRACT.md §2/§3 for the invariant table, severity
// semantics and the auto-repair boundary.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Violation codes are stable API: tests, audit views and operators match on
// them. Do not rename without updating docs/INFO_GRAPH_CONTRACT.md.
const (
	CodeEdgeDangling       = "E_EDGE_DANGLING"
	CodeIntentDoneNoYield  = "E_INTENT_DONE_NO_YIELD"
	CodeYieldKind          = "E_YIELD_KIND"
	CodeIntentRunningOwner = "E_INTENT_RUNNING_NO_OWNER"
	CodeIntentTerminalOwn  = "E_INTENT_TERMINAL_OWNER"
	CodeIntentLeaseExpired = "E_INTENT_LEASE_EXPIRED"
	CodeConfirmMissing     = "E_CONFIRM_MISSING"
	CodePreconditionUnmet  = "E_PRECONDITION_UNMET"
	CodeIntentOpenComplete = "E_INTENT_OPEN_COMPLETED"
	CodeFactNoProvenance   = "E_FACT_NO_PROVENANCE"
	CodeBeliefProvenNoFact = "E_BELIEF_PROVEN_NO_FACT"
	CodeGoalMetNoProof     = "E_GOAL_MET_NO_PROOF"
	CodeUnitKeyDup         = "E_UNIT_KEY_DUP"
	CodePayloadInvalid     = "E_PAYLOAD_INVALID"
	CodeProjectionMissing  = "E_PROJECTION_MISSING"
	CodeProjectionStale    = "E_PROJECTION_STALE"
)

// Severity splits "the design is violated" (error) from "needs attention /
// transient" (warning). Only errors make a report not OK.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// maxViolations bounds one report so a corrupted graph cannot flood an API
// response or the audit log. Truncated is reported explicitly.
const maxViolations = 500

// maxSampleIDs bounds the projection sample lists.
const maxSampleIDs = 50

// Violation is one structural finding. It carries identifiers, never target
// response bodies or model prose.
type Violation struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Kind     Kind     `json:"kind,omitempty"`
	UnitKey  string   `json:"unit_key,omitempty"`
	NodeID   int64    `json:"node_id,omitempty"`
	Detail   string   `json:"detail"`
}

// ProjectionCounts is the ledger/reporting/objective ↔ graph reconciliation.
type ProjectionCounts struct {
	Expected  int      `json:"expected"`
	Observed  int      `json:"observed"`
	Missing   []string `json:"missing,omitempty"`
	Stale     []string `json:"stale,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

// Report is one conformance snapshot.
type Report struct {
	ScanID     string            `json:"scan_id"`
	CheckedAt  time.Time         `json:"checked_at"`
	OK         bool              `json:"ok"`
	Errors     int               `json:"errors"`
	Warnings   int               `json:"warnings"`
	Violations []Violation       `json:"violations"`
	Projection *ProjectionCounts `json:"projection,omitempty"`
	Truncated  bool              `json:"truncated,omitempty"`
}

// ConformanceRecorder persists one read-only conformance snapshot. The PG
// repository implements it; the memory repository may not. Recording never
// repairs or rewrites graph state.
type ConformanceRecorder interface {
	RecordConformance(ctx context.Context, report Report) error
}

// ConformanceMaintenance extends the recorder with run-lifecycle cleanup so
// retention does not leave audit snapshots behind.
type ConformanceMaintenance interface {
	ConformanceRecorder
	DeleteConformance(ctx context.Context, scanID string) error
}

// Summary is the compact shape embedded in GET /graph.
func (r Report) Summary() map[string]any {
	out := map[string]any{
		"ok":        r.OK,
		"errors":    r.Errors,
		"warnings":  r.Warnings,
		"truncated": r.Truncated,
	}
	if len(r.Violations) > 0 {
		v := r.Violations[0]
		out["first"] = v.Code
		out["first_detail"] = v.Detail
	}
	if r.Projection != nil {
		out["projection_missing"] = len(r.Projection.Missing)
		out["projection_stale"] = len(r.Projection.Stale)
	}
	return out
}

// ValidateSnapshot checks the graph-internal invariants over one immutable
// snapshot. Pure function: no repository access, no clock reads beyond now.
func ValidateSnapshot(scanID string, nodes []Node, edges []Edge, now time.Time) Report {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rep := Report{ScanID: scanID, CheckedAt: now, Violations: make([]Violation, 0)}

	byID := make(map[int64]Node, len(nodes))
	byUnit := make(map[Kind]map[string]*Node, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		byID[n.ID] = *n
		if strings.TrimSpace(n.UnitKey) != "" {
			if byUnit[n.Kind] == nil {
				byUnit[n.Kind] = make(map[string]*Node)
			}
			if prev, ok := byUnit[n.Kind][n.UnitKey]; ok {
				rep.add(Violation{
					Code: CodeUnitKeyDup, Severity: SeverityError, Kind: n.Kind,
					UnitKey: n.UnitKey, NodeID: n.ID,
					Detail: fmt.Sprintf("unit key also used by node %d", prev.ID),
				})
			} else {
				byUnit[n.Kind][n.UnitKey] = n
			}
		}
		if !validPayload(n.Payload) {
			rep.add(Violation{
				Code: CodePayloadInvalid, Severity: SeverityError, Kind: n.Kind,
				UnitKey: n.UnitKey, NodeID: n.ID, Detail: "payload is not a JSON object",
			})
		}
	}

	incoming := make(map[int64][]Edge, len(edges))
	outgoing := make(map[int64][]Edge, len(edges))
	for _, e := range edges {
		src, okSrc := byID[e.SrcID]
		dst, okDst := byID[e.DstID]
		if !okSrc {
			rep.add(Violation{
				Code: CodeEdgeDangling, Severity: SeverityError,
				NodeID: e.SrcID, Detail: fmt.Sprintf("edge source missing (%s -> %d)", e.Rel, e.DstID),
			})
			continue
		}
		if !okDst {
			rep.add(Violation{
				Code: CodeEdgeDangling, Severity: SeverityError,
				NodeID: e.DstID, Detail: fmt.Sprintf("edge destination missing (%d -%s->)", e.SrcID, e.Rel),
			})
			continue
		}
		incoming[e.DstID] = append(incoming[e.DstID], e)
		outgoing[e.SrcID] = append(outgoing[e.SrcID], e)
		if e.Rel == RelYields {
			if src.Kind != KindIntent || (dst.Kind != KindFact && dst.Kind != KindFinding) {
				rep.add(Violation{
					Code: CodeYieldKind, Severity: SeverityError, Kind: src.Kind,
					UnitKey: src.UnitKey, NodeID: src.ID,
					Detail: fmt.Sprintf("yields edge must be intent -> fact/finding (got %s -> %s)", src.Kind, dst.Kind),
				})
			}
		}
	}

	for i := range nodes {
		n := &nodes[i]
		switch n.Kind {
		case KindIntent:
			checkIntent(&rep, n, incoming, outgoing, byUnit, now)
		case KindFact:
			if n.State == StateConfirmed || n.State == StateOrigin {
				// Evidence facts point at the belief they support (outgoing
				// supports); intent receipts are pointed at by their intent
				// (incoming yields). Either direction is provenance.
				if !hasAnyRel(outgoing[n.ID], RelSupports) && !hasAnyRel(incoming[n.ID], RelYields) {
					rep.add(Violation{
						Code: CodeFactNoProvenance, Severity: SeverityError, Kind: n.Kind,
						UnitKey: n.UnitKey, NodeID: n.ID,
						Detail: "fact has no supports or yields edge",
					})
				}
			}
		case KindBelief:
			if n.State == StateProven && !hasAnyRel(incoming[n.ID], RelSupports) {
				rep.add(Violation{
					Code: CodeBeliefProvenNoFact, Severity: SeverityWarning, Kind: n.Kind,
					UnitKey: n.UnitKey, NodeID: n.ID,
					Detail: "proven belief has no supporting fact",
				})
			}
		case KindGoal:
			if n.State == StateMet && !hasAnyRel(incoming[n.ID], RelProves) && !goalExempt(n) {
				rep.add(Violation{
					Code: CodeGoalMetNoProof, Severity: SeverityWarning, Kind: n.Kind,
					UnitKey: n.UnitKey, NodeID: n.ID,
					Detail: "met goal has no proves edge and is not manual/capability",
				})
			}
		}
	}

	rep.finalize()
	return rep
}

func checkIntent(rep *Report, n *Node, incoming, outgoing map[int64][]Edge,
	byUnit map[Kind]map[string]*Node, now time.Time) {
	switch n.State {
	case StateRunning:
		if strings.TrimSpace(n.Owner) == "" {
			rep.add(Violation{
				Code: CodeIntentRunningOwner, Severity: SeverityError, Kind: n.Kind,
				UnitKey: n.UnitKey, NodeID: n.ID, Detail: "running intent has no owner",
			})
		}
		if n.LeaseUntil == nil {
			rep.add(Violation{
				Code: CodeIntentLeaseExpired, Severity: SeverityWarning, Kind: n.Kind,
				UnitKey: n.UnitKey, NodeID: n.ID, Detail: "running intent has no lease",
			})
		} else if n.LeaseUntil.Before(now) {
			rep.add(Violation{
				Code: CodeIntentLeaseExpired, Severity: SeverityWarning, Kind: n.Kind,
				UnitKey: n.UnitKey, NodeID: n.ID,
				Detail: "lease expired at " + n.LeaseUntil.UTC().Format(time.RFC3339),
			})
		}
	case StateOpen:
		if n.CompletedAt != nil {
			rep.add(Violation{
				Code: CodeIntentOpenComplete, Severity: SeverityError, Kind: n.Kind,
				UnitKey: n.UnitKey, NodeID: n.ID, Detail: "open intent has completed_at",
			})
		}
	case StateDone:
		if !hasAnyRel(outgoing[n.ID], RelYields) {
			rep.add(Violation{
				Code: CodeIntentDoneNoYield, Severity: SeverityError, Kind: n.Kind,
				UnitKey: n.UnitKey, NodeID: n.ID, Detail: "done intent has no yields edge",
			})
		}
	}
	if TerminalState(KindIntent, n.State) {
		if strings.TrimSpace(n.Owner) != "" || n.LeaseUntil != nil {
			rep.add(Violation{
				Code: CodeIntentTerminalOwn, Severity: SeverityError, Kind: n.Kind,
				UnitKey: n.UnitKey, NodeID: n.ID, Detail: "terminal intent still holds owner/lease",
			})
		}
	}

	c, err := ParseContractStrict(n.Payload)
	if err != nil {
		rep.add(Violation{
			Code: CodePayloadInvalid, Severity: SeverityError, Kind: n.Kind,
			UnitKey: n.UnitKey, NodeID: n.ID, Detail: err.Error(),
		})
		return
	}
	if (n.State == StateRunning || n.State == StateDone) && c.NeedsConfirmation() {
		rep.add(Violation{
			Code: CodeConfirmMissing, Severity: SeverityError, Kind: n.Kind,
			UnitKey: n.UnitKey, NodeID: n.ID, Detail: "confirmation required but not recorded",
		})
	}
	if n.State == StateRunning || n.State == StateDone {
		for _, pre := range c.Preconditions {
			set := byUnit[pre.RefKind]
			got, ok := set[pre.Ref]
			if !ok {
				rep.add(Violation{
					Code: CodePreconditionUnmet, Severity: SeverityError, Kind: n.Kind,
					UnitKey: n.UnitKey, NodeID: n.ID,
					Detail: fmt.Sprintf("precondition %s/%s is missing", pre.RefKind, pre.Ref),
				})
				continue
			}
			if got.State != pre.NeedState {
				rep.add(Violation{
					Code: CodePreconditionUnmet, Severity: SeverityError, Kind: n.Kind,
					UnitKey: n.UnitKey, NodeID: n.ID,
					Detail: fmt.Sprintf("precondition %s/%s is %s, want %s", pre.RefKind, pre.Ref, got.State, pre.NeedState),
				})
			}
		}
	}
}

func (r *Report) add(v Violation) {
	if len(r.Violations) >= maxViolations {
		r.Truncated = true
		return
	}
	r.Violations = append(r.Violations, v)
}

func (r *Report) finalize() {
	sort.SliceStable(r.Violations, func(i, j int) bool {
		a, b := r.Violations[i], r.Violations[j]
		if a.Severity != b.Severity {
			return a.Severity == SeverityError
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.NodeID != b.NodeID {
			return a.NodeID < b.NodeID
		}
		return a.UnitKey < b.UnitKey
	})
	for _, v := range r.Violations {
		if v.Severity == SeverityError {
			r.Errors++
		} else {
			r.Warnings++
		}
	}
	r.OK = r.Errors == 0
}

// ValidateStore reads one run's graph and validates the snapshot.
func ValidateStore(ctx context.Context, repo Repository, scanID string) (Report, error) {
	if repo == nil {
		return Report{}, fmt.Errorf("graph: no repository")
	}
	nodes, edges, err := readGraph(ctx, repo, scanID)
	if err != nil {
		return Report{}, err
	}
	return ValidateSnapshot(scanID, nodes, edges, time.Now().UTC()), nil
}

// CheckProjection reconciles the field authorities' full ID sets against the
// graph projection. An empty expected set for a kind means "not authoritative
// right now" and skips the stale check, so a turn that has not projected a
// finding yet is not reported as drift.
func CheckProjection(ctx context.Context, repo Repository, scanID string, expected map[Kind][]string) (ProjectionCounts, []Violation, error) {
	if repo == nil {
		return ProjectionCounts{}, nil, fmt.Errorf("graph: no repository")
	}
	counts := ProjectionCounts{}
	var violations []Violation
	for _, kind := range allKinds() {
		want := expected[kind]
		nodes, err := repo.ListNodes(ctx, scanID, kind)
		if err != nil {
			return counts, nil, err
		}
		observed := make(map[string]struct{}, len(nodes))
		for _, n := range nodes {
			if strings.TrimSpace(n.UnitKey) == "" {
				continue
			}
			observed[n.UnitKey] = struct{}{}
		}
		counts.Observed += len(observed)
		seen := make(map[string]struct{}, len(want))
		for _, key := range want {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			counts.Expected++
			if _, ok := observed[key]; ok {
				continue
			}
			if len(counts.Missing) < maxSampleIDs {
				counts.Missing = append(counts.Missing, key)
			} else {
				counts.Truncated = true
			}
			violations = append(violations, Violation{
				Code: CodeProjectionMissing, Severity: SeverityError, Kind: kind,
				UnitKey: key, Detail: "authority has this id but the graph has no node",
			})
		}
		if len(want) == 0 {
			continue
		}
		for key := range observed {
			if _, ok := seen[key]; ok {
				continue
			}
			// Engine intent receipts (`intent-result:*`) are produced by
			// ConcludeIntent, not projected from ledger evidence. They carry a
			// yields edge and are not stale graph drift.
			if kind == KindFact && strings.HasPrefix(key, "intent-result:") {
				continue
			}
			if len(counts.Stale) < maxSampleIDs {
				counts.Stale = append(counts.Stale, key)
			} else {
				counts.Truncated = true
			}
			violations = append(violations, Violation{
				Code: CodeProjectionStale, Severity: SeverityWarning, Kind: kind,
				UnitKey: key, Detail: "graph node has no matching authority id",
			})
		}
	}
	sort.Strings(counts.Missing)
	sort.Strings(counts.Stale)
	sortViolations(violations)
	return counts, violations, nil
}

func readGraph(ctx context.Context, repo Repository, scanID string) ([]Node, []Edge, error) {
	var nodes []Node
	for _, kind := range allKinds() {
		items, err := repo.ListNodes(ctx, scanID, kind)
		if err != nil {
			return nil, nil, err
		}
		nodes = append(nodes, items...)
	}
	edges, err := repo.ListEdges(ctx, scanID)
	if err != nil {
		return nil, nil, err
	}
	return nodes, edges, nil
}

func allKinds() []Kind {
	return []Kind{KindGoal, KindIntent, KindBelief, KindFact, KindFinding, KindHint, KindDigest}
}

func hasAnyRel(edges []Edge, rels ...Rel) bool {
	for _, e := range edges {
		for _, r := range rels {
			if e.Rel == r {
				return true
			}
		}
	}
	return false
}

func validPayload(payload string) bool {
	payload = strings.TrimSpace(payload)
	if payload == "" || !strings.HasPrefix(payload, "{") {
		return false
	}
	var obj map[string]any
	return json.Unmarshal([]byte(payload), &obj) == nil
}

func goalExempt(n *Node) bool {
	p := n.PayloadJSON()
	verify, _ := p["verify_by"].(string)
	if strings.EqualFold(verify, "manual") {
		return true
	}
	kind, _ := p["kind"].(string)
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch {
	case kind == "manual", kind == "coverage":
		return true
	case strings.HasPrefix(kind, "capability:"):
		return true
	}
	return false
}

func sortViolations(v []Violation) {
	sort.SliceStable(v, func(i, j int) bool {
		if v[i].Severity != v[j].Severity {
			return v[i].Severity == SeverityError
		}
		if v[i].Code != v[j].Code {
			return v[i].Code < v[j].Code
		}
		if v[i].Kind != v[j].Kind {
			return v[i].Kind < v[j].Kind
		}
		if v[i].UnitKey != v[j].UnitKey {
			return v[i].UnitKey < v[j].UnitKey
		}
		return v[i].NodeID < v[j].NodeID
	})
}
