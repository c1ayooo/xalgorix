import { useEffect, useState } from "react";
import { useProjects, useCreateProject, useDeleteProject } from "@/api/queries";
import { useProjectSelection } from "@/store/project";
import { useI18n } from "@/i18n";
import { toast } from "sonner";
import {
  ArrowRight,
  FolderKanban,
  Loader2,
  Plus,
  Rocket,
  ShieldCheck,
  ShieldOff,
  SlidersHorizontal,
  Trash2,
  X,
} from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
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
import {
  ScopeFields,
  draftProblems,
  draftToScope,
  emptyScopeDraft,
  type ScopeDraft,
} from "@/components/scope-fields";
import { ScopeConfigDialog } from "@/components/scope-config-dialog";
import { QuickTaskDialog } from "@/components/quick-task-dialog";
import { timeAgo } from "@/lib/utils";
import type { Project } from "@/types/api";

/**
 * 项目页 = 授权边界的配置与起点。
 *
 * 三块交互共享同一个对象（platform.Scope）与同一份状态：
 *
 *   新建项目   名称 + 范围 + 围栏（一次填完，落库即生效）
 *   范围配置   PUT /api/projects/{id}/scope（含围栏参数 + 试算校验）
 *   快速创建   建任务 + 立即运行，起跑前用同一 Scope 试算目标
 *
 * 状态衔接：新建成功后把项目设为当前项目，并在页顶挂一条「下一步」引导条
 * （配置围栏 / 快速创建 / 关闭）；保存范围后 projects + projectScope 两个
 * 缓存键同时失效，卡片徽标与快速创建入口立即反映新边界。
 */
export default function ProjectsPage() {
  const { t } = useI18n();
  const { data, isLoading, error, refetch } = useProjects();
  const del = useDeleteProject();
  const { projectId, setProjectId } = useProjectSelection();

  const [formOpen, setFormOpen] = useState(false);
  const [deleting, setDeleting] = useState<Project | null>(null);
  const [scopeTarget, setScopeTarget] = useState<Project | null>(null);
  const [quickTarget, setQuickTarget] = useState<Project | null>(null);
  // 新建后的引导状态：只活在本次会话里，关闭或做任一步即清空。
  const [justCreated, setJustCreated] = useState<Project | null>(null);

  const projects = data?.projects ?? [];
  // 卡片操作要拿最新的项目副本（列表 15s 轮询，弹窗打开期间可能已变）。
  const liveProject = (id: string | null) =>
    id ? projects.find((p) => p.id === id) ?? null : null;

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

      {justCreated && (
        <Card className="border-emerald-500/30 bg-emerald-500/5">
          <CardContent className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between">
            <div className="min-w-0">
              <p className="text-sm font-medium">
                {t("scope.createdTitle")}
                <span className="mono"> · {justCreated.name}</span>
              </p>
              <p className="text-[11px] text-muted-foreground">
                {justCreated.scope.fence?.enforce
                  ? t("scope.createdNextFenced")
                  : t("scope.createdNextUnfenced")}
              </p>
            </div>
            <div className="flex shrink-0 flex-wrap items-center gap-2">
              <Button size="sm" variant="outline" onClick={() => setScopeTarget(justCreated)}>
                <SlidersHorizontal className="mr-1 h-3.5 w-3.5" />
                {t("scope.configure")}
              </Button>
              <Button size="sm" onClick={() => setQuickTarget(justCreated)}>
                <Rocket className="mr-1 h-3.5 w-3.5" />
                {t("scope.quickCreate")}
              </Button>
              <Button
                size="icon"
                variant="ghost"
                onClick={() => setJustCreated(null)}
                aria-label={t("common.cancel")}
              >
                <X className="h-4 w-4" />
              </Button>
            </div>
          </CardContent>
        </Card>
      )}

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
          {projects.map((p) => {
            const fence = p.scope.fence;
            const fenced = !!fence?.enforce;
            return (
              <Card key={p.id}>
                <CardContent className="flex flex-col gap-3 p-4 lg:flex-row lg:items-center">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <FolderKanban className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
                      <span className="truncate text-sm font-medium">{p.name}</span>
                      {p.id === projectId && (
                        <span className="rounded-sm border border-emerald-500/30 bg-emerald-500/10 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-emerald-700 dark:text-emerald-300">
                          {t("projects.active")}
                        </span>
                      )}
                      <span
                        className={
                          fenced
                            ? "inline-flex items-center gap-1 rounded-sm border border-emerald-500/30 bg-emerald-500/10 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-emerald-700 dark:text-emerald-300"
                            : "inline-flex items-center gap-1 rounded-sm border border-border px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-muted-foreground"
                        }
                        title={fenced ? t("scope.badgeOnHint") : t("scope.badgeOffHint")}
                      >
                        {fenced ? (
                          <ShieldCheck className="h-3 w-3" aria-hidden />
                        ) : (
                          <ShieldOff className="h-3 w-3" aria-hidden />
                        )}
                        {fenced ? t("scope.badgeOn") : t("scope.badgeOff")}
                      </span>
                    </div>
                    <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
                      <span className="mono">
                        {p.scope.targets.join(", ") || t("projects.noTargets")}
                      </span>
                      {p.scope.allow_ports && p.scope.allow_ports.length > 0 && (
                        <span className="mono">
                          {t("projects.fieldPorts")}: {p.scope.allow_ports.join(", ")}
                        </span>
                      )}
                      {fenced && fence?.deny_targets && fence.deny_targets.length > 0 && (
                        <span className="mono text-amber-700 dark:text-amber-400">
                          −{fence.deny_targets.join(", ")}
                        </span>
                      )}
                      <span>
                        {t("tasks.runCount")} {p.task_count ?? 0}
                      </span>
                      <span>{timeAgo(p.created_at)}</span>
                    </div>
                  </div>
                  <div className="flex shrink-0 flex-wrap items-center gap-2">
                    <Button size="sm" variant="outline" onClick={() => setScopeTarget(p)}>
                      <SlidersHorizontal className="mr-1 h-3.5 w-3.5" />
                      {t("scope.configure")}
                    </Button>
                    <Button size="sm" variant="outline" onClick={() => setQuickTarget(p)}>
                      <Rocket className="mr-1 h-3.5 w-3.5" />
                      {t("scope.quickCreate")}
                    </Button>
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
            );
          })}
        </div>
      )}

      <ProjectFormDialog
        open={formOpen}
        onOpenChange={setFormOpen}
        onCreated={(project) => {
          setProjectId(project.id);
          setJustCreated(project);
        }}
      />

      <ScopeConfigDialog
        open={!!scopeTarget}
        onOpenChange={(v) => !v && setScopeTarget(null)}
        project={liveProject(scopeTarget?.id ?? null) ?? scopeTarget}
        onSaved={(view) => {
          // 引导条里的项目副本也要跟着新边界走（否则徽标会撒谎）。
          if (justCreated && justCreated.id === view.project_id) {
            setJustCreated({ ...justCreated, scope: view.scope });
          }
        }}
      />

      <QuickTaskDialog
        open={!!quickTarget}
        onOpenChange={(v) => !v && setQuickTarget(null)}
        project={liveProject(quickTarget?.id ?? null) ?? quickTarget}
      />

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

/**
 * 新建项目：名称 + 范围 + 围栏一次填完。
 *
 * 提交前只挡客户端可判定的问题（围栏启用却无目标、端口非法、deny 带端口）；
 * 其余以后端校验为准（400 的 problems 原样展示）。成功后自动把新项目设为
 * 当前项目，并交给页面挂「下一步」引导条。
 */
function ProjectFormDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onCreated: (project: Project) => void;
}) {
  const { t } = useI18n();
  const create = useCreateProject();
  const [name, setName] = useState("");
  const [draft, setDraft] = useState<ScopeDraft>(emptyScopeDraft);
  const [serverProblems, setServerProblems] = useState<string[]>([]);

  useEffect(() => {
    if (open) {
      setName("");
      setDraft(emptyScopeDraft());
      setServerProblems([]);
    }
  }, [open]);

  const problems = draftProblems(draft);

  function submit() {
    if (!name.trim() || problems.length > 0) return;
    setServerProblems([]);
    create.mutate(
      { name: name.trim(), scope: draftToScope(draft) },
      {
        onSuccess: (proj) => {
          toast.success(`${t("toast.projectCreated")}: ${proj.name}`);
          onOpenChange(false);
          onCreated(proj);
        },
        onError: (err: Error) => {
          setServerProblems([err.message]);
          toast.error(`${t("toast.projectFailed")}: ${err.message}`);
        },
      },
    );
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !create.isPending && onOpenChange(v)}>
      <DialogContent className="max-h-[88vh] max-w-2xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t("projects.createTitle")}</DialogTitle>
          <DialogDescription>{t("projects.formDescription")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="proj-name">{t("projects.fieldName")}</Label>
            <Input id="proj-name" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <ScopeFields
            draft={draft}
            onChange={(next) => {
              setDraft(next);
              setServerProblems([]);
            }}
            disabled={create.isPending}
            idPrefix="proj-new"
          />
          {problems.length > 0 && (
            <ul className="space-y-1 text-[11px] text-amber-700 dark:text-amber-400">
              {problems.map((key) => (
                <li key={key}>· {t(key)}</li>
              ))}
            </ul>
          )}
          {serverProblems.length > 0 && (
            <ul className="space-y-1 rounded-sm border border-red-500/30 bg-red-500/5 px-2 py-1.5 text-[11px] text-red-700 dark:text-red-400">
              {serverProblems.map((p) => (
                <li key={p}>· {p}</li>
              ))}
            </ul>
          )}
        </div>
        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onOpenChange(false)}
            disabled={create.isPending}
          >
            {t("common.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={submit}
            disabled={create.isPending || !name.trim() || problems.length > 0}
          >
            {create.isPending ? (
              <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />
            ) : (
              <ArrowRight className="mr-1 h-3.5 w-3.5" />
            )}
            {t("common.create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
