import { useMemo } from "react";
import { Link } from "react-router-dom";
import { ExternalLink } from "lucide-react";

import { SeverityBadge } from "@/components/severity-badge";
import { EmptyState, ErrorState } from "@/components/states";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { useFindingsList } from "@/api/queries";
import { dedupFindings, findingTime, type FlatFinding } from "@/lib/findings";
import { timeAgo } from "@/lib/utils";
import { useI18n } from "@/i18n";

/**
 * 实时动态页的「发现」视图。
 *
 * 数据源是项目级发现投影（GET /api/findings?project_id=），不是事件流里内嵌的
 * `vulns` 快照。这一条是刻意的：事件流只承载「被广播的」发现——受
 * severity_filter 广播门、活跃 run 状态、实例 500 条内存缓冲三重裁剪——
 * 因此它和任务详情 › 发现必然对不上。项目级投影读的是落盘 scan.json 全量，
 * 覆盖已结束的 run、包含低于广播阈值的发现，并由服务端按同一身份键去重。
 *
 * 口径（当前项目 / 含已结束运行 / 服务端去重）必须显示在表头，避免操作员把
 * 这里的条数当成「事件流里看到的那些」。
 */
export function LiveFindings({ projectId }: { projectId: string | null }) {
  const { t } = useI18n();
  const query = useFindingsList(projectId ?? undefined, !!projectId);

  // 服务端已去重排序；dedupFindings 幂等，保留客户端兜底（旧投影/分页拼接）。
  const findings = useMemo<FlatFinding[]>(
    () => dedupFindings(query.data ?? []),
    [query.data],
  );

  if (!projectId) {
    return (
      <EmptyState
        className="m-3"
        title={t("live.findings.noProject.title")}
        description={t("live.findings.noProject.description")}
      />
    );
  }
  if (query.error) {
    return (
      <ErrorState
        className="m-3"
        title={t("live.findings.error")}
        description={query.error instanceof Error ? query.error.message : ""}
        action={
          <Button size="sm" variant="outline" onClick={() => query.refetch()}>
            {t("common.retry")}
          </Button>
        }
      />
    );
  }
  if (query.isLoading && !query.data) {
    return (
      <div className="space-y-2 p-4">
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-8 w-4/5" />
        <Skeleton className="h-8 w-3/5" />
      </div>
    );
  }
  if (findings.length === 0) {
    return (
      <EmptyState
        className="m-3"
        title={t("live.findings.empty.title")}
        description={t("live.findings.empty.description")}
      />
    );
  }

  const updated = query.dataUpdatedAt ? timeAgo(new Date(query.dataUpdatedAt).toISOString()) : "—";

  return (
    <div className="divide-y divide-border">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-1.5 text-[11px] text-muted-foreground mono">
        <span className="font-medium text-foreground">
          {t("live.findings.count").replace("%d", String(findings.length))}
        </span>
        <span>{t("live.findings.hint")}</span>
        <span className="ml-auto">
          {t("live.findings.updated").replace("%s", updated)}
        </span>
      </div>
      {findings.map((f) => (
        <Link
          key={`${f.scan_id}:${f.id}`}
          to={`/scans/${f.scan_id}`}
          className="flex min-w-0 items-start gap-3 px-3 py-2 transition-colors hover:bg-accent/40"
        >
          <SeverityBadge severity={f.severity} />
          <div className="min-w-0 flex-1">
            <p className="truncate text-xs font-medium text-foreground">{f.title}</p>
            <p className="mono truncate text-[11px] text-muted-foreground">
              {f.endpoint || f.scan_target}
            </p>
          </div>
          <span className="shrink-0 text-[11px] text-muted-foreground mono tabular-nums">
            {findingTime(f) ? timeAgo(findingTime(f)) : "—"}
          </span>
          <ExternalLink className="mt-0.5 size-3 shrink-0 text-muted-foreground" aria-hidden />
          <span className="sr-only">{t("live.findings.openRun")}</span>
        </Link>
      ))}
    </div>
  );
}
