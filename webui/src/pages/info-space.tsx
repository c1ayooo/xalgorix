import { useMemo, useState } from "react";
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
  useInfoAssociation,
  useInfoContext,
  useInfoRegion,
  useInstances,
} from "@/api/queries";
import { HttpError } from "@/api/client";
import { useActiveProjectId } from "@/lib/use-active-project";
import { useI18n } from "@/i18n";
import type { InfoItem, InfoNature } from "@/types/api";

type Tab = "region" | "association" | "context";

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

export default function InfoSpacePage() {
  const { t } = useI18n();
  const projectId = useActiveProjectId();
  const [tab, setTab] = useState<Tab>("region");
  const [subject, setSubject] = useState("");
  const [subjectKind, setSubjectKind] = useState("");
  const [purpose, setPurpose] = useState("");
  const [nature, setNature] = useState<InfoNature | "all">("all");
  const [limit, setLimit] = useState(50);
  const [scanId, setScanId] = useState("");
  const [since, setSince] = useState("");
  const [until, setUntil] = useState("");
  const [assocIds, setAssocIds] = useState("");
  const [edgeTypes, setEdgeTypes] = useState("supports,contradicts,references");
  const [depth, setDepth] = useState(2);

  const { data: instancesData } = useInstances(projectId ?? undefined);
  const instances = instancesData?.instances ?? [];
  const activeScanId = scanId || instances[0]?.id || "";

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
      scanId: activeScanId,
      ids: assocIds,
      edgeTypes: edgeTypes.split(",").map((v) => v.trim()).filter(Boolean),
      maxDepth: depth,
      limit,
    },
    tab === "association" && !!activeScanId && !!assocIds,
  );

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

  return (
    <div className="space-y-6">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t("info.title")}</h1>
          <p className="mt-1 text-sm text-muted-foreground">{t("info.subtitle")}</p>
        </div>
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <Database className="size-3.5" />
          {t("info.coldPathHint")}
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
                  <InfoItemCard key={`${item.scan_id}:${item.id}`} item={item} onAssociate={associate} />
                ))}
              </div>
            )}
          </TabsContent>

          <TabsContent value="association" className="space-y-4">
            <Card className="rounded-md">
              <CardContent className="flex flex-wrap items-center gap-2 p-4">
                <Network className="size-4 text-muted-foreground" />
                {scanSelect}
                <Input className="h-9 min-w-64 flex-1" placeholder={t("info.association.idsPlaceholder")} value={assocIds} onChange={(e) => setAssocIds(e.target.value)} />
                <Input className="h-9 w-56" value={edgeTypes} onChange={(e) => setEdgeTypes(e.target.value)} aria-label={t("info.association.edgeTypes")} />
                <Select value={String(depth)} onValueChange={(v) => setDepth(Number(v))}>
                  <SelectTrigger className="h-9 w-24"><SelectValue /></SelectTrigger>
                  <SelectContent>{[1, 2, 3].map((n) => <SelectItem key={n} value={String(n)}>d{n}</SelectItem>)}</SelectContent>
                </Select>
              </CardContent>
            </Card>
            {!activeScanId || !assocIds ? (
              <EmptyState title={t("info.association.empty.title")} description={t("info.association.empty.description")} />
            ) : association.isLoading ? <Loading /> : association.error ? (
              <QueryError error={association.error} onRetry={() => association.refetch()} />
            ) : !association.data?.available ? <DisabledState /> : association.data.hits.length === 0 ? (
              <EmptyState title={t("info.association.empty.title")} description={t("info.association.empty.description")} />
            ) : (
              <div className="space-y-3">
                <Notice truncated={association.data.truncated} incomplete={association.data.incomplete} gapReason={association.data.gap_reason} />
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
