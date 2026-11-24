import { ChevronDown, RotateCw } from "lucide-react";
import { useState } from "react";
import { cn } from "@/lib/utils";
import { fmtTokens } from "@/components/chat/format";
import {
  RailChild,
  RailGroup,
  RailItem,
  RailSection,
  railRoleIcon,
  type RailRole,
} from "@/components/sessions/session-item";
import type { ContentModule, RailEntry } from "@/components/sessions/modules/types";
import { useStreamBadge } from "@/components/session-item";
import { useI18n } from "@/i18n";
import type { Run } from "@/types/api";

/**
 * 会话页左栏（分组导航）。
 *
 * - 会话段：主 Agent 的 Run 列表（一段 Run = 一段主会话），只列 Run，不再把
 *   worker 塞进 Run 行的子树里——worker 是行动环的投影，属于工作区段的
 *   Workers 组。
 * - 工作区段：规划 / 决策 / 义务 / Worker 池 / 系统审计。每个模块先一行入口，
 *   模块提供的 `children`（当前 Run 的 worker 行）挂在自己的组下面，点击直达
 *   该 agent 的转录。
 *
 * 折叠只发生在主 Agent 列表；模块组始终展开。
 */
export function SessionRail({
  taskId,
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
  taskId: string;
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

  // 主 Agent 列表的折叠状态按 task 记忆；SessionRail 以 taskId 作 key 重新
  // 挂载，所以只在初始化时读一次 localStorage。
  const mainCollapseKey = `xalgorix.sessions.groups.${taskId}.main`;
  const [mainCollapsed, setMainCollapsed] = useState(() => readCollapsed(mainCollapseKey));
  const toggleMain = () =>
    setMainCollapsed((prev) => {
      const next = !prev;
      writeCollapsed(mainCollapseKey, next);
      return next;
    });

  const selectedRunInMain = runs.some((run) => run.instance_id === selectedInstanceId);

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
        <div className="flex w-full flex-col gap-0.5 p-2">
          <RailSection label={t("sessions.sectionSessions")} />
          <RailGroup
            role="main"
            label={t("sessions.groupMain")}
            count={runs.length}
            active={mainCollapsed && selectedRunInMain}
            open={!mainCollapsed}
            keepActiveChildren={mainCollapsed && selectedRunInMain}
            onToggle={toggleMain}
          >
            {runs.map((run) => {
              if (!run.instance_id) return null;
              const instanceId = run.instance_id;
              const runActive = lane === "main" && selectedInstanceId === instanceId;
              // 主列表折叠时，至少保留当前选中 Run 可见——否则切换 Run 后左栏
              // 会把选中项整个藏掉。
              if (mainCollapsed && !runActive) return null;
              return (
                <RailItem
                  key={run.id}
                  active={runActive}
                  status={run.status}
                  live={liveKeys.has(instanceId)}
                  title={run.title || run.id}
                  onClick={() => {
                    onSelectRun(instanceId);
                    onLaneChange("main");
                  }}
                />
              );
            })}
            {runs.length === 0 && (
              <p className="px-2 py-1 text-muted-foreground text-xs">{t("session.emptyRuns")}</p>
            )}
          </RailGroup>

          <RailSection className="mt-2" label={t("sessions.sectionWorkspace")} />
          {groups.map(({ module, entry }) => (
            <div key={module.id} className="flex flex-col">
              <RailItem
                active={lane === module.id}
                badge={entry.badge}
                live={entry.live}
                title={t(entry.labelKey)}
                icon={railRoleIcon(module.id as RailRole, "text-muted-foreground")}
                onClick={() => onLaneChange(module.id)}
              />
              {entry.children && entry.children.length > 0 && (
                // 模块自己的子行留在自己的组里（4zreco 的分组语法）：Workers
                // 组展开当前 Run 的每个 worker，点击直达该 agent 的转录。
                <div className="mb-1 ml-4 flex flex-col gap-0.5 border-l border-border/60 pl-1">
                  {entry.children.map((child) => (
                    <RailChild
                      key={child.id}
                      active={!!child.active}
                      status={child.status}
                      label={child.label}
                      meta={child.meta}
                      onClick={() => onLaneChange(child.id)}
                    />
                  ))}
                </div>
              )}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

function readCollapsed(key: string): boolean {
  try {
    return localStorage.getItem(key) === "1";
  } catch {
    return false;
  }
}

function writeCollapsed(key: string, collapsed: boolean) {
  try {
    localStorage.setItem(key, collapsed ? "1" : "0");
  } catch {
    // private mode / storage disabled: collapse still works for this mount
  }
}
