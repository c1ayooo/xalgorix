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

export function fmtBytes(n?: number): string {
  if (!n || n <= 0) return "0 B";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
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
