import { Link, useLocation } from "react-router-dom";
import { Coins, FolderKanban, Hourglass, Radio } from "lucide-react";
import { useApprovalsSummary, useInstances, useProjects } from "@/api/queries";
import { useActiveProjectId } from "@/lib/use-active-project";
import { useI18n } from "@/i18n";
import { useCountdown, formatCountdown } from "@/lib/use-countdown";
import { cn } from "@/lib/utils";

/**
 * OpsPulse — the single global "pulse" strip (4zreco pattern).
 *
 * One quiet line under the topbar answering the operator's standing
 * questions: which project am I in, what is waiting for my decision
 * (with the nearest deadline countdown), what is actively running.
 * It stays visible even when everything is zero — a flat line reads
 * as "all clear" at a glance; clicking the pending chip opens the
 * approval queue.
 */
export function OpsPulse() {
  const { t } = useI18n();
  const projectId = useActiveProjectId();
  const { data: projects } = useProjects();
  const { data: summary } = useApprovalsSummary();
  const { data: instances } = useInstances();

  const projectsList = projects?.projects ?? [];
  const active = projectsList.find((p) => p.id === projectId) ?? null;

  const pendingTotal = summary?.pending_total ?? 0;
  const nearest = summary?.nearest_deadline;
  const runningInstances =
    instances?.instances?.filter((i) => i.status === "running") ?? [];
  const running = runningInstances.length;
  // Token consumption of the active runs; the strip is about "what is
  // happening now", so finished runs are deliberately excluded.
  const tokens = runningInstances.reduce(
    (sum, i) => sum + (i.total_tokens || 0),
    0,
  );

  const { pathname } = useLocation();
  // 内容壳与页面主体保持一致：/chat 是全出血页（自带 p-3 sm:p-4 md:p-6），
  // 其余页面走 app-shell 的 `mx-auto max-w-7xl px-4 sm:px-6` 容器。脉搏条
  // 只让内层内容跟随该壳，border-b 仍通栏，避免宽屏下文字贴住侧栏。
  const contentShell =
    pathname === "/chat"
      ? "px-3 sm:px-4 md:px-6"
      : "mx-auto max-w-7xl px-4 sm:px-6";

  return (
    <PulseLine
      projectName={active?.name}
      pendingTotal={pendingTotal}
      nearestDeadline={nearest}
      running={running}
      tokens={tokens}
      contentShell={contentShell}
    />
  );
}

function PulseLine({
  projectName,
  pendingTotal,
  nearestDeadline,
  running,
  tokens,
  contentShell,
}: {
  projectName?: string;
  pendingTotal: number;
  nearestDeadline?: string;
  running: number;
  tokens: number;
  contentShell: string;
}) {
  const { t } = useI18n();
  const { remainingMs, expired } = useCountdown(nearestDeadline);
  const urgent = pendingTotal > 0 && !expired && (remainingMs ?? 0) < 60_000;

  return (
    <div className="shrink-0 border-b border-border bg-card/60">
      <div className={cn("flex h-9 items-center gap-3 text-xs", contentShell)}>
        {projectName ? (
          <span className="flex min-w-0 items-center gap-1.5 text-muted-foreground">
            <FolderKanban className="h-3 w-3 shrink-0" aria-hidden />
            <span className="truncate mono">{projectName}</span>
          </span>
        ) : (
          <span className="text-muted-foreground">{t("project.none")}</span>
        )}

        <span className="h-3 w-px shrink-0 bg-border" aria-hidden />

        <Link
          to="/approvals"
          className={cn(
            "flex shrink-0 items-center gap-1.5 rounded-sm px-1.5 py-0.5 transition-colors",
            pendingTotal > 0
              ? urgent
                ? "bg-red-500/10 text-red-700 dark:text-red-300 hover:bg-red-500/20"
                : "bg-amber-500/10 text-amber-700 dark:text-amber-300 hover:bg-amber-500/20"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          <Hourglass className="h-3 w-3 shrink-0" aria-hidden />
          <span className="tabular-nums mono">
            {t("pulse.pending")} {pendingTotal}
          </span>
          {pendingTotal > 0 && remainingMs !== null && !expired && (
            <span className="tabular-nums mono opacity-80">
              {formatCountdown(remainingMs)}
            </span>
          )}
        </Link>

        <span className="h-3 w-px shrink-0 bg-border" aria-hidden />

        <span
          className={cn(
            "flex shrink-0 items-center gap-1.5",
            running > 0 ? "text-emerald-700 dark:text-emerald-300" : "text-muted-foreground",
          )}
        >
          <Radio className="h-3 w-3 shrink-0" aria-hidden />
          <span className="tabular-nums mono">
            {t("pulse.running")} {running}
          </span>
        </span>

        <span className="h-3 w-px shrink-0 bg-border" aria-hidden />

        <span className="flex shrink-0 items-center gap-1.5 text-muted-foreground">
          <Coins className="h-3 w-3 shrink-0" aria-hidden />
          <span className="tabular-nums mono">
            {t("pulse.tokens")} {tokens.toLocaleString()}
          </span>
        </span>
      </div>
    </div>
  );
}
