import { AlertTriangle, Layers } from "lucide-react";
import { EmptyState, ErrorState } from "@/components/states";
import { PlanRail } from "@/components/plan-rail";
import { planProgressFromProjection, planSourceLabelKey, planStateFromProjection } from "@/lib/plan-projection";
import { useI18n } from "@/i18n";
import type { ScanPlanView } from "@/types/api";

/**
 * Planner 模块主体：计划的只读状态投影。
 *
 * 权威 = GET /api/scans/{id}/plan（引擎 ScanState.Plan）。这里不再从
 * build_plan/update_plan 事件反推——引擎自建计划根本不产生那些 tool_call，
 * 那正是"Planner 区域内容为空"的根因。
 *
 * 四态：加载（骨架）/ 未产出（absent）/ 部分（partial，附原因）/ 错误。
 */
export function PlanModule({
  view,
  loading,
  error,
  running,
}: {
  view?: ScanPlanView;
  loading: boolean;
  error: boolean;
  running: boolean;
}) {
  const { t } = useI18n();

  if (error) {
    return (
      <div className="min-h-0 flex-1 overflow-y-auto p-4">
        <ErrorState title={t("sessions.planErrorTitle")} description={t("sessions.planErrorHint")} />
      </div>
    );
  }

  if (loading && !view) {
    return (
      <div className="min-h-0 flex-1 space-y-2 p-4" aria-busy="true">
        <div className="h-3 w-1/3 animate-pulse rounded bg-muted" />
        <div className="h-3 w-2/3 animate-pulse rounded bg-muted" />
        <div className="h-3 w-1/2 animate-pulse rounded bg-muted" />
      </div>
    );
  }

  if (!view?.present) {
    return (
      <div className="grid min-h-0 flex-1 place-items-center p-6">
        <EmptyState title={t("sessions.planAbsentTitle")} description={t("sessions.planAbsentHint")} />
      </div>
    );
  }

  const state = planStateFromProjection(view);
  const progress = planProgressFromProjection(view);
  const partialText =
    view.partial_reason === "not_persisted"
      ? t("sessions.planPartialNotPersisted")
      : t("sessions.planPartialTruncated");

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      {/* 头部：来源 / 版本 / 进度。来源必须可见（字段级权威，宪章 §3.5）。 */}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border px-3 py-2 text-xs">
        <span className="inline-flex items-center gap-1 text-muted-foreground">
          <Layers className="size-3.5" aria-hidden />
          {t(planSourceLabelKey(view.source))}
        </span>
        {view.plan_revision && (
          <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
            {t("sessions.planRevision")} {view.plan_revision}
          </span>
        )}
        {view.surface_revision && view.surface_revision !== view.plan_revision && (
          <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
            {t("sessions.planSurfaceRevision")} {view.surface_revision}
          </span>
        )}
        <span className="font-mono text-[11px] tabular-nums text-foreground">
          {progress.completed + progress.skipped}/{progress.total}
        </span>
        {progress.skipped > 0 && (
          <span className="font-mono text-[10px] tabular-nums text-muted-foreground">
            {t("plan.skipped")} {progress.skipped}
          </span>
        )}
        {progress.pending > 0 && (
          <span className="font-mono text-[10px] tabular-nums text-muted-foreground">
            {t("sessions.planUnfinished")} {progress.pending}
          </span>
        )}
        {running && (
          <span className="inline-flex items-center gap-1 text-[10px] text-emerald-600 dark:text-emerald-400">
            <span className="size-1 animate-pulse rounded-full bg-emerald-500" aria-hidden />
            {t("session.live")}
          </span>
        )}
      </div>

      {view.partial && (
        <p className="mx-3 mt-2 flex items-start gap-1.5 rounded-sm border border-amber-500/30 bg-amber-500/10 px-2 py-1.5 text-[11px] text-amber-700 dark:text-amber-300">
          <AlertTriangle className="mt-px size-3.5 shrink-0" aria-hidden />
          <span className="min-w-0">{partialText}</span>
        </p>
      )}

      <div className="min-w-0">
        <PlanRail plan={state} progress={progress} currentPhase={view.current_phase} />
      </div>

      {/* 任务表：阶段 / 类 / 端点 / 依赖 / 处置。PlanRail 只给紧凑列表，
          这里补上 Intent 的完整字段（PlanTask = Intent，宪章 §2）。 */}
      {view.tasks.length > 0 && (
        <ul className="divide-y divide-border" aria-label={t("sessions.planItemTitle")}>
          {view.tasks.map((task) => (
            <li key={task.id} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 px-3 py-1.5 text-xs">
              <span className="w-9 shrink-0 font-mono text-[10px] text-muted-foreground tabular-nums">
                P{task.phase}
              </span>
              <span className="min-w-0 flex-1 truncate" title={task.title}>
                {task.title}
              </span>
              {task.vuln_class && (
                <span className="shrink-0 rounded bg-muted px-1 font-mono text-[10px] text-muted-foreground">
                  {task.vuln_class}
                </span>
              )}
              {task.prerequisite && (
                <span className="shrink-0 rounded bg-muted px-1 text-[10px] text-muted-foreground">
                  {t("sessions.planPrerequisite")}
                </span>
              )}
              <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{task.status}</span>
              {task.disposition && (
                <span className="shrink-0 font-mono text-[10px] text-amber-600 dark:text-amber-400">
                  {task.disposition}
                </span>
              )}
              {task.depends_on && task.depends_on.length > 0 && (
                <span className="w-full truncate font-mono text-[10px] text-muted-foreground">
                  {t("sessions.planDependencies")}: {task.depends_on.join(", ")}
                </span>
              )}
              {task.endpoint && (
                <span className="w-full truncate font-mono text-[10px] text-muted-foreground">{task.endpoint}</span>
              )}
              {task.notes && (
                <span className="w-full truncate text-[10px] text-muted-foreground" title={task.notes}>
                  {task.notes}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
