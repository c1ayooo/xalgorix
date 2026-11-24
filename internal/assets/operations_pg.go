package assets

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ListProjectAssets is the operator list: kind/task/run/text filters, archived
// tombstones hidden by default.
func (s *PGStore) ListProjectAssets(ctx context.Context, f AssetFilter) ([]Asset, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("assets: store unavailable")
	}
	args := []any{strings.TrimSpace(f.ProjectID)}
	where := []string{"a.project_id = $1"}
	joinedLinks := false
	if len(f.Kinds) > 0 {
		kinds := make([]string, 0, len(f.Kinds))
		for _, k := range f.Kinds {
			if ValidKind(k) {
				kinds = append(kinds, string(k))
			}
		}
		if len(kinds) > 0 {
			args = append(args, kinds)
			where = append(where, fmt.Sprintf("a.kind = ANY($%d)", len(args)))
		}
	}
	if taskID := strings.TrimSpace(f.TaskID); taskID != "" {
		joinedLinks = true
		args = append(args, taskID)
		where = append(where, fmt.Sprintf("l.task_id = $%d", len(args)))
	}
	if runID := strings.TrimSpace(f.RunID); runID != "" {
		joinedLinks = true
		args = append(args, runID)
		where = append(where, fmt.Sprintf("l.scan_id = $%d", len(args)))
	}
	if query := strings.TrimSpace(f.Query); query != "" {
		args = append(args, "%"+query+"%")
		where = append(where, fmt.Sprintf("(a.asset_key ILIKE $%d OR a.display_name ILIKE $%d)", len(args), len(args)))
	}
	if !f.IncludeArchived {
		where = append(where, "a.archived_at IS NULL")
	}
	query := `SELECT DISTINCT ` + assetColumnsPrefixed("a") + ` FROM assets a`
	if joinedLinks {
		query += ` JOIN task_asset_links l ON l.asset_id = a.id`
	}
	query += ` WHERE ` + strings.Join(where, " AND ") + ` ORDER BY a.kind ASC, a.asset_key ASC`
	if f.Limit > 0 {
		args = append(args, f.Limit)
		query += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("assets: list: %w", err)
	}
	defer rows.Close()
	return scanAssets(rows)
}

// GetAssetDetail returns one asset with provenance links and incident edges.
func (s *PGStore) GetAssetDetail(ctx context.Context, projectID string, id int64) (AssetDetail, error) {
	if s == nil || s.pool == nil {
		return AssetDetail{}, fmt.Errorf("assets: store unavailable")
	}
	row := s.pool.QueryRow(ctx, `SELECT `+assetColumns+` FROM assets WHERE project_id = $1 AND id = $2`,
		strings.TrimSpace(projectID), id)
	asset, err := scanAsset(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AssetDetail{}, ErrNotFound
		}
		return AssetDetail{}, fmt.Errorf("assets: get: %w", err)
	}
	out := AssetDetail{Asset: *asset, Links: []AssetLink{}, Edges: []Edge{}}

	linkRows, err := s.pool.Query(ctx, `
		SELECT scan_id, task_id, source, created_at
		FROM task_asset_links
		WHERE asset_id = $1
		ORDER BY created_at DESC
		LIMIT 200`, id)
	if err != nil {
		return AssetDetail{}, fmt.Errorf("assets: list links: %w", err)
	}
	for linkRows.Next() {
		var l AssetLink
		if err := linkRows.Scan(&l.ScanID, &l.TaskID, &l.Source, &l.CreatedAt); err != nil {
			linkRows.Close()
			return AssetDetail{}, fmt.Errorf("assets: scan link: %w", err)
		}
		out.Links = append(out.Links, l)
	}
	linkRows.Close()
	if err := linkRows.Err(); err != nil {
		return AssetDetail{}, fmt.Errorf("assets: iterate links: %w", err)
	}

	edgeRows, err := s.pool.Query(ctx, `
		SELECT src_id, rel, dst_id
		FROM asset_edges
		WHERE project_id = $1 AND (src_id = $2 OR dst_id = $2)
		ORDER BY src_id ASC, rel ASC, dst_id ASC`, projectID, id)
	if err != nil {
		return AssetDetail{}, fmt.Errorf("assets: list edges: %w", err)
	}
	for edgeRows.Next() {
		var e Edge
		if err := edgeRows.Scan(&e.SrcID, &e.Rel, &e.DstID); err != nil {
			edgeRows.Close()
			return AssetDetail{}, fmt.Errorf("assets: scan edge: %w", err)
		}
		out.Edges = append(out.Edges, e)
	}
	edgeRows.Close()
	if err := edgeRows.Err(); err != nil {
		return AssetDetail{}, fmt.Errorf("assets: iterate edges: %w", err)
	}
	return out, nil
}

// MergeAssets moves every link/edge from source to target inside one
// transaction and archives the source as a tombstone.
func (s *PGStore) MergeAssets(ctx context.Context, projectID string, sourceID, targetID int64, actor string) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("assets: store unavailable")
	}
	if sourceID == targetID {
		return fmt.Errorf("%w: source and target are the same asset", ErrConflict)
	}
	projectID = strings.TrimSpace(projectID)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("assets: begin merge: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `SELECT `+assetColumns+` FROM assets
		WHERE project_id = $1 AND id = ANY($2) FOR UPDATE`, projectID, []int64{sourceID, targetID})
	if err != nil {
		return fmt.Errorf("assets: lock merge rows: %w", err)
	}
	locked := map[int64]Asset{}
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			rows.Close()
			return fmt.Errorf("assets: scan merge row: %w", err)
		}
		locked[a.ID] = *a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("assets: iterate merge rows: %w", err)
	}
	src, okSrc := locked[sourceID]
	dst, okDst := locked[targetID]
	if !okSrc || !okDst {
		return ErrNotFound
	}
	if src.MergedIntoID != nil || src.ArchivedAt != nil {
		return fmt.Errorf("%w: source is already archived or merged", ErrConflict)
	}
	if dst.MergedIntoID != nil || dst.ArchivedAt != nil {
		return fmt.Errorf("%w: target is archived or merged", ErrConflict)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO task_asset_links (scan_id, task_id, asset_id, source, source_node_id, created_at)
		SELECT scan_id, task_id, $2, source, source_node_id, created_at
		FROM task_asset_links WHERE asset_id = $1
		ON CONFLICT DO NOTHING`, sourceID, targetID); err != nil {
		return fmt.Errorf("assets: move links: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM task_asset_links WHERE asset_id = $1`, sourceID); err != nil {
		return fmt.Errorf("assets: delete source links: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		WITH moved AS (
			SELECT project_id,
			       CASE WHEN src_id = $2 THEN $3 ELSE src_id END AS src_id,
			       rel,
			       CASE WHEN dst_id = $2 THEN $3 ELSE dst_id END AS dst_id,
			       created_at
			FROM asset_edges
			WHERE project_id = $1 AND (src_id = $2 OR dst_id = $2)
		)
		INSERT INTO asset_edges (project_id, src_id, rel, dst_id, created_at)
		SELECT project_id, src_id, rel, dst_id, created_at FROM moved WHERE src_id <> dst_id
		ON CONFLICT DO NOTHING`, projectID, sourceID, targetID); err != nil {
		return fmt.Errorf("assets: move edges: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM asset_edges WHERE project_id = $1 AND (src_id = $2 OR dst_id = $2)`,
		projectID, sourceID); err != nil {
		return fmt.Errorf("assets: delete source edges: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE assets SET
			first_info_item_id = CASE WHEN first_info_item_id = '' THEN $3 ELSE first_info_item_id END,
			last_info_item_id  = CASE WHEN $4 <> '' THEN $4 ELSE last_info_item_id END,
			updated_at = now()
		WHERE project_id = $1 AND id = $2`,
		projectID, targetID, src.FirstInfoItemID, src.LastInfoItemID); err != nil {
		return fmt.Errorf("assets: update merge target: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE assets SET archived_at = now(), archived_by = $3, merged_into_id = $4, updated_at = now()
		WHERE project_id = $1 AND id = $2`,
		projectID, sourceID, strings.TrimSpace(actor), targetID); err != nil {
		return fmt.Errorf("assets: tombstone merge source: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("assets: commit merge: %w", err)
	}
	return nil
}

// SetAssetsArchived archives or restores assets. Merged tombstones cannot be
// restored because their links and edges already moved to the target.
func (s *PGStore) SetAssetsArchived(ctx context.Context, projectID string, ids []int64, archived bool, actor string) (int, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("assets: store unavailable")
	}
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE assets SET
			archived_at = CASE WHEN $3 THEN now() ELSE NULL END,
			archived_by = CASE WHEN $3 THEN $4 ELSE '' END,
			updated_at = now()
		WHERE project_id = $1 AND id = ANY($2)
		  AND (archived_at IS NULL) = $3
		  AND ($3 OR merged_into_id IS NULL)`,
		strings.TrimSpace(projectID), ids, archived, strings.TrimSpace(actor))
	if err != nil {
		return 0, fmt.Errorf("assets: set archived: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
