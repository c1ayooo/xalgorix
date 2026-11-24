import { ChevronDown, Clock, Loader2 } from "lucide-react";
import { EmptyState } from "@/components/states";
import { cn } from "@/lib/utils";
import { useI18n } from "@/i18n";
import type { DelegatedAgent, InstanceAgentsView, WorkerUnit } from "@/types/api";

/**
 * Workers 模块主体：真实并发状态。
 *
 * 权威 = GET /api/instances/{id}/agents（agentsgraph 的内存状态；终态由
 * execution-graph checkpoint 重建）。旧实现从历史事件里去重 agent_id，
 * 并用 run 级 status 当每行状态，于是"已结束的 worker"和"正在跑的 worker"
 * 长得一模一样——这正是"两个 worker 只有一个在干活"的直接原因。
 *
 * R7 起可见单位是**工作单元（intent）**而不是 agent 实例：一个 unit 一条行，
 * 重派/重试追加到同一条；unit 的图状态是主状态，agent 只是执行明细。
 * legacy spawn 模式（units 为空）回退到 agent 行。
 */

/** 统一的行模型：unit 行或 legacy agent 行。 */
interface WorkerRow {
  /** ?agent= 深链值：unit:<unit_id> 或 agent_id。 */
  key: string;
  label: string;
  /** 行尾稳定标识（unit_id / agent_id）。 */
  meta: string;
  status: string;
  /** 同一 unit 的派发次数；legacy 行为 1。 */
  attempts: number;
  task?: string;
  target?: string;
  started_at?: string;
  completed_at?: string;
  updated_at?: string;
  error?: string;
  claims?: { id: string; vuln_class?: string; endpoint?: string; status?: string }[];
}

function unitRow(unit: WorkerUnit): WorkerRow {
  return {
    key: `unit:${unit.unit_id}`,
    label: unit.title || unit.vuln_class || unit.endpoint || unit.unit_id,
    meta: unit.unit_id,
    status: unit.status,
    attempts: unit.agent_ids?.length ?? 0,
    task: unit.target || unit.endpoint,
    started_at: unit.started_at,
    completed_at: unit.completed_at,
    updated_at: unit.updated_at,
    error: unit.error,
    claims: unit.claimed_hypotheses,
  };
}

function agentRow(agent: DelegatedAgent): WorkerRow {
  return {
    key: agent.agent_id,
    label: agent.lane || agent.task || agent.agent_id,
    meta: agent.agent_id,
    status: agent.status,
    attempts: 1,
    task: agent.task,
    target: agent.target,
    started_at: agent.started_at,
    completed_at: agent.completed_at,
    error: agent.error,
    claims: agent.claimed_hypotheses,
  };
}

/**
 * unit 行优先；同一 run 里的 legacy spawn（agent 没有 unit_id）仍按 agent
 * 一行补在后面，历史不因迁移丢失。整个 run 没有 unit 时退化为纯 agent 行。
 */
export function workerRows(view?: InstanceAgentsView): WorkerRow[] {
  const units = view?.units ?? [];
  const agents = view?.agents ?? [];
  if (units.length === 0) return agents.map(agentRow);
  // Terminal legacy spawns (no unit) are superseded by the unit rows: their
  // full history lives in the Plan module and the legacy ?agent= lane. Keep
  // only live legacy workers so the rail does not grow a second stale list.
  const liveLegacy = agents
    .filter((agent) => !agent.unit_id && (agent.status === "running" || agent.status === "queued"))
    .map(agentRow);
  return [...units.map(unitRow), ...liveLegacy].sort(compareWorkerRows);
}

/** Active work first, then newest activity (never the oldest intent first). */
function workerRowRank(row: WorkerRow): number {
  if (row.status === "running" || row.status === "paused") return 0;
  if (row.status === "queued") return 1;
  return 2;
}

function workerRowRecency(row: WorkerRow): number {
  const raw = row.completed_at || row.updated_at || row.started_at;
  const ms = raw ? Date.parse(raw) : 0;
  return Number.isFinite(ms) ? ms : 0;
}

function compareWorkerRows(a: WorkerRow, b: WorkerRow): number {
  const rank = workerRowRank(a) - workerRowRank(b);
  if (rank !== 0) return rank;
  const recency = workerRowRecency(b) - workerRowRecency(a);
  if (recency !== 0) return recency;
  return a.key.localeCompare(b.key);
}

export function WorkersModule({
  view,
  loading,
  error,
  onSelectLane,
}: {
  view?: InstanceAgentsView;
  loading: boolean;
  error: boolean;
  /** 点击 worker 行 → ?agent=<lane>，查看该 unit/agent 的转录。 */
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

  const rows = workerRows(view);
  if (rows.length === 0) {
    return (
      <div className="grid min-h-0 flex-1 place-items-center p-6">
        <EmptyState title={t("sessions.workerEmpty")} description={t("sessions.workerEmptyHint")} />
      </div>
    );
  }

  const running = rows.filter((row) => row.status === "running" || row.status === "paused");
  const queued = rows.filter((row) => row.status === "queued");
  const finished = rows.filter(
    (row) => row.status !== "running" && row.status !== "paused" && row.status !== "queued",
  );

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
        <WorkerGroup title={t("sessions.workerRunning")} rows={running} onSelectLane={onSelectLane} />
      )}
      {queued.length > 0 && (
        <WorkerGroup title={t("sessions.workerQueued")} rows={queued} onSelectLane={onSelectLane} />
      )}
      {finished.length > 0 && (
        <details className="group">
          <summary className="flex cursor-pointer items-center gap-1 border-b border-border px-3 py-1.5 text-[11px] font-medium text-muted-foreground transition-colors hover:bg-accent/30">
            <ChevronDown className="size-3.5 transition-transform group-open:rotate-180" aria-hidden />
            {t("sessions.workerFinished")} {finished.length}
          </summary>
          <WorkerGroup title={t("sessions.workerFinished")} rows={finished} muted onSelectLane={onSelectLane} />
        </details>
      )}
    </div>
  );
}

function WorkerGroup({
  title,
  rows,
  muted = false,
  onSelectLane,
}: {
  title: string;
  rows: WorkerRow[];
  muted?: boolean;
  onSelectLane?: (lane?: string) => void;
}) {
  const { t } = useI18n();
  return (
    <section aria-label={title}>
      <ul>
        {rows.map((row) => {
          const claim = row.claims?.[0];
          return (
            <li
              key={row.key}
              className={cn("border-b border-border last:border-0", muted && "opacity-70")}
            >
              <button
                type="button"
                onClick={() => onSelectLane?.(row.key)}
                className="block w-full cursor-pointer px-3 py-2 text-left text-xs transition-colors hover:bg-accent/40 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
              >
              <div className="flex min-w-0 items-center gap-2">
                <WorkerStatusDot status={row.status} />
                <span className="min-w-0 flex-1 truncate font-medium" title={row.label}>
                  {row.label}
                </span>
                {row.attempts > 1 && (
                  <span className="shrink-0 rounded bg-muted px-1 font-mono text-[10px] text-muted-foreground">
                    {row.attempts}×
                  </span>
                )}
                <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{row.meta}</span>
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
                  <span>
                    {row.status === "running"
                      ? t("sessions.workerNoClaim")
                      : row.status === "queued"
                        ? t("sessions.workerNotDispatched")
                        : t("sessions.workerIdle")}
                  </span>
                )}
                {row.task && (
                  <span className="min-w-0 truncate" title={row.task}>
                    {row.task}
                  </span>
                )}
                {row.started_at && (
                  <span className="font-mono">
                    {t("sessions.workerSince")} {shortTime(row.started_at)}
                  </span>
                )}
                {row.completed_at && (
                  <span className="font-mono">
                    {t("sessions.workerDone")} {shortTime(row.completed_at)}
                  </span>
                )}
                {row.error && <span className="w-full truncate text-red-600 dark:text-red-400">{row.error}</span>}
              </div>
              </button>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

export function WorkerStatusDot({ status }: { status: string }) {
  if (status === "running") {
    return <span className="size-1.5 shrink-0 animate-pulse rounded-full bg-emerald-500" aria-hidden />;
  }
  if (status === "queued") {
    return <span className="size-1.5 shrink-0 rounded-full bg-amber-500" aria-hidden />;
  }
  if (status === "paused") {
    return <span className="size-1.5 shrink-0 rounded-full bg-amber-400" aria-hidden />;
  }
  if (status === "failed" || status === "exhausted") {
    return <span className="size-1.5 shrink-0 rounded-full bg-red-500" aria-hidden />;
  }
  if (status === "stopped") {
    return <span className="size-1.5 shrink-0 rounded-full bg-orange-500" aria-hidden />;
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
