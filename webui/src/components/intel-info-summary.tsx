import { useMemo } from "react";
import { Database, ExternalLink } from "lucide-react";
import { Link } from "react-router-dom";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { InfoItemCard } from "@/components/info-item-card";
import {
  InfoDisabled,
  InfoNotice,
} from "@/components/info-views/common";
import { useInfoRegion } from "@/api/queries";
import { useI18n } from "@/i18n";

/**
 * Task-scoped InfoItem summary. The information space itself lives at `/info`
 * (scope bar + subject rail + three views); duplicating the full region table
 * and association graph here made two implementations drift apart, so this tab
 * keeps counts + the latest few items and deep-links with the task scope.
 *
 * InfoItem (cold path) and intelligence records (tool observations, triaged
 * separately below) are different objects and stay in separate blocks.
 */
export function IntelInfoSummary({
  taskId,
  projectId,
}: {
  taskId: string;
  projectId?: string;
}) {
  const { t } = useI18n();
  const region = useInfoRegion(
    useMemo(
      () => ({ projectId: projectId ?? "", taskId, limit: 12 }),
      [projectId, taskId],
    ),
    !!projectId,
  );

  const items = region.data?.items ?? [];
  const observed = items.filter((item) => item.nature === "observed").length;
  const inferred = items.length - observed;
  const spaceHref = `/info?task=${encodeURIComponent(taskId)}${
    projectId ? `&project=${encodeURIComponent(projectId)}` : ""
  }`;

  return (
    <section className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <Database className="size-4 text-muted-foreground" aria-hidden />
        <span className="text-sm font-medium">{t("intel.infoItems.title")}</span>
        <Badge variant="outline" className="gap-1">
          <span className="font-mono tabular-nums">{items.length}</span>
        </Badge>
        <Badge variant="outline" className="gap-1">
          {t("info.nature.observed")}
          <span className="font-mono tabular-nums">{observed}</span>
        </Badge>
        <Badge variant="outline" className="gap-1">
          {t("info.nature.inferred")}
          <span className="font-mono tabular-nums">{inferred}</span>
        </Badge>
        <Link
          to={spaceHref}
          className="ml-auto inline-flex items-center gap-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
        >
          <ExternalLink className="size-3" aria-hidden />
          {t("intel.infoItems.openSpace")}
        </Link>
      </div>

      {region.isLoading && !region.data ? (
        <Skeleton className="h-20 w-full" />
      ) : region.error ? (
        <p className="rounded-sm border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs">
          {region.error instanceof Error
            ? region.error.message
            : t("info.error.description")}
        </p>
      ) : !region.data?.available ? (
        <InfoDisabled />
      ) : items.length === 0 ? (
        <p className="text-[11px] text-muted-foreground">
          {t("intel.infoItems.emptyDescription")}
        </p>
      ) : (
        <>
          <InfoNotice
            truncated={region.data.truncated}
            incomplete={region.data.incomplete}
            gapReason={region.data.gap_reason}
          />
          {/* 紧凑摘要卡（InfoItemCard）：七维完整视图只在 /info 行展开里。 */}
          <div className="space-y-2">
            {items.slice(0, 3).map((item) => (
              <InfoItemCard key={`${item.scan_id}:${item.id}`} item={item} />
            ))}
          </div>
          {items.length > 3 && (
            <Link
              to={spaceHref}
              className="inline-flex items-center gap-1 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
            >
              {t("intel.infoItems.more")}
              <ExternalLink className="size-3" aria-hidden />
            </Link>
          )}
        </>
      )}
    </section>
  );
}
