import { useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { PlanRail } from "@/components/plan-rail";
import { ExecutionTrace } from "@/components/execution-trace";
import { extractPlan, planProgress } from "@/lib/execution-trace";
import { useI18n } from "@/i18n";
import { useEventsStore } from "@/store/events";
import { useRunEvents } from "@/api/queries";
import {
  eventsForRole,
  isRoleStreamEvent,
  toFeedEvents,
  type SessionRole,
} from "@/components/sessions/role-events";

/**
 * 角色会话只读流（规划/决策/系统审计）：数据 = 实例事件缓冲（与 rail 共享
 * 同一查询缓存；缓冲缺席回落持久 journal）+ SSE 防抖 refetch。渲染统一复用
 * 操作者既有的 ExecutionTrace 分组动态内容（规划卡/工具配对/轮次/审批/发现），
 * 不另造第二套行渲染。引擎没产生该角色的事件时诚实空态，不编造。
 */
export function RoleEventStream({
  instanceId,
  role,
  running,
}: {
  instanceId: string;
  role: SessionRole;
  running?: boolean;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data: historyEvents, isLoading } = useRunEvents(instanceId, running);

  // SSE → 防抖 refetch 桥（角色事件类型计数变化才触发）。
  const events = useEventsStore((s) => s.events);
  const seenRef = useRef<number | null>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    let count = 0;
    for (const e of events) {
      if (isRoleStreamEvent(e, instanceId)) count++;
    }
    if (seenRef.current === null) {
      seenRef.current = count;
      return;
    }
    if (count === seenRef.current) return;
    seenRef.current = count;
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => {
      void queryClient.invalidateQueries({ queryKey: ["run-events", instanceId] });
    }, 400);
  }, [events, instanceId, queryClient]);
  useEffect(
    () => () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
    },
    [],
  );

  const feedEvents = useMemo(() => toFeedEvents(historyEvents?.events ?? []), [historyEvents]);
  const rows = useMemo(() => eventsForRole(feedEvents, role), [feedEvents, role]);
  const latestPhase = useMemo(() => {
    for (let i = feedEvents.length - 1; i >= 0; i--) {
      const n = feedEvents[i].current_phase;
      if (n && n > 0) return n;
    }
    return undefined;
  }, [feedEvents]);

  const emptyTitle =
    role === "plan"
      ? t("sessions.planEmpty")
      : role === "decision"
        ? t("sessions.decisionEmpty")
        : t("sessions.systemEmpty");

  // 首次取数期间给骨架，避免分组一出现就短暂显示"暂无决策/系统事件"的假空态。
  if (isLoading && !historyEvents) {
    return (
      <div className="min-h-0 flex-1 space-y-2 p-4" aria-busy="true">
        <div className="h-3 w-1/3 animate-pulse rounded bg-muted" />
        <div className="h-3 w-2/3 animate-pulse rounded bg-muted" />
        <div className="h-3 w-1/2 animate-pulse rounded bg-muted" />
      </div>
    );
  }

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      {role === "plan" && rows.length > 0 && (
        <PlanRail
          plan={extractPlan(feedEvents)}
          progress={planProgress(extractPlan(feedEvents))}
          currentPhase={latestPhase}
        />
      )}
      <ExecutionTrace
        events={rows}
        instanceId={instanceId}
        active={running}
        emptyTitle={emptyTitle}
        className="min-h-0 flex-1"
      />
    </div>
  );
}
