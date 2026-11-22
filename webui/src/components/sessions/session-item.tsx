import {
  Brain,
  CheckCircle2,
  Clock,
  Crosshair,
  History,
  Loader2,
  Pause,
  Radio,
  User,
  XCircle,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { useI18n } from "@/i18n";

/**
 * 会话页 rail 行（参照 SessionItem）：状态图标 + 标题 + 实时章。
 * 状态只反映真实 run 状态；无对应数据的能力不渲染（方案 §5.5）。
 */

export function sessionStatusIcon(status: string | undefined, className?: string) {
  const key = (status || "").toLowerCase();
  const cls = cn("size-3.5 shrink-0", className);
  switch (key) {
    case "running":
      return <Loader2 className={cn(cls, "animate-spin text-sky-500")} aria-hidden />;
    case "paused":
      return <Pause className={cn(cls, "text-amber-600 dark:text-amber-400")} aria-hidden />;
    case "pending":
    case "queued":
      return <Clock className={cn(cls, "text-muted-foreground")} aria-hidden />;
    case "failed":
    case "error":
      return <XCircle className={cn(cls, "text-red-600 dark:text-red-400")} aria-hidden />;
    case "stopped":
    case "cancelled":
    case "interrupted":
      return <Clock className={cn(cls, "text-amber-600 dark:text-amber-400")} aria-hidden />;
    case "finished":
    case "completed":
    case "saved":
      return <CheckCircle2 className={cn(cls, "text-emerald-600 dark:text-emerald-400")} aria-hidden />;
    default:
      return <Clock className={cn(cls, "text-muted-foreground")} aria-hidden />;
  }
}

export function RailGroup({
  role,
  label,
  children,
}: {
  role: "main" | "plan" | "decision" | "system" | "worker";
  label: string;
  children: React.ReactNode;
}) {
  const Icon =
    role === "main"
      ? User
      : role === "plan"
        ? Brain
        : role === "decision"
          ? Crosshair
          : role === "system"
            ? History
            : Radio;
  return (
    <div className="flex flex-col gap-0.5">
      <div className="flex items-center gap-1.5 px-2 py-1 font-medium text-muted-foreground text-xs">
        <Icon className="size-3.5 shrink-0" aria-hidden />
        {label}
      </div>
      {children}
    </div>
  );
}

export function RailItem({
  active,
  status,
  live,
  title,
  onClick,
}: {
  active: boolean;
  status?: string;
  live?: boolean;
  title: string;
  onClick: () => void;
}) {
  const { t } = useI18n();
  return (
    <div
      className={cn(
        "group/session flex w-full items-center rounded-md transition-colors",
        active ? "bg-accent text-accent-foreground" : "hover:bg-accent/50",
      )}
    >
      <button
        type="button"
        onClick={onClick}
        aria-current={active ? "true" : undefined}
        title={title}
        className="flex min-w-0 flex-1 items-center gap-1 px-2 py-1.5 text-left text-sm"
      >
        {sessionStatusIcon(status) ?? <span className="size-3.5 shrink-0" />}
        <span className="min-w-0 flex-1 truncate font-medium text-sm">{title}</span>
        {live && (
          <span className="inline-flex shrink-0 items-center gap-1 rounded bg-emerald-500/10 px-1.5 py-0.5 font-medium text-[10px] text-emerald-600 dark:text-emerald-400">
            <span className="size-1 animate-pulse rounded-full bg-emerald-500" aria-hidden />
            {t("session.live")}
          </span>
        )}
      </button>
    </div>
  );
}
