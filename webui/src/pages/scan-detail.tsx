import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { toast } from "sonner";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  CardDescription,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { useI18n } from "@/i18n";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Separator } from "@/components/ui/separator";
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
import { ScanStatusPill } from "@/components/scan-status-pill";
import { StopReasonBadge } from "@/components/stop-reason-badge";
import { RunObjectiveCard } from "@/components/run-objective-card";
import { LedgerTab } from "@/components/ledger-tab";
import { SecurityTab } from "@/components/security-tab";
import { SeverityBadge } from "@/components/severity-badge";
import { Markdown } from "@/components/markdown";
import { VerificationBadge } from "@/components/verification-badge";
import {
  PhaseProgress,
  PHASES,
  phaseVisual,
  phaseStatusLabel,
} from "@/components/phase-progress";
import { CopyButton } from "@/components/copy-button";
import { TokenUsageTab } from "@/components/token-usage";
import { ScanFilesBrowser } from "@/components/scan-files-browser";
import { ErrorState, EmptyState } from "@/components/states";
import {
  useScan,
  useStopInstance,
  usePauseInstance,
  useResumeInstance,
  useStartSavedInstance,
  useStartTaskRun,
  useTask,
  useDeleteScan,
  useDeleteVuln,
  useSendTaskGuidance,
  useScanTraffic,
} from "@/api/queries";
import { api } from "@/api/client";
import { MAX_GUIDANCE_CHARS, newRequestId } from "@/lib/guidance";
import {
  filterEventsForInstance,
  mergeFeedEvents,
  toFeedEvent,
  useEventsStore,
  type FeedEvent,
} from "@/store/events";
import {
  timeAgo,
  formatTime,
  formatDuration,
  severityRank,
  normalizeSeverity,
  cn,
  menuContentClass,
  menuItemClass,
} from "@/lib/utils";
import {
  ChevronLeft,
  Download,
  ExternalLink,
  MoreHorizontal,
  X,
  Play,
  Pause,
  Trash2,
  ShieldAlert,
  Terminal,
  Sparkles,
  ListChecks,
  ArrowRight,
  Loader2,
  Send,
  Activity,
  FolderOpen,
  BookOpen,
} from "lucide-react";
import { LiveFeed, type FeedFilter } from "@/components/live-feed";
import { Pagination, DEFAULT_PAGE_SIZE } from "@/components/Pagination";
import type { SubScanSummary, VulnSummary, WSEvent } from "@/types/api";

export default function ScanDetailPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const { scanId } = useParams<{ scanId: string }>();
  const id = scanId ?? "";
  const { data: scan, isLoading, isFetching, error, refetch } = useScan(id);
  const stop = useStopInstance();
  const pause = usePauseInstance();
  const resume = useResumeInstance();
  const start = useStartSavedInstance();
  const rerun = useStartTaskRun();
  // D11 run/task mount linkage: mounted runs resolve their task (name for the
  // header, re-dispatch target). useTask is enabled-guarded on the id.
  const { data: task } = useTask(scan?.task_id);
  const del = useDeleteScan();
  const subscribe = useEventsStore((s) => s.subscribe);
  const unsubscribe = useEventsStore((s) => s.unsubscribe);
  const liveEvents = useEventsStore((s) => s.events);
  const subscriptionId = scan?.instance_id || scan?.id || id;

  // Lazy-paged older events. The detail response only carries a tail of the
  // event log (scan.events_truncated); these are the earlier events fetched on
  // demand via api.scanEvents, held in ascending order starting at
  // `earliestLoaded`.
  const [olderEvents, setOlderEvents] = useState<WSEvent[]>([]);
  const [earliestLoaded, setEarliestLoaded] = useState<number | null>(null);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  useEffect(() => {
    if (!subscriptionId) return;
    subscribe(subscriptionId);
    return () => unsubscribe();
  }, [subscriptionId, subscribe, unsubscribe]);

  // Reset paged events whenever we navigate to a different scan.
  useEffect(() => {
    setOlderEvents([]);
    setEarliestLoaded(null);
    setLoadingOlder(false);
  }, [id]);

  const loadEarlierEvents = useCallback(async () => {
    if (!scan) return;
    const inlineCount = (scan.events ?? []).length;
    const total = scan.events_total ?? inlineCount;
    const tailStart = Math.max(0, total - inlineCount);
    const base = earliestLoaded ?? tailStart;
    if (base <= 0 || loadingOlder) return;
    const pageSize = 300;
    const offset = Math.max(0, base - pageSize);
    const limit = base - offset;
    setLoadingOlder(true);
    try {
      const page = await api.scanEvents(scan.id, offset, limit);
      setOlderEvents((prev) => [...(page.events ?? []), ...prev]);
      setEarliestLoaded(offset);
    } catch {
      /* surfaced via the disabled state resetting; user can retry */
    } finally {
      setLoadingOlder(false);
    }
  }, [scan, earliestLoaded, loadingOlder]);

  if (error)
    return (
      <ErrorState
        title={t("scanDetail.loadFailed")}
        description={error instanceof Error ? error.message : t("common.unknownError")}
        action={
          <Button size="sm" variant="outline" onClick={() => refetch()}>
            {t("common.retry")}
          </Button>
        }
      />
    );
  if (isLoading) return <ScanDetailSkeleton />;
  if (!scan)
    return (
      <ErrorState
        title={t("scanDetail.startingTitle")}
        description={
          isFetching
            ? t("scanDetail.startingFetching")
            : t("scanDetail.startingStale")
        }
        action={
          <Button size="sm" variant="outline" onClick={() => refetch()}>
            {t("common.retry")}
          </Button>
        }
      />
    );

  const status = (scan.status || "").toLowerCase();
  const canPause = status === "running";
  const canResume = status === "paused";
  const canStop =
    status === "running" || status === "pending" || status === "paused";
  const canStart =
    status === "saved" ||
    status === "stopped" ||
    status === "failed" ||
    status === "finished";

  // Combine persisted events from the scan record with the live websocket
  // feed for this instance, deduped by content. `olderEvents` are the
  // lazily-paged earlier events; concatenated with the inline tail they form a
  // contiguous run whose first global index is `effectiveEarliest`.
  const eventInstanceId = scan.instance_id || scan.id || id;
  const wsForScan = filterEventsForInstance(liveEvents, eventInstanceId);
  const inlineEvents = scan.events ?? [];
  const eventsTotal = scan.events_total ?? inlineEvents.length;
  const tailStart = Math.max(0, eventsTotal - inlineEvents.length);
  const effectiveEarliest = earliestLoaded ?? tailStart;
  const persistedCombined = [...olderEvents, ...inlineEvents];
  const persistedAsFeed: FeedEvent[] = persistedCombined.map((e, i) =>
    toFeedEvent(e, `scan:${eventInstanceId}`, effectiveEarliest + i),
  );
  const mergedEvents = mergeFeedEvents(persistedAsFeed, wsForScan);

  // Older-event paging is only offered for terminal scans: a live scan's tail
  // updates every poll and streams via WS, so mixing in backward paging would
  // race. `remainingOlder` drives the "Load earlier" affordance.
  const isLiveScan =
    status === "running" || status === "pending" || status === "paused";
  const canLoadEarlier =
    !isLiveScan && !!scan.events_truncated && effectiveEarliest > 0;
  const remainingOlder = effectiveEarliest;

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <Link
          to="/scans"
          className="inline-flex items-center hover:text-foreground"
        >
          <ChevronLeft className="mr-1 h-3 w-3" />
          {t("scans.title")}
        </Link>
        {scan.task_id && (
          <>
            <span aria-hidden>/</span>
            <Link
              to={`/tasks/${scan.task_id}`}
              className="inline-flex items-center hover:text-foreground"
            >
              {task?.name || t("nav.tasks")}
            </Link>
          </>
        )}
      </div>

      <header className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
        <div className="space-y-2 min-w-0 flex-1">
          <div className="flex items-center gap-3 min-w-0">
            {/* Run 详情以任务命名（D11 挂载语义）；未挂载的遗留扫描
                退回 target 主标题——诚实降级，不编造归属。 */}
            <h1
              className={cn(
                "text-xl sm:text-2xl font-semibold tracking-tight text-foreground truncate min-w-0",
                !task?.name && "font-mono",
              )}
            >
              {task?.name || scan.target}
            </h1>
            <CopyButton value={scan.target} />
          </div>
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
            {task?.name && (
              <>
                <span className="mono truncate max-w-full">{scan.target}</span>
                <span className="hidden sm:inline">·</span>
              </>
            )}
            <span className="mono truncate max-w-full">{scan.id}</span>
            <span className="hidden sm:inline">·</span>
            <span>{t("instances.startedPrefix")}{timeAgo(scan.started_at)}</span>
            <span className="hidden sm:inline">·</span>
            <span>
              {t("scanDetail.durationPrefix")}{formatDuration(scan.started_at, scan.finished_at)}
            </span>
            {scan.scan_mode && (
              <>
                <span className="hidden sm:inline">·</span>
                <Badge variant="outline" className="font-normal capitalize">
                  {scan.scan_mode}
                </Badge>
              </>
            )}
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <ScanStatusPill status={scan.status} />
          {/* 终态原因常驻头部（原埋在配置 tab）：goal_achieved=emerald、
              义务未清=amber、自由文本原样 mono——词表照引擎枚举，不造新状态。 */}
          <StopReasonBadge reason={scan.stop_reason} />
          <Button variant="outline" size="sm" asChild>
            <a href={api.reportUrl(scan.id)} target="_blank" rel="noreferrer">
              <Download className="mr-1 h-4 w-4" /> {t("nav.reports")}
            </a>
          </Button>
          {canStart && scan.task_id && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                // D11: a mounted run re-dispatches through the unified
                // task-run entry — same instance, notes/ledger/checkpoint
                // stay continuous. The legacy instance start below remains
                // only for unmounted saved scans.
                const tid = scan.task_id;
                if (!tid) return;
                rerun.mutate(
                  { id: tid },
                  {
                    onSuccess: (res) => {
                      if (res.instance_id) {
                        navigate(`/scans/${res.instance_id}`);
                      } else {
                        void refetch();
                      }
                    },
                  },
                );
              }}
              disabled={rerun.isPending}
            >
              <Play className="mr-1 h-4 w-4" /> {t("scanDetail.action.rerun")}
            </Button>
          )}
          {canStart && !scan.task_id && (
            <Button
              variant="outline"
              size="sm"
              onClick={() =>
                // Instance-action endpoints key off the instance id, which for
                // wildcard scans differs from the scan record id (scan.id).
                start.mutate(scan.instance_id || scan.id, {
                  onSuccess: (res) => {
                    if (res.instance_id) {
                      navigate(`/scans/${res.instance_id}`);
                    } else {
                      void refetch();
                    }
                  },
                })
              }
              disabled={start.isPending}
            >
              <Play className="mr-1 h-4 w-4" /> {t("instances.action.start")}
            </Button>
          )}
          {canPause && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => pause.mutate(scan.instance_id || scan.id)}
              disabled={pause.isPending}
            >
              <Pause className="mr-1 h-4 w-4" /> {t("console.pause")}
            </Button>
          )}
          {canResume && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => resume.mutate(scan.instance_id || scan.id)}
              disabled={resume.isPending}
            >
              <Play className="mr-1 h-4 w-4" /> {t("console.resume")}
            </Button>
          )}
          {canStop && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => stop.mutate({ id: scan.instance_id || scan.id })}
              disabled={stop.isPending}
            >
              <X className="mr-1 h-4 w-4" /> {t("console.stop")}
            </Button>
          )}
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setConfirmDelete(true)}
            disabled={del.isPending}
          >
            <Trash2 className="mr-1 h-4 w-4" /> {t("common.delete")}
          </Button>
        </div>
      </header>

      <div className="grid gap-4 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader>
            <CardTitle className="text-sm">{t("scanDetail.phaseProgress")}</CardTitle>
            <CardDescription>
              {t("scanDetail.methodologyBefore")} {PHASES.length}{" "}
              {t("scanDetail.methodologyAfter")}{" "}
              <span className="text-foreground">
                {currentPhaseLabel(scan.current_phase, t("overview.ops.phaseWord"))}
              </span>
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <PhaseProgress
              current={scan.current_phase}
              selected={scan.phases}
              status={scan.status}
              phaseStatus={scan.phase_status}
              worked={scan.phases_worked}
            />
            {/* Responsive phase-tile grid. Cells use a min-width
                template so 22 short tiles flow into rows that fit the
                viewport instead of cramming 5 wide cells into 800 px
                of card space. */}
            <div className="grid grid-cols-[repeat(auto-fill,minmax(140px,1fr))] gap-2">
              {PHASES.map((p) => {
                const visual = phaseVisual(p.id, {
                  current: scan.current_phase,
                  isRunning: scan.status === "running",
                  phaseStatus: scan.phase_status,
                  worked: scan.phases_worked,
                });
                return (
                  <div
                    key={p.id}
                    title={`${p.id}. ${p.name} — ${phaseStatusLabel(visual.status, t)}`}
                    className={cn(
                      "flex items-baseline gap-1.5 rounded-md border border-border bg-muted/20 px-2 py-1.5 text-[11px]",
                      visual.status === "active" &&
                        "border-amber-400/50 text-amber-700 dark:text-amber-300",
                      visual.status === "completed" &&
                        "border-emerald-500/40 text-emerald-700 dark:text-emerald-300",
                      visual.status === "blocked" &&
                        "border-rose-500/40 text-rose-700 dark:text-rose-300",
                      visual.status === "not_applicable" &&
                        "border-sky-500/30 text-sky-700/80 dark:text-sky-300/70",
                    )}
                  >
                    <span className="mono shrink-0 text-muted-foreground">
                      {p.id}
                    </span>
                    <span className="truncate" title={p.name}>
                      {p.name}
                    </span>
                  </div>
                );
              })}
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-sm">{t("scanDetail.riskOverview")}</CardTitle>
            <CardDescription>
              {(scan.vulns ?? []).length} {t("common.findings")}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <RiskBreakdown vulns={scan.vulns ?? []} />
          </CardContent>
        </Card>

        {!!scan.sub_scan_total && (
          <Card className="lg:col-span-3">
            <CardHeader>
              <CardTitle className="text-sm">{t("scanDetail.wildcardCoverage")}</CardTitle>
              <CardDescription>
                {scan.sub_scan_completed ?? 0} {t("scans.subScanned")} ·{" "}
                {scan.sub_scan_running ?? 0} {t("scans.subRunning")} ·{" "}
                {scan.sub_scan_remaining ?? 0} {t("scans.subRemaining")}
              </CardDescription>
            </CardHeader>
            <CardContent>
              <SubdomainProgress
                completed={scan.sub_scan_completed ?? 0}
                running={scan.sub_scan_running ?? 0}
                remaining={scan.sub_scan_remaining ?? 0}
                total={scan.sub_scan_total ?? 0}
              />
            </CardContent>
          </Card>
        )}
      </div>

      {/* 本 run 的目标判定投影（判定环）：只渲染 runs[] 里属于自己的行，
          任务级 ring/缺口/VOI 池仍在任务详情概览，不在此复刻。 */}
      <RunObjectiveCard
        taskId={scan.task_id}
        runId={scan.run_id}
        scanId={scan.id}
      />

      <Tabs defaultValue="findings">
        <TabsList>
          <TabsTrigger value="findings">
            <ShieldAlert className="mr-1.5 h-3.5 w-3.5" />
            {t("scanDetail.tab.findings")}
          </TabsTrigger>
          <TabsTrigger value="events">
            <Terminal className="mr-1.5 h-3.5 w-3.5" />
            {t("scanDetail.tab.events")}
          </TabsTrigger>
          <TabsTrigger value="ledger">
            <BookOpen className="mr-1.5 h-3.5 w-3.5" />
            {t("scanDetail.tab.ledger")}
          </TabsTrigger>
          <TabsTrigger value="security">
            <ShieldAlert className="mr-1.5 h-3.5 w-3.5" />
            {t("scanDetail.tab.security")}
          </TabsTrigger>
          <TabsTrigger value="tokens">
            <Activity className="mr-1.5 h-3.5 w-3.5" />
            {t("scanDetail.tab.tokens")}
          </TabsTrigger>
          {!!scan.sub_scan_total && (
            <TabsTrigger value="subdomains">
              <ListChecks className="mr-1.5 h-3.5 w-3.5" />
              {t("scanDetail.tab.subdomains")}
            </TabsTrigger>
          )}
          <TabsTrigger value="config">
            <ListChecks className="mr-1.5 h-3.5 w-3.5" />
            {t("scanDetail.tab.config")}
          </TabsTrigger>
          <TabsTrigger value="files">
            <FolderOpen className="mr-1.5 h-3.5 w-3.5" />
            {t("scanDetail.tab.files")}
          </TabsTrigger>
        </TabsList>

        <TabsContent value="findings" className="space-y-2">
          <FindingsTab vulns={scan.vulns ?? []} scanId={scan.id} />
        </TabsContent>
        <TabsContent value="events">
          <EventsTab
            events={mergedEvents}
            scanId={scan.id}
            instanceId={eventInstanceId}
            taskId={scan.task_id}
            status={status}
            target={scan.target}
            canLoadEarlier={canLoadEarlier}
            remainingOlder={remainingOlder}
            loadingOlder={loadingOlder}
            onLoadEarlier={loadEarlierEvents}
          />
        </TabsContent>
        <TabsContent value="ledger">
          {/* 黑板双层（状态 ledger + 历史 audit）随 Run 走：per-run 数据归
              Run 详情；id 用实例 id（与任务详情账本 tab 同一约定）。 */}
          <LedgerTab scanId={eventInstanceId} running={isLiveScan} />
        </TabsContent>
          <TabsContent value="security">
            <SecurityTab scanId={eventInstanceId} running={isLiveScan} />
          </TabsContent>
        <TabsContent value="tokens">
          <TokenUsageTab scanId={scan.id} running={status === "running" || status === "paused"} />
        </TabsContent>
        {!!scan.sub_scan_total && (
          <TabsContent value="subdomains">
            <SubdomainsTab subScans={scan.sub_scans ?? []} />
          </TabsContent>
        )}
        <TabsContent value="config">
          <ConfigTab scan={scan} />
        </TabsContent>
        <TabsContent value="files">
          <ScanFilesBrowser scanId={scan.id} running={status === "running"} />
        </TabsContent>
      </Tabs>

      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("scanDetail.deleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("scanDetail.confirmDelete")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                del.mutate(scan.id, {
                  onSuccess: () => {
                    navigate("/scans", { replace: true });
                  },
                });
                setConfirmDelete(false);
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

function currentPhaseLabel(p?: number, phaseWord = "Phase "): string {
  if (!p) return "—";
  const found = PHASES.find((x) => x.id === p);
  return found ? `${p}. ${found.name}` : `${phaseWord}${p}`;
}

function SubdomainProgress({
  completed,
  running,
  remaining,
  total,
}: {
  completed: number;
  running: number;
  remaining: number;
  total: number;
}) {
  const { t } = useI18n();
  const denominator = Math.max(total, 1);
  const completedPct = (completed / denominator) * 100;
  const runningPct = (running / denominator) * 100;
  const remainingPct = Math.max(0, 100 - completedPct - runningPct);
  return (
    <div className="space-y-3">
      <div className="flex h-2 overflow-hidden rounded-sm bg-muted">
        <div className="bg-success" style={{ width: `${completedPct}%` }} />
        <div className="bg-warning" style={{ width: `${runningPct}%` }} />
        <div
          className="bg-muted-foreground/25"
          style={{ width: `${remainingPct}%` }}
        />
      </div>
      <div className="grid gap-2 text-xs sm:grid-cols-4">
        <ProgressStat label={t("scanDetail.subTotal")} value={total} />
        <ProgressStat label={t("scans.subScanned")} value={completed} />
        <ProgressStat label={t("scans.subRunning")} value={running} />
        <ProgressStat label={t("scans.subRemaining")} value={remaining} />
      </div>
    </div>
  );
}

function ProgressStat({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-md border border-border bg-muted/20 px-3 py-2">
      <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
        {label}
      </div>
      <div className="mono mt-1 text-lg text-foreground">{value}</div>
    </div>
  );
}

function RiskBreakdown({ vulns }: { vulns: VulnSummary[] }) {
  const { t } = useI18n();
  const counts = useMemo(() => {
    const c: Record<string, number> = {
      critical: 0,
      high: 0,
      medium: 0,
      low: 0,
      info: 0,
    };
    for (const v of vulns) {
      c[normalizeSeverity(v.severity)] += 1;
    }
    return c;
  }, [vulns]);
  const total = vulns.length || 1;
  const order: Array<keyof typeof counts> = [
    "critical",
    "high",
    "medium",
    "low",
    "info",
  ];
  return (
    <div className="space-y-3">
      <div className="flex h-12 items-end gap-1">
        {order.map((sev) => {
          const n = counts[sev as string];
          if (!n) return null;
          const pct = Math.max(4, Math.round((n / total) * 100));
          return (
            <div
              key={sev}
              className={cn(
                "h-full rounded-sm",
                sev === "critical" && "bg-red-500/70",
                sev === "high" && "bg-orange-500/70",
                sev === "medium" && "bg-amber-400/70",
                sev === "low" && "bg-blue-400/70",
                sev === "info" && "bg-neutral-500/60",
              )}
              style={{ width: `${pct}%` }}
              title={`${sev}: ${n}`}
            />
          );
        })}
        {vulns.length === 0 && (
          <div className="h-full w-full rounded-sm border border-dashed border-border" />
        )}
      </div>
      {/* Severity legend. Horizontal-scroll on very narrow widths so the
          five labels never overlap; wide enough to read at all sizes. */}
      <div className="-mx-1 flex gap-1.5 overflow-x-auto px-1 pb-1 text-[11px]">
        {order.map((sev) => (
          <div
            key={sev}
            className="flex min-w-[60px] flex-1 flex-col rounded-md border border-border bg-muted/20 px-2 py-1.5"
          >
            <span className="truncate uppercase tracking-wide text-muted-foreground">
              {t(`severity.${sev}`)}
            </span>
            <span className="mono text-base leading-tight text-foreground">
              {counts[sev as string]}
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

function SubdomainsTab({ subScans }: { subScans: SubScanSummary[] }) {
  const { t } = useI18n();
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState<number>(DEFAULT_PAGE_SIZE);

  const total = subScans.length;
  // Clamp the current page when the list shrinks/grows (it grows live during a
  // wildcard scan) so we never strand the user on an empty page.
  const totalPages = Math.max(1, Math.ceil(total / Math.max(1, pageSize)));
  const safePage = Math.min(Math.max(1, page), totalPages);
  useEffect(() => {
    if (safePage !== page) setPage(safePage);
  }, [safePage, page]);

  const paged = useMemo(() => {
    const start = (safePage - 1) * pageSize;
    return subScans.slice(start, start + pageSize);
  }, [subScans, safePage, pageSize]);

  if (subScans.length === 0) {
    return (
      <EmptyState
        title={t("scanDetail.subEmptyTitle")}
        description={t("scanDetail.subEmptyDescription")}
      />
    );
  }

  return (
    <Card>
      <CardContent className="p-0">
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="border-b border-border bg-muted/30 text-xs uppercase tracking-wider text-muted-foreground">
              <tr>
                <Th>{t("scanDetail.tab.subdomains")}</Th>
                <Th>{t("scans.col.status")}</Th>
                <Th>{t("scans.col.findings")}</Th>
                <Th>{t("scans.col.tokens")}</Th>
                <Th>{t("scans.col.started")}</Th>
              </tr>
            </thead>
            <tbody>
              {paged.map((sub) => (
                <tr
                  key={sub.id || sub.target}
                  className="border-b border-border/60 last:border-0"
                >
                  <Td>
                    <div className="mono text-sm text-foreground">
                      {sub.target}
                    </div>
                    <div className="mono text-xs text-muted-foreground">
                      {sub.id}
                    </div>
                  </Td>
                  <Td>
                    <ScanStatusPill status={sub.status} />
                  </Td>
                  <Td className="mono text-xs">{sub.vuln_count ?? 0}</Td>
                  <Td className="mono text-xs text-muted-foreground">
                    {sub.total_tokens ? sub.total_tokens.toLocaleString() : "—"}
                  </Td>
                  <Td className="text-muted-foreground">
                    {sub.started_at ? timeAgo(sub.started_at) : "—"}
                  </Td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <Pagination
          totalItems={total}
          page={safePage}
          pageSize={pageSize}
          onPageChange={setPage}
          onPageSizeChange={setPageSize}
        />
      </CardContent>
    </Card>
  );
}

function Th({
  children,
  className = "",
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <th className={cn("px-4 py-3 text-left font-medium", className)}>
      {children}
    </th>
  );
}

function Td({
  children,
  className = "",
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <td className={cn("px-4 py-3 align-middle", className)}>{children}</td>
  );
}

function findingKey(scanId: string, finding: VulnSummary): string {
  // source_scan_id disambiguates IDs reused by wildcard child scans. The
  // displayed scan is the safe fallback for responses from older records.
  return JSON.stringify([finding.source_scan_id || scanId, finding.id]);
}

function findingSourceScanId(scanId: string, finding: VulnSummary): string {
  return finding.source_scan_id || scanId;
}

function FindingsTab({
  vulns,
  scanId,
}: {
  vulns: VulnSummary[];
  scanId: string;
}) {
  const { t } = useI18n();
  const del = useDeleteVuln();
  const [selected, setSelected] = useState<VulnSummary | null>(null);
  const [selectedKeys, setSelectedKeys] = useState<Set<string>>(() => new Set());
  const [pendingDelete, setPendingDelete] = useState<string[] | null>(null);
  const sorted = useMemo(
    () =>
      [...vulns].sort(
        (a, b) => severityRank(b.severity) - severityRank(a.severity),
      ),
    [vulns],
  );
  const findingsByKey = useMemo(
    () => new Map(sorted.map((finding) => [findingKey(scanId, finding), finding])),
    [scanId, sorted],
  );

  const allSelected = sorted.length > 0 && selectedKeys.size === sorted.length;

  useEffect(() => {
    const allKeys = new Set(sorted.map((finding) => findingKey(scanId, finding)));
    setSelectedKeys((current) => {
      const next = new Set([...current].filter((key) => allKeys.has(key)));
      return next.size === current.size ? current : next;
    });
  }, [scanId, sorted]);

  function toggleSelect(key: string, checked: boolean) {
    setSelectedKeys((current) => {
      const next = new Set(current);
      if (checked) next.add(key);
      else next.delete(key);
      return next;
    });
  }

  function selectAll() {
    setSelectedKeys(new Set(sorted.map((finding) => findingKey(scanId, finding))));
  }
  function clearSelection() {
    setSelectedKeys(new Set());
  }

  async function deleteVulns(keys: string[]) {
    const selectedFindings = keys
      .map((key) => findingsByKey.get(key))
      .filter((finding): finding is VulnSummary => Boolean(finding));
    const unique = new Map<string, { scanId: string; vulnId: string }>();
    for (const finding of selectedFindings) {
      const sourceScanId = findingSourceScanId(scanId, finding);
      unique.set(
        JSON.stringify([sourceScanId, finding.id]),
        { scanId: sourceScanId, vulnId: finding.id },
      );
    }
    if (!unique.size) return;
    const identities = [...unique.values()];
    const settled = await Promise.allSettled(
      identities.map((identity) => del.mutateAsync(identity)),
    );
    const succeeded = identities.filter((_, i) => settled[i].status === "fulfilled");
    const failed = identities.length - succeeded.length;
    const succeededKeys = new Set(
      [...unique.entries()]
        .filter(([, identity]) => succeeded.includes(identity))
        .map(([key]) => key),
    );
    setSelectedKeys((current) => {
      const next = new Set(current);
      for (const key of keys) {
        if (succeededKeys.has(key)) next.delete(key);
      }
      return next;
    });
    if (succeeded.length > 0) {
      toast.success(`${succeeded.length} ${t("toast.deletedCount")}`);
    }
    if (failed > 0) {
      toast.error(`${failed} ${t("toast.deleteFailed")}`);
    }
  }

  if (sorted.length === 0)
    return (
      <EmptyState
        title={t("taskDetail.noFindingsTitle")}
        description={t("taskDetail.noFindingsDescription")}
      />
    );

  return (
    <>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={allSelected ? clearSelection : selectAll}
        >
          {allSelected ? t("common.clearSelection") : t("scans.selectAll")}
        </Button>
        <span className="text-xs text-muted-foreground">
          {selectedKeys.size} {t("scans.selectedCount")}
        </span>
        <BulkActionMenu
          disabled={selectedKeys.size === 0 || del.isPending}
          selectedCount={selectedKeys.size}
          onDelete={() => setPendingDelete([...selectedKeys])}
        />
      </div>
      <div className="space-y-2">
        {sorted.map((f) => {
          const key = findingKey(scanId, f);
          return (
            <Card
              key={key}
              id={`finding-${encodeURIComponent(key)}`}
              className={cn(
                "overflow-hidden",
                selectedKeys.has(key) && "ring-1 ring-primary/30",
              )}
            >
              <div className="flex items-start gap-2 p-2 pl-4">
                <input
                  type="checkbox"
                  checked={selectedKeys.has(key)}
                  aria-label={`Select ${f.title}`}
                  onChange={(e) => toggleSelect(key, e.currentTarget.checked)}
                  className="mt-3 h-4 w-4 shrink-0 rounded border-border bg-input accent-primary"
                />
                <button
                  type="button"
                  className="group block w-full flex-1 text-left transition-colors hover:bg-muted/30 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring rounded"
                  onClick={() => setSelected(f)}
                  aria-label={`Open finding details for ${f.title}`}
                >
                  <CardContent className="flex flex-col gap-3 p-3 sm:flex-row sm:items-start sm:justify-between">
                    <div className="min-w-0 space-y-1 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <SeverityBadge severity={f.severity} />
                        <h3 className="font-medium text-foreground">{f.title}</h3>
                        {f.cve && (
                          <Badge variant="outline" className="mono">
                            {f.cve}
                          </Badge>
                        )}
                        {f.cwe_id && (
                          <Badge
                            variant="outline"
                            className="mono text-emerald-600 dark:text-emerald-400 border-emerald-400/30"
                          >
                            {f.cwe_id}
                          </Badge>
                        )}
                        {f.owasp && (
                          <Badge
                            variant="outline"
                            className="mono text-amber-700 dark:text-amber-400 border-amber-400/30"
                          >
                            {f.owasp}
                          </Badge>
                        )}
                        <VerificationBadge verified={f.verified} tags={f.tags} />
                      </div>
                      {f.description && (
                        <p className="text-sm leading-relaxed text-muted-foreground line-clamp-3">
                          {f.description}
                        </p>
                      )}
                      <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
                        {f.target && <span className="mono">{f.target}</span>}
                        {f.endpoint && <span className="mono">{f.endpoint}</span>}
                        {f.method && <span className="mono">{f.method}</span>}
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-2">
                      {f.cvss != null && f.cvss > 0 && (
                        <Badge variant="outline" className="mono">
                          CVSS {f.cvss.toFixed(1)}
                        </Badge>
                      )}
                      <span className="inline-flex items-center gap-1 text-xs text-muted-foreground group-hover:text-foreground">
                        Details <ArrowRight className="h-3.5 w-3.5" />
                      </span>
                    </div>
                  </CardContent>
                </button>
                <FindingRowMenu
                  finding={f}
                  scanId={scanId}
                  deleting={del.isPending}
                  onDelete={() => setPendingDelete([key])}
                />
              </div>
            </Card>
          );
        })}
      </div>
      <FindingDetailsDialog
        scanId={scanId}
        finding={selected}
        onOpenChange={(open) => !open && setSelected(null)}
      />

      <AlertDialog
        open={!!pendingDelete}
        onOpenChange={(v) => !v && setPendingDelete(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("findings.deleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {pendingDelete
                ? pendingDelete.length === 1
                  ? t("findings.confirmDeleteOne")
                  : `${t("findings.confirmDeleteManyBefore")}${pendingDelete.length}${t("findings.confirmDeleteManyAfter")}`
                : ""}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                void deleteVulns(pendingDelete ?? []);
                setPendingDelete(null);
              }}
            >
              {t("common.delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function BulkActionMenu({
  disabled,
  selectedCount,
  onDelete,
}: {
  disabled: boolean;
  selectedCount: number;
  onDelete: () => void;
}) {
  const { t } = useI18n();
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <Button size="sm" variant="secondary" disabled={disabled}>
          {t("scans.col.actions")}
          <MoreHorizontal className="h-3.5 w-3.5" />
        </Button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content align="start" className={menuContentClass}>
          <DropdownMenu.Label className="px-2 py-1.5 text-xs text-muted-foreground">
            {selectedCount} {t("scans.selectedCount")}
          </DropdownMenu.Label>
          <DropdownMenu.Separator className="-mx-1 my-1 h-px bg-border" />
          <DropdownMenu.Item
            className={cn(menuItemClass, "text-red-600 focus:text-red-700 dark:text-red-400 dark:focus:text-red-300")}
            // Do NOT preventDefault: the menu must unmount before the confirm
            // AlertDialog mounts. Two stacked Radix modal layers capture
            // mismatched body pointer-events originals and the last unmount
            // leaves body { pointer-events: none } — the whole page dead
            // until reload (DismissableLayer last-restorer-wins).
            onSelect={() => onDelete()}
          >
            <Trash2 className="h-3.5 w-3.5" />
            {t("scans.deleteSelected")}
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

function FindingRowMenu({
  finding,
  scanId,
  deleting,
  onDelete,
}: {
  finding: VulnSummary;
  scanId: string;
  deleting: boolean;
  onDelete: () => void;
}) {
  const { t } = useI18n();
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <Button
          size="icon"
          variant="ghost"
          aria-label={`${t("scans.actionsForAria")} ${finding.title}`}
          className="mt-1 shrink-0"
        >
          <MoreHorizontal className="h-4 w-4" />
        </Button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content align="end" className={menuContentClass}>
          <DropdownMenu.Item asChild className={menuItemClass}>
            <Link to={`/scans/${scanId}`}>
              <ExternalLink className="h-3.5 w-3.5" />
              {t("scans.openDetails")}
            </Link>
          </DropdownMenu.Item>
          <DropdownMenu.Separator className="-mx-1 my-1 h-px bg-border" />
          <DropdownMenu.Item
            disabled={deleting}
            className={cn(
              menuItemClass,
              "text-red-600 focus:text-red-700 dark:text-red-400 dark:focus:text-red-300 data-[disabled]:pointer-events-none data-[disabled]:opacity-50",
            )}
            // Same layer-ordering constraint as the bulk delete above: the
            // menu must close before the confirm dialog opens.
            onSelect={() => onDelete()}
          >
            <Trash2 className="h-3.5 w-3.5" />
            {t("findings.deleteOne")}
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

function FindingDetailsDialog({
  scanId,
  finding,
  onOpenChange,
}: {
  scanId: string;
  finding: VulnSummary | null;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useI18n();
  return (
    <Dialog open={!!finding} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] w-[calc(100vw-2rem)] min-w-0 max-w-3xl overflow-x-hidden overflow-y-auto">
        {finding && (
          <div className="min-w-0 max-w-full space-y-4">
            <DialogHeader className="min-w-0">
              <div className="flex flex-wrap items-center gap-2 pr-8">
                <SeverityBadge severity={finding.severity} />
                {finding.cve && (
                  <Badge variant="outline" className="mono">
                    {finding.cve}
                  </Badge>
                )}
                {finding.cvss != null && finding.cvss > 0 && (
                  <Badge variant="outline" className="mono">
                    CVSS {finding.cvss.toFixed(1)}
                  </Badge>
                )}
                {finding.cwe_id && (
                  <Badge
                    variant="outline"
                    className="mono text-emerald-600 dark:text-emerald-400 border-emerald-400/30"
                  >
                    {finding.cwe_id}
                  </Badge>
                )}
                {finding.owasp && (
                  <Badge
                    variant="outline"
                    className="mono text-amber-700 dark:text-amber-400 border-amber-400/30"
                  >
                    {finding.owasp}
                  </Badge>
                )}
                <VerificationBadge verified={finding.verified} tags={finding.tags} />
              </div>
              <DialogTitle className="min-w-0 pr-8 text-lg [overflow-wrap:anywhere]">
                {finding.title}
              </DialogTitle>
              <DialogDescription>
                {t("scanDetail.detailDesc")}
              </DialogDescription>
            </DialogHeader>

            <div className="grid min-w-0 max-w-full gap-3 rounded-md border border-border bg-muted/20 p-3 text-sm sm:grid-cols-2">
              <DetailRow label={t("scans.col.target")} value={finding.target} mono />
              <DetailRow label={t("findings.field.endpoint")} value={finding.endpoint} mono />
              <DetailRow label={t("approval.method")} value={finding.method} mono />
              <DetailRow label={t("findings.field.cvssVector")} value={finding.cvss_vector} mono />
              <DetailRow label="CWE" value={finding.cwe_id} mono />
              <DetailRow label="OWASP" value={finding.owasp} mono />
              <DetailRow label={t("findings.field.id")} value={finding.id} mono />
              <DetailRow
                label={t("findings.field.verification")}
                value={finding.verification_method}
              />
            </div>

            <Separator />

            <div className="min-w-0 max-w-full space-y-4">
              <DetailSection title={t("findings.section.description")} value={finding.description} />
              <DetailSection title={t("findings.section.impact")} value={finding.impact} />
              <DetailSection
                title={t("findings.section.analysis")}
                value={finding.technical_analysis}
              />
              <DetailSection
                title={t("findings.section.poc")}
                value={finding.poc_description}
              />
              {finding.poc_script && (
                <DetailSection
                  title={t("findings.section.pocScript")}
                  value={finding.poc_script}
                  code
                />
              )}
              {finding.exploitation_proof && (
                <DetailSection
                  title={t("findings.section.exploitProof")}
                  value={finding.exploitation_proof}
                  code
                />
              )}
              <FindingHttpExchanges scanId={scanId} finding={finding} />
              <DetailSection title={t("findings.section.remediation")} value={finding.remediation} />
              {finding.fix && (
                <DetailSection title={t("findings.section.suggestedFix")} value={finding.fix} code />
              )}
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

function DetailRow({
  label,
  value,
  mono,
}: {
  label: string;
  value?: string;
  mono?: boolean;
}) {
  return (
    <div className="min-w-0">
      <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
        {label}
      </div>
      <div
        className={cn(
          "mt-1 min-w-0 max-w-full text-foreground [overflow-wrap:anywhere]",
          mono && "mono text-xs",
        )}
      >
        {value || "—"}
      </div>
    </div>
  );
}

function DetailSection({
  title,
  value,
  code,
}: {
  title: string;
  value?: string;
  code?: boolean;
}) {
  if (!value) return null;
  return (
    <section className="min-w-0 max-w-full space-y-2">
      <h4 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
        {title}
      </h4>
      {code ? (
        <pre className="max-h-64 min-w-0 max-w-full overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted p-3 text-xs leading-relaxed text-foreground [overflow-wrap:anywhere]">
          <code>{value}</code>
        </pre>
      ) : (
        <Markdown source={value} />
      )}
    </section>
  );
}

// 流量代理为该 finding 记录的原始 HTTP 交互（wire 包，未脱敏）。懒加载：
// 打开详情弹窗才拉取；扫描没有流量归档（代理关闭或旧扫描）或无匹配时整段
// 不渲染——不堆空态文案。
function FindingHttpExchanges({
  scanId,
  finding,
}: {
  scanId: string;
  finding: VulnSummary;
}) {
  const { t } = useI18n();
  const { data } = useScanTraffic(
    findingSourceScanId(scanId, finding),
    finding.endpoint,
    finding.method,
  );
  const exchanges = data?.exchanges ?? [];
  if (exchanges.length === 0) return null;
  return (
    <section className="min-w-0 max-w-full space-y-2">
      <h4 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
        {t("findings.section.httpExchanges")}
      </h4>
      <div className="min-w-0 max-w-full space-y-3">
        {exchanges.map((ex) => (
          <div
            key={ex.seq}
            className="min-w-0 max-w-full space-y-2 rounded-md border border-border p-2"
          >
            <div className="flex min-w-0 flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
              <span className="mono font-medium text-foreground">{ex.method}</span>
              <span className="mono min-w-0 truncate">{ex.url}</span>
              {typeof ex.status === "number" && (
                <span
                  className={cn(
                    "mono",
                    ex.denied
                      ? "text-destructive"
                      : ex.status < 400
                        ? "text-emerald-600 dark:text-emerald-400"
                        : "text-amber-600 dark:text-amber-400",
                  )}
                >
                  {ex.status}
                </span>
              )}
              {ex.high_risk && (
                <span className="mono min-w-0 truncate text-amber-600 dark:text-amber-400">
                  {ex.high_risk}
                </span>
              )}
            </div>
            {ex.request_raw && (
              <pre className="max-h-64 min-w-0 max-w-full overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted p-3 text-xs leading-relaxed text-foreground [overflow-wrap:anywhere]">
                <code>
                  {ex.request_raw}
                  {ex.request_truncated ? `\n${t("findings.http.truncated")}` : ""}
                </code>
              </pre>
            )}
            {ex.response_raw && (
              <pre className="max-h-64 min-w-0 max-w-full overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted p-3 text-xs leading-relaxed text-foreground [overflow-wrap:anywhere]">
                <code>
                  {ex.response_raw}
                  {ex.response_truncated ? `\n${t("findings.http.truncated")}` : ""}
                </code>
              </pre>
            )}
          </div>
        ))}
      </div>
      {data?.truncated && (
        <p className="mono text-[11px] text-muted-foreground">
          {t("findings.http.moreHidden").replace("%d", String(data.total_matched))}
        </p>
      )}
    </section>
  );
}

function EventsTab({
  events,
  scanId,
  instanceId,
  taskId,
  status,
  target,
  canLoadEarlier,
  remainingOlder,
  loadingOlder,
  onLoadEarlier,
}: {
  events: FeedEvent[];
  scanId: string;
  instanceId: string;
  taskId?: string;
  status: string;
  target: string;
  canLoadEarlier: boolean;
  remainingOlder: number;
  loadingOlder: boolean;
  onLoadEarlier: () => void;
}) {
  const { t } = useI18n();
  const [filter, setFilter] = useState<FeedFilter>("all");
  return (
    <div className="space-y-3">
      {canLoadEarlier && (
        <div className="flex items-center justify-center">
          <Button
            variant="outline"
            size="sm"
            onClick={onLoadEarlier}
            disabled={loadingOlder}
          >
            {loadingOlder
              ? t("console.loadingEarlier")
              : `${t("scanDetail.loadEarlier")} (${remainingOlder.toLocaleString()} ${t("scanDetail.loadEarlierCount")})`}
          </Button>
        </div>
      )}
      <LiveFeed
        events={events}
        filter={filter}
        onFilterChange={setFilter}
        exportFilePrefix={`xalgorix-${target || scanId}-events`}
        exportScope={scanId}
        emptyTitle={t("taskDetail.noEventsTitle")}
        emptyDescription={t("taskDetail.noEventsDescription")}
      />
      {status === "running" && taskId && (
        <ScanGuidanceComposer taskId={taskId} instanceId={instanceId} />
      )}
    </div>
  );
}

/**
 * Guidance composer on the legacy run page — the same unified HITL channel as
 * the conversation pages (POST /api/tasks/{id}/guidance, contract §3.1): one
 * persisted human turn per request_id, no optimistic echo, no /api/chat
 * bypass. Rendered only when the run is mounted on a task (task_id present)
 * and running; unmounted legacy scans have no unified channel.
 */
function ScanGuidanceComposer({
  taskId,
  instanceId,
}: {
  taskId: string;
  instanceId: string;
}) {
  const { t } = useI18n();
  const sendGuidance = useSendTaskGuidance();
  const [message, setMessage] = useState("");
  const [requestId, setRequestId] = useState(newRequestId);

  const trimmed = message.trim();
  const codePoints = Array.from(message).length;
  const over = codePoints > MAX_GUIDANCE_CHARS;

  function send() {
    if (!trimmed || over || sendGuidance.isPending) return;
    sendGuidance.mutate(
      { id: taskId, message: trimmed, requestId, instanceId },
      {
        onSuccess: (result) => {
          toast.success(
            result.status === "injected"
              ? t("toast.guidanceInjected")
              : t("toast.guidanceQueued"),
          );
          setMessage("");
          setRequestId(newRequestId());
        },
        onError: (error: Error) =>
          toast.error(`${t("toast.guidanceFailed")}: ${error.message}`),
      },
    );
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-sm">{t("scanDetail.guideScan")}</CardTitle>
        <CardDescription>{t("scanDetail.guideScanDesc")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex flex-col items-stretch gap-2 sm:flex-row sm:items-end">
          <Textarea
            value={message}
            onChange={(event) => setMessage(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter" && !event.shiftKey) {
                event.preventDefault();
                send();
              }
            }}
            placeholder={t("scanDetail.guidancePlaceholder")}
            aria-label={t("scanDetail.guidanceAriaLabel")}
            rows={3}
            maxLength={MAX_GUIDANCE_CHARS}
            disabled={sendGuidance.isPending}
          />
          <Button
            type="button"
            onClick={send}
            disabled={!trimmed || over || sendGuidance.isPending}
            className="shrink-0 sm:w-auto"
          >
            {sendGuidance.isPending ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Send className="h-4 w-4" />
            )}
            {t("console.send")}
          </Button>
        </div>
        <div className="flex items-center justify-between gap-3 text-[11px] text-muted-foreground">
          <span aria-live="polite">{t("scanDetail.guidanceHint")}</span>
          <span
            className={cn(
              "mono shrink-0 tabular-nums",
              over && "text-red-600 dark:text-red-400",
            )}
          >
            {codePoints}/{MAX_GUIDANCE_CHARS}
          </span>
        </div>
      </CardContent>
    </Card>
  );
}

function ConfigTab({
  scan,
}: {
  scan: NonNullable<ReturnType<typeof useScan>["data"]>;
}) {
  const { t } = useI18n();
  const items: Array<{ k: string; v: ReactNode }> = [
    { k: t("tasks.fieldScanMode"), v: scan.scan_mode || "—" },
    {
      k: t("schedules.label.reconAccess"),
      v: scan.recon_mode === "passive" ? t("schedules.access.passive") : t("schedules.access.active"),
    },
    {
      k: t("schedules.label.testingAccess"),
      v: scan.scan_intensity === "passive" ? t("schedules.access.passive") : t("schedules.access.active"),
    },
    {
      k: t("schedules.label.severityFilter"),
      v: (scan.severity_filter ?? []).join(", ") || t("scanDetail.config.all"),
    },
    { k: t("plan.phase"), v: (scan.phases ?? []).join(", ") || t("scanDetail.config.all") },
    { k: t("scanDetail.config.iterations"), v: <span className="mono">{scan.iterations}</span> },
    { k: t("scanDetail.config.toolCalls"), v: <span className="mono">{scan.tool_calls}</span> },
    {
      k: t("common.tokens"),
      v: (
        <span className="mono">{scan.total_tokens?.toLocaleString() ?? 0}</span>
      ),
    },
    { k: t("scanDetail.config.stopReason"), v: scan.stop_reason || "—" },
    { k: t("scans.col.started"), v: formatTime(scan.started_at) },
    { k: t("scanDetail.config.finished"), v: scan.finished_at ? formatTime(scan.finished_at) : "—" },
    {
      k: t("scanDetail.config.discordWebhook"),
      v:
        scan.discord_webhook_configured || scan.discord_webhook
          ? t("scanDetail.config.configured")
          : t("scanDetail.config.none"),
    },
  ];
  return (
    <Card>
      <CardContent className="p-0">
        <dl className="divide-y divide-border/60">
          {items.map((it) => (
            <div
              key={it.k}
              className="grid grid-cols-3 gap-2 px-4 py-3 text-sm"
            >
              <dt className="text-muted-foreground">{it.k}</dt>
              <dd className="col-span-2 text-foreground">{it.v}</dd>
            </div>
          ))}
          {scan.instruction && (
            <div className="grid grid-cols-3 gap-2 px-4 py-3 text-sm">
              <dt className="text-muted-foreground flex items-center gap-1">
                <Sparkles className="h-3 w-3" /> {t("tasks.fieldInstruction")}
              </dt>
              <dd className="col-span-2 whitespace-pre-wrap text-foreground/90">
                {scan.instruction}
              </dd>
            </div>
          )}
        </dl>
      </CardContent>
    </Card>
  );
}

function ScanDetailSkeleton() {
  return (
    <>
      <Skeleton className="h-4 w-24" />
      <Skeleton className="h-10 w-2/3" />
      <div className="grid gap-4 lg:grid-cols-3">
        <Skeleton className="h-40 lg:col-span-2" />
        <Skeleton className="h-40" />
      </div>
      <Skeleton className="h-10 w-72" />
      <Skeleton className="h-96 w-full" />
    </>
  );
}
