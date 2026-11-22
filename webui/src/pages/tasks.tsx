import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { toast } from "sonner";
import { CalendarClock, Plus, Target } from "lucide-react";
import { Link } from "react-router-dom";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { EmptyState, ErrorState } from "@/components/states";
import { Skeleton } from "@/components/ui/skeleton";
import { ScanStatusPill } from "@/components/scan-status-pill";
import { TaskFormDialog } from "@/components/task-form-dialog";
import { TaskRunButton } from "@/components/task-run-button";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { useDeleteTask, useTasks } from "@/api/queries";
import { useActiveProjectId } from "@/lib/use-active-project";
import { useI18n } from "@/i18n";
import { timeAgo } from "@/lib/utils";
import type { Task } from "@/types/api";

/**
 * Task list for the active project. Tasks are the Goal+Context hosts;
 * each row starts a run (which lands in /scans as before) or opens the
 * detail page. Deep link: /tasks?create=1 opens the form (consumed).
 */
export default function TasksPage() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const projectId = useActiveProjectId();
  const { data, isLoading, error, refetch } = useTasks(projectId ?? undefined);
  const del = useDeleteTask();

  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<Task | null>(null);
  const [deleting, setDeleting] = useState<Task | null>(null);

  const tasks = data?.tasks ?? [];

  // Consume ?create=1 deep link.
  useEffect(() => {
    if (searchParams.get("create") === "1") {
      setEditing(null);
      setFormOpen(true);
      const sp = new URLSearchParams(searchParams);
      sp.delete("create");
      setSearchParams(sp, { replace: true });
    }
  }, [searchParams, setSearchParams]);

  if (!projectId && !isLoading && !error) {
    return (
      <div className="space-y-6">
        <h1 className="font-sans text-2xl font-semibold tracking-tight">{t("tasks.title")}</h1>
        <EmptyState
          title={t("tasks.noProjectTitle")}
          description={t("tasks.noProjectDescription")}
          action={
            <Button asChild>
              <Link to="/projects">
                <Plus className="mr-1 h-4 w-4" />
                {t("project.manage")}
              </Link>
            </Button>
          }
        />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="font-sans text-2xl font-semibold tracking-tight">{t("tasks.title")}</h1>
          <p className="text-sm text-muted-foreground">{t("tasks.subtitle")}</p>
        </div>
        <Button
          onClick={() => {
            setEditing(null);
            setFormOpen(true);
          }}
        >
          <Plus className="mr-1 h-4 w-4" />
          {t("tasks.create")}
        </Button>
      </header>

      {error ? (
        <ErrorState
          title={t("tasks.loadFailed")}
          description={error instanceof Error ? error.message : ""}
          action={
            <Button size="sm" variant="outline" onClick={() => refetch()}>
              {t("common.retry")}
            </Button>
          }
        />
      ) : isLoading ? (
        <div className="space-y-3">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-24 w-full" />
          ))}
        </div>
      ) : tasks.length === 0 ? (
        <EmptyState
          title={t("tasks.emptyTitle")}
          description={t("tasks.emptyDescription")}
          action={
            <Button onClick={() => setFormOpen(true)}>
              <Plus className="mr-1 h-4 w-4" />
              {t("tasks.create")}
            </Button>
          }
        />
      ) : (
        <div className="space-y-3">
          {tasks.map((task) => (
            <Card key={task.id}>
              <CardContent className="flex flex-col gap-3 p-4 lg:flex-row lg:items-center">
                <div className="min-w-0 flex-1">
                  <Link
                    to={`/tasks/${task.id}`}
                    className="block truncate text-sm font-medium text-foreground hover:text-primary"
                  >
                    {task.name}
                  </Link>
                  <p className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">
                    {task.goal}
                  </p>
                  <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
                    {task.schedule && (
                      <span className="flex items-center gap-1">
                        <CalendarClock className="h-3 w-3" aria-hidden />
                        {t(`tasks.interval${cap(task.schedule.interval)}`)}
                      </span>
                    )}
                    {task.phase_policy?.scan_mode && (
                      <span className="mono">{task.phase_policy.scan_mode}</span>
                    )}
                    {task.objective ? (
                      <span className="rounded-sm bg-primary/10 px-1.5 py-0.5 text-primary">
                        {t("tasks.objectiveBadge")}: {task.objective.kind}
                      </span>
                    ) : (
                      <span className="text-muted-foreground/70">
                        {t("taskDetail.objectiveNotStructured")}
                      </span>
                    )}
                    <span>
                      {t("tasks.runCount")} {task.run_count ?? 0}
                    </span>
                    {(task.finding_count ?? 0) > 0 && (
                      <span className="flex items-center gap-1">
                        <Target className="h-3 w-3" aria-hidden />
                        {task.finding_count}
                      </span>
                    )}
                    {task.last_run_at && (
                      <span>
                        {t("tasks.lastRun")} {timeAgo(task.last_run_at)}
                      </span>
                    )}
                    {task.last_run_status && (
                      <ScanStatusPill status={task.last_run_status} />
                    )}
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <TaskRunButton task={task} />
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      setEditing(task);
                      setFormOpen(true);
                    }}
                  >
                    {t("common.edit")}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="text-red-600 hover:text-red-700 dark:text-red-400"
                    onClick={() => setDeleting(task)}
                    aria-label={`${t("common.delete")} ${task.name}`}
                  >
                    {t("common.delete")}
                  </Button>
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      <TaskFormDialog
        open={formOpen}
        onOpenChange={setFormOpen}
        projectId={projectId ?? ""}
        task={editing}
      />

      <AlertDialog open={!!deleting} onOpenChange={(v) => !v && setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("tasks.deleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("tasks.deleteDescriptionPrefix")} “{deleting?.name}”.{" "}
              {t("tasks.deleteDescriptionSuffix")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (!deleting) return;
                del.mutate(deleting.id, {
                  onSuccess: () => toast.success(t("toast.taskDeleted")),
                  onError: (err: Error) =>
                    toast.error(`${t("toast.taskFailed")}: ${err.message}`),
                });
                setDeleting(null);
              }}
            >
              {t("common.delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function cap(s: string) {
  return s.charAt(0).toUpperCase() + s.slice(1);
}
