package agent

// plan_graph.go — PlanTask facade over the authoritative exploration graph
// (R7/M1).
//
// The graph is the authority; ScanState.Plan is a per-turn read-through view.
// All plan mutations go through this adapter: build_plan replaces intent
// nodes, update_plan CASes one node, claim_next_task uses the PG claim, and
// hookPlanner reloads the graph at the start of a turn. A process without a
// graph backend (unit tests, legacy/offline) keeps the historical in-memory
// behavior unchanged.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

const graphCallTimeout = 5 * time.Second

func planGraphID(state *ScanState) string {
	if state == nil {
		return ""
	}
	// The graph authority is run-scoped (D11): a re-dispatch/sibling session
	// must read and write the same plan under the instance identity.
	if sctx := scanctx.Get(state.ScanContextID); sctx != nil {
		if id := strings.TrimSpace(sctx.InstanceID); id != "" {
			return id
		}
	}
	return strings.TrimSpace(state.ScanContextID)
}

func planGraphContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), graphCallTimeout)
}

// planTaskPayload is the intent node payload. It is the single source for the
// PlanTask view (migration 0020 v_plan_tasks).
func planTaskPayload(t *Task) string {
	if t == nil {
		return "{}"
	}
	payload := map[string]any{
		"task_id":       t.ID,
		"title":         t.Title,
		"phase":         t.Phase,
		"vuln_class":    t.VulnClass,
		"endpoint":      t.Endpoint,
		"origin":        t.Origin,
		"depends_on":    t.DependsOn,
		"notes":         t.Notes,
		"disposition":   t.Disposition,
		"prerequisite":  t.Prerequisite,
		"whole_target":  t.WholeTarget,
		"objective_ref": t.ObjectiveRef,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// planTaskPriority maps the methodology phase to claim priority: lower phase
// first. Equal priorities fall back to insertion (node id) order.
func planTaskPriority(t *Task) int {
	if t == nil {
		return 0
	}
	phase := t.Phase
	if phase < 1 {
		phase = 1
	}
	if phase > 22 {
		phase = 22
	}
	return 1000 - phase*10
}

func planGraphState(t *Task) graph.State {
	if t == nil {
		return graph.StateOpen
	}
	switch t.Status {
	case TaskActive:
		return graph.StateRunning
	case TaskCompleted:
		return graph.StateDone
	case TaskSkipped:
		return graph.StateBlocked
	default:
		return graph.StateOpen
	}
}

// initialPlanState keeps a newly persisted completed task open until its
// receipt exists, so no row is ever born "done without yield".
func initialPlanState(target graph.State) graph.State {
	if target == graph.StateDone {
		return graph.StateOpen
	}
	return target
}

// concludePlannedIntent settles a plan-driven completion through the same
// transactional path as a worker: one origin receipt fact plus a yields edge.
// ErrConflict means a worker/planner already settled the node, which is not a
// plan failure.
func concludePlannedIntent(ctx context.Context, repo graph.Repository, scanID string, nodeID int64, t *Task) error {
	if repo == nil || t == nil || strings.TrimSpace(t.ID) == "" {
		return nil
	}
	payload, err := json.Marshal(map[string]any{
		"result_of": t.ID,
		"owner":     "plan",
		"status":    "completed",
		"nature":    "inferred",
		"summary":   "task completed by plan update",
	})
	if err != nil {
		payload = []byte(`{"status":"completed","nature":"inferred"}`)
	}
	_, err = repo.ConcludePlanned(ctx, scanID, nodeID, graph.StateDone, []graph.NodeInput{{
		ScanID: scanID, Kind: graph.KindFact, State: graph.StateOrigin,
		UnitKey: "intent-result:" + t.ID, Origin: "plan", Payload: string(payload),
	}})
	if errors.Is(err, graph.ErrConflict) {
		return nil
	}
	return err
}

func taskFromPlanTask(t graph.PlanTask) *Task {
	status := TaskPending
	switch t.Status {
	case "active":
		status = TaskActive
	case "completed":
		status = TaskCompleted
	case "skipped":
		status = TaskSkipped
	}
	out := &Task{
		ID:           t.TaskID,
		Title:        t.Title,
		Phase:        t.Phase,
		VulnClass:    t.VulnClass,
		Endpoint:     t.Endpoint,
		Status:       status,
		DependsOn:    append([]string(nil), t.DependsOn...),
		Notes:        t.Notes,
		Origin:       t.Origin,
		Disposition:  t.Disposition,
		Prerequisite: t.Prerequisite,
		WholeTarget:  t.WholeTarget,
		ObjectiveRef: t.ObjectiveRef,
		Owner:        t.Owner,
	}
	if t.LeaseUntil != nil {
		out.LeaseUntil = *t.LeaseUntil
	}
	return out
}

func taskFromGraphNode(n *graph.Node) *Task {
	if n == nil {
		return nil
	}
	p := n.PayloadJSON()
	out := &Task{
		ID:           strValue(p["task_id"]),
		Title:        strValue(p["title"]),
		Phase:        intValue(p["phase"]),
		VulnClass:    strValue(p["vuln_class"]),
		Endpoint:     strValue(p["endpoint"]),
		Status:       TaskActive,
		DependsOn:    stringSliceValue(p["depends_on"]),
		Notes:        strValue(p["notes"]),
		Origin:       strValue(p["origin"]),
		Disposition:  strValue(p["disposition"]),
		Prerequisite: boolValue(p["prerequisite"]),
		WholeTarget:  boolValue(p["whole_target"]),
		ObjectiveRef: strValue(p["objective_ref"]),
		Owner:        n.Owner,
	}
	if out.ID == "" {
		out.ID = fmt.Sprintf("%d", n.ID)
	}
	if n.LeaseUntil != nil {
		out.LeaseUntil = *n.LeaseUntil
	}
	return out
}

func strValue(v any) string {
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

func stringSliceValue(v any) []string {
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

func loadPlanFromGraph(ctx context.Context, repo graph.Repository, scanID string) (*Plan, bool) {
	if repo == nil || strings.TrimSpace(scanID) == "" {
		return nil, false
	}
	tasks, err := repo.PlanTasks(ctx, scanID)
	if err != nil || len(tasks) == 0 {
		return nil, false
	}
	plan := NewPlan()
	for _, item := range tasks {
		if item.TaskID == "" {
			continue
		}
		plan.add(taskFromPlanTask(item))
	}
	if plan.IsEmpty() {
		return nil, false
	}
	return plan, true
}

// syncPlanFromGraphState reloads ScanState.Plan from the authoritative graph.
// Root only: delegated agents must not rewrite the shared plan from a lane.
func syncPlanFromGraphState(state *ScanState) {
	if state == nil || state.DelegatedAgent {
		return
	}
	repo := graph.Backend()
	if repo == nil {
		return
	}
	ctx, cancel := planGraphContext()
	defer cancel()
	tasks, err := repo.PlanTasks(ctx, planGraphID(state))
	if err != nil {
		// The graph is the authority: do not silently keep the in-memory plan.
		state.GraphUnavailable = true
		return
	}
	state.GraphUnavailable = false
	if len(tasks) == 0 {
		return
	}
	plan := NewPlan()
	for _, item := range tasks {
		if item.TaskID == "" {
			continue
		}
		plan.add(taskFromPlanTask(item))
	}
	if plan.IsEmpty() {
		return
	}
	state.Plan = plan
	state.PlanBuilt = true
}

// planFingerprint is the change detector used to avoid rewriting the graph on
// every turn. It covers every field the graph payload/view exposes.
func planFingerprint(plan *Plan) string {
	if plan == nil {
		return ""
	}
	var b strings.Builder
	for _, t := range plan.Tasks {
		if t == nil {
			continue
		}
		fmt.Fprintf(&b, "%s|%s|%s|%s|%d|%d|%s|%s|%s|%s|%v|%v|%s|%s|%s;",
			t.ID, t.Status, t.Owner, t.LeaseUntil.UTC().Format(time.RFC3339),
			t.Phase, planTaskPriority(t), t.Title, t.VulnClass, t.Endpoint,
			t.Origin, t.Prerequisite, t.WholeTarget, t.Notes, t.Disposition,
			t.ObjectiveRef)
		b.WriteString(strings.Join(t.DependsOn, ","))
		b.WriteByte(';')
	}
	return b.String()
}

// persistPlanToGraph upserts every task and rewrites dependency edges. Nodes
// missing from the new plan are marked deleted (soft delete keeps lineage).
func persistPlanToGraph(ctx context.Context, repo graph.Repository, scanID string, plan *Plan) error {
	if repo == nil || strings.TrimSpace(scanID) == "" || plan == nil {
		return nil
	}
	existing, err := repo.PlanTasks(ctx, scanID)
	if err != nil {
		return err
	}
	byTask := make(map[string]graph.PlanTask, len(existing))
	for _, item := range existing {
		byTask[item.TaskID] = item
	}

	nodeIDs := make(map[string]int64, len(plan.Tasks))
	seen := make(map[string]bool, len(plan.Tasks))
	for _, t := range plan.Tasks {
		if t == nil || strings.TrimSpace(t.ID) == "" {
			continue
		}
		seen[t.ID] = true
		target := planGraphState(t)
		payload := planTaskPayload(t)
		if prev, ok := byTask[t.ID]; ok {
			if prev.GraphState != string(graph.StateDeleted) {
				if err := repo.UpdateNode(ctx, scanID, prev.NodeID, payload, planTaskPriority(t)); err != nil {
					return err
				}
				if prev.GraphState != string(target) {
					if target == graph.StateDone {
						if err := concludePlannedIntent(ctx, repo, scanID, prev.NodeID, t); err != nil {
							return err
						}
					} else if _, err := repo.Transition(ctx, scanID, prev.NodeID, graph.KindIntent,
						graph.State(prev.GraphState), target); err != nil {
						return err
					}
				}
				nodeIDs[t.ID] = prev.NodeID
				continue
			}
		}

		node, err := repo.AddNode(ctx, graph.NodeInput{
			ScanID:   scanID,
			Kind:     graph.KindIntent,
			UnitKey:  t.ID,
			State:    initialPlanState(target),
			Priority: planTaskPriority(t),
			Origin:   t.Origin,
			Payload:  payload,
		})
		if err != nil {
			return err
		}
		if target == graph.StateDone {
			if err := concludePlannedIntent(ctx, repo, scanID, node.ID, t); err != nil {
				return err
			}
		}
		nodeIDs[t.ID] = node.ID
	}

	// Soft-delete intents removed from the plan.
	for _, prev := range existing {
		if seen[prev.TaskID] || prev.GraphState == string(graph.StateDeleted) {
			continue
		}
		_, _ = repo.Transition(ctx, scanID, prev.NodeID, graph.KindIntent,
			graph.State(prev.GraphState), graph.StateDeleted)
	}

	// Dependency edges need every node id resolved first.
	for _, t := range plan.Tasks {
		if t == nil {
			continue
		}
		dst, ok := nodeIDs[t.ID]
		if !ok {
			continue
		}
		deps := make([]int64, 0, len(t.DependsOn))
		for _, dep := range t.DependsOn {
			if id, ok := nodeIDs[dep]; ok {
				deps = append(deps, id)
			}
		}
		if err := repo.ReplaceDependencyEdges(ctx, scanID, dst, deps); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) planGraphScanID() string {
	if a == nil || a.scanCtx == nil {
		return ""
	}
	if id := strings.TrimSpace(a.scanCtx.InstanceID); id != "" {
		return id
	}
	return strings.TrimSpace(a.scanCtx.ID)
}

func (a *Agent) hasPlanGraph() bool {
	return graph.Backend() != nil && a.planGraphScanID() != ""
}

// persistCurrentPlan writes the agent's current plan to the graph.
func (a *Agent) persistCurrentPlan() {
	repo := graph.Backend()
	scanID := a.planGraphScanID()
	if repo == nil || scanID == "" || a.state == nil || a.state.DelegatedAgent {
		return
	}
	ctx, cancel := planGraphContext()
	defer cancel()
	if err := persistPlanToGraph(ctx, repo, scanID, a.state.Plan); err != nil {
		a.state.GraphUnavailable = true
		// PG failure must not break the turn; the run pauses at the next turn
		// boundary instead of silently continuing on a divergent plan.
		a.emit(Event{Type: "error", Content: "⚠️ plan graph write failed: " + err.Error()})
		return
	}
	a.state.GraphUnavailable = false
}

// persistTaskToGraph CASes one task's lifecycle change (update_plan).
func (a *Agent) persistTaskToGraph(t *Task) {
	repo := graph.Backend()
	scanID := a.planGraphScanID()
	if repo == nil || scanID == "" || t == nil || a.state == nil || a.state.DelegatedAgent {
		return
	}
	ctx, cancel := planGraphContext()
	defer cancel()
	existing, err := repo.PlanTasks(ctx, scanID)
	if err != nil {
		a.state.GraphUnavailable = true
		return
	}
	a.state.GraphUnavailable = false
	var prev *graph.PlanTask
	for i := range existing {
		if existing[i].TaskID == t.ID {
			prev = &existing[i]
			break
		}
	}
	payload := planTaskPayload(t)
	target := planGraphState(t)
	if prev == nil {
		node, err := repo.AddNode(ctx, graph.NodeInput{
			ScanID: scanID, Kind: graph.KindIntent, UnitKey: t.ID, State: initialPlanState(target),
			Priority: planTaskPriority(t), Origin: t.Origin, Payload: payload,
		})
		if err != nil {
			a.state.GraphUnavailable = true
			return
		}
		if target == graph.StateDone {
			if err := concludePlannedIntent(ctx, repo, scanID, node.ID, t); err != nil {
				a.state.GraphUnavailable = true
			}
		}
		return
	}
	if err := repo.UpdateNode(ctx, scanID, prev.NodeID, payload, planTaskPriority(t)); err != nil {
		a.state.GraphUnavailable = true
		return
	}
	if prev.GraphState == string(target) {
		return
	}
	if target == graph.StateDone {
		if err := concludePlannedIntent(ctx, repo, scanID, prev.NodeID, t); err != nil {
			a.state.GraphUnavailable = true
		}
		return
	}
	if _, err := repo.Transition(ctx, scanID, prev.NodeID, graph.KindIntent,
		graph.State(prev.GraphState), target); err != nil {
		a.state.GraphUnavailable = true
	}
}

// beginPlanGraphTurn reloads the root plan from the graph and returns a
// closure that persists any change made during this hook/turn. Call as
// `defer beginPlanGraphTurn(state)()`.
func beginPlanGraphTurn(state *ScanState) func() {
	if state == nil || state.DelegatedAgent {
		return func() {}
	}
	repo := graph.Backend()
	if repo == nil || planGraphID(state) == "" {
		return func() {}
	}
	syncPlanFromGraphState(state)
	before := planFingerprint(state.Plan)
	return func() {
		if planFingerprint(state.Plan) == before {
			return
		}
		ctx, cancel := planGraphContext()
		defer cancel()
		_ = persistPlanToGraph(ctx, repo, planGraphID(state), state.Plan)
	}
}

// claimNextTaskFromGraph is the authoritative claim path for claim_next_task.
func (a *Agent) claimNextTaskFromGraph() (*Task, error) {
	repo := graph.Backend()
	scanID := a.planGraphScanID()
	if repo == nil || scanID == "" || a.state == nil {
		return nil, nil
	}
	ctx, cancel := planGraphContext()
	defer cancel()
	node, err := repo.ClaimNextIntent(ctx, scanID, a.claimOwner(), DefaultTaskLease)
	if err != nil {
		if a.state != nil {
			a.state.GraphUnavailable = true
		}
		return nil, err
	}
	if a.state != nil {
		a.state.GraphUnavailable = false
	}
	if node == nil {
		return nil, nil
	}
	task := taskFromGraphNode(node)
	if task == nil {
		return nil, nil
	}
	if a.state.Plan == nil {
		a.state.Plan = NewPlan()
	}
	if existing := a.state.Plan.Get(task.ID); existing != nil {
		existing.Status = TaskActive
		existing.Owner = task.Owner
		existing.LeaseUntil = task.LeaseUntil
	} else {
		a.state.Plan.add(task)
	}
	a.state.PlanBuilt = true
	return task, nil
}
