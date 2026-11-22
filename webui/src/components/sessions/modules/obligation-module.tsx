import { CircleAlert, CircleSlash, Radio } from "lucide-react";
import { EmptyState } from "@/components/states";
import { useI18n } from "@/i18n";
import type { InstanceAgentsView, ScanPlanView } from "@/types/api";

/**
 * 义务模块（七环第 3 环）：把"还没收口的义务"集中呈现。
 *
 * 只读投影：未完成计划任务（Intent 未处置）、带 typed disposition 的跳过项
 * （跳过≠已覆盖）、以及正在运行但当前没有认领任何假设的 worker。
 *
 * 这里绝不重算收工闸门，也不改写任何判定——闸门结论只由引擎与报告管线给出
 * （宪章 §9.3 判定边界）。
 */
export function ObligationModule({
  plan,
  agents,
  running,
}: {
  plan?: ScanPlanView;
  agents?: InstanceAgentsView;
  running: boolean;
}) {
  const { t } = useI18n();

  const unfinished = (plan?.tasks ?? []).filter(
    (task) => task.status === "pending" || task.status === "active",
  );
  const skipped = (plan?.tasks ?? []).filter((task) => task.status === "skipped");
  const idleWorkers = (agents?.agents ?? []).filter(
    (agent) => agent.status === "running" && !(agent.claimed_hypotheses?.length ?? 0),
  );

  if (unfinished.length === 0 && skipped.length === 0 && idleWorkers.length === 0) {
    return (
      <div className="grid min-h-0 flex-1 place-items-center p-6">
        <EmptyState title={t("sessions.obligationEmpty")} description={t("sessions.obligationEmpty")} />
      </div>
    );
  }

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="flex items-center gap-2 border-b border-border px-3 py-2 text-xs text-muted-foreground">
        <CircleAlert className="size-3.5" aria-hidden />
        {t("sessions.planUnfinished")} {unfinished.length}
        <span aria-hidden>·</span>
        {t("plan.skipped")} {skipped.length}
        {running && (
          <span className="ml-auto inline-flex items-center gap-1 text-[10px] text-emerald-600 dark:text-emerald-400">
            <span className="size-1 animate-pulse rounded-full bg-emerald-500" aria-hidden />
            {t("session.live")}
          </span>
        )}
      </div>

      {idleWorkers.length > 0 && (
        <ul className="border-b border-border">
          {idleWorkers.map((agent) => (
            <li key={agent.agent_id} className="flex items-center gap-2 px-3 py-1.5 text-xs text-amber-600 dark:text-amber-400">
              <Radio className="size-3.5 shrink-0" aria-hidden />
              <span className="min-w-0 flex-1 truncate">{agent.lane}</span>
              <span className="shrink-0 text-[10px]">{t("sessions.workerIdle")}</span>
            </li>
          ))}
        </ul>
      )}

      <ul className="divide-y divide-border">
        {unfinished.map((task) => (
          <li key={task.id} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 px-3 py-1.5 text-xs">
            <span className="w-9 shrink-0 font-mono text-[10px] text-muted-foreground tabular-nums">P{task.phase}</span>
            <span className="min-w-0 flex-1 truncate" title={task.title}>
              {task.title}
            </span>
            {task.vuln_class && (
              <span className="shrink-0 rounded bg-muted px-1 font-mono text-[10px] text-muted-foreground">
                {task.vuln_class}
              </span>
            )}
            <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{task.status}</span>
          </li>
        ))}
        {skipped.map((task) => (
          <li key={task.id} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 px-3 py-1.5 text-xs opacity-80">
            <CircleSlash className="mt-0.5 size-3 shrink-0 text-muted-foreground" aria-hidden />
            <span className="w-9 shrink-0 font-mono text-[10px] text-muted-foreground tabular-nums">P{task.phase}</span>
            <span className="min-w-0 flex-1 truncate text-muted-foreground" title={task.title}>
              {task.title}
            </span>
            {task.disposition && (
              <span className="shrink-0 font-mono text-[10px] text-amber-600 dark:text-amber-400">{task.disposition}</span>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
