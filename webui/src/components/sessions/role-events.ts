import { toFeedEvent, type FeedEvent } from "@/store/events";
import type { WSEvent } from "@/types/api";

/**
 * 会话页角色会话的事件谓词（docs/SESSIONS_CHAT_1TO1_PLAN.md A 档之上的
 * 角色扩展，按宪章七环对齐）：规划（build_plan/update_plan）、决策
 * （decision）、系统审计（phase/生命周期）。思考流（thought/thinking）经
 * 用户裁决不做角色组（2026-11-22），只在主会话转录/执行轨迹里内联呈现。
 * 数据源 = 实例事件缓冲 + SSE 合并；转录七类契约不受影响。
 */

export type SessionRole = "plan" | "decision" | "system";

const ROLE_KEYS: readonly SessionRole[] = ["plan", "decision", "system"];

export function isSessionRole(lane: string | undefined): lane is SessionRole {
  return (ROLE_KEYS as readonly string[]).includes(lane ?? "");
}

/** 角色 → 观测的类型集合（会话分组只在这些事件存在时出现，不编造）。
 *  决策 = 信念环的真实动作：账本假设登记/补证工具调用（引擎无独立 decision
 *  事件类型）。渲染统一走操作者既有的 ExecutionTrace 分组动态内容。 */
const ROLE_EVENT_TYPES: Record<SessionRole, Set<string>> = {
  plan: new Set(["tool_call", "tool_result", "tool_error"]),
  decision: new Set(["tool_call", "tool_result", "tool_error"]),
  system: new Set(["phase", "paused", "resumed", "stopped", "error", "finished", "recovery"]),
};

const PLAN_TOOLS = new Set(["build_plan", "update_plan"]);
const DECISION_TOOLS = new Set(["record_hypothesis", "add_hypothesis_evidence"]);

export function isPlanEvent(e: FeedEvent): boolean {
  return (
    (e.type === "tool_call" || e.type === "tool_result" || e.type === "tool_error") &&
    PLAN_TOOLS.has(e.tool_name ?? "")
  );
}

export function isDecisionEvent(e: FeedEvent): boolean {
  return (
    (e.type === "tool_call" || e.type === "tool_result" || e.type === "tool_error") &&
    DECISION_TOOLS.has(e.tool_name ?? "")
  );
}

export function eventsForRole(events: FeedEvent[], role: SessionRole): FeedEvent[] {
  if (role === "plan") return events.filter(isPlanEvent);
  if (role === "decision") return events.filter(isDecisionEvent);
  const types = ROLE_EVENT_TYPES[role];
  return events.filter((e) => types.has(e.type));
}

/** REST 历史（WSEvent[]）→ 排序后的 FeedEvent[]（复用统一 dedupe/时间锚）。 */
export function toFeedEvents(events: WSEvent[]): FeedEvent[] {
  const converted = events.map((e, i) => toFeedEvent(e, "history", i));
  return converted.sort((a, b) => {
    const at = Date.parse(a.timestamp ?? "") || 0;
    const bt = Date.parse(b.timestamp ?? "") || 0;
    return at - bt || (a.seq ?? 0) - (b.seq ?? 0);
  });
}

/** SSE 帧是否会引起该实例任一角色会话的变化（防抖 refetch 的触发面）。 */
export function isRoleStreamEvent(e: { type: string; instance_id?: string; agent_id?: string }, instanceId: string | undefined): boolean {
  if (!instanceId) return false;
  if ((e.instance_id || e.agent_id) !== instanceId) return false;
  for (const types of Object.values(ROLE_EVENT_TYPES)) {
    if (types.has(e.type)) return true;
  }
  return false;
}
