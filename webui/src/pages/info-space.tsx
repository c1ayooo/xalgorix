import { useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Database, Filter, Network, RefreshCw } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
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
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs";
import { EmptyState, ErrorState } from "@/components/states";
import { InfoItemCard } from "@/components/info-item-card";
import { AssociationGraph } from "@/components/association-graph";
import {
  useColdPathSync,
  useInfoAssociation,
  useInfoContext,
  useInfoRegion,
  useInstances,
} from "@/api/queries";
import { HttpError } from "@/api/client";
import { useActiveProjectId } from "@/lib/use-active-project";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type {
  ColdPathState,
  ColdPathSyncState,
  InfoItem,
  InfoNature,
} from "@/types/api";

type Tab = "region" | "association" | "context";
type AssocScope = "project" | "scan";

function isTab(value: string | null): value is Tab {
  return value === "region" || value === "association" || value === "context";
}

function isColdPathDisabled(error: unknown): boolean {
  return error instanceof HttpError && error.status === 503;
}

function Notice({ truncated, incomplete, gapReason }: { truncated: boolean; incomplete: boolean; gapReason?: string }) {
  const { t } = useI18n();
  if (!truncated && !incomplete) return null;
  return (
    <div className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">
      {truncated && <span>{t("info.truncated")}</span>}
      {truncated && incomplete && <span> · </span>}
      {incomplete && (
        <span>
          {t("info.incomplete")}
          {gapReason ? `: ${gapReason}` : ""}
        </span>
      )}
    </div>
  );
}

/**
 * Cold-path sync badge. Five states, one meaning each: disabled is the
 * no-DSN normal state (gray, not red) — only failed carries the destructive
 * semantic. Counts stay mono/tabular per the machine-data rule.
 */
const COLDPATH_BADGE: Record<
  ColdPathState,
  { className: string; dot: string }
> = {
  applied: {
    className:
      "border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
    dot: "bg-emerald-500",
  },
  pending: {
    className:
      "border-sky-500/30 bg-sky-500/10 text-sky-600 dark:text-sky-400",
    dot: "bg-sky-500",
  },
  gap: {
    className:
      "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300",
    dot: "bg-amber-500",
  },
  failed: {
    className:
      "border-red-500/30 bg-red-500/10 text-red-600 dark:text-red-400",
    dot: "bg-red-500",
  },
  disabled: {
    className: "border-border bg-muted text-muted-foreground",
    dot: "bg-muted-foreground/60",
  },
};

function ColdPathBadge({ state }: { state: ColdPathSyncState }) {
  const { t } = useI18n();
  const meta = COLDPATH_BADGE[state.state] ?? COLDPATH_BADGE.disabled;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-[11px]",
        meta.className,
      )}
      aria-label={t(`info.coldpath.${state.state}`)}
    >
      <span aria-hidden className={cn("size-1.5 rounded-full", meta.dot)} />
      {t(`info.coldpath.${state.state}`)}
      <span className="font-mono text-[10px] tabular-nums opacity-80">
        {state.applied_seq}/{state.source_seq}
      </span>
    </span>
  );
}

function DisabledState() {
  const { t } = useI18n();
  return (
    <EmptyState
      icon={<Database className="h-6 w-6" />}
      title={t("info.disabled.title")}
      description={t("info.disabled.description")}
    />
  );
}

function QueryError({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const { t } = useI18n();
  if (isColdPathDisabled(error)) return <DisabledState />;
  return (
    <ErrorState
      title={t("info.error.title")}
      description={error instanceof Error ? error.message : t("info.error.description")}
      action={
        <Button size="sm" variant="outline" onClick={onRetry}>
          {t("common.retry")}
        </Button>
      }
    />
  );
}

function Loading() {
  return (
    <div className="space-y-3">
      <Skeleton className="h-20 w-full" />
      <Skeleton className="h-20 w-full" />
      <Skeleton className="h-20 w-full" />
    </div>
  );
}

/**
 * Information space — project-scoped, read-only InfoItem activation surface.
 *
 * Deep link (REDESIGN §7.1): `?tab=` (region|association|context) is synced
 * to the URL on every tab change (settings-tab pattern, replace); `?focus=`
 * is consumed once on arrival (approvals pattern): it prefills the matching
 * input — start item ids on the association tab, the subject filter on the
 * other two — then the URL is cleaned via replaceState so refresh/back do
 * not re-trigger it. The Run selector stays session-local: the default
 * project-wide association scope needs no scan id for a deep link to work.
 */
export default function InfoSpacePage() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const projectId = useActiveProjectId();
  const [tab, setTab] = useState<Tab>(() => {
    const raw = searchParams.get("tab");
    return isTab(raw) ? raw : "region";
  });
  const [focusId, setFocusId] = useState<string | null>(() =>
    searchParams.get("focus"),
  );
  const [subject, setSubject] = useState("");
  const [subjectKind, setSubjectKind] = useState("");
  const [purpose, setPurpose] = useState("");
  const [nature, setNature] = useState<InfoNature | "all">("all");
  const [limit, setLimit] = useState(50);
  const [scanId, setScanId] = useState("");
  const [since, setSince] = useState("");
  const [until, setUntil] = useState("");
  const [assocScope, setAssocScope] = useState<AssocScope>("project");
  const [assocIds, setAssocIds] = useState("");
  const [edgeTypes, setEdgeTypes] = useState("supports,contradicts,references");
  const [depth, setDepth] = useState(2);

  const { data: instancesData } = useInstances(projectId ?? undefined);
  const instances = instancesData?.instances ?? [];
  const activeScanId = scanId || instances[0]?.id || "";

  // Sync the active tab into the URL (settings-tab pattern).
  useEffect(() => {
    if (searchParams.get("tab") === tab) return;
    const sp = new URLSearchParams(searchParams);
    sp.set("tab", tab);
    setSearchParams(sp, { replace: true });
  }, [tab, searchParams, setSearchParams]);

  // Consume ?focus= once (approvals pattern): prefill the matching input,
  // keep the id for the region-card highlight, then clean the URL.
  useEffect(() => {
    const focus = searchParams.get("focus");
    if (!focus) return;
    setFocusId(focus);
    if (tab === "association") {
      setAssocIds(focus);
    } else {
      setSubject(focus);
    }
    const sp = new URLSearchParams(searchParams);
    sp.delete("focus");
    setSearchParams(sp, { replace: true });
  }, [searchParams, setSearchParams, tab]);

  const regionParams = useMemo(
    () => ({
      projectId: projectId ?? "",
      subject: subject.trim() || undefined,
      subjectKind: subjectKind.trim() || undefined,
      purpose: purpose.trim() || undefined,
      nature: nature === "all" ? ("" as const) : nature,
      limit,
    }),
    [projectId, subject, subjectKind, purpose, nature, limit],
  );
  const region = useInfoRegion(regionParams, tab === "region");
  const context = useInfoContext(
    {
      projectId: projectId ?? "",
      scanId: activeScanId,
      subject: subject.trim() || undefined,
      subjectKind: subjectKind.trim() || undefined,
      since: since || undefined,
      until: until || undefined,
      limit,
    },
    tab === "context" && !!activeScanId,
  );
  const association = useInfoAssociation(
    {
      projectId: projectId ?? "",
      scanId: assocScope === "scan" ? activeScanId : undefined,
      ids: assocIds,
      edgeTypes: edgeTypes.split(",").map((v) => v.trim()).filter(Boolean),
      maxDepth: depth,
      limit,
    },
    tab === "association" && !!assocIds,
  );

  // Cold-path sync state of the currently selected run. Endpoint failures
  // degrade silently: the badge simply does not render.
  const coldpath = useColdPathSync(activeScanId, !!activeScanId);

  function associate(item: InfoItem) {
    setAssocIds(item.id);
    setScanId(item.scan_id);
    setTab("association");
  }

  const scanSelect = (
    <Select
      value={activeScanId}
      onValueChange={setScanId}
      disabled={instances.length === 0}
    >
      <SelectTrigger className="h-9 w-[240px]"><SelectValue placeholder={t("info.scan.placeholder")} /></SelectTrigger>
      <SelectContent>
        {instances.map((inst) => (
          <SelectItem key={inst.id} value={inst.id}>
            <span className="font-mono text-xs">{inst.id}</span>
            <span className="ml-2 text-xs text-muted-foreground">{inst.targets}</span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );

  const scopeToggle = (
    <div
      className="flex items-center gap-1 rounded-md border border-border bg-muted/30 p-1"
      role="group"
      aria-label={t("info.association.scope")}
    >
      <Button
        type="button"
        size="sm"
        variant={assocScope === "project" ? "default" : "ghost"}
        onClick={() => setAssocScope("project")}
      >
        {t("info.association.scope.project")}
      </Button>
      <Button
        type="button"
        size="sm"
        variant={assocScope === "scan" ? "default" : "ghost"}
        onClick={() => setAssocScope("scan")}
      >
        {t("info.association.scope.scan")}
      </Button>
    </div>
  );

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t("info.title")}</h1>
          <p className="mt-1 text-sm text-muted-foreground">{t("info.subtitle")}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <Database className="size-3.5" />
          {t("info.coldPathHint")}
          {coldpath.data && !coldpath.error && (
            <>
              <span aria-hidden className="h-3 w-px bg-border" />
              <ColdPathBadge state={coldpath.data} />
            </>
          )}
        </div>
      </header>

      {!projectId ? (
        <EmptyState title={t("info.noProject.title")} description={t("info.noProject.description")} />
      ) : (
        <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)} className="space-y-4">
          <TabsList>
            <TabsTrigger value="region">{t("info.tabs.region")}</TabsTrigger>
            <TabsTrigger value="association">{t("info.tabs.association")}</TabsTrigger>
            <TabsTrigger value="context">{t("info.tabs.context")}</TabsTrigger>
          </TabsList>

          <TabsContent value="region" className="space-y-4">
            <Card className="rounded-md">
              <CardContent className="flex flex-wrap items-center gap-2 p-4">
                <Filter className="size-4 text-muted-foreground" />
                <Input className="h-9 w-48" placeholder={t("info.filter.subjectPlaceholder")} value={subject} onChange={(e) => setSubject(e.target.value)} />
                <Input className="h-9 w-36" placeholder={t("info.filter.kindPlaceholder")} value={subjectKind} onChange={(e) => setSubjectKind(e.target.value)} />
                <Input className="h-9 w-44" placeholder={t("info.filter.purposePlaceholder")} value={purpose} onChange={(e) => setPurpose(e.target.value)} />
                <Select value={nature} onValueChange={(v) => setNature(v as InfoNature | "all")}>
                  <SelectTrigger className="h-9 w-36"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">{t("info.filter.allNature")}</SelectItem>
                    <SelectItem value="observed">{t("info.nature.observed")}</SelectItem>
                    <SelectItem value="inferred">{t("info.nature.inferred")}</SelectItem>
                  </SelectContent>
                </Select>
                <Select value={String(limit)} onValueChange={(v) => setLimit(Number(v))}>
                  <SelectTrigger className="h-9 w-24"><SelectValue /></SelectTrigger>
                  <SelectContent>{[25, 50, 100, 200].map((n) => <SelectItem key={n} value={String(n)}>{n}</SelectItem>)}</SelectContent>
                </Select>
                <Button size="sm" variant="outline" onClick={() => region.refetch()} aria-label={t("info.refresh")}>
                  <RefreshCw className="size-3.5" />
                </Button>
              </CardContent>
            </Card>
            {region.isLoading ? <Loading /> : region.error ? (
              <QueryError error={region.error} onRetry={() => region.refetch()} />
            ) : !region.data?.available ? <DisabledState /> : (
              <div className="space-y-3">
                <Notice truncated={region.data.truncated} incomplete={region.data.incomplete} gapReason={region.data.gap_reason} />
                {region.data.items.length === 0 ? (
                  <EmptyState title={t("info.region.empty.title")} description={t("info.region.empty.description")} />
                ) : region.data.items.map((item) => (
                  <InfoItemCard
                    key={`${item.scan_id}:${item.id}`}
                    item={item}
                    onAssociate={associate}
                    focused={focusId === item.id}
                  />
                ))}
              </div>
            )}
          </TabsContent>

          <TabsContent value="association" className="space-y-4">
            <Card className="rounded-md">
              <CardContent className="flex flex-wrap items-center gap-2 p-4">
                <Network className="size-4 text-muted-foreground" />
                {scopeToggle}
                {assocScope === "scan" && scanSelect}
                <Input className="h-9 min-w-64 flex-1" placeholder={t("info.association.idsPlaceholder")} value={assocIds} onChange={(e) => setAssocIds(e.target.value)} />
                <Input className="h-9 w-56" value={edgeTypes} onChange={(e) => setEdgeTypes(e.target.value)} aria-label={t("info.association.edgeTypes")} />
                <Select value={String(depth)} onValueChange={(v) => setDepth(Number(v))}>
                  <SelectTrigger className="h-9 w-24"><SelectValue /></SelectTrigger>
                  <SelectContent>{[1, 2, 3].map((n) => <SelectItem key={n} value={String(n)}>d{n}</SelectItem>)}</SelectContent>
                </Select>
              </CardContent>
            </Card>
            {!assocIds || (assocScope === "scan" && !activeScanId) ? (
              <EmptyState title={t("info.association.empty.title")} description={t("info.association.empty.description")} />
            ) : association.isLoading ? <Loading /> : association.error ? (
              <QueryError error={association.error} onRetry={() => association.refetch()} />
            ) : !association.data?.available ? <DisabledState /> : association.data.hits.length === 0 ? (
              <EmptyState title={t("info.association.empty.title")} description={t("info.association.empty.description")} />
            ) : (
              <div className="space-y-3">
                <div className="flex flex-wrap items-center gap-3">
                  <Notice truncated={association.data.truncated} incomplete={association.data.incomplete} gapReason={association.data.gap_reason} />
                  {typeof association.data.scans_consulted === "number" && (
                    <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
                      {t("info.association.scansConsulted")} {association.data.scans_consulted}
                    </span>
                  )}
                </div>
                <AssociationGraph hits={association.data.hits} />
              </div>
            )}
          </TabsContent>

          <TabsContent value="context" className="space-y-4">
            <Card className="rounded-md">
              <CardContent className="flex flex-wrap items-center gap-2 p-4">
                {scanSelect}
                <Input className="h-9 w-44" placeholder={t("info.filter.subjectPlaceholder")} value={subject} onChange={(e) => setSubject(e.target.value)} />
                <Input className="h-9 w-56" type="datetime-local" value={since} onChange={(e) => setSince(e.target.value ? new Date(e.target.value).toISOString() : "")} aria-label={t("info.context.since")} />
                <Input className="h-9 w-56" type="datetime-local" value={until} onChange={(e) => setUntil(e.target.value ? new Date(e.target.value).toISOString() : "")} aria-label={t("info.context.until")} />
                <Button size="sm" variant="outline" onClick={() => context.refetch()} aria-label={t("info.refresh")}><RefreshCw className="size-3.5" /></Button>
              </CardContent>
            </Card>
            {!activeScanId ? <EmptyState title={t("info.context.empty.title")} description={t("info.context.empty.description")} /> : context.isLoading ? <Loading /> : context.error ? (
              <QueryError error={context.error} onRetry={() => context.refetch()} />
            ) : !context.data?.available ? <DisabledState /> : context.data.items.length === 0 ? (
              <EmptyState title={t("info.context.empty.title")} description={t("info.context.empty.description")} />
            ) : (
              <div className="space-y-3">
                <Notice truncated={context.data.truncated} incomplete={context.data.incomplete} gapReason={context.data.gap_reason} />
                {context.data.items.map((item) => <InfoItemCard key={`${item.scan_id}:${item.id}`} item={item} />)}
              </div>
            )}
          </TabsContent>
        </Tabs>
      )}
    </div>
  );
}
