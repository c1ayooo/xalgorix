package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/infoitem"
	"github.com/xalgord/xalgorix/v4/internal/tools"
)

func (a *Agent) registerInfoTools(reg *tools.Registry) {
	reg.Register(&tools.Tool{
		Name: "search_info",
		Description: "Read-only InfoItem activation over the optional PostgreSQL cold path. " +
			"mode=region filters by subject/purpose/nature/time; mode=association follows explicit " +
			"relation edges from ids up to 3 hops; mode=context returns a fixed-dimension slice. " +
			"InfoItems are annotated observations/inferences, NOT finding proof: use them to find " +
			"context, never to justify a vulnerability without the normal evidence and report gates. " +
			"When the cold path is disabled this returns an explanatory result; use read_ledger instead.",
		Parameters: []tools.Parameter{
			{Name: "mode", Description: "One of: region, association, context.", Required: true},
			{Name: "subject", Description: "Optional exact subject identifier (target/endpoint).", Required: false},
			{Name: "subject_kind", Description: "Optional subject kind (endpoint, host, account, ...).", Required: false},
			{Name: "purpose", Description: "Optional exact purpose label.", Required: false},
			{Name: "nature", Description: "Optional observed or inferred.", Required: false},
			{Name: "context", Description: "Optional JSON object of exact context key/value filters.", Required: false},
			{Name: "ids", Description: "Association start InfoItem IDs as JSON array or comma-separated list.", Required: false},
			{Name: "edge_types", Description: "Optional association edge types: supports, contradicts, references, same_session, same_subject.", Required: false},
			{Name: "max_depth", Description: "Association max depth, 1-3 (default 2).", Required: false},
			{Name: "limit", Description: "Max results, 1-200 (default 20).", Required: false},
			{Name: "since", Description: "Optional RFC3339 lower bound on observed_at.", Required: false},
			{Name: "until", Description: "Optional RFC3339 upper bound on observed_at.", Required: false},
		},
		Execute: a.searchInfoTool,
	})
}

func (a *Agent) searchInfoTool(args map[string]string) (tools.Result, error) {
	reader := infoitem.DefaultReader()
	if a.scanCtx == nil || strings.TrimSpace(a.scanCtx.ID) == "" {
		return tools.Result{Error: "info search requires a scan context"}, nil
	}
	if reader == nil {
		return tools.Result{Output: "Cross-scan InfoItem retrieval is disabled because the PostgreSQL cold path is not configured. Use read_ledger for the local hypothesis/evidence context; scanning and reporting are unaffected."}, nil
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
	scanID := a.scanCtx.ID
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}

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
