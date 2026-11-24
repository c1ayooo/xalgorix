// Package assets is the asset-graph authority (R7/M1).
//
// Asset identity is authoritative here: one row per normalized
// (project, kind, asset_key), project-scoped and reused across runs.
// InfoItem remains the observation/provenance authority; the asset row keeps
// first/last InfoItem references, never a copy of the raw observation.
package assets

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound is returned for an unknown asset inside the requested project.
var ErrNotFound = errors.New("assets: not found")

// ErrConflict is returned for an operation the current asset state forbids
// (merging a tombstone, restoring a merged source, merging an asset into
// itself).
var ErrConflict = errors.New("assets: conflict")

// Kind is one asset type. The DB CHECK in migration 0020 mirrors this set.
type Kind string

const (
	KindRootDomain Kind = "root_domain"
	KindSubdomain  Kind = "subdomain"
	KindIP         Kind = "ip"
	KindService    Kind = "service"
	KindEndpoint   Kind = "endpoint"
	KindApp        Kind = "app"
)

// Asset is one normalized, project-scoped asset identity.
type Asset struct {
	ID              int64      `json:"id"`
	ProjectID       string     `json:"project_id"`
	Kind            Kind       `json:"kind"`
	Key             string     `json:"key"`
	DisplayName     string     `json:"display_name,omitempty"`
	FirstInfoItemID string     `json:"first_info_item_id,omitempty"`
	LastInfoItemID  string     `json:"last_info_item_id,omitempty"`
	ArchivedAt      *time.Time `json:"archived_at,omitempty"`
	ArchivedBy      string     `json:"archived_by,omitempty"`
	// MergedIntoID marks a tombstone: the source row of a merge. Links and
	// edges have already moved to the target; the row remains for audit.
	MergedIntoID *int64    `json:"merged_into_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// AssetInput upserts one asset identity. InfoItemID is the observation that
// produced/refreshed it (empty for engine-derived inventory).
type AssetInput struct {
	ProjectID   string
	Kind        Kind
	Key         string
	DisplayName string
	InfoItemID  string
	// Locator is the concrete way this observation reached the identity
	// (host, host:port, URL or path). It is not part of the asset identity; it
	// is recorded as a time-bound locator row when non-empty.
	Locator string
}

// Locator is one time-bound way to reach an asset identity. Old locators stay
// queryable; ValidUntil nil means "still current as of the latest observation".
type Locator struct {
	ID               int64      `json:"id"`
	ProjectID        string     `json:"project_id"`
	AssetID          int64      `json:"asset_id"`
	Locator          string     `json:"locator"`
	InstanceID       string     `json:"instance_id,omitempty"`
	ObservedAt       time.Time  `json:"observed_at"`
	ValidFrom        time.Time  `json:"valid_from"`
	ValidUntil       *time.Time `json:"valid_until,omitempty"`
	SourceInfoItemID string     `json:"source_info_item_id,omitempty"`
}

// LocatorInput records one observed locator for an asset.
type LocatorInput struct {
	ProjectID        string
	AssetID          int64
	Locator          string
	InstanceID       string
	ObservedAt       time.Time
	ValidUntil       *time.Time
	SourceInfoItemID string
}

// Edge is one deterministic structural relation.
type Edge struct {
	SrcID int64  `json:"src_id"`
	DstID int64  `json:"dst_id"`
	Rel   string `json:"rel"`
}

// RelExposes is the deterministic host → endpoint relation produced by observed
// URL ingestion: the same observation proves both identities. It is the edge
// the dashboard uses to drill from a target into its exposed paths.
const RelExposes = "exposes"

// Link records that a run/task knows an asset.
type Link struct {
	ScanID       string `json:"scan_id"`
	TaskID       string `json:"task_id,omitempty"`
	AssetID      int64  `json:"asset_id"`
	Source       string `json:"source,omitempty"`
	SourceNodeID *int64 `json:"source_node_id,omitempty"`
}

// AssetFilter selects the project asset list. Empty fields mean "no filter";
// archived tombstones are excluded unless IncludeArchived is set.
type AssetFilter struct {
	ProjectID       string
	Kinds           []Kind
	TaskID          string
	RunID           string
	Query           string
	IncludeArchived bool
	Limit           int
}

// AssetLink is one run/task provenance link for an asset.
type AssetLink struct {
	ScanID    string    `json:"scan_id"`
	TaskID    string    `json:"task_id,omitempty"`
	Source    string    `json:"source,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// AssetDetail is one asset plus its provenance links and incident edges.
type AssetDetail struct {
	Asset Asset       `json:"asset"`
	Links []AssetLink `json:"links"`
	Edges []Edge      `json:"edges"`
}

// Repository is the asset-graph authority seam.
type Repository interface {
	UpsertAsset(ctx context.Context, in AssetInput) (*Asset, error)
	LinkTaskAsset(ctx context.Context, in Link) error
	AddEdge(ctx context.Context, projectID string, srcID, dstID int64, rel string) error
	ListAssets(ctx context.Context, projectID string, kinds ...Kind) ([]Asset, error)
	ListTaskAssets(ctx context.Context, scanID string) ([]Asset, error)
	ListEdges(ctx context.Context, projectID string) ([]Edge, error)
	// ListProjectAssets is the operator list: filters by kind/task/run/query and
	// hides archived tombstones unless requested.
	ListProjectAssets(ctx context.Context, filter AssetFilter) ([]Asset, error)
	// GetAssetDetail returns one asset with provenance links and incident edges.
	GetAssetDetail(ctx context.Context, projectID string, id int64) (AssetDetail, error)
	// RecordLocator appends one observed locator for an existing asset. It is
	// idempotent for the same (asset, locator, observed_at) tuple.
	RecordLocator(ctx context.Context, in LocatorInput) (*Locator, error)
	// ListAssetLocators returns the locator history, newest observation first.
	ListAssetLocators(ctx context.Context, projectID string, assetID int64) ([]Locator, error)
	// MergeAssets moves source links/edges to target and archives the source as
	// a tombstone. Both must belong to projectID.
	MergeAssets(ctx context.Context, projectID string, sourceID, targetID int64, actor string) error
	// SetAssetsArchived archives or restores assets. Merged tombstones cannot be
	// restored (the links already moved). Returns the number of rows changed.
	SetAssetsArchived(ctx context.Context, projectID string, ids []int64, archived bool, actor string) (int, error)
	Close() error
}

// Normalize fills defaults and validates an asset identity.
func (in *AssetInput) Normalize() error {
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	in.Key = NormalizeKey(in.Kind, in.Key)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if in.Key == "" {
		return fmt.Errorf("assets: key is required")
	}
	if !ValidKind(in.Kind) {
		return fmt.Errorf("assets: invalid kind %q", in.Kind)
	}
	if in.DisplayName == "" {
		in.DisplayName = in.Key
	}
	return nil
}

// NormalizeKey is the deterministic dedup key for a kind.
func NormalizeKey(kind Kind, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	switch kind {
	case KindEndpoint:
		if !strings.HasPrefix(key, "/") {
			key = "/" + key
		}
		return strings.ToLower(key)
	case KindRootDomain, KindSubdomain:
		return strings.ToLower(strings.TrimSuffix(key, "."))
	case KindIP, KindService:
		return strings.ToLower(key)
	default:
		return strings.ToLower(key)
	}
}

// ValidKind reports whether k is a known asset kind.
func ValidKind(k Kind) bool {
	switch k {
	case KindRootDomain, KindSubdomain, KindIP, KindService, KindEndpoint, KindApp:
		return true
	default:
		return false
	}
}
