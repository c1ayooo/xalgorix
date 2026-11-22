// Execution trace model for the task-detail session console.
//
// The transport is unchanged (WS events + paginated REST history); this module
// only turns a flat event stream into something an operator can read as work:
//
//   • planning — reconstructed from build_plan / update_plan tool calls, which
//     carry the full task array and per-task status transitions as arguments.
//     No new backend endpoint is involved; if the engine auto-planned without
//     calling build_plan, the per-task list simply is not available (callers
//     fall back to the instance's plan_tasks_* counters).
//   • execution — tool_call/tool_result pairs are bound into one step (a pair
//     is matched FIFO per agent+tool, because parallel agents can interleave),
//     thinking/phase events become round separators, and approval/finding/
//     lifecycle events stay as one-line notices.
//
// Everything here is deterministic over the same event array so pagination and
// live merges produce the same transcript.

import type { FeedEvent } from "@/store/events";

export type PlanTaskStatus = "pending" | "in_progress" | "completed" | "skipped";

export interface PlanTask {
  id: string;
  title: string;
  phase?: number;
  vulnClass?: string;
  endpoint?: string;
  status: PlanTaskStatus;
  notes?: string;
  updatedAt?: number;
}

export interface PlanState {
  tasks: PlanTask[];
  /** Number of successful build_plan calls observed (the last one wins). */
  revisions: number;
  updatedAt?: number;
  /** "event" = task list parsed from events; "none" = not reported. */
  source: "event" | "none";
  /** True when history was paginated or a plan payload was truncated. */
  incomplete: boolean;
  incompleteReason?: "history" | "truncated" | "unparsable";
}

export interface PlanProgress {
  total: number;
  completed: number;
  skipped: number;
  inProgress: number;
  pending: number;
  /** Share of disposed tasks (completed + skipped) over total, 0..1. */
  ratio: number;
  /** Where the numbers came from: parsed plan or the instance counters. */
  source: "plan" | "instance" | "none";
  current?: PlanTask;
}

export type TraceItem =
  | { kind: "round"; key: string; at: number; label: string }
  | { kind: "thought"; key: string; at: number; text: string }
  | { kind: "tool"; key: string; at: number; use?: FeedEvent; result?: FeedEvent }
  | { kind: "answer"; key: string; at: number; event: FeedEvent }
  | { kind: "human"; key: string; at: number; event: FeedEvent }
  | { kind: "approval"; key: string; at: number; event: FeedEvent }
  | { kind: "plan"; key: string; at: number; event: FeedEvent }
  | { kind: "notice"; key: string; at: number; event: FeedEvent };

const PLAN_TOOLS = new Set(["build_plan", "update_plan"]);

function eventAt(e: FeedEvent): number {
  const parsed = e.timestamp ? Date.parse(e.timestamp) : NaN;
  return Number.isFinite(parsed) ? parsed : e._receivedAt || 0;
}

function parseJSON(raw?: string): { value: unknown; truncated: boolean } {
  const text = (raw ?? "").trim();
  if (!text) return { value: null, truncated: false };
  try {
    return { value: JSON.parse(text), truncated: false };
  } catch {
    // Tolerate a truncated array by closing it before the last complete object.
    // The caller must surface `truncated` instead of treating the recovered
    // prefix as the complete plan.
    const start = text.indexOf("[");
    if (start < 0) return { value: null, truncated: false };
    const end = text.lastIndexOf("}");
    if (end <= start) return { value: null, truncated: false };
    try {
      return {
        value: JSON.parse(`${text.slice(start, end + 1)}]`),
        truncated: true,
      };
    } catch {
      return { value: null, truncated: true };
    }
  }
}

function normalizePlanStatus(raw?: string): PlanTaskStatus {
  const s = (raw ?? "").trim().toLowerCase();
  if (s === "active" || s === "in_progress" || s === "running") return "in_progress";
  if (s === "completed" || s === "done") return "completed";
  if (!s || s === "pending" || s === "queued" || s === "todo") return "pending";
  // Typed dispositions (not_applicable / blocked_* / exhausted / superseded …)
  // all mean "ruled out", i.e. skipped with a reason.
  return "skipped";
}

interface PendingCall {
  event: FeedEvent;
  pairKey: string;
  assigned: boolean;
}

/**
 * Bind results to calls without ever crossing agent boundaries. Results that
 * omit tool_name fall back to the same agent's oldest unassigned call.
 */
function pairToolResults(events: FeedEvent[]): Map<FeedEvent, FeedEvent> {
  const paired = new Map<FeedEvent, FeedEvent>();
  const byPair = new Map<string, PendingCall[]>();
  const byAgent = new Map<string, PendingCall[]>();

  for (const event of events) {
    if (event.type === "tool_call" && event.tool_name) {
      const call: PendingCall = {
        event,
        pairKey: toolPairKey(event),
        assigned: false,
      };
      const pairQueue = byPair.get(call.pairKey) ?? [];
      pairQueue.push(call);
      byPair.set(call.pairKey, pairQueue);
      const agentQueue = byAgent.get(event.agent_id ?? "") ?? [];
      agentQueue.push(call);
      byAgent.set(event.agent_id ?? "", agentQueue);
      continue;
    }
    if (
      event.type !== "tool_result" &&
      event.type !== "tool_output" &&
      event.type !== "tool_error"
    ) {
      continue;
    }
    let call: PendingCall | undefined;
    if (event.tool_name) {
      call = (byPair.get(toolPairKey(event)) ?? []).find((c) => !c.assigned);
    } else {
      call = (byAgent.get(event.agent_id ?? "") ?? []).find((c) => !c.assigned);
    }
    if (!call) continue;
    call.assigned = true;
    paired.set(call.event, event);
  }
  return paired;
}

function toolResultSucceeded(event: FeedEvent): boolean {
  return event.type !== "tool_error" && !event.error?.trim();
}

/**
 * Reconstruct the plan timeline from successful build_plan / update_plan tool
 * calls. Later build_plan calls replace the task list; update_plan mutates one
 * task. Rejected calls are skipped, and truncated / paginated history is
 * surfaced as incomplete instead of being presented as the full plan.
 */
export function extractPlan(events: FeedEvent[], historyIncomplete = false): PlanState {
  const tasks = new Map<string, PlanTask>();
  const paired = pairToolResults(events);
  let revisions = 0;
  let updatedAt: number | undefined;
  let incompleteReason: PlanState["incompleteReason"];

  for (const e of events) {
    if (e.type !== "tool_call" || !e.tool_name || !PLAN_TOOLS.has(e.tool_name)) {
      continue;
    }
    const result = paired.get(e);
    if (result && !toolResultSucceeded(result)) {
      continue;
    }
    const at = eventAt(e);
    const args = e.tool_args ?? {};

    if (e.tool_name === "build_plan") {
      const parsed = parseJSON(args.tasks);
      if (parsed.truncated) incompleteReason = "truncated";
      if (!Array.isArray(parsed.value) || parsed.value.length === 0) {
        if ((args.tasks ?? "").trim() && !parsed.truncated) {
          incompleteReason = incompleteReason ?? "unparsable";
        }
        continue;
      }
      tasks.clear();
      for (const raw of parsed.value) {
        if (!raw || typeof raw !== "object") continue;
        const obj = raw as Record<string, unknown>;
        const id = String(obj.id ?? "").trim();
        if (!id) continue;
        const phase = Number(obj.phase);
        tasks.set(id, {
          id,
          title: String(obj.title ?? "").trim() || id,
          phase: Number.isFinite(phase) && phase > 0 ? phase : undefined,
          vulnClass: typeof obj.vuln_class === "string" ? obj.vuln_class : undefined,
          endpoint: typeof obj.endpoint === "string" ? obj.endpoint : undefined,
          status: "pending",
          notes: typeof obj.notes === "string" ? obj.notes : undefined,
        });
      }
      if (tasks.size > 0) {
        revisions += 1;
        updatedAt = at;
      }
      continue;
    }

    // update_plan
    const status = (args.status ?? "").trim();
    if (!status) continue;
    const id = (args.task_id ?? "").trim();
    if (!id) {
      // Omitted id = transition the single active task, or the next pending one
      // when marking something active.
      const target =
        [...tasks.values()].find((t) => t.status === "in_progress") ??
        (normalizePlanStatus(status) === "in_progress"
          ? [...tasks.values()].find((t) => t.status === "pending")
          : undefined);
      if (!target) continue;
      target.status = normalizePlanStatus(status);
      target.notes = (args.notes ?? "").trim() || target.notes;
      target.updatedAt = at;
      updatedAt = at;
      continue;
    }
    const task = tasks.get(id);
    if (!task) continue;
    task.status = normalizePlanStatus(status);
    task.notes = (args.notes ?? "").trim() || task.notes;
    task.updatedAt = at;
    updatedAt = at;
  }

  if (historyIncomplete) incompleteReason = incompleteReason ?? "history";
  return {
    tasks: [...tasks.values()],
    revisions,
    updatedAt,
    source: tasks.size > 0 ? "event" : "none",
    incomplete: incompleteReason !== undefined,
    incompleteReason,
  };
}

/**
 * Plan progress for the rail. Prefers the parsed task list; falls back to the
 * instance's plan_tasks_* counters (the engine's own tally, which also covers
 * auto-built plans that never emitted build_plan).
 */
export function planProgress(
  plan: PlanState,
  instance?: {
    plan_present?: boolean;
    plan_tasks_total?: number;
    plan_tasks_completed?: number;
    plan_tasks_skipped?: number;
    plan_tasks_unfinished?: number;
  },
): PlanProgress {
  if (plan.tasks.length > 0) {
    let completed = 0;
    let skipped = 0;
    let inProgress = 0;
    let pending = 0;
    for (const task of plan.tasks) {
      if (task.status === "completed") completed += 1;
      else if (task.status === "skipped") skipped += 1;
      else if (task.status === "in_progress") inProgress += 1;
      else pending += 1;
    }
    const total = plan.tasks.length;
    return {
      total,
      completed,
      skipped,
      inProgress,
      pending,
      ratio: total > 0 ? (completed + skipped) / total : 0,
      source: "plan",
      current:
        plan.tasks.find((t) => t.status === "in_progress") ??
        [...plan.tasks].reverse().find((t) => t.status !== "pending"),
    };
  }

  const total = instance?.plan_tasks_total ?? 0;
  if (!instance?.plan_present && total === 0) {
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
  const completed = instance?.plan_tasks_completed ?? 0;
  const skipped = instance?.plan_tasks_skipped ?? 0;
  const pending = Math.max(0, instance?.plan_tasks_unfinished ?? total - completed - skipped);
  return {
    total,
    completed,
    skipped,
    inProgress: 0,
    pending,
    ratio: total > 0 ? (completed + skipped) / total : 0,
    source: "instance",
  };
}

/** Compact one-line rendering of a tool call's most meaningful argument. */
export function toolSummary(event?: FeedEvent): string {
  if (!event) return "";
  const args = event.tool_args ?? {};
  const preferred = [
    "command",
    "url",
    "endpoint",
    "target",
    "path",
    "query",
    "selector",
    "message",
    "instruction",
    "file_path",
  ];
  for (const key of preferred) {
    const value = args[key];
    if (value && value.trim()) return value.trim();
  }
  const first = Object.entries(args).find(([, v]) => v && v.trim());
  if (first) return first[1].trim();
  // Older/legacy emitters put a human-readable rendering in `content`
  // (e.g. "nuclei -rl 60 -u https://…"); reuse it instead of showing nothing.
  return (event.content ?? "").trim();
}

function toolPairKey(e: FeedEvent): string {
  return `${e.agent_id ?? ""}\u0000${e.tool_name ?? ""}`;
}

function thinkingLabel(content?: string): string {
  const text = (content ?? "").trim();
  if (!text) return "";
  return text;
}

/**
 * Group a flat event stream into renderable transcript items. The order of
 * `events` is preserved (callers pass mergeFeedEvents output, already sorted).
 */
export function buildTrace(events: FeedEvent[]): TraceItem[] {
  const out: TraceItem[] = [];
  const paired = pairToolResults(events);
  const pairedResults = new Set(paired.values());
  let lastRound = "";

  const pushRound = (key: string, at: number, label: string) => {
    if (!label || label === lastRound) return;
    lastRound = label;
    out.push({ kind: "round", key, at, label });
  };

  events.forEach((event, index) => {
    const at = eventAt(event);
    const key = event._key || `${index}`;
    const type = event.type || "";

    if (type === "thinking" || type === "thought") {
      const text = thinkingLabel(event.content);
      // A bare "Iteration N/M" marker is a round boundary; free-form reasoning
      // stays a compact thought row (it is content, not a separator).
      if (/^Iteration\s+\d+/i.test(text)) {
        pushRound(key, at, text);
      } else if (text) {
        out.push({ kind: "thought", key, at, text });
      }
      return;
    }
    if (type === "phase") {
      const phase = event.current_phase;
      pushRound(key, at, phase ? `phase ${phase}` : (event.content ?? "").trim());
      return;
    }
    if (type === "assistant") {
      out.push({ kind: "answer", key, at, event });
      return;
    }
    if (type === "guidance_injected" || type === "guidance_queued") {
      out.push({ kind: "human", key, at, event });
      return;
    }
    if (type === "approval_requested") {
      out.push({ kind: "approval", key, at, event });
      return;
    }
    if (type === "tool_call") {
      if (event.tool_name && PLAN_TOOLS.has(event.tool_name)) {
        // Empty update_plan calls are bookkeeping noise; only surface a plan
        // card when the call actually names a task or a status.
        const args = event.tool_args ?? {};
        if (
          event.tool_name === "update_plan" &&
          !(args.task_id ?? "").trim() &&
          !(args.status ?? "").trim()
        ) {
          return;
        }
        out.push({ kind: "plan", key, at, event });
        return;
      }
      const group: Extract<TraceItem, { kind: "tool" }> = {
        kind: "tool",
        key,
        at,
        use: event,
        result: paired.get(event),
      };
      out.push(group);
      return;
    }
    if (type === "tool_result" || type === "tool_output" || type === "tool_error") {
      // Paired results are rendered inside their call step; only orphan
      // results remain as standalone notices.
      if (!pairedResults.has(event)) {
        out.push({ kind: "notice", key, at, event });
      }
      return;
    }
    if (
      type === "vuln" ||
      type === "vuln_found" ||
      type === "vulns" ||
      type === "error" ||
      type === "approval_decided" ||
      type === "decision" ||
      type === "report_ready" ||
      type === "target_started" ||
      type === "target_completed" ||
      type === "queue_started" ||
      type === "queue_finished" ||
      type === "stopped" ||
      type === "paused" ||
      type === "resumed" ||
      type === "guidance_consumed" ||
      type === "recovery"
    ) {
      out.push({ kind: "notice", key, at, event });
      return;
    }
    if (event.content || event.output || event.error) {
      out.push({ kind: "notice", key, at, event });
    }
  });

  return out;
}

/** True only while the Run is active and the call has no result yet. */
export function toolRunning(
  item: Extract<TraceItem, { kind: "tool" }>,
  active = true,
): boolean {
  return active && !!item.use && !item.result;
}

/** A step/plan event's display summary, without the tool name prefix. */
export function planEventLabel(event: FeedEvent): string {
  const args = event.tool_args ?? {};
  if (event.tool_name === "build_plan") {
    const parsed = parseJSON(args.tasks).value;
    const n = Array.isArray(parsed) ? parsed.length : 0;
    return n > 0 ? `${n}` : "";
  }
  const id = (args.task_id ?? "").trim();
  const status = (args.status ?? "").trim();
  return [id, status].filter(Boolean).join(" · ");
}
