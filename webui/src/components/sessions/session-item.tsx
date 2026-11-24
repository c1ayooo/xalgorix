import type { ReactNode } from "react";
import {
  Brain,
  CheckCircle2,
  ChevronDown,
  CircleAlert,
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

export type RailRole = "main" | "plan" | "decision" | "system" | "obligation" | "workers" | "worker";

/** role → 图标。组头与模块叶子行共用，避免同一语义出现两种图标。 */
export function railRoleIcon(role: RailRole, className?: string) {
  const Icon =
    role === "main"
      ? User
      : role === "plan"
        ? Brain
        : role === "decision"
          ? Crosshair
          : role === "system"
            ? History
            : role === "obligation"
              ? CircleAlert
              : Radio;
  return <Icon className={cn("size-3.5 shrink-0", className)} aria-hidden />;
}

/**
 * 左栏段标题（会话 / 工作区）：纯标签，不可点、不可折叠。
 * 两段式结构把「会话」（Run / worker）与「工作区投影」（plan/decision/…）
 * 分开，避免组头与组内条目同名造成的重复层级。
 */
export function RailSection({ label, className }: { label: string; className?: string }) {
  return (
    <div
      className={cn(
        "px-2 pt-2 pb-0.5 font-medium text-[10px] text-muted-foreground/70 uppercase tracking-wider",
        className,
      )}
    >
      {label}
    </div>
  );
}

/**
 * 可折叠的列表组头（当前只有「主 Agent」Run 列表使用）。
 * 只有真实列表才配 chevron；单条目模块是叶子行，不走这里。
 * 折叠且组内含当前选中项时显示品牌色小圆点，避免选中项消失后无提示。
 */
export function RailGroup({
  role,
  label,
  count,
  active,
  open = true,
  /** 折叠时仍渲染 children（调用方负责只保留选中路径）。 */
  keepActiveChildren,
  onToggle,
  children,
}: {
  role: RailRole;
  label: string;
  /** 组内条目数；0 不渲染徽标。 */
  count?: number;
  /** 折叠时组内是否包含当前选中项。 */
  active?: boolean;
  open?: boolean;
  keepActiveChildren?: boolean;
  onToggle?: () => void;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-col gap-0.5">
      {onToggle ? (
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={open}
          className="flex items-center gap-1.5 rounded-md px-2 py-1 text-left font-medium text-muted-foreground text-xs transition-colors hover:bg-accent/40 hover:text-foreground"
        >
          {railRoleIcon(role)}
          <span className="min-w-0 flex-1 truncate">{label}</span>
          {typeof count === "number" && count > 0 && (
            <span className="shrink-0 font-mono text-[10px] tabular-nums opacity-60">{count}</span>
          )}
          {active && !open && <span className="size-1.5 shrink-0 rounded-full bg-primary" aria-hidden />}
          <ChevronDown
            className={cn("size-3 shrink-0 transition-transform", !open && "-rotate-90")}
            aria-hidden
          />
        </button>
      ) : (
        <div className="flex items-center gap-1.5 px-2 py-1 font-medium text-muted-foreground text-xs">
          {railRoleIcon(role)}
          <span className="min-w-0 flex-1 truncate">{label}</span>
        </div>
      )}
      {(open || keepActiveChildren) && children}
    </div>
  );
}

/**
 * 叶子行（Run / 模块入口）。`expandable` 只给有子项的节点：
 * 行主体负责选中，右侧独立 chevron 负责展开/收起——两个点击目标互不吞并。
 */
export function RailItem({
  active,
  status,
  live,
  badge,
  title,
  icon,
  onClick,
  expandable,
  expanded,
  onToggleExpand,
  expandLabel,
  hasActiveChild,
}: {
  active: boolean;
  status?: string;
  live?: boolean;
  /** 计数徽标，例如运行中 `2/3`、义务缺口 `4`。 */
  badge?: string;
  title: string;
  /** 覆盖状态图标（模块入口用 role 图标）。 */
  icon?: ReactNode;
  onClick: () => void;
  expandable?: boolean;
  expanded?: boolean;
  onToggleExpand?: () => void;
  expandLabel?: string;
  /** 折叠时其中一个子项处于选中态。 */
  hasActiveChild?: boolean;
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
        className="flex min-w-0 flex-1 items-center gap-1.5 px-2 py-1.5 text-left text-sm"
      >
        {icon ?? sessionStatusIcon(status)}
        <span className="min-w-0 flex-1 truncate font-medium text-sm">{title}</span>
        {badge && (
          <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground tabular-nums">
            {badge}
          </span>
        )}
        {live && (
          <span className="inline-flex shrink-0 items-center gap-1 rounded bg-emerald-500/10 px-1.5 py-0.5 font-medium text-[10px] text-emerald-600 dark:text-emerald-400">
            <span className="size-1 animate-pulse rounded-full bg-emerald-500" aria-hidden />
            {t("session.live")}
          </span>
        )}
      </button>
      {expandable && (
        <button
          type="button"
          onClick={onToggleExpand}
          aria-expanded={expanded}
          aria-label={expandLabel}
          title={expandLabel}
          className="relative mr-1 flex size-6 shrink-0 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        >
          {hasActiveChild && !expanded && (
            <span className="absolute top-1 right-1 size-1.5 rounded-full bg-primary" aria-hidden />
          )}
          <ChevronDown
            className={cn("size-3 transition-transform", !expanded && "-rotate-90")}
            aria-hidden
          />
        </button>
      )}
    </div>
  );
}

/** 会话段里的子行（某个 Run 的 worker）。 */
export function RailChild({
  active,
  status,
  label,
  meta,
  onClick,
}: {
  active: boolean;
  status?: string;
  label: string;
  meta?: string;
  onClick: () => void;
}) {
  return (
    <div
      className={cn(
        "flex w-full items-center rounded-md transition-colors",
        active ? "bg-accent text-accent-foreground" : "hover:bg-accent/40",
      )}
    >
      <button
        type="button"
        onClick={onClick}
        aria-current={active ? "true" : undefined}
        title={label}
        className="flex min-w-0 flex-1 items-center gap-1.5 px-1.5 py-1 text-left"
      >
        {sessionStatusIcon(status)}
        <span className="min-w-0 flex-1 truncate font-medium text-[11px]">{label}</span>
        {meta && <span className="shrink-0 font-mono text-[9px] text-muted-foreground">{meta}</span>}
      </button>
    </div>
  );
}
