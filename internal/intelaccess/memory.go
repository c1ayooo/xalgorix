// memory.go — the in-process projection store: unit tests, offline harnesses,
// and the memory graph backend. Same semantics as PGStore, including "never
// downgrade a state" and "grants only grow on derivation".

package intelaccess

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
)

// MemoryStore is a concurrency-safe in-memory Store.
type MemoryStore struct {
	mu           sync.RWMutex
	rows         map[string]map[string]*Access // scanID → fingerprint → row
	capabilities []*Capability
	nextCapID    int64
	uses         []UseRecord
	nextUseID    int64
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rows: make(map[string]map[string]*Access)}
}

func (m *MemoryStore) scan(scanID string) map[string]*Access {
	byFP, ok := m.rows[scanID]
	if !ok {
		byFP = make(map[string]*Access)
		m.rows[scanID] = byFP
	}
	return byFP
}

// DeriveScan projects every item; existing rows keep their state and gains.
func (m *MemoryStore) DeriveScan(ctx context.Context, scanID string, items []intelligence.Item) (int, error) {
	return m.derive(ctx, scanID, 0, items)
}

// DeriveScanForIntent adds first-wins attribution.
func (m *MemoryStore) DeriveScanForIntent(ctx context.Context, scanID string, intentID int64, items []intelligence.Item) (int, error) {
	return m.derive(ctx, scanID, intentID, items)
}

func (m *MemoryStore) derive(_ context.Context, scanID string, intentID int64, items []intelligence.Item) (int, error) {
	scanID = strings.TrimSpace(scanID)
	if scanID == "" {
		return 0, nil
	}
	derived := deriveAll(items)
	if len(derived) == 0 {
		return 0, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	byFP := m.scan(scanID)
	now := time.Now().UTC()
	for _, d := range derived {
		row, ok := byFP[d.Fingerprint]
		if !ok {
			row = &Access{
				ID:             int64(len(m.rows)<<20 + len(byFP) + 1),
				ScanID:         scanID,
				Fingerprint:    d.Fingerprint,
				State:          StateProposed,
				SourceIntentID: intentID,
				CreatedAt:      now,
			}
			byFP[d.Fingerprint] = row
		}
		// Shape fields follow the observation; state and confirmations are
		// human/engine decisions and are never overwritten by a re-derive.
		row.Kind = d.Kind
		row.Subject = d.Subject
		row.Targets = mergeTargets(row.Targets, d.Targets)
		row.UpdatedAt = now
	}
	return len(derived), nil
}

// Get returns one row.
func (m *MemoryStore) Get(_ context.Context, scanID, fingerprint string) (Access, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	row, ok := m.rows[strings.TrimSpace(scanID)][strings.TrimSpace(fingerprint)]
	if !ok {
		return Access{}, false, nil
	}
	return cloneAccess(row), true, nil
}

// ListUsable returns confirmed/active, unexpired rows (exact target match when
// one is given), newest endorsement first, capped.
func (m *MemoryStore) ListUsable(_ context.Context, scanID, target string, limit int) ([]Access, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	byFP := m.rows[strings.TrimSpace(scanID)]
	out := make([]Access, 0, len(byFP))
	now := time.Now().UTC()
	for _, row := range byFP {
		if !row.Usable(now) {
			continue
		}
		if target != "" && !containsTarget(row.Targets, target) {
			continue
		}
		out = append(out, cloneAccess(row))
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := endorsementTime(out[i]), endorsementTime(out[j])
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return out[i].Fingerprint < out[j].Fingerprint
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Lookup resolves one fingerprint across scans, preferring a usable row.
func (m *MemoryStore) Lookup(_ context.Context, scanIDs []string, fingerprint string) (Access, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	fingerprint = strings.TrimSpace(fingerprint)
	var first *Access
	now := time.Now().UTC()
	for _, scanID := range scanIDs {
		row, ok := m.rows[strings.TrimSpace(scanID)][fingerprint]
		if !ok {
			continue
		}
		if row.Usable(now) {
			return cloneAccess(row), true, nil
		}
		if first == nil {
			cp := cloneAccess(row)
			first = &cp
		}
	}
	if first != nil {
		return *first, true, nil
	}
	return Access{}, false, nil
}

// Confirm upserts the operator endorsement of one item within one scan.
func (m *MemoryStore) Confirm(_ context.Context, in ConfirmInput) (Access, error) {
	scanID := strings.TrimSpace(in.ScanID)
	fingerprint := strings.TrimSpace(in.Fingerprint)
	if scanID == "" || fingerprint == "" {
		return Access{}, ErrNotFound
	}
	targets, err := normalizeAll(in.Targets)
	if err != nil {
		return Access{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	byFP := m.scan(scanID)
	now := time.Now().UTC()
	row, ok := byFP[fingerprint]
	if !ok {
		if in.Kind == "" {
			return Access{}, ErrNotFound
		}
		row = &Access{
			ID:          int64(len(m.rows)<<20 + len(byFP) + 1),
			ProjectID:   in.ProjectID,
			ScanID:      scanID,
			Fingerprint: fingerprint,
			CreatedAt:   now,
		}
		byFP[fingerprint] = row
	}
	if in.Kind != "" {
		row.Kind = in.Kind
	}
	if in.Subject != "" {
		row.Subject = in.Subject
	}
	row.State = StateConfirmed
	row.ConfirmedBy = in.By
	row.ConfirmedAt = &now
	row.ExpiresAt = in.ExpiresAt
	row.RevokeReason = ""
	row.Targets = mergeTargets(row.Targets, targets)
	row.UpdatedAt = now
	return cloneAccess(row), nil
}

// Revoke marks the row inert. A missing row is ErrNotFound: revoking what was
// never derived would fabricate a decision record.
func (m *MemoryStore) Revoke(_ context.Context, scanID, fingerprint, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[strings.TrimSpace(scanID)][strings.TrimSpace(fingerprint)]
	if !ok {
		return ErrNotFound
	}
	row.State = StateRevoked
	row.RevokeReason = strings.TrimSpace(reason)
	row.UpdatedAt = time.Now().UTC()
	return nil
}

// AddGrants appends operator targets to an existing row.
func (m *MemoryStore) AddGrants(_ context.Context, scanID, fingerprint string, targets []string) (Access, error) {
	normalized, err := normalizeAll(targets)
	if err != nil {
		return Access{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[strings.TrimSpace(scanID)][strings.TrimSpace(fingerprint)]
	if !ok {
		return Access{}, ErrNotFound
	}
	row.Targets = mergeTargets(row.Targets, normalized)
	row.UpdatedAt = time.Now().UTC()
	return cloneAccess(row), nil
}

func concatTargets(groups ...[]string) []string {
	out := make([]string, 0, 8)
	seen := make(map[string]bool)
	for _, group := range groups {
		for _, target := range group {
			if target == "" || seen[target] {
				continue
			}
			seen[target] = true
			out = append(out, target)
		}
	}
	sort.Strings(out)
	return out
}

func mergeTargets(existing, added []string) []string {
	return concatTargets(existing, added)
}

func containsTarget(targets []string, target string) bool {
	for _, t := range targets {
		if t == target {
			return true
		}
	}
	return false
}

func endorsementTime(a Access) time.Time {
	if a.ConfirmedAt != nil {
		return *a.ConfirmedAt
	}
	return a.UpdatedAt
}

func cloneAccess(a *Access) Access {
	out := *a
	out.Targets = append([]string(nil), a.Targets...)
	if a.ConfirmedAt != nil {
		t := *a.ConfirmedAt
		out.ConfirmedAt = &t
	}
	if a.ExpiresAt != nil {
		t := *a.ExpiresAt
		out.ExpiresAt = &t
	}
	if a.LastUsedAt != nil {
		t := *a.LastUsedAt
		out.LastUsedAt = &t
	}
	return out
}

// ── capabilities (M3) ───────────────────────────────────────────────────────

// FindGrant returns the usable access whose grants cover target.
func (m *MemoryStore) FindGrant(_ context.Context, scanID, target string) (Access, bool, error) {
	canonical, ok := NormalizeTarget(target)
	if !ok {
		return Access{}, false, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := time.Now().UTC()
	var best *Access
	for _, row := range m.rows[strings.TrimSpace(scanID)] {
		if !row.Usable(now) || !grantMatches(row.Targets, canonical) {
			continue
		}
		if best == nil || endorsementTime(*row).After(endorsementTime(*best)) {
			best = row
		}
	}
	if best == nil {
		return Access{}, false, nil
	}
	return cloneAccess(best), true, nil
}

// IssueCapabilities upserts one row per target, sharing a batch id.
func (m *MemoryStore) IssueCapabilities(_ context.Context, in IssueInput) ([]Capability, error) {
	scanID := strings.TrimSpace(in.ScanID)
	if scanID == "" || in.IntentID <= 0 || in.AccessID <= 0 {
		return nil, ErrNotFound
	}
	if in.UsesLeft <= 0 {
		return nil, &InvalidTargetError{Raw: "uses_left must be > 0"}
	}
	if in.TTL <= 0 {
		return nil, &InvalidTargetError{Raw: "ttl must be > 0"}
	}
	if strings.TrimSpace(in.IssuedBy) == "" {
		return nil, &InvalidTargetError{Raw: "issued_by is required"}
	}
	targets, err := normalizeAll(in.Targets)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, &InvalidTargetError{Raw: "at least one target is required"}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Parity with PG's foreign key: a capability always names a real access row.
	if m.accessByIDLocked(scanID, in.AccessID) == nil {
		return nil, ErrNotFound
	}
	now := time.Now().UTC()
	batchID := capabilityBatchID(scanID, in.AccessID, now)
	out := make([]Capability, 0, len(targets))
	for _, target := range targets {
		m.nextCapID++
		capability := Capability{
			ID:        m.nextCapID,
			ScanID:    scanID,
			IntentID:  in.IntentID,
			AccessID:  in.AccessID,
			Target:    target,
			BatchID:   batchID,
			UsesLeft:  in.UsesLeft,
			ExpiresAt: now.Add(in.TTL),
			IssuedBy:  strings.TrimSpace(in.IssuedBy),
			CreatedAt: now,
		}
		if existing := m.findCapabilityLocked(scanID, in.IntentID, in.AccessID, target); existing != nil {
			existing.UsesLeft = capability.UsesLeft
			existing.ExpiresAt = capability.ExpiresAt
			existing.IssuedBy = capability.IssuedBy
			existing.BatchID = capability.BatchID
			out = append(out, *existing)
			continue
		}
		m.capabilities = append(m.capabilities, &capability)
		out = append(out, capability)
	}
	return out, nil
}

// ConsumeCapability spends one use and bumps the access counters.
func (m *MemoryStore) ConsumeCapability(_ context.Context, scanID string, intentID int64, target string) (Capability, bool, error) {
	scanID = strings.TrimSpace(scanID)
	target = strings.TrimSpace(target)
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	for _, capability := range m.capabilities {
		if capability.ScanID != scanID || capability.IntentID != intentID || capability.Target != target {
			continue
		}
		if capability.UsesLeft <= 0 || !capability.ExpiresAt.After(now) {
			continue
		}
		capability.UsesLeft--
		if row := m.accessByIDLocked(scanID, capability.AccessID); row != nil {
			row.UseCount++
			row.LastUsedAt = &now
			row.UpdatedAt = now
			if row.State == StateConfirmed {
				row.State = StateActive
			}
		}
		return *capability, true, nil
	}
	return Capability{}, false, nil
}

// ListCapabilities returns one intent's permissions, newest first.
func (m *MemoryStore) ListCapabilities(_ context.Context, scanID string, intentID int64) ([]Capability, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Capability, 0, 4)
	for _, capability := range m.capabilities {
		if capability.ScanID == strings.TrimSpace(scanID) && capability.IntentID == intentID {
			out = append(out, *capability)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func (m *MemoryStore) findCapabilityLocked(scanID string, intentID, accessID int64, target string) *Capability {
	for _, capability := range m.capabilities {
		if capability.ScanID == scanID && capability.IntentID == intentID &&
			capability.AccessID == accessID && capability.Target == target {
			return capability
		}
	}
	return nil
}

func (m *MemoryStore) accessByIDLocked(scanID string, accessID int64) *Access {
	for _, row := range m.rows[scanID] {
		if row.ID == accessID {
			return row
		}
	}
	return nil
}

// ── audit projection (M4) ───────────────────────────────────────────────────

// maxMemoryUseRows bounds the in-memory audit ring so a long test process
// cannot grow without limit.
const maxMemoryUseRows = 2000

// RecordUse appends one audit row.
func (m *MemoryStore) RecordUse(_ context.Context, row UseRecord) error {
	row.ScanID = strings.TrimSpace(row.ScanID)
	if row.ScanID == "" || strings.TrimSpace(row.Decision) == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextUseID++
	row.ID = m.nextUseID
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now().UTC()
	}
	m.uses = append(m.uses, row)
	if len(m.uses) > maxMemoryUseRows {
		m.uses = m.uses[len(m.uses)-maxMemoryUseRows:]
	}
	return nil
}

// ListUses returns the newest audit rows of one run.
func (m *MemoryStore) ListUses(_ context.Context, scanID string, limit int) ([]UseRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	scanID = strings.TrimSpace(scanID)
	out := make([]UseRecord, 0, 8)
	for i := len(m.uses) - 1; i >= 0; i-- {
		if m.uses[i].ScanID != scanID {
			continue
		}
		out = append(out, m.uses[i])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// RecordFailure increments the access failure counter and revokes at threshold.
func (m *MemoryStore) RecordFailure(_ context.Context, scanID, fingerprint string, threshold int) (Access, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[strings.TrimSpace(scanID)][strings.TrimSpace(fingerprint)]
	if !ok {
		return Access{}, false, ErrNotFound
	}
	if threshold <= 0 {
		threshold = 3
	}
	row.FailCount++
	row.UpdatedAt = time.Now().UTC()
	revoked := false
	if row.FailCount >= threshold && row.State != StateRevoked {
		row.State = StateRevoked
		row.RevokeReason = "repeated dispatch failure"
		revoked = true
	}
	return cloneAccess(row), revoked, nil
}

// ListAccess returns every access row of one run.
func (m *MemoryStore) ListAccess(_ context.Context, scanID string) ([]Access, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rows := m.rows[strings.TrimSpace(scanID)]
	out := make([]Access, 0, len(rows))
	for _, row := range rows {
		out = append(out, cloneAccess(row))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].Fingerprint < out[j].Fingerprint
	})
	return out, nil
}

// ListScanCapabilities returns the run's permissions, newest first.
func (m *MemoryStore) ListScanCapabilities(_ context.Context, scanID string, limit int) ([]Capability, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Capability, 0, 4)
	for _, capability := range m.capabilities {
		if capability.ScanID == strings.TrimSpace(scanID) {
			out = append(out, *capability)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
