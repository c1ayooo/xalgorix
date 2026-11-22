import { Shield, ShieldAlert } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState, ErrorState } from "@/components/states";
import { RunTraffic } from "@/components/run-traffic";
import { useScanSecurity } from "@/api/queries";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";

/**
 * Run 安全链视图（GET /api/scans/{id}/security，REDESIGN D11 后续）：
 * ①内置守卫规则目录（静态，id 由前端本地化）②本次扫描的守卫拦截事件
 * （按守卫分类，journal 投影）③流量记录（复用 RunTraffic 的代理存档）。
 * 命令历史在「事件」tab，不在此重复。
 */

// 内置守卫目录：layer = 宪章 §2 安全链归属（network/fs/process/agent）。
// id 与后端拦截事件的 category 对齐（scopeguard 等同名）。
const GUARD_CATALOG: { id: string; layer: string }[] = [
  { id: "scopeguard", layer: "network" },
  { id: "dialguard", layer: "network" },
  { id: "requestguard", layer: "network" },
  { id: "egress", layer: "network" },
  { id: "sandbox", layer: "fs" },
  { id: "secrets", layer: "process" },
  { id: "target_scope", layer: "agent" },
  { id: "phase", layer: "agent" },
  { id: "auth", layer: "agent" },
  { id: "activity", layer: "agent" },
  { id: "benchmark", layer: "agent" },
  { id: "goal", layer: "agent" },
  { id: "ledger", layer: "agent" },
  { id: "report", layer: "agent" },
];

const LAYER_LABEL_KEY: Record<string, string> = {
  network: "scanDetail.security.layer.network",
  fs: "scanDetail.security.layer.fs",
  process: "scanDetail.security.layer.process",
  agent: "scanDetail.security.layer.agent",
};

export function SecurityTab({ scanId, running }: { scanId: string; running: boolean }) {
  const { t } = useI18n();
  const { data, isLoading, error } = useScanSecurity(scanId, running);

  if (isLoading) {
    return (
      <div className="space-y-2">
        <Skeleton className="h-4 w-64" />
        <Skeleton className="h-40 w-full" />
      </div>
    );
  }
  if (error) {
    return (
      <ErrorState
        title={t("scanDetail.security.error")}
        description={error instanceof Error ? error.message : ""}
      />
    );
  }
  if (!data) return null;

  const blocks = data.blocks ?? [];
  const counters = data.counters ?? {};

  return (
    <div className="space-y-3">
      <p className="text-xs text-muted-foreground">{t("scanDetail.security.subtitle")}</p>

      {/* 内置守卫规则目录 */}
      <div>
        <div className="mb-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {t("scanDetail.security.guardsTitle")}
        </div>
        <div className="grid gap-1.5 sm:grid-cols-2">
          {GUARD_CATALOG.map((g) => (
            <GuardCard key={g.id} guard={g} count={counters[g.id] ?? 0} />
          ))}
        </div>
      </div>

      {/* 拦截事件 */}
      <div>
        <div className="mb-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {t("scanDetail.security.blocks")}
        </div>
        {blocks.length === 0 ? (
          <EmptyState
            icon={<Shield className="h-8 w-8 text-muted-foreground/40" />}
            title={t("scanDetail.security.blocksEmpty")}
          />
        ) : (
          <div className="space-y-1">
            {blocks.map((b, idx) => (
              <div
                key={`${b.ts}-${idx}`}
                className="flex items-start gap-2 rounded-md border border-border bg-card px-2.5 py-1.5"
              >
                <span className="font-mono text-[11px] tabular-nums text-muted-foreground">
                  {(b.ts || "").slice(11, 19)}
                </span>
                <Badge variant="destructive" className="shrink-0 font-mono text-[10px]">
                  {t(`scanDetail.security.cat.${b.category}`)}
                </Badge>
                {b.tool && (
                  <span className="font-mono text-[11px] text-muted-foreground">{b.tool}</span>
                )}
                <span className="min-w-0 flex-1 break-all font-mono text-[11px]">{b.reason}</span>
              </div>
            ))}
            {data.truncated && (
              <p className="text-[11px] text-muted-foreground">
                {t("scanDetail.security.truncated")}
              </p>
            )}
          </div>
        )}
      </div>

      {/* 流量记录：代理存档的原始 HTTP 交互 */}
      <div>
        <div className="mb-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {t("scanDetail.security.trafficTitle")}
        </div>
        <Card>
          <CardContent className="p-3">
            <div className="mb-2 flex items-center gap-1.5 text-xs text-muted-foreground">
              <ShieldAlert className="h-3.5 w-3.5" />
              {t("scanDetail.security.trafficNote")}
            </div>
            <RunTraffic scanId={scanId} />
          </CardContent>
        </Card>
      </div>

      <p className="text-[11px] text-muted-foreground">
        {t("scanDetail.security.commandsNote")}
      </p>
    </div>
  );
}

function GuardCard({ guard, count }: { guard: { id: string; layer: string }; count: number }) {
  const { t } = useI18n();
  const active = count > 0;
  return (
    <div
      className={cn(
        "rounded-md border bg-card px-2.5 py-2",
        active ? "border-destructive/30 bg-destructive/5" : "border-border",
      )}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs font-medium">{t(`scanDetail.security.guard.${guard.id}`)}</span>
        <Badge
          variant="outline"
          className={cn(
            "font-mono text-[10px]",
            active && "border-destructive/30 text-destructive",
          )}
        >
          {count}
        </Badge>
      </div>
      <p className="mt-0.5 line-clamp-2 text-[11px] leading-snug text-muted-foreground">
        {t(`scanDetail.security.guardDetail.${guard.id}`)}
      </p>
      <span className="mt-1 inline-block text-[10px] uppercase tracking-wide text-muted-foreground/70">
        {t(LAYER_LABEL_KEY[guard.layer] ?? "scanDetail.security.layer.agent")}
      </span>
    </div>
  );
}
