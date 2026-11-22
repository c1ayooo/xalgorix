import { ChevronDown, Clock, Loader2 } from "lucide-react";
import { EmptyState } from "@/components/states";
import { cn } from "@/lib/utils";
import { useI18n } from "@/i18n";
import type { DelegatedAgent, InstanceAgentsView } from "@/types/api";

/**
 * Workers 模块主体：真实并发状态。
 *
 * 权威 = GET /api/instances/{id}/agents（agentsgraph 的内存状态；终态由
 * execution-graph checkpoint 重建）。旧实现从历史事件里去重 agent_id，
 * 并用 run 级 status 当每行状态，于是"已结束的 worker"和"正在跑的 worker"
 * 长得一模一样——这正是"两个 worker 只有一个在干活"的直接原因。
 */
export function WorkersModule({
  view,
  loading,
  error,
  onSelectLane,
}: {
  view?: InstanceAgentsView;
  loading: boolean;
  error: boolean;
  /** 点击 worker 行 → ?agent=<agent_id>，查看该 agent 的转录。 */
  onSelectLane?: (lane?: string) => void;
}) {
  const { t } = useI18n();

  if (error) {
    return (
      <div className="grid min-h-0 flex-1 place-items-center p-6">
        <EmptyState title={t("sessions.workerUnavailable")} description={t("sessions.workerEmptyHint")} />
      </div>
    );
  }
  if (loading && !view) {
    return (
      <div className="min-h-0 flex-1 space-y-2 p-4" aria-busy="true">
        <div className="h-3 w-1/3 animate-pulse rounded bg-muted" />
        <div className="h-3 w-1/2 animate-pulse rounded bg-muted" />
      </div>
    );
  }
  if (!view?.available) {
    return (
      <div className="grid min-h-0 flex-1 place-items-center p-6">
        <EmptyState title={t("sessions.workerUnavailable")} description={t("sessions.workerEmptyHint")} />
      </div>
    );
  }
  if (view.agents.length === 0) {
    return (
      <div className="grid min-h-0 flex-1 place-items-center p-6">
        <EmptyState title={t("sessions.workerEmpty")} description={t("sessions.workerEmptyHint")} />
      </div>
    );
  }

  const running = view.agents.filter((agent) => agent.status === "running");
  const queued = view.agents.filter((agent) => agent.status === "queued");
  const finished = view.agents.filter((agent) => agent.status !== "running" && agent.status !== "queued");

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border px-3 py-2 text-xs text-muted-foreground">
        <span className="inline-flex items-center gap-1">
          <Loader2 className="size-3.5 text-sky-500" aria-hidden />
          {t("sessions.workerRunning")} {view.concurrency.running}
          {view.concurrency.limit > 0 ? `/${view.concurrency.limit}` : ""}
          <span className="text-[10px]">({t("sessions.workerConcurrency")})</span>
        </span>
        {view.concurrency.queued > 0 && (
          <span className="inline-flex items-center gap-1 text-amber-600 dark:text-amber-400">
            <Clock className="size-3.5" aria-hidden />
            {t("sessions.workerQueued")} {view.concurrency.queued}
          </span>
        )}
        {view.partial_reason === "checkpoint" && (
          <span className="font-mono text-[10px]">checkpoint</span>
        )}
      </div>

      {running.length > 0 && (
        <WorkerGroup title={t("sessions.workerRunning")} agents={running} onSelectLane={onSelectLane} />
      )}
      {queued.length > 0 && (
        <WorkerGroup title={t("sessions.workerQueued")} agents={queued} onSelectLane={onSelectLane} />
      )}
      {finished.length > 0 && (
        <details className="group">
          <summary className="flex cursor-pointer items-center gap-1 border-b border-border px-3 py-1.5 text-[11px] font-medium text-muted-foreground transition-colors hover:bg-accent/30">
            <ChevronDown className="size-3.5 transition-transform group-open:rotate-180" aria-hidden />
            {t("sessions.workerFinished")} {finished.length}
          </summary>
          <WorkerGroup title={t("sessions.workerFinished")} agents={finished} muted onSelectLane={onSelectLane} />
        </details>
      )}
    </div>
  );
}

function WorkerGroup({
  title,
  agents,
  muted = false,
  onSelectLane,
}: {
  title: string;
  agents: DelegatedAgent[];
  muted?: boolean;
  onSelectLane?: (lane?: string) => void;
}) {
  const { t } = useI18n();
  return (
    <section aria-label={title}>
      <ul>
        {agents.map((agent) => {
          const claim = agent.claimed_hypotheses?.[0];
          return (
            <li
              key={agent.agent_id}
              className={cn("border-b border-border last:border-0", muted && "opacity-70")}
            >
              <button
                type="button"
                onClick={() => onSelectLane?.(agent.agent_id)}
                className="block w-full cursor-pointer px-3 py-2 text-left text-xs transition-colors hover:bg-accent/40 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
              >
              <div className="flex min-w-0 items-center gap-2">
                <WorkerStatusDot status={agent.status} />
                <span className="min-w-0 flex-1 truncate font-medium" title={agent.task || agent.lane}>
                  {agent.lane}
                </span>
                {agent.queued_position ? (
                  <span className="shrink-0 rounded bg-muted px-1 font-mono text-[10px] text-muted-foreground">
                    {t("sessions.workerQueuePosition")} #{agent.queued_position}
                  </span>
                ) : null}
                <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{agent.agent_id}</span>
              </div>
              <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 pl-4 text-[10px] text-muted-foreground">
                {claim ? (
                  <span className="min-w-0 truncate">
                    <span className="text-foreground">{t("sessions.workerClaimed")}</span>{" "}
                    <span className="font-mono">{claim.id}</span>
                    {claim.vuln_class ? ` · ${claim.vuln_class}` : ""}
                    {claim.endpoint ? ` · ${claim.endpoint}` : ""}
                  </span>
                ) : (
                  <span>{agent.status === "running" ? t("sessions.workerNoClaim") : t("sessions.workerIdle")}</span>
                )}
                {agent.started_at && (
                  <span className="font-mono">
                    {t("sessions.workerSince")} {shortTime(agent.started_at)}
                  </span>
                )}
                {agent.completed_at && (
                  <span className="font-mono">
                    {t("sessions.workerDone")} {shortTime(agent.completed_at)}
                  </span>
                )}
                {agent.error && <span className="w-full truncate text-red-600 dark:text-red-400">{agent.error}</span>}
              </div>
              </button>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

export function WorkerStatusDot({ status }: { status: DelegatedAgent["status"] }) {
  if (status === "running") {
    return <span className="size-1.5 shrink-0 animate-pulse rounded-full bg-emerald-500" aria-hidden />;
  }
  if (status === "queued") {
    return <span className="size-1.5 shrink-0 rounded-full bg-amber-500" aria-hidden />;
  }
  if (status === "failed") {
    return <span className="size-1.5 shrink-0 rounded-full bg-red-500" aria-hidden />;
  }
  return <span className="size-1.5 shrink-0 rounded-full bg-muted-foreground/40" aria-hidden />;
}

function shortTime(iso: string): string {
  const at = Date.parse(iso);
  if (!Number.isFinite(at)) return iso;
  const d = new Date(at);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}:${String(
    d.getSeconds(),
  ).padStart(2, "0")}`;
}
