import { useMemo, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { ArrowLeft, ChevronRight, FileText, Target } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { EmptyState, ErrorState } from "@/components/states";
import { SeverityBadge } from "@/components/severity-badge";
import { ScanStatusPill } from "@/components/scan-status-pill";
import { SessionsTab } from "@/components/sessions-tab";
import { LedgerTab } from "@/components/ledger-tab";
import { IntelTab } from "@/components/intel-tab";
import { TokenUsageTab } from "@/components/token-usage";
import { ScanFilesBrowser } from "@/components/scan-files-browser";
import { ApprovalInlineBar } from "@/components/approval-inline-bar";
import { TaskObjectivePanel } from "@/components/task-objective-panel";
import { TaskRunButton, taskRunState } from "@/components/task-run-button";
import { TaskGoalDialog } from "@/components/task-goal-dialog";
import {
  useApprovals,
  useProjects,
  useTask,
  useTaskFindings,
  useTaskRuns,
} from "@/api/queries";
import { api } from "@/api/client";
import { severityRank, timeAgo, cn } from "@/lib/utils";
import { useI18n } from "@/i18n";
import { useRunSelection } from "@/hooks/use-run-selection";
import { RunTraffic } from "@/components/run-traffic";
import type { Approval, Run } from "@/types/api";
import type { FlatFinding } from "@/lib/findings";

/** 本页的 tab 词汇（4zreco 交互骨架 × REDESIGN §7.2 内容处方：会话默认 + 消耗）。 */
type TaskDetailTab =
  | "sessions"
  | "overview"
  | "findings"
  | "intel"
  | "approvals"
  | "ledger"
  | "files"
  | "runs"
  | "reports"
  | "usage";

const TAB_VALUES: TaskDetailTab[] = [
  "sessions",
  "overview",
  "findings",
  "intel",
  "approvals",
  "ledger",
  "files",
  "runs",
  "reports",
  "usage",
];

/** 旧深链兼容：双栏时代的 tab 名映射到新词汇；未知值回默认会话。 */
function normalizeTab(raw: string | null): TaskDetailTab {
  if (raw === "pulse") return "overview";
  if ((TAB_VALUES as string[]).includes(raw ?? "")) return raw as TaskDetailTab;
  return "sessions";
}

/**
 * Task detail implements REDESIGN §7.2 with UI_DESIGN §4/§6 tokens and
 * composition: sticky task header, full-page tabs, Run-scoped projections,
 * and a human-only session view (CORE_PHILOSOPHY §3.2/§8). Operational event
 * details stay in Run/ledger/approval views rather than masquerading as chat.
 * `?run=&tab=` is the deep-link contract; legacy `pulse` maps to `overview`.
 */
export default function TaskDetailPage() {
  const { taskId } = useParams<{ taskId: string }>();
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const { data: task, isLoading, error, refetch } = useTask(taskId);
  const { data: runsData, isLoading: runsLoading } = useTaskRuns(taskId);
  const { data: findings } = useTaskFindings(taskId);
  const { data: projectsData } = useProjects();
  // 运行 tab 行内展开的流量面板（run id 集合；懒加载在 RunTraffic 内部）。
  const [trafficRuns, setTrafficRuns] = useState<Set<string>>(() => new Set());
  const toggleRunTraffic = (runId: string) =>
    setTrafficRuns((prev) => {
      const next = new Set(prev);
      if (next.has(runId)) next.delete(runId);
      else next.add(runId);
      return next;
    });

  const runs = runsData?.items ?? [];

  // 深链 ?run=&agent=&tab=：URL 是会话选择的唯一权威（与对话页同一 hook）。
  function writeParams(mutate: (sp: URLSearchParams) => void) {
    const sp = new URLSearchParams(searchParams);
    mutate(sp);
    setSearchParams(sp, { replace: true });
  }
  const { selectedInstanceId, selectRun } = useRunSelection(
    runs,
    searchParams.get("run"),
    (id) =>
      writeParams((sp) => {
        if (id) sp.set("run", id);
        else sp.delete("run");
        sp.delete("agent"); // 切换会话回到整段视角
      }),
  );
  const tab = normalizeTab(searchParams.get("tab"));
  // 一键「新目标」弹窗（无活跃 run 且已有 ≥1 次运行时入口可见）。
  const [goalOpen, setGoalOpen] = useState(false);

  // run 级 tab（账本/文件/报告）跟随会话选择；未选中时回退最新有实例的 run。
  const latestInstanceId = runs.find((r) => r.instance_id)?.instance_id;
  const scopedInstanceId = selectedInstanceId ?? latestInstanceId;
  const scopedRun = runs.find((r) => r.instance_id === scopedInstanceId);
  const scopedRunning = scopedRun?.status === "running";

  const project = useMemo(
    () => projectsData?.projects.find((p) => p.id === task?.project_id),
    [projectsData, task?.project_id],
  );

  function openRunInSessions(instanceId: string) {
    writeParams((sp) => {
      sp.set("run", instanceId);
      sp.set("tab", "sessions");
    });
  }

  if (error) {
    return (
      <div className="space-y-6">
        <BackLink />
        <ErrorState
          title={t("taskDetail.loadFailed")}
          description={error instanceof Error ? error.message : ""}
          action={
            <Button size="sm" variant="outline" onClick={() => refetch()}>
              {t("common.retry")}
            </Button>
          }
        />
      </div>
    );
  }
  if (isLoading || !task) {
    return (
      <div className="space-y-6">
        <BackLink />
        <Skeleton className="h-32 w-full" />
      </div>
    );
  }

  const runState = taskRunState(task, runs);

  return (
    <>
      {/* 一键「新目标」：保存目标 + 立即发起下一次运行。 */}
      <TaskGoalDialog open={goalOpen} onOpenChange={setGoalOpen} task={task} />
      <Tabs
        value={tab}
        onValueChange={(v) => writeParams((sp) => sp.set("tab", v))}
        className="flex min-h-0 flex-1 flex-col gap-0 lg:h-[calc(100vh-8.75rem)]"
      >
        {/* sticky 头部（4zreco 三行语法）。 */}
        <header className="sticky top-0 z-10 border-b border-border bg-background/95 backdrop-blur">
          <div className="flex min-w-0 flex-wrap items-center gap-2 py-2">
            <Link
              to="/tasks"
              aria-label={t("taskDetail.back")}
              title={t("taskDetail.back")}
              className="rounded-md p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
            >
              <ArrowLeft className="size-4" />
            </Link>
            <h1 className="min-w-0 truncate text-sm font-semibold">{task.name}</h1>
            <code className="shrink-0 rounded bg-muted px-1 py-0.5 font-mono text-xs text-muted-foreground">
              {task.id}
            </code>
            {project && (
              <Link
                to={`/projects/${project.id}`}
                className="shrink-0 transition-colors hover:text-foreground"
                title={project.name}
              >
                <Badge variant="outline" className="max-w-40">
                  <span className="truncate">{project.name}</span>
                </Badge>
              </Link>
            )}
            {/* 任务级运行状态章（运行状态机投影）。 */}
            <Badge
              variant={runState === "running" ? "success" : runState === "idle" ? "muted" : "warning"}
              className="shrink-0 gap-1"
            >
              {runState === "running" && (
                <span className="size-1 rounded-full bg-emerald-500 pulse-dot" aria-hidden />
              )}
              {t(`taskDetail.state.${runState}`)}
            </Badge>
            <div className="ml-auto flex shrink-0 items-center gap-2">
              {/* 目标跑完（无活跃 run 且已有运行历史）→ 提供一键续驱入口。 */}
              {runState === "idle" && runs.length > 0 && (
                <Button variant="outline" onClick={() => setGoalOpen(true)}>
                  <Target className="h-4 w-4" />
                  {t("tasks.newGoal")}
                </Button>
              )}
              {/* 运行状态机：运行 → 排队中 → 暂停 → 恢复（与列表页同一组件）。 */}
              <TaskRunButton task={task} runs={runs} />
            </div>
          </div>
          <p className="truncate pb-1.5 text-xs text-muted-foreground" title={task.goal}>
            {task.goal}
          </p>
          {task.instruction && (
            <p
              className="truncate pb-1.5 text-muted-foreground text-xs"
              title={task.instruction}
            >
              {task.instruction}
            </p>
          )}
          <TabsList className="w-full justify-start overflow-x-auto rounded-none border-b-0">
            <TabsTrigger value="sessions">{t("taskDetail.tabSessions")}</TabsTrigger>
            <TabsTrigger value="overview">{t("taskDetail.tabOverview")}</TabsTrigger>
            <TabsTrigger value="findings">
              {t("taskDetail.tabFindings")} ({task.finding_count ?? findings?.length ?? 0})
            </TabsTrigger>
            <TabsTrigger value="intel">{t("taskDetail.tabIntel")}</TabsTrigger>
            <TabsTrigger value="approvals">{t("taskDetail.tabApprovals")}</TabsTrigger>
            <TabsTrigger value="ledger">{t("taskDetail.tabLedger")}</TabsTrigger>
            <TabsTrigger value="files">{t("taskDetail.tabFiles")}</TabsTrigger>
            <TabsTrigger value="runs">
              {t("taskDetail.tabRuns")} ({task.run_count ?? runs.length})
            </TabsTrigger>
            <TabsTrigger value="reports">{t("taskDetail.tabReports")}</TabsTrigger>
            <TabsTrigger value="usage">{t("taskDetail.tabUsage")}</TabsTrigger>
          </TabsList>
        </header>

        {/* 内容区（4zreco：flex-1 p-4 lg:p-6；TabsContent mt-0）。 */}
        <div className="min-h-0 flex-1 overflow-y-auto p-4 lg:p-6">
          <TabsContent value="sessions" className="mt-0">
            <SessionsTab
              taskId={task.id}
              runs={runs}
              selectedInstanceId={selectedInstanceId}
              onSelectRun={selectRun}
            />
          </TabsContent>

          {/* 概览：合并双栏时代的 态势+概览 —— objective 面板单挂载。 */}
          <TabsContent value="overview" className="mt-0 space-y-4">
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <StatCard label={t("taskDetail.statRuns")} value={String(task.run_count ?? 0)} />
              <StatCard label={t("taskDetail.statFindings")} value={String(task.finding_count ?? 0)} />
              <StatCard
                label={t("taskDetail.statLastRun")}
                value={task.last_run_at ? timeAgo(task.last_run_at) : "—"}
              />
              <StatCard
                label={t("taskDetail.statSchedule")}
                value={task.schedule ? t(`tasks.interval${cap(task.schedule.interval)}`) : "—"}
              />
            </div>
            <PendingApprovalsCard
              taskId={task.id}
              onOpenAll={() => writeParams((sp) => sp.set("tab", "approvals"))}
            />
            <Card>
              <CardHeader>
                <CardTitle>{t("taskDetail.pulseFindings")}</CardTitle>
              </CardHeader>
              <CardContent className="p-0">
                {findings && findings.length > 0 ? (
                  <ul>
                    {[...findings]
                      .sort(
                        (a, b) =>
                          severityRank(b.severity) - severityRank(a.severity),
                      )
                      .slice(0, 3)
                      .map((f) => (
                        <li
                          key={`${f.scan_id}-${f.id}`}
                          className="flex items-center gap-2 border-b border-border px-3 py-2 last:border-0"
                        >
                          <SeverityBadge severity={f.severity} />
                          <span className="min-w-0 flex-1 truncate text-xs">{f.title}</span>
                        </li>
                      ))}
                  </ul>
                ) : (
                  <p className="p-3 text-xs text-muted-foreground">
                    {t("taskDetail.noFindingsTitle")}
                  </p>
                )}
              </CardContent>
            </Card>
            <TaskObjectivePanel taskId={taskId} />
            <Card>
              <CardHeader>
                <CardTitle>{t("taskDetail.policyTitle")}</CardTitle>
              </CardHeader>
              <CardContent className="space-y-1.5 text-sm">
                <PolicyRow label={t("tasks.fieldScanMode")} value={task.phase_policy?.scan_mode ?? "single"} />
                <PolicyRow label={t("tasks.fieldIntensity")} value={task.phase_policy?.intensity ?? "active"} />
                <PolicyRow
                  label={t("tasks.fieldSchedule")}
                  value={
                    task.schedule
                      ? `${t(`tasks.interval${cap(task.schedule.interval)}`)} ${task.schedule.time ?? ""} ${task.schedule.timezone ?? ""}`
                      : t("tasks.noSchedule")
                  }
                />
                <PolicyRow label={t("taskDetail.created")} value={timeAgo(task.created_at)} />
              </CardContent>
            </Card>
          </TabsContent>

          {/* 发现：任务级 findings 全宽列表。 */}
          <TabsContent value="findings" className="mt-0">
            {findings && findings.length > 0 ? (
              <Card>
                <CardContent className="p-0">
                  <ul>
                    {findings.map((f: FlatFinding) => (
                      <li
                        key={`${f.scan_id}-${f.id}`}
                        className="flex items-center gap-3 border-b border-border px-4 py-3 last:border-0"
                      >
                        <SeverityBadge severity={f.severity} />
                        <div className="min-w-0 flex-1">
                          <div className="truncate text-sm font-medium">{f.title}</div>
                          <div className="truncate text-xs text-muted-foreground mono">
                            {f.endpoint || f.target}
                          </div>
                        </div>
                        <Link
                          to={`/scans/${f.scan_id}`}
                          className="shrink-0 text-xs text-muted-foreground hover:text-foreground"
                        >
                          {t("taskDetail.viewRun")}
                        </Link>
                      </li>
                    ))}
                  </ul>
                </CardContent>
              </Card>
            ) : (
              <EmptyState
                title={t("taskDetail.noFindingsTitle")}
                description={t("taskDetail.noFindingsDescription")}
              />
            )}
          </TabsContent>

          {/* 情报：凭据/线索/敏感数据 + 分诊。 */}
          <TabsContent value="intel" className="mt-0">
            <IntelTab taskId={task.id} />
          </TabsContent>

          {/* 审批：本任务待决队列 + 已裁决历史。 */}
          <TabsContent value="approvals" className="mt-0">
            <TaskApprovalsTab taskId={task.id} />
          </TabsContent>

          {/* 账本：选中 run 的 hypothesis/evidence 快照。 */}
          <TabsContent value="ledger" className="mt-0">
            <LedgerTab scanId={scopedInstanceId} running={scopedRunning} />
          </TabsContent>

          {/* 文件：选中 run 的扫描目录只读浏览器（与 Run 详情页同一组件）。 */}
          <TabsContent value="files" className="mt-0">
            {scopedInstanceId ? (
              <ScanFilesBrowser scanId={scopedInstanceId} running={scopedRunning} />
            ) : (
              <EmptyState
                title={t("ledger.noRunTitle")}
                description={t("ledger.noRunDescription")}
              />
            )}
          </TabsContent>

          {/* 运行：run 列表；点击切换到会话 tab 并选中该 run；行内可展开流量代理 raw 交互。 */}
          <TabsContent value="runs" className="mt-0">
            {runs.length > 0 ? (
              <Card>
                <CardContent className="p-0">
                  <ul>
                    {runs.map((r) => {
                      const trafficOpen = trafficRuns.has(r.id);
                      return (
                        <li key={r.id} className="border-b border-border last:border-0">
                          <div className="flex flex-wrap items-center gap-3 px-4 py-3">
                            {r.instance_id && (
                              <button
                                type="button"
                                onClick={() => toggleRunTraffic(r.id)}
                                aria-expanded={trafficOpen}
                                aria-label={t("runs.traffic.aria")}
                                className="flex size-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:bg-muted/40 hover:text-foreground"
                              >
                                <ChevronRight
                                  className={cn(
                                    "h-3.5 w-3.5 transition-transform",
                                    trafficOpen && "rotate-90",
                                  )}
                                  aria-hidden
                                />
                              </button>
                            )}
                            {r.instance_id ? (
                              <button
                                type="button"
                                onClick={() => openRunInSessions(r.instance_id!)}
                                className="mono text-sm text-foreground hover:text-primary"
                              >
                                {r.id}
                              </button>
                            ) : (
                              <span className="mono text-sm">{r.id}</span>
                            )}
                            <ScanStatusPill status={r.status} />
                            {r.trigger && (
                              <span className="text-[11px] uppercase tracking-wider text-muted-foreground mono">
                                {r.trigger}
                              </span>
                            )}
                            <span className="text-xs text-muted-foreground">
                              {r.started_at ? timeAgo(r.started_at) : "—"}
                            </span>
                            <span className="ml-auto flex items-center gap-3 text-xs text-muted-foreground mono tabular-nums">
                              <span>
                                {t("taskDetail.vulns")} {r.vuln_count}
                              </span>
                              <span>
                                {t("taskDetail.tokens")} {r.total_tokens.toLocaleString()}
                              </span>
                              {r.instance_id && (
                                <Link
                                  to={`/scans/${r.instance_id}`}
                                  className="text-xs text-muted-foreground hover:text-foreground"
                                >
                                  {t("taskDetail.viewRun")}
                                </Link>
                              )}
                            </span>
                          </div>
                          {trafficOpen && r.instance_id && (
                            <RunTraffic scanId={r.instance_id} />
                          )}
                        </li>
                      );
                    })}
                  </ul>
                </CardContent>
              </Card>
            ) : (
              <EmptyState
                title={t("taskDetail.noRunsTitle")}
                description={t("taskDetail.noRunsDescription")}
                action={<TaskRunButton task={task} runs={runs} />}
              />
            )}
          </TabsContent>

          {/* 报告：终态 run 的 PDF。 */}
          <TabsContent value="reports" className="mt-0">
            <TaskReportsTab runs={runs} />
          </TabsContent>

          {/* 消耗（REDESIGN §7.2 处方）：选中 run 的 token 用量图表。 */}
          <TabsContent value="usage" className="mt-0">
            {scopedInstanceId ? (
              <TokenUsageTab scanId={scopedInstanceId} running={scopedRunning} />
            ) : (
              <EmptyState
                title={t("ledger.noRunTitle")}
                description={t("ledger.noRunDescription")}
              />
            )}
          </TabsContent>
        </div>
      </Tabs>
    </>
  );
}

function BackLink() {
  const { t } = useI18n();
  return (
    <Link
      to="/tasks"
      className="inline-flex items-center gap-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
    >
      <ArrowLeft className="h-3 w-3" aria-hidden />
      {t("taskDetail.back")}
    </Link>
  );
}

function StatCard({ label, value }: { label: string; value: string }) {
  return (
    <Card>
      <CardContent className="p-4">
        <div className="text-[10px] uppercase tracking-wider text-muted-foreground">{label}</div>
        <div className="mt-1 text-xl font-semibold tabular-nums">{value}</div>
      </CardContent>
    </Card>
  );
}

function PolicyRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex gap-3">
      <span className="w-24 shrink-0 text-xs text-muted-foreground">{label}</span>
      <span className="mono text-sm text-foreground">{value}</span>
    </div>
  );
}

function cap(s: string) {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** 本任务待审批（最多 3 张，行内决策 + 跳审批 tab / 全局队列）。 */
function PendingApprovalsCard({
  taskId,
  onOpenAll,
}: {
  taskId: string;
  onOpenAll: () => void;
}) {
  const { t } = useI18n();
  // 后端支持 task_id 过滤——不再全量拉取后客户端过滤。
  const { data } = useApprovals({ status: "pending", taskId });
  const mine = (data?.approvals ?? []).slice(0, 3);
  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle>{t("taskDetail.pulseApprovals")}</CardTitle>
        <button
          type="button"
          onClick={onOpenAll}
          className="text-xs text-muted-foreground hover:text-foreground"
        >
          {t("approval.openQueue")}
        </button>
      </CardHeader>
      <CardContent className="space-y-2 p-3 pt-0">
        {mine.length > 0 ? (
          mine.map((a) => <ApprovalInlineBar key={a.id} approval={a} />)
        ) : (
          <p className="text-xs text-muted-foreground">
            {t("approvals.emptyPendingTitle")}
          </p>
        )}
      </CardContent>
    </Card>
  );
}

/** 审批 tab：待决队列（行内决策）+ 已裁决历史。 */
function TaskApprovalsTab({ taskId }: { taskId: string }) {
  const { t } = useI18n();
  const pending = useApprovals({ status: "pending", taskId });
  const decided = useApprovals({ status: "decided", taskId });
  const pendingItems = pending.data?.approvals ?? [];
  const decidedItems = decided.data?.approvals ?? [];

  return (
    <div className="space-y-4">
      <section className="space-y-2">
        <h2 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {t("taskDetail.approvalsPending")}
        </h2>
        {pendingItems.length > 0 ? (
          <div className="space-y-2">
            {pendingItems.map((a) => (
              <ApprovalInlineBar key={a.id} approval={a} />
            ))}
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">
            {t("approvals.emptyPendingTitle")}
          </p>
        )}
      </section>
      <section className="space-y-2">
        <h2 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {t("taskDetail.approvalsHistory")}
        </h2>
        {decidedItems.length > 0 ? (
          <Card>
            <CardContent className="p-0">
              <ul>
                {decidedItems.map((a) => (
                  <ApprovalHistoryRow key={a.id} approval={a} />
                ))}
              </ul>
            </CardContent>
          </Card>
        ) : (
          <p className="text-xs text-muted-foreground">
            {t("taskDetail.approvalsHistoryEmpty")}
          </p>
        )}
      </section>
    </div>
  );
}

const TERMINAL_RUN_STATUS = ["finished", "completed", "stopped", "failed", "saved"];

function approvalSummary(a: Approval): string {
  if (a.kind === "command") return a.payload.command ?? "";
  const method = a.payload.method ?? "";
  const url = a.payload.url ?? "";
  return `${method} ${url}`.trim();
}

function ApprovalHistoryRow({ approval: a }: { approval: Approval }) {
  const { t } = useI18n();
  const variant =
    a.status === "approved"
      ? "success"
      : a.status === "rejected"
        ? "destructive"
        : "muted";
  return (
    <li className="flex flex-wrap items-center gap-3 border-b border-border px-4 py-2.5 last:border-0">
      <Badge variant={variant} className="shrink-0">
        {t(`approval.status.${a.status}`)}
      </Badge>
      <span className="shrink-0 font-mono text-[10px] uppercase tracking-wider text-muted-foreground">
        {a.kind}
      </span>
      <span className="min-w-0 flex-1 truncate font-mono text-xs">
        {approvalSummary(a)}
      </span>
      {a.reason && (
        <span className="hidden max-w-48 truncate text-xs text-muted-foreground sm:inline">
          {a.reason}
        </span>
      )}
      <span className="shrink-0 text-xs text-muted-foreground">
        {a.decided_at ? timeAgo(a.decided_at) : "—"}
      </span>
    </li>
  );
}

/** 报告 tab：终态 run 的 PDF（按需由 /api/report/{id} 生成）。 */
function TaskReportsTab({ runs }: { runs: Run[] }) {
  const { t } = useI18n();
  const withReport = runs.filter(
    (r) => r.instance_id && TERMINAL_RUN_STATUS.includes(r.status),
  );
  if (runs.length === 0) {
    return (
      <EmptyState
        title={t("taskDetail.reportsEmptyTitle")}
        description={t("taskDetail.noRunsDescription")}
      />
    );
  }
  if (withReport.length === 0) {
    return (
      <EmptyState
        title={t("taskDetail.reportsEmptyTitle")}
        description={t("taskDetail.reportsPendingDescription")}
      />
    );
  }
  return (
    <Card>
      <CardContent className="p-0">
        <ul>
          {withReport.map((r) => (
            <li
              key={r.id}
              className="flex flex-wrap items-center gap-3 border-b border-border px-4 py-3 last:border-0"
            >
              <FileText className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
              <span className="mono text-sm">{r.id}</span>
              <ScanStatusPill status={r.status} />
              <span className="text-xs text-muted-foreground">
                {r.started_at ? timeAgo(r.started_at) : "—"}
              </span>
              <a
                href={api.reportUrl(r.instance_id!)}
                target="_blank"
                rel="noreferrer"
                className="ml-auto flex items-center gap-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
              >
                {t("taskDetail.reportsOpen")}
              </a>
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  );
}
