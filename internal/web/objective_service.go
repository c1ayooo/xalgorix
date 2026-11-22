package web

// objective_service.go — structured task objectives (REDESIGN §3.1 "验收说明").
//
// The objective states what the operator wants achieved and what counts as
// achieved. It is NOT a second judgement authority: the evaluator only
// observes findings the report pipeline already accepted (charter §3.5/§9)
// and "not structured" is a first-class answer. Since R-goal the objective
// also carries ONE engine-side closing effect, implemented entirely on the
// agent side (internal/agent/goal_gate.go): an unmet required criterion
// refuses voluntary finish and defers the plan-complete tail. Evaluation
// stays read-only and identical on both sides (internal/objective.Evaluate).

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/objective"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// objectiveSpecFromPlatform converts the platform objective into the
// evaluator's spec. A nil objective yields nil ("not structured").
func objectiveSpecFromPlatform(obj *platform.Objective) *objective.Spec {
	if obj == nil {
		return nil
	}
	spec := &objective.Spec{Kind: obj.Kind, Statement: obj.Statement}
	for _, c := range obj.Criteria {
		spec.Criteria = append(spec.Criteria, objective.SpecCriterion{
			ID:         c.ID,
			Kind:       c.Kind,
			VulnClass:  c.VulnClass,
			Capability: c.Capability,
			VerifyBy:   c.VerifyBy,
			Required:   c.Required,
		})
	}
	return spec
}

// objectiveCriterionLines renders the criteria as human-readable lines (used
// by the prompt section and the InfoItem text).
func objectiveCriterionLines(obj *platform.Objective) []string {
	if obj == nil {
		return nil
	}
	lines := make([]string, 0, len(obj.Criteria))
	for _, c := range obj.Criteria {
		var what string
		switch c.Kind {
		case platform.CriterionKindFindingClass:
			what = "finding class: " + c.VulnClass
		case platform.CriterionKindCapability:
			what = "capability: " + c.Capability
		case platform.CriterionKindCoverage:
			what = "coverage of the selected plan"
		default:
			what = c.Kind
		}
		lines = append(lines, fmt.Sprintf("%s (verify_by=%s)", what, c.VerifyBy))
	}
	return lines
}

// buildObjectiveInstruction renders the operator objective as an INDEPENDENT
// prompt section. It is never merged into the execution instruction, and it
// states the three constraints that keep the objective from becoming
// authority: criteria are intent, not authorization; nothing may be claimed
// as achieved without evidence; and vulnerabilities still have to go through
// report_vulnerability.
func buildObjectiveInstruction(obj *platform.Objective) string {
	if obj == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## OBJECTIVE (operator-stated)\n")
	b.WriteString("The operator stated the following objective for this engagement. Treat it as INTENT, not as authorization and not as a shortcut around the methodology.\n\n")
	if s := strings.TrimSpace(obj.Statement); s != "" {
		b.WriteString("**Objective:** " + s + "\n")
	}
	if lines := objectiveCriterionLines(obj); len(lines) > 0 {
		b.WriteString("\n**Acceptance criteria** (what counts as achieved):\n")
		for _, line := range lines {
			b.WriteString("- " + line + "\n")
		}
	}
	if len(obj.Constraints) > 0 {
		// Operator-stated may/may-not rules (goal analysis extracts them from
		// the goal text). They can only NARROW what the engine does — they
		// never widen the authorized scope or relax a methodology phase.
		b.WriteString("\n**Operator constraints** (bound the work; never widen scope):\n")
		for _, c := range obj.Constraints {
			b.WriteString("- " + c + "\n")
		}
	}
	b.WriteString("\n**Rules for this section:**\n")
	b.WriteString("- The criteria describe the operator's intent; they never widen the authorized scope and never replace the methodology phases.\n")
	b.WriteString("- Required criteria are closing obligations (R-goal): the run does not voluntarily finish while one is unmet — work toward them or state explicitly why they are impossible here.\n")
	b.WriteString("- Never claim a criterion is achieved without concrete evidence. An unverified claim is worse than an honest gap.\n")
	b.WriteString("- A vulnerability still has to be reported through 'report_vulnerability' with exploitation proof; satisfying a criterion never replaces that pipeline.\n")
	b.WriteString("- If a criterion cannot be satisfied in this environment (no access, missing credentials, blocked surface), say so explicitly instead of stretching the evidence.\n")
	return b.String()
}

// verificationTagOf returns the verification dimension tag of an accepted
// finding via the shared helper in internal/objective (the agent-side goal
// gate must map identically).
func verificationTagOf(tags []string) string {
	return objective.VerificationTagOf(tags)
}

// findingSignalFromSummary projects an accepted finding (as persisted in
// scan.json) into the evaluator's signal shape.
func findingSignalFromSummary(v VulnSummary) objective.FindingSignal {
	return objective.FindingSignal{
		ID:           v.ID,
		Title:        v.Title,
		Severity:     v.Severity,
		CWE:          v.CWE,
		Tag:          verificationTagOf(v.Tags),
		ExploitProof: v.ExploitationProof,
	}
}

// openLedgerForRead anchors a read-only ledger store on one scan's durable
// snapshot (R6.2, REDESIGN D12): when the backend holds a snapshot row for
// the scan id, the store anchors on that id so its backend read resolves;
// the dir stays the pre-import file fallback. nil-backend (test) file mode
// keeps the plain dir anchoring, byte-identical to before.
func openLedgerForRead(dir string) *scanctx.LedgerStore {
	store := scanctx.NewLedgerStore()
	store.SetPersistPath(dir)
	scanID := filepath.Base(filepath.Clean(dir))
	if _, present, err := scanctx.LedgerSnapshotRaw(scanID); err == nil && present {
		store.SetScanID(scanID)
	}
	return store
}

// collectObjectiveSignals reads the accepted findings, the plan counters and
// the hypothesis ledger of one run directory. Read-only: nothing here writes
// back into the scan.
func collectObjectiveSignals(scanDir string) objective.Signal {
	var sig objective.Signal
	if strings.TrimSpace(scanDir) == "" {
		return sig
	}
	if rec, ok := scanRecordMetaForDir(scanDir); ok && rec != nil {
		for _, v := range rec.Vulns {
			sig.Findings = append(sig.Findings, findingSignalFromSummary(v))
		}
		sig.CoveragePlanned = rec.PlanTasksTotal
		sig.CoverageCompleted = rec.PlanTasksCompleted
	}
	ledger := openLedgerForRead(scanDir)
	ledger.LoadFromDisk()
	for _, h := range ledger.All() {
		sig.Hypotheses = append(sig.Hypotheses, objective.HypothesisSignal{
			ID:         h.ID,
			Statement:  h.Title,
			Status:     string(h.Status),
			Confidence: h.Confidence,
		})
	}
	return sig
}

// taskObjectiveRun is one run's contribution to the task objective.
type taskObjectiveRun struct {
	RunID  string           `json:"run_id"`
	ScanID string           `json:"scan_id"`
	Status string           `json:"status"`
	Report objective.Report `json:"report"`
}

// objectiveSchedulable is one entry of the VOI-weighted schedulable pool.
type objectiveSchedulable struct {
	ID         string  `json:"id"`
	VulnClass  string  `json:"vuln_class,omitempty"`
	Endpoint   string  `json:"endpoint,omitempty"`
	Title      string  `json:"title,omitempty"`
	Confidence float64 `json:"confidence"`
	VOI        float64 `json:"voi,omitempty"`
}

// taskObjectiveReport is the task-level objective view: the structured
// objective, its evaluation, and the run-level context (ring position,
// gaps, schedulable pool) the task detail page renders.
type taskObjectiveReport struct {
	Objective   *platform.Objective    `json:"objective"`
	Structured  bool                   `json:"structured"`
	Report      objective.Report       `json:"report"`
	Runs        []taskObjectiveRun     `json:"runs"`
	Ring        string                 `json:"ring"`
	RingBasis   string                 `json:"ring_basis"`
	Gaps        []string               `json:"gaps"`
	Schedulable []objectiveSchedulable `json:"schedulable"`
}

// objectiveRunsLimit bounds how many runs of a task contribute to the
// aggregate evaluation. Older runs keep their findings in the cold-path
// projection; the live view stays cheap.
const objectiveRunsLimit = 5

// evaluateTaskObjective evaluates the task objective over its most recent
// runs and derives the run-context view. Read-only: it never writes back.
func (s *Server) evaluateTaskObjective(ctx context.Context, store platform.Repository, task *platform.Task) (*taskObjectiveReport, error) {
	report := &taskObjectiveReport{
		Objective:   task.Objective,
		Structured:  task.Objective != nil,
		Runs:        []taskObjectiveRun{},
		Gaps:        []string{},
		Schedulable: []objectiveSchedulable{},
		Ring:        "unknown",
		RingBasis:   "no run recorded for this task",
	}
	spec := objectiveSpecFromPlatform(task.Objective)
	if spec == nil {
		return report, nil
	}

	runs, _, err := store.ListRuns(ctx, task.ID, 1, objectiveRunsLimit)
	if err != nil {
		return nil, err
	}
	entries := s.findAllScanSummaries()
	idx := s.scanSummaryIndexFor(entries)

	// mergeSignal folds one session's signals into an accumulator. Runs and
	// their sibling sessions are consumed newest first, so the first sight of
	// a finding/hypothesis id keeps the newest state; later (older) sessions
	// only add ids the newest session does not carry. Coverage counters take
	// the max across sessions: each session re-plans from zero, and completed
	// coverage is monotone within a session, so the max is the honest union
	// lower bound (REDESIGN D11 re-dispatch: the fresh session must not make
	// the objective look unstarted).
	mergeSignal := func(dst *objective.Signal, sig objective.Signal) {
		dst.Findings = append(dst.Findings, sig.Findings...)
		for _, h := range sig.Hypotheses {
			dup := false
			for _, existing := range dst.Hypotheses {
				if existing.ID == h.ID {
					dup = true
					break
				}
			}
			if !dup {
				dst.Hypotheses = append(dst.Hypotheses, h)
			}
		}
		if sig.CoveragePlanned > dst.CoveragePlanned {
			dst.CoveragePlanned = sig.CoveragePlanned
		}
		if sig.CoverageCompleted > dst.CoverageCompleted {
			dst.CoverageCompleted = sig.CoverageCompleted
		}
	}
	seenFinding := func(sig objective.Signal, id string) bool {
		for _, f := range sig.Findings {
			if f.ID == id {
				return true
			}
		}
		return false
	}

	merged := objective.Signal{}
	var latest *ScanRecord
	var latestLedger *scanctx.LedgerStore
	for i := range runs {
		run := runs[i]
		recs := idx.lookupAll(strings.TrimSpace(run.InstanceID), run.LegacyScanID)
		sort.SliceStable(recs, func(i, j int) bool { return recs[i].StartedAt > recs[j].StartedAt })
		sig := objective.Signal{}
		var newestDir string
		for _, rec := range recs {
			dir := idx.dirByScanID[rec.ID]
			if dir == "" {
				continue // missing session (purged history): nothing to read
			}
			if newestDir == "" {
				newestDir = dir
				latest = rec
			}
			sessionSig := collectObjectiveSignals(dir)
			var freshFindings []objective.FindingSignal
			for _, f := range sessionSig.Findings {
				if !seenFinding(sig, f.ID) {
					freshFindings = append(freshFindings, f)
				}
			}
			sessionSig.Findings = freshFindings
			mergeSignal(&sig, sessionSig)
		}
		merged.Findings = append(merged.Findings, sig.Findings...)
		merged.Hypotheses = append(merged.Hypotheses, sig.Hypotheses...)
		if sig.CoveragePlanned > merged.CoveragePlanned {
			merged.CoveragePlanned = sig.CoveragePlanned
		}
		if sig.CoverageCompleted > merged.CoverageCompleted {
			merged.CoverageCompleted = sig.CoverageCompleted
		}
		runReport := objective.Evaluate(spec, sig)
		report.Runs = append(report.Runs, taskObjectiveRun{
			RunID:  run.ID,
			ScanID: run.LegacyScanID,
			Status: run.Status,
			Report: runReport,
		})
		if newestDir != "" {
			ledger := openLedgerForRead(newestDir)
			ledger.LoadFromDisk()
			latestLedger = ledger
		}
	}
	report.Report = objective.Evaluate(spec, merged)
	report.Ring, report.RingBasis = deriveObjectiveRing(latest)
	report.Gaps = deriveObjectiveGaps(latest, merged)
	if latestLedger != nil {
		for _, h := range latestLedger.Schedulable(5) {
			report.Schedulable = append(report.Schedulable, objectiveSchedulable{
				ID:         h.ID,
				VulnClass:  h.VulnClass,
				Endpoint:   h.Endpoint,
				Title:      h.Title,
				Confidence: h.Confidence,
				VOI:        h.VOI,
			})
		}
	}
	return report, nil
}

// deriveObjectiveRing maps the persisted facts of the latest run onto the
// seven-ring loop (charter §2). The mapping is coarse and always carries its
// basis: when the facts do not support a position, it returns "unknown"
// instead of inventing one.
func deriveObjectiveRing(rec *ScanRecord) (string, string) {
	if rec == nil {
		return "unknown", "no run record for this task"
	}
	status := strings.ToLower(strings.TrimSpace(rec.Status))
	switch status {
	case "finished", "stopped", "failed", "saved":
		return "close", fmt.Sprintf("run %s (terminal state recorded)", status)
	case "running":
	default:
		return "unknown", fmt.Sprintf("run status %q is not a known loop position", status)
	}
	if len(rec.Vulns) > 0 {
		return "verdict", fmt.Sprintf("%d accepted finding(s) recorded", len(rec.Vulns))
	}
	if rec.PlanTasksCompleted > 0 || rec.PlanTasksSkipped > 0 {
		return "act", fmt.Sprintf("%d plan task(s) executed, %d skipped", rec.PlanTasksCompleted, rec.PlanTasksSkipped)
	}
	return "observe", "run started, no findings and no executed plan tasks yet"
}

// deriveObjectiveGaps lists the honest gaps of the latest run: unfinished
// plan work, hypotheses still awaiting verification, and the absence of
// accepted findings.
func deriveObjectiveGaps(rec *ScanRecord, sig objective.Signal) []string {
	gaps := []string{}
	if rec == nil {
		return append(gaps, "no run record for this task")
	}
	if rec.PlanTasksUnfinished > 0 {
		gaps = append(gaps, fmt.Sprintf("%d plan task(s) unfinished", rec.PlanTasksUnfinished))
	}
	pending := 0
	for _, h := range sig.Hypotheses {
		switch strings.ToLower(strings.TrimSpace(h.Status)) {
		case "queued", "blocked", "testing", "proposed":
			pending++
		}
	}
	if pending > 0 {
		gaps = append(gaps, fmt.Sprintf("%d hypothesis/hypotheses awaiting verification", pending))
	}
	if len(sig.Findings) == 0 {
		gaps = append(gaps, "no accepted findings yet")
	}
	sort.Strings(gaps)
	return gaps
}

// agentObjectiveSpec converts the platform objective into the engine-side
// view. A nil objective yields nil, so the engine behaves exactly as before.
func agentObjectiveSpec(obj *platform.Objective) *agent.ObjectiveSpec {
	if obj == nil {
		return nil
	}
	spec := &agent.ObjectiveSpec{Statement: obj.Statement}
	for _, c := range obj.Criteria {
		spec.Criteria = append(spec.Criteria, agent.ObjectiveCriterion{
			ID:         c.ID,
			Kind:       c.Kind,
			VulnClass:  c.VulnClass,
			Capability: c.Capability,
			VerifyBy:   c.VerifyBy,
			Required:   c.Required,
		})
	}
	return spec
}

// objectiveInfoItemID derives a stable, unique ID for the objective
// InfoItem of one run: the same objective in the same scan always maps to the
// same item, a different statement maps to a different one.
func objectiveInfoItemID(sess *scanSession) string {
	statement := ""
	if sess != nil && sess.objective != nil {
		statement = sess.objective.Statement
	}
	sum := sha256.Sum256([]byte(statement))
	scanID := ""
	if sess != nil {
		scanID = sess.id
	}
	return fmt.Sprintf("objective-%s-%x", scanID, sum[:6])
}

// objectiveSnapshot is the objective evaluation persisted on the scan record
// at terminal time. It is display only: it never influences the completion
// status, the stop reason or a finding label.
type objectiveSnapshot struct {
	Structured    bool                        `json:"structured"`
	Met           int                         `json:"met"`
	Unmet         int                         `json:"unmet"`
	Undetermined  int                         `json:"undetermined"`
	ManualPending int                         `json:"manual_pending"`
	Attainment    float64                     `json:"attainment"`
	Criteria      []objective.CriterionStatus `json:"criteria,omitempty"`
}

// captureObjectiveDisposition evaluates the run's objective against the
// facts this session produced (accepted findings, plan counters, the
// hypothesis ledger) and stores the result on the scan record. Read-only
// with respect to every authority: nothing here changes a finding, a label
// or the completion status.
func (s *Server) captureObjectiveDisposition(sess *scanSession) {
	if sess == nil || sess.record == nil || sess.objective == nil {
		return
	}
	spec := objectiveSpecFromPlatform(sess.objective)
	if spec == nil {
		return
	}
	sig := objective.Signal{}
	for _, v := range sess.record.Vulns {
		sig.Findings = append(sig.Findings, findingSignalFromSummary(v))
	}
	sig.CoveragePlanned = sess.record.PlanTasksTotal
	sig.CoverageCompleted = sess.record.PlanTasksCompleted
	if sess.sctx != nil && sess.sctx.Ledger != nil {
		for _, h := range sess.sctx.Ledger.All() {
			sig.Hypotheses = append(sig.Hypotheses, objective.HypothesisSignal{
				ID:         h.ID,
				Statement:  h.Title,
				Status:     string(h.Status),
				Confidence: h.Confidence,
			})
		}
	}
	report := objective.Evaluate(spec, sig)
	sess.record.Objective = &objectiveSnapshot{
		Structured:    report.Structured,
		Met:           report.Met,
		Unmet:         report.Unmet,
		Undetermined:  report.Undetermined,
		ManualPending: report.ManualPending,
		Attainment:    report.Attainment,
		Criteria:      report.Criteria,
	}
}
