import { useCallback, useEffect, useMemo, useState } from "react";
import { EmptyState } from "@/components/states";
import { SessionRail } from "@/components/sessions/session-rail";
import { SessionWorkspace } from "@/components/sessions/session-workspace";
import { toFeedEvents } from "@/components/sessions/role-events";
import { railModules } from "@/components/sessions/modules/registry";
import { findModule } from "@/components/sessions/modules/registry";
import { useProjectionInvalidation } from "@/components/sessions/modules/use-projection-invalidation";
import type { ModuleData } from "@/components/sessions/modules/types";
import { useLiveSessionKeys } from "@/components/session-item";
import { useInstanceAgents, useRunEvents, useScanPlan } from "@/api/queries";
import { cn } from "@/lib/utils";
import { useI18n } from "@/i18n";
import { pickLatestRun } from "@/hooks/use-run-selection";
import type { Run } from "@/types/api";

/**
 * 任务详情「会话」页（1:1 复刻 + 模块化扩展）。
 *
 * 数据分层（宪章 §5.1/§6：面板读状态，不读事件）：
 *  - 主会话转录 / 角色事件流：事件缓冲（transcript + run-events）；
 *  - Planner：GET /api/scans/{id}/plan（引擎计划投影）；
 *  - Workers：GET /api/instances/{id}/agents（agentsgraph 状态投影）。
 * SSE 只做投影失效（agent_* / plan_updated），绝不用于拼装状态。
 *
 * 结构：左栏分组由 modules/registry 注册表驱动（不再写死三个角色组），
 * 新增模块只登记一条记录。
 */
export function SessionsTab({
  taskId,
  runs,
  selectedInstanceId,
  onSelectRun,
  agentLaneName,
  onSelectAgentLane,
  onOpenWorker,
}: {
  taskId: string;
  runs: Run[];
  selectedInstanceId?: string;
  onSelectRun: (instanceId: string) => void;
  /** ?agent= 深链值：undefined=主会话；模块 id；或 worker 的 agent_id。 */
  agentLaneName?: string;
  onSelectAgentLane: (lane: string | undefined) => void;
  /** 打开某个 run 下某个 worker：一次写入 ?run=&agent=，避免两次 setSearchParams 互相覆盖。 */
  onOpenWorker: (instanceId: string, agentId: string) => void;
}) {
  const { t } = useI18n();
  const [listOpen, setListOpen] = useState(false);
  const liveKeys = useLiveSessionKeys();
  const selectedRun = runs.find((run) => run.instance_id === selectedInstanceId);
  const running = selectedRun?.status === "running";

  useProjectionInvalidation(selectedInstanceId, taskId);

  // 与角色会话工作区同一查询键 → 一份缓存同时供分组枚举与渲染。
  const { data: runEvents } = useRunEvents(selectedInstanceId, running);
  const feedEvents = useMemo(() => toFeedEvents(runEvents?.events ?? []), [runEvents]);

  const plan = useScanPlan(selectedInstanceId, running);
  const agents = useInstanceAgents(selectedInstanceId, running);

  // Clicking a rail group before choosing a Run must still show that group's
  // data. The sessions page deliberately does not auto-select a Run on open
  // (R7: no implicit main-agent session), so the first group click binds the
  // latest Run and the lane in ONE URL write — two writes in the same tick
  // would overwrite each other.
  const latestRun = useMemo(() => pickLatestRun(runs), [runs]);
  // Default-load: bind the latest run on open so the workspace is not empty
  // until the operator clicks something. Layout is unchanged; this only removes
  // the "click Planner first" step.
  useEffect(() => {
    if (!selectedInstanceId && latestRun?.instance_id) {
      onSelectRun(latestRun.instance_id);
    }
  }, [selectedInstanceId, latestRun?.instance_id, onSelectRun]);

  const selectLane = useCallback(
    (next?: string) => {
      if (next && !selectedInstanceId && latestRun?.instance_id) {
        onOpenWorker(latestRun.instance_id, next);
        return;
      }
      onSelectAgentLane(next);
    },
    [selectedInstanceId, latestRun, onOpenWorker, onSelectAgentLane],
  );

  // No explicit lane: open the plan projection by default (the module was
  // always loaded; only its rendering was gated behind a click). The main
  // conversation is addressed explicitly as lane "main".
  const lane = agentLaneName ?? (selectedInstanceId ? "plan" : undefined);
  const moduleData: ModuleData = useMemo(
    () => ({
      run: selectedRun,
      instanceId: selectedInstanceId,
      running,
      lane,
      plan: plan.data,
      planLoading: plan.isLoading,
      planError: !!plan.error,
      agents: agents.data,
      agentsLoading: agents.isLoading,
      agentsError: !!agents.error,
      feedEvents,
      onSelectLane: selectLane,
    }),
    [
      selectedRun,
      selectedInstanceId,
      runs.length,
      running,
      lane,
      plan.data,
      plan.isLoading,
      plan.error,
      agents.data,
      agents.isLoading,
      agents.error,
      feedEvents,
      selectLane,
    ],
  );

  const groups = useMemo(() => railModules(moduleData), [moduleData]);

  const activeTitle = useMemo(() => {
    const module = findModule(lane);
    if (module) return t(module.titleKey ?? module.labelKey);
    if (lane && lane !== "main") return lane;
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
        key={taskId}
        taskId={taskId}
        runs={runs}
        selectedInstanceId={selectedInstanceId}
        onSelectRun={onSelectRun}
        lane={lane}
        onLaneChange={selectLane}
        groups={groups}
        liveKeys={liveKeys}
        totalTokens={runs.reduce((sum, run) => sum + (run.total_tokens ?? 0), 0)}
        listOpen={listOpen}
        onToggleList={() => setListOpen((open) => !open)}
        activeTitle={activeTitle}
      />

      {selectedInstanceId ? (
        <SessionWorkspace
          key={selectedInstanceId}
          taskId={taskId}
          run={selectedRun}
          lane={lane}
          data={moduleData}
        />
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
