import { useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { toast } from "sonner";
import { ArrowLeft, Play, Radio } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { EmptyState, ErrorState } from "@/components/states";
import { SeverityBadge } from "@/components/severity-badge";
import { ScanStatusPill } from "@/components/scan-status-pill";
import { RunConsole } from "@/components/run-console";
import { GuidanceTab } from "@/components/guidance-tab";
import { ApprovalInlineBar } from "@/components/approval-inline-bar";
import { useApprovals } from "@/api/queries";
import { severityRank } from "@/lib/utils";
import { useI18n } from "@/i18n";
import {
  useInstanceEvents,
  useStartTaskRun,
  useTask,
  useTaskFindings,
  useTaskRuns,
} from "@/api/queries";
import { timeAgo } from "@/lib/utils";
import type { FlatFinding } from "@/lib/findings";

/**
 * Task detail — the operator's mission control for one task.
 *
 * 双栏（4zreco sessions-tab 模式）：左栏常驻实时事件流（最新 Run 的
 * transcript + 行内审批卡），右栏收纳上下文（概览/发现/运行/介入）。
 * 操作员一屏同时看到"跑到哪了"与"一切可干预"，事件流不再藏在子页签。
 * 小屏（<lg）纵向堆叠：事件流在上、上下文在下。R5 的证据全景接入右栏。
 */
export default function TaskDetailPage() {
  const { taskId } = useParams<{ taskId: string }>();
  const { t } = useI18n();
  const { data: task, isLoading, error, refetch } = useTask(taskId);
  const { data: runsData } = useTaskRuns(taskId);
  const { data: findings } = useTaskFindings(taskId);
  const startRun = useStartTaskRun();

  const runs = runsData?.items ?? [];
  const latestRun = useMemo(
    () =>
      runs.find((r) => r.status === "running") ??
      runs.find((r) => r.instance_id) ??
      runs[0] ??
      null,
    [runs],
  );
  // 会话控制台的选中 Run：默认最新活跃，可手动切换历史 Run。
  const [selectedInstanceId, setSelectedInstanceId] = useState<
    string | undefined
  >(latestRun?.instance_id);
  const effectiveInstanceId = selectedInstanceId ?? latestRun?.instance_id;

  // 首次数据到达 / 最新活跃 Run 变化时跟随（用户未手动选择前）。
  useEffect(() => {
    if (!selectedInstanceId && latestRun?.instance_id) {
      setSelectedInstanceId(latestRun.instance_id);
    }
  }, [latestRun?.instance_id, selectedInstanceId]);

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

  return (
    <div className="space-y-6">
      <BackLink />
      <header className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0">
          <h1 className="font-sans text-2xl font-semibold tracking-tight">{task.name}</h1>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">{task.goal}</p>
          {task.instruction && (
            <p className="mt-1 text-xs text-muted-foreground">{task.instruction}</p>
          )}
        </div>
        <Button
          className="shrink-0"
          disabled={startRun.isPending}
          onClick={() =>
            startRun.mutate(
              { id: task.id },
              {
                onSuccess: () => toast.success(t("toast.runStarted")),
                onError: (err: Error) =>
                  toast.error(`${t("toast.runFailed")}: ${err.message}`),
              },
            )
          }
        >
          <Play className="h-3.5 w-3.5" />
          {t("tasks.run")}
        </Button>
      </header>

      {/* 4zreco sessions-tab 模式：大屏整页视口定高、面板内部滚动，
          页面本身不滚。 */}
      <div className="grid items-start gap-4 lg:h-[calc(100vh-16.5rem)] lg:grid-cols-[minmax(0,1fr)_400px] lg:grid-rows-[minmax(0,1fr)]">
        {/* 左栏：会话控制台（Run 切换 + 控制 + transcript + 双模式 composer）。 */}
        <div className="h-[560px] lg:h-full lg:min-h-0">
          <RunConsole
            taskId={task.id}
            runs={runs}
            selectedInstanceId={effectiveInstanceId}
            onSelectRun={setSelectedInstanceId}
          />
        </div>

        {/* 右栏：任务上下文（概览/发现/运行/介入），大屏面板内滚动。 */}
        <Tabs
          defaultValue="pulse"
          className="min-h-0 lg:h-full lg:overflow-y-auto lg:pr-1"
        >
          <TabsList>
            <TabsTrigger value="pulse">{t("taskDetail.tabPulse")}</TabsTrigger>
            <TabsTrigger value="overview">{t("taskDetail.tabOverview")}</TabsTrigger>
            <TabsTrigger value="findings">
              {t("taskDetail.tabFindings")} ({task.finding_count ?? findings?.length ?? 0})
            </TabsTrigger>
            <TabsTrigger value="runs">
              {t("taskDetail.tabRuns")} ({task.run_count ?? runs.length})
            </TabsTrigger>
            <TabsTrigger value="guidance">{t("taskDetail.tabGuidance")}</TabsTrigger>
          </TabsList>

          <TabsContent value="pulse" className="space-y-3 pt-4">
            <PendingApprovalsCard taskId={task.id} />
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
            <Card>
              <CardHeader>
                <CardTitle>{t("taskDetail.gapsTitle")}</CardTitle>
              </CardHeader>
              <CardContent>
                <p className="text-xs text-muted-foreground">
                  {t("taskDetail.gapsPlaceholder")}
                </p>
              </CardContent>
            </Card>
          </TabsContent>

          <TabsContent value="overview" className="space-y-4 pt-4">
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
          <Card>
            <CardHeader>
              <CardTitle>{t("taskDetail.gapsTitle")}</CardTitle>
            </CardHeader>
            <CardContent>
              <p className="text-xs text-muted-foreground">{t("taskDetail.gapsPlaceholder")}</p>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="findings" className="pt-4">
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

        <TabsContent value="runs" className="pt-4">
          {runs.length > 0 ? (
            <Card>
              <CardContent className="p-0">
                <ul>
                  {runs.map((r) => (
                    <li
                      key={r.id}
                      className="flex flex-wrap items-center gap-3 border-b border-border px-4 py-3 last:border-0"
                    >
                      {r.instance_id ? (
                        <Link
                          to={`/scans/${r.instance_id}`}
                          className="mono text-sm text-foreground hover:text-primary"
                        >
                          {r.id}
                        </Link>
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
                      </span>
                    </li>
                  ))}
                </ul>
              </CardContent>
            </Card>
          ) : (
            <EmptyState
              title={t("taskDetail.noRunsTitle")}
              description={t("taskDetail.noRunsDescription")}
              action={
                <Button onClick={() => startRun.mutate({ id: task.id })}>
                  <Play className="mr-1 h-4 w-4" />
                  {t("tasks.run")}
                </Button>
              }
            />
          )}
        </TabsContent>

          <TabsContent value="guidance">
            <GuidanceTab taskId={task.id} />
          </TabsContent>
        </Tabs>
      </div>
    </div>
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

/** 本任务待审批（最多 3 张，行内决策 + 深链到队列）。 */
function PendingApprovalsCard({ taskId }: { taskId: string }) {
  const { t } = useI18n();
  const { data } = useApprovals({ status: "pending" });
  const mine = (data?.approvals ?? [])
    .filter((a) => a.task_id === taskId)
    .slice(0, 3);
  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle>{t("taskDetail.pulseApprovals")}</CardTitle>
        <Link
          to="/approvals"
          className="text-xs text-muted-foreground hover:text-foreground"
        >
          {t("approval.openQueue")}
        </Link>
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
