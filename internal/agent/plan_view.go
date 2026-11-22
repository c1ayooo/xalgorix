package agent

import "strings"

// PlanView is the read-only projection of the root plan. Panels read this
// instead of reconstructing the plan from build_plan/update_plan tool calls
// (charter §5.1/§6: telemetry is not the state authority; the engine builds
// and refreshes most plans without ever calling those tools).
//
// Field authority: the engine-owned ScanState.Plan. Origin keeps provenance
// visible — engine-generated tasks ("auto*") and model-authored tasks ("llm")
// are both real, and a mixed plan says so instead of presenting one as the
// other.
type PlanView struct {
	Present bool `json:"present"`
	// fingerprint is the change detector used to emit plan_updated only when
	// the plan actually moved (never per turn).
	fingerprint string
	// Source is "engine" | "model" | "mixed" | "none".
	Source string `json:"source"`
	// PlanRevision is the surface fingerprint the plan was grounded in.
	PlanRevision string `json:"plan_revision,omitempty"`
	// SurfaceRevision is kept separate from PlanRevision so a stale plan is
	// visible as plan_revision != surface_revision.
	SurfaceRevision string           `json:"surface_revision,omitempty"`
	CurrentPhase    int              `json:"current_phase,omitempty"`
	Counters        PlanCountersView `json:"counters"`
	Tasks           []PlanTaskView   `json:"tasks"`
	// Partial marks a projection that cannot show the full task list (the run
	// predates plan persistence, or its detail was not captured). Counters stay
	// honest; the UI renders the reason instead of an empty plan.
	Partial       bool   `json:"partial,omitempty"`
	PartialReason string `json:"partial_reason,omitempty"`
}

// PlanCountersView mirrors PlanDisposition: executed vs skipped are separate
// (skips are justified non-coverage), unfinished is pending+active.
type PlanCountersView struct {
	Total      int `json:"total"`
	Completed  int `json:"completed"`
	Skipped    int `json:"skipped"`
	Unfinished int `json:"unfinished"`
}

// PlanTaskView is one plan task (a PlanTask is an Intent per charter §2).
type PlanTaskView struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Phase        int      `json:"phase"`
	VulnClass    string   `json:"vuln_class,omitempty"`
	Endpoint     string   `json:"endpoint,omitempty"`
	Status       string   `json:"status"`
	DependsOn    []string `json:"depends_on,omitempty"`
	Notes        string   `json:"notes,omitempty"`
	Disposition  string   `json:"disposition,omitempty"`
	Origin       string   `json:"origin,omitempty"`
	Prerequisite bool     `json:"prerequisite,omitempty"`
	WholeTarget  bool     `json:"whole_target,omitempty"`
}

// buildPlanView copies the plan out of live state. It must run on the owner
// loop (the same safe point that publishes the assessment snapshot) so no
// concurrent reader observes a task mid-mutation.
func buildPlanView(state *ScanState) *PlanView {
	view := &PlanView{Source: "none", Tasks: []PlanTaskView{}}
	if state == nil {
		return view
	}
	view.SurfaceRevision = state.PlanSurfaceRevision
	plan := state.Plan
	if plan == nil || len(plan.Tasks) == 0 {
		return view
	}
	view.Present = true
	engineTasks, modelTasks := 0, 0
	var activePhase, lastCompletedPhase int
	view.Tasks = make([]PlanTaskView, 0, len(plan.Tasks))
	for _, task := range plan.Tasks {
		if task == nil {
			continue
		}
		origin := strings.ToLower(strings.TrimSpace(task.Origin))
		if strings.HasPrefix(origin, "llm") || origin == "model" {
			modelTasks++
		} else {
			engineTasks++
		}
		if task.Status == TaskActive {
			activePhase = task.Phase
		} else if task.Status == TaskCompleted {
			lastCompletedPhase = task.Phase
		}
		view.Tasks = append(view.Tasks, PlanTaskView{
			ID:           task.ID,
			Title:        task.Title,
			Phase:        task.Phase,
			VulnClass:    task.VulnClass,
			Endpoint:     task.Endpoint,
			Status:       string(task.Status),
			DependsOn:    append([]string(nil), task.DependsOn...),
			Notes:        task.Notes,
			Disposition:  task.Disposition,
			Origin:       origin,
			Prerequisite: task.Prerequisite,
			WholeTarget:  task.WholeTarget,
		})
	}
	switch {
	case engineTasks > 0 && modelTasks > 0:
		view.Source = "mixed"
	case modelTasks > 0:
		view.Source = "model"
	default:
		view.Source = "engine"
	}
	if state.PlanSurfaceRevision != "" {
		view.PlanRevision = state.PlanSurfaceRevision
	}
	if activePhase > 0 {
		view.CurrentPhase = activePhase
	} else {
		view.CurrentPhase = lastCompletedPhase
	}
	pending, active, completed, skipped := plan.Counts()
	view.Counters = PlanCountersView{
		Total:      len(view.Tasks),
		Completed:  completed,
		Skipped:    skipped,
		Unfinished: pending + active,
	}
	var b strings.Builder
	b.WriteString(view.PlanRevision)
	for _, task := range view.Tasks {
		b.WriteByte('|')
		b.WriteString(task.ID)
		b.WriteByte(':')
		b.WriteString(task.Status)
		if task.Disposition != "" {
			b.WriteByte(':')
			b.WriteString(task.Disposition)
		}
	}
	view.fingerprint = b.String()
	return view
}

// publishPlanSnapshot rebuilds the immutable plan projection. Runs on the owner
// loop only (turn boundaries / terminal capture), so the copy can never observe
// a task mid-mutation.
func (a *Agent) publishPlanSnapshot() (PlanView, bool) {
	if a == nil || a.state == nil {
		return PlanView{Source: "none", Tasks: []PlanTaskView{}}, false
	}
	view := buildPlanView(a.state)
	changed := true
	if prev := a.publishedPlan.Load(); prev != nil && prev.fingerprint == view.fingerprint {
		changed = false
	}
	a.publishedPlan.Store(view)
	return *view, changed
}

// PlanSnapshot returns the last published plan projection. It is safe to call
// from HTTP handlers: it reads the immutable plan snapshot and never races with
// in-flight plan mutations.
func (a *Agent) PlanSnapshot() PlanView {
	if a == nil {
		return PlanView{Source: "none", Tasks: []PlanTaskView{}}
	}
	if view := a.publishedPlan.Load(); view != nil {
		return *view
	}
	return PlanView{Source: "none", Tasks: []PlanTaskView{}}
}
