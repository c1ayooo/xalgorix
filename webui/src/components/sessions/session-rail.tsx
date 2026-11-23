import { ChevronDown, RotateCw } from "lucide-react";
import { cn } from "@/lib/utils";
import { fmtTokens } from "@/components/chat/format";
import { RailGroup, RailItem } from "@/components/sessions/session-item";
import { isWorkerLane } from "@/components/sessions/modules/registry";
import { WorkerStatusDot } from "@/components/sessions/modules/workers-module";
import type {
  ContentModule,
  RailEntry,
  SessionsModuleId,
} from "@/components/sessions/modules/types";
import { useStreamBadge } from "@/components/session-item";
import { useI18n } from "@/i18n";
import type { DelegatedAgent, Run, TaskWorkerRun } from "@/types/api";

/**
 * 会话页左栏：折叠开关 / 链路章 / 任务 token 汇总 + 角色模块分组。
 *
 * 分组不再写死（旧实现固定 plan/decision/system 三组 + 事件推导的 worker 组），
 * 而是由 modules/registry 的可见模块生成；每组的徽标（如 `2/3`、`+1`）由模块
 * 自己提供。主 Agent 组仍是任务下的 Run 列表（每个 Run 一段主会话）。
 */
export function SessionRail({
  runs,
  selectedInstanceId,
  onSelectRun,
  lane,
  onLaneChange,
  groups,
  liveKeys,
  totalTokens,
  listOpen,
  onToggleList,
  activeTitle,
  workerRuns,
  onOpenWorker,
}: {
  runs: Run[];
  selectedInstanceId?: string;
  onSelectRun: (instanceId: string) => void;
  /** 当前角色/泳道（?agent= 深链语义）：空=主会话；模块 id；或 worker agent_id。 */
  lane?: string;
  onLaneChange: (lane: string | undefined) => void;
  groups: { module: ContentModule; entry: RailEntry }[];
  liveKeys: Set<string>;
  totalTokens: number;
  listOpen: boolean;
  onToggleList: () => void;
  /** 折叠态标题栏显示的当前会话名。 */
  activeTitle: string;
  /** 任务级 worker 历史（每个 run 的全部委派 worker，含已结束）。 */
  workerRuns?: TaskWorkerRun[];
  /** 打开某 run 下某 worker 的转录：一次写入 ?run=&agent=。 */
  onOpenWorker?: (instanceId: string, agentId: string) => void;
}) {
  const { t } = useI18n();
  const { live: sseLive, reconnecting: sseReconnecting } = useStreamBadge();
  const workersByInstance = new Map<string, DelegatedAgent[]>();
  for (const run of workerRuns ?? []) {
    if (run.instance_id && run.workers.agents.length > 0) {
      workersByInstance.set(run.instance_id, run.workers.agents);
    }
  }

  return (
    <div className="flex min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-card">
      <div className="border-b border-border px-3 py-2">
        <div className="flex items-center justify-between gap-2">
          <button
            type="button"
            onClick={onToggleList}
            aria-expanded={listOpen}
            aria-controls="session-list-panel"
            className="flex min-w-0 flex-1 items-center gap-1 text-left font-medium text-muted-foreground text-xs transition-colors hover:text-foreground lg:pointer-events-none"
          >
            <ChevronDown
              className={cn("size-3.5 shrink-0 transition-transform lg:hidden", !listOpen && "-rotate-90")}
              aria-hidden
            />
            <span className="shrink-0">{t("sessions.list")}</span>
            {!listOpen && (
              <span className="min-w-0 truncate text-foreground lg:hidden" title={activeTitle}>
                · {activeTitle}
              </span>
            )}
          </button>
          <span
            role="status"
            title={sseLive ? t("sessions.liveTip") : t("sessions.reconnectTip")}
            className={cn(
              "inline-flex shrink-0 items-center gap-1 text-xs",
              sseLive
                ? "text-emerald-600 dark:text-emerald-400"
                : sseReconnecting
                  ? "text-amber-600 dark:text-amber-400"
                  : "text-muted-foreground",
            )}
          >
            {sseLive ? (
              <>
                <span className="size-1 animate-pulse rounded-full bg-emerald-500" aria-hidden />
                {t("session.live")}
              </>
            ) : sseReconnecting ? (
              <>
                <RotateCw className="size-3" aria-hidden />
                {t("session.reconnecting")}
              </>
            ) : null}
          </span>
        </div>
        {totalTokens > 0 && (
          <div className="mt-2 flex min-w-0 flex-wrap items-center gap-1 text-muted-foreground text-xs">
            <span>{t("sessions.totalTokens")}</span>
            <span aria-hidden>·</span>
            <span className="rounded bg-muted px-1.5 py-0.5 font-mono tabular-nums">{fmtTokens(totalTokens)}</span>
          </div>
        )}
      </div>

      <div
        id="session-list-panel"
        className={cn("min-h-0 flex-1 overflow-y-auto", !listOpen && "max-lg:hidden")}
      >
        <div className="flex w-full flex-col gap-3 p-2">
          <RailGroup role="main" label={t("sessions.groupMain")}>
            {runs.map((run) => {
              if (!run.instance_id) return null;
              // 选中 run 的 worker 由下方 Workers 组逐行列出（含历史），这里
              // 只为其它 run 保留历史快捷入口，避免同一条 worker 出现两次。
              const workers =
                run.instance_id === selectedInstanceId
                  ? []
                  : (workersByInstance.get(run.instance_id) ?? []);
              return (
                <div key={run.id} className="flex flex-col">
                  <RailItem
                    active={!lane && selectedInstanceId === run.instance_id}
                    status={run.status}
                    live={liveKeys.has(run.instance_id)}
                    title={run.title || run.id}
                    onClick={() => {
                      onSelectRun(run.instance_id!);
                      onLaneChange(undefined);
                    }}
                  />
                  {workers.length > 0 && (
                    // 历史优先：该 run 委派过的每个 worker 都常驻一行（运行中/排队/
                    // 已结束都在），点击直接打开它的转录，live 只是状态点不是过滤条件。
                    <div className="mb-1 ml-3 border-l border-border/60 pl-1">
                      {workers.map((worker) => {
                        const workerActive = selectedInstanceId === run.instance_id && lane === worker.agent_id;
                        return (
                          <div
                            key={worker.agent_id}
                            className={cn(
                              "flex w-full items-center rounded-md transition-colors",
                              workerActive ? "bg-accent text-accent-foreground" : "hover:bg-accent/40",
                            )}
                          >
                            <button
                              type="button"
                              onClick={() => onOpenWorker?.(run.instance_id!, worker.agent_id)}
                              aria-current={workerActive ? "true" : undefined}
                              title={worker.task || worker.lane}
                              className="flex min-w-0 flex-1 items-center gap-1.5 px-1.5 py-1 text-left"
                            >
                              <WorkerStatusDot status={worker.status} />
                              <span className="min-w-0 flex-1 truncate font-medium text-[11px]">{worker.lane}</span>
                              <span className="shrink-0 font-mono text-[9px] text-muted-foreground">
                                {worker.agent_id}
                              </span>
                            </button>
                          </div>
                        );
                      })}
                    </div>
                  )}
                </div>
              );
            })}
            {runs.length === 0 && <p className="px-2 py-1 text-muted-foreground text-xs">{t("session.emptyRuns")}</p>}
          </RailGroup>

          {groups.map(({ module, entry }) => (
            <RailGroup key={module.id} role={module.id as SessionsModuleId} label={t(entry.labelKey)}>
              <RailItem
                active={lane === module.id || (module.id === "workers" && isWorkerLane(lane))}
                badge={entry.badge}
                live={entry.live}
                title={t(entry.labelKey)}
                onClick={() => onLaneChange(module.id)}
              />
              {entry.children && entry.children.length > 0 && (
                // 一个 worker 一行：运行中/排队/已结束（PG 历史）都在，点击
                // 直接打开该 agent 的转录，不需要先进组再点一次。
                <div className="mb-1 ml-3 border-l border-border/60 pl-1">
                  {entry.children.map((child) => (
                    <div
                      key={child.id}
                      className={cn(
                        "flex w-full items-center rounded-md transition-colors",
                        child.active ? "bg-accent text-accent-foreground" : "hover:bg-accent/40",
                      )}
                    >
                      <button
                        type="button"
                        onClick={() => onLaneChange(child.id)}
                        aria-current={child.active ? "true" : undefined}
                        title={child.label}
                        className="flex min-w-0 flex-1 items-center gap-1.5 px-1.5 py-1 text-left"
                      >
                        <WorkerStatusDot status={child.status as DelegatedAgent["status"]} />
                        <span className="min-w-0 flex-1 truncate font-medium text-[11px]">{child.label}</span>
                        {child.meta && (
                          <span className="shrink-0 font-mono text-[9px] text-muted-foreground">{child.meta}</span>
                        )}
                      </button>
                    </div>
                  ))}
                </div>
              )}
            </RailGroup>
          ))}
        </div>
      </div>
    </div>
  );
}
