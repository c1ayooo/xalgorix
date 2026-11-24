// Package agent — ledger_cross_scan.go wires the M4 region and association
// activation consumers (CORE_PHILOSOPHY.md §4.1/§4.2) onto the ledger tools.
//
// Both consumers are OUTPUT-ONLY annotations on existing tool results:
//   - read_ledger(filter=schedulable) gains a per-entry "same-endpoint
//     cross-scan history" region note (region consumer);
//   - claim_next_hypothesis gains a trailing "same-subject cross-scan history"
//     section (region note) plus a "current-scan related items" line that
//     follows relation edges rooted at THIS scan's same-subject items
//     (association consumer — relation edges are per-scan; cross-scan edge
//     graphs are a charter §13 research item, so the note says what it covers).
//
// Output states are distinguishable (charter §4.4 "空结果与错误的区别"):
//   - reader absent (no DSN): output unchanged — that is the normal file-only
//     mode, not a failure;
//   - reader error: a one-line visible failure note on the triggering entry
//     plus a single log line; the listing itself never fails;
//   - sync gaps: the result's Incomplete/GapReason from §6.4 are carried into
//     the note, never dropped.
//
// Budgets are small and deterministic: one region query per distinct endpoint
// capped at crossScanMaxRegionQueries per tool call, one association query per
// claim, so the hot path never issues unbounded cold-path reads.
package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// Budgets for the cross-scan annotations (M4). Kept small and deterministic.
const (
	crossScanRegionLimit      = 5
	crossScanAssocMaxDepth    = 2
	crossScanAssocLimit       = 10
	crossScanMaxScanLabels    = 3
	crossScanMaxRegionQueries = 6
	crossScanRegionMarker     = "同端点跨扫描历史"
	crossScanAssocMarker      = "同主体跨扫描历史"
	crossScanAssocReachMarker = "当前扫描内关联可达项"
	crossScanTruncatedNote    = "结果已截断"
	crossScanGapNote          = "冷路径同步存在缺口"
	crossScanBudgetNoteFmt    = "    ↳ 跨扫描历史查询预算已用尽（本次调用最多查询 %d 个端点）\n"
)

// crossScanHistory aggregates same-subject InfoItems found around one subject.
// total/observed/inferred/scans/ids describe PRIOR scans only (the hot-path
// ledger already covers the current one); currentIDs holds this scan's item
// IDs for the same subject, capped, so the association consumer can follow
// per-scan relation edges from real roots.
type crossScanHistory struct {
	total      int
	observed   int
	inferred   int
	scans      []string
	ids        []string
	currentIDs []string
	truncated  bool
	incomplete bool
	gapReason  string
}

// fetchCrossScanHistory runs one region query across the PROJECT's scans (the
// cross-scan shape §4.1; the project is the isolation fence, tasks inside one
// project share history) for the subject identifier and folds the result into
// counts. An empty whitelist means the caller has no project scope, so nothing
// is queried — an AllScans fallback would leak another project's scan IDs and
// counts into this model's output. The query is validated here so a test stub
// that skips validation cannot mask a malformed query against the real store.
func fetchCrossScanHistory(ctx context.Context, reader infoitem.Reader, scanIDs []string, currentScanID, subject string, limit int) (crossScanHistory, error) {
	if len(scanIDs) == 0 {
		return crossScanHistory{}, nil
	}
	q := infoitem.RegionQuery{ScanIDs: append([]string(nil), scanIDs...), Subject: subject, Limit: limit}
	if err := q.Validate(); err != nil {
		return crossScanHistory{}, err
	}
	res, err := reader.Region(ctx, q)
	if err != nil {
		return crossScanHistory{}, err
	}
	// Defence in depth: even if the store ignored the whitelist, a foreign
	// project's items must never be counted, labelled or handed back as IDs.
	allowed := make(map[string]bool, len(scanIDs))
	for _, id := range scanIDs {
		allowed[id] = true
	}
	hist := crossScanHistory{truncated: res.Truncated, incomplete: res.Incomplete, gapReason: res.GapReason}
	seen := make(map[string]bool)
	for _, item := range res.Items {
		if item.ScanID != "" && !allowed[item.ScanID] {
			continue
		}
		if currentScanID != "" && item.ScanID == currentScanID {
			if len(hist.currentIDs) < crossScanAssocLimit {
				hist.currentIDs = append(hist.currentIDs, item.ID)
			}
			continue
		}
		switch item.Nature {
		case infoitem.NatureObserved:
			hist.observed++
		case infoitem.NatureInferred:
			hist.inferred++
		}
		hist.total++
		hist.ids = append(hist.ids, item.ID)
		if item.ScanID != "" && !seen[item.ScanID] {
			seen[item.ScanID] = true
			if len(hist.scans) < crossScanMaxScanLabels {
				hist.scans = append(hist.scans, item.ScanID)
			}
		}
	}
	return hist, nil
}

// appendGapNote writes the §6.4 sync-gap marker when the cold path reports an
// incomplete projection for the queried range.
func appendGapNote(b *strings.Builder, incomplete bool, gapReason string) {
	if !incomplete {
		return
	}
	b.WriteString(", " + crossScanGapNote)
	if gapReason != "" {
		fmt.Fprintf(b, "(%s)", gapReason)
	}
}

// crossScanRegionCache annotates read_ledger(filter=schedulable) entries with
// the same-endpoint cross-scan region note. It is nil when the cold-path
// reader is absent. Within one tool call it issues at most one region query
// per unique endpoint, capped at crossScanMaxRegionQueries queries; the first
// reader error stops further queries and surfaces one visible failure note
// instead of silently degrading every entry.
type crossScanRegionCache struct {
	reader     infoitem.Reader
	ctx        context.Context
	scanID     string
	scanIDs    []string
	failed     bool
	budgetLeft int
	budgetNote string
	notes      map[string]string
}

// newCrossScanRegionCache returns nil when the optional cold-path reader is
// not configured OR the agent has no project scan whitelist; the caller's
// nil-receiver fast path keeps the tool output unchanged in the no-PG mode
// required by the charter and in project-less runs (where a cross-scan query
// would have no isolated scope to read).
func newCrossScanRegionCache(a *Agent) *crossScanRegionCache {
	reader := infoitem.DefaultReader()
	if reader == nil {
		return nil
	}
	scanIDs := a.crossScanScanIDsAllowed()
	if len(scanIDs) == 0 {
		return nil
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var scanID string
	if a.scanCtx != nil {
		scanID = a.scanCtx.ID
	}
	return &crossScanRegionCache{
		reader:     reader,
		ctx:        ctx,
		scanID:     scanID,
		scanIDs:    append([]string(nil), scanIDs...),
		budgetLeft: crossScanMaxRegionQueries,
		notes:      make(map[string]string),
	}
}

// writeTo appends the annotation line for one schedulable entry, or nothing
// when there is no cross-scan history (or the reader already failed).
func (c *crossScanRegionCache) writeTo(b *strings.Builder, endpoint string) {
	if c == nil {
		return
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return
	}
	if note, ok := c.notes[endpoint]; ok {
		b.WriteString(note)
		return
	}
	note := c.query(endpoint)
	c.notes[endpoint] = note
	b.WriteString(note)
}

func (c *crossScanRegionCache) query(endpoint string) string {
	if c.failed {
		return ""
	}
	if c.budgetLeft <= 0 {
		if c.budgetNote == "" {
			// The budget note appears exactly once, on the first over-budget
			// entry; later entries stay quiet to avoid spamming the listing.
			c.budgetNote = fmt.Sprintf(crossScanBudgetNoteFmt, crossScanMaxRegionQueries)
			return c.budgetNote
		}
		return ""
	}
	c.budgetLeft--
	hist, err := fetchCrossScanHistory(c.ctx, c.reader, c.scanIDs, c.scanID, endpoint, crossScanRegionLimit)
	if err != nil {
		// §3.4: a cold-path failure must be visible, not silently dropped. The
		// listing itself keeps working; later entries stay quiet to avoid spam.
		c.failed = true
		log.Printf("[ledger] cross-scan region query failed for %q: %v", endpoint, err)
		return fmt.Sprintf("    ↳ 冷路径查询失败，跨扫描历史暂不可用: %v\n", err)
	}
	if hist.total == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "    ↳ %s: %d 条既往观察 (observed=%d inferred=%d)",
		crossScanRegionMarker, hist.total, hist.observed, hist.inferred)
	if len(hist.scans) > 0 {
		fmt.Fprintf(&b, ", 扫描: %s", strings.Join(hist.scans, ", "))
	}
	if hist.truncated {
		b.WriteString(", " + crossScanTruncatedNote)
	}
	appendGapNote(&b, hist.incomplete, hist.gapReason)
	b.WriteString("\n")
	return b.String()
}

// appendCrossScanAssociationNote adds the "same-subject cross-scan history"
// section to a successful claim_next_hypothesis result. This is the M4
// association consumer: it region-queries same-subject InfoItems across scans,
// then follows relation edges from THIS scan's same-subject item IDs with a
// bounded depth and result budget (relation edges are per-scan; cross-scan
// edge graphs are charter §13 research, so the note names what it covers).
// Reader absence or a fully empty result leaves the output unchanged; a query
// error surfaces as a visible one-line note and never fails the tool.
func (a *Agent) appendCrossScanAssociationNote(b *strings.Builder, h scanctx.Hypothesis) {
	reader := infoitem.DefaultReader()
	if reader == nil {
		return
	}
	subject := strings.TrimSpace(h.Endpoint)
	if subject == "" {
		return
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var scanID string
	if a.scanCtx != nil {
		scanID = a.scanCtx.ID
	}
	scanIDs := a.crossScanScanIDsAllowed()
	if len(scanIDs) == 0 {
		return
	}
	hist, err := fetchCrossScanHistory(ctx, reader, scanIDs, scanID, subject, crossScanRegionLimit)
	if err != nil {
		log.Printf("[ledger] cross-scan history lookup failed for %q: %v", subject, err)
		fmt.Fprintf(b, "\n冷路径查询失败，跨扫描历史暂不可用: %v\n", err)
		return
	}
	if hist.total > 0 {
		fmt.Fprintf(b, "\n%s: %d 条既往观察 (observed=%d inferred=%d)",
			crossScanAssocMarker, hist.total, hist.observed, hist.inferred)
		if len(hist.scans) > 0 {
			fmt.Fprintf(b, ", 扫描: %s", strings.Join(hist.scans, ", "))
		}
		if hist.truncated {
			b.WriteString(", " + crossScanTruncatedNote)
		}
		appendGapNote(b, hist.incomplete, hist.gapReason)
		b.WriteString("\n")
	}
	// Relation edges live per scan (charter §13 keeps cross-scan graphs out of
	// scope), so the traversal roots are THIS scan's same-subject items. With
	// none projected yet there is nothing to traverse and the section is
	// honestly omitted.
	if scanID == "" || len(hist.currentIDs) == 0 {
		return
	}
	assoc, err := reader.Associate(ctx, infoitem.AssociationQuery{
		ScanID:   scanID,
		StartIDs: hist.currentIDs,
		MaxDepth: crossScanAssocMaxDepth,
		Limit:    crossScanAssocLimit,
	})
	if err != nil {
		log.Printf("[ledger] cross-scan association query failed for %q: %v", subject, err)
		fmt.Fprintf(b, "当前扫描内关联查询失败: %v\n", err)
		return
	}
	if len(assoc.Hits) == 0 && !assoc.Truncated && !assoc.Incomplete {
		return
	}
	fmt.Fprintf(b, "%s: %d 个 (沿边 ≤%d 跳)", crossScanAssocReachMarker, len(assoc.Hits), crossScanAssocMaxDepth)
	assocScans := make([]string, 0, len(assoc.Hits))
	seen := make(map[string]bool)
	for _, hit := range assoc.Hits {
		if hit.Item.ScanID != "" && !seen[hit.Item.ScanID] {
			seen[hit.Item.ScanID] = true
			assocScans = append(assocScans, hit.Item.ScanID)
		}
	}
	if len(assocScans) > 0 {
		fmt.Fprintf(b, ", 扫描: %s", strings.Join(assocScans, ", "))
	}
	if assoc.Truncated {
		b.WriteString(", " + crossScanTruncatedNote)
	}
	appendGapNote(b, assoc.Incomplete, assoc.GapReason)
	b.WriteString("\n")
}
