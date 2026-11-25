// trace.go — R3/P1 Information-to-Decision Trace.
//
// Three audit-only projections live here:
//
//   - InfoRead: an engine-side record of a model/planner information read
//     (what was read, why, whether the result was truncated/incomplete).
//   - ActionAttempt: one target-facing action fingerprint, so a repeated action
//     can be traced to the earlier attempt and its evidence instead of being
//     repeated blindly.
//   - Decision: the candidate set, hard constraints, selection and information
//     use of one material planning/control decision.
//
// All are journal-backed audit records with bounded in-memory projections.
// They are never consulted by state transitions, claim ordering, verification
// or reporting: reading information cannot change a verdict by itself.

package scanctx

import (
	"fmt"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
)

// InfoRead is one engine-recorded information read.
type InfoRead struct {
	Tool   string `json:"tool"`
	Mode   string `json:"mode"`
	Reason string `json:"reason,omitempty"`
	// Query is the canonical, bounded JSON description of the read request
	// (mode/filters/ids/limits). It is a description, never a result body.
	Query  string   `json:"query,omitempty"`
	RefIDs []string `json:"ref_ids,omitempty"`
	Actor  string   `json:"actor,omitempty"`
	// ResultCount is the number of rows the read returned. Zero with
	// Incomplete/Truncated set is NOT evidence of absence.
	ResultCount int       `json:"result_count"`
	Found       bool      `json:"found"`
	Truncated   bool      `json:"truncated"`
	Incomplete  bool      `json:"incomplete"`
	At          time.Time `json:"at"`
}

// Decision is one structured planning/control decision snapshot. The type is
// owned by the journal (the durable audit record) and re-exported here so
// scanctx callers share one vocabulary.
type Decision = domainjournal.Decision

// DecisionCandidate is one option considered by a decision.
type DecisionCandidate = domainjournal.DecisionCandidate

// ActionAttempt is one target-facing action fingerprint.
type ActionAttempt struct {
	ID           string
	Fingerprint  string
	Tool         string
	Summary      string
	EvidenceRefs []string
	Count        int
	At           time.Time
}

// InfoTrace is the read-side join for one InfoItem/Evidence ID: who read it,
// which evidence was derived from it, which hypotheses hold that evidence,
// which intents those observations name, and which decisions cited any of
// them as their basis.
type InfoTrace struct {
	ID         string     `json:"id"`
	Reads      []InfoRead `json:"reads,omitempty"`
	Evidence   []Evidence `json:"evidence,omitempty"`
	Hypotheses []string   `json:"hypotheses,omitempty"`
	IntentIDs  []string   `json:"intent_ids,omitempty"`
	Decisions  []Decision `json:"decisions,omitempty"`
}

const (
	maxInfoReadProjection      = 500
	maxActionAttemptProjection = 500
	maxDecisionProjection      = 500
)

// AppendInfoRead records one information read. It is called by the tool
// execution layer after a successful (or not-found) read, never by the model.
func (ls *LedgerStore) AppendInfoRead(read InfoRead, actor domainjournal.Actor) error {
	read.Tool = truncate(strings.TrimSpace(read.Tool), maxHypothesisFieldLen)
	read.Mode = truncate(strings.TrimSpace(read.Mode), maxHypothesisFieldLen)
	read.Reason = truncate(strings.TrimSpace(read.Reason), maxSummaryLen)
	read.Query = truncate(strings.TrimSpace(read.Query), 4096)
	read.Actor = truncate(strings.TrimSpace(read.Actor), maxHypothesisFieldLen)
	if read.ResultCount < 0 {
		read.ResultCount = 0
	}
	read.At = time.Now().UTC()

	ls.mu.RLock()
	j := ls.journal
	scanID := ls.scanID
	ls.mu.RUnlock()
	if j == nil {
		return fmt.Errorf("scanctx: no journal for info read")
	}
	op := domainjournal.OpReadInfoItem
	if read.Tool == "search_info" {
		op = domainjournal.OpSearchInfo
	}
	summary := fmt.Sprintf("%s mode=%s found=%t truncated=%t incomplete=%t count=%d refs=%s query=%s reason=%s",
		read.Tool, read.Mode, read.Found, read.Truncated, read.Incomplete, read.ResultCount,
		strings.Join(read.RefIDs, ","), read.Query, read.Reason)
	if _, err := j.Append(domainjournal.Record{
		ScanID:    scanID,
		Actor:     actor,
		Operation: op,
		Summary:   truncate(summary, 2048),
	}); err != nil {
		return fmt.Errorf("scanctx: append info read: %w", err)
	}
	ls.mu.Lock()
	ls.infoReads = append(ls.infoReads, read)
	if len(ls.infoReads) > maxInfoReadProjection {
		ls.infoReads = append([]InfoRead(nil), ls.infoReads[len(ls.infoReads)-maxInfoReadProjection:]...)
	}
	ls.mu.Unlock()
	return nil
}

// InfoReads returns a copy of the bounded read projection.
func (ls *LedgerStore) InfoReads() []InfoRead {
	if ls == nil {
		return nil
	}
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return append([]InfoRead(nil), ls.infoReads...)
}

// AppendDecision records one structured decision snapshot. The journal append
// is the durable audit write; the in-memory slice is a bounded projection for
// TraceInfoItem. A failed journal append returns an error and does not add the
// projection, so an unaudited decision is never presented as traceable.
func (ls *LedgerStore) AppendDecision(d Decision, actor domainjournal.Actor) (Decision, error) {
	if ls == nil {
		return d, fmt.Errorf("scanctx: nil ledger")
	}
	d.Action = truncate(strings.TrimSpace(d.Action), maxHypothesisFieldLen)
	if d.Action == "" {
		return d, fmt.Errorf("scanctx: decision action is required")
	}
	d.Actor = truncate(strings.TrimSpace(d.Actor), maxHypothesisFieldLen)
	d.IntentID = truncate(strings.TrimSpace(d.IntentID), maxHypothesisFieldLen)
	d.HypothesisID = truncate(strings.TrimSpace(d.HypothesisID), maxHypothesisFieldLen)
	d.Selected = truncate(strings.TrimSpace(d.Selected), maxHypothesisFieldLen)
	d.Reason = truncate(strings.TrimSpace(d.Reason), maxSummaryLen)
	d.InfoUse = truncate(strings.TrimSpace(d.InfoUse), 64)
	d.NoInfoReason = truncate(strings.TrimSpace(d.NoInfoReason), 128)
	d.SnapshotDigest = truncate(strings.TrimSpace(d.SnapshotDigest), 128)
	d.BasisRefs = uniqueTrimmed(d.BasisRefs, 32)
	d.HardConstraints = uniqueTrimmed(d.HardConstraints, 32)
	d.ShadowOrder = uniqueTrimmed(d.ShadowOrder, 64)
	if len(d.Candidates) > 64 {
		d.Candidates = d.Candidates[:64]
	}
	d.ShadowReason = truncate(strings.TrimSpace(d.ShadowReason), maxSummaryLen)
	d.At = time.Now().UTC()

	ls.mu.Lock()
	ls.decisionSeq++
	d.ID = fmt.Sprintf("D-%d", ls.decisionSeq)
	j := ls.journal
	scanID := ls.scanID
	ls.mu.Unlock()

	if j == nil {
		return d, fmt.Errorf("scanctx: no journal for decision")
	}
	cp := d
	cp.BasisRefs = append([]string(nil), d.BasisRefs...)
	cp.HardConstraints = append([]string(nil), d.HardConstraints...)
	cp.Candidates = append([]DecisionCandidate(nil), d.Candidates...)
	cp.ShadowOrder = append([]string(nil), d.ShadowOrder...)
	summary := fmt.Sprintf("decision %s %s selected=%s basis=%s info_use=%s no_info=%s candidates=%d reason=%s",
		d.ID, d.Action, d.Selected, strings.Join(d.BasisRefs, ","), d.InfoUse, d.NoInfoReason,
		len(d.Candidates), d.Reason)
	if _, err := j.Append(domainjournal.Record{
		ScanID:       scanID,
		Actor:        actor,
		Operation:    domainjournal.OpDecision,
		IntentID:     d.IntentID,
		HypothesisID: d.HypothesisID,
		Summary:      truncate(summary, 2048),
		Decision:     &cp,
	}); err != nil {
		return d, fmt.Errorf("scanctx: append decision: %w", err)
	}

	ls.mu.Lock()
	ls.decisions = append(ls.decisions, cp)
	if len(ls.decisions) > maxDecisionProjection {
		ls.decisions = append([]Decision(nil), ls.decisions[len(ls.decisions)-maxDecisionProjection:]...)
	}
	ls.mu.Unlock()
	return cp, nil
}

// Decisions returns a copy of the bounded decision projection.
func (ls *LedgerStore) Decisions() []Decision {
	if ls == nil {
		return nil
	}
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	out := make([]Decision, len(ls.decisions))
	for i := range ls.decisions {
		out[i] = ls.decisions[i]
		out[i].BasisRefs = append([]string(nil), ls.decisions[i].BasisRefs...)
		out[i].HardConstraints = append([]string(nil), ls.decisions[i].HardConstraints...)
		out[i].Candidates = append([]DecisionCandidate(nil), ls.decisions[i].Candidates...)
		out[i].ShadowOrder = append([]string(nil), ls.decisions[i].ShadowOrder...)
	}
	return out
}

func uniqueTrimmed(values []string, max int) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		v := strings.TrimSpace(raw)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
		if len(out) >= max {
			break
		}
	}
	return out
}

// TraceInfoItem joins reads, evidence, hypotheses, intents and decisions by
// one ID. Evidence matches when its ID or InfoItemID is the requested ID;
// hypotheses are those holding that evidence; intents are Evidence.IntentID
// values; decisions are those whose basis/candidate/selection references the
// ID directly or through one of the evidence/hypothesis IDs found above.
func (ls *LedgerStore) TraceInfoItem(id string) InfoTrace {
	trace := InfoTrace{ID: strings.TrimSpace(id)}
	if ls == nil || trace.ID == "" {
		return trace
	}
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	for _, r := range ls.infoReads {
		for _, ref := range r.RefIDs {
			if strings.EqualFold(strings.TrimSpace(ref), trace.ID) {
				trace.Reads = append(trace.Reads, r)
				break
			}
		}
	}
	seenHyp := map[string]bool{}
	seenIntent := map[string]bool{}
	linked := map[string]bool{strings.ToLower(trace.ID): true}
	for _, h := range ls.hyps {
		for _, ev := range h.Evidence {
			match := strings.EqualFold(ev.ID, trace.ID) ||
				strings.EqualFold(ev.InfoItemID, trace.ID) ||
				strings.EqualFold(h.ID, trace.ID)
			if !match {
				continue
			}
			trace.Evidence = append(trace.Evidence, ev)
			linked[strings.ToLower(ev.ID)] = true
			if !seenHyp[h.ID] {
				seenHyp[h.ID] = true
				trace.Hypotheses = append(trace.Hypotheses, h.ID)
				linked[strings.ToLower(h.ID)] = true
			}
			if ev.IntentID != "" && !seenIntent[ev.IntentID] {
				seenIntent[ev.IntentID] = true
				trace.IntentIDs = append(trace.IntentIDs, ev.IntentID)
				linked[strings.ToLower(ev.IntentID)] = true
			}
		}
	}
	for _, d := range ls.decisions {
		if decisionReferences(d, linked) {
			trace.Decisions = append(trace.Decisions, d)
		}
	}
	return trace
}

// decisionReferences reports whether any structured ID in d is in linked.
// Only explicit IDs count; reason prose is never parsed for identity.
func decisionReferences(d Decision, linked map[string]bool) bool {
	check := func(raw string) bool {
		return raw != "" && linked[strings.ToLower(strings.TrimSpace(raw))]
	}
	if check(d.Selected) || check(d.IntentID) || check(d.HypothesisID) {
		return true
	}
	for _, ref := range d.BasisRefs {
		if check(ref) {
			return true
		}
	}
	for _, c := range d.Candidates {
		if check(c.ID) {
			return true
		}
	}
	return false
}

// RecordActionAttempt records one action fingerprint and returns the prior
// attempt when the same fingerprint was seen before. The prior attempt is the
// trace anchor a repeat-suppression nudge cites.
func (ls *LedgerStore) RecordActionAttempt(fingerprint, tool, summary string, evidenceRefs []string, actor domainjournal.Actor) (ActionAttempt, *ActionAttempt, error) {
	fingerprint = truncate(strings.TrimSpace(fingerprint), maxSummaryLen)
	if fingerprint == "" {
		return ActionAttempt{}, nil, fmt.Errorf("scanctx: empty action fingerprint")
	}
	ls.mu.Lock()
	var prior *ActionAttempt
	count := 1
	for i := range ls.actionAttempts {
		if ls.actionAttempts[i].Fingerprint != fingerprint {
			continue
		}
		cp := ls.actionAttempts[i]
		prior = &cp // latest matching attempt is the trace anchor
		if cp.Count >= count {
			count = cp.Count + 1
		}
	}
	ls.actionSeq++
	att := ActionAttempt{
		ID:           fmt.Sprintf("A-%d", ls.actionSeq),
		Fingerprint:  fingerprint,
		Tool:         truncate(strings.TrimSpace(tool), maxHypothesisFieldLen),
		Summary:      truncate(strings.TrimSpace(summary), maxSummaryLen),
		EvidenceRefs: append([]string(nil), evidenceRefs...),
		Count:        count,
		At:           time.Now().UTC(),
	}
	ls.actionAttempts = append(ls.actionAttempts, att)
	if len(ls.actionAttempts) > maxActionAttemptProjection {
		ls.actionAttempts = append([]ActionAttempt(nil), ls.actionAttempts[len(ls.actionAttempts)-maxActionAttemptProjection:]...)
	}
	ls.mu.Unlock()

	if j := ls.journalForAppend(); j != nil {
		priorID := ""
		if prior != nil {
			priorID = prior.ID
		}
		_, err := j.Append(domainjournal.Record{
			ScanID:    ls.scanIDForAppend(),
			Actor:     actor,
			Operation: domainjournal.OpActionAttempt,
			Summary:   truncate(fmt.Sprintf("%s %s prior=%s %s", att.ID, fingerprint, priorID, att.Summary), 2048),
		})
		if err != nil {
			return ActionAttempt{}, prior, fmt.Errorf("scanctx: append action attempt: %w", err)
		}
	}
	return att, prior, nil
}

// ActionAttempts returns a copy of the bounded attempt projection.
func (ls *LedgerStore) ActionAttempts() []ActionAttempt {
	if ls == nil {
		return nil
	}
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return append([]ActionAttempt(nil), ls.actionAttempts...)
}

// AppendEngagementEvent writes one egress-policy audit record. It is called by
// the budget, never by the model, and never mutates scan state.
func (ls *LedgerStore) AppendEngagementEvent(operation, summary string) error {
	if ls == nil {
		return nil
	}
	op := domainjournal.OpEgressPause
	if strings.Contains(operation, "stop") {
		op = domainjournal.OpEgressStop
	}
	ls.mu.RLock()
	j := ls.journal
	scanID := ls.scanID
	ls.mu.RUnlock()
	if j == nil {
		return nil
	}
	_, err := j.Append(domainjournal.Record{
		ScanID: scanID, Actor: engineActor("egress"), Operation: op,
		Summary: truncate(strings.TrimSpace(summary), 1024),
	})
	return err
}

func (ls *LedgerStore) journalForAppend() *domainjournal.Journal {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return ls.journal
}

func (ls *LedgerStore) scanIDForAppend() string {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return ls.scanID
}
