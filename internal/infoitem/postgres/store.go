package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
)

// Store is the PostgreSQL cold-path adapter. It implements infoitem.Reader and
// applies journal records idempotently. It never participates in scan
// scheduling, verification, or finding acceptance.
type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) Pool() *pgxpool.Pool {
	if s == nil {
		return nil
	}
	return s.pool
}

// Watermark describes how far a scan's cold path has been applied.
type Watermark struct {
	ScanID     string    `json:"scan_id"`
	AppliedSeq uint64    `json:"applied_seq"`
	Status     string    `json:"status"`
	LastError  string    `json:"last_error,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (s *Store) Watermark(ctx context.Context, scanID string) (Watermark, error) {
	if s == nil || s.pool == nil {
		return Watermark{}, errors.New("infoitem/postgres: store unavailable")
	}
	var wm Watermark
	err := s.pool.QueryRow(ctx, `
		SELECT scan_id, applied_seq, status, last_error, updated_at
		FROM info_sync_watermarks
		WHERE scan_id = $1`, scanID,
	).Scan(&wm.ScanID, &wm.AppliedSeq, &wm.Status, &wm.LastError, &wm.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Watermark{ScanID: scanID, Status: "pending"}, nil
	}
	if err != nil {
		return Watermark{}, fmt.Errorf("infoitem/postgres: read watermark: %w", err)
	}
	return wm, nil
}

// Apply writes one durable domain record and advances the contiguous
// watermark. Records may be replayed; only a strictly newer source sequence
// can replace an InfoItem.
func (s *Store) Apply(ctx context.Context, rec domainjournal.Record) error {
	if s == nil || s.pool == nil {
		return errors.New("infoitem/postgres: store unavailable")
	}
	if err := rec.Validate(); err != nil {
		return fmt.Errorf("infoitem/postgres: invalid record: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("infoitem/postgres: begin apply: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	payload, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("infoitem/postgres: marshal audit payload: %w", err)
	}
	beforeJSON, err := marshalOptional(rec.Before)
	if err != nil {
		return err
	}
	afterJSON, err := marshalOptional(rec.After)
	if err != nil {
		return err
	}
	actorJSON, err := json.Marshal(rec.Actor)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO info_audit_records
			(scan_id, seq, at, actor, operation, hypothesis_id, evidence_id, summary, before_state, after_state, payload)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (scan_id, seq) DO NOTHING`,
		rec.ScanID, int64(rec.Seq), rec.At, actorJSON, string(rec.Operation),
		rec.HypothesisID, rec.EvidenceID, rec.Summary, beforeJSON, afterJSON, payload,
	); err != nil {
		return fmt.Errorf("infoitem/postgres: write audit record: %w", err)
	}

	if rec.InfoItem != nil {
		if err := upsertItem(ctx, tx, *rec.InfoItem); err != nil {
			return err
		}
	}
	for _, item := range rec.InfoItems {
		if err := upsertItem(ctx, tx, item); err != nil {
			return err
		}
	}
	for _, rel := range rec.Relations {
		if err := upsertRelation(ctx, tx, rel); err != nil {
			return err
		}
	}
	// R8.3 association lighting: deterministic lexical candidates become
	// explicit `references` edges. Best-effort: a recall failure must not fail
	// the core cold-path upsert.
	if err := inferRelationsTx(ctx, tx, rec); err != nil {
		log.Printf("[infoitem] %s seq %d: relation inference skipped: %v", rec.ScanID, rec.Seq, err)
	}
	if err := advanceWatermark(ctx, tx, rec.ScanID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("infoitem/postgres: commit apply: %w", err)
	}
	return nil
}

func upsertItem(ctx context.Context, tx pgx.Tx, item infoitem.Item) error {
	// Rule-based coordinate derivation runs at the persistence boundary so a
	// producer that omits a derivable purpose is stored with a truthful
	// coordinate status instead of a hard-coded "complete".
	item.Normalize()
	content, err := json.Marshal(item.Coordinates.Content)
	if err != nil {
		return fmt.Errorf("infoitem/postgres: marshal content: %w", err)
	}
	coordinates, err := json.Marshal(item.Coordinates)
	if err != nil {
		return fmt.Errorf("infoitem/postgres: marshal coordinates: %w", err)
	}
	source, err := json.Marshal(item.Coordinates.Source)
	if err != nil {
		return fmt.Errorf("infoitem/postgres: marshal source: %w", err)
	}
	subject, err := json.Marshal(item.Coordinates.Subject)
	if err != nil {
		return fmt.Errorf("infoitem/postgres: marshal subject: %w", err)
	}
	itemContext, err := marshalStringMap(item.Coordinates.Context)
	if err != nil {
		return fmt.Errorf("infoitem/postgres: marshal context: %w", err)
	}
	relationIDs, err := marshalStringSlice(item.Coordinates.RelationIDs)
	if err != nil {
		return fmt.Errorf("infoitem/postgres: marshal relation ids: %w", err)
	}
	supports, err := marshalStringSlice(item.Supports)
	if err != nil {
		return fmt.Errorf("infoitem/postgres: marshal supports: %w", err)
	}
	validFrom := optionalTime(item.ValidFrom)
	status := item.CoordinateStatus
	if status == "" {
		status = item.ClassifyCoordinateStatus()
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO info_items
			(id, scan_id, source_seq, nature, content, coordinates, source, subject, purpose, context,
			 relation_ids, supports, confidence, coordinate_status, valid_from, valid_until, observed_at, created_at,
			 instance_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,
			COALESCE((
				SELECT NULLIF(j.instance_id, '')
				FROM run_journal j
				WHERE j.scan_id = $2 AND j.seq = $3
			), ''))
		ON CONFLICT (scan_id, id) DO UPDATE SET
			source_seq = EXCLUDED.source_seq,
			nature = EXCLUDED.nature,
			content = EXCLUDED.content,
			coordinates = EXCLUDED.coordinates,
			source = EXCLUDED.source,
			subject = EXCLUDED.subject,
			purpose = EXCLUDED.purpose,
			context = EXCLUDED.context,
			relation_ids = EXCLUDED.relation_ids,
			supports = EXCLUDED.supports,
			confidence = EXCLUDED.confidence,
			coordinate_status = EXCLUDED.coordinate_status,
			valid_from = EXCLUDED.valid_from,
			valid_until = EXCLUDED.valid_until,
			observed_at = EXCLUDED.observed_at,
			created_at = EXCLUDED.created_at,
			instance_id = COALESCE(NULLIF(EXCLUDED.instance_id, ''), info_items.instance_id)
		WHERE info_items.source_seq <= EXCLUDED.source_seq`,
		item.ID, item.ScanID, int64(item.SourceSeq), string(item.Nature), content, coordinates,
		source, subject, item.Coordinates.Purpose, itemContext, relationIDs, supports,
		item.Confidence, string(status), validFrom, item.ValidUntil, item.Coordinates.ObservedAt, item.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("infoitem/postgres: upsert info item: %w", err)
	}
	return nil
}

func upsertRelation(ctx context.Context, tx pgx.Tx, rel infoitem.Relation) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO info_item_edges (scan_id, source_seq, from_id, to_id, type, weight, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (scan_id, source_seq, from_id, to_id, type) DO NOTHING`,
		rel.ScanID, int64(rel.SourceSeq), rel.FromID, rel.ToID, string(rel.Type), rel.Weight, rel.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("infoitem/postgres: upsert relation: %w", err)
	}
	return nil
}

const (
	// relationRecallCandidates bounds the cold-path lexical comparison window.
	relationRecallCandidates = 50
	// relationRecallMin keeps only clearly similar candidates as edges.
	relationRecallMin = 0.5
	// relationRecallMinRunes avoids inferring edges from placeholder-short
	// text (for example "observed response k-1"), where a few shared tokens
	// are noise rather than evidence of a related observation.
	relationRecallMinRunes = 40
)

// inferRelationsTx writes deterministic lexical `references` candidates for
// the items in one journal record. It is not semantic inference: the score and
// both IDs are stored, so a consumer can re-check and ignore an edge.
func inferRelationsTx(ctx context.Context, tx pgx.Tx, rec domainjournal.Record) error {
	items := make([]infoitem.Item, 0, 1+len(rec.InfoItems))
	if rec.InfoItem != nil {
		items = append(items, *rec.InfoItem)
	}
	items = append(items, rec.InfoItems...)
	if len(items) == 0 {
		return nil
	}
	for n := range items {
		items[n].ScanID = rec.ScanID
		if items[n].Coordinates.ObservedAt.IsZero() {
			items[n].Coordinates.ObservedAt = rec.At
		}
	}
	now := time.Now().UTC()
	for _, item := range items {
		text := strings.TrimSpace(item.Coordinates.Content.Text)
		if len([]rune(text)) < relationRecallMinRunes || item.ID == "" {
			continue
		}
		rows, err := tx.Query(ctx, `
			SELECT id, COALESCE(content->>'text', '')
			FROM info_items
			WHERE scan_id = $1 AND id <> $2
			ORDER BY source_seq DESC, id ASC
			LIMIT $3`,
			rec.ScanID, item.ID, relationRecallCandidates,
		)
		if err != nil {
			return err
		}
		docs := make([]infoitem.Doc, 0, relationRecallCandidates)
		for rows.Next() {
			var id, body string
			if err := rows.Scan(&id, &body); err != nil {
				rows.Close()
				return err
			}
			docs = append(docs, infoitem.Doc{ID: id, Title: id, Summary: body})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, hit := range infoitem.Rank(text, docs, 3) {
			if hit.Score < relationRecallMin {
				continue
			}
			if err := upsertRelation(ctx, tx, infoitem.Relation{
				ScanID:    rec.ScanID,
				SourceSeq: rec.Seq,
				FromID:    item.ID,
				ToID:      hit.ID,
				Type:      infoitem.RelationReferences,
				Weight:    hit.Score,
				CreatedAt: now,
			}); err != nil {
				return err
			}
		}
	}

	// Deterministic structural edges: same subject, same producing session,
	// and control-vs-positive contradictions. The lexical pass above only
	// covers long prose; these edges need no text at all.
	for _, item := range items {
		if item.ID == "" {
			continue
		}
		candidates, err := loadRelationCandidates(ctx, tx, rec.ScanID, item.ID, infoitem.RelationCandidateLimit)
		if err != nil {
			return err
		}
		for _, rel := range infoitem.InferRelations(item, candidates, rec.Seq, now) {
			if err := upsertRelation(ctx, tx, rel); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadRelationCandidates reads the bounded recent window used by structural
// relation inference. It selects only the fields inference compares, never
// content bodies.
func loadRelationCandidates(ctx context.Context, tx pgx.Tx, scanID, itemID string, limit int) ([]infoitem.RelationCandidate, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id,
		       COALESCE(subject->>'kind', ''),
		       COALESCE(subject->>'identifier', ''),
		       COALESCE(purpose, ''),
		       COALESCE(context->>'evidence_kind', ''),
		       COALESCE(source->>'agent_id', ''),
		       COALESCE(context->>'hypothesis', '')
		FROM info_items
		WHERE scan_id = $1 AND id <> $2
		ORDER BY source_seq DESC, id ASC
		LIMIT $3`, scanID, itemID, limit)
	if err != nil {
		return nil, fmt.Errorf("infoitem/postgres: load relation candidates: %w", err)
	}
	defer rows.Close()
	out := make([]infoitem.RelationCandidate, 0, limit)
	for rows.Next() {
		var c infoitem.RelationCandidate
		if err := rows.Scan(&c.ID, &c.Subject.Kind, &c.Subject.Identifier, &c.Purpose,
			&c.EvidenceKind, &c.AgentID, &c.HypothesisID); err != nil {
			return nil, fmt.Errorf("infoitem/postgres: scan relation candidate: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("infoitem/postgres: iterate relation candidates: %w", err)
	}
	return out, nil
}

func advanceWatermark(ctx context.Context, tx pgx.Tx, scanID string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO info_sync_watermarks (scan_id, applied_seq, status, updated_at)
		VALUES ($1, 0, 'pending', now())
		ON CONFLICT (scan_id) DO NOTHING`, scanID); err != nil {
		return fmt.Errorf("infoitem/postgres: initialize watermark: %w", err)
	}
	var applied int64
	if err := tx.QueryRow(ctx, `
		SELECT applied_seq FROM info_sync_watermarks WHERE scan_id = $1 FOR UPDATE`, scanID,
	).Scan(&applied); err != nil {
		return fmt.Errorf("infoitem/postgres: lock watermark: %w", err)
	}
	var maxSeq int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(seq), 0) FROM info_audit_records WHERE scan_id = $1`, scanID,
	).Scan(&maxSeq); err != nil {
		return fmt.Errorf("infoitem/postgres: read max audit seq: %w", err)
	}
	newApplied := applied
	if maxSeq > applied {
		var contiguous int64
		err := tx.QueryRow(ctx, `
			WITH ordered AS (
				SELECT seq, seq - row_number() OVER (ORDER BY seq) AS grp
				FROM info_audit_records
				WHERE scan_id = $1 AND seq > $2
			), first_group AS (
				SELECT grp FROM ordered ORDER BY seq LIMIT 1
			)
			SELECT CASE WHEN MIN(seq) = $2 + 1 THEN MAX(seq) ELSE $2 END
			FROM ordered, first_group
			WHERE ordered.grp = first_group.grp`, scanID, applied,
		).Scan(&contiguous)
		if err != nil {
			return fmt.Errorf("infoitem/postgres: compute contiguous watermark: %w", err)
		}
		newApplied = contiguous
	}
	status := "applied"
	if newApplied < maxSeq {
		status = "gap"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE info_sync_watermarks
		SET applied_seq = $2, status = $3, last_error = '', updated_at = now()
		WHERE scan_id = $1`, scanID, newApplied, status,
	); err != nil {
		return fmt.Errorf("infoitem/postgres: advance watermark: %w", err)
	}
	return nil
}

// Region implements property-filter activation.
func (s *Store) Region(ctx context.Context, q infoitem.RegionQuery) (infoitem.RegionResult, error) {
	if s == nil || s.pool == nil {
		return infoitem.RegionResult{}, errors.New("infoitem/postgres: store unavailable")
	}
	q.Normalize()
	if err := q.Validate(); err != nil {
		return infoitem.RegionResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// AllScans drops the scan filter: that is the cross-scan retrieval shape
	// the M4 region consumer uses (CORE v2.1 §4.1); per-scan callers keep
	// passing ScanIDs.
	conditions := []string{"TRUE", "(nature = 'observed' OR (jsonb_typeof(supports) = 'array' AND jsonb_array_length(supports) > 0))"}
	args := []any{}
	if !q.AllScans {
		conditions[0] = "scan_id = ANY($1)"
		args = append(args, q.ScanIDs)
	}
	add := func(expr string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(expr, len(args)))
	}
	if q.SubjectKind != "" {
		add("subject->>'kind' = $%d", q.SubjectKind)
	}
	if q.Subject != "" {
		add("subject->>'identifier' = $%d", q.Subject)
	}
	if q.Purpose != "" {
		add("purpose = $%d", q.Purpose)
	}
	if q.Nature != "" {
		add("nature = $%d", string(q.Nature))
	}
	for key, value := range q.ContextEquals {
		args = append(args, key, value)
		conditions = append(conditions, fmt.Sprintf("context->>$%d = $%d", len(args)-1, len(args)))
	}
	if q.ObservedAfter != nil {
		add("observed_at >= $%d", *q.ObservedAfter)
	}
	if q.ObservedBefore != nil {
		add("observed_at <= $%d", *q.ObservedBefore)
	}
	// Keyset paging must mirror ORDER BY observed_at DESC, id ASC exactly:
	// resume strictly after the cursor in that ordering, so rows sharing the
	// same observed_at cannot be skipped across pages.
	if q.Cursor != nil {
		args = append(args, q.Cursor.ObservedAt, q.Cursor.ID)
		at, id := len(args)-1, len(args)
		conditions = append(conditions, fmt.Sprintf(
			"(observed_at < $%d OR (observed_at = $%d AND id > $%d))", at, at, id))
	}
	args = append(args, q.Limit+1)
	query := fmt.Sprintf(`
		SELECT id, scan_id, source_seq, nature, content, coordinates, source, subject, purpose,
		       context, relation_ids, supports, confidence, coordinate_status,
		       valid_from, valid_until, observed_at, created_at
		FROM info_items
		WHERE %s
		ORDER BY observed_at DESC, id ASC
		LIMIT $%d`, strings.Join(conditions, " AND "), len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return infoitem.RegionResult{}, fmt.Errorf("infoitem/postgres: region query: %w", err)
	}
	defer rows.Close()
	items, err := scanItems(rows, q.Limit)
	if err != nil {
		return infoitem.RegionResult{}, err
	}
	incomplete, reason, err := s.incomplete(ctx, q.ScanIDs)
	if err != nil {
		return infoitem.RegionResult{}, err
	}
	res := infoitem.RegionResult{
		Items:      items.items,
		Truncated:  items.truncated,
		HasMore:    items.truncated,
		Incomplete: incomplete,
		GapReason:  reason,
	}
	// The cursor is the last row actually returned, not the extra probe row:
	// keyset conditions are strict, so resuming from it can neither repeat nor
	// skip a row.
	if res.HasMore && len(res.Items) > 0 {
		last := res.Items[len(res.Items)-1]
		res.NextCursor = infoitem.RegionCursor{
			ObservedAt: last.Coordinates.ObservedAt,
			ID:         last.ID,
		}.String()
	}
	return res, nil
}

// Associate implements bounded relation-edge activation.
func (s *Store) Associate(ctx context.Context, q infoitem.AssociationQuery) (infoitem.AssociationResult, error) {
	if s == nil || s.pool == nil {
		return infoitem.AssociationResult{}, errors.New("infoitem/postgres: store unavailable")
	}
	q.Normalize()
	if err := q.Validate(); err != nil {
		return infoitem.AssociationResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	edgeTypes := make([]string, len(q.EdgeTypes))
	for i, typ := range q.EdgeTypes {
		edgeTypes[i] = string(typ)
	}
	rows, err := s.pool.Query(ctx, `
		WITH RECURSIVE walk(start_id, node_id, depth, path, visited, rels) AS (
			SELECT t.id, t.id, 0, ARRAY[t.id]::text[], ARRAY[t.id]::text[], ARRAY[]::text[]
			FROM unnest($2::text[]) AS t(id)
			UNION ALL
			SELECT w.start_id, nxt.id, w.depth + 1, w.path || nxt.id, w.visited || nxt.id,
			       w.rels || e.type::text
			FROM walk w
			JOIN info_item_edges e
			  ON e.scan_id = $1 AND (e.from_id = w.node_id OR e.to_id = w.node_id)
			CROSS JOIN LATERAL (
				SELECT CASE WHEN e.from_id = w.node_id THEN e.to_id ELSE e.from_id END AS id
			) nxt
			JOIN info_items ni
			  ON ni.scan_id = $1 AND ni.id = nxt.id
			WHERE w.depth < $3
			  AND NOT (nxt.id = ANY(w.visited))
			  AND (cardinality($4::text[]) = 0 OR e.type = ANY($4::text[]))
			  AND (ni.nature = 'observed' OR (jsonb_typeof(ni.supports) = 'array' AND jsonb_array_length(ni.supports) > 0))
		)
		SELECT DISTINCT ON (node_id) node_id, path, depth, rels
		FROM walk
		WHERE depth > 0
		ORDER BY node_id, depth, path
		LIMIT $5`, q.ScanID, q.StartIDs, q.MaxDepth, edgeTypes, q.Limit+1,
	)
	if err != nil {
		return infoitem.AssociationResult{}, fmt.Errorf("infoitem/postgres: association query: %w", err)
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
			return infoitem.AssociationResult{}, err
		}
		paths = append(paths, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return infoitem.AssociationResult{}, err
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
	items, err := s.itemsByIDs(ctx, q.ScanID, ids)
	if err != nil {
		return infoitem.AssociationResult{}, err
	}
	hits := make([]infoitem.AssociationHit, 0, len(paths))
	for _, p := range paths {
		if item, ok := items[p.id]; ok {
			hits = append(hits, infoitem.AssociationHit{
				Item:  item,
				Path:  p.path,
				Rels:  relationTypes(p.rels),
				Depth: p.depth,
			})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Depth != hits[j].Depth {
			return hits[i].Depth < hits[j].Depth
		}
		return strings.Join(hits[i].Path, "\x00") < strings.Join(hits[j].Path, "\x00")
	})
	incomplete, reason, err := s.incomplete(ctx, []string{q.ScanID})
	if err != nil {
		return infoitem.AssociationResult{}, err
	}
	return infoitem.AssociationResult{Hits: hits, Truncated: truncated, Incomplete: incomplete, GapReason: reason}, nil
}

// ContextSlice implements fixed-dimension context projection.
func (s *Store) ContextSlice(ctx context.Context, q infoitem.ContextQuery) (infoitem.ContextResult, error) {
	if s == nil || s.pool == nil {
		return infoitem.ContextResult{}, errors.New("infoitem/postgres: store unavailable")
	}
	q.Normalize()
	if err := q.Validate(); err != nil {
		return infoitem.ContextResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	conditions := []string{"scan_id = $1"}
	args := []any{q.ScanID}
	if q.Subject != "" {
		args = append(args, q.Subject)
		conditions = append(conditions, fmt.Sprintf("subject->>'identifier' = $%d", len(args)))
	}
	if q.SubjectKind != "" {
		args = append(args, q.SubjectKind)
		conditions = append(conditions, fmt.Sprintf("subject->>'kind' = $%d", len(args)))
	}
	for key, value := range q.ContextEquals {
		args = append(args, key, value)
		conditions = append(conditions, fmt.Sprintf("context->>$%d = $%d", len(args)-1, len(args)))
	}
	if q.Since != nil {
		args = append(args, *q.Since)
		conditions = append(conditions, fmt.Sprintf("observed_at >= $%d", len(args)))
	}
	if q.Until != nil {
		args = append(args, *q.Until)
		conditions = append(conditions, fmt.Sprintf("observed_at <= $%d", len(args)))
	}
	args = append(args, q.Limit+1)
	query := fmt.Sprintf(`
		SELECT id, scan_id, source_seq, nature, content, coordinates, source, subject, purpose,
		       context, relation_ids, supports, confidence, coordinate_status,
		       valid_from, valid_until, observed_at, created_at
		FROM info_items
		WHERE %s
		ORDER BY observed_at ASC, id ASC
		LIMIT $%d`, strings.Join(conditions, " AND "), len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return infoitem.ContextResult{}, fmt.Errorf("infoitem/postgres: context query: %w", err)
	}
	defer rows.Close()
	items, err := scanItems(rows, q.Limit)
	if err != nil {
		return infoitem.ContextResult{}, err
	}
	incomplete, reason, err := s.incomplete(ctx, []string{q.ScanID})
	if err != nil {
		return infoitem.ContextResult{}, err
	}
	return infoitem.ContextResult{Items: items.items, Truncated: items.truncated, Incomplete: incomplete, GapReason: reason}, nil
}

// GetByID returns one exact cold-path item. Authorization of the scan id is
// the caller's responsibility (project/task scope); this store only enforces
// the (scan_id, id) lookup.
func (s *Store) GetByID(ctx context.Context, scanID, id string) (infoitem.Item, error) {
	id = strings.TrimSpace(id)
	if strings.TrimSpace(scanID) == "" || id == "" {
		return infoitem.Item{}, infoitem.ErrNotFound
	}
	items, err := s.itemsByIDs(ctx, scanID, []string{id})
	if err != nil {
		return infoitem.Item{}, err
	}
	item, ok := items[id]
	if !ok {
		return infoitem.Item{}, infoitem.ErrNotFound
	}
	return item, nil
}

func (s *Store) itemsByIDs(ctx context.Context, scanID string, ids []string) (map[string]infoitem.Item, error) {
	if len(ids) == 0 {
		return map[string]infoitem.Item{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, scan_id, source_seq, nature, content, coordinates, source, subject, purpose,
		       context, relation_ids, supports, confidence, coordinate_status,
		       valid_from, valid_until, observed_at, created_at
		FROM info_items WHERE scan_id = $1 AND id = ANY($2)`, scanID, ids)
	if err != nil {
		return nil, fmt.Errorf("infoitem/postgres: fetch associated items: %w", err)
	}
	defer rows.Close()
	scanned, err := scanItems(rows, len(ids))
	if err != nil {
		return nil, err
	}
	out := make(map[string]infoitem.Item, len(scanned.items))
	for _, item := range scanned.items {
		out[item.ID] = item
	}
	return out, nil
}

type scannedItems struct {
	items     []infoitem.Item
	truncated bool
}

// relationTypes converts the CTE's text[] hop types into the typed vocabulary.
// Empty input stays nil so the additive JSON field disappears and older
// consumers keep the exact previous shape.
func relationTypes(raw []string) []infoitem.RelationType {
	if len(raw) == 0 {
		return nil
	}
	out := make([]infoitem.RelationType, len(raw))
	for i, value := range raw {
		out[i] = infoitem.RelationType(value)
	}
	return out
}

func scanItems(rows pgx.Rows, limit int) (scannedItems, error) {
	var out scannedItems
	for rows.Next() {
		var (
			item                                      infoitem.Item
			nature, status                            string
			content, coordinates, source, subjectJSON []byte
			contextJSON, relationIDs, supportsJSON    []byte
			validFrom, validUntil                     *time.Time
			observedAt, createdAt                     time.Time
			sourceSeq                                 int64
			purpose                                   string
		)
		if err := rows.Scan(
			&item.ID, &item.ScanID, &sourceSeq, &nature, &content, &coordinates, &source,
			&subjectJSON, &purpose, &contextJSON, &relationIDs, &supportsJSON,
			&item.Confidence, &status, &validFrom, &validUntil, &observedAt, &createdAt,
		); err != nil {
			return out, fmt.Errorf("infoitem/postgres: scan item: %w", err)
		}
		item.SourceSeq = uint64(sourceSeq)
		item.Nature = infoitem.Nature(nature)
		item.CoordinateStatus = infoitem.CoordinateStatus(status)
		item.ValidUntil = validUntil
		item.Coordinates.ObservedAt = observedAt
		item.CreatedAt = createdAt
		if validFrom != nil {
			item.ValidFrom = *validFrom
		}
		if err := json.Unmarshal(content, &item.Coordinates.Content); err != nil {
			return out, fmt.Errorf("infoitem/postgres: decode content: %w", err)
		}
		if err := json.Unmarshal(coordinates, &item.Coordinates); err != nil {
			return out, fmt.Errorf("infoitem/postgres: decode coordinates: %w", err)
		}
		if err := json.Unmarshal(source, &item.Coordinates.Source); err != nil {
			return out, fmt.Errorf("infoitem/postgres: decode source: %w", err)
		}
		if err := json.Unmarshal(subjectJSON, &item.Coordinates.Subject); err != nil {
			return out, fmt.Errorf("infoitem/postgres: decode subject: %w", err)
		}
		if err := json.Unmarshal(contextJSON, &item.Coordinates.Context); err != nil {
			return out, fmt.Errorf("infoitem/postgres: decode context: %w", err)
		}
		if err := json.Unmarshal(relationIDs, &item.Coordinates.RelationIDs); err != nil {
			return out, fmt.Errorf("infoitem/postgres: decode relation ids: %w", err)
		}
		if err := json.Unmarshal(supportsJSON, &item.Supports); err != nil {
			return out, fmt.Errorf("infoitem/postgres: decode supports: %w", err)
		}
		item.Coordinates.Purpose = purpose
		out.items = append(out.items, item)
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("infoitem/postgres: iterate items: %w", err)
	}
	if len(out.items) > limit {
		out.items = out.items[:limit]
		out.truncated = true
	}
	return out, nil
}

func (s *Store) incomplete(ctx context.Context, scanIDs []string) (bool, string, error) {
	if len(scanIDs) == 0 {
		return false, "", nil
	}
	var broken int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM unnest($1::text[]) AS sid(scan_id)
		JOIN info_sync_watermarks w ON w.scan_id = sid.scan_id
		WHERE w.status <> 'applied'`, scanIDs).Scan(&broken)
	if err != nil {
		return false, "", fmt.Errorf("infoitem/postgres: check sync state: %w", err)
	}
	if broken > 0 {
		return true, fmt.Sprintf("%d scan(s) have not reached an applied cold-path watermark", broken), nil
	}
	return false, "", nil
}

func marshalStringSlice(values []string) ([]byte, error) {
	if values == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(values)
}

func marshalStringMap(values map[string]string) ([]byte, error) {
	if values == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(values)
}

func marshalOptional(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("infoitem/postgres: marshal optional state: %w", err)
	}
	return data, nil
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
