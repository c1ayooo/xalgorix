import { useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Boxes, Database, List, Orbit, RefreshCw, Search, X } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { EmptyState } from "@/components/states";
import { ColdPathBadge } from "@/components/coldpath-badge";
import { AssociationDialog } from "@/components/info-views/association-dialog";
import { NebulaMap } from "@/components/info-views/nebula-map";
import { SubjectList } from "@/components/info-views/subject-list";
import { SubjectDetailDialog } from "@/components/info-views/subject-detail-dialog";
import { InfoDisabled, InfoNotice, InfoQueryError } from "@/components/info-views/common";
import {
  useColdPathSync,
  useInfoHeatmap,
  useInfoRelations,
  useProjects,
  useTasks,
} from "@/api/queries";
import { useActiveProjectId } from "@/lib/use-active-project";
import { useProjectSelection } from "@/store/project";
import { useI18n } from "@/i18n";
import { HEAT_RAMP_CSS } from "@/lib/nebula";
import type { InfoHeatmapRow } from "@/types/api";

type WindowKey = "24h" | "7d" | "all";
const WINDOWS: WindowKey[] = ["24h", "7d", "all"];
const TOP_N = 200;

function parseWindow(raw: string | null): WindowKey {
  return raw === "24h" || raw === "7d" ? raw : "all";
}

function windowAfter(window: WindowKey): string | undefined {
  if (window === "all") return undefined;
  const ms = window === "24h" ? 24 * 60 * 60 * 1000 : 7 * 24 * 60 * 60 * 1000;
  return new Date(Date.now() - ms).toISOString();
}

/**
 * Information space = the project's heat map (CORE hot/cold + loop).
 *
 * Each tile is one subject: area = how much information exists, colour = the
 * furthest loop stage it reached (observation is cold, finding is hot). The map
 * is the page; there is no filter form or table. Selecting a tile opens the
 * detail dialog, where stage chips narrow the evidence by ring.
 *
 * URL: `?project=&task=&run=&window=24h|7d|all&subject=&subjectKind=&focus=`.
 * `?project=` is a one-shot switch of the sidebar project; `?focus=` is a
 * one-shot association seed. Both are cleaned after use.
 */
export default function InfoSpacePage() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const activeProjectId = useActiveProjectId();
  const { setProjectId } = useProjectSelection();
  const projectParam = searchParams.get("project") ?? "";
  const projectId = projectParam || activeProjectId || "";
  const taskParam = searchParams.get("task") ?? "";
  const runParam = searchParams.get("run") ?? "";
  const windowKey = parseWindow(searchParams.get("window"));
  const subject = searchParams.get("subject") ?? "";
  const subjectKind = searchParams.get("subjectKind") ?? "";
  const focusId = searchParams.get("focus");

  const [search, setSearch] = useState("");
  const [debouncedSearch, setDebouncedSearch] = useState("");
  const [view, setView] = useState<"map" | "list">("map");
  const [showInfoRelations, setShowInfoRelations] = useState(false);
  const [assocSeed, setAssocSeed] = useState<string | null>(null);
  const [assocOpen, setAssocOpen] = useState(false);

  function patch(mutate: (sp: URLSearchParams) => void) {
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        mutate(next);
        return next;
      },
      { replace: true },
    );
  }

  // `?project=` switches the shared switcher once, then leaves the URL so the
  // page follows later sidebar switches.
  useEffect(() => {
    if (!projectParam) return;
    setProjectId(projectParam);
    patch((sp) => sp.delete("project"));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectParam]);

  // `?focus=` opens the bounded relation walk once, then is cleaned.
  useEffect(() => {
    if (!focusId) return;
    setAssocSeed(focusId);
    setAssocOpen(true);
    patch((sp) => sp.delete("focus"));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusId]);

  // Keystrokes must not each fire an aggregation query.
  useEffect(() => {
    const id = window.setTimeout(() => setDebouncedSearch(search.trim()), 250);
    return () => window.clearTimeout(id);
  }, [search]);

  const { data: projectsData } = useProjects();
  const { data: tasksData } = useTasks(projectId);
  const project = projectsData?.projects.find((p) => p.id === projectId);
  const tasks = tasksData?.tasks ?? [];
  const coldpath = useColdPathSync(runParam, !!runParam);

  const after = useMemo(() => windowAfter(windowKey), [windowKey]);
  const heatmap = useInfoHeatmap(
    {
      projectId,
      taskId: taskParam || undefined,
      scanId: runParam || undefined,
      q: debouncedSearch || undefined,
      after,
      limit: TOP_N,
    },
    !!projectId,
  );
  const relations = useInfoRelations(
    {
      projectId,
      taskId: taskParam || undefined,
      scanId: runParam || undefined,
      after,
      limit: 600,
    },
    !!projectId,
  );

  const allEdges = relations.data?.edges ?? [];
  const visibleEdges = useMemo(
    () =>
      showInfoRelations
        ? allEdges
        : allEdges.filter((edge) => edge.category === "chain"),
    [allEdges, showInfoRelations],
  );

  const rows = heatmap.data?.rows ?? [];
  const subjectTotal = heatmap.data?.subject_total ?? rows.length;
  const itemTotal = rows.reduce((sum, row) => sum + row.total, 0);
  const selectedRow =
    rows.find(
      (row) =>
        row.subject === subject && (row.subject_kind ?? "") === (subjectKind ?? ""),
    ) ?? null;

  function selectSubject(row: InfoHeatmapRow) {
    patch((sp) => {
      sp.set("subject", row.subject);
      if (row.subject_kind) sp.set("subjectKind", row.subject_kind);
      else sp.delete("subjectKind");
    });
  }

  function clearSubject() {
    patch((sp) => {
      sp.delete("subject");
      sp.delete("subjectKind");
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
    <div className="page-enter space-y-3">
      <PageHeader
        coldpath={
          runParam && coldpath.data && !coldpath.error ? (
            <ColdPathBadge state={coldpath.data} />
          ) : null
        }
      />

      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="outline" className="max-w-44">
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
              sp.delete("subject");
              sp.delete("subjectKind");
            })
          }
        >
          <SelectTrigger className="h-8 w-48 text-xs" aria-label={t("scopeBar.task")}>
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

        <div className="flex items-center gap-0.5 rounded-md border border-border p-0.5">
          {WINDOWS.map((key) => (
            <Button
              key={key}
              size="sm"
              variant={windowKey === key ? "secondary" : "ghost"}
              className="h-7 px-2.5 text-xs"
              onClick={() =>
                patch((sp) => {
                  if (key === "all") sp.delete("window");
                  else sp.set("window", key);
                })
              }
              aria-pressed={windowKey === key}
            >
              {t(`info.heatmap.window.${key}`)}
            </Button>
          ))}
        </div>

        {runParam && (
          <Badge variant="muted" className="gap-1 font-mono text-[10px]">
            {t("info.heatmap.runScope")} {runParam}
            <button
              type="button"
              className="rounded-sm p-0.5 hover:bg-accent"
              onClick={() => patch((sp) => sp.delete("run"))}
              aria-label={t("info.heatmap.clearRun")}
            >
              <X className="size-3" />
            </button>
          </Badge>
        )}

        <div className="relative min-w-[200px] flex-1">
          <Search className="absolute left-2.5 top-2 h-4 w-4 text-muted-foreground" />
          <Input
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={t("info.map.searchPlaceholder")}
            className="h-8 pl-8 text-xs"
          />
        </div>

        <Button asChild size="sm" variant="outline" className="h-8 text-xs">
          <Link to={taskParam ? `/assets?task=${encodeURIComponent(taskParam)}` : "/assets"}>
            <Boxes className="size-3.5" aria-hidden />
            {t("nav.assets")}
          </Link>
        </Button>

        <div className="flex items-center gap-0.5 rounded-md border border-border p-0.5">
          <Button
            size="sm"
            variant={view === "map" ? "secondary" : "ghost"}
            className="h-7 px-2 text-xs"
            onClick={() => setView("map")}
            aria-pressed={view === "map"}
          >
            <Orbit className="size-3.5" aria-hidden />
            {t("info.map.viewMap")}
          </Button>
          <Button
            size="sm"
            variant={view === "list" ? "secondary" : "ghost"}
            className="h-7 px-2 text-xs"
            onClick={() => setView("list")}
            aria-pressed={view === "list"}
          >
            <List className="size-3.5" aria-hidden />
            {t("info.map.viewList")}
          </Button>
        </div>

        <Button
          size="sm"
          variant="ghost"
          className="h-8 w-8 p-0"
          onClick={() => heatmap.refetch()}
          aria-label={t("info.refresh")}
          title={t("info.refresh")}
        >
          <RefreshCw className="size-3.5" />
        </Button>
      </div>

      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-[10px] text-muted-foreground">
        <span className="font-mono text-[11px] tabular-nums">
          {t("info.map.summary")
            .replace("{subjects}", String(subjectTotal))
            .replace("{items}", String(itemTotal))}
        </span>
        <span className="font-mono tabular-nums">
          Top {Math.min(rows.length, TOP_N)}
        </span>
        <span className="font-mono tabular-nums">
          {t("info.map.relationCount").replace("{n}", String(visibleEdges.length))}
        </span>
        {relations.error && (
          <span className="text-amber-700 dark:text-amber-300">
            {t("info.map.relationsUnavailable")}
          </span>
        )}
        {relations.data?.truncated && (
          <span className="text-amber-700 dark:text-amber-300">
            {t("info.map.relationsTruncated")}
          </span>
        )}
        {heatmap.data?.truncated && (
          <span className="text-amber-700 dark:text-amber-300">
            {t("info.heatmap.truncated")
              .replace("{n}", String(rows.length))
              .replace("{total}", String(subjectTotal))}
          </span>
        )}
        {!!heatmap.data?.unlabeled && (
          <span>
            {t("info.heatmap.unlabeled").replace("{n}", String(heatmap.data.unlabeled))}
          </span>
        )}
        <span className="inline-flex items-center gap-1.5">
          <span>{t("info.map.legend")}</span>
          <span
            className="h-2 w-16 rounded-sm border border-border/60"
            style={{ background: HEAT_RAMP_CSS }}
            aria-hidden
          />
          <span>{t("info.map.stageHint")}</span>
        </span>
        <span className="inline-flex items-center gap-1">
          <span
            className="h-px w-4"
            style={{ background: "rgb(205, 90, 70)" }}
            aria-hidden
          />
          {t("info.map.chainLegend")}
        </span>
        <span className="inline-flex items-center gap-1">
          <span
            className="h-px w-4 border-t border-dashed"
            style={{ borderColor: "rgb(138, 143, 153)" }}
            aria-hidden
          />
          {t("info.map.infoLegend")}
        </span>
        <span>{t("info.map.sizeHint")}</span>
        <label className="ml-auto flex items-center gap-1.5">
          <Switch
            checked={showInfoRelations}
            onCheckedChange={setShowInfoRelations}
          />
          {t("info.map.showInfoRelations")}
        </label>
      </div>

      {heatmap.data?.incomplete && (
        <InfoNotice truncated={false} incomplete gapReason={heatmap.data.gap_reason} />
      )}

      {heatmap.isLoading && !heatmap.data ? (
        <Skeleton className="h-[clamp(420px,calc(100vh_-_19rem),900px)] w-full" />
      ) : heatmap.error ? (
        <InfoQueryError error={heatmap.error} onRetry={() => heatmap.refetch()} />
      ) : !heatmap.data?.available ? (
        <InfoDisabled />
      ) : rows.length === 0 ? (
        <EmptyState
          title={
            debouncedSearch ? t("info.map.noMatchTitle") : t("info.map.emptyTitle")
          }
          description={
            debouncedSearch
              ? t("info.map.noMatchDescription")
              : t("info.map.emptyDescription")
          }
        />
      ) : view === "map" ? (
        <NebulaMap
          rows={rows}
          edges={visibleEdges}
          layoutEdges={allEdges}
          selected={subject ? { subject, subjectKind } : null}
          onSelect={selectSubject}
        />
      ) : (
        <SubjectList
          rows={rows}
          selected={subject ? { subject, subjectKind } : null}
          onSelect={selectSubject}
        />
      )}

      <SubjectDetailDialog
        open={!!subject}
        onOpenChange={(open) => !open && clearSubject()}
        subject={subject}
        subjectKind={subjectKind}
        row={selectedRow}
        projectId={projectId}
        taskId={taskParam || undefined}
        scanId={runParam || undefined}
        after={after}
        edges={visibleEdges}
        onOpenSubject={(nextSubject, nextKind) =>
          patch((sp) => {
            sp.set("subject", nextSubject);
            if (nextKind) sp.set("subjectKind", nextKind);
            else sp.delete("subjectKind");
          })
        }
        onAssociate={(item) => {
          setAssocSeed(item.id);
          setAssocOpen(true);
        }}
      />

      <AssociationDialog
        open={assocOpen}
        projectId={projectId}
        taskId={taskParam || undefined}
        scanId={runParam || undefined}
        seed={assocSeed}
        onOpenChange={setAssocOpen}
      />
    </div>
  );
}

function PageHeader({ coldpath }: { coldpath: React.ReactNode }) {
  const { t } = useI18n();
  return (
    <header className="flex flex-wrap items-end justify-between gap-3">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("info.title")}</h1>
        <p className="mt-1 text-sm text-muted-foreground">{t("info.map.subtitle")}</p>
      </div>
      {coldpath && (
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          {coldpath}
        </div>
      )}
    </header>
  );
}
