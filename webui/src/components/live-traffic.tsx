import { useMemo, useState } from "react";
import { useQueries } from "@tanstack/react-query";
import { ChevronRight, ShieldCheck } from "lucide-react";

import { api } from "@/api/client";
import { timeAgo, cn } from "@/lib/utils";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/states";
import { useI18n } from "@/i18n";
import type { HttpExchange } from "@/types/api";

// 实时动态页的 HTTP 视图：聚合所有活跃/最近运行（含已结束）的 MITM 代理流量归档
// （GET /api/scans/{id}/traffic），按时间倒序展示，展开即请求/响应 wire 包原文。
//
// 资产绑定：每条流量带后端派生的 `host`，与 run 的资产集合（target / 父域 /
// 已发现子域 sub_scans[].target）比对命中后打「资产」徽标——这正是流量可以
// 作为该资产漏洞验证证据的判据，避免把 A 资产的包当成 B 资产的证据。
const TRAFFIC_LIMIT = 50;
const LIVE_POLL_MS = 5000;

/** 流量面板只需要的最小运行视图：实时实例与重启后从扫描列表重建的运行共用。 */
export interface TrafficRun {
  id: string;
  name?: string;
  targets?: string;
  status: string;
  started_at?: string;
  parent_target?: string;
  sub_scans?: { target: string }[];
}

function isActiveStatus(status?: string): boolean {
  const s = (status || "").toLowerCase();
  return s === "running" || s === "pending" || s === "paused";
}

export function LiveTraffic({
  instances,
  projectName,
}: {
  instances: TrafficRun[];
  projectName?: string;
}) {
  const { t } = useI18n();
  const trafficQueries = useQueries({
    queries: instances.map((inst) => ({
      queryKey: ["scan", inst.id, "traffic", "", "", "", "", 0, TRAFFIC_LIMIT] as const,
      queryFn: () => api.scanTraffic(inst.id, { limit: TRAFFIC_LIMIT }),
      // Active runs stream; a finished run's archive is immutable, so polling
      // it would only burn IO.
      refetchInterval: isActiveStatus(inst.status) ? LIVE_POLL_MS : false,
      staleTime: 3000,
      retry: false,
    })),
  });

  const assetsByInstance = useMemo(() => {
    const map = new Map<string, Set<string>>();
    for (const inst of instances) map.set(inst.id, assetHosts(inst));
    return map;
  }, [instances]);

  const rows = useMemo(() => {
    const out: { ex: HttpExchange; instance: TrafficRun; asset: boolean }[] = [];
    trafficQueries.forEach((query, index) => {
      const inst = instances[index];
      if (!inst) return;
      const assets = assetsByInstance.get(inst.id) ?? new Set<string>();
      for (const ex of query.data?.exchanges ?? []) {
        out.push({ ex, instance: inst, asset: !!ex.host && assets.has(ex.host) });
      }
    });
    out.sort((a, b) => Date.parse(b.ex.at) - Date.parse(a.ex.at));
    return out;
  }, [assetsByInstance, instances, trafficQueries]);

  const loading = trafficQueries.some((q) => q.isLoading);
  const anyError = trafficQueries.some((q) => q.isError);
  const anyPresent = trafficQueries.some((q) => q.data?.present);

  if (instances.length === 0) {
    return (
      <EmptyState
        className="m-3"
        title={t("live.http.empty.title")}
        description={
          projectName
            ? t("live.http.noRunsInProject").replace("%s", projectName)
            : t("live.http.noRuns")
        }
      />
    );
  }

  if (loading && rows.length === 0) {
    return (
      <div className="space-y-2 p-4">
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-8 w-4/5" />
        <Skeleton className="h-8 w-3/5" />
      </div>
    );
  }

  if (rows.length === 0) {
    return (
      <EmptyState
        className="m-3"
        title={t("live.http.empty.title")}
        description={
          anyError
            ? t("live.http.error")
            : anyPresent
              ? t("live.http.empty.description")
              : t("live.http.absent")
        }
      />
    );
  }

  const assetCount = rows.filter((row) => row.asset).length;

  return (
    <div className="divide-y divide-border">
      <div className="flex flex-wrap items-center gap-3 px-3 py-1.5 text-[11px] text-muted-foreground mono">
        <span>{t("live.http.swaps").replace("%d", String(rows.length))}</span>
        <span>{t("live.http.assets").replace("%d", String(assetCount))}</span>
        <span className="ml-auto">{t("live.http.hint")}</span>
      </div>
      {rows.map(({ ex, instance, asset }) => (
        <TrafficRow
          key={`${instance.id}:${ex.session ?? ""}:${ex.seq}`}
          exchange={ex}
          runName={instance.name || (instance.targets ?? "").split(",")[0] || instance.id}
          runStatus={instance.status}
          asset={asset}
        />
      ))}
    </div>
  );
}

function TrafficRow({
  exchange,
  runName,
  runStatus,
  asset,
}: {
  exchange: HttpExchange;
  runName: string;
  runStatus?: string;
  asset: boolean;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const hasRaw = !!(exchange.request_raw || exchange.response_raw);

  return (
    <div>
      <button
        type="button"
        onClick={() => hasRaw && setOpen((v) => !v)}
        disabled={!hasRaw}
        aria-expanded={hasRaw ? open : undefined}
        className="flex w-full min-w-0 flex-wrap items-center gap-2 px-3 py-2 text-left text-xs hover:bg-accent/40 disabled:cursor-default"
      >
        {hasRaw && (
          <ChevronRight
            className={cn(
              "h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform",
              open && "rotate-90",
            )}
            aria-hidden
          />
        )}
        <span className="shrink-0 text-[11px] text-muted-foreground">
          {timeAgo(exchange.at)}
        </span>
        <span className="mono shrink-0 font-medium text-foreground">{exchange.method}</span>
        {exchange.host && (
          <span
            className={cn(
              "mono flex shrink-0 items-center gap-1 rounded-sm border px-1.5 py-0.5 text-[10px]",
              asset
                ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300"
                : "border-border text-muted-foreground",
            )}
          >
            {asset && <ShieldCheck className="h-3 w-3" aria-hidden />}
            {exchange.host}
          </span>
        )}
        <span className="mono min-w-0 flex-1 truncate text-foreground/90">{exchange.url}</span>
        {typeof exchange.status === "number" && (
          <span
            className={cn(
              "mono shrink-0",
              exchange.denied
                ? "text-destructive"
                : exchange.status < 400
                  ? "text-emerald-600 dark:text-emerald-400"
                  : "text-amber-600 dark:text-amber-400",
            )}
          >
            {exchange.status}
          </span>
        )}
        <span className="mono shrink-0 max-w-[18ch] truncate text-[11px] text-muted-foreground">
          {runName}
        </span>
        {runStatus && runStatus !== "running" && (
          <span className="mono shrink-0 rounded-sm border border-border px-1 py-0.5 text-[10px] text-muted-foreground">
            {runStatus}
          </span>
        )}
      </button>
      {open && hasRaw && (
        <div className="min-w-0 space-y-2 px-3 pb-3">
          {exchange.request_raw && (
            <div className="space-y-1">
              <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
                {t("live.http.request")}
              </div>
              <pre className="max-h-64 min-w-0 max-w-full overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted p-3 text-xs leading-relaxed text-foreground [overflow-wrap:anywhere]">
                <code>
                  {exchange.request_raw}
                  {exchange.request_truncated ? `\n${t("findings.http.truncated")}` : ""}
                </code>
              </pre>
            </div>
          )}
          {exchange.response_raw && (
            <div className="space-y-1">
              <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
                {t("live.http.response")}
              </div>
              <pre className="max-h-64 min-w-0 max-w-full overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted p-3 text-xs leading-relaxed text-foreground [overflow-wrap:anywhere]">
                <code>
                  {exchange.response_raw}
                  {exchange.response_truncated ? `\n${t("findings.http.truncated")}` : ""}
                </code>
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

/** 一个 run 的资产 host 集合：目标、父域、已发现子域（资产发现的落地面）。 */
function assetHosts(inst: TrafficRun): Set<string> {
  const hosts = new Set<string>();
  const add = (raw?: string) => {
    if (!raw) return;
    const host = normalizeHost(raw);
    if (host) hosts.add(host);
  };
  add(inst.parent_target);
  for (const raw of (inst.targets ?? "").split(",")) add(raw);
  for (const sub of inst.sub_scans ?? []) add(sub.target);
  return hosts;
}

function normalizeHost(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed) return "";
  try {
    const parsed = new URL(
      trimmed.includes("://") ? trimmed : `https://${trimmed}`,
    );
    return parsed.hostname.toLowerCase();
  } catch {
    return "";
  }
}
