package graph

// infospace.go — the graph authority's read-only implementation of the
// unified information space (IG1).
//
// The exploration graph is the aggregate information layer; info_items is the
// atomic observation layer. The SQL reads the IG0 views and never mutates
// state, takes a lease, or bypasses the graph authority. `run_id` is the
// canonical key: a run may span sibling ScanContexts, whose atoms are joined
// back to the run through run_journal.
//
// Bounds are part of the contract: region/context limit <=200 with keyset
// paging, association depth <=3, result caps, and explicit truncation. A cold
// path gap is surfaced as Incomplete/GapReason instead of being reported as an
// empty result.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/infospace"
)

const infoSpaceTimeout = 10 * time.Second

// Info-link roles. The DB CHECK constraint on graph_info_links admits exactly
// these values; the read view projects every role as the `materializes`
// aggregate→atom relation.
const (
	InfoLinkRoleProvenance   = "provenance"
	InfoLinkRoleSupports     = "supports"
	InfoLinkRoleContradicts  = "contradicts"
	InfoLinkRoleMaterializes = "materializes"
)

// InfoLinkWriter is the optional aggregate↔atom link write surface (IG3).
// Projectors type-assert it so a backend without link support (memory store,
// offline mode) is unaffected: links are a read model, never state authority.
type InfoLinkWriter interface {
	UpsertInfoLink(ctx context.Context, runID string, nodeID int64, infoItemID, role string) error
}

// UpsertInfoLink records that the aggregate node (runID, nodeID) is backed by
// the InfoItem atom infoItemID in the given role. Idempotent on
// (run_id, node_id, info_item_id, role), the same key the 9002 backfill uses,
// so re-projection and the backfill cannot duplicate rows. An empty role
// defaults to "provenance".
func (s *PGStore) UpsertInfoLink(ctx context.Context, runID string, nodeID int64, infoItemID, role string) error {
	if !s.infoSpaceAvailable() {
		return errors.New("graph: information space unavailable")
	}
	runID = strings.TrimSpace(runID)
	infoItemID = strings.TrimSpace(infoItemID)
	role = strings.ToLower(strings.TrimSpace(role))
	if runID == "" {
		return errors.New("graph: info link requires a run_id")
	}
	if nodeID <= 0 {
		return errors.New("graph: info link requires a positive node_id")
	}
	if infoItemID == "" {
		return errors.New("graph: info link requires an info_item_id")
	}
	if role == "" {
		role = InfoLinkRoleProvenance
	}
	switch role {
	case InfoLinkRoleProvenance, InfoLinkRoleSupports, InfoLinkRoleContradicts, InfoLinkRoleMaterializes:
	default:
		return fmt.Errorf("graph: unknown info link role %q", role)
	}
	ctx, cancel := context.WithTimeout(ctx, infoSpaceTimeout)
	defer cancel()
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO graph_info_links (run_id, node_id, info_item_id, role)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (run_id, node_id, info_item_id, role) DO NOTHING`,
		runID, nodeID, infoItemID, role); err != nil {
		return fmt.Errorf("graph: upsert info link: %w", err)
	}
	return nil
}

func (s *PGStore) infoSpaceAvailable() bool {
	return s != nil && s.pool != nil
}

// Region implements infospace.Reader.
func (s *PGStore) Region(ctx context.Context, q infospace.RegionQuery) (infospace.RegionResult, error) {
	if !s.infoSpaceAvailable() {
		return infospace.RegionResult{}, errors.New("graph: information space unavailable")
	}
	q.Normalize()
	if err := q.Validate(); err != nil {
		return infospace.RegionResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, infoSpaceTimeout)
	defer cancel()

	kinds := q.Kinds
	if len(kinds) == 0 {
		// Keep an empty (non-NULL) array: cardinality(NULL) makes the whole
		// OR condition NULL, silently filtering every row.
		kinds = []string{}
	}
	contextJSON, err := marshalContext(q.ContextEquals)
	if err != nil {
		return infospace.RegionResult{}, err
	}
	var cursorAt *time.Time
	cursorID := ""
	if q.Cursor != nil {
		at := q.Cursor.ObservedAt
		cursorAt = &at
		cursorID = q.Cursor.ID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT scan_id, run_id, info_id, layer, kind, state, unit_key, priority, origin,
		       subject, subject_kind, purpose, nature, observed_at, context, payload, created_at
		FROM info_space_items
		WHERE ($2 OR run_id = ANY($1::text[]))
		  AND ($3 = '' OR layer = $3)
		  AND (cardinality($4::text[]) = 0 OR kind = ANY($4::text[]))
		  AND ($5 = '' OR state = $5)
		  AND ($6 = '' OR subject = $6)
		  AND ($7 = '' OR subject_kind = $7)
		  AND ($8 = '' OR purpose = $8)
		  AND ($9 = '' OR nature = $9)
		  AND (layer = 'aggregate' OR nature = 'observed'
		       OR (jsonb_typeof(payload->'supports') = 'array'
		           AND jsonb_array_length(payload->'supports') > 0))
		  AND ($10::jsonb = '{}'::jsonb OR context @> $10::jsonb)
		  AND ($11::timestamptz IS NULL OR observed_at >= $11)
		  AND ($12::timestamptz IS NULL OR observed_at < $12)
		  AND ($13::timestamptz IS NULL OR observed_at < $13 OR (observed_at = $13 AND info_id > $14))
		ORDER BY observed_at DESC, info_id ASC
		LIMIT $15`,
		q.RunIDs, q.AllRuns, string(q.Layer), kinds, q.State, q.Subject, q.SubjectKind,
		q.Purpose, string(q.Nature), contextJSON, q.ObservedAfter, q.ObservedBefore,
		cursorAt, cursorID, q.Limit+1,
	)
	if err != nil {
		return infospace.RegionResult{}, fmt.Errorf("graph: region query: %w", err)
	}
	items, err := scanInfoSpaceItems(rows)
	if err != nil {
		return infospace.RegionResult{}, err
	}
	res := infospace.RegionResult{Items: items}
	if len(items) > q.Limit {
		res.Truncated = true
		res.HasMore = true
		res.Items = items[:q.Limit]
		last := res.Items[len(res.Items)-1]
		res.NextCursor = infoitem.RegionCursor{ObservedAt: last.ObservedAt, ID: last.InfoID}.String()
	}
	res.Incomplete, res.GapReason = s.infoSpaceGap(ctx, q.RunIDs, q.AllRuns, q.Layer)
	return res, nil
}

// heatmapRankedSQL aggregates the unified view into (subject, info type)
// cells. The classification is the decision-facing loop: atoms split by nature,
// aggregates follow belief → action (intent) → evidence (fact) → verdict
// (finding). goal/hint/digest are deliberately excluded (workflow objects, not
// information about the target). Rows are capped by distinct subject, never by
// raw item count.
const heatmapRankedSQL = `
WITH classified AS (
    SELECT subject, subject_kind, observed_at,
           CASE
             WHEN layer = 'atom' AND nature = 'observed' THEN 'observation'
             WHEN layer = 'atom' THEN 'inference'
             WHEN layer = 'aggregate' AND kind IN ('belief','intent','fact','finding') THEN kind
           END AS info_type
    FROM info_space_items
    WHERE ($2 OR run_id = ANY($1::text[]))
      AND ($3 = '' OR subject = $3)
      AND ($4 = '' OR subject_kind = $4)
      AND ($5 = '' OR purpose = $5)
      AND ($8::timestamptz IS NULL OR observed_at >= $8)
      AND ($9::timestamptz IS NULL OR observed_at < $9)
      AND ($10 = '' OR position(lower($10) in lower(subject)) > 0)
      -- Same activation rule as Region: an inferred atom without supports is
      -- stored but cannot be activated, so it stays off the map.
      AND (layer = 'aggregate' OR nature = 'observed'
           OR (jsonb_typeof(payload->'supports') = 'array'
               AND jsonb_array_length(payload->'supports') > 0))
), typed AS (
    SELECT subject, subject_kind, observed_at, info_type
    FROM classified
    WHERE info_type = ANY($6::text[])
), ranked AS (
    SELECT subject, subject_kind, count(*) AS total, max(observed_at) AS last_observed
    FROM typed
    WHERE subject <> ''
    GROUP BY subject, subject_kind
    ORDER BY total DESC, subject ASC, subject_kind ASC
    LIMIT $7
)
SELECT r.subject, r.subject_kind, r.total, r.last_observed, c.info_type, count(*) AS n
FROM ranked r
JOIN typed c
  ON c.subject = r.subject AND c.subject_kind = r.subject_kind
GROUP BY r.subject, r.subject_kind, r.total, r.last_observed, c.info_type
ORDER BY r.total DESC, r.subject ASC, r.subject_kind ASC, c.info_type ASC`

// heatmapTotalsSQL shares the classification/filter with heatmapRankedSQL but
// answers the two numbers the capped map cannot: total distinct subjects
// (truncation disclosure) and unlabeled items (cannot be placed on the map).
const heatmapTotalsSQL = `
WITH classified AS (
    SELECT subject, subject_kind, observed_at,
           CASE
             WHEN layer = 'atom' AND nature = 'observed' THEN 'observation'
             WHEN layer = 'atom' THEN 'inference'
             WHEN layer = 'aggregate' AND kind IN ('belief','intent','fact','finding') THEN kind
           END AS info_type
    FROM info_space_items
    WHERE ($2 OR run_id = ANY($1::text[]))
      AND ($3 = '' OR subject = $3)
      AND ($4 = '' OR subject_kind = $4)
      AND ($5 = '' OR purpose = $5)
      AND ($7::timestamptz IS NULL OR observed_at >= $7)
      AND ($8::timestamptz IS NULL OR observed_at < $8)
      AND ($9 = '' OR position(lower($9) in lower(subject)) > 0)
      AND (layer = 'aggregate' OR nature = 'observed'
           OR (jsonb_typeof(payload->'supports') = 'array'
               AND jsonb_array_length(payload->'supports') > 0))
), typed AS (
    SELECT subject, subject_kind, observed_at, info_type
    FROM classified
    WHERE info_type = ANY($6::text[])
)
SELECT
    count(*) FILTER (WHERE subject = '')::bigint AS unlabeled,
    count(DISTINCT (subject, subject_kind)) FILTER (WHERE subject <> '')::bigint AS subject_total
FROM typed`

// Heatmap implements infospace.HeatmapReader. It is the aggregation form of
// Region: counts only, bounded by subject rows, deterministic ordering, and
// the same cold-path gap disclosure.
func (s *PGStore) Heatmap(ctx context.Context, q infospace.HeatmapQuery) (infospace.HeatmapResult, error) {
	if !s.infoSpaceAvailable() {
		return infospace.HeatmapResult{}, errors.New("graph: information space unavailable")
	}
	q.Normalize()
	if err := q.Validate(); err != nil {
		return infospace.HeatmapResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, infoSpaceTimeout)
	defer cancel()

	types := make([]string, len(q.Types))
	for i, t := range q.Types {
		types[i] = string(t)
	}
	rows, err := s.pool.Query(ctx, heatmapRankedSQL,
		q.RunIDs, q.AllRuns, q.Subject, q.SubjectKind, q.Purpose, types,
		q.RowLimit, q.ObservedAfter, q.ObservedBefore, q.Query,
	)
	if err != nil {
		return infospace.HeatmapResult{}, fmt.Errorf("graph: heatmap query: %w", err)
	}
	res := infospace.HeatmapResult{Types: q.Types, Rows: []infospace.HeatmapRow{}}
	index := make(map[string]int, q.RowLimit)
	for rows.Next() {
		var subject, subjectKind, infoType string
		var total, n int64
		var last pgtype.Timestamptz
		if err := rows.Scan(&subject, &subjectKind, &total, &last, &infoType, &n); err != nil {
			rows.Close()
			return infospace.HeatmapResult{}, fmt.Errorf("graph: scan heatmap cell: %w", err)
		}
		var lastAt *time.Time
		if last.Valid {
			at := last.Time
			lastAt = &at
		}
		key := subjectKind + "\x00" + subject
		i, ok := index[key]
		if !ok {
			i = len(res.Rows)
			index[key] = i
			counts := make(map[infospace.InfoType]int, len(q.Types))
			for _, t := range q.Types {
				counts[t] = 0
			}
			res.Rows = append(res.Rows, infospace.HeatmapRow{
				Subject:      subject,
				SubjectKind:  subjectKind,
				Total:        int(total),
				Counts:       counts,
				LastObserved: lastAt,
			})
		}
		res.Rows[i].Counts[infospace.InfoType(infoType)] = int(n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return infospace.HeatmapResult{}, fmt.Errorf("graph: heatmap rows: %w", err)
	}

	var unlabeled, subjectTotal int64
	if err := s.pool.QueryRow(ctx, heatmapTotalsSQL,
		q.RunIDs, q.AllRuns, q.Subject, q.SubjectKind, q.Purpose, types,
		q.ObservedAfter, q.ObservedBefore, q.Query,
	).Scan(&unlabeled, &subjectTotal); err != nil {
		return infospace.HeatmapResult{}, fmt.Errorf("graph: heatmap totals: %w", err)
	}
	res.Unlabeled = int(unlabeled)
	res.SubjectTotal = int(subjectTotal)
	res.Truncated = res.SubjectTotal > len(res.Rows)
	res.Incomplete, res.GapReason = s.infoSpaceGap(ctx, q.RunIDs, q.AllRuns, "")
	return res, nil
}

const relationsSQL = `
WITH items AS (
    SELECT run_id, info_id, subject, subject_kind, observed_at,
           COALESCE(NULLIF(payload->>'title', ''), NULLIF(payload->>'summary', ''),
                    NULLIF(left(COALESCE(payload->'content'->>'text', ''), 160), ''),
                    subject) AS summary
    FROM info_space_items
    WHERE ($2 OR run_id = ANY($1::text[]))
      AND ($3::timestamptz IS NULL OR observed_at >= $3)
      AND ($4::timestamptz IS NULL OR observed_at < $4)
      AND (layer = 'aggregate' OR nature = 'observed'
           OR (jsonb_typeof(payload->'supports') = 'array'
               AND jsonb_array_length(payload->'supports') > 0))
)
SELECT s.subject, s.subject_kind, d.subject, d.subject_kind, e.rel,
       count(*) AS n,
       (array_agg(s.summary ORDER BY s.observed_at DESC))[1] AS src_title,
       (array_agg(d.summary ORDER BY d.observed_at DESC))[1] AS dst_title,
       max(GREATEST(s.observed_at, d.observed_at)) AS last_at
FROM info_space_edges e
JOIN items s ON s.run_id = e.run_id AND s.info_id = e.src_info_id
JOIN items d ON d.run_id = e.run_id AND d.info_id = e.dst_info_id
WHERE s.subject <> '' AND d.subject <> ''
  AND (s.subject <> d.subject OR s.subject_kind <> d.subject_kind)
GROUP BY s.subject, s.subject_kind, d.subject, d.subject_kind, e.rel
ORDER BY (CASE WHEN e.rel IN ('references','contradicts','same_session','same_subject','materializes')
               THEN 1 ELSE 0 END),
         count(*) DESC, s.subject ASC, d.subject ASC, e.rel ASC
LIMIT $5`

// Relations implements infospace.RelationsReader: subject-level relation
// aggregates for the project graph. Chain edges sort before information
// associations so a capped answer still carries the attack paths.
func (s *PGStore) Relations(ctx context.Context, q infospace.RelationsQuery) (infospace.RelationsResult, error) {
	if !s.infoSpaceAvailable() {
		return infospace.RelationsResult{}, errors.New("graph: information space unavailable")
	}
	q.Normalize()
	if err := q.Validate(); err != nil {
		return infospace.RelationsResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, infoSpaceTimeout)
	defer cancel()

	rows, err := s.pool.Query(ctx, relationsSQL,
		q.RunIDs, q.AllRuns, q.ObservedAfter, q.ObservedBefore, q.Limit+1,
	)
	if err != nil {
		return infospace.RelationsResult{}, fmt.Errorf("graph: relations query: %w", err)
	}
	defer rows.Close()

	res := infospace.RelationsResult{Edges: []infospace.RelationEdge{}}
	for rows.Next() {
		var edge infospace.RelationEdge
		var count int64
		var last pgtype.Timestamptz
		if err := rows.Scan(
			&edge.SrcSubject, &edge.SrcKind, &edge.DstSubject, &edge.DstKind,
			&edge.Rel, &count, &edge.SrcTitle, &edge.DstTitle, &last,
		); err != nil {
			return infospace.RelationsResult{}, fmt.Errorf("graph: scan relation: %w", err)
		}
		edge.Count = int(count)
		edge.Category = infospace.RelationCategoryOf(edge.Rel)
		if last.Valid {
			at := last.Time
			edge.LastAt = &at
		}
		res.Edges = append(res.Edges, edge)
	}
	if err := rows.Err(); err != nil {
		return infospace.RelationsResult{}, fmt.Errorf("graph: relations rows: %w", err)
	}
	if len(res.Edges) > q.Limit {
		res.Truncated = true
		res.Edges = res.Edges[:q.Limit]
	}
	res.Incomplete, res.GapReason = s.infoSpaceGap(ctx, q.RunIDs, q.AllRuns, "")
	return res, nil
}

// Associate implements infospace.Reader.
func (s *PGStore) Associate(ctx context.Context, q infospace.AssociationQuery) (infospace.AssociationResult, error) {
	if !s.infoSpaceAvailable() {
		return infospace.AssociationResult{}, errors.New("graph: information space unavailable")
	}
	q.Normalize()
	if err := q.Validate(); err != nil {
		return infospace.AssociationResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, infoSpaceTimeout)
	defer cancel()

	edgeTypes := q.EdgeTypes
	if len(edgeTypes) == 0 {
		edgeTypes = []string{}
	}
	rows, err := s.pool.Query(ctx, `
		WITH RECURSIVE walk(start_id, node_id, depth, path, rels) AS (
			SELECT t.id, t.id, 0, ARRAY[t.id]::text[], ARRAY[]::text[]
			FROM unnest($2::text[]) AS t(id)
			UNION ALL
			SELECT w.start_id,
			       nxt.next_id,
			       w.depth + 1,
			       w.path || nxt.next_id,
			       w.rels || e.rel
			FROM walk w
			JOIN info_space_edges e
			  ON e.run_id = $1
			 AND (e.src_info_id = w.node_id OR e.dst_info_id = w.node_id)
			CROSS JOIN LATERAL (
				SELECT CASE WHEN e.src_info_id = w.node_id THEN e.dst_info_id ELSE e.src_info_id END AS next_id
			) nxt
			JOIN info_space_items ni
			  ON ni.run_id = $1 AND ni.info_id = nxt.next_id
			WHERE w.depth < $3
			  AND NOT (nxt.next_id = ANY(w.path))
			  AND (cardinality($4::text[]) = 0 OR e.rel = ANY($4::text[]))
			  -- Observed atoms and substantiated inferred atoms can be
			  -- activated; aggregate nodes are authoritative graph objects.
			  AND (ni.layer = 'aggregate' OR ni.nature = 'observed'
			       OR (jsonb_typeof(ni.payload->'supports') = 'array'
			           AND jsonb_array_length(ni.payload->'supports') > 0))
		)
		SELECT DISTINCT ON (node_id) node_id, path, depth, rels
		FROM walk
		WHERE depth > 0
		ORDER BY node_id, depth, path
		LIMIT $5`,
		q.RunID, q.StartIDs, q.MaxDepth, edgeTypes, q.Limit+1,
	)
	if err != nil {
		return infospace.AssociationResult{}, fmt.Errorf("graph: association query: %w", err)
	}
	type hitPath struct {
		id    string
		path  []string
		depth int
		rels  []string
	}
	var paths []hitPath
	for rows.Next() {
		var h hitPath
		if err := rows.Scan(&h.id, &h.path, &h.depth, &h.rels); err != nil {
			rows.Close()
			return infospace.AssociationResult{}, err
		}
		paths = append(paths, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return infospace.AssociationResult{}, err
	}
	rows.Close()

	truncated := len(paths) > q.Limit
	if truncated {
		paths = paths[:q.Limit]
	}
	ids := make([]string, 0, len(paths))
	for _, p := range paths {
		ids = append(ids, p.id)
	}
	itemsByID, err := s.infoSpaceItemsByIDs(ctx, q.RunID, ids)
	if err != nil {
		return infospace.AssociationResult{}, err
	}
	hits := make([]infospace.AssociationHit, 0, len(paths))
	for _, p := range paths {
		item, ok := itemsByID[p.id]
		if !ok {
			continue
		}
		hits = append(hits, infospace.AssociationHit{
			Item: item, Path: p.path, Rels: p.rels, Depth: p.depth,
		})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Depth != hits[j].Depth {
			return hits[i].Depth < hits[j].Depth
		}
		return strings.Join(hits[i].Path, "\x00") < strings.Join(hits[j].Path, "\x00")
	})
	incomplete, reason := s.infoSpaceGap(ctx, []string{q.RunID}, false, "")
	return infospace.AssociationResult{
		Hits: hits, Truncated: truncated, Incomplete: incomplete, GapReason: reason,
	}, nil
}

// ContextSlice implements infospace.Reader.
func (s *PGStore) ContextSlice(ctx context.Context, q infospace.ContextQuery) (infospace.ContextResult, error) {
	if !s.infoSpaceAvailable() {
		return infospace.ContextResult{}, errors.New("graph: information space unavailable")
	}
	q.Normalize()
	if err := q.Validate(); err != nil {
		return infospace.ContextResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, infoSpaceTimeout)
	defer cancel()

	contextJSON, err := marshalContext(q.ContextEquals)
	if err != nil {
		return infospace.ContextResult{}, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT scan_id, run_id, info_id, layer, kind, state, unit_key, priority, origin,
		       subject, subject_kind, purpose, nature, observed_at, context, payload, created_at
		FROM info_space_items
		WHERE run_id = $1
		  AND ($2 = '' OR subject = $2)
		  AND ($3 = '' OR subject_kind = $3)
		  AND ($4 = '' OR layer = $4)
		  AND (layer = 'aggregate' OR nature = 'observed'
		       OR (jsonb_typeof(payload->'supports') = 'array'
		           AND jsonb_array_length(payload->'supports') > 0))
		  AND ($5::jsonb = '{}'::jsonb OR context @> $5::jsonb)
		  AND ($6::timestamptz IS NULL OR observed_at >= $6)
		  AND ($7::timestamptz IS NULL OR observed_at < $7)
		ORDER BY observed_at DESC, info_id ASC
		LIMIT $8`,
		q.RunID, q.Subject, q.SubjectKind, string(q.Layer), contextJSON,
		q.Since, q.Until, q.Limit+1,
	)
	if err != nil {
		return infospace.ContextResult{}, fmt.Errorf("graph: context query: %w", err)
	}
	items, err := scanInfoSpaceItems(rows)
	if err != nil {
		return infospace.ContextResult{}, err
	}
	res := infospace.ContextResult{Items: items}
	if len(items) > q.Limit {
		res.Truncated = true
		res.Items = items[:q.Limit]
	}
	res.Incomplete, res.GapReason = s.infoSpaceGap(ctx, []string{q.RunID}, false, q.Layer)
	return res, nil
}

func (s *PGStore) infoSpaceItemsByIDs(ctx context.Context, runID string, ids []string) (map[string]infospace.Item, error) {
	if len(ids) == 0 {
		return map[string]infospace.Item{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT scan_id, run_id, info_id, layer, kind, state, unit_key, priority, origin,
		       subject, subject_kind, purpose, nature, observed_at, context, payload, created_at
		FROM info_space_items
		WHERE run_id = $1 AND info_id = ANY($2::text[])`, runID, ids)
	if err != nil {
		return nil, fmt.Errorf("graph: info space items by ids: %w", err)
	}
	items, err := scanInfoSpaceItems(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[string]infospace.Item, len(items))
	for _, item := range items {
		out[item.InfoID] = item
	}
	return out, nil
}

func scanInfoSpaceItems(rows pgx.Rows) ([]infospace.Item, error) {
	defer rows.Close()
	var out []infospace.Item
	for rows.Next() {
		var (
			item       infospace.Item
			layer      string
			contextRaw []byte
			payloadRaw []byte
		)
		if err := rows.Scan(
			&item.ScanID, &item.RunID, &item.InfoID, &layer, &item.Kind, &item.State,
			&item.UnitKey, &item.Priority, &item.Origin, &item.Subject, &item.SubjectKind,
			&item.Purpose, &item.Nature, &item.ObservedAt, &contextRaw, &payloadRaw, &item.CreatedAt,
		); err != nil {
			return nil, err
		}
		item.Layer = infospace.Layer(layer)
		item.Context = contextRaw
		item.Payload = payloadRaw
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func marshalContext(values map[string]string) (string, error) {
	if len(values) == 0 {
		return "{}", nil
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("infospace: encode context filter: %w", err)
	}
	return string(data), nil
}

// infoSpaceGap reports a cold-path projection gap for the queried run(s). The
// aggregate layer is graph-authoritative, so an aggregate-only query is never
// marked incomplete. Atoms live under ScanContext ids; run_journal maps them
// back to the run.
func (s *PGStore) infoSpaceGap(ctx context.Context, runIDs []string, allRuns bool, layer infospace.Layer) (bool, string) {
	if layer == infospace.LayerAggregate {
		return false, ""
	}
	var status, lastErr string
	err := s.pool.QueryRow(ctx, `
		SELECT w.status, w.last_error
		FROM info_sync_watermarks w
		WHERE w.status IN ('gap','failed')
		  AND ($1
		       OR w.scan_id = ANY($2::text[])
		       OR EXISTS (
		            SELECT 1 FROM run_journal j
		            WHERE j.scan_id = w.scan_id AND j.instance_id = ANY($2::text[])
		       ))
		ORDER BY w.updated_at DESC
		LIMIT 1`, allRuns, runIDs).Scan(&status, &lastErr)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ""
	}
	if err != nil {
		// A watermark read failure must not masquerade as a complete result.
		return true, "cold-path watermark unavailable: " + err.Error()
	}
	if strings.TrimSpace(lastErr) == "" {
		return true, "cold path " + status
	}
	return true, "cold path " + status + ": " + lastErr
}
