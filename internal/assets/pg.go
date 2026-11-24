package assets

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	infopg "github.com/c1ayooo/xalgorix/v4/internal/infoitem/postgres"
)

// PGStore is the production asset-graph authority.
type PGStore struct {
	pool *pgxpool.Pool
}

// Open validates the DSN, clamps the pool, applies the shared migrations and
// returns the asset authority.
func Open(ctx context.Context, dsn string) (*PGStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("assets: empty database URL")
	}
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("assets: parse database URL: %w", err)
	}
	if poolConfig.MaxConns < 2 || poolConfig.MaxConns > 8 {
		poolConfig.MaxConns = 4
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("assets: create PostgreSQL pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("assets: PostgreSQL unavailable: %w", err)
	}
	if err := infopg.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &PGStore{pool: pool}, nil
}

// Close releases the pool.
func (s *PGStore) Close() error {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
	return nil
}

// UpsertAsset inserts or updates one normalized asset identity.
func (s *PGStore) UpsertAsset(ctx context.Context, in AssetInput) (*Asset, error) {
	if err := in.Normalize(); err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO assets
			(project_id, kind, asset_key, display_name, first_info_item_id, last_info_item_id)
		VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT (project_id, kind, asset_key) DO UPDATE SET
			display_name = COALESCE(NULLIF(EXCLUDED.display_name, ''), assets.display_name),
			first_info_item_id = COALESCE(NULLIF(assets.first_info_item_id, ''), EXCLUDED.first_info_item_id),
			last_info_item_id = COALESCE(NULLIF(EXCLUDED.last_info_item_id, ''), assets.last_info_item_id),
			updated_at = now()
		RETURNING `+assetColumns,
		in.ProjectID, string(in.Kind), in.Key, in.DisplayName, in.InfoItemID,
	)
	return scanAsset(row)
}

// LinkTaskAsset records that a run/task knows an asset. Idempotent.
func (s *PGStore) LinkTaskAsset(ctx context.Context, in Link) error {
	if strings.TrimSpace(in.ScanID) == "" || in.AssetID <= 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO task_asset_links (scan_id, task_id, asset_id, source, source_node_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT DO NOTHING`,
		in.ScanID, in.TaskID, in.AssetID, in.Source, in.SourceNodeID,
	)
	return err
}

// AddEdge inserts one deterministic structural relation.
func (s *PGStore) AddEdge(ctx context.Context, projectID string, srcID, dstID int64, rel string) error {
	if srcID <= 0 || dstID <= 0 || strings.TrimSpace(rel) == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO asset_edges (project_id, src_id, rel, dst_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`,
		projectID, srcID, rel, dstID,
	)
	return err
}

// ListAssets returns project assets, optionally filtered by kind.
func (s *PGStore) ListAssets(ctx context.Context, projectID string, kinds ...Kind) ([]Asset, error) {
	args := []any{projectID}
	query := `SELECT ` + assetColumns + ` FROM assets WHERE project_id = $1`
	if len(kinds) > 0 {
		raw := make([]string, 0, len(kinds))
		for _, k := range kinds {
			raw = append(raw, string(k))
		}
		args = append(args, raw)
		query += ` AND kind = ANY($2)`
	}
	query += ` ORDER BY kind ASC, asset_key ASC`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssets(rows)
}

// ListTaskAssets returns the assets linked to one run.
func (s *PGStore) ListTaskAssets(ctx context.Context, scanID string) ([]Asset, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT `+assetColumnsPrefixed("a")+`
		FROM task_asset_links l
		JOIN assets a ON a.id = l.asset_id
		WHERE l.scan_id = $1
		ORDER BY a.kind ASC, a.asset_key ASC`, scanID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssets(rows)
}

// ListEdges returns the project's structural edges.
func (s *PGStore) ListEdges(ctx context.Context, projectID string) ([]Edge, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT src_id, rel, dst_id FROM asset_edges WHERE project_id = $1 ORDER BY src_id, rel, dst_id`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Edge
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.SrcID, &e.Rel, &e.DstID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

const assetColumns = `id, project_id, kind, asset_key, display_name,
	first_info_item_id, last_info_item_id, archived_at, archived_by, merged_into_id,
	created_at, updated_at`

func assetColumnsPrefixed(alias string) string {
	cols := []string{"id", "project_id", "kind", "asset_key", "display_name",
		"first_info_item_id", "last_info_item_id", "archived_at", "archived_by",
		"merged_into_id", "created_at", "updated_at"}
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = alias + "." + c
	}
	return strings.Join(out, ", ")
}

func scanAsset(row interface{ Scan(dest ...any) error }) (*Asset, error) {
	var a Asset
	if err := row.Scan(
		&a.ID, &a.ProjectID, &a.Kind, &a.Key, &a.DisplayName,
		&a.FirstInfoItemID, &a.LastInfoItemID, &a.ArchivedAt, &a.ArchivedBy,
		&a.MergedIntoID, &a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &a, nil
}

func scanAssets(rows pgx.Rows) ([]Asset, error) {
	var out []Asset
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// RecordLocator appends one observed locator for an existing asset. The asset
// must belong to the project; a locator never crosses project scope.
func (s *PGStore) RecordLocator(ctx context.Context, in LocatorInput) (*Locator, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("assets: store unavailable")
	}
	projectID := strings.TrimSpace(in.ProjectID)
	locator := strings.TrimSpace(in.Locator)
	if projectID == "" || in.AssetID <= 0 || locator == "" {
		return nil, fmt.Errorf("assets: locator requires project_id, asset_id and locator")
	}
	observed := in.ObservedAt.UTC()
	if observed.IsZero() {
		observed = time.Now().UTC()
	}
	validFrom := observed
	row := s.pool.QueryRow(ctx, `
		INSERT INTO asset_locators
			(project_id, asset_id, locator, instance_id, observed_at, valid_from, valid_until, source_info_item_id)
		SELECT $1, a.id, $3, $4, $5, $6, $7, $8
		FROM assets a
		WHERE a.project_id = $1 AND a.id = $2
		ON CONFLICT (project_id, asset_id, locator, observed_at)
		DO UPDATE SET valid_from = EXCLUDED.valid_from,
		              valid_until = EXCLUDED.valid_until,
		              source_info_item_id = EXCLUDED.source_info_item_id
		RETURNING id, project_id, asset_id, locator, instance_id, observed_at, valid_from, valid_until, source_info_item_id`,
		projectID, in.AssetID, locator, strings.TrimSpace(in.InstanceID), observed, validFrom, in.ValidUntil, strings.TrimSpace(in.SourceInfoItemID),
	)
	var out Locator
	if err := row.Scan(&out.ID, &out.ProjectID, &out.AssetID, &out.Locator, &out.InstanceID, &out.ObservedAt, &out.ValidFrom, &out.ValidUntil, &out.SourceInfoItemID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("assets: record locator: %w", err)
	}
	return &out, nil
}

// ListAssetLocators returns one asset's locator history, newest first.
func (s *PGStore) ListAssetLocators(ctx context.Context, projectID string, assetID int64) ([]Locator, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("assets: store unavailable")
	}
	projectID = strings.TrimSpace(projectID)
	// An unknown asset inside this project is ErrNotFound, matching GetAssetDetail
	// and the in-memory store; a known asset with no locators yet is empty.
	var exists bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM assets WHERE project_id = $1 AND id = $2)`,
		projectID, assetID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("assets: list locators: %w", err)
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, project_id, asset_id, locator, instance_id, observed_at, valid_from, valid_until, source_info_item_id
		FROM asset_locators
		WHERE project_id = $1 AND asset_id = $2
		ORDER BY observed_at DESC, id DESC`,
		projectID, assetID)
	if err != nil {
		return nil, fmt.Errorf("assets: list locators: %w", err)
	}
	defer rows.Close()
	var out []Locator
	for rows.Next() {
		var l Locator
		if err := rows.Scan(&l.ID, &l.ProjectID, &l.AssetID, &l.Locator, &l.InstanceID, &l.ObservedAt, &l.ValidFrom, &l.ValidUntil, &l.SourceInfoItemID); err != nil {
			return nil, fmt.Errorf("assets: scan locator: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("assets: list locators: %w", err)
	}
	return out, nil
}
