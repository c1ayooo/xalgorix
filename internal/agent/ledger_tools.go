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
//     The schedulable listing carries an output-only same-endpoint history note
//     per entry when the optional cold-path reader is available AND the agent
//     has a project scan whitelist (M4 region consumer, see
//     ledger_cross_scan.go), and claim_next_hypothesis appends a same-subject
//     history section after a successful claim (M4 association consumer). Both
//     read only THIS project's scans (the project is the isolation fence) and
//     degrade to the unchanged output when the reader or scope is absent.
//
// Like plan_tools.go, the tools are deliberately tolerant of malformed input —
// they return a corrective tool result rather than erroring out of the loop.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
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
		Description: "Record or refine a FORMAL attack hypothesis in the shared, durable ledger — the " +
			"team's single source of truth for what to test and what has been proven. Only record a " +
			"claim you actually believe and can falsify; a coverage gap or a vulnerability class is not a " +
			"hypothesis. Hypotheses are deduplicated by vuln_class + endpoint + parameter + role, so " +
			"re-recording the same target refines the existing entry instead of duplicating it (and " +
			"promotes an engine coverage seed into a formal hypothesis).",
		Parameters: []tools.Parameter{
			{Name: "vuln_class", Description: "Vulnerability class, e.g. sqli, xss, idor, ssrf, ssti, rce, auth, csrf, lfi, xxe.", Required: true},
			{Name: "endpoint", Description: "Target endpoint/path, e.g. /api/orders. Query string is ignored for dedup.", Required: false},
			{Name: "parameter", Description: "Input/parameter under test, e.g. id, q, redirect.", Required: false},
			{Name: "role", Description: "Auth context this applies to: anonymous, user, admin, or a named role.", Required: false},
			{Name: "title", Description: "REQUIRED. The falsifiable claim in one sentence, e.g. 'GET /api/orders?order_id= as user returns another user's order'.", Required: true},
			{Name: "target", Description: "Target host/base URL if multi-target.", Required: false},
			{Name: "data_flow", Description: "For whitebox: source->sink path, e.g. 'req.query.id -> db.query'.", Required: false},
			{Name: "required_privilege", Description: "Privilege needed to test (e.g. 'authenticated user').", Required: false},
			{Name: "baseline", Description: "Control/negative result that distinguishes a real bug from intended behavior.", Required: false},
			{Name: "falsification", Description: "What observation would REFUTE this claim (e.g. 'the same request as another user returns 403 on every attempt').", Required: false},
			{Name: "next_observation", Description: "The single observation most likely to discriminate this hypothesis from an alternative.", Required: false},
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
			{Name: "info_item_id", Description: "Optional InfoItem/Evidence ID this observation was derived from (R3 trace link). Must resolve in this scan's ledger.", Required: false},
			{Name: "intent_id", Description: "Optional intent/unit id whose execution produced this observation (R3 trace link).", Required: false},
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

	reg.Register(&tools.Tool{
		Name: "read_info_item",
		Description: "Read one ledger evidence / InfoItem in this scan at an explicit layer. " +
			"index returns IDs and a bounded title/summary; detail returns the bounded body " +
			"(request/response/supports); artifact returns references only (info_item_id, finding_id), " +
			"never bytes. Use this instead of guessing from an earlier prompt: the index is what " +
			"planners see, detail is an explicit fetch. If the ID is not in this scan's ledger, pass " +
			"its scan_id (cross-scan reads require the run's project scope).",
		Parameters: []tools.Parameter{
			{Name: "id", Description: "Evidence ID (e.g. E-12), InfoItem ID, or the ID shown in an index line.", Required: true},
			{Name: "layer", Description: "index (default), detail, or artifact.", Required: false},
			{Name: "scan_id", Description: "Optional scan/run id for a cold-path lookup outside this scan; only same-project scans are accepted.", Required: false},
		},
		Execute: a.readInfoItemTool,
	})

	reg.Register(&tools.Tool{
		Name: "record_deviation",
		Description: "Record a concrete departure from the assigned intent: a target that behaved " +
			"differently, a chain step skipped, a scope/blocker discovered, or a material change in " +
			"the plan you actually executed. Audit-only — it does not change plan state. Use it when " +
			"the honest work differs from the intent so the planner and report can explain why.",
		Parameters: []tools.Parameter{
			{Name: "deviation", Description: "What changed and why, with concrete evidence/refs.", Required: true},
			{Name: "intent_id", Description: "The claimed intent id (PlanTask id), if known.", Required: false},
		},
		Execute: a.recordDeviationTool,
	})
}

func (a *Agent) recordDeviationTool(args map[string]string) (tools.Result, error) {
	if a.scanCtx == nil || a.scanCtx.Ledger == nil {
		return tools.Result{Error: "ledger unavailable in this context"}, nil
	}
	deviation := strings.TrimSpace(args["deviation"])
	if deviation == "" {
		return tools.Result{Error: "deviation is required"}, nil
	}
	if err := a.scanCtx.Ledger.AppendDeviation(args["intent_id"], deviation, a.ID); err != nil {
		return tools.Result{Error: err.Error()}, nil
	}
	return tools.Result{Output: "deviation recorded"}, nil
}

// SetCrossScanResolver installs the project-scope guard for read_info_item's
// scan_id parameter. Without it, cross-scan detail is refused; same-scan
// cold-path fallback still works.
func (a *Agent) SetCrossScanResolver(fn func(scanID string) bool) {
	a.crossScanMu.Lock()
	a.crossScanAllowed = fn
	a.crossScanMu.Unlock()
}

func (a *Agent) crossScanOK(scanID string) bool {
	a.crossScanMu.RLock()
	fn := a.crossScanAllowed
	a.crossScanMu.RUnlock()
	return fn != nil && fn(scanID)
}

// SetCrossScanScanIDsResolver installs the project scan whitelist the M4
// cross-scan history annotations may read. The project is the isolation
// fence — tasks inside one project share history, projects never do — so
// without a resolver the annotations are suppressed instead of falling back
// to an AllScans query. The resolver returns the project's scan IDs
// (including this run); an empty result disables the annotations.
func (a *Agent) SetCrossScanScanIDsResolver(fn func() []string) {
	a.crossScanMu.Lock()
	a.crossScanScanIDs = fn
	a.crossScanMu.Unlock()
}

func (a *Agent) crossScanScanIDsAllowed() []string {
	a.crossScanMu.RLock()
	fn := a.crossScanScanIDs
	a.crossScanMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// readInfoItemTool serves the L0/L1/L2 read views. It reads the shared ledger
// first; on a miss it falls back to the cold-path Reader for the same scan
// (or an explicitly authorized same-project scan). Cross-scan reads require a
// resolver, because the tool argument is model-controlled and the Reader
// itself does not know project membership.
func (a *Agent) readInfoItemTool(args map[string]string) (tools.Result, error) {
	id := strings.TrimSpace(args["id"])
	if id == "" {
		return tools.Result{Error: "id is required (evidence ID, InfoItem ID, or an index line ID)"}, nil
	}
	layer := strings.ToLower(strings.TrimSpace(args["layer"]))
	if layer == "" {
		layer = "index"
	}
	if layer != "index" && layer != "detail" && layer != "artifact" && layer != "trace" {
		return tools.Result{Error: "layer must be index, detail, artifact, or trace"}, nil
	}
	current := ""
	if a.scanCtx != nil {
		current = strings.TrimSpace(a.scanCtx.ID)
	}
	target := strings.TrimSpace(args["scan_id"])
	if target == "" {
		target = current
	}
	if target != "" && target != current {
		if !a.crossScanOK(target) {
			return tools.Result{Error: "cross-scan detail is not authorized for scan " + target}, nil
		}
	}

	if layer == "trace" {
		if a.scanCtx == nil || a.scanCtx.Ledger == nil {
			return tools.Result{Error: "trace requires an active ledger"}, nil
		}
		trace := a.scanCtx.Ledger.TraceInfoItem(id)
		out, err := json.Marshal(trace)
		if err != nil {
			return tools.Result{Error: "encode trace: " + err.Error()}, nil
		}
		count := len(trace.Reads) + len(trace.Evidence) + len(trace.Decisions)
		a.recordInfoRead("read_info_item", "trace", infoReadQueryJSON(id, "trace", target),
			[]string{id}, count, count > 0, false, false, "trace lookup")
		return tools.Result{Output: string(out)}, nil
	}

	if current != "" && target == current {
		if l := a.ledger(); l != nil {
			for _, h := range l.All() {
				for i := range h.Evidence {
					ev := &h.Evidence[i]
					if ev.ID != id && ev.InfoItemID != id {
						continue
					}
					out, err := json.Marshal(infoItemView(h.ID, ev, layer))
					if err != nil {
						return tools.Result{Error: "encode info item: " + err.Error()}, nil
					}
					truncated := layer == "detail" && (len([]rune(ev.Summary)) > 4096 ||
						len([]rune(ev.Request)) > 4096 || len([]rune(ev.Response)) > 4096)
					a.recordInfoRead("read_info_item", layer, infoReadQueryJSON(id, layer, target),
						[]string{id, ev.ID}, 1, true, truncated, false, "ledger read")
					return tools.Result{Output: string(out)}, nil
				}
			}
		}
	}

	reader := infoitem.DefaultReader()
	if reader == nil || target == "" {
		return tools.Result{Error: "info item not found in this scan: " + id}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	item, err := reader.GetByID(ctx, target, id)
	if errors.Is(err, infoitem.ErrNotFound) {
		a.recordInfoRead("read_info_item", layer, infoReadQueryJSON(id, layer, target),
			[]string{id}, 0, false, false, false, "cold-path not found")
		return tools.Result{Error: "info item not found: " + id + " (scan " + target + ")"}, nil
	}
	if err != nil {
		a.recordInfoRead("read_info_item", layer, infoReadQueryJSON(id, layer, target),
			[]string{id}, 0, false, false, true, "cold-path error: "+err.Error())
		return tools.Result{Error: "cold-path read failed: " + err.Error()}, nil
	}
	var payload any
	truncated := false
	switch layer {
	case "detail":
		detail := infoitem.DetailOf(item, 0)
		payload = detail
		truncated = detail.Truncated
	case "artifact":
		payload = infoitem.ArtifactOf(item)
	default:
		index := infoitem.Index(item, 0, 0)
		payload = index
		truncated = index.Truncated
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return tools.Result{Error: "encode info item: " + err.Error()}, nil
	}
	a.recordInfoRead("read_info_item", layer, infoReadQueryJSON(id, layer, target),
		[]string{id}, 1, true, truncated, false, "cold-path read")
	return tools.Result{Output: string(out)}, nil
}

// recordInfoRead appends one engine-side read trace. Failures are logged by the
// ledger but never turn a successful read into an error.
func (a *Agent) recordInfoRead(tool, mode, query string, refs []string, count int, found, truncated, incomplete bool, reason string) {
	if a == nil || a.scanCtx == nil || a.scanCtx.Ledger == nil {
		return
	}
	_ = a.scanCtx.Ledger.AppendInfoRead(scanctx.InfoRead{
		Tool: tool, Mode: mode, Query: query, Reason: reason, RefIDs: refs, ResultCount: count,
		Actor: a.ledgerOrigin(), Found: found, Truncated: truncated, Incomplete: incomplete,
	}, scanctx.AuditActorForAgent(a.ledgerOrigin()))
}

func infoReadQueryJSON(id, layer, scanID string) string {
	q := map[string]any{"id": id, "layer": layer}
	if strings.TrimSpace(scanID) != "" {
		q["scan_id"] = strings.TrimSpace(scanID)
	}
	b, _ := json.Marshal(q)
	return string(b)
}

func infoItemView(hypothesisID string, ev *scanctx.Evidence, layer string) map[string]any {
	title := infoItemTitle(ev.Summary)
	switch layer {
	case "detail":
		return map[string]any{
			"layer":         "detail",
			"id":            ev.ID,
			"hypothesis_id": hypothesisID,
			"kind":          ev.Kind,
			"nature":        string(scanctx.NormalizeEvidenceNature(ev.Nature)),
			"title":         title,
			"summary":       infoItemClamp(ev.Summary, 4096),
			"request":       infoItemClamp(ev.Request, 4096),
			"response":      infoItemClamp(ev.Response, 4096),
			"supports":      ev.Supports,
			"source":        ev.Source,
			"agent_id":      ev.AgentID,
			"info_item_id":  ev.InfoItemID,
			"finding_id":    ev.FindingID,
			"confidence":    ev.Confidence,
			"truncated":     len([]rune(ev.Request)) > 4096 || len([]rune(ev.Response)) > 4096 || len([]rune(ev.Summary)) > 4096,
		}
	case "artifact":
		return map[string]any{
			"layer":        "artifact",
			"id":           ev.ID,
			"info_item_id": ev.InfoItemID,
			"finding_id":   ev.FindingID,
			"has_request":  ev.Request != "",
			"has_response": ev.Response != "",
			"source":       ev.Source,
		}
	default:
		return map[string]any{
			"layer":         "index",
			"id":            ev.ID,
			"hypothesis_id": hypothesisID,
			"kind":          ev.Kind,
			"nature":        string(scanctx.NormalizeEvidenceNature(ev.Nature)),
			"title":         title,
			"summary":       infoItemClamp(infoItemFlat(ev.Summary), 240),
			"has_detail":    ev.Summary != "" || ev.Request != "" || ev.Response != "",
			"has_artifact":  ev.InfoItemID != "" || ev.FindingID != "",
			"confidence":    ev.Confidence,
		}
	}
}

func infoItemTitle(s string) string {
	line := strings.TrimSpace(s)
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	return infoItemClamp(line, 80)
}

func infoItemFlat(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func infoItemClamp(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max]) + "…"
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
	claim := strings.TrimSpace(args["title"])
	if claim == "" {
		return tools.Result{Error: "title is required: state the falsifiable CLAIM in one sentence (a vulnerability class is not a hypothesis)"}, nil
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
		Title:             claim,
		Target:            strings.TrimSpace(args["target"]),
		DataFlow:          strings.TrimSpace(args["data_flow"]),
		RequiredPrivilege: strings.TrimSpace(args["required_privilege"]),
		Baseline:          strings.TrimSpace(args["baseline"]),
		Falsification:     strings.TrimSpace(args["falsification"]),
		NextObservation:   strings.TrimSpace(args["next_observation"]),
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
	infoItemID := strings.TrimSpace(args["info_item_id"])
	if infoItemID != "" {
		if !evidenceLinkResolves(l, infoItemID) {
			return tools.Result{Error: fmt.Sprintf("unknown info_item_id %q — link to an Evidence/InfoItem ID visible in this scan's ledger", infoItemID)}, nil
		}
	}
	ev := scanctx.Evidence{
		Kind:       kind,
		Nature:     nature,
		Supports:   supports,
		InfoItemID: infoItemID,
		IntentID:   strings.TrimSpace(args["intent_id"]),
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

// evidenceLinkResolves reports whether an InfoItem/Evidence reference exists in
// this scan's ledger. Engine-side validation: the model cannot invent a trace
// anchor that no authoritative object backs.
func evidenceLinkResolves(l *scanctx.LedgerStore, ref string) bool {
	if l == nil || strings.TrimSpace(ref) == "" {
		return false
	}
	if _, ok := l.Get(ref); ok {
		return true
	}
	for _, h := range l.All() {
		for _, ev := range h.Evidence {
			if strings.EqualFold(ev.ID, ref) || strings.EqualFold(ev.InfoItemID, ref) {
				return true
			}
		}
	}
	return false
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
		current, _ := l.Get(id)
		if res := validateHypothesisTransition(&current, status, strings.TrimSpace(args["next_action"])); res != nil {
			return *res, nil
		}
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

// validateHypothesisTransition enforces the R2 quality contract at the model
// tool boundary. Store-level transitions stay permissive for engine-owned
// paths (verifier records, reclaim), but a model may not declare a hypothesis
// proven without a reported finding, or rejected without any refuting
// baseline/control observation.
func validateHypothesisTransition(h *scanctx.Hypothesis, status scanctx.HypothesisStatus, nextAction string) *tools.Result {
	if h == nil {
		r := tools.Result{Error: "unknown hypothesis"}
		return &r
	}
	switch status {
	case scanctx.HypothesisProven:
		for _, ev := range h.Evidence {
			if ev.Kind == scanctx.EvidenceFindingRef && strings.TrimSpace(ev.FindingID) != "" {
				return nil
			}
		}
		r := tools.Result{Error: "proven requires an authoritative finding: call report_vulnerability first, then add_hypothesis_evidence(kind=finding_ref, finding_id=<XALG-…>) and retry. Hypothesis confidence is not proof."}
		return &r
	case scanctx.HypothesisRejected:
		if strings.TrimSpace(h.Baseline) != "" {
			return nil
		}
		for _, ev := range h.Evidence {
			// An engine-assigned stable ID is not a substantiation; only an
			// explicit cross-object link, supports, or a real observation is.
			explicitLink := ev.InfoItemID != "" && !strings.HasPrefix(ev.InfoItemID, "evidence:")
			if ev.Nature == scanctx.EvidenceObserved || len(ev.Supports) > 0 || explicitLink {
				return nil
			}
			switch strings.ToLower(ev.Kind) {
			case "baseline", "control", "contradiction", "exploit":
				return nil
			}
		}
		r := tools.Result{Error: "rejected requires a refuting observation: record a baseline/control, an observed probe, or link the InfoItem it came from (add_hypothesis_evidence). An unsubstantiated inferred note is not a refutation."}
		return &r
	case scanctx.HypothesisBlocked:
		if nextAction == "" && strings.TrimSpace(h.NextAction) == "" {
			r := tools.Result{Error: "blocked requires a reason: pass next_action describing the missing precondition (credentials, feature flag, access, …)."}
			return &r
		}
	}
	return nil
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
		// M4 region consumer (output-only): per-entry same-endpoint history
		// across THIS project's scans. nil cache (no cold-path reader or no
		// project scope) keeps the output unchanged.
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
	// M4 association consumer (output-only): same-subject history across THIS
	// project's scans appended at the end. Reader/project-scope absence or any
	// query error keeps the output unchanged; claim semantics above are
	// untouched.
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
