import { useEffect, useState } from "react";
import { useProjects, useCreateProject, useDeleteProject } from "@/api/queries";
import { useProjectSelection } from "@/store/project";
import { useI18n } from "@/i18n";
import { toast } from "sonner";
import { FolderKanban, Plus, Trash2 } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
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
import { EmptyState, ErrorState } from "@/components/states";
import { Skeleton } from "@/components/ui/skeleton";
import { timeAgo } from "@/lib/utils";
import type { Project } from "@/types/api";

/**
 * Project management: list + create + delete, and selection. The project is
 * the authorization boundary — its scope.targets feed the engine's target
 * validation, so a project without targets cannot start runs.
 */
export default function ProjectsPage() {
  const { t } = useI18n();
  const { data, isLoading, error, refetch } = useProjects();
  const create = useCreateProject();
  const del = useDeleteProject();
  const { projectId, setProjectId } = useProjectSelection();

  const [formOpen, setFormOpen] = useState(false);
  const [deleting, setDeleting] = useState<Project | null>(null);

  const projects = data?.projects ?? [];

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="font-sans text-2xl font-semibold tracking-tight">{t("projects.title")}</h1>
          <p className="text-sm text-muted-foreground">{t("projects.subtitle")}</p>
        </div>
        <Button onClick={() => setFormOpen(true)}>
          <Plus className="mr-1 h-4 w-4" />
          {t("projects.create")}
        </Button>
      </header>

      {error ? (
        <ErrorState
          title={t("projects.loadFailed")}
          description={error instanceof Error ? error.message : ""}
          action={
            <Button size="sm" variant="outline" onClick={() => refetch()}>
              {t("common.retry")}
            </Button>
          }
        />
      ) : isLoading ? (
        <div className="space-y-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-20 w-full" />
          ))}
        </div>
      ) : projects.length === 0 ? (
        <EmptyState
          title={t("projects.emptyTitle")}
          description={t("projects.emptyDescription")}
          action={
            <Button onClick={() => setFormOpen(true)}>
              <Plus className="mr-1 h-4 w-4" />
              {t("projects.create")}
            </Button>
          }
        />
      ) : (
        <div className="space-y-3">
          {projects.map((p) => (
            <Card key={p.id}>
              <CardContent className="flex flex-col gap-3 p-4 lg:flex-row lg:items-center">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <FolderKanban className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
                    <span className="truncate text-sm font-medium">{p.name}</span>
                    {p.id === projectId && (
                      <span className="rounded-sm border border-emerald-500/30 bg-emerald-500/10 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-emerald-700 dark:text-emerald-300">
                        {t("projects.active")}
                      </span>
                    )}
                  </div>
                  <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
                    <span className="mono">{p.scope.targets.join(", ") || t("projects.noTargets")}</span>
                    <span>
                      {t("tasks.runCount")} {p.task_count ?? 0}
                    </span>
                    <span>{timeAgo(p.created_at)}</span>
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  {p.id !== projectId && (
                    <Button size="sm" variant="outline" onClick={() => setProjectId(p.id)}>
                      {t("projects.switchTo")}
                    </Button>
                  )}
                  <Button
                    size="icon"
                    variant="ghost"
                    className="text-red-600 hover:text-red-700 dark:text-red-400"
                    onClick={() => setDeleting(p)}
                    aria-label={`${t("common.delete")} ${p.name}`}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      <ProjectFormDialog open={formOpen} onOpenChange={setFormOpen} />

      <AlertDialog open={!!deleting} onOpenChange={(v) => !v && setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("projects.deleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("projects.deleteDescriptionPrefix")} “{deleting?.name}”.{" "}
              {t("projects.deleteDescriptionSuffix")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (!deleting) return;
                del.mutate(deleting.id, {
                  onSuccess: () => toast.success(t("toast.projectDeleted")),
                  onError: (err: Error) =>
                    toast.error(`${t("toast.projectFailed")}: ${err.message}`),
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

function ProjectFormDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
}) {
  const { t } = useI18n();
  const create = useCreateProject();
  const { projectId, setProjectId } = useProjectSelection();
  const [name, setName] = useState("");
  const [targets, setTargets] = useState("");
  const [ports, setPorts] = useState("");

  useEffect(() => {
    if (open) {
      setName("");
      setTargets("");
      setPorts("");
    }
  }, [open]);

  function submit() {
    if (!name.trim()) return;
    const scope = {
      targets: targets
        .split(/[\n,]/)
        .map((s) => s.trim())
        .filter(Boolean),
      allow_ports: ports
        .split(/[\n,]/)
        .map((s) => Number.parseInt(s.trim(), 10))
        .filter((n) => Number.isFinite(n) && n > 0),
    };
    create.mutate(
      { name: name.trim(), scope },
      {
        onSuccess: (proj) => {
          toast.success(`${t("toast.projectCreated")}: ${proj.name}`);
          if (!projectId) setProjectId(proj.id);
          onOpenChange(false);
        },
        onError: (err: Error) =>
          toast.error(`${t("toast.projectFailed")}: ${err.message}`),
      },
    );
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("projects.createTitle")}</DialogTitle>
          <DialogDescription>{t("projects.formDescription")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="proj-name">{t("projects.fieldName")}</Label>
            <Input id="proj-name" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="proj-targets">{t("projects.fieldTargets")}</Label>
            <Textarea
              id="proj-targets"
              rows={3}
              value={targets}
              onChange={(e) => setTargets(e.target.value)}
              placeholder={"acme.com\napi.acme.com"}
            />
            <p className="text-[11px] text-muted-foreground">{t("projects.targetsHint")}</p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="proj-ports">{t("projects.fieldPorts")}</Label>
            <Input
              id="proj-ports"
              value={ports}
              onChange={(e) => setPorts(e.target.value)}
              placeholder="80, 443"
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" size="sm" onClick={() => onOpenChange(false)} disabled={create.isPending}>
            {t("common.cancel")}
          </Button>
          <Button size="sm" onClick={submit} disabled={create.isPending || !name.trim()}>
            {t("common.create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
