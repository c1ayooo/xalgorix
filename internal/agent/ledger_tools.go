// Package agent — ledger_tools.go registers the hypothesis/evidence ledger
// tools: record_hypothesis, add_hypothesis_evidence, update_hypothesis, and
// read_ledger.
//
// The ledger (internal/scanctx.LedgerStore) is the durable, scan-shared
// "global exploitation context": a typed graph of attack hypotheses and the
// evidence gathered for each. Unlike the in-memory planner Plan (which lives on
// the per-agent ScanState and is neither shared nor persisted), the ledger
// lives on the ScanContext, so the coordinator and every delegated specialist
// read and write the SAME graph, and it survives restart/resume via
// <scanDir>/ledger.json.
//
// These tools let agents:
//   - record_hypothesis: register or refine an attack hypothesis (deduped by
//     vuln class + endpoint + parameter + role).
//   - add_hypothesis_evidence: attach an observation (baseline, probe, exploit
//     attempt, artifact) or link a confirmed finding by its XALG-* id.
//   - update_hypothesis: transition status (queued/testing/proven/rejected/
//     blocked/exhausted), assign an owner, set the next-best action, or record
//     an exploit attempt.
//   - read_ledger: read the current hypotheses (all / schedulable / open).
//     The schedulable listing carries an output-only same-endpoint cross-scan
//     history note per entry when the optional cold-path reader is available
//     (M4 region consumer, see ledger_cross_scan.go), and claim_next_hypothesis
//     appends a same-subject cross-scan history section after a successful
//     claim (M4 association consumer). Both degrade to the unchanged output
//     when the reader is absent or a query fails.
//
// Like plan_tools.go, the tools are deliberately tolerant of malformed input —
// they return a corrective tool result rather than erroring out of the loop.
package agent

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// ledger returns the scan-shared ledger, or nil if this agent has no scan
// context (e.g. some unit tests). Callers must nil-check.
func (a *Agent) ledger() *scanctx.LedgerStore {
	if a.scanCtx == nil {
		return nil
	}
	return a.scanCtx.Ledger
}

// registerLedgerTools adds the five ledger tools. The agent is captured by
// closure so the tools operate on the shared ScanContext ledger.
func (a *Agent) registerLedgerTools(reg *tools.Registry) {
	reg.Register(&tools.Tool{
		Name: "record_hypothesis",
		Description: "Record or refine an attack hypothesis in the shared, durable ledger — the " +
			"team's single source of truth for what to test and what has been proven. Call this as " +
			"soon as recon suggests a plausible weakness (e.g. 'IDOR on /api/orders?id= as user'). " +
			"Hypotheses are deduplicated by vuln_class + endpoint + parameter + role, so re-recording " +
			"the same target refines the existing entry instead of duplicating it. The coordinator " +
			"and every specialist share this ledger, so recording here is how parallel agents avoid " +
			"overlapping and how work is scheduled.",
		Parameters: []tools.Parameter{
			{Name: "vuln_class", Description: "Vulnerability class, e.g. sqli, xss, idor, ssrf, ssti, rce, auth, csrf, lfi, xxe.", Required: true},
			{Name: "endpoint", Description: "Target endpoint/path, e.g. /api/orders. Query string is ignored for dedup.", Required: false},
			{Name: "parameter", Description: "Input/parameter under test, e.g. id, q, redirect.", Required: false},
			{Name: "role", Description: "Auth context this applies to: anonymous, user, admin, or a named role.", Required: false},
			{Name: "title", Description: "Short human-readable title.", Required: false},
			{Name: "target", Description: "Target host/base URL if multi-target.", Required: false},
			{Name: "data_flow", Description: "For whitebox: source->sink path, e.g. 'req.query.id -> db.query'.", Required: false},
			{Name: "required_privilege", Description: "Privilege needed to test (e.g. 'authenticated user').", Required: false},
			{Name: "baseline", Description: "Control/baseline result that distinguishes a real bug from intended behavior.", Required: false},
			{Name: "preconditions", Description: "REQUIRED. Preconditions before testing as a JSON array or comma-separated list. If they are not known yet, pass [\"unknown\"]; never silently omit them.", Required: true},
			{Name: "next_action", Description: "The next concrete step to attempt.", Required: false},
			{Name: "confidence", Description: "0.0-1.0 belief this is exploitable (default 0.5).", Required: false},
			{Name: "voi", Description: "Optional 0.0-1.0 information-decision value; used only as a bounded ranking weight, never as evidence.", Required: false},
			{Name: "status", Description: "Optional initial status: queued (default), testing, blocked.", Required: false},
		},
		Execute: a.recordHypothesisTool,
	})

	reg.Register(&tools.Tool{
		Name: "add_hypothesis_evidence",
		Description: "Attach an observation to a hypothesis: a baseline/control result, a probe " +
			"response, an exploit attempt, an artifact, or a link to a confirmed finding. This is how " +
			"evidence accumulates toward a proof and how other agents see your progress. When you have " +
			"filed a finding with report_vulnerability, link it here with kind=finding_ref and its " +
			"XALG-* id so the hypothesis is backed by the authoritative finding.",
		Parameters: []tools.Parameter{
			{Name: "hypothesis_id", Description: "The hypothesis id from record_hypothesis (e.g. H-3).", Required: true},
			{Name: "kind", Description: "One of: baseline, probe, exploit, artifact, finding_ref.", Required: false},
			{Name: "nature", Description: "observed (raw request/response/tool output) or inferred (your interpretation; default). observed requires request or response.", Required: false},
			{Name: "supports", Description: "For inferred evidence: JSON array or comma-separated Evidence IDs in this hypothesis that substantiate the inference.", Required: false},
			{Name: "summary", Description: "What was observed and why it matters. Avoid presenting interpretation as raw observation.", Required: true},
			{Name: "request", Description: "Optional raw request evidence (bounded).", Required: false},
			{Name: "response", Description: "Optional raw response evidence (bounded).", Required: false},
			{Name: "finding_id", Description: "When kind=finding_ref, the reported finding id (e.g. XALG-3).", Required: false},
			{Name: "confidence", Description: "0.0-1.0 quality of this observation.", Required: false},
		},
		Execute: a.addHypothesisEvidenceTool,
	})

	reg.Register(&tools.Tool{
		Name: "update_hypothesis",
		Description: "Update a hypothesis's lifecycle: set its status, assign an owner, record the " +
			"next-best action, or log an exploit attempt. Statuses: queued (identified, untested), " +
			"testing (being worked), proven (exploited with evidence), rejected (disproven by a " +
			"baseline), blocked (needs a precondition), exhausted (attempts spent, no proof). Keep this " +
			"current so the shared ledger reflects reality and the scheduler does not re-open settled work.",
		Parameters: []tools.Parameter{
			{Name: "hypothesis_id", Description: "The hypothesis id (e.g. H-3).", Required: true},
			{Name: "status", Description: "One of: queued, testing, proven, rejected, blocked, exhausted.", Required: false},
			{Name: "next_action", Description: "The next concrete step to attempt.", Required: false},
			{Name: "assigned_to", Description: "Owner (agent/delegation id) taking this hypothesis.", Required: false},
			{Name: "record_attempt", Description: "Set to true to increment the exploit-attempt counter.", Required: false},
			{Name: "voi", Description: "Optional 0.0-1.0 information-decision value for deterministic ranking.", Required: false},
		},
		Execute: a.updateHypothesisTool,
	})

	reg.Register(&tools.Tool{
		Name: "read_ledger",
		Description: "Read the shared hypothesis/evidence ledger. Use filter=schedulable to get the " +
			"highest-value untested hypotheses to work next, filter=open for active/queued work, or " +
			"filter=all for everything. Check this before starting new work so you build on the team's " +
			"findings instead of repeating them.",
		Parameters: []tools.Parameter{
			{Name: "filter", Description: "One of: open (default), schedulable, all.", Required: false},
			{Name: "limit", Description: "Max hypotheses to return for schedulable (default 10).", Required: false},
		},
		Execute: a.readLedgerTool,
	})

	reg.Register(&tools.Tool{
		Name: "claim_next_hypothesis",
		Description: "Atomically claim the single highest-value untested hypothesis from the shared " +
			"ledger to work next. This deterministically picks the top QUEUED hypothesis (highest " +
			"confidence first), optionally filtered to your vuln_class, assigns it to you, and moves it " +
			"to 'testing' so no other agent works the same target. Prefer this over eyeballing " +
			"read_ledger: it guarantees you take the most promising lead and prevents two specialists " +
			"from colliding. Returns the hypothesis and its next action; if nothing is claimable it says so.",
		Parameters: []tools.Parameter{
			{Name: "vuln_class", Description: "Optional: only claim a hypothesis of this class (e.g. idor, sqli, xss, ssrf). Omit to claim the top hypothesis of any class.", Required: false},
		},
		Execute: a.claimNextHypothesisTool,
	})
}

func (a *Agent) recordHypothesisTool(args map[string]string) (tools.Result, error) {
	l := a.ledger()
	if l == nil {
		return tools.Result{Error: "ledger unavailable in this context"}, nil
	}
	vc := strings.TrimSpace(args["vuln_class"])
	if vc == "" {
		return tools.Result{Error: "vuln_class is required (e.g. sqli, idor, ssrf)"}, nil
	}
	conf := 0.5
	if raw := strings.TrimSpace(args["confidence"]); raw != "" {
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			conf = f
		}
	}
	voi, voiEstimated, err := parseVOIArg(args["voi"])
	if err != nil {
		return tools.Result{Error: err.Error()}, nil
	}
	preconditions := parsePreconditions(args["preconditions"])
	if len(preconditions) == 0 {
		return tools.Result{Error: "preconditions is required. Pass the concrete prerequisites as a JSON array, or [\"unknown\"] when they are not yet known."}, nil
	}
	h := scanctx.Hypothesis{
		VulnClass:         vc,
		Endpoint:          strings.TrimSpace(args["endpoint"]),
		Parameter:         strings.TrimSpace(args["parameter"]),
		Role:              strings.TrimSpace(args["role"]),
		Title:             strings.TrimSpace(args["title"]),
		Target:            strings.TrimSpace(args["target"]),
		DataFlow:          strings.TrimSpace(args["data_flow"]),
		RequiredPrivilege: strings.TrimSpace(args["required_privilege"]),
		Baseline:          strings.TrimSpace(args["baseline"]),
		NextAction:        strings.TrimSpace(args["next_action"]),
		Preconditions:     preconditions,
		Confidence:        conf,
		VOI:               voi,
		VOIEstimated:      voiEstimated,
		Origin:            a.ledgerOrigin(),
	}
	if st := strings.TrimSpace(args["status"]); st != "" {
		h.Status = scanctx.NormalizeHypothesisStatus(st)
	}
	actor := scanctx.AuditActorForAgent(a.ledgerOrigin())
	stored, err := l.UpsertAs(h, actor)
	if err != nil {
		return tools.Result{Error: "hypothesis audit/persistence failed: " + err.Error()}, nil
	}
	return tools.Result{
		Output: fmt.Sprintf("Hypothesis %s recorded [%s] %s%s (confidence %.2f). Update it as you gather evidence.",
			stored.ID, stored.Status, stored.VulnClass, formatLoc(stored), stored.Confidence),
		Metadata: map[string]any{"hypothesis_id": stored.ID},
	}, nil
}

func (a *Agent) addHypothesisEvidenceTool(args map[string]string) (tools.Result, error) {
	l := a.ledger()
	if l == nil {
		return tools.Result{Error: "ledger unavailable in this context"}, nil
	}
	id := strings.TrimSpace(args["hypothesis_id"])
	if id == "" {
		return tools.Result{Error: "hypothesis_id is required"}, nil
	}
	summary := strings.TrimSpace(args["summary"])
	if summary == "" {
		return tools.Result{Error: "summary is required"}, nil
	}
	kind := strings.ToLower(strings.TrimSpace(args["kind"]))
	findingID := strings.TrimSpace(args["finding_id"])
	if kind == scanctx.EvidenceFindingRef || findingID != "" {
		if findingID == "" {
			return tools.Result{Error: "finding_id is required for a finding_ref"}, nil
		}
		if kind != "" && kind != scanctx.EvidenceFindingRef {
			return tools.Result{Error: "finding_id requires kind=finding_ref"}, nil
		}
		if !reportedFindingIDs(a.scanCtx.ID)[strings.ToUpper(findingID)] {
			return tools.Result{Error: "finding_id does not match a saved report; call report_vulnerability first, then use its returned ID"}, nil
		}
		kind = scanctx.EvidenceFindingRef
	}
	conf := 0.0
	if raw := strings.TrimSpace(args["confidence"]); raw != "" {
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			conf = f
		}
	}
	nature := scanctx.NormalizeEvidenceNature(scanctx.EvidenceNature(strings.ToLower(strings.TrimSpace(args["nature"]))))
	supports := parsePreconditions(args["supports"])
	current, ok := l.Get(id)
	if !ok {
		return tools.Result{Error: fmt.Sprintf("unknown hypothesis id %q — call record_hypothesis first or read_ledger to list ids", id)}, nil
	}
	if nature == scanctx.EvidenceObserved {
		if len(supports) > 0 {
			return tools.Result{Error: "supports is only valid for inferred evidence; observed evidence must carry its own request/response"}, nil
		}
		if strings.TrimSpace(args["request"]) == "" && strings.TrimSpace(args["response"]) == "" {
			return tools.Result{Error: "observed evidence requires at least one raw request or response field; use nature=inferred for an interpretation without raw data"}, nil
		}
	} else {
		existing := make(map[string]bool, len(current.Evidence))
		for _, prior := range current.Evidence {
			existing[prior.ID] = true
		}
		for _, support := range supports {
			if !existing[support] {
				return tools.Result{Error: fmt.Sprintf("unknown support evidence %q for %s — use read_ledger to list existing Evidence IDs", support, id)}, nil
			}
		}
	}
	ev := scanctx.Evidence{
		Kind:       kind,
		Nature:     nature,
		Supports:   supports,
		Summary:    summary,
		Request:    args["request"],
		Response:   args["response"],
		FindingID:  findingID,
		AgentID:    a.ledgerOrigin(),
		Confidence: conf,
	}
	if err := l.AddEvidenceAs(id, ev, scanctx.AuditActorForAgent(a.ledgerOrigin())); err != nil {
		return tools.Result{Error: "evidence audit/persistence failed: " + err.Error()}, nil
	}
	got, _ := l.Get(id)
	summaryOut := fmt.Sprintf("Evidence added to %s (%d total, nature=%s, confidence now %.2f).", id, len(got.Evidence), nature, got.Confidence)
	if nature == scanctx.EvidenceInferred && len(supports) == 0 {
		summaryOut += " It is unsubstantiated and cannot be used as proof until supporting Evidence IDs are linked."
	}
	return tools.Result{Output: summaryOut}, nil
}

func (a *Agent) updateHypothesisTool(args map[string]string) (tools.Result, error) {
	l := a.ledger()
	if l == nil {
		return tools.Result{Error: "ledger unavailable in this context"}, nil
	}
	id := strings.TrimSpace(args["hypothesis_id"])
	if id == "" {
		return tools.Result{Error: "hypothesis_id is required"}, nil
	}
	if _, ok := l.Get(id); !ok {
		return tools.Result{Error: fmt.Sprintf("unknown hypothesis id %q — use read_ledger to list ids", id)}, nil
	}
	actor := scanctx.AuditActorForAgent(a.ledgerOrigin())
	var applied []string
	if st := strings.TrimSpace(args["status"]); st != "" {
		status := scanctx.NormalizeHypothesisStatus(st)
		if err := l.SetStatusAs(id, status, strings.TrimSpace(args["next_action"]), actor); err != nil {
			return tools.Result{Error: "status audit/persistence failed: " + err.Error()}, nil
		}
		applied = append(applied, "status="+string(status))
	} else if na := strings.TrimSpace(args["next_action"]); na != "" {
		// next_action without a status change still updates the field.
		if cur, ok := l.Get(id); ok {
			if err := l.SetStatusAs(id, cur.Status, na, actor); err != nil {
				return tools.Result{Error: "next_action audit/persistence failed: " + err.Error()}, nil
			}
			applied = append(applied, "next_action")
		}
	}
	if owner := strings.TrimSpace(args["assigned_to"]); owner != "" {
		if err := l.AssignAs(id, owner, actor); err != nil {
			return tools.Result{Error: "assignment audit/persistence failed: " + err.Error()}, nil
		}
		applied = append(applied, "assigned_to="+owner)
	}
	if isTrue(args["record_attempt"]) {
		if err := l.RecordAttemptAs(id, actor); err != nil {
			return tools.Result{Error: "attempt audit/persistence failed: " + err.Error()}, nil
		}
		applied = append(applied, "attempt+1")
	}
	if raw := strings.TrimSpace(args["voi"]); raw != "" {
		voi, estimated, err := parseVOIArg(raw)
		if err != nil {
			return tools.Result{Error: err.Error()}, nil
		}
		if err := l.SetVOIAs(id, voi, estimated, actor); err != nil {
			return tools.Result{Error: "VOI audit/persistence failed: " + err.Error()}, nil
		}
		applied = append(applied, "voi")
	}
	if len(applied) == 0 {
		return tools.Result{Error: "nothing to update — pass status, next_action, assigned_to, record_attempt, or voi"}, nil
	}
	got, _ := l.Get(id)
	return tools.Result{
		Output: fmt.Sprintf("Hypothesis %s updated (%s). Now [%s] confidence %.2f, attempts %d.",
			id, strings.Join(applied, ", "), got.Status, got.Confidence, got.Attempts),
	}, nil
}

func (a *Agent) readLedgerTool(args map[string]string) (tools.Result, error) {
	l := a.ledger()
	if l == nil {
		return tools.Result{Error: "ledger unavailable in this context"}, nil
	}
	filter := strings.ToLower(strings.TrimSpace(args["filter"]))
	if filter == "" {
		filter = "open"
	}
	switch filter {
	case "schedulable", "next":
		limit := 10
		if raw := strings.TrimSpace(args["limit"]); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				limit = n
			}
		}
		sched := l.Schedulable(limit)
		if len(sched) == 0 {
			return tools.Result{Output: "No schedulable hypotheses. Record new ones from recon, or the surface is exhausted."}, nil
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Top %d hypotheses to work next (highest confidence first):\n", len(sched)))
		// M4 region consumer (output-only): per-entry same-endpoint cross-scan
		// history. nil cache (no cold-path reader) keeps the output unchanged.
		regionNotes := newCrossScanRegionCache(a)
		for _, h := range sched {
			b.WriteString(fmt.Sprintf("• %s [%s] %s%s conf=%.2f pre=%s", h.ID, h.Status, h.VulnClass, formatLoc(h), h.Confidence, preconditionSummary(h)))
			if h.VOIEstimated {
				b.WriteString(fmt.Sprintf(" voi=%.2f", h.VOI))
			}
			if h.NextAction != "" {
				b.WriteString(" → " + h.NextAction)
			}
			b.WriteString("\n")
			regionNotes.writeTo(&b, h.Endpoint)
		}
		return tools.Result{Output: b.String()}, nil
	case "all":
		all := l.All()
		if len(all) == 0 {
			return tools.Result{Output: "Ledger is empty. Record hypotheses as recon reveals attack surface."}, nil
		}
		var b strings.Builder
		counts := l.Counts()
		b.WriteString(fmt.Sprintf("Ledger: %d hypotheses (queued=%d testing=%d proven=%d rejected=%d blocked=%d exhausted=%d)\n",
			len(all), counts[scanctx.HypothesisQueued], counts[scanctx.HypothesisTesting], counts[scanctx.HypothesisProven],
			counts[scanctx.HypothesisRejected], counts[scanctx.HypothesisBlocked], counts[scanctx.HypothesisExhausted]))
		for _, h := range all {
			b.WriteString(fmt.Sprintf("• %s [%s] %s%s conf=%.2f evidence=%d pre=%s\n", h.ID, h.Status, h.VulnClass, formatLoc(h), h.Confidence, len(h.Evidence), preconditionSummary(h)))
			if h.VOIEstimated {
				b.WriteString(fmt.Sprintf("    voi=%.2f estimated\n", h.VOI))
			}
			writeEvidenceRefs(&b, h.Evidence)
		}
		return tools.Result{Output: b.String()}, nil
	default: // "open"
		out := l.FormatForContext()
		if out == "" {
			return tools.Result{Output: "Ledger is empty. Record hypotheses as recon reveals attack surface."}, nil
		}
		return tools.Result{Output: out}, nil
	}
}

func (a *Agent) claimNextHypothesisTool(args map[string]string) (tools.Result, error) {
	// Recon-first sequencing (coordinator only): vulnerability testing enters
	// the hypothesis ledger only after comprehensive reconnaissance mapped
	// the surface — same milestone the specialist wave waits for. Testing
	// from a half-mapped surface is what makes identical targets produce
	// wildly different finding sets between runs. Specialists are exempt:
	// the wave itself is gated on the same milestone, so children always
	// start from a complete surface. Bounded bypass (3 blocks) so a target
	// where a milestone is genuinely impossible cannot deadlock the scan.
	if a.state != nil && a.state.ProfessionalAssessment && a.delegatedAgentID == "" &&
		!a.ctfMission && !a.state.DiscoveryMode && !a.reconPhaseComplete() {
		if a.state.ReconGateBlocks < 3 {
			a.state.ReconGateBlocks++
			return tools.Result{Error: fmt.Sprintf(
				"RECON FIRST: comprehensive reconnaissance must complete before hypothesis testing. Still missing: %s. "+
					"Complete them, refresh the Endpoint Inventory note, then claim again.",
				strings.Join(a.reconIncompleteReasons(), "; "))}, nil
		}
	}
	l := a.ledger()
	if l == nil {
		return tools.Result{Error: "ledger unavailable in this context"}, nil
	}
	classFilter := strings.ToLower(strings.TrimSpace(args["vuln_class"]))

	owner := a.ledgerOrigin()
	got, claimed, claimErr := l.ClaimNextAs(classFilter, owner, scanctx.AuditActorForAgent(owner))
	if claimErr != nil {
		return tools.Result{Error: "claim audit/persistence failed: " + claimErr.Error()}, nil
	}
	borrowedFromAnotherLane := false
	if !claimed && a.delegatedAgentID != "" {
		// Idle-worker borrowing (P1), specialists only: the lane's class-scoped
		// pool is empty, but another lane's untaken (unowned) queued work
		// remains. Claiming it keeps a live worker busy instead of idling while
		// the scan is still uncovering surface. Lane ownership stays a soft hint
		// (MarkAssigned); hypotheses owned by ANOTHER agent are never stolen.
		// The coordinator keeps its strict class-filter contract — its claim
		// output is the documented scheduling surface (M4) and must not drift.
		if borrowed, ok, err := l.ClaimNextUnassignedAs(owner, scanctx.AuditActorForAgent(owner)); err != nil {
			return tools.Result{Error: "claim audit/persistence failed: " + err.Error()}, nil
		} else if ok {
			got, claimed, borrowedFromAnotherLane = borrowed, true, true
		}
	}
	if !claimed {
		// Idle with no claimable work is a first-class, observable state: the
		// worker row must say "no claimable work" instead of looking busy. The
		// concurrency slot is released on natural completion (delegated agents
		// are never force-cancelled just to free a slot).
		if a.delegatedAgentID != "" {
			a.emit(Event{
				Type:    "agent_idle",
				Content: classFilter,
				Meta: map[string]any{
					"agent_id":   a.delegatedAgentID,
					"vuln_class": classFilter,
				},
			})
		}
		if classFilter != "" {
			return tools.Result{Output: fmt.Sprintf("No queued %q hypotheses to claim. Use read_ledger(filter=schedulable) to see other classes, or record new ones from recon with record_hypothesis.", classFilter)}, nil
		}
		return tools.Result{Output: "No queued hypotheses to claim right now. Record new ones from recon with record_hypothesis, or the surface may be exhausted."}, nil
	}

	var b strings.Builder
	if borrowedFromAnotherLane {
		fmt.Fprintf(&b, "Borrowed unclaimed work from another lane (your assigned classes were exhausted).\n")
	}
	fmt.Fprintf(&b, "Claimed %s — now [%s], owner %s: %s%s (confidence %.2f).\n",
		got.ID, got.Status, owner, got.VulnClass, formatLoc(got), got.Confidence)
	if got.VOIEstimated {
		fmt.Fprintf(&b, "VOI estimate: %.2f\n", got.VOI)
	}
	if got.NextAction != "" {
		fmt.Fprintf(&b, "Next action: %s\n", got.NextAction)
	}
	if got.Baseline != "" {
		fmt.Fprintf(&b, "Baseline/control: %s\n", got.Baseline)
	}
	fmt.Fprintf(&b, "Preconditions: %s\n", preconditionSummary(got))
	if len(got.Evidence) > 0 {
		b.WriteString("Existing evidence (reference these IDs in supports when recording an inference):\n")
		writeEvidenceRefs(&b, got.Evidence)
	}
	b.WriteString("Work this hypothesis, record evidence with add_hypothesis_evidence, then set its final status with update_hypothesis: proven (link the finding via kind=finding_ref) or rejected (with the baseline that ruled it out).")
	// M4 association consumer (output-only): same-subject cross-scan history
	// appended at the end. Reader absence or any query error keeps the output
	// unchanged; claim semantics above are untouched.
	a.appendCrossScanAssociationNote(&b, got)
	return tools.Result{
		Output:   b.String(),
		Metadata: map[string]any{"hypothesis_id": got.ID, "vuln_class": got.VulnClass, "status": string(got.Status)},
	}, nil
}

// ledgerOrigin identifies the writing agent: the delegation ID for a specialist,
// or "coordinator" for the root.
func (a *Agent) ledgerOrigin() string {
	if a.delegatedAgentID != "" {
		return a.delegatedAgentID
	}
	return "coordinator"
}

// formatLoc renders the endpoint/parameter/role suffix for display.
func formatLoc(h scanctx.Hypothesis) string {
	var parts []string
	if h.Endpoint != "" {
		parts = append(parts, h.Endpoint)
	}
	if h.Parameter != "" {
		parts = append(parts, "["+h.Parameter+"]")
	}
	if h.Role != "" {
		parts = append(parts, "as "+h.Role)
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

func preconditionSummary(h scanctx.Hypothesis) string {
	if len(h.Preconditions) == 0 {
		return "unknown"
	}
	return strings.Join(h.Preconditions, "; ")
}

func writeEvidenceRefs(b *strings.Builder, evidence []scanctx.Evidence) {
	for _, ev := range evidence {
		nature := scanctx.NormalizeEvidenceNature(ev.Nature)
		fmt.Fprintf(b, "    - %s [%s/%s] %s", ev.ID, ev.Kind, nature, ev.Summary)
		if len(ev.Supports) > 0 {
			fmt.Fprintf(b, " supports=%s", strings.Join(ev.Supports, ","))
		}
		if len(ev.Supports) == 0 && nature == scanctx.EvidenceInferred && ev.Kind != scanctx.EvidenceFindingRef {
			b.WriteString(" unsubstantiated")
		}
		b.WriteString("\n")
	}
}

// parseVOIArg validates an optional bounded VOI estimate.
func parseVOIArg(raw string) (float64, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return 0, false, fmt.Errorf("voi must be a number within [0,1]")
	}
	return value, true, nil
}

// parsePreconditions accepts either a JSON array or a comma-separated list.
func parsePreconditions(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(raw), &arr); err == nil {
			return arr
		}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// isTrue interprets common truthy tool-argument spellings.
func isTrue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "1", "y":
		return true
	}
	return false
}
