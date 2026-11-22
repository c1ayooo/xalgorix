import { useMemo, useState } from "react";
import { EmptyState } from "@/components/states";
import { SessionRail } from "@/components/sessions/session-rail";
import { SessionWorkspace } from "@/components/sessions/session-workspace";
import { toFeedEvents } from "@/components/sessions/role-events";
import { agentLane, isMainAgentLane } from "@/components/chat/format";
import { useLiveSessionKeys } from "@/components/session-item";
import { useRunEvents } from "@/api/queries";
import { cn } from "@/lib/utils";
import { useI18n } from "@/i18n";
import type { Run } from "@/types/api";

/**
 * Task-detail session tab（1:1 复刻 + 角色扩展，docs/SESSIONS_CHAT_1TO1_PLAN.md §5
 * 与宪章七环对齐）：rail 角色组 = 主 Agent（各 Run，可交互）/ 规划 /
 * 决策 / 系统审计 / Workers（agent 泳道）。思考流经用户裁决（2026-11-22）不做
 * 角色组，只在主会话转录与执行轨迹中内联呈现。角色组数据源 = 实例事件缓冲
 * （decision/phase/build_plan 全在），引擎没产生的事件组不显示，不编造。
 * URL 权威：?run=（run 选择）+ ?agent=（角色/泳道深链，task-detail 持有）。
 */
export function SessionsTab({
  taskId,
  runs,
  selectedInstanceId,
  onSelectRun,
  agentLaneName,
  onSelectAgentLane,
}: {
  taskId: string;
  runs: Run[];
  selectedInstanceId?: string;
  onSelectRun: (instanceId: string) => void;
  /** ?agent= 深链值：undefined=主会话；plan|decision|system=角色会话；其余=泳道短名。 */
  agentLaneName?: string;
  onSelectAgentLane: (lane: string | undefined) => void;
}) {
  const { t } = useI18n();
  const [listOpen, setListOpen] = useState(false);
  const liveKeys = useLiveSessionKeys();
  const selectedRun = runs.find((run) => run.instance_id === selectedInstanceId);

  // 与角色会话工作区同一查询键 → 一份缓存同时供分组枚举与渲染。
  const { data: runEvents } = useRunEvents(selectedInstanceId, selectedRun?.status === "running");
  const feedEvents = useMemo(() => toFeedEvents(runEvents?.events ?? []), [runEvents]);

  const lanes = useMemo(() => {
    const seen = new Set<string>();
    for (const e of feedEvents) {
      if (!e.agent_id || isMainAgentLane(selectedInstanceId, e.agent_id)) continue;
      seen.add(agentLane(selectedInstanceId, e.agent_id));
    }
    return [...seen].sort((a, b) => a.localeCompare(b, "zh-CN"));
  }, [feedEvents, selectedInstanceId]);

  const presence = useMemo(() => {
    let plan = false;
    let decision = false;
    let system = false;
    for (const e of feedEvents) {
      if (e.type === "tool_call" && (e.tool_name === "build_plan" || e.tool_name === "update_plan")) plan = true;
      else if (e.type === "tool_call" && (e.tool_name === "record_hypothesis" || e.tool_name === "add_hypothesis_evidence")) decision = true;
      else if (["phase", "paused", "resumed", "stopped", "error", "finished", "recovery"].includes(e.type)) system = true;
    }
    return { plan, decision, system };
  }, [feedEvents]);

  const totalTokens = useMemo(() => runs.reduce((sum, run) => sum + (run.total_tokens ?? 0), 0), [runs]);

  const lane = agentLaneName;
  const activeTitle = useMemo(() => {
    if (lane === "plan") return t("sessions.planItemTitle");
    if (lane === "decision") return t("sessions.decisionItemTitle");
    if (lane === "system") return t("sessions.systemItem");
    if (lane) return lane;
    return selectedRun?.title || selectedRun?.id || t("session.emptyRuns");
  }, [lane, selectedRun, t]);

  return (
    // 页面容器（task-detail）已全断点视口定高：h-full 跟随即可，消息区内部
    // 滚动、composer 钉底（方案 §5.3：不再叠 100svh 魔法数）。
    <div
      className={cn(
        "grid h-full min-h-0 grid-cols-1 gap-4",
        listOpen ? "grid-rows-[minmax(0,40svh)_minmax(0,1fr)]" : "grid-rows-[auto_minmax(0,1fr)]",
        "lg:grid-cols-[18rem_minmax(0,1fr)] lg:grid-rows-[minmax(0,1fr)]",
      )}
    >
      <SessionRail
        runs={runs}
        selectedInstanceId={selectedInstanceId}
        onSelectRun={onSelectRun}
        lane={lane}
        onLaneChange={onSelectAgentLane}
        lanes={lanes}
        planPresent={presence.plan}
        decisionPresent={presence.decision}
        systemPresent={presence.system}
        liveKeys={liveKeys}
        totalTokens={totalTokens}
        listOpen={listOpen}
        onToggleList={() => setListOpen((open) => !open)}
        activeTitle={activeTitle}
      />

      {selectedInstanceId ? (
        <SessionWorkspace key={selectedInstanceId} taskId={taskId} run={selectedRun} lane={lane} />
      ) : (
        <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-lg border border-border bg-card">
          <div className="grid h-full place-items-center p-6">
            <EmptyState title={t("console.noRunTitle")} description={t("console.noRunDescription")} />
          </div>
        </div>
      )}
    </div>
  );
}
