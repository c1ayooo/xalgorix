package web

// goal_analysis.go — operator goal intent analysis (REDESIGN §3.1, R-goal).
// One LLM call turns a vague task goal into a structured objective draft
// (rewritten statement + decidable criteria + operator-stated constraints).
//
// Two entry points share this file's machinery:
//   - POST /api/projects/{id}/analyze-goal: a pure preview for the task form
//     ("分析目标" button). It never persists anything.
//   - the round-0 auto analysis at task run start (orchestrator.go): when a
//     task has a goal but no structured objective, analyzeGoalForRun persists
//     the normalized objective on the task before the agent starts; failures
//     fall back to running the raw goal (fail-open, XALGORIX_GOAL_ANALYSIS).
//
// The evaluation boundary is unchanged: what the analyzer produces is still
// operator-stated intent — the evaluator only ever observes findings the
// report pipeline accepted (internal/objective); finding labels stay with
// report_vulnerability.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/goalanalysis"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

// Sentinel errors for the analyzer availability states. They never fail a run:
// the round-0 path maps both to "run the raw goal".
var (
	errGoalAnalysisUnavailable = errors.New("goalanalysis: no LLM function wired")
	errGoalAnalysisDisabled    = errors.New("goalanalysis: disabled by configuration")
)

// goalAnalysisTimeout bounds the single analyzer LLM call, both for the
// preview endpoint and the round-0 path (which must not stall dispatch).
const goalAnalysisTimeout = 120 * time.Second

// roundZeroGoalAnalysis fills req.Objective when a task-sourced run carries a
// raw goal but no structured objective: one analyzer call, persist the
// normalized objective on the task (best-effort), then let this run use it.
// The run never fails on analysis — any error logs, broadcasts the skip and
// leaves the raw goal in place (fail-open, same degradation philosophy as the
// approval/coldpath layers). Legacy /api/scan runs (no objective coordinates)
// return immediately: byte-identical behavior.
func (s *Server) roundZeroGoalAnalysis(req *ScanRequest) {
	if req.Objective != nil || req.ObjectiveTaskID == "" || req.ObjectiveProjectID == "" || req.ObjectiveGoal == "" {
		return
	}
	store := s.currentPlatformStore()
	if store == nil {
		return
	}
	cfg := s.configSnapshot()
	if cfg == nil || !cfg.GoalAnalysis {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), goalAnalysisTimeout+5*time.Second)
	defer cancel()
	task, err := store.GetTask(ctx, req.ObjectiveTaskID)
	if err != nil {
		log.Printf("[goal-analysis] task %s lookup: %v — running the raw goal", req.ObjectiveTaskID, err)
		s.broadcastGoalAnalyzed(req.ObjectiveTaskID, req.ObjectiveProjectID, "", nil, "skipped", err)
		return
	}
	if task.Objective != nil {
		// Structured in the meantime (form save, or an earlier round-0 /
		// queue resume): adopt it, never re-analyze over the operator's text.
		req.Objective = task.Objective
		return
	}
	project, err := store.GetProject(ctx, req.ObjectiveProjectID)
	if err != nil {
		log.Printf("[goal-analysis] task %s project lookup: %v — running the raw goal", task.ID, err)
		s.broadcastGoalAnalyzed(task.ID, req.ObjectiveProjectID, task.Name, nil, "skipped", err)
		return
	}
	obj, notes, err := s.analyzeGoalForRun(ctx, task, project)
	if err != nil {
		if !errors.Is(err, errGoalAnalysisDisabled) && !errors.Is(err, errGoalAnalysisUnavailable) {
			log.Printf("[goal-analysis] task %s: %v — running the raw goal", task.ID, err)
		}
		s.broadcastGoalAnalyzed(task.ID, task.ProjectID, task.Name, nil, "skipped", err)
		return
	}
	// Persist so future runs, the objective panel and the goal gate see the
	// same criteria. A wedged store must not undo the run: the analyzed
	// objective stays on the request either way.
	if _, err := store.UpdateTask(ctx, task.ID, platform.TaskUpdate{Objective: obj}); err != nil {
		log.Printf("[goal-analysis] task %s: persist objective: %v (run continues with it)", task.ID, err)
	}
	req.Objective = obj
	log.Printf("[goal-analysis] task %s: goal structured into %d criteria%s", task.ID, len(obj.Criteria), noteSuffix(notes))
	s.broadcastGoalAnalyzed(task.ID, task.ProjectID, task.Name, obj, "analyzed", nil)
}

// broadcastGoalAnalyzed emits the task-level goal_analyzed dashboard event
// (web-layer emission only, same discipline as approval/guidance events). The
// analyzer statement rides in ResultMeta so consumers never parse prose.
// analyzeErr stays generic on the wire — the full error (which can embed
// provider endpoints) is already in the server log; broadcast clients get
// the status only.
func (s *Server) broadcastGoalAnalyzed(taskID, projectID, taskName string, obj *platform.Objective, status string, analyzeErr error) {
	name := taskName
	if name == "" {
		name = taskID
	}
	evt := WSEvent{
		Type:    "goal_analyzed",
		Content: fmt.Sprintf("Goal analysis (%s) for task %s", status, name),
		ResultMeta: map[string]any{
			"task_id":    taskID,
			"project_id": projectID,
			"status":     status,
		},
	}
	if obj != nil {
		evt.ResultMeta["statement"] = obj.Statement
		evt.ResultMeta["criteria"] = len(obj.Criteria)
	}
	if analyzeErr != nil {
		evt.Error = "goal analysis skipped; running the raw goal"
	}
	s.broadcast(evt)
}

// noteSuffix renders the analyzer notes for the log line (empty → "").
func noteSuffix(notes string) string {
	if n := strings.TrimSpace(notes); n != "" {
		return ": " + n
	}
	return ""
}

// analyzeGoalForRun is the round-0 entry: analyze the task's raw goal and
// return the normalized objective plus the analyzer notes. It never fails a
// run — callers fall back to the raw goal on any error.
func (s *Server) analyzeGoalForRun(ctx context.Context, task *platform.Task, project *platform.Project) (*platform.Objective, string, error) {
	cfg := s.configSnapshot()
	if cfg == nil || !cfg.GoalAnalysis {
		return nil, "", errGoalAnalysisDisabled
	}
	proposal, err := s.analyzeGoalLLMCall(ctx, task.Goal, task.Instruction, project)
	if err != nil {
		return nil, "", err
	}
	obj, err := platform.NormalizeObjective(proposal.Objective())
	if err != nil {
		return nil, "", fmt.Errorf("goalanalysis: normalized proposal rejected: %w", err)
	}
	return obj, proposal.Notes, nil
}

func (s *Server) analyzeGoalLLMCall(ctx context.Context, goal, instruction string, project *platform.Project) (*goalanalysis.Proposal, error) {
	if s.goalAnalyzeLLM == nil {
		return nil, errGoalAnalysisUnavailable
	}
	actx, cancel := context.WithTimeout(ctx, goalAnalysisTimeout)
	defer cancel()
	return goalanalysis.Analyze(actx, goalanalysis.AnalyzeInput{
		Goal:         goal,
		Instruction:  instruction,
		ScopeTargets: flattenPlatformTargets(project.Scope.Targets),
	}, s.goalAnalyzeLLM)
}

// goalCriterionDTO / goalProposalDTO are the wire shape of a proposal. The
// criterion shape mirrors platform.Criterion (minus server-assigned IDs) so
// the task form can apply it directly to its objective editor.
type goalCriterionDTO struct {
	Kind       string `json:"kind"`
	VulnClass  string `json:"vuln_class,omitempty"`
	Capability string `json:"capability,omitempty"`
	VerifyBy   string `json:"verify_by"`
	Required   bool   `json:"required"`
	Rationale  string `json:"rationale,omitempty"`
}

type goalProposalDTO struct {
	Statement   string             `json:"statement"`
	Criteria    []goalCriterionDTO `json:"criteria,omitempty"`
	Constraints []string           `json:"constraints,omitempty"`
	Notes       string             `json:"notes,omitempty"`
}

// handleAnalyzeGoal serves POST /api/projects/{id}/analyze-goal. It is a
// preview only: the response carries the proposal for the operator to edit in
// the task form; nothing is written to the store.
func (s *Server) handleAnalyzeGoal(w http.ResponseWriter, r *http.Request, store platform.Repository, projectID string) {
	if r.Method != http.MethodPost {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	limitJSONBody(w, r)
	var req struct {
		Goal        string `json:"goal"`
		Instruction string `json:"instruction"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	goal := strings.TrimSpace(req.Goal)
	if goal == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "goal is required"})
		return
	}
	project, err := store.GetProject(r.Context(), projectID)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	proposal, err := s.analyzeGoalLLMCall(r.Context(), goal, req.Instruction, project)
	if err != nil {
		if errors.Is(err, errGoalAnalysisUnavailable) {
			writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "goal analysis is unavailable on this server"})
			return
		}
		log.Printf("[goal-analysis] project %s: %v", projectID, err)
		writeJSONStatus(w, http.StatusBadGateway, map[string]string{"error": "goal analysis failed: " + err.Error()})
		return
	}
	dto := goalProposalDTO{Statement: proposal.Statement, Constraints: proposal.Constraints, Notes: proposal.Notes}
	for _, c := range proposal.Criteria {
		dto.Criteria = append(dto.Criteria, goalCriterionDTO{
			Kind:       c.Kind,
			VulnClass:  c.VulnClass,
			Capability: c.Capability,
			VerifyBy:   c.VerifyBy,
			Required:   c.Required,
			Rationale:  c.Rationale,
		})
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"proposal": dto})
}
