import { useEventsStore } from "@/store/events";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";

const STATUS_KEY: Record<string, string> = {
  idle: "ws.status.idle",
  connecting: "ws.status.connecting",
  connected: "ws.status.connected",
  disconnected: "ws.status.disconnected",
  reconnecting: "ws.status.reconnecting",
};

const STATUS_COLOR: Record<string, string> = {
  idle: "bg-neutral-500",
  connecting: "bg-amber-500",
  connected: "bg-emerald-500",
  disconnected: "bg-red-500",
  reconnecting: "bg-amber-500",
};

export function ConnectionStatus({ compact }: { compact?: boolean }) {
  const { t } = useI18n();
  const status = useEventsStore((s) => s.status);
  const label = STATUS_KEY[status] ? t(STATUS_KEY[status]) : status;
  return (
    <div
      className={cn(
        "inline-flex items-center gap-2 rounded-md border border-border bg-card px-2 py-1 text-xs",
        compact && "py-0.5",
      )}
      title={`SSE: ${label}`}
    >
      <span
        className={cn(
          "h-1.5 w-1.5 rounded-full",
          STATUS_COLOR[status] || "bg-neutral-500",
          status === "connecting" || status === "reconnecting" ? "pulse-dot" : "",
        )}
      />
      <span className="text-muted-foreground">{label}</span>
    </div>
  );
}

export function ConnectionBanner() {
  const { t } = useI18n();
  const status = useEventsStore((s) => s.status);
  const err = useEventsStore((s) => s.lastError);
  if (status === "connected" || status === "idle" || status === "connecting") {
    return null;
  }
  return (
    <div className="border-b border-amber-500/30 bg-amber-500/10 px-6 py-2 text-xs text-amber-700 dark:text-amber-200">
      {status === "reconnecting"
        ? t("ws.banner.reconnecting")
        : t("ws.banner.disconnected")}
      {err ? ` — ${err}` : t("ws.banner.pausedSuffix")}
    </div>
  );
}
