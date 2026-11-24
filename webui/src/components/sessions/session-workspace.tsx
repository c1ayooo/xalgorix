import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { Clock } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { EmptyState } from "@/components/states";
import { Transcript } from "@/components/chat/transcript";
import { SessionComposer } from "@/components/sessions/session-composer";
import { findModule } from "@/components/sessions/modules/registry";
import { WorkerStatusDot } from "@/components/sessions/modules/workers-module";
import type { ModuleData } from "@/components/sessions/modules/types";
import { ScanStatusPill } from "@/components/scan-status-pill";
import { agentLane, fmtDuration, fmtTokens, isMainAgentLane } from "@/components/chat/format";
import { useI18n } from "@/i18n";
import { useInstances, usePauseInstance, useResumeInstance, useStopInstance, useTranscript } from "@/api/queries";
import { useLiveSessionKeys } from "@/components/session-item";
import type { Run, TranscriptItem } from "@/types/api";

/** 会话页首屏转录页大小；更早历史由 Transcript 的向上滚动分页补齐。 */
const SESSION_TRANSCRIPT_PAGE = 200;

/**
 * 会话页工作区（右栏）：头部（标题/权威来源/状态/时长/tokens/运行控制）+
 * 模块主体 + 主会话 Composer。
 *
 * 主体解析顺序：主会话（无 ?agent=）→ 完整转录；注册表模块 → module.render；
 * 其余 ?agent= 值 → 该 worker 泳道的转录过滤（兼容旧深链）。
 * 模块数据由 sessions-tab 一次查询后传入，模块自身不再各自取数。
 */
export function SessionWorkspace({
  taskId,
  run,
  lane,
  data,
}: {
  taskId: string;
  run: Run | undefined;
  /** undefined=主会话；模块 id；或 worker agent_id。 */
  lane?: string;
  data: ModuleData;
}) {
  const { t } = useI18n();
  const instanceId = run?.instance_id;
  const liveKeys = useLiveSessionKeys();
  const { data: instancesData } = useInstances();
  const instance = instancesData?.instances.find((item) => item.id === instanceId);
  const status = instance?.status ?? run?.status ?? "";
  const running = status === "running";
  const paused = status === "paused";

  const pause = usePauseInstance();
  const resume = useResumeInstance();
  const stop = useStopInstance();
  const [stopOpen, setStopOpen] = useState(false);
  const [stopReason, setStopReason] = useState("");
  const [editSeed, setEditSeed] = useState<{ text: string; nonce: number } | undefined>(undefined);

  // 首屏只取最近一页（滚动到顶再加载更早历史）。500 行意味着首开要拉 ~260KB
  // 并一次渲染 500 个复杂行，是"进入 run 后整页卡住"的主因之一；200 行仍覆盖
  // 一屏对话与最近工具回合，其余按需加载。
  const { data: transcriptData } = useTranscript(instanceId, undefined, SESSION_TRANSCRIPT_PAGE);
  const items = useMemo(() => transcriptData?.items ?? [], [transcriptData]);

  const module = findModule(lane);
  const isMain = !lane || lane === "main";
  // Unit lanes are addressed as "unit:<unit_id>". One unit = one worker across
  // every dispatch attempt; the server filters the transcript by the union of
  // its agent ids, so the client filter must not narrow it again.
  const unitKey = lane?.startsWith("unit:") ? lane.slice("unit:".length) : undefined;
  const isUnitLane = !!unitKey;
  // Worker lane identity/status travels with the transcript: the lane itself is
  // a session, so it never needs a generic "no activity" placeholder.
  const workerHeader = useMemo(() => {
    if (isMain || module) return undefined;
    if (isUnitLane) {
      const unit = data.agents?.units?.find((entry) => entry.unit_id === unitKey);
      if (!unit) return undefined;
      return {
        status: unit.status,
        title: unit.title || unit.vuln_class || unit.endpoint || unit.unit_id,
        state: unit.state,
        task: unit.target || unit.endpoint,
      };
    }
    const entry = data.agents?.agents.find((row) => row.agent_id === lane);
    if (!entry) return undefined;
    return {
      status: entry.status,
      title: entry.lane || entry.agent_id,
      state: entry.status,
      task: entry.task,
    };
  }, [data.agents, isMain, module, isUnitLane, lane, unitKey]);
  const laneFilter = useMemo(
    () =>
      isUnitLane
        ? () => true
        : (item: TranscriptItem) => filterForLane([item], instanceId, lane).length > 0,
    [instanceId, lane, isUnitLane],
  );
  // Unit lanes are server-filtered (persisted rows), but live delta bubbles
  // still come from the whole instance. Restrict them to this unit's agents so
  // two concurrently running units never show each other's typing.
  const unitAgentIds = useMemo(() => {
    if (!isUnitLane) return undefined;
    const unit = data.agents?.units?.find((entry) => entry.unit_id === unitKey);
    return new Set(unit?.agent_ids ?? []);
  }, [data.agents, isUnitLane, unitKey]);
  const live = liveKeys.has(instanceId ?? "") || running;

  // 主 Agent 栏是纯对话：隐藏工具调用卡；意图/提示插入折叠成一条结果行；
  // 只读查询工具留在 journal（审计）但不进入对话。PlanRail 等 Planner 内容
  // 属于左栏 Planner 模块，不再混入主 Agent。
  const mainTransform = useCallback(
    (item: TranscriptItem): TranscriptItem | null => {
      if (item.kind === "tool_use") return null;
      if (item.kind !== "tool_result") return item;
      const output = compactToolOutput(item.output);
      // 失败（例如提案缺 target）不能伪装成成功，如实显示结果。
      if ((item.output ?? "").trimStart().startsWith("error:")) {
        return { ...item, kind: "system", text: `${t("sessions.actionFailed")} · ${output}` };
      }
      // propose_intent 的成功结果由 ProposalCard 呈现，工具结果行不再重复。
      if (item.tool === "add_hint") {
        return {
          ...item,
          kind: "system",
          text: `${t("sessions.hintRecorded")} · ${output}`,
        };
      }
      return null;
    },
    [t],
  );

  if (!run || !instanceId) {
    return (
      <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-lg border border-border bg-card">
        <div className="grid h-full place-items-center p-6">
          <EmptyState title={t("console.noRunTitle")} description={t("console.noRunDescription")} />
        </div>
      </div>
    );
  }

  const title = isMain
    ? run.title || run.id
    : module
      ? t(module.titleKey ?? module.labelKey)
      : `${t("sessions.workspaceWorker")} · ${lane}`;

  const emptyText = (
    <p className="py-10 pl-0 text-center text-muted-foreground text-sm">{t("sessions.mainEmpty")}</p>
  );

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-lg border border-border bg-card">
      {/* 头部：标题 + 权威来源 + 状态 + 实时 + token/时长 + 运行控制（主会话）。 */}
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-border px-3 py-2 sm:px-4 sm:py-2.5">
        <span className="min-w-0 max-w-56 truncate font-medium text-sm" title={title}>
          {title}
        </span>
        {module && (
          <span
            className="shrink-0 rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground"
            title={t("sessions.list")}
          >
            {module.authority}
          </span>
        )}
        <ScanStatusPill status={status} />
        {liveKeys.has(instanceId) && (
          <span className="inline-flex items-center gap-1 rounded bg-emerald-500/10 px-1.5 py-0.5 font-medium text-xs text-emerald-600 dark:text-emerald-400">
            <span className="size-1 animate-pulse rounded-full bg-emerald-500" aria-hidden />
            {t("session.live")}
          </span>
        )}
        <div className="ml-auto flex min-w-0 max-w-full items-center justify-end gap-x-3 gap-y-1 text-muted-foreground text-xs max-sm:w-full max-sm:flex-wrap">
          <span className="tabular-nums" title={t("sessions.tokens")}>
            {fmtTokens(run.total_tokens)}
          </span>
          <RunDuration items={items} live={live} title={t("sessions.duration")} />
          {isMain && instance && paused && (
            <Button
              size="sm"
              variant="outline"
              className="h-7 px-2 text-xs"
              disabled={resume.isPending}
              onClick={() => resume.mutate(instance.id, { onSuccess: () => toast.success(t("toast.runResumed")) })}
            >
              {t("console.resume")}
            </Button>
          )}
          {isMain && instance && running && (
            <Button
              size="sm"
              variant="outline"
              className="h-7 px-2 text-xs"
              disabled={pause.isPending}
              onClick={() => pause.mutate(instance.id, { onSuccess: () => toast.success(t("toast.runPaused")) })}
            >
              {t("console.pause")}
            </Button>
          )}
          {isMain && instance && (running || paused) && (
            <Button
              size="sm"
              variant="ghost"
              className="h-7 px-2 text-xs text-red-600 hover:text-red-700 dark:text-red-400"
              onClick={() => {
                setStopReason("");
                setStopOpen(true);
              }}
            >
              {t("console.stop")}
            </Button>
          )}
        </div>
      </div>

      {workerHeader && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border px-3 py-1.5 text-xs text-muted-foreground">
          <WorkerStatusDot status={workerHeader.status} />
          <span className="font-medium text-foreground">{workerHeader.title}</span>
          <span className="rounded bg-muted px-1 font-mono text-[10px]">{workerHeader.state}</span>
          {workerHeader.task && (
            <span className="min-w-0 truncate" title={workerHeader.task}>
              {workerHeader.task}
            </span>
          )}
        </div>
      )}

      {module ? (
        module.render(data)
      ) : (
        <Transcript
          instanceId={instanceId}
          running={running}
          limit={SESSION_TRANSCRIPT_PAGE}
          human={isMain}
          filter={laneFilter}
          taskId={taskId}
          onEditMessage={(text) => setEditSeed({ text, nonce: Date.now() })}
          transform={isMain ? mainTransform : undefined}
          agent={isMain || isUnitLane ? undefined : lane}
          unit={isUnitLane ? unitKey : undefined}
          liveAgentIds={unitAgentIds}
          // Worker lanes are identified by the header above; a truly empty one
          // must not replace it with a generic "no activity" message.
          empty={isMain ? emptyText : <></>}
        />
      )}

      {isMain && (
        <SessionComposer
          taskId={taskId}
          instanceId={instanceId}
          running={running}
          paused={paused}
          seed={editSeed}
          onStop={() => setStopOpen(true)}
        />
      )}

      <AlertDialog open={stopOpen} onOpenChange={setStopOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("console.stopTitle")}</AlertDialogTitle>
            <AlertDialogDescription>{t("console.stopDescription")}</AlertDialogDescription>
          </AlertDialogHeader>
          <div className="space-y-1.5">
            <Label htmlFor="stop-reason">{t("console.stopReasonLabel")}</Label>
            <textarea
              id="stop-reason"
              rows={2}
              value={stopReason}
              onChange={(event) => setStopReason(event.target.value)}
              placeholder={t("console.stopReasonPlaceholder")}
              className="w-full resize-y rounded-md border border-border bg-input/40 px-3 py-2 text-sm outline-none placeholder:text-muted-foreground focus:ring-1 focus:ring-ring"
            />
          </div>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                stop.mutate(
                  { id: instanceId, reason: stopReason.trim() || undefined },
                  {
                    onSuccess: () => {
                      toast.success(t("toast.runStopped"));
                      setStopOpen(false);
                    },
                    onError: (error: Error) => toast.error(`${t("console.stopFailed")}: ${error.message}`),
                  },
                );
              }}
            >
              {t("console.stopConfirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

/**
 * 运行时长 = 首步 → 末步；活动会话按 1.5s 节拍续跳。
 *
 * 自成一个叶子组件，节拍只重绘这一个 span——旧实现把 tick 放在工作区顶层，
 * 每 1.5s 连同 200~500 行转录、composer 整块重渲染。
 */
function RunDuration({ items, live, title }: { items: TranscriptItem[]; live: boolean; title: string }) {
  const [tick, setTick] = useState(0);
  useEffect(() => {
    if (!live) return;
    const id = setInterval(() => setTick((n) => n + 1), 1500);
    return () => clearInterval(id);
  }, [live]);
  const durationMs = useMemo(() => {
    let min = Number.POSITIVE_INFINITY;
    let max = 0;
    for (const item of items) {
      const at = Date.parse(item.at);
      if (!Number.isFinite(at)) continue;
      if (at < min) min = at;
      if (at > max) max = at;
    }
    if (!Number.isFinite(min) || max === 0) return null;
    return Math.max(0, (live ? Date.now() : max) - min);
    // tick 只用于让 live 分支的 Date.now() 随时间推进。
  }, [items, live, tick]);
  if (durationMs == null) return null;
  return (
    <span className="inline-flex items-center gap-1 tabular-nums" title={title}>
      <Clock className="size-3" aria-hidden />
      {fmtDuration(durationMs)}
    </span>
  );
}

/** 工具结果压成一行结果提示（意图/提示插入行）。 */
function compactToolOutput(output?: string): string {
  const line = (output ?? "").split("\n").find((value) => value.trim() !== "") ?? "";
  return line.length > 120 ? `${line.slice(0, 120)}…` : line;
}

/** 泳道过滤（A 档映射）：主=非 main 泳道行（含人工，排除系统）；system=系统行；worker=该泳道行。 */
function filterForLane(items: TranscriptItem[], instanceId: string | undefined, lane?: string): TranscriptItem[] {
  if (!lane || lane === "main") return items.filter((item) => item.kind !== "system" && isMainAgentLane(instanceId, item.agent_id));
  if (lane === "system") return items.filter((item) => item.kind === "system");
  // Lane values arrive as the raw agent id from /agents (which may carry an
  // instance prefix), while event rows carry whatever id the agent emitted.
  // Compare the normalized lane on both sides, never the raw strings.
  const target = agentLane(instanceId, lane);
  return items.filter((item) => agentLane(instanceId, item.agent_id) === target);
}
