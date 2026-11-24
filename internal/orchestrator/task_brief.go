package orchestrator

// LiveTaskBrief is the live, hot-updatable slice of a run's human-authored task
// content: goal, instructions and the derived objective. It is separate from
// PlannerConfig/Pool config because an operator may edit the task while the
// run is active; the engine must let the planner and the main agent see the
// new direction without restarting the run.
//
// Authority stays with the platform Task (persisted first); this struct is the
// in-process projection a run reads each round. Updates are serialized so the
// planner loop and the HTTP handler never race on the brief.
type LiveTaskBrief struct {
	Goal                 string
	Instruction          string
	UserInstruction      string
	ObjectiveStatement   string
	ObjectiveConstraints []string
	ObjectiveCriteria    []PlannerCriterion
}

// briefFromPlannerConfig projects the startup config onto a LiveTaskBrief.
func briefFromPlannerConfig(cfg PlannerConfig) LiveTaskBrief {
	return LiveTaskBrief{
		Goal:                 cfg.Goal,
		Instruction:          cfg.Instruction,
		UserInstruction:      cfg.UserInstruction,
		ObjectiveStatement:   cfg.ObjectiveStatement,
		ObjectiveConstraints: append([]string(nil), cfg.ObjectiveConstraints...),
		ObjectiveCriteria:    append([]PlannerCriterion(nil), cfg.ObjectiveCriteria...),
	}
}

func (b LiveTaskBrief) clone() LiveTaskBrief {
	b.ObjectiveConstraints = append([]string(nil), b.ObjectiveConstraints...)
	b.ObjectiveCriteria = append([]PlannerCriterion(nil), b.ObjectiveCriteria...)
	return b
}
