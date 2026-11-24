import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  Archive,
  ArchiveRestore,
  Boxes,
  ChevronRight,
  Database,
  ExternalLink,
  GitMerge,
  Plus,
  RefreshCw,
  Search,
} from "lucide-react";
import { toast } from "sonner";
import { useI18n } from "@/i18n";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { EmptyState, ErrorState } from "@/components/states";
import {
  useArchiveProjectAssets,
  useMergeProjectAssets,
  useProjectAsset,
  useProjectAssetEdges,
  useProjectAssets,
  useRecordProjectAsset,
  useTasks,
} from "@/api/queries";
import { useActiveProjectId } from "@/lib/use-active-project";
import type { Asset, AssetEdge, AssetKind } from "@/types/api";
import { cn, timeAgo } from "@/lib/utils";

const KINDS: AssetKind[] = [
  "root_domain",
  "subdomain",
  "ip",
  "service",
  "endpoint",
  "app",
];

/** Targets are the identities an operator scopes an engagement to. Paths
 * (endpoints) are children, never top-level rows. */
const TARGET_KINDS: AssetKind[] = ["root_domain", "subdomain", "ip", "service", "app"];
const TARGET_KIND_SET = new Set<AssetKind>(TARGET_KINDS);

const KIND_TONE: Record<AssetKind, string> = {
  root_domain: "text-foreground",
  subdomain: "text-sky-600 dark:text-sky-400",
  ip: "text-violet-600 dark:text-violet-400",
  service: "text-amber-700 dark:text-amber-400",
  endpoint: "text-emerald-700 dark:text-emerald-400",
  app: "text-rose-700 dark:text-rose-400",
};

/**
 * Assets page = target-first drill-down (project-scoped, cross-run).
 *
 * The flat "every asset kind in one table" view conflated hosts and paths and
 * made the operator pick a kind before they knew what they were looking at.
 * Now the first level is targets (hosts/services/apps); selecting one opens the
 * paths that target exposes (asset_edges rel=exposes) plus provenance. Paths
 * without a parent stay visible in a separate, explicitly labeled section —
 * nothing is hidden, nothing is guessed.
 *
 * URL: `?task=&target=&q=` (`task_id` is a read-compatible alias).
 */
export default function AssetsPage() {
  const { t } = useI18n();
  const projectId = useActiveProjectId();
  const { data: tasksData } = useTasks(projectId ?? undefined);
  const tasks = tasksData?.tasks ?? [];

  const [sp, setSp] = useSearchParams();
  const taskId = sp.get("task") ?? sp.get("task_id") ?? "all";
  const targetParam = Number(sp.get("target") || 0);
  const targetId = Number.isFinite(targetParam) && targetParam > 0 ? targetParam : null;
  const qParam = sp.get("q") ?? "";
  const [query, setQuery] = useState(qParam);
  const [includeArchived, setIncludeArchived] = useState(false);
  const [showUnattached, setShowUnattached] = useState(false);
  const [mergeSource, setMergeSource] = useState<Asset | null>(null);
  const [recordOpen, setRecordOpen] = useState(false);

  useEffect(() => {
    setQuery(qParam);
  }, [qParam]);

  // Scope change invalidates any open asset detail/merge target. Deep links on
  // first render are preserved (the ref starts at the current project).
  const prevProject = useRef(projectId);
  useEffect(() => {
    if (prevProject.current === projectId) return;
    prevProject.current = projectId;
    setMergeSource(null);
    setSp((prev) => {
      const next = new URLSearchParams(prev);
      next.delete("target");
      return next;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId]);

  const list = useProjectAssets(projectId, {
    q: query || undefined,
    task_id: taskId === "all" ? undefined : taskId,
    include_archived: includeArchived,
    limit: 1000,
  });
  // Full project inventory: child paths may not belong to the filtered task,
  // and merge/parent resolution must still see them. Archived rows stay in the
  // map so a child tombstone renders honestly.
  const all = useProjectAssets(projectId, { include_archived: true, limit: 1000 });
  const edgesQuery = useProjectAssetEdges(projectId);
  const detail = useProjectAsset(projectId, targetId);

  const assets = list.data?.items ?? [];
  const allAssets = all.data?.items ?? [];
  const assetByID = useMemo(() => {
    const map = new Map<number, Asset>();
    for (const asset of allAssets) map.set(asset.id, asset);
    for (const asset of assets) map.set(asset.id, asset);
    return map;
  }, [allAssets, assets]);

  const edges = edgesQuery.data?.edges ?? [];
  const { pathCountByTarget, linkedPaths } = useMemo(() => {
    const pathCounts = new Map<number, number>();
    const linked = new Set<number>();
    for (const edge of edges) {
      const child = assetByID.get(edge.dst_id);
      if (!child || !TARGET_KIND_SET.has(assetByID.get(edge.src_id)?.kind as AssetKind)) {
        continue;
      }
      if (child.kind === "endpoint") {
        linked.add(child.id);
        pathCounts.set(edge.src_id, (pathCounts.get(edge.src_id) ?? 0) + 1);
      }
    }
    return { pathCountByTarget: pathCounts, linkedPaths: linked };
  }, [edges, assetByID]);

  const targets = useMemo(
    () => assets.filter((asset) => TARGET_KIND_SET.has(asset.kind)),
    [assets],
  );
  // A search for a path must still find it: paths are shown as matches under
  // their target instead of turning the page into a dead end.
  const matchedPaths = useMemo(
    () => (query ? assets.filter((asset) => asset.kind === "endpoint") : []),
    [assets, query],
  );
  const activePaths = useMemo(
    () =>
      allAssets.filter(
        (asset) =>
          asset.kind === "endpoint" && !asset.archived_at && !asset.merged_into_id,
      ),
    [allAssets],
  );
  const unattachedPaths = useMemo(
    () => activePaths.filter((asset) => !linkedPaths.has(asset.id)),
    [activePaths, linkedPaths],
  );

  const archive = useArchiveProjectAssets(projectId);
  const merge = useMergeProjectAssets(projectId);

  function patch(mutate: (next: URLSearchParams) => void) {
    setSp((prev) => {
      const next = new URLSearchParams(prev);
      mutate(next);
      return next;
    });
  }

  const openTarget = (id: number) => patch((next) => next.set("target", String(id)));
  const closeTarget = () => patch((next) => next.delete("target"));

  const setTaskFilter = (value: string) => {
    patch((next) => {
      next.delete("task_id");
      next.delete("target");
      if (value === "all") next.delete("task");
      else next.set("task", value);
    });
  };

  const applyArchived = (ids: number[], archived: boolean) => {
    if (!ids.length) return;
    archive.mutate(
      { ids, archived },
      {
        onSuccess: (res) => {
          toast.success(
            archived
              ? t("assets.archivedCount").replace("{n}", String(res.updated))
              : t("assets.restoredCount").replace("{n}", String(res.updated)),
          );
        },
        onError: (err) => toast.error(err instanceof Error ? err.message : String(err)),
      },
    );
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">{t("assets.title")}</h1>
          <p className="mt-1 text-sm text-muted-foreground">{t("assets.targetsHint")}</p>
        </div>
        <Button size="sm" onClick={() => setRecordOpen(true)} disabled={!projectId}>
          <Plus className="h-3.5 w-3.5" />
          {t("assets.record")}
        </Button>
      </div>

      <Card>
        <CardContent className="flex flex-wrap items-center gap-2 p-3">
          <div className="relative min-w-[220px] flex-1">
            <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t("assets.searchPlaceholder")}
              className="pl-8"
            />
          </div>
          <Select value={taskId} onValueChange={setTaskFilter}>
            <SelectTrigger className="w-[220px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("assets.taskAll")}</SelectItem>
              {tasks.map((task) => (
                <SelectItem key={task.id} value={task.id}>
                  {task.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            <Switch checked={includeArchived} onCheckedChange={setIncludeArchived} />
            {t("assets.includeArchived")}
          </label>
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              list.refetch();
              all.refetch();
              edgesQuery.refetch();
            }}
            disabled={list.isFetching}
            title={t("common.retry")}
          >
            <RefreshCw className={cn("h-4 w-4", list.isFetching && "animate-spin")} />
          </Button>
        </CardContent>
      </Card>

      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
        <span className="font-mono tabular-nums">
          {t("assets.summary")
            .replace("{targets}", String(targets.length))
            .replace("{paths}", String(activePaths.length))}
        </span>
        <span>{t("assets.scopeNote")}</span>
      </div>

      {list.isError && (
        <ErrorState
          title={t("assets.error")}
          description={list.error instanceof Error ? list.error.message : undefined}
          action={<Button size="sm" onClick={() => list.refetch()}>{t("common.retry")}</Button>}
        />
      )}

      {list.isLoading && !list.data ? (
        <div className="space-y-2">
          {[...Array(6)].map((_, i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </div>
      ) : !list.isError &&
        targets.length === 0 &&
        matchedPaths.length === 0 &&
        unattachedPaths.length === 0 ? (
        <EmptyState
          icon={<Boxes className="h-6 w-6" />}
          title={query ? t("assets.noMatchTitle") : t("assets.targetEmptyTitle")}
          description={
            query ? t("assets.noMatchDescription") : t("assets.targetEmptyDescription")
          }
          action={
            query ? undefined : (
              <Button size="sm" variant="outline" onClick={() => setRecordOpen(true)}>
                <Plus className="h-3.5 w-3.5" />
                {t("assets.record")}
              </Button>
            )
          }
        />
      ) : (
        <>
          {targets.length > 0 && (
            <Card>
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead className="border-b border-border text-xs text-muted-foreground">
                    <tr>
                      <th className="px-4 py-2 text-left">{t("assets.columnAsset")}</th>
                      <th className="px-3 py-2 text-left">{t("assets.columnKind")}</th>
                      <th className="px-3 py-2 text-left">{t("assets.columnPaths")}</th>
                      <th className="px-3 py-2 text-left">{t("assets.columnUpdated")}</th>
                      <th className="px-3 py-2 text-right">{t("common.actions")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {targets.map((asset) => (
                      <TargetRow
                        key={asset.id}
                        asset={asset}
                        pathCount={pathCountByTarget.get(asset.id) ?? 0}
                        archived={!!asset.archived_at}
                        onOpen={() => openTarget(asset.id)}
                        onArchive={() => applyArchived([asset.id], !asset.archived_at)}
                        onMerge={() => setMergeSource(asset)}
                      />
                    ))}
                  </tbody>
                </table>
              </div>
            </Card>
          )}

          {matchedPaths.length > 0 && (
            <Card>
              <div className="border-b border-border px-4 py-2 text-xs font-medium">
                {t("assets.matchingPaths")}
                <span className="ml-2 font-mono text-[10px] tabular-nums text-muted-foreground">
                  {matchedPaths.length}
                </span>
              </div>
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <tbody>
                    {matchedPaths.map((asset) => (
                      <TargetRow
                        key={asset.id}
                        asset={asset}
                        pathCount={0}
                        showPathCount={false}
                        archived={!!asset.archived_at}
                        onOpen={() => openTarget(asset.id)}
                        onArchive={() => applyArchived([asset.id], !asset.archived_at)}
                        onMerge={() => setMergeSource(asset)}
                      />
                    ))}
                  </tbody>
                </table>
              </div>
            </Card>
          )}
        </>
      )}

      {unattachedPaths.length > 0 && (
        <Card>
          <CardContent className="space-y-2 p-3">
            <button
              type="button"
              className="flex w-full items-center gap-1.5 text-left"
              onClick={() => setShowUnattached((value) => !value)}
              aria-expanded={showUnattached}
            >
              <ChevronRight
                className={cn(
                  "h-3.5 w-3.5 text-muted-foreground transition-transform",
                  showUnattached && "rotate-90",
                )}
              />
              <span className="text-xs font-medium">{t("assets.unattachedTitle")}</span>
              <span className="font-mono text-[10px] tabular-nums text-muted-foreground">
                {unattachedPaths.length}
              </span>
            </button>
            <p className="text-[11px] text-muted-foreground">
              {t("assets.unattachedDescription")}
            </p>
            {showUnattached && (
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <tbody>
                    {unattachedPaths.map((asset) => (
                      <TargetRow
                        key={asset.id}
                        asset={asset}
                        pathCount={0}
                        showPathCount={false}
                        unattached
                        archived={!!asset.archived_at}
                        onOpen={() => openTarget(asset.id)}
                        onArchive={() => applyArchived([asset.id], !asset.archived_at)}
                        onMerge={() => setMergeSource(asset)}
                      />
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </CardContent>
        </Card>
      )}

      <p className="text-xs text-muted-foreground">
        {t("assets.showing").replace("{n}", String(targets.length))}
      </p>

      <AssetDetailDialog
        open={targetId !== null}
        onOpenChange={(open) => !open && closeTarget()}
        detail={detail.data}
        loading={detail.isLoading}
        assetByID={assetByID}
        projectId={projectId}
        onOpenAsset={openTarget}
        onArchive={(asset, archived) => applyArchived([asset.id], archived)}
        onMerge={(asset) => {
          closeTarget();
          setMergeSource(asset);
        }}
      />

      <MergeAssetDialog
        key={mergeSource?.id ?? "none"}
        source={mergeSource}
        candidates={allAssets.filter(
          (a) =>
            a.id !== mergeSource?.id &&
            !a.archived_at &&
            !a.merged_into_id &&
            // Merging a target into a path (or vice versa) would corrupt the
            // identity level; only same-level merges are offered.
            (mergeSource ? a.kind === mergeSource.kind : true),
        )}
        pending={merge.isPending}
        onOpenChange={(open) => !open && setMergeSource(null)}
        onConfirm={(targetId) => {
          if (!mergeSource) return;
          merge.mutate(
            { sourceId: mergeSource.id, targetId },
            {
              onSuccess: () => {
                toast.success(t("assets.mergeSuccess"));
                setMergeSource(null);
              },
              onError: (err) =>
                toast.error(err instanceof Error ? err.message : String(err)),
            },
          );
        }}
      />

      <RecordAssetDialog
        open={recordOpen}
        projectId={projectId}
        onOpenChange={setRecordOpen}
        onRecorded={() => {
          setRecordOpen(false);
          toast.success(t("assets.recordSuccess"));
        }}
      />
    </div>
  );
}

function TargetRow({
  asset,
  pathCount,
  showPathCount = true,
  unattached = false,
  archived,
  onOpen,
  onArchive,
  onMerge,
}: {
  asset: Asset;
  pathCount: number;
  showPathCount?: boolean;
  unattached?: boolean;
  archived: boolean;
  onOpen: () => void;
  onArchive: () => void;
  onMerge: () => void;
}) {
  const { t } = useI18n();
  const merged = !!asset.merged_into_id;
  return (
    <tr
      className={cn(
        "border-b border-border/60 last:border-0 hover:bg-accent/40",
        archived && "opacity-60",
      )}
    >
      <td className="px-4 py-2">
        <button className="text-left font-medium hover:underline" onClick={onOpen}>
          {asset.display_name || asset.key}
        </button>
        {asset.display_name && asset.display_name !== asset.key && (
          <div className="text-xs text-muted-foreground">{asset.key}</div>
        )}
      </td>
      <td className="px-3 py-2">
        <span className={cn("text-xs font-medium", KIND_TONE[asset.kind])}>
          {t(`assets.kind.${asset.kind}`)}
        </span>
      </td>
      <td className="px-3 py-2">
        {showPathCount ? (
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {pathCount}
          </span>
        ) : unattached ? (
          <Badge variant="muted">{t("assets.unattachedBadge")}</Badge>
        ) : (
          <span className="text-xs text-muted-foreground">—</span>
        )}
      </td>
      <td className="px-3 py-2 text-xs text-muted-foreground">
        {timeAgo(asset.updated_at)}
      </td>
      <td className="px-3 py-2">
        <div className="flex justify-end gap-1">
          <Button size="sm" variant="ghost" onClick={onOpen}>
            {t("assets.detail")}
          </Button>
          {!merged && (
            <>
              <Button
                size="sm"
                variant="ghost"
                onClick={onMerge}
                aria-label={t("assets.merge")}
                title={t("assets.merge")}
              >
                <GitMerge className="h-3.5 w-3.5" />
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={onArchive}
                aria-label={archived ? t("assets.restore") : t("assets.archive")}
                title={archived ? t("assets.restore") : t("assets.archive")}
              >
                {archived ? (
                  <ArchiveRestore className="h-3.5 w-3.5" />
                ) : (
                  <Archive className="h-3.5 w-3.5" />
                )}
              </Button>
            </>
          )}
        </div>
      </td>
    </tr>
  );
}

function AssetDetailDialog({
  open,
  onOpenChange,
  detail,
  loading,
  assetByID,
  projectId,
  onOpenAsset,
  onArchive,
  onMerge,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  detail: ReturnType<typeof useProjectAsset>["data"];
  loading: boolean;
  assetByID: Map<number, Asset>;
  projectId: string | null;
  onOpenAsset: (id: number) => void;
  onArchive: (asset: Asset, archived: boolean) => void;
  onMerge: (asset: Asset) => void;
}) {
  const { t } = useI18n();
  const asset = detail?.asset;
  // Cross-lens hand-off. Identity stays in the asset graph; the information
  // space owns observation/provenance, so we link by its own keys (task, run,
  // InfoItem seed) instead of pretending the subject strings are the same.
  const infoHref = (params: { task?: string; run?: string; focus?: string }) => {
    const sp = new URLSearchParams();
    if (projectId) sp.set("project", projectId);
    if (params.task) sp.set("task", params.task);
    if (params.run) sp.set("run", params.run);
    if (params.focus) sp.set("focus", params.focus);
    const qs = sp.toString();
    return qs ? `/info?${qs}` : "/info";
  };

  const children = useMemo(() => {
    if (!detail?.asset) return [];
    return detail.edges
      .filter((edge) => edge.src_id === detail.asset.id)
      .map((edge) => ({ edge, other: assetByID.get(edge.dst_id) }))
      .filter((entry): entry is { edge: AssetEdge; other: Asset } => !!entry.other);
  }, [detail, assetByID]);
  const parents = useMemo(() => {
    if (!detail?.asset) return [];
    return detail.edges
      .filter((edge) => edge.dst_id === detail.asset.id)
      .map((edge) => ({ edge, other: assetByID.get(edge.src_id) }))
      .filter((entry): entry is { edge: AssetEdge; other: Asset } => !!entry.other);
  }, [detail, assetByID]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            {asset ? asset.display_name || asset.key : t("common.loading")}
            {asset && <Badge variant="outline">{t(`assets.kind.${asset.kind}`)}</Badge>}
          </DialogTitle>
          <DialogDescription>{asset ? asset.key : ""}</DialogDescription>
        </DialogHeader>
        {loading || !detail ? (
          <Skeleton className="h-40 w-full" />
        ) : (
          <div className="space-y-4 text-sm">
            <div className="grid grid-cols-2 gap-3">
              <Field label={t("assets.createdAt")} value={timeAgo(detail.asset.created_at)} />
              <Field label={t("assets.updatedAt")} value={timeAgo(detail.asset.updated_at)} />
              <Field
                label={t("assets.firstInfoItem")}
                value={detail.asset.first_info_item_id || "—"}
                href={
                  detail.asset.first_info_item_id
                    ? infoHref({ focus: detail.asset.first_info_item_id })
                    : undefined
                }
              />
              <Field
                label={t("assets.lastInfoItem")}
                value={detail.asset.last_info_item_id || "—"}
                href={
                  detail.asset.last_info_item_id
                    ? infoHref({ focus: detail.asset.last_info_item_id })
                    : undefined
                }
              />
              {detail.asset.archived_at && (
                <Field
                  label={t("assets.archivedBy")}
                  value={`${detail.asset.archived_by || "—"} · ${timeAgo(detail.asset.archived_at)}`}
                />
              )}
              {detail.asset.merged_into_id && (
                <Field
                  label={t("assets.mergedInto")}
                  value={`#${detail.asset.merged_into_id}`}
                />
              )}
            </div>

            {detail.asset.kind !== "endpoint" && (
              <section>
                <h4 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                  {t("assets.children")}
                </h4>
                {children.length === 0 ? (
                  <p className="text-xs text-muted-foreground">{t("assets.noChildren")}</p>
                ) : (
                  <ul className="space-y-1">
                    {children.map(({ edge, other }) => (
                      <li key={`${edge.dst_id}-${edge.rel}`} className="flex items-center gap-2 text-xs">
                        <Badge variant="muted">{t(`assets.edge.${edge.rel}`)}</Badge>
                        <button
                          type="button"
                          className="min-w-0 flex-1 truncate text-left font-mono hover:underline"
                          onClick={() => onOpenAsset(other.id)}
                        >
                          {other.display_name || other.key}
                        </button>
                        <span className={cn("text-[10px] font-medium", KIND_TONE[other.kind])}>
                          {t(`assets.kind.${other.kind}`)}
                        </span>
                      </li>
                    ))}
                  </ul>
                )}
              </section>
            )}

            {parents.length > 0 && (
              <section>
                <h4 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                  {t("assets.parents")}
                </h4>
                <ul className="space-y-1">
                  {parents.map(({ edge, other }) => (
                    <li key={`${edge.src_id}-${edge.rel}`} className="flex items-center gap-2 text-xs">
                      <Badge variant="muted">{t(`assets.edge.${edge.rel}`)}</Badge>
                      <button
                        type="button"
                        className="min-w-0 flex-1 truncate text-left font-mono hover:underline"
                        onClick={() => onOpenAsset(other.id)}
                      >
                        {other.display_name || other.key}
                      </button>
                      <span className={cn("text-[10px] font-medium", KIND_TONE[other.kind])}>
                        {t(`assets.kind.${other.kind}`)}
                      </span>
                    </li>
                  ))}
                </ul>
              </section>
            )}

            <section>
              <h4 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                {t("assets.provenance")}
              </h4>
              {detail.links.length === 0 ? (
                <p className="text-xs text-muted-foreground">{t("assets.noProvenance")}</p>
              ) : (
                <ul className="space-y-1">
                  {detail.links.map((link, i) => (
                    <li key={`${link.scan_id}-${i}`} className="flex items-center gap-2 text-xs">
                      <Link className="inline-flex items-center gap-1 hover:underline" to={`/scans/${link.scan_id}`}>
                        {link.scan_id}
                        <ExternalLink className="h-3 w-3" />
                      </Link>
                      {link.task_id && (
                        <Link
                          to={infoHref({ task: link.task_id })}
                          className="text-muted-foreground hover:text-foreground hover:underline"
                        >
                          task {link.task_id}
                        </Link>
                      )}
                      {link.source && <Badge variant="muted">{link.source}</Badge>}
                      <span className="text-muted-foreground">{timeAgo(link.created_at)}</span>
                      <Link
                        to={infoHref({ run: link.scan_id })}
                        className="ml-auto inline-flex items-center gap-1 text-muted-foreground transition-colors hover:text-foreground"
                        title={t("assets.openInInfo")}
                        aria-label={t("assets.openInInfo")}
                      >
                        <Database className="h-3 w-3" />
                      </Link>
                    </li>
                  ))}
                </ul>
              )}
            </section>

            {asset && !asset.merged_into_id && (
              <div className="flex justify-end gap-2 border-t border-border pt-3">
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => onArchive(asset, !asset.archived_at)}
                >
                  {asset.archived_at ? <ArchiveRestore className="h-3.5 w-3.5" /> : <Archive className="h-3.5 w-3.5" />}
                  {asset.archived_at ? t("assets.restore") : t("assets.archive")}
                </Button>
                {!asset.archived_at && (
                  <Button size="sm" variant="outline" onClick={() => onMerge(asset)}>
                    <GitMerge className="h-3.5 w-3.5" />
                    {t("assets.merge")}
                  </Button>
                )}
              </div>
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

function MergeAssetDialog({
  source,
  candidates,
  pending,
  onOpenChange,
  onConfirm,
}: {
  source: Asset | null;
  candidates: Asset[];
  pending: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: (targetId: number) => void;
}) {
  const { t } = useI18n();
  const [targetId, setTargetId] = useState<string>("");
  return (
    <Dialog open={!!source} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("assets.mergeTitle")}</DialogTitle>
          <DialogDescription>
            {t("assets.mergeDescription").replace("{key}", source?.key ?? "")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-2">
          <Label>{t("assets.mergeTarget")}</Label>
          <Select value={targetId} onValueChange={setTargetId}>
            <SelectTrigger>
              <SelectValue placeholder={t("assets.mergeTargetPlaceholder")} />
            </SelectTrigger>
            <SelectContent>
              {candidates.map((a) => (
                <SelectItem key={a.id} value={String(a.id)}>
                  {t(`assets.kind.${a.kind}`)} · {a.key}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={!targetId || pending}
            onClick={() => onConfirm(Number(targetId))}
          >
            {t("assets.merge")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function RecordAssetDialog({
  open,
  projectId,
  onOpenChange,
  onRecorded,
}: {
  open: boolean;
  projectId: string | null;
  onOpenChange: (open: boolean) => void;
  onRecorded: () => void;
}) {
  const { t } = useI18n();
  const mutation = useRecordProjectAsset(projectId);
  const [kind, setKind] = useState<AssetKind>("endpoint");
  const [key, setKey] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [infoItemId, setInfoItemId] = useState("");
  const [error, setError] = useState<string | null>(null);

  const submit = () => {
    if (!key.trim()) {
      setError(t("assets.keyRequired"));
      return;
    }
    setError(null);
    mutation.mutate(
      {
        kind,
        key: key.trim(),
        display_name: displayName.trim() || undefined,
        info_item_id: infoItemId.trim() || undefined,
      },
      {
        onSuccess: () => {
          setKey("");
          setDisplayName("");
          setInfoItemId("");
          onRecorded();
        },
        onError: (err) => setError(err instanceof Error ? err.message : String(err)),
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("assets.recordTitle")}</DialogTitle>
          <DialogDescription>{t("assets.recordDescription")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <div className="space-y-1">
            <Label>{t("assets.kindLabel")}</Label>
            <Select value={kind} onValueChange={(v) => setKind(v as AssetKind)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {KINDS.map((k) => (
                  <SelectItem key={k} value={k}>
                    {t(`assets.kind.${k}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1">
            <Label>{t("assets.keyLabel")}</Label>
            <Input
              value={key}
              onChange={(e) => setKey(e.target.value)}
              placeholder={t("assets.keyPlaceholder")}
            />
          </div>
          <div className="space-y-1">
            <Label>{t("assets.displayNameLabel")}</Label>
            <Input value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
          </div>
          <div className="space-y-1">
            <Label>{t("assets.infoItemLabel")}</Label>
            <Input
              value={infoItemId}
              onChange={(e) => setInfoItemId(e.target.value)}
              placeholder="ii-… / evidence:…"
            />
          </div>
          {error && <p className="text-xs text-red-600 dark:text-red-400">{error}</p>}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button disabled={mutation.isPending} onClick={submit}>
            {t("common.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function Field({
  label,
  value,
  href,
}: {
  label: string;
  value: string;
  href?: string;
}) {
  return (
    <div>
      <div className="text-xs text-muted-foreground">{label}</div>
      {href ? (
        <Link
          to={href}
          className="inline-flex items-center gap-1 break-all font-mono text-xs hover:underline"
        >
          {value}
          <ExternalLink className="h-3 w-3 shrink-0" />
        </Link>
      ) : (
        <div className="break-all font-mono text-xs">{value}</div>
      )}
    </div>
  );
}
