package web

// Read-only exploration projection for one scan — the shared node/edge model
// behind the two exploration views (REDESIGN §7.2 任务详情「探索链路」拓扑 +
// 「播报板」流水; 4zreco dual-graph pattern, projected):
//
//	GET /api/scans/{id}/exploration
//
// The projection reduces two durable scan-local sources into small nodes and
// edges:
//   - the domain journal — the full append-only history the Eino engine's
//     callback/middleware chain writes at every domain mutation boundary
//     (hypothesis lifecycle, evidence, findings, delegations, objective,
//     traffic/tool observations), and
//   - ledger.json — the state authority (charter §5/§6.3), used for current
//     hypothesis titles/states and the structural finding_ref links
//     (Evidence.FindingID) that the journal record alone does not carry.
//
// Discipline mirrors scan_audit.go / scan_ledger.go: the projection never
// mutates either file and never participates in vulnerability judgment. Only
// structurally existing edges are drawn — no soft matching, no fabricated
// associations (charter §2: 对象映射不得反向). A missing journal is surfaced
// as absent, never as an empty graph.

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// Node kinds (fixed graph columns, left → right): goal → agent → hypothesis
// → evidence/observation → finding.
const (
	exploreKindGoal        = "goal"
	exploreKindAgent       = "agent"
	exploreKindHypothesis  = "hypothesis"
	exploreKindEvidence    = "evidence"
	exploreKindFinding     = "finding"
	exploreKindObservation = "observation"
)

// Edge relations (structurally existing links only).
const (
	exploreRelClaim    = "claim"    // agent/main loop → hypothesis
	exploreRelYields   = "yields"   // hypothesis → evidence
	exploreRelProves   = "proves"   // evidence (finding_ref) → finding
	exploreRelSupports = "supports" // evidence → evidence
)

const (
	// exploreMainNodeID is the synthetic main-loop node (the Eino runtime
	// ChatModelAgent iteration): every claim made by an actor that is not a
	// delegated specialist structurally originated from the main loop.
	exploreMainNodeID = "main"
	// exploreSummaryCap bounds node summaries in the projection payload; full
	// evidence text stays in the ledger tab (GET /api/scans/{id}/ledger).
	exploreSummaryCap = 200
)

type explorationNode struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Seq      uint64 `json:"seq"` // first journal seq that produced it
	At       string `json:"at"`  // RFC3339, first occurrence
	Title    string `json:"title"`
	Summary  string `json:"summary,omitempty"`
	State    string `json:"state,omitempty"`  // hypothesis lifecycle / finding verified
	Nature   string `json:"nature,omitempty"` // evidence: observed|inferred
	Severity string `json:"severity,omitempty"`
	Actor    string `json:"actor,omitempty"` // agent id / engine source
	Up       int    `json:"up"`
	Down     int    `json:"down"`
}

type explorationEdge struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
	Rel string `json:"rel"`
}

type explorationView struct {
	ScanID         string            `json:"scan_id"`
	JournalPresent bool              `json:"journal_present"`
	JournalDamaged bool              `json:"journal_damaged"`
	LedgerPresent  bool              `json:"ledger_present"`
	Incomplete     bool              `json:"incomplete"`
	Note           string            `json:"note,omitempty"`
	Nodes          []explorationNode `json:"nodes"`
	Edges          []explorationEdge `json:"edges"`
}

// explorationSession is one session contributing to the chain: its record id
// keys every durable read (journal rows, ledger snapshot rows) and dir only
// anchors the legacy file fallback — it may be empty for a session whose
// directory retention already removed (R6.2: sessions resolve from the
// durable record sweep, not from disk).
type explorationSession struct {
	scanID string
	dir    string
}

// explorationSessions expands the resolved scan identity into the sessions
// whose journals/ledgers compose THIS target's chain, oldest session first.
// Instance form: same-target siblings concatenate — a 1:1 re-dispatch
// continues the same investigation in a fresh session directory (REDESIGN
// D11), so the run view must span its sessions. Distinct-target siblings of
// a multi-target dispatch stay out of the graph (a chain is per target) and
// are disclosed in the note instead. Record-id form stays scoped to that one
// session (the scan-detail view).
//
// R6.2 retirement (REDESIGN D12): with the durable stores registered the
// sessions enumerate from the PG record sweep keyed by the run's instance id
// — PG aggregates the run, so no sibling directory walk happens here, and a
// purged session still contributes its durable rows. The legacy walk
// (explorationSessionDirs below) serves only the nil-backend (test) file
// mode, where directories are the durable unit.
func (s *Server) explorationSessions(res scanResolution, entries []scanEntry) (sessions []explorationSession, otherTargets int) {
	if !res.instanceForm || res.rec == nil || res.rec.InstanceID == "" {
		return []explorationSession{{scanID: scanStateIDForDir(res.dir), dir: res.dir}}, 0
	}
	if currentEventStateBackend() == nil {
		dirs, other := s.explorationSessionDirs(res, entries)
		out := make([]explorationSession, 0, len(dirs))
		for _, d := range dirs {
			out = append(out, explorationSession{scanID: scanStateIDForDir(d), dir: d})
		}
		return out, other
	}
	idx := s.scanSummaryIndexFor(entries)
	recs := idx.lookupAll(res.rec.InstanceID, res.rec.ID)
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].StartedAt > recs[j].StartedAt })
	target := res.rec.Target
	for _, rec := range recs {
		if rec.ID == "" {
			continue
		}
		if target != "" && rec.Target != target {
			otherTargets++
			continue
		}
		sessions = append(sessions, explorationSession{scanID: rec.ID, dir: idx.dirByScanID[rec.ID]})
	}
	if len(sessions) == 0 {
		sessions = []explorationSession{{scanID: scanStateIDForDir(res.dir), dir: res.dir}}
	}
	return sessions, otherTargets
}

// explorationSessionDirs is the pre-R6.2 file-mode sibling walk, kept
// byte-identical for the nil-backend (test) mode: instance form expands into
// the same-target sibling session directories, newest-first sorted then
// reversed to chronological; record-id form stays scoped to one session.
func (s *Server) explorationSessionDirs(res scanResolution, entries []scanEntry) (dirs []string, otherTargets int) {
	if !res.instanceForm || res.rec == nil || res.rec.InstanceID == "" {
		return []string{res.dir}, 0
	}
	idx := s.scanSummaryIndexFor(entries)
	recs := idx.lookupAll(res.rec.InstanceID, res.rec.ID)
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].StartedAt > recs[j].StartedAt })
	target := res.rec.Target
	for _, rec := range recs {
		d := idx.dirByScanID[rec.ID]
		if d == "" {
			continue
		}
		if target != "" && rec.Target == target {
			dirs = append(dirs, d)
			continue
		}
		otherTargets++
	}
	// newest-first → chronological
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	if len(dirs) == 0 {
		dirs = []string{res.dir}
	}
	return dirs, otherTargets
}

func joinNotes(base, extra string) string {
	if extra == "" {
		return base
	}
	return base + " " + extra
}

// handleScanExploration serves GET /api/scans/{id}/exploration.
//
// Instance-form requests project the run's chain across its same-target
// sibling sessions (D11 re-dispatch): journals concatenate chronologically
// and the newest session's ledger wins per hypothesis id, so a resumed run
// keeps its previous chain instead of surfacing an empty graph.
func (s *Server) handleScanExploration(w http.ResponseWriter, r *http.Request) {
	scanID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/scans/"), "/exploration")
	if scanID == "" {
		http.Error(w, `{"error":"scan id is required"}`, http.StatusBadRequest)
		return
	}
	entries := s.findAllScanSummaries()
	res, ok := s.resolveScanRecordByIDFrom(scanID, func() []scanEntry { return entries })
	if !ok {
		http.Error(w, `{"error":"scan not found"}`, http.StatusNotFound)
		return
	}
	sessions, otherTargets := s.explorationSessions(res, entries)
	sessionCount := len(sessions)

	// D11 披露：instance 是 run 身份——重派发让同一 target 名下有多个会话目录
	// (合并投影)，多目标派发让一个 instance 名下有多个 target 的记录（如实注明
	// 只画本 target 的链，不冒充完整历史）。
	disclosure := ""
	if sessionCount > 1 {
		disclosure = fmt.Sprintf(
			"this run re-dispatched the same target %d times; the projection merges all sibling sessions (oldest → newest)",
			sessionCount,
		)
	}
	if otherTargets > 0 {
		disclosure = joinNotes(disclosure, fmt.Sprintf(
			"the run dispatched %d further target scans sharing the instance; the graph covers this target's sessions only",
			otherTargets,
		))
	}

	view := explorationView{
		ScanID: filepath.Base(scanID),
		Nodes:  []explorationNode{},
		Edges:  []explorationEdge{},
	}

	// ── ledger snapshot: current-state authority ─────────────────────────
	// Hypothesis titles/states and the structural finding_ref links live
	// here; the journal supplies history the snapshot may have capped away
	// (evidence cap) or never recorded as state (observations, delegates).
	// Across sessions the walk is oldest → newest, so the newest session's
	// state wins per hypothesis id — the continuity continuation of the
	// chain, never a silent overwrite of a distinct hypothesis.
	//
	// R6.2 (charter §6.3): each session's snapshot resolves through the
	// ledger read seam — the durable row keyed by the session's record id
	// once the backend is registered (the store is anchored on the scan id;
	// the dir only feeds the pre-import file fallback), the legacy ledger.json
	// file otherwise. Presence follows the same source per session.
	var ledger map[string]scanctx.Hypothesis
	for _, sess := range sessions {
		store := scanctx.NewLedgerStore()
		if sess.dir != "" {
			store.SetPersistPath(sess.dir)
		}
		if _, present, rerr := scanctx.LedgerSnapshotRaw(sess.scanID); rerr == nil && present {
			// Durable row exists: key the store on the scan id so the
			// backend read resolves (and stable InfoItem ids derive).
			store.SetScanID(sess.scanID)
		} else if rerr != nil {
			log.Printf("[exploration] durable ledger read for %s failed, trying legacy file: %v", sess.scanID, rerr)
			if sess.dir == "" {
				continue
			}
			if _, serr := os.Stat(filepath.Join(sess.dir, ledgerFileName)); serr != nil {
				continue
			}
		} else if sess.dir == "" {
			// No durable row and no directory anchor: nothing to read.
			continue
		} else if _, serr := os.Stat(filepath.Join(sess.dir, ledgerFileName)); serr != nil {
			continue
		}
		store.LoadFromDisk()
		hyps := store.All()
		if ledger == nil {
			ledger = make(map[string]scanctx.Hypothesis, len(hyps))
		}
		for _, h := range hyps {
			ledger[h.ID] = h
		}
		view.LedgerPresent = true
	}
	ledgerMissing := !view.LedgerPresent

	// ── journal: append-only history ─────────────────────────────────────
	// R6.2 (charter §6.3): each session's records resolve through the journal
	// read seam keyed by the session's record id — the PG primary store's
	// per-scan stream once the backend is registered, the legacy per-scan
	// file otherwise (the dir anchors that fallback only; a purged session
	// contributes its durable rows with no file read). Presence = records in
	// either source; a session with neither contributes nothing.
	var records []domainjournal.Record
	journalDamaged := false
	for _, sess := range sessions {
		journalPath := ""
		if sess.dir != "" {
			journalPath = filepath.Join(sess.dir, domainjournal.FileName)
		}
		filePresent := journalPath != ""
		if filePresent {
			if _, err := os.Stat(journalPath); errors.Is(err, os.ErrNotExist) {
				filePresent = false
			}
		}
		sessionRecords, damaged, err := domainjournal.ReadRecordsResolved(r.Context(), journalPath, sess.scanID)
		if err != nil {
			http.Error(w, `{"error":"failed to read domain journal"}`, http.StatusInternalServerError)
			return
		}
		if filePresent || len(sessionRecords) > 0 || damaged {
			view.JournalPresent = true
			journalDamaged = journalDamaged || damaged
			records = append(records, sessionRecords...)
		}
	}
	if !view.JournalPresent {
		if ledgerMissing {
			view.Incomplete = true
			if sessionCount > 1 {
				view.Note = joinNotes("no domain journal and no ledger snapshot exist in any of this run's sessions", disclosure)
			} else {
				view.Note = joinNotes("no domain journal and no ledger snapshot exist for this scan", disclosure)
			}
			writeJSONStatus(w, http.StatusOK, view)
			return
		}
		// Scans predating the journal: the chain is the ledger's current
		// state only — stated honestly instead of a fabricated history.
		buildExploration(&view, ledger, nil)
		view.Incomplete = true
		if sessionCount > 1 {
			view.Note = joinNotes("no domain journal exists in any of this run's sessions; the graph is the ledger's current state only (no claim history, delegates or observations)", disclosure)
		} else {
			view.Note = joinNotes("no domain journal exists for this scan; the graph is the ledger's current state only (no claim history, delegates or observations)", disclosure)
		}
		sortAndCountExploration(&view)
		writeJSONStatus(w, http.StatusOK, view)
		return
	}
	view.JournalDamaged = journalDamaged

	buildExploration(&view, ledger, records)

	notes := make([]string, 0, 3)
	if journalDamaged {
		notes = append(notes, "journal has a damaged trailing entry; nodes below the damage are authoritative, the tail is incomplete")
	}
	if !view.LedgerPresent {
		notes = append(notes, "no ledger snapshot exists; hypothesis states reflect journal records only")
	}
	view.Incomplete = journalDamaged || !view.LedgerPresent
	if disclosure != "" {
		notes = append(notes, disclosure)
	}
	view.Note = strings.Join(notes, " ")

	sortAndCountExploration(&view)
	writeJSONStatus(w, http.StatusOK, view)
}

// explorationBuilder accumulates nodes/edges during the journal walk.
type explorationBuilder struct {
	ledger   map[string]scanctx.Hypothesis
	nodes    map[string]*explorationNode
	edgeSeen map[[3]string]struct{}
	edges    []explorationEdge
}

func buildExploration(view *explorationView, ledger map[string]scanctx.Hypothesis, records []domainjournal.Record) {
	b := &explorationBuilder{
		ledger:   ledger,
		nodes:    map[string]*explorationNode{},
		edgeSeen: map[[3]string]struct{}{},
		// Non-nil from the start: a journal with only observations (no edges
		// yet) must marshal edges as [] — nil would serialize as JSON null and
		// crash the dashboard's array iteration.
		edges: []explorationEdge{},
	}
	for i := range records {
		b.consume(&records[i])
	}
	b.mergeLedger()
	for _, n := range b.nodes {
		view.Nodes = append(view.Nodes, *n)
	}
	view.Edges = b.edges
}

// consume folds one journal record into the node/edge model.
func (b *explorationBuilder) consume(rec *domainjournal.Record) {
	switch rec.Operation {
	case domainjournal.OpRecordObjective:
		n := b.node(exploreKindGoal, "goal", rec)
		n.Summary = truncExploreSummary(firstNonEmptyStr(infoItemText(rec.InfoItem), rec.Summary))

	case domainjournal.OpAgentDelegat:
		if id := delegateAgentID(rec.Summary); id != "" {
			n := b.node(exploreKindAgent, "agent:"+id, rec)
			n.Summary = truncExploreSummary(rec.Summary)
			n.Actor = rec.Actor.Source
			if role := delegateRole(rec.Summary); role != "" {
				n.Title = role
			}
		}

	case domainjournal.OpSkillLoad, domainjournal.OpMcpCall:
		// Invocation audit (skill loads / MCP calls) lives in the execution
		// column: real Eino-side actions, one node per record.
		id := fmt.Sprintf("inv:%d", rec.Seq)
		n := b.node(exploreKindAgent, id, rec)
		n.Summary = truncExploreSummary(rec.Summary)
		n.Actor = rec.Actor.Source

	case domainjournal.OpRecordHypothesis, domainjournal.OpSeedHypothesis:
		if rec.HypothesisID == "" {
			return
		}
		n := b.node(exploreKindHypothesis, hypNodeID(rec.HypothesisID), rec)
		n.Title = b.hypTitle(rec.HypothesisID)
		n.Summary = truncExploreSummary(recordBrief(rec.Summary))
		if rec.After != nil && rec.After.Status != "" {
			n.State = rec.After.Status
		}

	case domainjournal.OpClaimHypothesis, domainjournal.OpAssignHypothesis:
		if rec.HypothesisID == "" {
			return
		}
		id := hypNodeID(rec.HypothesisID)
		n := b.node(exploreKindHypothesis, id, rec)
		if n.Title == "" {
			n.Title = b.hypTitle(rec.HypothesisID)
		}
		n.Summary = truncExploreSummary(recordBrief(rec.Summary))
		if rec.After != nil && rec.After.Status != "" {
			n.State = rec.After.Status
		}
		b.edge(b.claimSource(rec.Actor), id, exploreRelClaim)

	case domainjournal.OpAddEvidence:
		if rec.EvidenceID == "" {
			return
		}
		evID := evNodeID(rec.EvidenceID)
		n := b.node(exploreKindEvidence, evID, rec)
		n.Title = evidenceKindLabel(rec.Summary)
		n.Summary = truncExploreSummary(firstNonEmptyStr(infoItemText(rec.InfoItem), rec.Summary))
		if rec.InfoItem != nil {
			n.Nature = string(rec.InfoItem.Nature)
		}
		if rec.HypothesisID != "" {
			b.edge(hypNodeID(rec.HypothesisID), evID, exploreRelYields)
		}

	case domainjournal.OpSetStatus, domainjournal.OpRecordAttempt, domainjournal.OpSetVOI:
		if rec.HypothesisID == "" {
			return
		}
		id := hypNodeID(rec.HypothesisID)
		n := b.node(exploreKindHypothesis, id, rec)
		if n.Title == "" {
			n.Title = b.hypTitle(rec.HypothesisID)
		}
		if rec.After != nil && rec.After.Status != "" {
			n.State = rec.After.Status
		}

	case domainjournal.OpFindingRecorded:
		findingID, title, severity, verified := findingFields(rec.InfoItem, rec.Summary)
		if findingID == "" {
			return
		}
		n := b.node(exploreKindFinding, findingNodeID(findingID), rec)
		n.Title = title
		n.Severity = severity
		n.State = verified
		n.Summary = truncExploreSummary(rec.Summary)

	case domainjournal.OpObserveTraffic, domainjournal.OpToolObservation:
		n := b.node(exploreKindObservation, obsNodeID(rec), rec)
		n.Title, n.Summary = observationFields(rec.InfoItem, rec.Summary)
		n.Actor = rec.Actor.Source
		if rec.InfoItem != nil {
			n.Nature = string(rec.InfoItem.Nature)
		}

	case domainjournal.OpMergeLedger, domainjournal.OpResetLedger:
		// Structural history rewrites: the ledger's current state already
		// reflects the outcome; no synthetic nodes are invented here.
	}
}

// node returns the existing node or creates it stamped with this record's
// seq/at (first occurrence wins).
func (b *explorationBuilder) node(kind, id string, rec *domainjournal.Record) *explorationNode {
	if n, ok := b.nodes[id]; ok {
		return n
	}
	n := &explorationNode{
		ID:   id,
		Kind: kind,
	}
	if rec != nil {
		n.Seq = rec.Seq
		n.At = rec.At.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	b.nodes[id] = n
	return n
}

// hypTitle prefers the ledger's authoritative title; the hypothesis id is the
// fallback for hypotheses the current ledger no longer carries.
func (b *explorationBuilder) hypTitle(hypID string) string {
	if h, ok := b.ledger[hypID]; ok && strings.TrimSpace(h.Title) != "" {
		return h.Title
	}
	return hypID
}

// claimSource maps a claim/assign actor to its node: a delegated specialist
// when the actor is an agent with a delegate node, otherwise the synthetic
// main-loop node (structurally: the Eino ChatModelAgent iteration).
func (b *explorationBuilder) claimSource(actor domainjournal.Actor) string {
	if actor.Kind == domainjournal.ActorAgent && actor.ID != "" {
		if _, ok := b.nodes["agent:"+actor.ID]; ok {
			return "agent:" + actor.ID
		}
	}
	if _, ok := b.nodes[exploreMainNodeID]; !ok {
		b.nodes[exploreMainNodeID] = &explorationNode{
			ID:    exploreMainNodeID,
			Kind:  exploreKindAgent,
			Title: "主循环 (Eino ChatModelAgent)",
			Actor: "chatmodel-agent",
		}
	}
	return exploreMainNodeID
}

func (b *explorationBuilder) edge(src, dst, rel string) {
	if src == dst {
		return
	}
	key := [3]string{src, dst, rel}
	if _, dup := b.edgeSeen[key]; dup {
		return
	}
	b.edgeSeen[key] = struct{}{}
	b.edges = append(b.edges, explorationEdge{Src: src, Dst: dst, Rel: rel})
}

// mergeLedger applies the ledger's authoritative state: hypothesis
// titles/statuses and the structural finding_ref links (Evidence.FindingID →
// proves edges) plus surviving supports edges. Journal history stays; the
// ledger only corrects and completes it.
func (b *explorationBuilder) mergeLedger() {
	for _, h := range b.ledger {
		id := hypNodeID(h.ID)
		n := b.node(exploreKindHypothesis, id, nil)
		if strings.TrimSpace(h.Title) != "" {
			n.Title = h.Title
		}
		if h.Status != "" {
			n.State = string(h.Status)
		}
		for _, ev := range h.Evidence {
			evID := evNodeID(ev.ID)
			if en, ok := b.nodes[evID]; ok && en.Title == "" {
				en.Title = string(ev.Kind)
			}
			if ev.FindingID != "" {
				fid := findingNodeID(ev.FindingID)
				if _, ok := b.nodes[fid]; !ok {
					b.nodes[fid] = &explorationNode{
						ID:    fid,
						Kind:  exploreKindFinding,
						Title: ev.FindingID,
					}
				}
				b.edge(evID, fid, exploreRelProves)
			}
			for _, sup := range ev.Supports {
				b.edge(evID, evNodeID(sup), exploreRelSupports)
			}
		}
	}
}

// sortAndCountExploration gives the projection a deterministic order (nodes
// by (seq, id), edges by (src, dst, rel)) and recomputes up/down counts from
// the final edge set. Same inputs → same projection.
func sortAndCountExploration(view *explorationView) {
	sort.SliceStable(view.Nodes, func(i, j int) bool {
		if view.Nodes[i].Seq != view.Nodes[j].Seq {
			return view.Nodes[i].Seq < view.Nodes[j].Seq
		}
		return view.Nodes[i].ID < view.Nodes[j].ID
	})
	sort.SliceStable(view.Edges, func(i, j int) bool {
		if view.Edges[i].Src != view.Edges[j].Src {
			return view.Edges[i].Src < view.Edges[j].Src
		}
		if view.Edges[i].Dst != view.Edges[j].Dst {
			return view.Edges[i].Dst < view.Edges[j].Dst
		}
		return view.Edges[i].Rel < view.Edges[j].Rel
	})
	for i := range view.Nodes {
		view.Nodes[i].Up, view.Nodes[i].Down = 0, 0
	}
	for _, e := range view.Edges {
		for i := range view.Nodes {
			switch view.Nodes[i].ID {
			case e.Src:
				view.Nodes[i].Down++
			case e.Dst:
				view.Nodes[i].Up++
			}
		}
	}
}

// ── field extraction helpers (code-fixed record formats) ────────────────────

func hypNodeID(hypID string) string  { return "hyp:" + hypID }
func evNodeID(evID string) string    { return "ev:" + evID }
func findingNodeID(id string) string { return "finding:" + id }

func obsNodeID(rec *domainjournal.Record) string {
	if rec.InfoItem != nil && rec.InfoItem.ID != "" {
		return "obs:" + rec.InfoItem.ID
	}
	return fmt.Sprintf("obs:seq-%d", rec.Seq)
}

// recordBrief strips the leading op word from record summaries ("claim H-3 …"
// → "H-3 …") so node summaries read naturally.
func recordBrief(summary string) string {
	s := strings.TrimSpace(summary)
	if i := strings.Index(s, " "); i > 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}

func truncExploreSummary(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= exploreSummaryCap {
		return s
	}
	return s[:exploreSummaryCap]
}

func infoItemText(item *infoitem.Item) string {
	if item == nil {
		return ""
	}
	return strings.TrimSpace(item.Coordinates.Content.Text)
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// evidenceKindLabel extracts the evidence kind from the add_evidence record
// summary ("evidence EV-3 [probe/observed]" → "probe"); fallback "evidence".
func evidenceKindLabel(summary string) string {
	i := strings.Index(summary, "[")
	j := strings.Index(summary, "]")
	if i >= 0 && j > i {
		if k := strings.Index(summary[i:j], "/"); k >= 0 {
			return summary[i+1 : i+k]
		}
		return summary[i+1 : j]
	}
	return "evidence"
}

// delegateAgentID extracts the specialist id from delegation audit summaries
// ("create_agent id=x role=…", "spawn_agent id=…", "delegate_result id=…").
func delegateAgentID(summary string) string {
	i := strings.Index(summary, "id=")
	if i < 0 {
		return ""
	}
	rest := summary[i+len("id="):]
	if j := strings.IndexAny(rest, " \t\""); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// delegateRole extracts the quoted role from delegation summaries.
func delegateRole(summary string) string {
	i := strings.Index(summary, `role="`)
	if i < 0 {
		return ""
	}
	rest := summary[i+len(`role="`):]
	if j := strings.Index(rest, `"`); j >= 0 {
		return rest[:j]
	}
	return ""
}

// findingFields extracts (finding_id, title, severity, verified) from a
// finding projection InfoItem (reporting.go fixes this context vocabulary).
func findingFields(item *infoitem.Item, fallback string) (string, string, string, string) {
	if item == nil {
		return "", truncExploreSummary(fallback), "", ""
	}
	ctx := item.Coordinates.Context
	findingID := ctx["finding_id"]
	title := ctx["finding"]
	if title == "" {
		title = truncExploreSummary(fallback)
	}
	return findingID, title, ctx["severity"], ctx["verified"]
}

// observationFields extracts (subject, text) from a traffic/tool observation.
func observationFields(item *infoitem.Item, fallback string) (string, string) {
	if item == nil {
		return "", truncExploreSummary(fallback)
	}
	return item.Coordinates.Subject.Identifier, truncExploreSummary(item.Coordinates.Content.Text)
}
