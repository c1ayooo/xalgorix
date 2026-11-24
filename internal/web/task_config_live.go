package web

// task_config_live.go — hot application of task edits to an ACTIVE run.
//
// An operator can edit a task while its run is executing. The platform Task
// stays the authority (persisted first, and its revision moves so the run
// button reflects "new content"), and this file projects the change into the
// live run without restarting it:
//
//   - human-input ring (CORE §2): a source=human InfoItem + hint node describing
//     the change, so the planner/main agent OBSERVE it instead of silently
//     receiving a mutated prompt;
//   - planner + worker-pool brief: next rounds/intents use the new goal,
//     instruction and objective (planner.Notify wakes it immediately);
//   - main agent: a structured message asking it to re-read and adjust;
//   - model chain: hot-swapped endpoints (next LLM call, no abort);
//   - egress proxy: per-run pin updated + that run's tunnels force-closed, so
//     the next connection uses the new proxy;
//   - audit_mode: stamped onto the live ScanContext (the approval gate reads it
//     per call).
//
// Workers are never killed or steered here — the user's decision: new intents
// carry the new direction, the main agent decides whether to steer anything.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/orchestrator"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/proxy"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/trafficproxy"
)

// liveRunHandles are the in-process handles a task edit needs for one run.
// Registered right before the agent starts and removed at teardown.
type liveRunHandles struct {
	instanceID string
	scanCtx    *scanctx.ScanContext
	agent      *agent.Agent
	planner    *orchestrator.Planner
	pool       *orchestrator.Pool
	llmClient  *llm.Client
}

func (s *Server) registerLiveRun(h *liveRunHandles) {
	if s == nil || h == nil || h.instanceID == "" {
		return
	}
	s.liveRunsMu.Lock()
	if s.liveRuns == nil {
		s.liveRuns = make(map[string]*liveRunHandles)
	}
	s.liveRuns[h.instanceID] = h
	s.liveRunsMu.Unlock()
}

func (s *Server) unregisterLiveRun(instanceID string) {
	if s == nil || instanceID == "" {
		return
	}
	s.liveRunsMu.Lock()
	delete(s.liveRuns, instanceID)
	s.liveRunsMu.Unlock()
}

func (s *Server) liveRun(instanceID string) *liveRunHandles {
	s.liveRunsMu.RLock()
	defer s.liveRunsMu.RUnlock()
	return s.liveRuns[instanceID]
}

// applyTaskConfigToLiveRuns applies one PATCH to the task's active run, if any.
func (s *Server) applyTaskConfigToLiveRuns(ctx context.Context, oldTask, newTask *platform.Task) {
	if oldTask == nil || newTask == nil {
		return
	}
	diff := taskConfigDiff(oldTask, newTask)
	if len(diff) == 0 {
		return
	}
	store := s.currentPlatformStore()
	if store == nil {
		return
	}
	run, ok := s.taskActiveRun(ctx, store, newTask.ID)
	if !ok || strings.TrimSpace(run.InstanceID) == "" {
		return // persisted for the next dispatch; nothing live to update
	}
	h := s.liveRun(run.InstanceID)
	if h == nil {
		return // run is between agent sessions; the next dispatch reads the task
	}
	s.applyTaskConfigToLiveRun(ctx, h, newTask, diff)
}

func (s *Server) applyTaskConfigToLiveRun(ctx context.Context, h *liveRunHandles, task *platform.Task, diff []string) {
	summary := taskConfigUpdateSummary(diff, nil, task)

	// Human-input ring: the change is an observation, not a silent mutation.
	s.projectTaskConfigUpdate(h, task, summary)

	brief := taskBriefFromTask(task)
	if h.planner != nil {
		h.planner.UpdateBriefing(brief) // also Notify()s the loop
	}
	if h.pool != nil {
		h.pool.UpdateBriefing(brief)
	}

	if h.scanCtx != nil {
		h.scanCtx.AuditMode = effectiveTaskAuditMode(task, s.cfg)
	}

	if containsDiff(diff, "objective") && h.agent != nil {
		// The finish gate and the graph projection read ScanState.Objective, not
		// the planner brief. Replacing only the brief let a mid-run goal edit
		// diverge from the closing decision (P0 Mission Continuity).
		h.agent.SetObjective(agentObjectiveSpec(task.Objective))
	}

	if containsDiff(diff, "llm_chain", "active_llm") && h.llmClient != nil {
		eps := s.taskModelEndpoints(ctx, task)
		if len(eps) > 0 && !h.llmClient.SetChainEndpoints(eps) {
			log.Printf("[task-config] %s: model chain changed but the live client has a fixed endpoint (next run will use it)", h.instanceID)
		}
	}

	if containsDiff(diff, "proxy") {
		// The proxy/tunnel layers key by ScanContext ID, not the instance id.
		// Using the instance id silently left the old pin in place (XALG022).
		key := strings.TrimSpace(h.instanceID)
		if h.scanCtx != nil && strings.TrimSpace(h.scanCtx.ID) != "" {
			key = strings.TrimSpace(h.scanCtx.ID)
		}
		applyLiveEgressPin(key, task.Proxy)
		if closed := trafficproxy.CloseScanTunnels(key); closed > 0 {
			log.Printf("[task-config] %s: closed %d tunnel(s) after the egress proxy changed", key, closed)
		}
	}

	if h.agent != nil {
		msg := fmt.Sprintf("TASK CONFIG UPDATED (task %s) — re-read the goal/instruction and adjust the plan. Existing workers are NOT restarted; new intents use the updated brief.\n\n%s", task.ID, summary)
		if _, err := h.agent.SendMessage(msg); err != nil {
			log.Printf("[task-config] %s: deliver update to the main agent failed: %v", h.instanceID, err)
		} else {
			h.agent.NotifyMainSystem("Task configuration changed mid-run; the plan should be re-evaluated against the new goal/instructions.")
		}
	}
}

// projectTaskConfigUpdate writes the edit into the information space as a
// source=human observation and an R7 hint node (same path as guidance).
func (s *Server) projectTaskConfigUpdate(h *liveRunHandles, task *platform.Task, summary string) {
	if h == nil || h.scanCtx == nil {
		return
	}
	g := platform.Guidance{
		ID:         fmt.Sprintf("task-config-%s-%d", task.ID, time.Now().UnixNano()),
		TaskID:     task.ID,
		InstanceID: h.instanceID,
		Message:    summary,
		CreatedBy:  "operator:task-config",
		Status:     platform.GuidanceInjected,
		CreatedAt:  time.Now().UTC(),
	}
	s.projectGuidanceInfoItem(h.scanCtx, g)
	s.projectGuidanceHint(context.Background(), &g)
}

func taskBriefFromTask(task *platform.Task) orchestrator.LiveTaskBrief {
	brief := orchestrator.LiveTaskBrief{
		Goal:            task.Goal,
		Instruction:     task.Instruction,
		UserInstruction: task.Instruction,
	}
	if task.Objective != nil {
		brief.ObjectiveStatement = task.Objective.Statement
		brief.ObjectiveConstraints = append([]string(nil), task.Objective.Constraints...)
		for _, c := range task.Objective.Criteria {
			brief.ObjectiveCriteria = append(brief.ObjectiveCriteria, orchestrator.PlannerCriterion{
				ID: c.ID, Kind: c.Kind, VulnClass: c.VulnClass,
				Capability: c.Capability, VerifyBy: c.VerifyBy, Required: c.Required,
			})
		}
	}
	return brief
}

// taskModelEndpoints resolves the task's ordered chain (active first) into
// endpoints for the live client. Unresolvable entries are skipped; the PATCH
// validation already rejected unknown profiles.
func (s *Server) taskModelEndpoints(ctx context.Context, task *platform.Task) []llm.Endpoint {
	chain := orderedTaskLLMChain(task.LLMChain, task.ActiveLLM)
	if len(chain) == 0 && strings.TrimSpace(task.ActiveLLM) != "" {
		chain = []string{strings.TrimSpace(task.ActiveLLM)}
	}
	eps := make([]llm.Endpoint, 0, len(chain))
	for _, key := range chain {
		ep, err := s.resolveScanCredentials(ctx, ScanRequest{ProviderProfile: key}, s.cfg)
		if err != nil {
			log.Printf("[task-config] model chain entry %q did not resolve: %v", key, err)
			continue
		}
		if !isZeroEndpoint(ep) {
			eps = append(eps, ep)
		}
	}
	return eps
}

// applyLiveEgressPin pins (or clears) one run's egress selection.
func applyLiveEgressPin(instanceID, rawPin string) {
	pin := platform.NormalizeTaskProxy(rawPin)
	switch pin {
	case "":
		proxy.ClearScanUpstream(instanceID)
	case "direct":
		proxy.SetScanUpstream(instanceID, proxy.ScanUpstream{Direct: true})
	default:
		idx, err := strconv.Atoi(pin)
		if err != nil || idx <= 0 {
			return
		}
		entry := proxy.EntryAt(idx)
		if entry == nil {
			log.Printf("[task-config] %s: proxy entry %d no longer exists; keeping the previous egress", instanceID, idx)
			return
		}
		proxy.SetScanUpstream(instanceID, proxy.ScanUpstream{Entry: entry})
	}
}

// taskConfigDiff lists the run-relevant fields that changed. Name/schedule do
// not appear: they have no live-run behavior (schedule affects future
// dispatches, name is cosmetic).
func taskConfigDiff(oldTask, newTask *platform.Task) []string {
	var diff []string
	if strings.TrimSpace(oldTask.Goal) != strings.TrimSpace(newTask.Goal) {
		diff = append(diff, "goal")
	}
	if strings.TrimSpace(oldTask.Instruction) != strings.TrimSpace(newTask.Instruction) {
		diff = append(diff, "instruction")
	}
	if !reflect.DeepEqual(oldTask.Objective, newTask.Objective) {
		diff = append(diff, "objective")
	}
	if !reflect.DeepEqual(oldTask.PhasePolicy, newTask.PhasePolicy) {
		diff = append(diff, "phase_policy")
	}
	if !reflect.DeepEqual(oldTask.ContextPaths, newTask.ContextPaths) {
		diff = append(diff, "context_paths")
	}
	if !reflect.DeepEqual(oldTask.LLMChain, newTask.LLMChain) {
		diff = append(diff, "llm_chain")
	}
	if strings.TrimSpace(oldTask.ActiveLLM) != strings.TrimSpace(newTask.ActiveLLM) {
		diff = append(diff, "active_llm")
	}
	if platform.NormalizeTaskProxy(oldTask.Proxy) != platform.NormalizeTaskProxy(newTask.Proxy) {
		diff = append(diff, "proxy")
	}
	if platform.NormalizeTaskAuditMode(oldTask.AuditMode) != platform.NormalizeTaskAuditMode(newTask.AuditMode) {
		diff = append(diff, "audit_mode")
	}
	return diff
}

func containsDiff(diff []string, fields ...string) bool {
	for _, want := range fields {
		for _, got := range diff {
			if got == want {
				return true
			}
		}
	}
	return false
}

// taskConfigUpdateSummary is the human-readable change note carried by the
// InfoItem and the main-agent message. Values are truncated; secret fields
// never appear here.
func taskConfigUpdateSummary(diff []string, oldTask, newTask *platform.Task) string {
	var b strings.Builder
	b.WriteString("Task configuration changed: ")
	b.WriteString(strings.Join(diff, ", "))
	if newTask == nil {
		return b.String()
	}
	clip := func(s string, n int) string {
		s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
		if len([]rune(s)) > n {
			return string([]rune(s)[:n]) + "…"
		}
		return s
	}
	add := func(label, value string) {
		if strings.TrimSpace(value) == "" {
			value = "—"
		}
		fmt.Fprintf(&b, "\n- %s: %s", label, value)
	}
	if containsDiff(diff, "goal") {
		add("goal", clip(newTask.Goal, 300))
	}
	if containsDiff(diff, "instruction") {
		add("instruction", clip(newTask.Instruction, 300))
	}
	if containsDiff(diff, "objective") {
		st := "cleared"
		if newTask.Objective != nil {
			st = clip(newTask.Objective.Statement, 240)
		}
		add("objective", st)
	}
	if containsDiff(diff, "phase_policy") {
		if newTask.PhasePolicy == nil {
			add("phase_policy", "default")
		} else {
			add("phase_policy", fmt.Sprintf("mode=%s intensity=%s", newTask.PhasePolicy.ScanMode, newTask.PhasePolicy.Intensity))
		}
	}
	if containsDiff(diff, "context_paths") {
		add("context_paths", clip(strings.Join(newTask.ContextPaths, ", "), 240))
	}
	if containsDiff(diff, "llm_chain", "active_llm") {
		add("model", fmt.Sprintf("active=%s chain=[%s]", strings.TrimSpace(newTask.ActiveLLM), strings.Join(newTask.LLMChain, ", ")))
	}
	if containsDiff(diff, "proxy") {
		pin := platform.NormalizeTaskProxy(newTask.Proxy)
		if pin == "" {
			pin = "inherit global pool"
		}
		add("proxy", pin)
	}
	if containsDiff(diff, "audit_mode") {
		add("audit_mode", platform.NormalizeTaskAuditMode(newTask.AuditMode))
	}
	if oldTask != nil {
		// Keep the diff auditable without dumping whole documents.
		raw, _ := json.Marshal(map[string]any{
			"changed":     diff,
			"goal_before": clip(oldTask.Goal, 160),
			"goal_after":  clip(newTask.Goal, 160),
		})
		if len(raw) > 0 {
			b.WriteString("\n")
			b.Write(raw)
		}
	}
	return b.String()
}
