import { useEffect, useRef } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useEventsStore } from "@/store/events";

/**
 * SSE → 投影失效桥。
 *
 * agent_* / plan_updated 帧只表示"状态可能变了"，不是状态本身（宪章 §5.1：
 * 遥测不是权威）。这里只做去抖失效，让 useInstanceAgents / useScanPlan 重取
 * 投影，绝不用事件内容拼装状态。
 */
const INVALIDATING_TYPES = new Set([
  "agent_queued",
  "agent_started",
  "agent_completed",
  "agent_failed",
  "plan_updated",
]);

export function useProjectionInvalidation(instanceId?: string, taskId?: string) {
  const queryClient = useQueryClient();
  const events = useEventsStore((state) => state.events);
  const seenRef = useRef<number | null>(null);

  useEffect(() => {
    let count = 0;
    for (const event of events) {
      if (INVALIDATING_TYPES.has(event.type)) count++;
    }
    if (seenRef.current === null) {
      seenRef.current = count;
      return;
    }
    if (count === seenRef.current) return;
    seenRef.current = count;
    const key = instanceId ?? "none";
    void queryClient.invalidateQueries({ queryKey: ["instance-agents", key] });
    void queryClient.invalidateQueries({ queryKey: ["scan-plan", key] });
    // Task-scoped worker history shares the same lifecycle signal.
    if (taskId) void queryClient.invalidateQueries({ queryKey: ["task-workers", taskId] });
  }, [events, instanceId, taskId, queryClient]);
}
