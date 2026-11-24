package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryStore is the process-local graph implementation. It exists for unit
// tests and the legacy/offline path. Production MUST use PGStore: the whole
// point of R7 is that the graph authority is durable and shared.
type MemoryStore struct {
	mu     sync.RWMutex
	nodes  map[int64]*Node
	edges  map[memoryEdge]struct{}
	nextID int64
}

type memoryEdge struct {
	scanID   string
	src, dst int64
	rel      Rel
}

// NewMemoryStore returns an empty in-memory graph.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		nodes: make(map[int64]*Node),
		edges: make(map[memoryEdge]struct{}),
	}
}

// Close is a no-op.
func (m *MemoryStore) Close() error { return nil }

// AddNode inserts one node.
func (m *MemoryStore) AddNode(_ context.Context, in NodeInput) (*Node, error) {
	if err := in.Normalize(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *m.addNodeLocked(in)
	return &cp, nil
}

// addNodeLocked allocates and stores a node. The caller holds m.mu; keeping
// allocation under the same lock as the update path is what makes
// UpsertNode's find-or-insert atomic for concurrent projections.
func (m *MemoryStore) addNodeLocked(in NodeInput) *Node {
	m.nextID++
	now := time.Now().UTC()
	n := &Node{
		ID:             m.nextID,
		ScanID:         in.ScanID,
		InstanceID:     in.InstanceID,
		UnitKey:        in.UnitKey,
		Kind:           in.Kind,
		State:          in.State,
		Priority:       in.Priority,
		Origin:         in.Origin,
		Owner:          in.Owner,
		LeaseUntil:     in.LeaseUntil,
		Payload:        in.Payload,
		ContentVersion: 1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	m.nodes[n.ID] = n
	return n
}

// UpsertNode updates the node with the same (scan, kind, unit_key) or inserts.
// Find-or-insert happens under one lock: two concurrent projections of the
// same unit must converge on one node, never create duplicate lineage.
func (m *MemoryStore) UpsertNode(ctx context.Context, in NodeInput) (*Node, error) {
	if err := in.Normalize(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.UnitKey) == "" {
		return m.AddNode(ctx, in)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, n := range m.nodes {
		if n.ScanID != in.ScanID || n.Kind != in.Kind || n.UnitKey != in.UnitKey {
			continue
		}
		n.State = in.State
		n.Priority = in.Priority
		n.Origin = in.Origin
		n.Owner = in.Owner
		n.InstanceID = in.InstanceID
		n.LeaseUntil = in.LeaseUntil
		if n.Payload != in.Payload {
			n.ContentVersion++
		}
		n.Payload = in.Payload
		n.UpdatedAt = time.Now().UTC()
		cp := *n
		return &cp, nil
	}
	cp := *m.addNodeLocked(in)
	return &cp, nil
}

// AddEdge inserts one idempotent lineage edge.
func (m *MemoryStore) AddEdge(_ context.Context, in EdgeInput) error {
	if err := in.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.nodes[in.SrcID]; !ok {
		return fmt.Errorf("graph: source node %d not found", in.SrcID)
	}
	if _, ok := m.nodes[in.DstID]; !ok {
		return fmt.Errorf("graph: destination node %d not found", in.DstID)
	}
	m.edges[memoryEdge{scanID: in.ScanID, src: in.SrcID, dst: in.DstID, rel: in.Rel}] = struct{}{}
	return nil
}

// UpdateNode rewrites one intent's payload/priority.
func (m *MemoryStore) UpdateNode(_ context.Context, scanID string, id int64, payload string, priority int) error {
	if !json.Valid([]byte(payload)) {
		return fmt.Errorf("graph: payload is not valid JSON")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok || n.ScanID != scanID || n.Kind != KindIntent {
		return ErrNotFound
	}
	if n.Payload != payload {
		n.ContentVersion++
	}
	n.Payload = payload
	n.Priority = priority
	n.UpdatedAt = time.Now().UTC()
	return nil
}

// ReplaceDependencyEdges rewrites the depends_on edges into one intent.
func (m *MemoryStore) ReplaceDependencyEdges(_ context.Context, scanID string, dstID int64, srcIDs []int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key := range m.edges {
		if key.scanID == scanID && key.dst == dstID && key.rel == RelDependsOn {
			delete(m.edges, key)
		}
	}
	for _, src := range srcIDs {
		if src <= 0 || src == dstID {
			continue
		}
		if _, ok := m.nodes[src]; !ok {
			return fmt.Errorf("graph: dependency node %d not found", src)
		}
		m.edges[memoryEdge{scanID: scanID, src: src, dst: dstID, rel: RelDependsOn}] = struct{}{}
	}
	return nil
}

// GetNode returns a copy or ErrNotFound.
func (m *MemoryStore) GetNode(_ context.Context, scanID string, id int64) (*Node, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n, ok := m.nodes[id]
	if !ok || n.ScanID != scanID {
		return nil, ErrNotFound
	}
	cp := *n
	return &cp, nil
}

// ListNodes returns copies filtered by kind/state.
func (m *MemoryStore) ListNodes(_ context.Context, scanID string, kind Kind, states ...State) ([]Node, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	stateSet := make(map[State]bool, len(states))
	for _, s := range states {
		stateSet[s] = true
	}
	var out []Node
	for _, n := range m.nodes {
		if n.ScanID != scanID || n.Kind != kind {
			continue
		}
		if len(stateSet) > 0 && !stateSet[n.State] {
			continue
		}
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ListEdges returns every lineage edge in one scan.
func (m *MemoryStore) ListEdges(_ context.Context, scanID string) ([]Edge, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Edge
	for edge := range m.edges {
		if edge.scanID != scanID {
			continue
		}
		out = append(out, Edge{SrcID: edge.src, DstID: edge.dst, Rel: edge.rel})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SrcID != out[j].SrcID {
			return out[i].SrcID < out[j].SrcID
		}
		if out[i].Rel != out[j].Rel {
			return out[i].Rel < out[j].Rel
		}
		return out[i].DstID < out[j].DstID
	})
	return out, nil
}

// ClaimNextIntent atomically claims the next ready open intent.
func (m *MemoryStore) ClaimNextIntent(_ context.Context, scanID, owner string, ttl time.Duration) (*Node, error) {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reclaimExpiredLocked(scanID, time.Now().UTC())
	var best *Node
	for _, n := range m.nodes {
		if n.ScanID != scanID || n.Kind != KindIntent || n.State != StateOpen {
			continue
		}
		if !m.dependenciesSatisfiedLocked(n) {
			continue
		}
		if !m.contractReadyLocked(n) {
			continue
		}
		if best == nil || n.Priority > best.Priority || (n.Priority == best.Priority && n.ID < best.ID) {
			best = n
		}
	}
	if best == nil {
		return nil, nil
	}
	best.State = StateRunning
	best.Owner = owner
	lease := time.Now().UTC().Add(ttl)
	best.LeaseUntil = &lease
	best.CompletedAt = nil
	best.ContentVersion++
	best.UpdatedAt = time.Now().UTC()
	cp := *best
	return &cp, nil
}

// contractReadyLocked reports whether the intent's contract allows a claim.
func (m *MemoryStore) contractReadyLocked(n *Node) bool {
	c, err := ParseContractStrict(n.Payload)
	if err != nil {
		return false
	}
	if c.NeedsConfirmation() {
		return false
	}
	for _, pre := range c.Preconditions {
		if !m.preconditionSatisfiedLocked(n.ScanID, pre) {
			return false
		}
	}
	return true
}

func (m *MemoryStore) preconditionSatisfiedLocked(scanID string, pre Precondition) bool {
	for _, n := range m.nodes {
		if n.ScanID == scanID && n.Kind == pre.RefKind && n.UnitKey == pre.Ref && n.State == pre.NeedState {
			return true
		}
	}
	return false
}

// ConcludeIntent settles one claimed intent and its produced nodes under one
// lock, mirroring the PG transaction.
func (m *MemoryStore) ConcludeIntent(ctx context.Context, scanID string, id int64, owner string, to State, produced []NodeInput) (*Node, error) {
	return m.conclude(ctx, scanID, id, to, produced, concludeMode{owner: owner, requireClaim: true})
}

// ConcludePlanned settles an intent from the plan surface without a claim.
func (m *MemoryStore) ConcludePlanned(ctx context.Context, scanID string, id int64, to State, produced []NodeInput) (*Node, error) {
	return m.conclude(ctx, scanID, id, to, produced, concludeMode{})
}

func (m *MemoryStore) conclude(ctx context.Context, scanID string, id int64, to State, produced []NodeInput, mode concludeMode) (*Node, error) {
	if !ValidState(KindIntent, to) || !TerminalState(KindIntent, to) {
		return nil, fmt.Errorf("graph: conclude intent: %q is not a terminal intent state", to)
	}
	if to == StateDone && len(produced) == 0 {
		return nil, ErrNoProduct
	}
	if to != StateDone && len(produced) > 0 {
		return nil, fmt.Errorf("graph: conclude intent: %s must not produce nodes", to)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok || n.ScanID != scanID || n.Kind != KindIntent {
		return nil, ErrNotFound
	}
	if mode.requireClaim {
		if n.State != StateRunning || n.Owner != mode.owner {
			return nil, fmt.Errorf("%w: intent %d is %s owned by %q", ErrConflict, n.ID, n.State, n.Owner)
		}
		now := time.Now().UTC()
		if n.LeaseUntil != nil && n.LeaseUntil.Before(now) {
			return nil, fmt.Errorf("%w: intent %d lease expired at %s", ErrLeaseExpired, n.ID, n.LeaseUntil.UTC().Format(time.RFC3339))
		}
	} else if n.State != StateOpen && n.State != StateRunning && n.State != StatePaused {
		return nil, fmt.Errorf("%w: intent %d is %s", ErrConflict, n.ID, n.State)
	} else if n.State == StateRunning && strings.HasPrefix(n.Owner, "work#") {
		return nil, fmt.Errorf("%w: intent %d is owned by live worker %q", ErrConflict, n.ID, n.Owner)
	}
	now := time.Now().UTC()
	contract, err := ParseContractStrict(n.Payload)
	if err != nil {
		return nil, fmt.Errorf("graph: conclude intent %d: %w", n.ID, err)
	}
	if to == StateDone && contract.NeedsConfirmation() {
		return nil, fmt.Errorf("%w: intent %d", ErrConfirmationRequired, n.ID)
	}
	if to == StateDone {
		if err := ValidateProducedYields(contract, produced); err != nil {
			return nil, fmt.Errorf("graph: conclude intent %d: %w", n.ID, err)
		}
		for _, pre := range contract.Preconditions {
			if !m.preconditionSatisfiedLocked(scanID, pre) {
				return nil, fmt.Errorf("%w: %s/%s is not %s", ErrPreconditionUnmet, pre.RefKind, pre.Ref, pre.NeedState)
			}
		}
	}

	producedIDs := make([]int64, 0, len(produced))
	for i := range produced {
		in := produced[i]
		in.ScanID = scanID
		if in.Kind == "" {
			in.Kind = KindFact
		}
		if in.State == "" {
			in.State = StateOrigin
		}
		if in.Kind != KindFact && in.Kind != KindFinding {
			return nil, fmt.Errorf("graph: conclude intent %d: produced kind %q must be fact or finding", n.ID, in.Kind)
		}
		if strings.TrimSpace(in.UnitKey) == "" {
			return nil, fmt.Errorf("graph: conclude intent %d: produced node requires a unit_key", n.ID)
		}
		if in.Origin == "" {
			in.Origin = "intent:" + n.UnitKey
		}
		if err := in.Normalize(); err != nil {
			return nil, err
		}
		var target *Node
		for _, existing := range m.nodes {
			if existing.ScanID == scanID && existing.Kind == in.Kind && existing.UnitKey == in.UnitKey {
				target = existing
				break
			}
		}
		if target == nil {
			m.nextID++
			target = &Node{
				ID:             m.nextID,
				ScanID:         scanID,
				InstanceID:     in.InstanceID,
				UnitKey:        in.UnitKey,
				Kind:           in.Kind,
				State:          in.State,
				Priority:       in.Priority,
				Origin:         in.Origin,
				Owner:          in.Owner,
				LeaseUntil:     in.LeaseUntil,
				Payload:        in.Payload,
				ContentVersion: 1,
				CreatedAt:      now,
				UpdatedAt:      now,
			}
			m.nodes[target.ID] = target
		} else {
			target.State = in.State
			target.Priority = in.Priority
			target.Origin = in.Origin
			target.InstanceID = in.InstanceID
			if target.Payload != in.Payload {
				target.ContentVersion++
			}
			target.Payload = in.Payload
			target.UpdatedAt = now
		}
		producedIDs = append(producedIDs, target.ID)
	}
	for _, pid := range producedIDs {
		m.edges[memoryEdge{scanID: scanID, src: n.ID, dst: pid, rel: RelYields}] = struct{}{}
	}

	n.State = to
	n.Owner = ""
	n.LeaseUntil = nil
	n.CompletedAt = &now
	n.ContentVersion++
	n.UpdatedAt = now
	// Same seam as the PG transaction: derived projections settle with the
	// conclusion and a hook failure never fails it.
	runConcludeHookMemory(ctx, scanID, n.ID)
	cp := *n
	return &cp, nil
}

// RenewLease extends a live claim.
func (m *MemoryStore) RenewLease(_ context.Context, scanID string, id int64, owner string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok || n.ScanID != scanID || n.Kind != KindIntent || n.State != StateRunning || n.Owner != owner {
		return false, nil
	}
	lease := time.Now().UTC().Add(ttl)
	n.LeaseUntil = &lease
	n.UpdatedAt = time.Now().UTC()
	return true, nil
}

// ReclaimExpired returns expired claims to their open/queued states.
func (m *MemoryStore) ReclaimExpired(_ context.Context, scanID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reclaimExpiredLocked(scanID, time.Now().UTC()), nil
}

// ResetRunning is the process-start recovery.
func (m *MemoryStore) ResetRunning(_ context.Context, scanID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, node := range m.nodes {
		if node.ScanID != scanID {
			continue
		}
		if node.Kind == KindIntent && node.State == StateRunning {
			node.State = StateOpen
			node.Owner = ""
			node.LeaseUntil = nil
			node.CompletedAt = nil
			n++
		}
		if node.Kind == KindBelief && node.State == StateTesting {
			node.State = StateQueued
			node.Owner = ""
			node.LeaseUntil = nil
			n++
		}
	}
	return n, nil
}

// Transition performs one CAS transition. intent -> done is structurally
// refused: done is reachable only via ConcludeIntent/ConcludePlanned, which
// re-checks owner/lease/confirmation/preconditions and writes the yields
// receipt in the same transaction (see pg.go and types.go).
func (m *MemoryStore) Transition(_ context.Context, scanID string, id int64, kind Kind, from, to State) (bool, error) {
	if !ValidKind(kind) {
		return false, fmt.Errorf("graph: invalid kind %q", kind)
	}
	if kind == KindIntent && to == StateDone {
		return false, fmt.Errorf("graph: intent %d: done is reachable only via ConcludeIntent (owner/lease/confirmation/precondition checks + yields receipt), not Transition", id)
	}
	if !ValidState(kind, from) || !ValidState(kind, to) {
		return false, fmt.Errorf("graph: invalid transition %s %s -> %s", kind, from, to)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok || n.ScanID != scanID || n.Kind != kind || n.State != from {
		return false, nil
	}
	n.State = to
	if TerminalState(kind, to) || to == StateOpen || to == StateQueued {
		n.Owner = ""
		n.LeaseUntil = nil
	}
	if TerminalState(kind, to) {
		now := time.Now().UTC()
		n.CompletedAt = &now
	}
	n.ContentVersion++
	n.UpdatedAt = time.Now().UTC()
	return true, nil
}

// SettleIntent is the owner-fenced worker settlement path (see the Repository
// contract). It mirrors PGStore: state, lease release and reason are one
// atomic update, and a stale owner can never rewrite a reclaimed row.
func (m *MemoryStore) SettleIntent(_ context.Context, scanID string, id int64, owner string, to State, reason string) (bool, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return false, fmt.Errorf("graph: settle intent %d: owner is required", id)
	}
	// StatePaused parks the intent for an operator decision (INTEL_HANDOFF_SPEC
	// M3: a capability request); it is the one non-terminal settlement.
	if to != StateBlocked && to != StateExhausted && to != StateStopped && to != StateOpen && to != StatePaused {
		return false, fmt.Errorf("graph: settle intent %d: %q is not a worker settlement target", id, to)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok || n.ScanID != scanID || n.Kind != KindIntent ||
		n.State != StateRunning || n.Owner != owner {
		return false, nil
	}
	n.State = to
	n.Owner = ""
	n.LeaseUntil = nil
	if TerminalState(KindIntent, to) {
		now := time.Now().UTC()
		n.CompletedAt = &now
		if r := strings.TrimSpace(reason); r != "" {
			n.BlockedReason = r
		}
	} else {
		n.CompletedAt = nil
		if to == StatePaused {
			if r := strings.TrimSpace(reason); r != "" {
				n.BlockedReason = r
			}
		}
	}
	n.ContentVersion++
	n.UpdatedAt = time.Now().UTC()
	return true, nil
}

// SetBlockedReason records the operator-facing reason for a terminal intent
// (blocked, exhausted or stopped). It never changes state.
func (m *MemoryStore) SetBlockedReason(_ context.Context, scanID string, id int64, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok || n.ScanID != scanID || n.Kind != KindIntent {
		return nil
	}
	if n.State != StateBlocked && n.State != StateExhausted && n.State != StateStopped {
		return nil
	}
	n.BlockedReason = strings.TrimSpace(reason)
	n.ContentVersion++
	n.UpdatedAt = time.Now().UTC()
	return nil
}

// Counts aggregates header counters.
func (m *MemoryStore) Counts(_ context.Context, scanID string) (Counts, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var c Counts
	for _, n := range m.nodes {
		if n.ScanID != scanID {
			continue
		}
		switch n.Kind {
		case KindIntent:
			switch n.State {
			case StateOpen:
				c.OpenIntents++
			case StateRunning:
				c.RunningIntents++
			case StatePaused:
				c.PausedIntents++
			case StateDone:
				c.DoneIntents++
			case StateBlocked, StateExhausted, StateStopped:
				c.BlockedIntents++
			}
		case KindBelief:
			switch n.State {
			case StateQueued:
				c.QueuedBeliefs++
			case StateTesting:
				c.TestingBeliefs++
			case StateProven:
				c.ProvenBeliefs++
			}
		case KindFact:
			if n.State == StateConfirmed || n.State == StateOrigin {
				c.ConfirmedFacts++
			}
		case KindFinding:
			if n.State == StateConfirmed {
				c.ConfirmedFinding++
			}
		case KindGoal:
			switch n.State {
			case StateOpen:
				c.OpenGoals++
			case StateMet:
				c.MetGoals++
			}
		}
	}
	return c, nil
}

// PlanTasks derives the legacy PlanTask view from intent nodes.
func (m *MemoryStore) PlanTasks(_ context.Context, scanID string) ([]PlanTask, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []PlanTask
	for _, n := range m.nodes {
		if n.ScanID != scanID || n.Kind != KindIntent || n.State == StateDeleted {
			continue
		}
		out = append(out, planTaskFromNode(n))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Phase != out[j].Phase {
			return out[i].Phase < out[j].Phase
		}
		return out[i].NodeID < out[j].NodeID
	})
	return out, nil
}

// Units derives the unified intent+belief view.
func (m *MemoryStore) Units(_ context.Context, scanID string) ([]Unit, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := time.Now().UTC()
	var out []Unit
	for _, n := range m.nodes {
		if n.ScanID != scanID {
			continue
		}
		switch n.Kind {
		case KindIntent:
			if n.State == StateDeleted {
				continue
			}
			t := planTaskFromNode(n)
			out = append(out, Unit{
				Kind: "intent", NodeID: n.ID, UnitID: t.TaskID,
				Subject: taskSubject(t), State: string(n.State),
				Owner: n.Owner, LeaseUntil: n.LeaseUntil,
				VulnClass: t.VulnClass, Phase: t.Phase,
				Expired: (n.State == StateRunning || n.State == StatePaused) &&
					n.LeaseUntil != nil && n.LeaseUntil.Before(now),
			})
		case KindBelief:
			p := n.PayloadJSON()
			unitID := str(p["hypothesis_id"])
			if unitID == "" {
				unitID = fmt.Sprintf("%d", n.ID)
			}
			subject := str(p["subject"])
			if subject == "" {
				subject = unitID
			}
			out = append(out, Unit{
				Kind: "belief", NodeID: n.ID, UnitID: unitID, Subject: subject,
				State: string(n.State), Owner: n.Owner, LeaseUntil: n.LeaseUntil,
				VulnClass: str(p["vuln_class"]), Phase: intValue(p["phase"]),
				Expired: n.State == StateTesting && n.LeaseUntil != nil && n.LeaseUntil.Before(now),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Phase != out[j].Phase {
			return out[i].Phase < out[j].Phase
		}
		return out[i].NodeID < out[j].NodeID
	})
	return out, nil
}

func (m *MemoryStore) reclaimExpiredLocked(scanID string, now time.Time) int64 {
	var n int64
	for _, node := range m.nodes {
		if node.ScanID != scanID || node.LeaseUntil == nil || !node.LeaseUntil.Before(now) {
			continue
		}
		switch {
		case node.Kind == KindIntent && node.State == StateRunning:
			node.State = StateOpen
			node.Owner = ""
			node.LeaseUntil = nil
			node.CompletedAt = nil
			n++
		case node.Kind == KindBelief && node.State == StateTesting:
			node.State = StateQueued
			node.Owner = ""
			node.LeaseUntil = nil
			n++
		}
	}
	return n
}

func (m *MemoryStore) dependenciesSatisfiedLocked(n *Node) bool {
	for edge := range m.edges {
		if edge.scanID != n.ScanID || edge.dst != n.ID {
			continue
		}
		if edge.rel != RelSpawns && edge.rel != RelDerivedFrom && edge.rel != RelDependsOn {
			continue
		}
		parent, ok := m.nodes[edge.src]
		if !ok {
			continue
		}
		if parent.Kind != KindIntent && parent.Kind != KindBelief {
			continue
		}
		if !TerminalState(parent.Kind, parent.State) {
			return false
		}
	}
	return true
}

func planTaskFromNode(n *Node) PlanTask {
	p := n.PayloadJSON()
	taskID := str(p["task_id"])
	if taskID == "" {
		taskID = fmt.Sprintf("%d", n.ID)
	}
	status := "pending"
	switch n.State {
	case StateOpen:
		status = "pending"
	case StateRunning, StatePaused:
		status = "active"
	case StateDone:
		status = "completed"
	case StateBlocked, StateExhausted, StateStopped, StateDeleted:
		status = "skipped"
	}
	origin := str(p["origin"])
	if origin == "" {
		origin = n.Origin
	}
	return PlanTask{
		NodeID:       n.ID,
		TaskID:       taskID,
		InstanceID:   n.InstanceID,
		Title:        str(p["title"]),
		Phase:        intValue(p["phase"]),
		VulnClass:    str(p["vuln_class"]),
		Endpoint:     str(p["endpoint"]),
		Target:       str(p["target"]),
		Status:       status,
		GraphState:   string(n.State),
		Disposition:  str(p["disposition"]),
		Origin:       origin,
		DependsOn:    stringSlice(p["depends_on"]),
		Notes:        str(p["notes"]),
		Prerequisite: boolValue(p["prerequisite"]),
		WholeTarget:  boolValue(p["whole_target"]),
		ObjectiveRef: str(p["objective_ref"]),
		Owner:        n.Owner,
		LeaseUntil:   n.LeaseUntil,
		Priority:     n.Priority,
		CreatedAt:    n.CreatedAt,
		UpdatedAt:    n.UpdatedAt,
	}
}

func taskSubject(t PlanTask) string {
	if t.Endpoint != "" {
		return "endpoint:" + lower(t.Endpoint)
	}
	if t.VulnClass != "" {
		return fmt.Sprintf("phase:%d:%s", t.Phase, lower(t.VulnClass))
	}
	return fmt.Sprintf("phase:%d", t.Phase)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func intValue(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func boolValue(v any) bool {
	b, _ := v.(bool)
	return b
}

func stringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		if ss, ok := v.([]string); ok {
			return ss
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func lower(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'A' && r <= 'Z' {
			out[i] = r + ('a' - 'A')
		}
	}
	return string(out)
}
