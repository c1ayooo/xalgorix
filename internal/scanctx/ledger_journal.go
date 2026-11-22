package scanctx

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/domainjournal"
	"github.com/xalgord/xalgorix/v4/internal/infoitem"
)

func engineActor(source string) domainjournal.Actor {
	return domainjournal.Actor{Kind: domainjournal.ActorEngine, Source: strings.TrimSpace(source)}
}

func modelActor(agentID string) domainjournal.Actor {
	return AuditActorForAgent(agentID)
}

// AuditActorForAgent builds the actor for a model-facing ledger mutation.
func AuditActorForAgent(agentID string) domainjournal.Actor {
	return domainjournal.Actor{Kind: domainjournal.ActorAgent, ID: strings.TrimSpace(agentID), Source: "ledger_tool"}
}

func (h *Hypothesis) auditState() *domainjournal.State {
	if h == nil {
		return nil
	}
	return &domainjournal.State{
		Status:       string(h.Status),
		AssignedTo:   h.AssignedTo,
		Confidence:   h.Confidence,
		VOI:          h.VOI,
		VOIEstimated: h.VOIEstimated,
		Attempts:     h.Attempts,
	}
}

// EnableJournal opens the per-scan domain journal. A journal is optional for
// tests and file-only deployments; production scan contexts enable it
// automatically when a scan directory is available.
func (ls *LedgerStore) EnableJournal(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("scanctx: journal directory is empty")
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if strings.TrimSpace(ls.scanID) == "" {
		return fmt.Errorf("scanctx: scan id is required before enabling journal")
	}
	j, err := domainjournal.Open(filepath.Join(dir, domainjournal.FileName), ls.scanID)
	if err != nil {
		return err
	}
	ls.journal = j
	return nil
}

// AppendTrafficObservation records one proxy-observed request/response
// (R2b trafficproxy) as an observed InfoItem in this scan's domain journal.
// The record is an audit entry only: it never mutates the hypothesis graph
// and never participates in verdicts. Called from the trafficproxy sink
// goroutine — never on the proxy request path.
func (ls *LedgerStore) AppendTrafficObservation(item *infoitem.Item, summary string) error {
	return ls.appendObservation(domainjournal.OpObserveTraffic, engineActor("trafficproxy"), item, summary)
}

// AppendToolObservation records one engine-detected observation taken from a
// tool's own output (sensitive file served with 200, credential material in a
// response, and the like). Like AppendTrafficObservation it is an audit entry
// only — the hypothesis graph is untouched — but unlike a model-written
// evidence record it cannot be skipped by the model: budget exhaustion or an
// LLM error after the tool returned still leaves the observation in the
// journal, and therefore in the cold-path projection.
func (ls *LedgerStore) AppendToolObservation(item *infoitem.Item, summary string) error {
	return ls.appendObservation(domainjournal.OpToolObservation, engineActor("tool-observation"), item, summary)
}

// AppendFindingObservation projects a persisted finding into the information
// space. Called by the report gate after the finding is durable, so every
// finding carries an InfoItem even when the model never linked a hypothesis.
func (ls *LedgerStore) AppendFindingObservation(item *infoitem.Item, summary string) error {
	return ls.appendObservation(domainjournal.OpFindingRecorded, engineActor("report-gate"), item, summary)
}

// appendObservation is the shared journal-only append used by observation
// producers. It takes no ledger lock and never mutates hypothesis state.
func (ls *LedgerStore) appendObservation(op domainjournal.Operation, actor domainjournal.Actor, item *infoitem.Item, summary string) error {
	if item == nil {
		return fmt.Errorf("scanctx: nil info item for %s", op)
	}
	ls.mu.RLock()
	j := ls.journal
	scanID := ls.scanID
	ls.mu.RUnlock()
	if j == nil {
		return fmt.Errorf("scanctx: no journal for %s", op)
	}
	item.ScanID = scanID
	rec := domainjournal.Record{
		ScanID:    scanID,
		Actor:     actor,
		Operation: op,
		Summary:   summary,
		InfoItem:  item,
	}
	// Journal.Append serializes on its own mutex and assigns scan_id/seq, so
	// this append neither contends the ledger mutex nor mutates state.
	if _, err := j.Append(rec); err != nil {
		return fmt.Errorf("scanctx: append %s: %w", op, err)
	}
	return nil
}

// AppendInvocationAudit records one integration invocation (skill load /
// sub-agent delegation / MCP tool call) as an audit-only domain journal
// entry. Like AppendTrafficObservation it never mutates the hypothesis graph
// and never participates in verdicts — it is the per-scan authority for the
// web layer's cross-scan invocation projection (charter §5.1: telemetry is
// never an audit source). op must be one of the audit-only invocation
// operations (OpSkillLoad / OpAgentDelegat / OpMcpCall); an unknown op is an
// error, not a silent write.
func (ls *LedgerStore) AppendInvocationAudit(op domainjournal.Operation, actor domainjournal.Actor, summary string) error {
	switch op {
	case domainjournal.OpSkillLoad, domainjournal.OpAgentDelegat, domainjournal.OpMcpCall:
	default:
		return fmt.Errorf("scanctx: operation %q is not an invocation-audit op", op)
	}
	ls.mu.RLock()
	j := ls.journal
	ls.mu.RUnlock()
	if j == nil {
		return fmt.Errorf("scanctx: no journal for invocation audit")
	}
	rec := domainjournal.Record{
		ScanID:    ls.scanID,
		Actor:     actor,
		Operation: op,
		Summary:   summary,
	}
	// Journal.Append serializes on its own mutex and assigns scan_id/seq.
	if _, err := j.Append(rec); err != nil {
		return fmt.Errorf("scanctx: append invocation audit: %w", err)
	}
	return nil
}

// JournalLastSeq reports the last durable audit sequence (0 without a journal).
func (ls *LedgerStore) JournalLastSeq() uint64 {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	if ls.journal == nil {
		return 0
	}
	return ls.journal.LastSeq()
}

// appendAuditLocked must be called with ls.mu held, before the in-memory
// mutation is committed. A failed durable append therefore leaves the old
// state intact.
func (ls *LedgerStore) appendAuditLocked(rec domainjournal.Record) error {
	if ls.journal == nil {
		return nil
	}
	rec.ScanID = ls.scanID
	if _, err := ls.journal.Append(rec); err != nil {
		return fmt.Errorf("scanctx: append domain audit: %w", err)
	}
	return nil
}

func formatHypothesisBrief(h *Hypothesis) string {
	if h == nil {
		return ""
	}
	var loc string
	if h.Endpoint != "" {
		loc = " " + h.Endpoint
	}
	if h.Parameter != "" {
		loc += " [" + h.Parameter + "]"
	}
	if h.Role != "" {
		loc += " as " + h.Role
	}
	return strings.TrimSpace(h.VulnClass + loc)
}

// mutateHypothesisAs serializes a clone/audit/commit cycle. The in-memory
// record is only replaced after the audit append succeeds, so a failed audit
// cannot leave an unaudited mutation behind.
func (ls *LedgerStore) mutateHypothesisAs(hypID string, actor domainjournal.Actor, op domainjournal.Operation, summary string, mutate func(*Hypothesis)) (Hypothesis, error) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	h := ls.hyps[hypID]
	if h == nil {
		return Hypothesis{}, fmt.Errorf("scanctx: unknown hypothesis %q", hypID)
	}
	before := h.auditState()
	cp := h.clone()
	mutate(&cp)
	cp.UpdatedAt = time.Now().UTC()
	if err := ls.appendAuditLocked(domainjournal.Record{
		Actor:        actor,
		Operation:    op,
		HypothesisID: hypID,
		Summary:      summary,
		Before:       before,
		After:        cp.auditState(),
	}); err != nil {
		return Hypothesis{}, err
	}
	*h = cp
	if err := ls.persistLocked(); err != nil {
		return cp.clone(), err
	}
	return cp.clone(), nil
}

func (ls *LedgerStore) persistLocked() error {
	data, path := ls.marshalLocked()
	return ls.writeFile(data, path)
}

// evidenceInfoItemLocked builds the cold-path projection for one evidence
// record. Raw request/response bodies stay in the local ledger; PostgreSQL only
// receives the bounded summary and provenance pointers.
func (ls *LedgerStore) evidenceInfoItemLocked(h *Hypothesis, ev Evidence) (*infoitem.Item, []infoitem.Relation) {
	if h == nil || ls.scanID == "" {
		return nil, nil
	}
	id := ev.InfoItemID
	if id == "" {
		id = infoitem.StableEvidenceID(ls.scanID, ev.ID)
	}
	subject := h.Endpoint
	if subject == "" {
		subject = h.Target
	}
	if subject == "" {
		subject = h.ID
	}
	sourceKind := "agent"
	if ev.Source != "" {
		sourceKind = "engine"
	}
	if ev.Kind == EvidenceFindingRef {
		sourceKind = "finding"
	}
	context := map[string]string{
		"scan_id":    ls.scanID,
		"vuln_class": h.VulnClass,
		"hypothesis": h.ID,
	}
	if h.Parameter != "" {
		context["parameter"] = h.Parameter
	}
	if h.Role != "" {
		context["role"] = h.Role
	}
	if h.Target != "" {
		context["target"] = h.Target
	}
	supports := make([]string, 0, len(ev.Supports))
	relations := make([]infoitem.Relation, 0, len(ev.Supports))
	relationIDs := make([]string, 0, len(ev.Supports))
	for _, support := range ev.Supports {
		supportItemID := infoitem.StableEvidenceID(ls.scanID, support)
		supports = append(supports, supportItemID)
		relationIDs = append(relationIDs, supportItemID)
		relations = append(relations, infoitem.Relation{
			ScanID:    ls.scanID,
			FromID:    id,
			ToID:      supportItemID,
			Type:      infoitem.RelationSupports,
			Weight:    ev.Confidence,
			CreatedAt: ev.CreatedAt,
		})
	}
	item := &infoitem.Item{
		ID:         id,
		ScanID:     ls.scanID,
		Nature:     infoitem.Nature(ev.Nature),
		Supports:   supports,
		Confidence: ev.Confidence,
		Coordinates: infoitem.Coordinates{
			Content: infoitem.Content{
				Text:      ev.Summary,
				MediaType: "text/plain",
			},
			Source: infoitem.Source{
				Kind:       sourceKind,
				Name:       ev.Source,
				AgentID:    ev.AgentID,
				EvidenceID: ev.ID,
			},
			ObservedAt:  ev.CreatedAt,
			Subject:     infoitem.Subject{Kind: "endpoint", Identifier: subject},
			Purpose:     h.VulnClass,
			Context:     context,
			RelationIDs: relationIDs,
		},
		CoordinateStatus: infoitem.CoordinatesComplete,
		CreatedAt:        ev.CreatedAt,
	}
	return item, relations
}
