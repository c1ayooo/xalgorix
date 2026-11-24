package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/infospace"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

func (a *Agent) registerInfoTools(reg *tools.Registry) {
	reg.Register(&tools.Tool{
		Name: "search_info",
		Description: "Read-only activation over the unified information space. " +
			"mode=region filters by subject/purpose/nature/time; mode=association follows explicit " +
			"relation edges from ids up to 3 hops; mode=context returns a fixed-dimension slice. " +
			"source=auto (default) reads the exploration graph aggregates + InfoItem atoms when the " +
			"graph authority is available, and falls back to the cold-path InfoItem reader otherwise. " +
			"layer=aggregate|atom narrows the result. Results are annotated context, NOT finding proof: " +
			"never justify a vulnerability without the normal evidence and report gates.",
		Parameters: []tools.Parameter{
			{Name: "mode", Description: "One of: region, association, context.", Required: true},
			{Name: "source", Description: "Optional auto (default), graph, or info (cold path only).", Required: false},
			{Name: "layer", Description: "Optional aggregate, atom, or all (default).", Required: false},
			{Name: "subject", Description: "Optional exact subject identifier (target/endpoint).", Required: false},
			{Name: "subject_kind", Description: "Optional subject kind (endpoint, host, account, ...).", Required: false},
			{Name: "purpose", Description: "Optional exact purpose label.", Required: false},
			{Name: "nature", Description: "Optional observed or inferred.", Required: false},
			{Name: "context", Description: "Optional JSON object of exact context key/value filters.", Required: false},
			{Name: "ids", Description: "Association start InfoItem IDs as JSON array or comma-separated list.", Required: false},
			{Name: "edge_types", Description: "Optional association edge types: supports, contradicts, references, same_session, same_subject, spawns, derived_from, yields, proves, covers, depends_on.", Required: false},
			{Name: "max_depth", Description: "Association max depth, 1-3 (default 2).", Required: false},
			{Name: "limit", Description: "Max results, 1-200 (default 20).", Required: false},
			{Name: "since", Description: "Optional RFC3339 lower bound on observed_at.", Required: false},
			{Name: "until", Description: "Optional RFC3339 upper bound on observed_at.", Required: false},
		},
		Execute: a.searchInfoTool,
	})
}

func (a *Agent) searchInfoTool(args map[string]string) (tools.Result, error) {
	if a.scanCtx == nil || strings.TrimSpace(a.scanCtx.ID) == "" {
		return tools.Result{Error: "info search requires a scan context"}, nil
	}
	mode := strings.ToLower(strings.TrimSpace(args["mode"]))
	limit, err := parseInfoLimit(args["limit"])
	if err != nil {
		return tools.Result{Error: err.Error()}, nil
	}
	contextFilter, err := parseContextFilter(args["context"])
	if err != nil {
		return tools.Result{Error: err.Error()}, nil
	}
	nature := infoitem.Nature(strings.ToLower(strings.TrimSpace(args["nature"])))
	if nature != "" && !nature.Valid() {
		return tools.Result{Error: "nature must be observed or inferred"}, nil
	}
	since, err := parseInfoTime(args["since"])
	if err != nil {
		return tools.Result{Error: "since: " + err.Error()}, nil
	}
	until, err := parseInfoTime(args["until"])
	if err != nil {
		return tools.Result{Error: "until: " + err.Error()}, nil
	}
	source := strings.ToLower(strings.TrimSpace(args["source"]))
	if source == "" {
		source = "auto"
	}
	layer := infospace.Layer(strings.ToLower(strings.TrimSpace(args["layer"])))
	if layer == "all" {
		layer = ""
	}
	if !layer.Valid() {
		return tools.Result{Error: "layer must be aggregate, atom, or all"}, nil
	}
	if source == "auto" || source == "graph" {
		if reader, ok := graph.Backend().(infospace.Reader); ok && reader != nil {
			return a.searchInfoGraph(reader, mode, layer, args, limit, contextFilter, nature, since, until)
		}
		if source == "graph" {
			return tools.Result{Output: "Unified information space is disabled because the graph authority is not configured. Use read_ledger for the local hypothesis/evidence context; scanning and reporting are unaffected."}, nil
		}
	}
	reader := infoitem.DefaultReader()
	if reader == nil {
		return tools.Result{Output: "Cross-scan InfoItem retrieval is disabled because the PostgreSQL cold path is not configured. Use read_ledger for the local hypothesis/evidence context; scanning and reporting are unaffected."}, nil
	}
	scanID := a.scanCtx.ID
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	// R3: record the query itself (mode + refs) before execution. Result-level
	// truncation/incomplete flags are carried by the tool output; the trace
	// records that the read happened and against which IDs.
	a.recordInfoRead("search_info", mode, parsePreconditions(args["ids"]), true, false, false,
		"subject="+strings.TrimSpace(args["subject"])+" purpose="+strings.TrimSpace(args["purpose"]))

	switch mode {
	case "region":
		q := infoitem.RegionQuery{
			ScanIDs:        []string{scanID},
			Subject:        strings.TrimSpace(args["subject"]),
			SubjectKind:    strings.TrimSpace(args["subject_kind"]),
			Purpose:        strings.TrimSpace(args["purpose"]),
			Nature:         nature,
			ContextEquals:  contextFilter,
			ObservedAfter:  since,
			ObservedBefore: until,
			Limit:          limit,
		}
		q.Normalize()
		if err := q.Validate(); err != nil {
			return tools.Result{Error: err.Error()}, nil
		}
		res, err := reader.Region(ctx, q)
		if err != nil {
			return tools.Result{Error: "region activation failed: " + err.Error()}, nil
		}
		return infoResult("region", res.Items, res.Truncated, res.Incomplete, res.GapReason, nil), nil

	case "association":
		ids := parsePreconditions(args["ids"])
		if len(ids) == 0 {
			return tools.Result{Error: "association mode requires ids"}, nil
		}
		depth := 2
		if raw := strings.TrimSpace(args["max_depth"]); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > infoitem.MaxAssociationDepth {
				return tools.Result{Error: fmt.Sprintf("max_depth must be 1-%d", infoitem.MaxAssociationDepth)}, nil
			}
			depth = n
		}
		var edgeTypes []infoitem.RelationType
		for _, raw := range parsePreconditions(args["edge_types"]) {
			typ := infoitem.RelationType(strings.ToLower(raw))
			if !typ.Valid() {
				return tools.Result{Error: fmt.Sprintf("unsupported edge type %q", raw)}, nil
			}
			edgeTypes = append(edgeTypes, typ)
		}
		q := infoitem.AssociationQuery{
			ScanID: scanID, StartIDs: ids, EdgeTypes: edgeTypes,
			MaxDepth: depth, Limit: limit,
		}
		q.Normalize()
		if err := q.Validate(); err != nil {
			return tools.Result{Error: err.Error()}, nil
		}
		res, err := reader.Associate(ctx, q)
		if err != nil {
			return tools.Result{Error: "association activation failed: " + err.Error()}, nil
		}
		items := make([]infoitem.Item, 0, len(res.Hits))
		paths := make([][]string, 0, len(res.Hits))
		for _, hit := range res.Hits {
			items = append(items, hit.Item)
			paths = append(paths, hit.Path)
		}
		return infoResult("association", items, res.Truncated, res.Incomplete, res.GapReason, paths), nil

	case "context":
		q := infoitem.ContextQuery{
			ScanID: scanID, Subject: strings.TrimSpace(args["subject"]),
			SubjectKind:   strings.TrimSpace(args["subject_kind"]),
			ContextEquals: contextFilter, Since: since, Until: until, Limit: limit,
		}
		q.Normalize()
		if err := q.Validate(); err != nil {
			return tools.Result{Error: err.Error()}, nil
		}
		res, err := reader.ContextSlice(ctx, q)
		if err != nil {
			return tools.Result{Error: "context activation failed: " + err.Error()}, nil
		}
		return infoResult("context", res.Items, res.Truncated, res.Incomplete, res.GapReason, nil), nil
	default:
		return tools.Result{Error: "mode must be region, association, or context"}, nil
	}
}

func parseInfoLimit(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 20, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 || n > infoitem.MaxQueryLimit {
		return 0, fmt.Errorf("limit must be 1-%d", infoitem.MaxQueryLimit)
	}
	return n, nil
}

func parseContextFilter(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("context must be a JSON object of string values: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func parseInfoTime(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("must be RFC3339: %w", err)
	}
	return &t, nil
}

func infoResult(mode string, items []infoitem.Item, truncated, incomplete bool, gapReason string, paths [][]string) tools.Result {
	var b strings.Builder
	fmt.Fprintf(&b, "InfoItem activation mode=%s results=%d truncated=%t incomplete=%t\n", mode, len(items), truncated, incomplete)
	if gapReason != "" {
		b.WriteString("gap: " + gapReason + "\n")
	}
	for n, item := range items {
		if n < len(paths) && len(paths[n]) > 0 {
			fmt.Fprintf(&b, "path: %s\n", strings.Join(paths[n], " -> "))
		}
		content := truncStr(item.Coordinates.Content.Text, 500)
		fmt.Fprintf(&b, "- %s [%s] subject=%s purpose=%s: %s", item.ID, item.Nature,
			item.Coordinates.Subject.Identifier, item.Coordinates.Purpose, content)
		if len(item.Supports) > 0 {
			fmt.Fprintf(&b, " supports=%s", strings.Join(item.Supports, ","))
		}
		if !item.Substantiated() {
			b.WriteString(" unsubstantiated")
		}
		b.WriteString("\n")
	}
	b.WriteString("Reminder: InfoItems are annotated context, not finding proof. Verdicts remain with the Verifier and report pipeline.")
	return tools.Result{
		Output: b.String(),
		Metadata: map[string]any{
			"mode": mode, "count": len(items), "truncated": truncated, "incomplete": incomplete,
		},
	}
}

// searchInfoGraph serves the unified information space through the graph
// authority. It is the preferred path: aggregates (goal/intent/belief/fact/
// finding/hint/digest) and atoms (InfoItem) are queried together under the run
// id, with the same bounds as the cold-path reader.
func (a *Agent) searchInfoGraph(reader infospace.Reader, mode string, layer infospace.Layer, args map[string]string, limit int, contextFilter map[string]string, nature infoitem.Nature, since, until *time.Time) (tools.Result, error) {
	runID := a.planGraphScanID()
	if runID == "" {
		return tools.Result{Error: "info search requires a run identity"}, nil
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	switch mode {
	case "region":
		q := infospace.RegionQuery{
			RunIDs:         []string{runID},
			Layer:          layer,
			Subject:        strings.TrimSpace(args["subject"]),
			SubjectKind:    strings.TrimSpace(args["subject_kind"]),
			Purpose:        strings.TrimSpace(args["purpose"]),
			Nature:         nature,
			ContextEquals:  contextFilter,
			ObservedAfter:  since,
			ObservedBefore: until,
			Limit:          limit,
		}
		q.Normalize()
		if err := q.Validate(); err != nil {
			return tools.Result{Error: err.Error()}, nil
		}
		res, err := reader.Region(ctx, q)
		if err != nil {
			return tools.Result{Error: "region activation failed: " + err.Error()}, nil
		}
		return infoSpaceResult("region", res.Items, res.Truncated, res.Incomplete, res.GapReason, nil, nil), nil

	case "association":
		ids := parsePreconditions(args["ids"])
		if len(ids) == 0 {
			return tools.Result{Error: "association mode requires ids"}, nil
		}
		depth := infospace.DefaultDepth
		if raw := strings.TrimSpace(args["max_depth"]); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > infospace.MaxDepth {
				return tools.Result{Error: fmt.Sprintf("max_depth must be 1-%d", infospace.MaxDepth)}, nil
			}
			depth = n
		}
		var edgeTypes []string
		for _, raw := range parsePreconditions(args["edge_types"]) {
			rel := strings.ToLower(strings.TrimSpace(raw))
			if !infospace.KnownRelation(rel) {
				return tools.Result{Error: fmt.Sprintf("unsupported edge type %q", raw)}, nil
			}
			edgeTypes = append(edgeTypes, rel)
		}
		q := infospace.AssociationQuery{
			RunID: runID, StartIDs: ids, EdgeTypes: edgeTypes, MaxDepth: depth, Limit: limit,
		}
		q.Normalize()
		if err := q.Validate(); err != nil {
			return tools.Result{Error: err.Error()}, nil
		}
		res, err := reader.Associate(ctx, q)
		if err != nil {
			return tools.Result{Error: "association activation failed: " + err.Error()}, nil
		}
		items := make([]infospace.Item, 0, len(res.Hits))
		paths := make([][]string, 0, len(res.Hits))
		rels := make([][]string, 0, len(res.Hits))
		for _, hit := range res.Hits {
			items = append(items, hit.Item)
			paths = append(paths, hit.Path)
			rels = append(rels, hit.Rels)
		}
		return infoSpaceResult("association", items, res.Truncated, res.Incomplete, res.GapReason, paths, rels), nil

	case "context":
		q := infospace.ContextQuery{
			RunID:         runID,
			Subject:       strings.TrimSpace(args["subject"]),
			SubjectKind:   strings.TrimSpace(args["subject_kind"]),
			Layer:         layer,
			ContextEquals: contextFilter,
			Since:         since,
			Until:         until,
			Limit:         limit,
		}
		q.Normalize()
		if err := q.Validate(); err != nil {
			return tools.Result{Error: err.Error()}, nil
		}
		res, err := reader.ContextSlice(ctx, q)
		if err != nil {
			return tools.Result{Error: "context activation failed: " + err.Error()}, nil
		}
		return infoSpaceResult("context", res.Items, res.Truncated, res.Incomplete, res.GapReason, nil, nil), nil
	default:
		return tools.Result{Error: "mode must be region, association, or context"}, nil
	}
}

func infoSpaceResult(mode string, items []infospace.Item, truncated, incomplete bool, gap string, paths, rels [][]string) tools.Result {
	var b strings.Builder
	fmt.Fprintf(&b, "InfoSpace activation source=graph mode=%s results=%d truncated=%t incomplete=%t\n", mode, len(items), truncated, incomplete)
	if gap != "" {
		b.WriteString("gap: " + gap + "\n")
	}
	for n, item := range items {
		if n < len(paths) && len(paths[n]) > 0 {
			fmt.Fprintf(&b, "path: %s", strings.Join(paths[n], " -> "))
			if n < len(rels) && len(rels[n]) > 0 {
				fmt.Fprintf(&b, " (%s)", strings.Join(rels[n], ", "))
			}
			b.WriteString("\n")
		}
		nature := item.Nature
		if nature == "" {
			nature = "-"
		}
		state := item.State
		if state == "" {
			state = "-"
		}
		fmt.Fprintf(&b, "- %s [%s/%s] kind=%s state=%s subject=%s purpose=%s",
			item.InfoID, item.Layer, nature, item.Kind, state, item.Subject, item.Purpose)
		if summary := infoSpaceSummary(item); summary != "" {
			b.WriteString(" — " + summary)
		}
		b.WriteString("\n")
	}
	b.WriteString("Reminder: the information space is annotated context, not finding proof. Verdicts remain with the Verifier and report pipeline.")
	return tools.Result{
		Output: b.String(),
		Metadata: map[string]any{
			"source": "graph", "mode": mode, "count": len(items),
			"truncated": truncated, "incomplete": incomplete,
		},
	}
}

// infoSpaceSummary renders a bounded one-line index entry; the full body stays
// behind read_info_item / ledger reads.
func infoSpaceSummary(item infospace.Item) string {
	var doc map[string]any
	if len(item.Payload) == 0 || json.Unmarshal(item.Payload, &doc) != nil {
		return ""
	}
	if content, ok := doc["content"].(map[string]any); ok {
		if text, ok := content["text"].(string); ok {
			return truncStr(text, 300)
		}
	}
	for _, key := range []string{"title", "notes", "summary"} {
		if value, ok := doc[key].(string); ok && strings.TrimSpace(value) != "" {
			return truncStr(value, 200)
		}
	}
	return ""
}
