package assets

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MemoryStore is the test/legacy asset implementation. Production uses
// PGStore: assets are project-scoped and shared across runs.
type MemoryStore struct {
	mu            sync.RWMutex
	assets        map[int64]*Asset
	byKey         map[string]int64 // project|kind|key -> id
	links         map[string]map[int64]struct{}
	details       map[int64]map[string]AssetLink // asset id -> scan|source -> link
	edges         map[string]Edge
	locators      []Locator
	nextID        int64
	nextLocatorID int64
}

// NewMemoryStore returns an empty in-memory asset graph.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		assets:  make(map[int64]*Asset),
		byKey:   make(map[string]int64),
		links:   make(map[string]map[int64]struct{}),
		details: make(map[int64]map[string]AssetLink),
		edges:   make(map[string]Edge),
	}
}

// Close is a no-op.
func (m *MemoryStore) Close() error { return nil }

// UpsertAsset inserts or updates one asset identity.
func (m *MemoryStore) UpsertAsset(_ context.Context, in AssetInput) (*Asset, error) {
	if err := in.Normalize(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := in.ProjectID + "|" + string(in.Kind) + "|" + in.Key
	now := time.Now().UTC()
	if id, ok := m.byKey[key]; ok {
		a := m.assets[id]
		if in.DisplayName != "" {
			a.DisplayName = in.DisplayName
		}
		if a.FirstInfoItemID == "" {
			a.FirstInfoItemID = in.InfoItemID
		}
		if in.InfoItemID != "" {
			a.LastInfoItemID = in.InfoItemID
		}
		a.UpdatedAt = now
		cp := *a
		return &cp, nil
	}
	m.nextID++
	a := &Asset{
		ID: m.nextID, ProjectID: in.ProjectID, Kind: in.Kind, Key: in.Key,
		DisplayName: in.DisplayName, FirstInfoItemID: in.InfoItemID,
		LastInfoItemID: in.InfoItemID, CreatedAt: now, UpdatedAt: now,
	}
	m.assets[a.ID] = a
	m.byKey[key] = a.ID
	cp := *a
	return &cp, nil
}

// LinkTaskAsset records a run/task link.
func (m *MemoryStore) LinkTaskAsset(_ context.Context, in Link) error {
	if strings.TrimSpace(in.ScanID) == "" || in.AssetID <= 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.links[in.ScanID]
	if set == nil {
		set = make(map[int64]struct{})
		m.links[in.ScanID] = set
	}
	set[in.AssetID] = struct{}{}
	byAsset := m.details[in.AssetID]
	if byAsset == nil {
		byAsset = make(map[string]AssetLink)
		m.details[in.AssetID] = byAsset
	}
	key := in.ScanID + "|" + in.Source
	if _, ok := byAsset[key]; !ok {
		byAsset[key] = AssetLink{ScanID: in.ScanID, TaskID: in.TaskID, Source: in.Source, CreatedAt: time.Now().UTC()}
	}
	return nil
}

// AddEdge inserts one structural edge.
func (m *MemoryStore) AddEdge(_ context.Context, projectID string, srcID, dstID int64, rel string) error {
	if srcID <= 0 || dstID <= 0 || strings.TrimSpace(rel) == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := projectID + "|" + strconv.FormatInt(srcID, 10) + "|" + rel + "|" + strconv.FormatInt(dstID, 10)
	m.edges[key] = Edge{SrcID: srcID, DstID: dstID, Rel: rel}
	return nil
}

// ListAssets returns project assets, optionally filtered by kind.
func (m *MemoryStore) ListAssets(_ context.Context, projectID string, kinds ...Kind) ([]Asset, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	kindSet := make(map[Kind]bool, len(kinds))
	for _, k := range kinds {
		kindSet[k] = true
	}
	var out []Asset
	for _, a := range m.assets {
		if a.ProjectID != projectID {
			continue
		}
		if len(kindSet) > 0 && !kindSet[a.Kind] {
			continue
		}
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// ListTaskAssets returns the assets linked to one run.
func (m *MemoryStore) ListTaskAssets(_ context.Context, scanID string) ([]Asset, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := m.links[scanID]
	var out []Asset
	for id := range ids {
		if a := m.assets[id]; a != nil {
			out = append(out, *a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// ListEdges returns the project's structural edges.
func (m *MemoryStore) ListEdges(_ context.Context, projectID string) ([]Edge, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Edge
	for key, e := range m.edges {
		if strings.HasPrefix(key, projectID+"|") {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SrcID != out[j].SrcID {
			return out[i].SrcID < out[j].SrcID
		}
		return out[i].Rel < out[j].Rel
	})
	return out, nil
}

// ListProjectAssets implements the operator list filter.
func (m *MemoryStore) ListProjectAssets(_ context.Context, f AssetFilter) ([]Asset, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	kindSet := make(map[Kind]bool, len(f.Kinds))
	for _, k := range f.Kinds {
		kindSet[k] = true
	}
	query := strings.ToLower(strings.TrimSpace(f.Query))
	var out []Asset
	for _, a := range m.assets {
		if a.ProjectID != f.ProjectID {
			continue
		}
		if !f.IncludeArchived && a.ArchivedAt != nil {
			continue
		}
		if len(kindSet) > 0 && !kindSet[a.Kind] {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(a.Key+" "+a.DisplayName), query) {
			continue
		}
		if f.TaskID != "" || f.RunID != "" {
			matched := false
			for _, link := range m.details[a.ID] {
				if f.TaskID != "" && link.TaskID != f.TaskID {
					continue
				}
				if f.RunID != "" && link.ScanID != f.RunID {
					continue
				}
				matched = true
				break
			}
			if !matched {
				continue
			}
		}
		out = append(out, *a)
	}
	sortAssets(out)
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// GetAssetDetail returns provenance links and incident edges.
func (m *MemoryStore) GetAssetDetail(_ context.Context, projectID string, id int64) (AssetDetail, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a := m.assets[id]
	if a == nil || a.ProjectID != projectID {
		return AssetDetail{}, ErrNotFound
	}
	out := AssetDetail{Asset: *a, Links: []AssetLink{}, Edges: []Edge{}}
	for _, link := range m.details[id] {
		out.Links = append(out.Links, link)
	}
	sort.Slice(out.Links, func(i, j int) bool { return out.Links[i].CreatedAt.After(out.Links[j].CreatedAt) })
	for key, e := range m.edges {
		if !strings.HasPrefix(key, projectID+"|") {
			continue
		}
		if e.SrcID == id || e.DstID == id {
			out.Edges = append(out.Edges, e)
		}
	}
	sort.Slice(out.Edges, func(i, j int) bool {
		if out.Edges[i].SrcID != out.Edges[j].SrcID {
			return out.Edges[i].SrcID < out.Edges[j].SrcID
		}
		return out.Edges[i].Rel < out.Edges[j].Rel
	})
	return out, nil
}

// MergeAssets moves links/edges to target and tombstones the source.
func (m *MemoryStore) MergeAssets(_ context.Context, projectID string, sourceID, targetID int64, actor string) error {
	if sourceID == targetID {
		return fmt.Errorf("%w: source and target are the same asset", ErrConflict)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	src, dst := m.assets[sourceID], m.assets[targetID]
	if src == nil || src.ProjectID != projectID || dst == nil || dst.ProjectID != projectID {
		return ErrNotFound
	}
	if src.MergedIntoID != nil || src.ArchivedAt != nil {
		return fmt.Errorf("%w: source is already archived or merged", ErrConflict)
	}
	if dst.MergedIntoID != nil || dst.ArchivedAt != nil {
		return fmt.Errorf("%w: target is archived or merged", ErrConflict)
	}

	if m.details[targetID] == nil {
		m.details[targetID] = make(map[string]AssetLink)
	}
	for key, link := range m.details[sourceID] {
		if _, ok := m.details[targetID][key]; !ok {
			m.details[targetID][key] = link
		}
	}
	delete(m.details, sourceID)
	for scanID, set := range m.links {
		if _, ok := set[sourceID]; !ok {
			continue
		}
		delete(set, sourceID)
		set[targetID] = struct{}{}
		_ = scanID
	}
	for key, e := range m.edges {
		if !strings.HasPrefix(key, projectID+"|") || (e.SrcID != sourceID && e.DstID != sourceID) {
			continue
		}
		delete(m.edges, key)
		srcNew, dstNew := e.SrcID, e.DstID
		if srcNew == sourceID {
			srcNew = targetID
		}
		if dstNew == sourceID {
			dstNew = targetID
		}
		if srcNew == dstNew {
			continue
		}
		newKey := projectID + "|" + strconv.FormatInt(srcNew, 10) + "|" + e.Rel + "|" + strconv.FormatInt(dstNew, 10)
		m.edges[newKey] = Edge{SrcID: srcNew, DstID: dstNew, Rel: e.Rel}
	}
	if dst.FirstInfoItemID == "" {
		dst.FirstInfoItemID = src.FirstInfoItemID
	}
	if src.LastInfoItemID != "" {
		dst.LastInfoItemID = src.LastInfoItemID
	}
	now := time.Now().UTC()
	dst.UpdatedAt = now
	src.ArchivedAt = &now
	src.ArchivedBy = strings.TrimSpace(actor)
	src.MergedIntoID = &targetID
	src.UpdatedAt = now
	return nil
}

// SetAssetsArchived archives or restores assets.
func (m *MemoryStore) SetAssetsArchived(_ context.Context, projectID string, ids []int64, archived bool, actor string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	changed := 0
	for _, id := range ids {
		a := m.assets[id]
		if a == nil || a.ProjectID != projectID {
			continue
		}
		if !archived && a.MergedIntoID != nil {
			continue // merged tombstones cannot be restored independently
		}
		if archived == (a.ArchivedAt != nil) {
			continue
		}
		if archived {
			a.ArchivedAt = &now
			a.ArchivedBy = strings.TrimSpace(actor)
		} else {
			a.ArchivedAt = nil
			a.ArchivedBy = ""
		}
		a.UpdatedAt = now
		changed++
	}
	return changed, nil
}

func sortAssets(out []Asset) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Key < out[j].Key
	})
}

// String is a debug helper.
func (a Asset) String() string {
	return fmt.Sprintf("%s:%s", a.Kind, a.Key)
}

// RecordLocator appends one locator row for an existing asset. The same
// (asset, locator, observed_at) observation is idempotent.
func (m *MemoryStore) RecordLocator(_ context.Context, in LocatorInput) (*Locator, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	projectID := strings.TrimSpace(in.ProjectID)
	locator := strings.TrimSpace(in.Locator)
	if projectID == "" || in.AssetID <= 0 || locator == "" {
		return nil, fmt.Errorf("assets: locator requires project_id, asset_id and locator")
	}
	a := m.assets[in.AssetID]
	if a == nil || a.ProjectID != projectID {
		return nil, ErrNotFound
	}
	observed := in.ObservedAt.UTC()
	if observed.IsZero() {
		observed = time.Now().UTC()
	}
	for i := range m.locators {
		l := &m.locators[i]
		if l.ProjectID == projectID && l.AssetID == in.AssetID && l.Locator == locator && l.ObservedAt.Equal(observed) {
			l.ValidUntil = in.ValidUntil
			l.SourceInfoItemID = strings.TrimSpace(in.SourceInfoItemID)
			out := *l
			return &out, nil
		}
	}
	m.nextLocatorID++
	out := Locator{
		ID:               m.nextLocatorID,
		ProjectID:        projectID,
		AssetID:          in.AssetID,
		Locator:          locator,
		InstanceID:       strings.TrimSpace(in.InstanceID),
		ObservedAt:       observed,
		ValidFrom:        observed,
		ValidUntil:       in.ValidUntil,
		SourceInfoItemID: strings.TrimSpace(in.SourceInfoItemID),
	}
	m.locators = append(m.locators, out)
	return &out, nil
}

// ListAssetLocators returns one asset's locator history, newest first.
func (m *MemoryStore) ListAssetLocators(_ context.Context, projectID string, assetID int64) ([]Locator, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a := m.assets[assetID]
	if a == nil || a.ProjectID != projectID {
		return nil, ErrNotFound
	}
	out := make([]Locator, 0)
	for _, l := range m.locators {
		if l.ProjectID == projectID && l.AssetID == assetID {
			out = append(out, l)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].ObservedAt.Equal(out[j].ObservedAt) {
			return out[i].ObservedAt.After(out[j].ObservedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}
