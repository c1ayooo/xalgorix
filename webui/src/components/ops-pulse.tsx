import { Link } from "react-router-dom";
import { FolderKanban, Hourglass, Radio } from "lucide-react";
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
  const running =
    instances?.instances?.filter((i) => i.status === "running").length ?? 0;

  return <PulseLine
    projectName={active?.name}
    pendingTotal={pendingTotal}
    nearestDeadline={nearest}
    running={running}
  />;
}

function PulseLine({
  projectName,
  pendingTotal,
  nearestDeadline,
  running,
}: {
  projectName?: string;
  pendingTotal: number;
  nearestDeadline?: string;
  running: number;
}) {
  const { t } = useI18n();
  const { remainingMs, expired } = useCountdown(nearestDeadline);
  const urgent = pendingTotal > 0 && !expired && (remainingMs ?? 0) < 60_000;

  return (
    <div className="flex h-9 shrink-0 items-center gap-3 border-b border-border bg-card/60 px-4 text-xs">
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
    </div>
  );
}
