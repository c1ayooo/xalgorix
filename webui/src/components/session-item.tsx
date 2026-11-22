import { useEffect, useMemo, useState } from "react";
import {
  CheckCircle2,
  Hourglass,
  Loader2,
  Pause,
  XCircle,
  CircleSlash,
} from "lucide-react";
import { useEventsStore } from "@/store/events";
import { cn } from "@/lib/utils";
import { useI18n } from "@/i18n";

/**
 * 会话项语法（4zreco SessionItem 对齐）：状态图标 + mono 标识 + 实时章。
 * 对话页与任务详情会话栏共用；状态只反映真实 run 状态，不编造。
 */

const LIVE_WINDOW_MS = 5000;

/** 5s 内有新事件即视为「实时」（4zreco STREAM_WINDOW 语义）。返回集合为
 * 实例 id 与 agent_id 的并集键。附带 2s 重渲染节拍，保证过期下沉。 */
export function useLiveSessionKeys(): Set<string> {
  const events = useEventsStore((s) => s.events);
  const [, setTick] = useState(0);
  const hasRecent = useMemo(() => {
    const now = Date.now();
    const keys = new Set<string>();
    for (const e of events) {
      const at = e._receivedAt || 0;
      if (now - at <= LIVE_WINDOW_MS) {
        if (e.instance_id) keys.add(e.instance_id);
        if (e.agent_id) keys.add(e.agent_id);
      }
    }
    return keys;
  }, [events]);
  useEffect(() => {
    if (hasRecent.size === 0) return;
    const t = setInterval(() => setTick((n) => n + 1), 2000);
    return () => clearInterval(t);
  }, [hasRecent.size]);
  return hasRecent;
}

/** 事件流连接状态 → 会话栏头部的可见链路章（实时 / 重连中）。 */
export function useStreamBadge(): { live: boolean; reconnecting: boolean } {
  const status = useEventsStore((s) => s.status);
  return { live: status === "connected", reconnecting: status === "reconnecting" || status === "connecting" };
}

export function StreamBadge({ className }: { className?: string }) {
  const { t } = useI18n();
  const { live, reconnecting } = useStreamBadge();
  if (!live && !reconnecting) return null;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-sm border px-1.5 py-0.5 text-[10px] font-medium",
        live
          ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
          : "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-400",
        className,
      )}
      role="status"
    >
      <span className={cn("h-1.5 w-1.5 rounded-full", live ? "bg-emerald-500 pulse-dot" : "bg-amber-500")} />
      {live ? t("session.live") : t("session.reconnecting")}
    </span>
  );
}

/** run 状态 → 会话项状态图标（4zreco 状态语义：运行=转圈 / 完成=对勾 / 停止失败=红）。 */
export function SessionStatusIcon({ status, className }: { status?: string; className?: string }) {
  const key = (status || "").toLowerCase();
  const cls = cn("h-3.5 w-3.5 shrink-0", className);
  switch (key) {
    case "running":
      return <Loader2 className={cn(cls, "animate-spin text-emerald-600 dark:text-emerald-400")} aria-hidden />;
    case "pending":
    case "queued":
      return <Hourglass className={cn(cls, "text-amber-600 dark:text-amber-400")} aria-hidden />;
    case "paused":
      return <Pause className={cn(cls, "text-amber-600 dark:text-amber-400")} aria-hidden />;
    case "failed":
      return <XCircle className={cn(cls, "text-red-600 dark:text-red-400")} aria-hidden />;
    case "stopped":
    case "cancelled":
      return <CircleSlash className={cn(cls, "text-red-500/80")} aria-hidden />;
    case "finished":
    case "completed":
    case "saved":
      return <CheckCircle2 className={cn(cls, "text-muted-foreground")} aria-hidden />;
    default:
      return <CircleSlash className={cn(cls, "text-muted-foreground/60")} aria-hidden />;
  }
}

/** 通用会话行：状态图标 + 标识（mono）+ 附加信息 + 实时章 + 选中态。 */
export function SessionItem({
  status,
  label,
  meta,
  live,
  selected,
  onClick,
  title,
  className,
}: {
  status?: string;
  /** mono 标识（run id / #agent 短名）。 */
  label: string;
  meta?: string;
  live?: boolean;
  selected?: boolean;
  onClick?: () => void;
  title?: string;
  className?: string;
}) {
  const { t } = useI18n();
  return (
    <button
      type="button"
      onClick={onClick}
      title={title}
      className={cn(
        "flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left transition-colors",
        selected
          ? "bg-accent text-accent-foreground"
          : "text-muted-foreground hover:bg-accent/40 hover:text-foreground",
        className,
      )}
      aria-current={selected ? "true" : undefined}
    >
      <SessionStatusIcon status={status} />
      <span className="min-w-0 flex-1 truncate mono text-[11px]">{label}</span>
      {meta && <span className="shrink-0 text-[10px] text-muted-foreground tabular-nums">{meta}</span>}
      {live && (
        <span className="inline-flex shrink-0 items-center gap-1 rounded-sm border border-emerald-500/30 bg-emerald-500/10 px-1.5 py-0.5 text-[10px] font-medium text-emerald-600 dark:text-emerald-400">
          <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 pulse-dot" />
          {t("session.live")}
        </span>
      )}
    </button>
  );
}
