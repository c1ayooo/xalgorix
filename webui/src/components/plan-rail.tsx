import { useState } from "react";
import {
  ChevronDown,
  Circle,
  CircleCheck,
  CirclePlay,
  ListChecks,
  SkipForward,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { useI18n } from "@/i18n";
import type { PlanProgress, PlanState, PlanTask, PlanTaskStatus } from "@/lib/execution-trace";

const STATUS_ICON: Record<PlanTaskStatus, typeof Circle> = {
  pending: Circle,
  in_progress: CirclePlay,
  completed: CircleCheck,
  skipped: SkipForward,
};

const STATUS_TONE: Record<PlanTaskStatus, string> = {
  pending: "text-muted-foreground",
  in_progress: "text-sky-600 dark:text-sky-400",
  completed: "text-emerald-600 dark:text-emerald-400",
  skipped: "text-muted-foreground",
};

function TaskRow({ task }: { task: PlanTask }) {
  const Icon = STATUS_ICON[task.status];
  return (
    <li
      className="flex items-start gap-1.5 rounded-sm px-1.5 py-1 text-xs"
      title={task.notes || task.title}
    >
      <Icon className={cn("mt-0.5 size-3 shrink-0", STATUS_TONE[task.status])} aria-hidden />
      {task.phase ? (
        <span className="mt-px shrink-0 rounded-sm border border-border px-1 text-[10px] text-muted-foreground mono tabular-nums">
          P{task.phase}
        </span>
      ) : null}
      <span
        className={cn(
          "min-w-0 flex-1 break-words",
          task.status === "completed" && "text-muted-foreground",
          task.status === "skipped" && "text-muted-foreground line-through",
        )}
      >
        {task.title}
      </span>
      <span className="mt-px shrink-0 text-[10px] text-muted-foreground mono">
        {task.id}
      </span>
    </li>
  );
}

/**
 * Plan rail — the planning half of the execution visualisation.
 *
 * One quiet strip above the transcript: disposed/total, the active phase and
 * the current task. Expanding reveals the reconstructed task list (from
 * build_plan / update_plan events) grouped in plan order. When the engine
 * auto-planned without publishing a task list, the strip says so and shows the
 * instance counters instead of inventing rows.
 */
export function PlanRail({
  plan,
  progress,
  currentPhase,
  defaultOpen = true,
}: {
  plan: PlanState;
  progress: PlanProgress;
  /** Engine-reported phase number (instance.current_phase), if any. */
  currentPhase?: number;
  defaultOpen?: boolean;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(defaultOpen);

  const hasTasks = plan.tasks.length > 0;
  const hasProgress = progress.total > 0;
  const disposed = progress.completed + progress.skipped;
  const pct = Math.round(progress.ratio * 100);

  return (
    <div className="min-w-0 shrink-0 border-b border-border bg-muted/20">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-controls="plan-rail-body"
        className="flex w-full min-w-0 items-center gap-2 px-3 py-1.5 text-left transition-colors hover:bg-accent/30"
      >
        <ListChecks className="size-3.5 shrink-0 text-violet-600 dark:text-violet-300" aria-hidden />
        <span className="shrink-0 text-[11px] font-medium text-muted-foreground">
          {t("plan.title")}
        </span>
        {hasProgress ? (
          <>
            <span className="shrink-0 text-[11px] text-foreground mono tabular-nums">
              {disposed}/{progress.total}
            </span>
            <span
              className="hidden h-1 w-24 shrink-0 overflow-hidden rounded-full bg-border sm:block"
              role="progressbar"
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={pct}
              aria-label={t("plan.progress")}
            >
              <span
                className="block h-full rounded-full bg-emerald-500"
                style={{ width: `${pct}%` }}
              />
            </span>
            {progress.skipped > 0 && (
              <span className="shrink-0 text-[10px] text-muted-foreground mono tabular-nums">
                {t("plan.skipped")} {progress.skipped}
              </span>
            )}
          </>
        ) : (
          <span className="shrink-0 text-[11px] text-muted-foreground">
            {t("plan.empty")}
          </span>
        )}
        {(currentPhase ?? 0) > 0 && (
          <span className="shrink-0 rounded-sm border border-border px-1 text-[10px] text-muted-foreground mono tabular-nums">
            {t("plan.phase")} {currentPhase}/22
          </span>
        )}
        {progress.current && (
          <span className="min-w-0 flex-1 truncate text-[11px] text-muted-foreground">
            <span className="text-sky-600 dark:text-sky-400">
              {t("plan.current")}
            </span>{" "}
            {progress.current.title}
          </span>
        )}
        {!progress.current && <span className="flex-1" />}
        <ChevronDown
          className={cn(
            "size-3.5 shrink-0 text-muted-foreground transition-transform",
            !open && "-rotate-90",
          )}
          aria-hidden
        />
      </button>

      {open && (
        <div id="plan-rail-body" className="px-3 pb-2">
          {plan.incomplete && (
            <p className="mb-1.5 rounded-sm border border-amber-500/30 bg-amber-500/10 px-2 py-1 text-[11px] text-amber-700 dark:text-amber-300">
              {t("plan.incomplete")}
            </p>
          )}
          {hasTasks ? (
            <ul className="max-h-36 space-y-0.5 overflow-y-auto" aria-label={t("plan.title")}>
              {plan.tasks.map((task) => (
                <TaskRow key={task.id} task={task} />
              ))}
            </ul>
          ) : (
            <p className="rounded-sm border border-dashed border-border px-2 py-1.5 text-[11px] text-muted-foreground">
              {hasProgress ? t("plan.auto") : t("plan.emptyHint")}
            </p>
          )}
        </div>
      )}
    </div>
  );
}
