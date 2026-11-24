import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import {
  Download,
  FileText,
  Plus,
  Search,
  Trash2,
  XCircle,
} from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/states";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
import {
  useCancelReport,
  useCreateTaskReport,
  useDeleteReport,
  useProjects,
  useReportsList,
  useTaskRuns,
  useTasks,
} from "@/api/queries";
import { timeAgo } from "@/lib/utils";
import { useActiveProjectId } from "@/lib/use-active-project";
import { useI18n } from "@/i18n";
import type { Report, ReportFormat, ReportMode } from "@/types/api";

type TaskGroup = {
  taskId: string;
  taskName: string;
  targets: string[];
  items: Report[];
  latest: Report;
};

function groupReports(reports: Report[]): TaskGroup[] {
  const byTask = new Map<string, Report[]>();
  for (const report of reports) {
    const list = byTask.get(report.task_id) ?? [];
    list.push(report);
    byTask.set(report.task_id, list);
  }
  const groups: TaskGroup[] = [];
  for (const [taskId, items] of byTask) {
    items.sort((a, b) => b.revision - a.revision);
    const latest = items[0];
    const targets = Array.from(
      new Set(items.flatMap((r) => r.targets ?? [])),
    ).sort();
    groups.push({
      taskId,
      taskName: latest.task_name || taskId.slice(0, 8),
      targets,
      items,
      latest,
    });
  }
  groups.sort(
    (a, b) =>
      new Date(b.latest.created_at).getTime() -
      new Date(a.latest.created_at).getTime(),
  );
  return groups;
}

function statusTone(status: Report["status"]): string {
  switch (status) {
    case "ready":
      return "text-emerald-600 border-emerald-400/40 dark:text-emerald-400";
    case "failed":
      return "text-red-600 border-red-400/40 dark:text-red-400";
    case "cancelled":
      return "text-muted-foreground";
    case "generating":
    case "queued":
      return "text-amber-700 border-amber-400/40 dark:text-amber-400";
    default:
      return "";
  }
}

function StatusBadge({ report }: { report: Report }) {
  const { t } = useI18n();
  const label = t(`reports.status.${report.status}`, report.status);
  return (
    <Badge variant="outline" className={statusTone(report.status)}>
      {label}
    </Badge>
  );
}

function totalArtifacts(report: Report) {
  return report.artifacts.filter((a) => a.kind === "total");
}

function subArtifacts(report: Report) {
  return report.artifacts
    .filter((a) => a.kind === "sub")
    .sort((a, b) => (a.target ?? "").localeCompare(b.target ?? ""));
}

export default function ReportsPage() {
  const { t } = useI18n();
  const { data, isLoading } = useReportsList();
  const [query, setQuery] = useState("");
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [deleting, setDeleting] = useState<Report | null>(null);
  const [genOpen, setGenOpen] = useState(false);
  const del = useDeleteReport();
  const cancel = useCancelReport();

  const groups = useMemo(() => groupReports(data?.items ?? []), [data]);
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return groups;
    return groups.filter(
      (g) =>
        g.taskName.toLowerCase().includes(q) ||
        g.taskId.toLowerCase().includes(q) ||
        g.targets.some((target) => target.toLowerCase().includes(q)),
    );
  }, [groups, query]);

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">
            {t("reports.title", "报告")}
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {t(
              "reports.subtitle",
              "一个任务对应一份报告；复跑生成新版本，历史版本可查。报告只在手动点击时生成。",
            )}
          </p>
        </div>
        <Button onClick={() => setGenOpen(true)}>
          <Plus className="h-4 w-4" />
          {t("reports.generate", "生成报告")}
        </Button>
      </div>

      <Card>
        <CardContent className="p-3">
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t("reports.searchPlaceholder", "按任务、目标搜索报告…")}
              className="pl-8"
            />
          </div>
        </CardContent>
      </Card>

      <Card className="overflow-hidden">
        {isLoading ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-16 w-full" />
            ))}
          </div>
        ) : filtered.length === 0 ? (
          <EmptyState
            icon={<FileText className="h-6 w-6" />}
            title={t("reports.emptyTitle", "暂无报告")}
            description={t(
              "reports.emptyDescription",
              "选择任务并手动生成报告；生成后这里显示任务级的最新版本与历史版本。",
            )}
          />
        ) : (
          <ul className="divide-y divide-border">
            {filtered.map((group) => {
              const latest = group.latest;
              const older = group.items.slice(1);
              const active =
                latest.status === "queued" || latest.status === "generating";
              const totals = totalArtifacts(latest);
              const subs = subArtifacts(latest);
              const isExpanded = !!expanded[group.taskId];
              return (
                <li key={group.taskId} className="px-4 py-4 text-sm">
                  <div className="flex flex-wrap items-start gap-3">
                    <FileText className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <Link
                          to={`/tasks/${group.taskId}`}
                          className="truncate font-medium text-foreground hover:underline"
                        >
                          {group.taskName}
                        </Link>
                        <Badge variant="outline" className="mono text-[10px]">
                          r{latest.revision}
                        </Badge>
                        <StatusBadge report={latest} />
                        <Badge variant="outline" className="text-[10px]">
                          {t(`reports.mode.${latest.mode}`, latest.mode)}
                        </Badge>
                      </div>
                      <p className="mt-1 truncate text-[11px] text-muted-foreground">
                        {group.targets.join("、") || latest.task_id}
                        {" · "}
                        {timeAgo(latest.created_at)}
                        {latest.error ? ` · ${latest.error}` : ""}
                        {active && latest.progress ? ` · ${latest.progress}` : ""}
                      </p>
                    </div>
                    <div className="flex flex-wrap items-center gap-1">
                      {totals.map((artifact) => (
                        <Button
                          key={artifact.format}
                          asChild
                          size="sm"
                          variant="outline"
                        >
                          <a href={artifact.url}>
                            <Download className="h-3.5 w-3.5" />
                            {artifact.format.toUpperCase()}
                          </a>
                        </Button>
                      ))}
                      {active && (
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={cancel.isPending}
                          onClick={() => cancel.mutate(latest.id)}
                        >
                          <XCircle className="h-3.5 w-3.5" />
                          {t("reports.cancel", "取消")}
                        </Button>
                      )}
                      {older.length > 0 && (
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() =>
                            setExpanded((prev) => ({
                              ...prev,
                              [group.taskId]: !prev[group.taskId],
                            }))
                          }
                        >
                          {t("reports.history", "历史版本")} ({older.length})
                        </Button>
                      )}
                      {!active && (
                        <Button
                          size="sm"
                          variant="ghost"
                          className="text-red-600 hover:text-red-700 dark:text-red-400"
                          onClick={() => setDeleting(latest)}
                        >
                          <Trash2 className="h-3.5 w-3.5" />
                        </Button>
                      )}
                    </div>
                  </div>

                  {subs.length > 0 && (
                    <details className="mt-3 rounded-md border border-border/60 bg-muted/20 px-3 py-2">
                      <summary className="cursor-pointer text-xs text-muted-foreground">
                        {t("reports.subReports", "子报告")} ({subs.length})
                      </summary>
                      <div className="mt-2 flex flex-wrap gap-2">
                        {subs.map((artifact) => (
                          <a
                            key={`${artifact.scan_id}-${artifact.format}`}
                            href={artifact.url}
                            className="mono rounded border border-border px-2 py-1 text-[11px] hover:bg-muted"
                          >
                            {artifact.target || artifact.scan_id} ·{" "}
                            {artifact.format.toUpperCase()}
                          </a>
                        ))}
                      </div>
                    </details>
                  )}

                  {isExpanded && older.length > 0 && (
                    <ul className="mt-3 space-y-2 border-l-2 border-border pl-3">
                      {older.map((report) => (
                        <li
                          key={report.id}
                          className="flex flex-wrap items-center gap-2 text-xs"
                        >
                          <span className="mono">r{report.revision}</span>
                          <StatusBadge report={report} />
                          <span className="text-muted-foreground">
                            {timeAgo(report.created_at)}
                          </span>
                          {totalArtifacts(report).map((artifact) => (
                            <a
                              key={artifact.format}
                              href={artifact.url}
                              className="rounded border border-border px-2 py-0.5 hover:bg-muted"
                            >
                              {artifact.format.toUpperCase()}
                            </a>
                          ))}
                          <Button
                            size="sm"
                            variant="ghost"
                            className="ml-auto text-red-600 dark:text-red-400"
                            onClick={() => setDeleting(report)}
                          >
                            <Trash2 className="h-3 w-3" />
                          </Button>
                        </li>
                      ))}
                    </ul>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </Card>

      <GenerateReportDialog open={genOpen} onOpenChange={setGenOpen} />

      <AlertDialog
        open={!!deleting}
        onOpenChange={(v) => !v && setDeleting(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("reports.deleteTitle", "删除报告？")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                "reports.confirmDelete",
                "将永久删除该报告版本及其 PDF / Markdown / HTML 文件，扫描记录不受影响。",
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel", "取消")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={del.isPending}
              onClick={() => {
                if (deleting) del.mutate(deleting.id);
                setDeleting(null);
              }}
            >
              {t("common.delete", "删除")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

export function GenerateReportDialog({
  open,
  onOpenChange,
  defaultProjectId,
  defaultTaskId,
  lockTask = false,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  defaultProjectId?: string;
  defaultTaskId?: string;
  lockTask?: boolean;
}) {
  const { t } = useI18n();
  const { data: projectsData } = useProjects();
  const projects = projectsData?.projects ?? [];
  const activeProjectId = useActiveProjectId();
  const [projectId, setProjectId] = useState(defaultProjectId ?? "");
  const [taskId, setTaskId] = useState(defaultTaskId ?? "");
  const [runId, setRunId] = useState("");
  const [formats, setFormats] = useState<ReportFormat[]>(["pdf", "md", "html"]);
  const [mode, setMode] = useState<ReportMode>("template");
  const [model, setModel] = useState("");
  const create = useCreateTaskReport();
  const { data: tasksData } = useTasks(projectId || undefined);
  const tasks = tasksData?.tasks ?? [];
  const { data: runsData } = useTaskRuns(taskId || undefined);
  const runs = runsData?.items ?? [];

  useEffect(() => {
    // Same scope as the rest of the UI (stored selection, else the project with
    // active runs) so the dialog never preselects a project nothing else is
    // scoped to.
    if (!projectId && activeProjectId) setProjectId(activeProjectId);
  }, [projectId, activeProjectId]);

  useEffect(() => {
    if (lockTask) return;
    setTaskId("");
    setRunId("");
  }, [projectId, lockTask]);

  const toggleFormat = (format: ReportFormat) => {
    setFormats((prev) =>
      prev.includes(format)
        ? prev.filter((f) => f !== format)
        : [...prev, format],
    );
  };

  const submit = () => {
    if (!taskId || formats.length === 0) return;
    create.mutate(
      {
        taskId,
        req: {
          formats,
          mode,
          language: "zh-CN",
          template: "standard",
          ...(runId ? { run_ids: [runId] } : {}),
          ...(mode === "agent" && model.trim() ? { model: model.trim() } : {}),
        },
      },
      {
        onSuccess: () => {
          onOpenChange(false);
          setRunId("");
        },
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>{t("reports.generate", "生成报告")}</DialogTitle>
          <DialogDescription>
            {t(
              "reports.generateHint",
              "按任务聚合所有目标与 wildcard 子域，生成一份总报告和每个目标的子报告。模板模式不消耗 token；Agent 模式由模型撰写叙述。",
            )}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          {!lockTask && (
            <div className="grid gap-3 sm:grid-cols-2">
              <label className="space-y-1 text-xs">
                <span className="text-muted-foreground">
                  {t("reports.project", "项目")}
                </span>
                <Select value={projectId} onValueChange={setProjectId}>
                  <SelectTrigger>
                    <SelectValue
                      placeholder={t("reports.selectProject", "选择项目")}
                    />
                  </SelectTrigger>
                  <SelectContent>
                    {projects.map((project) => (
                      <SelectItem key={project.id} value={project.id}>
                        {project.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </label>
              <label className="space-y-1 text-xs">
                <span className="text-muted-foreground">
                  {t("reports.task", "任务")}
                </span>
                <Select value={taskId} onValueChange={setTaskId}>
                  <SelectTrigger>
                    <SelectValue placeholder={t("reports.selectTask", "选择任务")} />
                  </SelectTrigger>
                  <SelectContent>
                    {tasks.map((task) => (
                      <SelectItem key={task.id} value={task.id}>
                        {task.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </label>
            </div>
          )}

          <label className="block space-y-1 text-xs">
            <span className="text-muted-foreground">
              {t("reports.run", "运行")}
            </span>
            <Select
              value={runId || "__latest__"}
              onValueChange={(value) => setRunId(value === "__latest__" ? "" : value)}
            >
              <SelectTrigger>
                <SelectValue placeholder={t("reports.latestRun", "最新运行（默认）")} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="__latest__">{t("reports.latestRun", "最新运行（默认）")}</SelectItem>
                {runs.map((run) => (
                  <SelectItem key={run.id} value={run.id}>
                    {run.id.slice(0, 8)} · {run.status} · {timeAgo(run.started_at)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </label>

          <div className="space-y-1 text-xs">
            <span className="text-muted-foreground">
              {t("reports.formats", "导出格式")}
            </span>
            <div className="flex flex-wrap gap-3 pt-1">
              {(["pdf", "md", "html"] as ReportFormat[]).map((format) => (
                <label key={format} className="flex items-center gap-1.5">
                  <input
                    type="checkbox"
                    checked={formats.includes(format)}
                    onChange={() => toggleFormat(format)}
                    className="h-3.5 w-3.5 accent-primary"
                  />
                  <span className="uppercase">{format}</span>
                </label>
              ))}
            </div>
          </div>

          <div className="grid gap-3 sm:grid-cols-2">
            <label className="space-y-1 text-xs">
              <span className="text-muted-foreground">
                {t("reports.modeLabel", "生成模式")}
              </span>
              <Select
                value={mode}
                onValueChange={(value) => setMode(value as ReportMode)}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="template">
                    {t("reports.mode.template", "模板（0 token）")}
                  </SelectItem>
                  <SelectItem value="agent">
                    {t("reports.mode.agent", "报告撰写 Agent")}
                  </SelectItem>
                </SelectContent>
              </Select>
            </label>
            {mode === "agent" && (
              <label className="space-y-1 text-xs">
                <span className="text-muted-foreground">
                  {t("reports.model", "模型（可选）")}
                </span>
                <Input
                  value={model}
                  onChange={(e) => setModel(e.target.value)}
                  placeholder={t("reports.modelHint", "留空使用当前配置")}
                />
              </label>
            )}
          </div>

          {mode === "agent" && (
            <p className="rounded-md border border-amber-400/40 bg-amber-500/5 px-3 py-2 text-[11px] text-amber-700 dark:text-amber-400">
              {t(
                "reports.agentCostHint",
                "Agent 模式会调用模型撰写叙述并消耗 token；校验失败时自动回退模板，报告仍可交付。",
              )}
            </p>
          )}
          {create.isError && (
            <p className="text-xs text-red-600 dark:text-red-400">
              {(create.error as Error).message}
            </p>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.cancel", "取消")}
          </Button>
          <Button
            onClick={submit}
            disabled={!taskId || formats.length === 0 || create.isPending}
          >
            {create.isPending
              ? t("reports.starting", "提交中…")
              : t("reports.start", "开始生成")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
