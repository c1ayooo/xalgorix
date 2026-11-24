package agent

// context_snapshot.go — IG6b/IG6c: the context-compaction *projection*.
//
// A compaction does not "summarize the conversation" from text. It rebuilds a
// fixed-order snapshot from the authoritative stores (ledger, graph hints/
// intents, report-gate findings, coverage, notes, tool-output archives),
// renders it deterministically, and then persists the same snapshot as:
//
//   - a graph digest node (kind=digest, state=active; the previous active
//     digest for the region becomes superseded, with a derived_from edge), and
//   - an inferred InfoItem with non-empty Supports, appended to the domain
//     journal — the existing journal → River → PG cold path persists it.
//
// The hot path reads only in-memory authorities. Graph reads happen once per
// compaction (not per model turn), which is inside the INFO_AS §9 budget: the
// rare compaction is not "the hot path going through PostgreSQL".
//
// Authority: every section carries a [gate]/[ledger]/[graph]/[engine] tag and
// emits IDs, never bodies. The snapshot is explicitly not proof; consumers
// re-fetch with read_ledger / read_info_item / read_tool_output / read_notes.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/notes"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/reporting"
)

const (
	snapshotDefaultBudgetBytes = 24 * 1024
	snapshotMaxBudgetBytes     = 128 * 1024
	snapshotWorkerSeedBytes    = 8 * 1024
	snapshotNotesBudgetBytes   = 6000

	snapHintsMax    = 8
	snapIntentsMax  = 12
	snapBeliefsMax  = 15
	snapDeadMax     = 15
	snapFindingsMax = 12
	snapObsMax      = 10
	snapGapsMax     = 10
	snapActionsMax  = 10
)

// contextSnapshot is one rendered compaction projection.
type contextSnapshot struct {
	Region string
	Text   string
	Refs   []string
	// Sections counts rendered lines per section (empty sections are absent).
	Sections map[string]int
	// HasState is true when at least one authoritative state section rendered.
	// Without it the caller keeps the lossy transcript digest.
	HasState bool
	// NodeIDs are graph nodes the digest derives from (hints, intents) so the
	// persistence step can add derived_from edges without another query.
	NodeIDs []int64
	// DroppedMessages is how many transcript messages this compaction aged
	// out. Persisted for the audit question "which digest swallowed seq N".
	DroppedMessages int
}

// digestRegion names the region a snapshot belongs to. One lane = one agent
// run; workers each own their chain, so a restart or a sibling never collides.
func (a *Agent) digestRegion() string {
	if a == nil {
		return "lane:unknown"
	}
	id := strings.TrimSpace(a.delegatedAgentID)
	if id == "" {
		id = strings.TrimSpace(a.ID)
	}
	if id == "" {
		id = "root"
	}
	return "lane:agent:" + id
}

// snapshotBudgetBytes is 25% of the model window (the compaction leaves the
// recent window verbatim; this is the projected replacement). Without a
// configured window it falls back to a fixed 24 KiB.
func (a *Agent) snapshotBudgetBytes() int {
	if a != nil && a.cfg != nil && a.cfg.LLMContextWindow > 0 {
		tokens := int(float64(a.cfg.LLMContextWindow) * 0.25)
		if tokens > 0 {
			bytes := tokens * bytesPerTokenEstimate
			if bytes > snapshotMaxBudgetBytes {
				return snapshotMaxBudgetBytes
			}
			return bytes
		}
	}
	return snapshotDefaultBudgetBytes
}

// snapshotBuilder enforces the budget and collects refs deterministically.
type snapshotBuilder struct {
	a        *Agent
	region   string
	budget   int
	sb       strings.Builder
	refs     map[string]bool
	sections map[string]int
	nodeIDs  []int64
	hasState bool
}

func (b *snapshotBuilder) ref(id string) {
	id = strings.TrimSpace(id)
	if id != "" {
		b.refs[id] = true
	}
}

func (b *snapshotBuilder) remaining() int {
	return b.budget - b.sb.Len()
}

// add renders one section. Lines are already ordered and capped by the caller.
func (b *snapshotBuilder) add(name string, lines []string) {
	if len(lines) == 0 {
		return
	}
	header := "## " + name + "\n"
	if b.remaining() <= len(header)+1 {
		return
	}
	b.sb.WriteString(header)
	rendered := 0
	for i, line := range lines {
		if b.remaining() <= len(line)+1 {
			fmt.Fprintf(&b.sb, "- … %d more line(s) omitted to fit the context budget\n", len(lines)-i)
			break
		}
		b.sb.WriteString(line)
		b.sb.WriteString("\n")
		rendered++
	}
	b.sections[name] = rendered
	b.hasState = true
}

// addFixed renders a constant section (instructions) and does NOT mark the
// snapshot as carrying state: a no-state setup must still fall back to the
// lossy transcript digest instead of shipping an empty projection.
func (b *snapshotBuilder) addFixed(name string, lines []string) {
	if len(lines) == 0 {
		return
	}
	header := "## " + name + "\n"
	if b.remaining() <= len(header)+1 {
		return
	}
	b.sb.WriteString(header)
	for i, line := range lines {
		if b.remaining() <= len(line)+1 {
			fmt.Fprintf(&b.sb, "- … %d more line(s) omitted to fit the context budget\n", len(lines)-i)
			break
		}
		b.sb.WriteString(line)
		b.sb.WriteString("\n")
	}
	b.sections[name] = len(lines)
}

func (b *snapshotBuilder) addRaw(name, text string) {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return
	}
	header := "## " + name + "\n"
	if b.remaining() <= len(header)+1 {
		return
	}
	b.sb.WriteString(header)
	body := text
	if len(body) > b.remaining() {
		body = trimToValidUTF8(body, b.remaining()-80) + "\n… (truncated to fit the context budget)"
	}
	b.sb.WriteString(body)
	b.sb.WriteString("\n")
	b.sections[name] = strings.Count(body, "\n") + 1
	b.hasState = true
}

func (b *snapshotBuilder) finish() contextSnapshot {
	refs := make([]string, 0, len(b.refs))
	for id := range b.refs {
		refs = append(refs, id)
	}
	sort.Strings(refs)
	if b.sections == nil {
		b.sections = map[string]int{}
	}
	return contextSnapshot{
		Region:   b.region,
		Text:     strings.TrimRight(b.sb.String(), "\n"),
		Refs:     refs,
		Sections: b.sections,
		HasState: b.hasState,
		NodeIDs:  b.nodeIDs,
	}
}

func newSnapshotBuilder(a *Agent, region string, budget int, label string) *snapshotBuilder {
	if budget <= 0 {
		budget = snapshotDefaultBudgetBytes
	}
	if strings.TrimSpace(label) == "" {
		label = "projection"
	}
	b := &snapshotBuilder{
		a:        a,
		region:   region,
		budget:   budget,
		refs:     make(map[string]bool),
		sections: make(map[string]int),
	}
	runID, agentID := "", ""
	iteration := 0
	if a != nil {
		agentID = a.ID
		if a.state != nil {
			iteration = a.state.Iteration
		}
		if a.scanCtx != nil {
			runID = a.planGraphScanID()
		}
	}
	fmt.Fprintf(&b.sb, "[CONTEXT SNAPSHOT — region %s | run %s | agent %s | iteration %d | %s]\n",
		region, runID, agentID, iteration, label)
	b.sb.WriteString("Deterministic projection from authoritative stores. NOT proof: re-fetch IDs with " +
		"read_ledger / read_info_item / read_tool_output / read_notes before concluding or retesting.\n")
	return b
}

// buildContextSnapshot renders the fixed-order projection. dropped is the
// transcript window that is leaving the message buffer; it contributes only a
// bounded "recent actions" inventory (tool counts, result heads, archive ids).
func (a *Agent) buildContextSnapshot(region string, dropped []llm.Message, budget int) contextSnapshot {
	return a.buildContextSnapshotLabeled(region, dropped, budget,
		fmt.Sprintf("compaction %d", a.compactionCount+1))
}

// buildContextSnapshotLabeled renders the same projection with an explicit
// provenance label ("compaction N" or "worker seed") in the header.
func (a *Agent) buildContextSnapshotLabeled(region string, dropped []llm.Message, budget int, label string) contextSnapshot {
	b := newSnapshotBuilder(a, region, budget, label)
	a.snapshotHints(b)
	a.snapshotIntents(b)
	a.snapshotBeliefs(b)
	a.snapshotFindings(b)
	a.snapshotObservations(b)
	a.snapshotCoverage(b)
	a.snapshotNotes(b)
	a.snapshotActions(b, dropped)
	b.addFixed("Recovery", []string{
		"- [engine] ledger: read_ledger (hypotheses, evidence, schedulable pool)",
		"- [engine] information space: search_info / read_info_item; archives: read_tool_output",
		"- [engine] notes: read_notes(key=...); coverage: read the plan and gap report",
		"- [engine] do not treat this snapshot as proof and do not retest a rejected/exhausted direction without new evidence.",
	})
	snap := b.finish()
	snap.DroppedMessages = len(dropped)
	return snap
}

func (a *Agent) snapshotHints(b *snapshotBuilder) {
	repo := graph.Backend()
	scanID := a.planGraphScanID()
	if repo == nil || scanID == "" {
		return
	}
	ctx, cancel := planGraphContext()
	defer cancel()
	nodes, err := repo.ListNodes(ctx, scanID, graph.KindHint, graph.StateActive)
	if err != nil {
		log.Printf("[agent] snapshot: hints unavailable: %v", err)
		return
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	lines := make([]string, 0, snapHintsMax)
	for _, n := range nodes {
		if len(lines) >= snapHintsMax {
			break
		}
		if n.Origin != "" && n.Origin != "human" {
			continue
		}
		msg := strings.TrimSpace(stringField(n.PayloadJSON(), "message"))
		if msg == "" {
			continue
		}
		key := strings.TrimSpace(n.UnitKey)
		if key == "" {
			key = fmt.Sprintf("node-%d", n.ID)
		}
		lines = append(lines, fmt.Sprintf("- [graph] hint %s: %s", key, clipSnapshotText(msg, 200)))
		b.ref(key)
		b.nodeIDs = append(b.nodeIDs, n.ID)
	}
	b.add("Human input (operator guidance)", lines)
}

func (a *Agent) snapshotIntents(b *snapshotBuilder) {
	repo := graph.Backend()
	scanID := a.planGraphScanID()
	if repo == nil || scanID == "" {
		return
	}
	ctx, cancel := planGraphContext()
	defer cancel()
	nodes, err := repo.ListNodes(ctx, scanID, graph.KindIntent,
		graph.StateOpen, graph.StateRunning, graph.StatePaused)
	if err != nil {
		log.Printf("[agent] snapshot: intents unavailable: %v", err)
		return
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Priority != nodes[j].Priority {
			return nodes[i].Priority > nodes[j].Priority
		}
		return nodes[i].UnitKey < nodes[j].UnitKey
	})
	lines := make([]string, 0, snapIntentsMax)
	for _, n := range nodes {
		if len(lines) >= snapIntentsMax {
			break
		}
		p := n.PayloadJSON()
		title := strings.TrimSpace(stringField(p, "title"))
		if title == "" {
			title = strings.TrimSpace(n.UnitKey)
		}
		target := strings.TrimSpace(stringField(p, "target"))
		class := strings.TrimSpace(stringField(p, "vuln_class"))
		endpoint := strings.TrimSpace(stringField(p, "endpoint"))
		loc := strings.TrimSpace(strings.Join(nonEmpty(target, endpoint), " "))
		lines = append(lines, fmt.Sprintf("- [graph] intent %s %s: %s%s%s",
			strings.TrimSpace(n.UnitKey), n.State, clipSnapshotText(title, 120),
			optPrefix(" @ ", loc), optPrefix(" class=", class)))
		b.ref(n.UnitKey)
		b.nodeIDs = append(b.nodeIDs, n.ID)
	}
	b.add("Plan intents (authoritative plan graph)", lines)
}

func (a *Agent) snapshotBeliefs(b *snapshotBuilder) {
	l := a.ledger()
	if l == nil {
		return
	}
	all := l.All()
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })

	open := make([]string, 0, snapBeliefsMax)
	dead := make([]string, 0, snapDeadMax)
	for _, h := range all {
		switch h.Status {
		case scanctx.HypothesisQueued, scanctx.HypothesisTesting, scanctx.HypothesisBlocked:
			if len(open) >= snapBeliefsMax {
				continue
			}
			open = append(open, fmt.Sprintf("- [ledger] belief %s %s: %s %s conf=%.2f%s",
				h.ID, h.Status, h.VulnClass, beliefLocation(h), h.Confidence,
				optPrefix(" next=", clipSnapshotText(h.NextAction, 100))))
			b.ref(h.ID)
		case scanctx.HypothesisRejected, scanctx.HypothesisExhausted:
			if len(dead) >= snapDeadMax {
				continue
			}
			dead = append(dead, fmt.Sprintf("- [ledger] belief %s %s: %s %s attempts=%d",
				h.ID, h.Status, h.VulnClass, beliefLocation(h), h.Attempts))
			b.ref(h.ID)
		}
	}
	b.add("Open beliefs (ledger)", open)
	b.add("Dead ends — rejected/exhausted; do not retest without new evidence", dead)
}

func (a *Agent) snapshotFindings(b *snapshotBuilder) {
	if a.scanCtx == nil {
		return
	}
	findings := reporting.GetVulnerabilitiesForContext(a.scanCtx.ID)
	if len(findings) == 0 {
		return
	}
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].ID < findings[j].ID })
	lines := make([]string, 0, snapFindingsMax)
	for _, f := range findings {
		if len(lines) >= snapFindingsMax {
			break
		}
		tag := "needs-manual-verification"
		if len(f.Tags) > 0 {
			tag = strings.Join(f.Tags, ",")
		}
		lines = append(lines, fmt.Sprintf("- [gate] finding %s %s: %s %s tag=%s",
			f.ID, strings.ToLower(f.Severity), clipSnapshotText(f.Title, 120),
			clipSnapshotText(f.Endpoint, 100), tag))
		b.ref(f.ID)
	}
	b.add("Confirmed findings (report gate — acceptance decided here)", lines)
}

func (a *Agent) snapshotObservations(b *snapshotBuilder) {
	l := a.ledger()
	if l == nil {
		return
	}
	type obs struct {
		id, hyp, kind, summary string
		at                     time.Time
	}
	var all []obs
	for _, h := range l.All() {
		for _, ev := range h.Evidence {
			if ev.Nature != scanctx.EvidenceObserved {
				continue // missing/legacy nature is inferred, never an observation
			}
			all = append(all, obs{id: ev.ID, hyp: h.ID, kind: ev.Kind, summary: ev.Summary, at: ev.CreatedAt})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].at.Equal(all[j].at) {
			return all[i].at.After(all[j].at)
		}
		return all[i].id < all[j].id
	})
	lines := make([]string, 0, snapObsMax)
	for _, o := range all {
		if len(lines) >= snapObsMax {
			break
		}
		lines = append(lines, fmt.Sprintf("- [ledger] evidence %s (%s) hyp=%s: %s",
			o.id, o.kind, o.hyp, clipSnapshotText(o.summary, 160)))
		b.ref(o.id)
		b.ref(o.hyp)
	}
	b.add("Recent observations (observed evidence only)", lines)
}

func (a *Agent) snapshotCoverage(b *snapshotBuilder) {
	if a.state == nil {
		return
	}
	gaps := CoverageGaps(a.state, a.state.DiscoveredEndpoints)
	if len(gaps) == 0 {
		return
	}
	sort.SliceStable(gaps, func(i, j int) bool {
		if gaps[i].VulnClass != gaps[j].VulnClass {
			return gaps[i].VulnClass < gaps[j].VulnClass
		}
		return gaps[i].Endpoint < gaps[j].Endpoint
	})
	lines := make([]string, 0, snapGapsMax)
	for _, g := range gaps {
		if len(lines) >= snapGapsMax {
			break
		}
		ep := g.Endpoint
		if ep == "" {
			ep = "(whole target)"
		}
		lines = append(lines, fmt.Sprintf("- [engine] gap %s @ %s", g.VulnClass, clipSnapshotText(ep, 120)))
	}
	b.add("Coverage gaps (engine coverage matrix)", lines)
}

func (a *Agent) snapshotNotes(b *snapshotBuilder) {
	if a.scanCtx == nil {
		return
	}
	budget := snapshotNotesBudgetBytes
	if remaining := b.remaining(); remaining < budget {
		budget = remaining
	}
	if budget <= 0 {
		return
	}
	text := notes.FormatForContextBudgetID(a.scanCtx.ID, budget)
	if strings.TrimSpace(text) == "" {
		return
	}
	b.addRaw("Saved notes (budgeted; read_notes(key=...) for the full value)", text)
}

func (a *Agent) snapshotActions(b *snapshotBuilder, dropped []llm.Message) {
	toolsRun := make(map[string]int)
	type result struct{ line, archive string }
	var results []result
	for _, msg := range dropped {
		if msg.Role == "assistant" {
			for _, call := range msg.ToolCalls {
				if name := strings.TrimSpace(call.Function.Name); name != "" {
					toolsRun[name]++
				}
			}
			for _, line := range strings.Split(msg.Content, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "<function=") {
					name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), "<function="), ">")
					if name != "" {
						toolsRun[name]++
					}
				}
			}
		}
		if msg.Role == "tool" || (msg.Role == "user" && isToolResultMessage(msg.Content)) {
			archiveID, _ := archiveIDFromMessage(msg.Content)
			results = append(results, result{line: firstActionLine(msg.Content), archive: archiveID})
			if archiveID != "" {
				b.ref(archiveID)
			}
		}
	}
	if len(results) > snapActionsMax {
		results = results[len(results)-snapActionsMax:]
	}
	names := make([]string, 0, len(toolsRun))
	for name := range toolsRun {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if toolsRun[names[i]] != toolsRun[names[j]] {
			return toolsRun[names[i]] > toolsRun[names[j]]
		}
		return names[i] < names[j]
	})
	lines := make([]string, 0, len(names)+len(results))
	for _, name := range names {
		lines = append(lines, fmt.Sprintf("- [digest] tool %s ×%d", name, toolsRun[name]))
	}
	for _, r := range results {
		line := "- [digest] result: " + r.line
		if r.archive != "" {
			line += " [archive " + r.archive + "]"
		}
		lines = append(lines, line)
	}
	b.add("Recent actions in the aged window (counts/heads — a call is not proof of success)", lines)
}

// persistContextDigest writes the snapshot as the region's active digest node
// and supersedes the previous one. The InfoItem (inferred + supports) goes to
// the domain journal; the existing journal → River → PG cold path persists it.
// Best-effort: compaction never blocks on PG or on a cold-path watermark.
func (a *Agent) persistContextDigest(snap contextSnapshot, bytesBefore int) {
	if a == nil || a.scanCtx == nil {
		return
	}
	repo := graph.Backend()
	scanID := a.planGraphScanID()
	if repo == nil || scanID == "" {
		return
	}
	sum := sha256.Sum256([]byte(snap.Region + "\x00" + snap.Text))
	digestID := fmt.Sprintf("dg-%x", sum[:8])
	now := time.Now().UTC()
	payload, err := json.Marshal(map[string]any{
		"digest_id":        digestID,
		"region":           snap.Region,
		"snapshot":         snap.Text,
		"refs":             snap.Refs,
		"sections":         snap.Sections,
		"bytes_before":     bytesBefore,
		"bytes_after":      len(snap.Text),
		"dropped_messages": snap.DroppedMessages,
		"compaction_seq":   a.compactionCount + 1,
		"info": map[string]any{
			"subject":     map[string]any{"kind": "region", "identifier": snap.Region},
			"purpose":     "summary",
			"nature":      "inferred",
			"observed_at": now.Format(time.RFC3339),
			"context":     map[string]string{"run_id": scanID, "lane": a.ID},
		},
	})
	if err != nil {
		log.Printf("[agent] digest payload: %v", err)
		return
	}
	ctx, cancel := planGraphContext()
	defer cancel()
	node, err := repo.UpsertNode(ctx, graph.NodeInput{
		ScanID:  scanID,
		Kind:    graph.KindDigest,
		State:   graph.StateActive,
		UnitKey: digestID,
		Origin:  "engine",
		Payload: string(payload),
	})
	if err != nil {
		log.Printf("[agent] digest node upsert failed (region %s): %v", snap.Region, err)
		return
	}
	// Supersede the previous active digest in the same region and record the
	// lineage edge. Different lanes never supersede each other.
	prev, err := repo.ListNodes(ctx, scanID, graph.KindDigest, graph.StateActive)
	if err == nil {
		for _, n := range prev {
			if n.ID == node.ID || n.UnitKey == digestID {
				continue
			}
			if stringField(n.PayloadJSON(), "region") != snap.Region {
				continue
			}
			if _, terr := repo.Transition(ctx, scanID, n.ID, graph.KindDigest, graph.StateActive, graph.StateSuperseded); terr != nil {
				log.Printf("[agent] digest supersede %d failed: %v", n.ID, terr)
				continue
			}
			if eerr := repo.AddEdge(ctx, graph.EdgeInput{ScanID: scanID, SrcID: node.ID, DstID: n.ID, Rel: graph.RelDerivedFrom}); eerr != nil {
				log.Printf("[agent] digest lineage edge failed: %v", eerr)
			}
		}
	}
	for _, id := range snap.NodeIDs {
		if id <= 0 || id == node.ID {
			continue
		}
		_ = repo.AddEdge(ctx, graph.EdgeInput{ScanID: scanID, SrcID: node.ID, DstID: id, Rel: graph.RelDerivedFrom})
	}
	// Atomic provenance: inferred + non-empty supports. Without refs there is
	// no substantiated claim to publish, so the node remains the only artifact.
	if len(snap.Refs) == 0 {
		log.Printf("[agent] digest %s has no refs; InfoItem skipped (inferred requires supports)", digestID)
		return
	}
	item := infoitem.Item{
		ID:     "digest:" + scanID + ":" + digestID,
		Nature: infoitem.NatureInferred,
		Coordinates: infoitem.Coordinates{
			Content:    infoitem.Content{Text: snap.Text, MediaType: "text/markdown"},
			Source:     infoitem.Source{Kind: "engine", Name: "context-compactor", AgentID: a.ID},
			ObservedAt: now,
			Subject:    infoitem.Subject{Kind: "region", Identifier: snap.Region},
			Purpose:    "summary",
			Context: map[string]string{
				"run_id":     scanID,
				"lane":       a.ID,
				"digest_id":  digestID,
				"compaction": fmt.Sprintf("%d", a.compactionCount+1),
			},
		},
		Supports: append([]string(nil), snap.Refs...),
	}
	item.Normalize()
	l := a.ledger()
	if l == nil {
		log.Printf("[agent] digest %s not projected: ledger unavailable", digestID)
		return
	}
	if err := l.AppendContextDigest(&item, "context compaction snapshot "+digestID); err != nil {
		log.Printf("[agent] digest InfoItem append failed (cold path will miss it): %v", err)
		return
	}
	// IG3 read model: record that the digest node materializes this atom. The
	// link is optional (memory store/offline have no link surface) and never
	// state authority.
	if writer, ok := repo.(graph.InfoLinkWriter); ok {
		if err := writer.UpsertInfoLink(ctx, scanID, node.ID, item.ID, graph.InfoLinkRoleMaterializes); err != nil {
			log.Printf("[agent] digest info link failed: %v", err)
		}
	}
}

// workerSeedContext renders the region snapshot used to seed a dispatched
// worker. It is bounded (8 KiB) and never persisted: it is a read of the
// shared run state, so a new worker does not have to rediscover dead ends.
func (a *Agent) workerSeedContext(unitID string) string {
	region := "intent:" + strings.TrimSpace(unitID)
	if region == "intent:" {
		region = "run:" + a.planGraphScanID()
	}
	snap := a.buildContextSnapshotLabeled(region, nil, snapshotWorkerSeedBytes, "worker seed")
	if !snap.HasState {
		return ""
	}
	return "## Shared run state (engine projection; re-fetch IDs before relying on them)\n" + snap.Text
}

// firstActionLine returns the first informative line of a tool result: the
// formatted header ("Tool 'x' result:") and archive banners are skipped, so the
// aged-window inventory keeps the output's actual first content line.
func firstActionLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Tool '") || strings.HasPrefix(line, "[ARCHIVED as ") {
			continue
		}
		return clipSnapshotText(line, 140)
	}
	return clipSnapshotText(strings.TrimSpace(content), 140)
}

func beliefLocation(h scanctx.Hypothesis) string {
	loc := h.Endpoint
	if loc == "" {
		loc = h.Target
	}
	if h.Parameter != "" {
		loc += " [" + h.Parameter + "]"
	}
	if h.Role != "" {
		loc += " as " + h.Role
	}
	return strings.TrimSpace(loc)
}

func clipSnapshotText(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	return trimToValidUTF8(s, max) + "…"
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

func optPrefix(prefix, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return prefix + value
}
