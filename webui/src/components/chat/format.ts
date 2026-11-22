/** Shared formatting helpers for the chat surface (docs/CHAT_PAGE_REDESIGN.md). */

type Translate = (key: string) => string;

function withCount(template: string, n: number): string {
  return template.replace("{n}", String(n));
}

export function fmtRelative(iso: string | undefined, t: Translate): string {
  if (!iso) return "";
  const at = Date.parse(iso);
  if (Number.isNaN(at)) return "";
  const diff = Date.now() - at;
  if (diff < 60_000) return t("chat.justNow");
  const minutes = Math.floor(diff / 60_000);
  if (minutes < 60) return withCount(t("chat.minutesAgo"), minutes);
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return withCount(t("chat.hoursAgo"), hours);
  const days = Math.floor(hours / 24);
  if (days < 7) return withCount(t("chat.daysAgo"), days);
  return new Date(at).toLocaleDateString(undefined, { month: "numeric", day: "numeric" });
}

/** 列表行的绝对时间戳（参照会话项口径：M/D HH:mm）。 */
export function fmtListDate(iso: string | undefined): string {
  if (!iso) return "";
  const at = Date.parse(iso);
  if (Number.isNaN(at)) return "";
  return new Date(at).toLocaleDateString("zh-CN", {
    month: "numeric",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function fmtBytes(n?: number): string {
  if (!n || n <= 0) return "0 B";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

/** 紧凑 token 计数（1234 → 1.2k，2_000_000 → 2M）。 */
export function fmtTokens(n?: number): string {
  if (!n || n <= 0) return "0";
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 10_000 ? 0 : 1)}k`;
  return String(n);
}

/** 运行时长（毫秒跨度 → 1m30s / 2h05m）。 */
export function fmtDuration(ms: number): string {
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m${String(s % 60).padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  return `${h}h${String(m % 60).padStart(2, "0")}m`;
}

/**
 * agent_id → 泳道短名（去实例前缀；coordinator / 裸 instance id = 主循环）。
 * 与 execution-trace 的 agentLabel 同一口径，抽出供会话页分组复用。
 */
export function agentLane(instanceId: string | undefined, agentId: string | undefined): string {
  if (!agentId) return "";
  const short = instanceId ? agentId.replace(`${instanceId}/`, "") : agentId;
  const tail = short.replace(/^.*\//, "") || short;
  return tail;
}

export function isMainAgentLane(instanceId: string | undefined, agentId: string | undefined): boolean {
  if (!agentId) return true; // 无 agent_id 的行（人工/系统）归主会话
  const lane = agentLane(instanceId, agentId);
  return lane === "coordinator" || (!!instanceId && agentId === instanceId);
}

/**
 * Status → dot class + i18n key. Unknown statuses return no key: the caller
 * shows the raw value instead of inventing a translation.
 */
export function statusTone(status?: string): { dot: string; labelKey?: string } {
  switch (status) {
    case "running":
      return { dot: "bg-emerald-500 animate-pulse", labelKey: "chat.status.running" };
    case "paused":
      return { dot: "bg-amber-500", labelKey: "chat.status.paused" };
    case "pending":
    case "queued":
      return { dot: "bg-sky-500", labelKey: "chat.status.pending" };
    case "finished":
    case "completed":
      return { dot: "bg-muted-foreground/50", labelKey: "chat.status.finished" };
    case "failed":
    case "error":
      return { dot: "bg-red-500", labelKey: "chat.status.failed" };
    case "stopped":
    case "interrupted":
      return { dot: "bg-orange-500", labelKey: "chat.status.stopped" };
    default:
      return { dot: "bg-muted-foreground/40" };
  }
}
