import { ChevronDown, RotateCw } from "lucide-react";
import { cn } from "@/lib/utils";
import { fmtTokens } from "@/components/chat/format";
import { RailGroup, RailItem } from "@/components/sessions/session-item";
import { isWorkerLane } from "@/components/sessions/modules/registry";
import type {
  ContentModule,
  RailEntry,
  SessionsModuleId,
} from "@/components/sessions/modules/types";
import { useStreamBadge } from "@/components/session-item";
import { useI18n } from "@/i18n";
import type { Run } from "@/types/api";

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
}) {
  const { t } = useI18n();
  const { live: sseLive, reconnecting: sseReconnecting } = useStreamBadge();

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
            {runs.map((run) =>
              run.instance_id ? (
                <RailItem
                  key={run.id}
                  active={!lane && selectedInstanceId === run.instance_id}
                  status={run.status}
                  live={liveKeys.has(run.instance_id)}
                  title={run.title || run.id}
                  onClick={() => {
                    onSelectRun(run.instance_id!);
                    onLaneChange(undefined);
                  }}
                />
              ) : null,
            )}
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
            </RailGroup>
          ))}
        </div>
      </div>
    </div>
  );
}
