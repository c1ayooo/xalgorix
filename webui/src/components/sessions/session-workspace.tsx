import { useEffect, useMemo, useState } from "react";
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
import { PlanRail } from "@/components/plan-rail";
import { planProgressFromProjection, planStateFromProjection } from "@/lib/plan-projection";
import { findModule } from "@/components/sessions/modules/registry";
import type { ModuleData } from "@/components/sessions/modules/types";
import { ScanStatusPill } from "@/components/scan-status-pill";
import { agentLane, fmtDuration, fmtTokens, isMainAgentLane } from "@/components/chat/format";
import { useI18n } from "@/i18n";
import { useInstances, usePauseInstance, useResumeInstance, useStopInstance, useTranscript } from "@/api/queries";
import { useLiveSessionKeys } from "@/components/session-item";
import type { Run, TranscriptItem } from "@/types/api";

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

  // 与 rail 同一查询键（limit=500）→ 共享缓存；时长/空态取自同份行。
  const { data: transcriptData } = useTranscript(instanceId, undefined, 500);
  const items = useMemo(() => transcriptData?.items ?? [], [transcriptData]);

  const module = findModule(lane);
  const isMain = !lane;
  const laneFilter = useMemo(
    () => (item: TranscriptItem) => filterForLane([item], instanceId, lane).length > 0,
    [instanceId, lane],
  );
  const live = liveKeys.has(instanceId ?? "") || running;

  // 运行时长 = 首步 → 末步；活动会话按当前时间续跳（1.5s 节拍）。
  const [, setTick] = useState(0);
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
  }, [items, live]);

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

  const emptyText = isMain ? (
    <p className="py-10 pl-0 text-center text-muted-foreground text-sm">{t("sessions.mainEmpty")}</p>
  ) : (
    <p className="py-10 text-center text-muted-foreground text-sm">{t("sessions.laneEmpty")}</p>
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
          {durationMs != null && (
            <span className="inline-flex items-center gap-1 tabular-nums" title={t("sessions.duration")}>
              <Clock className="size-3" aria-hidden />
              {fmtDuration(durationMs)}
            </span>
          )}
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

      {module ? (
        module.render(data)
      ) : (
        <>
          {/* 主会话视图直接暴露规划内容：引擎/模型产出的任务图 + 当前任务，
              不必先点左栏 Planner 才知道"到底规划了什么"。 */}
          {isMain && data.plan?.present && (
            <PlanRail
              plan={planStateFromProjection(data.plan)}
              progress={planProgressFromProjection(data.plan)}
              currentPhase={data.plan.current_phase}
            />
          )}
          <Transcript
            instanceId={instanceId}
            running={running}
            limit={500}
            filter={laneFilter}
            empty={emptyText}
          />
        </>
      )}

      {isMain && (
        <SessionComposer
          taskId={taskId}
          instanceId={instanceId}
          running={running}
          paused={paused}
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

/** 泳道过滤（A 档映射）：主=非 main 泳道行（含人工，排除系统）；system=系统行；worker=该泳道行。 */
function filterForLane(items: TranscriptItem[], instanceId: string | undefined, lane?: string): TranscriptItem[] {
  if (!lane) return items.filter((item) => item.kind !== "system" && isMainAgentLane(instanceId, item.agent_id));
  if (lane === "system") return items.filter((item) => item.kind === "system");
  return items.filter((item) => agentLane(instanceId, item.agent_id) === lane);
}
