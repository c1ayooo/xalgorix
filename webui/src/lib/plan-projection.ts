import type { PlanProgress, PlanState, PlanTask, PlanTaskStatus } from "@/lib/execution-trace";
import type { ScanPlanView } from "@/types/api";

/**
 * Adapters from the plan projection (GET /api/scans/{id}/plan) to the view
 * model the plan rail already renders.
 *
 * Why this exists: the previous path reconstructed the plan from
 * build_plan/update_plan tool-call telemetry. The engine builds and refreshes
 * most plans itself without emitting those calls, so the Planner pane was empty
 * on engine-authored runs and after a resume. The projection is the state
 * authority (charter §5/§6); events stay as fallback only.
 */

const FALLBACK_STATUS: Record<string, PlanTaskStatus> = {
  pending: "pending",
  active: "in_progress",
  in_progress: "in_progress",
  completed: "completed",
  skipped: "skipped",
};

export function planTasksFromProjection(view?: ScanPlanView): PlanTask[] {
  if (!view?.tasks?.length) return [];
  return view.tasks.map((task) => ({
    id: task.id,
    title: task.title || task.id,
    phase: task.phase > 0 ? task.phase : undefined,
    vulnClass: task.vuln_class,
    endpoint: task.endpoint,
    status: FALLBACK_STATUS[task.status] ?? "pending",
    notes: task.notes,
  }));
}

export function planStateFromProjection(view?: ScanPlanView): PlanState {
  const tasks = planTasksFromProjection(view);
  const present = !!view?.present;
  return {
    tasks,
    revisions: present ? 1 : 0,
    source: present ? "projection" : "none",
    incomplete: !!view?.partial,
    incompleteReason:
      view?.partial_reason === "not_persisted"
        ? "not_persisted"
        : view?.partial_reason === "cold_path"
          ? "truncated"
          : view?.partial
            ? "history"
            : undefined,
  };
}

export function planProgressFromProjection(view?: ScanPlanView): PlanProgress {
  const counters = view?.counters;
  const total = counters?.total ?? 0;
  if (!total && !view?.present) {
    return {
      total: 0,
      completed: 0,
      skipped: 0,
      inProgress: 0,
      pending: 0,
      ratio: 0,
      source: "none",
    };
  }
  const completed = counters?.completed ?? 0;
  const skipped = counters?.skipped ?? 0;
  const unfinished = counters?.unfinished ?? Math.max(0, total - completed - skipped);
  const tasks = planTasksFromProjection(view);
  const current =
    tasks.find((task) => task.status === "in_progress") ??
    [...tasks].reverse().find((task) => task.status !== "pending");
  return {
    total,
    completed,
    skipped,
    inProgress: tasks.filter((task) => task.status === "in_progress").length,
    pending: unfinished,
    ratio: total > 0 ? (completed + skipped) / total : 0,
    source: tasks.length > 0 ? "plan" : "instance",
    current,
  };
}

/** 计划来源徽标文案键（engine / model / mixed / none）。 */
export function planSourceLabelKey(source?: ScanPlanView["source"]): string {
  switch (source) {
    case "engine":
      return "sessions.planSourceEngine";
    case "model":
      return "sessions.planSourceModel";
    case "mixed":
      return "sessions.planSourceMixed";
    default:
      return "sessions.planSourceNone";
  }
}
