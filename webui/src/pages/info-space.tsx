import { useEffect, useMemo } from "react";
import { useSearchParams } from "react-router-dom";
import { Database, Filter, RefreshCw } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { EmptyState } from "@/components/states";
import { ColdPathBadge } from "@/components/coldpath-badge";
import { InfoSubjectRail } from "@/components/info-subject-rail";
import { AssociationView } from "@/components/info-views/association-view";
import { ContextSlice } from "@/components/info-views/context-slice";
import {
  InfoDisabled,
  InfoLoading,
  InfoNotice,
  InfoQueryError,
} from "@/components/info-views/common";
import { RegionTable } from "@/components/info-views/region-table";
import {
  useColdPathSync,
  useInfoAssociation,
  useInfoContext,
  useInfoRegion,
  useInstances,
  useProjects,
  useTaskRuns,
  useTasks,
} from "@/api/queries";
import { useActiveProjectId } from "@/lib/use-active-project";
import { useI18n } from "@/i18n";
import type { InfoItem, InfoNature } from "@/types/api";

type Tab = "region" | "association" | "context";

const TABS: Tab[] = ["region", "association", "context"];
const DEFAULT_DEPTH = 2;

function parseTab(raw: string | null): Tab {
  return TABS.includes(raw as Tab) ? (raw as Tab) : "region";
}

function parseNature(raw: string | null): InfoNature | "" {
  return raw === "observed" || raw === "inferred" ? raw : "";
}

/** datetime-local input value → RFC3339 for the API (empty stays empty). */
function toISO(value: string): string | undefined {
  if (!value) return undefined;
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? undefined : parsed.toISOString();
}

function splitList(value: string | null): string[] {
  return (value ?? "")
    .split(",")
    .map((entry) => entry.trim())
    .filter(Boolean);
}

/**
 * Information space (CORE §4 activation surface). The URL is the only state
 * authority: `?tab=&project=&task=&run=&subject=&subjectKind=&purpose=&nature=
 * &after=&before=&cursor=&back=&seed=&edge=&depth=&since=&until=`.
 *
 * Layout = scope bar ▸ posture header ▸ subject rail + three views. The subject
 * rail turns the charter's deterministic subject filter into navigation, and
 * the region view is a dense table whose expanded row is the full seven-
 * dimension InfoItem view (including the hypotheses it projects into).
 */
export default function InfoSpacePage() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const activeProjectId = useActiveProjectId();
  // Deep links from the ledger may point at another project; honour it read-only
  // instead of silently querying the wrong whitelist.
  const projectId = searchParams.get("project") || activeProjectId || "";

  const tab = parseTab(searchParams.get("tab"));
  const taskParam = searchParams.get("task") ?? "";
  const runParam = searchParams.get("run") ?? "";
  const subject = searchParams.get("subject") ?? "";
  const subjectKind = searchParams.get("subjectKind") ?? "";
  const purpose = searchParams.get("purpose") ?? "";
  const nature = parseNature(searchParams.get("nature"));
  const after = searchParams.get("after") ?? "";
  const before = searchParams.get("before") ?? "";
  const cursor = searchParams.get("cursor") ?? "";
  const back = searchParams.get("back") ?? "";
  const since = searchParams.get("since") ?? "";
  const until = searchParams.get("until") ?? "";
  const seeds = splitList(searchParams.get("seed"));
  const edgeTypes = splitList(searchParams.get("edge"));
  const depth = clampDepth(Number(searchParams.get("depth") ?? DEFAULT_DEPTH));
  const focusId = searchParams.get("focus");

  function patch(mutate: (sp: URLSearchParams) => void) {
    const sp = new URLSearchParams(searchParams);
    mutate(sp);
    setSearchParams(sp, { replace: true });
  }

  // `?focus=` is a one-shot deep link (approvals pattern): turn it into a seed,
  // then clean the URL so refresh/back do not re-trigger it.
  useEffect(() => {
    if (!focusId) return;
    patch((sp) => {
      sp.delete("focus");
      if (!sp.get("seed")) sp.set("seed", focusId);
      sp.set("tab", "association");
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusId]);

  const { data: projectsData } = useProjects();
  const { data: tasksData } = useTasks(projectId);
  const { data: instancesData } = useInstances(projectId);
  const { data: taskRunsData } = useTaskRuns(taskParam || undefined);

  const project = projectsData?.projects.find((p) => p.id === projectId);
  const tasks = tasksData?.tasks ?? [];

  const runOptions = useMemo(() => {
    if (taskParam) {
      const runs = taskRunsData?.items ?? [];
      return runs
        .filter((run) => run.instance_id)
        .map((run) => ({ value: run.instance_id!, label: run.id }));
    }
    return (instancesData?.instances ?? []).map((instance) => ({
      value: instance.id,
      label: instance.id,
    }));
  }, [taskParam, taskRunsData, instancesData]);

  // Explicit run wins; otherwise the first option (runs are newest-first).
  const activeRun = runParam || runOptions[0]?.value || "";

  const regionParams = useMemo(
    () => ({
      projectId,
      taskId: taskParam || undefined,
      subject: subject.trim() || undefined,
      subjectKind: subjectKind.trim() || undefined,
      purpose: purpose.trim() || undefined,
      nature,
      after: toISO(after),
      before: toISO(before),
      cursor: cursor || undefined,
      limit: 50,
    }),
    [projectId, taskParam, subject, subjectKind, purpose, nature, after, before, cursor],
  );
  // The region query is the page's base data: the posture header and the subject
  // rail read it on every tab, so it is always enabled.
  const region = useInfoRegion(regionParams, !!projectId);

  const association = useInfoAssociation(
    useMemo(
      () => ({
        projectId,
        taskId: taskParam || undefined,
        scanId: runParam || undefined,
        ids: seeds.join(","),
        edgeTypes: edgeTypes.length ? edgeTypes : undefined,
        maxDepth: depth,
        limit: 100,
      }),
      [projectId, taskParam, runParam, seeds, edgeTypes, depth],
    ),
    tab === "association" && !!projectId && seeds.length > 0,
  );

  const context = useInfoContext(
    useMemo(
      () => ({
        projectId,
        scanId: activeRun,
        subject: subject.trim() || undefined,
        subjectKind: subjectKind.trim() || undefined,
        since: toISO(since),
        until: toISO(until),
        limit: 100,
      }),
      [projectId, activeRun, subject, subjectKind, since, until],
    ),
    tab === "context" && !!projectId && !!activeRun,
  );

  // 冷路径健康章始终跟随当前解析到的 Run（region 也是项目/任务级查询，但同步
  // 健康是按 Run 计的，标出来源 run 才算如实）。
  const coldpath = useColdPathSync(activeRun, !!activeRun);

  const items = region.data?.items ?? [];
  const stats = useMemo(() => summarise(items), [items]);

  function selectSubject(identifier: string, kind: string) {
    patch((sp) => {
      if (identifier) sp.set("subject", identifier);
      else sp.delete("subject");
      if (kind) sp.set("subjectKind", kind);
      else sp.delete("subjectKind");
      sp.delete("cursor");
      sp.delete("back");
    });
  }

  function startAssociation(id: string) {
    patch((sp) => {
      sp.set("tab", "association");
      sp.set("seed", id);
    });
  }

  if (!projectId) {
    return (
      <div className="space-y-6">
        <PageHeader coldpath={null} />
        <EmptyState
          title={t("info.noProject.title")}
          description={t("info.noProject.description")}
        />
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <PageHeader
        coldpath={
          coldpath.data && !coldpath.error ? (
            <ColdPathBadge state={coldpath.data} />
          ) : null
        }
      />

      {/* 作用域条：项目 ▸ 任务 ▸ Run + 时间窗，URL 唯一权威。 */}
      <Card className="rounded-md">
        <CardContent className="flex flex-wrap items-center gap-2 p-3">
          <Badge variant="outline" className="max-w-40">
            <Database className="size-3" aria-hidden />
            <span className="truncate">{project?.name ?? t("info.noProject.title")}</span>
          </Badge>
          <Select
            value={taskParam || "all"}
            onValueChange={(value) =>
              patch((sp) => {
                if (value === "all") sp.delete("task");
                else sp.set("task", value);
                sp.delete("run");
                sp.delete("cursor");
                sp.delete("back");
              })
            }
          >
            <SelectTrigger className="h-9 w-52" aria-label={t("scopeBar.task")}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("scopeBar.allTasks")}</SelectItem>
              {tasks.map((task) => (
                <SelectItem key={task.id} value={task.id}>
                  {task.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {tab !== "region" && (
            <Select
              value={runParam || (tab === "context" ? activeRun : "all")}
              onValueChange={(value) =>
                patch((sp) => {
                  if (value === "all") sp.delete("run");
                  else sp.set("run", value);
                })
              }
              disabled={runOptions.length === 0}
            >
              <SelectTrigger className="h-9 w-56" aria-label={t("scopeBar.run")}>
                <SelectValue placeholder={t("info.scan.placeholder")} />
              </SelectTrigger>
              <SelectContent>
                {tab === "association" && (
                  <SelectItem value="all">{t("scopeBar.allRuns")}</SelectItem>
                )}
                {runOptions.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    <span className="font-mono text-xs">{option.label}</span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          {tab !== "association" && (
            <>
              <Input
                type="datetime-local"
                value={tab === "context" ? since : after}
                onChange={(event) => {
                  const value = event.target.value;
                  patch((sp) => {
                    const key = tab === "context" ? "since" : "after";
                    if (value) sp.set(key, value);
                    else sp.delete(key);
                    sp.delete("cursor");
                    sp.delete("back");
                  });
                }}
                aria-label={t("info.context.since")}
                className="h-9 w-52"
              />
              <Input
                type="datetime-local"
                value={tab === "context" ? until : before}
                onChange={(event) => {
                  const value = event.target.value;
                  patch((sp) => {
                    const key = tab === "context" ? "until" : "before";
                    if (value) sp.set(key, value);
                    else sp.delete(key);
                    sp.delete("cursor");
                    sp.delete("back");
                  });
                }}
                aria-label={t("info.context.until")}
                className="h-9 w-52"
              />
            </>
          )}
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              region.refetch();
              if (tab === "association") association.refetch();
              if (tab === "context") context.refetch();
            }}
            aria-label={t("info.refresh")}
          >
            <RefreshCw className="size-3.5" aria-hidden />
            {t("info.refresh")}
          </Button>
        </CardContent>
      </Card>

      {/* 态势头：口径 = 当前页已加载行，禁止冒充全量。 */}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Stat label={t("posture.loaded")} value={String(stats.total)} note={t("posture.loadedNote")} />
        <Stat
          label={t("posture.nature")}
          value={`${stats.observed}/${stats.inferred}`}
          note={t("posture.natureNote")}
        />
        <Stat label={t("posture.sources")} value={String(stats.sources)} note={stats.topSource || t("posture.sourcesNote")} />
        <Stat
          label={t("posture.coordinates")}
          value={String(stats.complete)}
          note={`${stats.partial} ${t("posture.coordinatePartial")}`}
        />
      </div>

      <div className="grid gap-4 lg:grid-cols-[15rem_minmax(0,1fr)]">
        <aside className="rounded-md border border-border bg-card p-3 lg:max-h-[calc(100vh-20rem)]">
          <InfoSubjectRail
            items={items}
            activeSubject={subject}
            onSelect={selectSubject}
          />
        </aside>

        <Tabs
          value={tab}
          onValueChange={(value) => patch((sp) => sp.set("tab", value))}
          className="min-w-0 space-y-3"
        >
          <TabsList className="w-full justify-start overflow-x-auto">
            <TabsTrigger value="region">{t("info.tabs.region")}</TabsTrigger>
            <TabsTrigger value="association">{t("info.tabs.association")}</TabsTrigger>
            <TabsTrigger value="context">{t("info.tabs.context")}</TabsTrigger>
          </TabsList>

          <TabsContent value="region" className="mt-0 space-y-3">
            <RegionFilters
              subject={subject}
              subjectKind={subjectKind}
              purpose={purpose}
              nature={nature}
              clear={() =>
                patch((sp) => {
                  ["subject", "subjectKind", "purpose", "nature"].forEach((key) =>
                    sp.delete(key),
                  );
                  sp.delete("cursor");
                  sp.delete("back");
                })
              }
              onChange={(key, value) =>
                patch((sp) => {
                  if (value) sp.set(key, value);
                  else sp.delete(key);
                  sp.delete("cursor");
                  sp.delete("back");
                })
              }
            />
            {region.isLoading && !region.data ? (
              <InfoLoading rows={4} />
            ) : region.error ? (
              <InfoQueryError error={region.error} onRetry={() => region.refetch()} />
            ) : !region.data?.available ? (
              <InfoDisabled />
            ) : items.length === 0 ? (
              <EmptyState
                title={t("info.region.empty.title")}
                description={t("info.region.empty.description")}
              />
            ) : (
              <>
                <InfoNotice
                  truncated={region.data.truncated}
                  incomplete={region.data.incomplete}
                  gapReason={region.data.gap_reason}
                />
                <RegionTable
                  items={items}
                  focusedId={seeds[0] ?? null}
                  onSeed={startAssociation}
                  onSelectSubject={selectSubject}
                />
                <Pager
                  hasMore={!!region.data.has_more}
                  nextCursor={region.data.next_cursor}
                  onNext={(next) =>
                    patch((sp) => {
                      sp.set("back", cursor);
                      sp.set("cursor", next);
                    })
                  }
                  canBack={!!cursor}
                  onBack={() =>
                    patch((sp) => {
                      sp.delete("cursor");
                      if (back) sp.set("cursor", back);
                      sp.delete("back");
                    })
                  }
                />
              </>
            )}
          </TabsContent>

          <TabsContent value="association" className="mt-0 space-y-3">
            {seeds.length > 0 && association.isLoading && !association.data ? (
              <InfoLoading rows={3} />
            ) : association.error ? (
              <InfoQueryError error={association.error} onRetry={() => association.refetch()} />
            ) : null}
            {seeds.length === 0 ? (
              <EmptyState
                title={t("info.association.empty.title")}
                description={t("info.association.empty.description")}
              />
            ) : (
              <>
                {association.data && !association.data.available ? (
                  <InfoDisabled />
                ) : (
                  <>
                    {association.data && (
                      <InfoNotice
                        truncated={association.data.truncated}
                        incomplete={association.data.incomplete}
                        gapReason={association.data.gap_reason}
                      />
                    )}
                    <AssociationView
                      hits={association.data?.hits ?? []}
                      seeds={seeds}
                      edgeTypes={edgeTypes}
                      depth={depth}
                      onSeedsChange={(next) =>
                        patch((sp) => {
                          if (next.length) sp.set("seed", next.join(","));
                          else sp.delete("seed");
                        })
                      }
                      onEdgeTypesChange={(next) =>
                        patch((sp) => {
                          if (next.length) sp.set("edge", next.join(","));
                          else sp.delete("edge");
                        })
                      }
                      onDepthChange={(next) =>
                        patch((sp) => sp.set("depth", String(next)))
                      }
                      onSeedNode={(id) =>
                        patch((sp) => {
                          sp.set("seed", id);
                        })
                      }
                    />
                    {association.data?.hits.length === 0 && (
                      <EmptyState
                        title={t("info.association.empty.title")}
                        description={t("info.association.empty.description")}
                      />
                    )}
                    {typeof association.data?.scans_consulted === "number" && (
                      <p className="font-mono text-[10px] text-muted-foreground tabular-nums">
                        {t("info.association.scansConsulted")}{" "}
                        {association.data.scans_consulted}
                      </p>
                    )}
                  </>
                )}
              </>
            )}
          </TabsContent>

          <TabsContent value="context" className="mt-0 space-y-3">
            {!activeRun ? (
              <EmptyState
                title={t("info.context.empty.title")}
                description={t("info.context.empty.description")}
              />
            ) : context.isLoading && !context.data ? (
              <InfoLoading rows={4} />
            ) : context.error ? (
              <InfoQueryError error={context.error} onRetry={() => context.refetch()} />
            ) : !context.data?.available ? (
              <InfoDisabled />
            ) : (
              <>
                {context.data && (
                  <InfoNotice
                    truncated={context.data.truncated}
                    incomplete={context.data.incomplete}
                    gapReason={context.data.gap_reason}
                  />
                )}
                <ContextSlice
                  scanId={activeRun}
                  scanSelect={
                    <span className="font-mono text-[11px] text-muted-foreground">
                      {t("scopeBar.run")}: {activeRun}
                    </span>
                  }
                  items={context.data?.items ?? []}
                  subject={subject}
                  since={since}
                  until={until}
                  onSubjectChange={(value) =>
                    patch((sp) => {
                      if (value) sp.set("subject", value);
                      else sp.delete("subject");
                    })
                  }
                  onSinceChange={(value) =>
                    patch((sp) => {
                      if (value) sp.set("since", value);
                      else sp.delete("since");
                    })
                  }
                  onUntilChange={(value) =>
                    patch((sp) => {
                      if (value) sp.set("until", value);
                      else sp.delete("until");
                    })
                  }
                />
              </>
            )}
          </TabsContent>
        </Tabs>
      </div>
    </div>
  );
}

function PageHeader({ coldpath }: { coldpath: React.ReactNode }) {
  const { t } = useI18n();
  return (
    <header className="flex flex-wrap items-end justify-between gap-3">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("info.title")}</h1>
        <p className="mt-1 text-sm text-muted-foreground">{t("info.subtitle")}</p>
      </div>
      <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
        <Database className="size-3.5" aria-hidden />
        {t("info.coldPathHint")}
        {coldpath && (
          <>
            <span aria-hidden className="h-3 w-px bg-border" />
            {coldpath}
          </>
        )}
      </div>
    </header>
  );
}

function RegionFilters({
  subject,
  subjectKind,
  purpose,
  nature,
  onChange,
  clear,
}: {
  subject: string;
  subjectKind: string;
  purpose: string;
  nature: InfoNature | "";
  onChange: (key: string, value: string) => void;
  clear: () => void;
}) {
  const { t } = useI18n();
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-card p-3">
      <Filter className="size-4 text-muted-foreground" aria-hidden />
      <Input
        value={subject}
        onChange={(event) => onChange("subject", event.target.value)}
        placeholder={t("info.filter.subjectPlaceholder")}
        className="h-9 w-48"
      />
      <Input
        value={subjectKind}
        onChange={(event) => onChange("subjectKind", event.target.value)}
        placeholder={t("info.filter.kindPlaceholder")}
        className="h-9 w-36"
      />
      <Input
        value={purpose}
        onChange={(event) => onChange("purpose", event.target.value)}
        placeholder={t("info.filter.purposePlaceholder")}
        className="h-9 w-44"
      />
      <Select
        value={nature || "all"}
        onValueChange={(value) => onChange("nature", value === "all" ? "" : value)}
      >
        <SelectTrigger className="h-9 w-36" aria-label={t("info.detail.nature")}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="all">{t("info.filter.allNature")}</SelectItem>
          <SelectItem value="observed">{t("info.nature.observed")}</SelectItem>
          <SelectItem value="inferred">{t("info.nature.inferred")}</SelectItem>
        </SelectContent>
      </Select>
      <Button size="sm" variant="ghost" className="h-9" onClick={clear}>
        {t("common.clear")}
      </Button>
    </div>
  );
}

function Pager({
  hasMore,
  nextCursor,
  canBack,
  onNext,
  onBack,
}: {
  hasMore: boolean;
  nextCursor?: string;
  canBack: boolean;
  onNext: (cursor: string) => void;
  onBack: () => void;
}) {
  const { t } = useI18n();
  if (!hasMore && !canBack) return null;
  return (
    <div className="flex items-center justify-end gap-2">
      <Button size="sm" variant="outline" disabled={!canBack} onClick={onBack}>
        {t("common.previous")}
      </Button>
      <Button
        size="sm"
        variant="outline"
        disabled={!hasMore || !nextCursor}
        onClick={() => nextCursor && onNext(nextCursor)}
      >
        {t("common.next")}
      </Button>
    </div>
  );
}

function Stat({
  label,
  value,
  note,
}: {
  label: string;
  value: string;
  note: string;
}) {
  return (
    <Card>
      <CardContent className="p-3">
        <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
          {label}
        </div>
        <div className="mt-0.5 text-xl font-semibold tabular-nums">{value}</div>
        <div className="truncate text-[10px] text-muted-foreground" title={note}>
          {note}
        </div>
      </CardContent>
    </Card>
  );
}

function summarise(items: InfoItem[]) {
  let observed = 0;
  let complete = 0;
  let partial = 0;
  const sources = new Map<string, number>();
  for (const item of items) {
    if (item.nature === "observed") observed += 1;
    if (item.coordinate_status === "complete") complete += 1;
    else if (item.coordinate_status) partial += 1;
    const kind = item.coordinates.source?.kind || "unknown";
    sources.set(kind, (sources.get(kind) ?? 0) + 1);
  }
  const topSource =
    [...sources.entries()].sort((a, b) => b[1] - a[1])[0]?.[0] ?? "";
  return {
    total: items.length,
    observed,
    inferred: items.length - observed,
    complete,
    partial,
    sources: sources.size,
    topSource,
  };
}

function clampDepth(value: number): number {
  if (!Number.isFinite(value)) return DEFAULT_DEPTH;
  return Math.min(3, Math.max(1, Math.trunc(value)));
}
