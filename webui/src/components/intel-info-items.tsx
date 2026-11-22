import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Database, ExternalLink, Network } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/states";
import { ColdPathBadge } from "@/components/coldpath-badge";
import { InfoItemCard } from "@/components/info-item-card";
import { AssociationGraph } from "@/components/association-graph";
import {
  useColdPathSync,
  useInfoAssociation,
  useInfoRegion,
} from "@/api/queries";
import { HttpError } from "@/api/client";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type { InfoItem, InfoNature } from "@/types/api";

/**
 * 任务级信息空间切片（CORE §4 人工审计视图消费者）：该任务全部 run 的
 * InfoItem（区域点亮）+ 以任一信息项为根的关联图（关联点亮，跳数/预算
 * 沿用后端上限）。数据走冷路径，无 DSN 时 503 reason=cold_path_disabled
 * 如实说明——503 不是空数组。完整语境切片在项目级 /info 页，这里深链。
 */

const EDGE_TYPES = ["supports", "contradicts", "references", "same_session"];

function InfoNotice({
  truncated,
  incomplete,
  gapReason,
}: {
  truncated: boolean;
  incomplete: boolean;
  gapReason?: string;
}) {
  const { t } = useI18n();
  if (!truncated && !incomplete) return null;
  return (
    <div className="rounded-sm border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">
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

function InfoDisabled() {
  const { t } = useI18n();
  return (
    <EmptyState
      icon={<Database className="h-6 w-6" />}
      title={t("info.disabled.title")}
      description={t("info.disabled.description")}
    />
  );
}

function InfoError({
  error,
  onRetry,
}: {
  error: unknown;
  onRetry: () => void;
}) {
  const { t } = useI18n();
  if (error instanceof HttpError && error.status === 503) {
    return <InfoDisabled />;
  }
  return (
    <div className="rounded-sm border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs">
      <span>{error instanceof Error ? error.message : t("info.error.description")}</span>
      <button
        type="button"
        onClick={onRetry}
        className="ml-2 text-muted-foreground transition-colors hover:text-foreground"
      >
        {t("common.retry")}
      </button>
    </div>
  );
}

export function IntelInfoItems({
  taskId,
  projectId,
}: {
  taskId: string;
  projectId?: string;
}) {
  const { t } = useI18n();
  const [nature, setNature] = useState<InfoNature | "all">("all");
  const [seedId, setSeedId] = useState<string | null>(null);

  const region = useInfoRegion(
    useMemo(
      () => ({
        projectId: projectId ?? "",
        taskId,
        nature: nature === "all" ? ("" as const) : nature,
        limit: 50,
      }),
      [projectId, taskId, nature],
    ),
    !!projectId,
  );

  // 关联点亮：以选中信息项为根的受限遍历（深度 ≤3 由后端钳制）。
  const association = useInfoAssociation(
    {
      projectId: projectId ?? "",
      taskId,
      ids: seedId ?? "",
      edgeTypes: EDGE_TYPES,
      maxDepth: 2,
      limit: 50,
    },
    !!projectId && !!seedId,
  );

  // 最新 run 的冷路径徽标——切片是跨 run 的，徽标只标注同步健康示例；
  // 逐 run 状态在运行 tab 与账本 tab。
  const latestScanId = region.data?.items[0]?.scan_id;
  const coldpath = useColdPathSync(latestScanId ?? "", !!latestScanId);

  function associate(item: InfoItem) {
    setSeedId(item.id);
  }

  const items = region.data?.items ?? [];

  return (
    <div className="space-y-3">
      {/* 区块头平级渲染（账本同款），InfoItemCard 自带边框——禁止卡中卡。 */}
      <div className="flex flex-wrap items-center gap-2">
        <Database className="size-4 text-muted-foreground" aria-hidden />
        <span className="text-sm font-medium">{t("intel.infoItems.title")}</span>
        <span className="font-mono text-xs tabular-nums text-muted-foreground">
          {items.length}
        </span>
        {coldpath.data && !coldpath.error && (
          <ColdPathBadge state={coldpath.data} />
        )}
        <Link
          to="/info?tab=region"
          className="ml-auto inline-flex items-center gap-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
        >
          <ExternalLink className="size-3" aria-hidden />
          {t("intel.infoItems.openSpace")}
        </Link>
      </div>

      {/* nature 过滤 chips（区域点亮的确定性过滤，不引入新语义）。 */}
      <div className="flex flex-wrap items-center gap-2">
        {(["all", "observed", "inferred"] as const).map((n) => (
          <button
            key={n}
            type="button"
            onClick={() => setNature(n)}
            className={cn(
              "rounded-full border px-2.5 py-0.5 text-xs transition-colors",
              nature === n
                ? "border-foreground bg-foreground text-background"
                : "border-border text-muted-foreground hover:text-foreground",
            )}
          >
            {n === "all"
              ? t("info.filter.allNature")
              : t(`info.nature.${n}`)}
          </button>
        ))}
      </div>

      {region.isLoading && !region.data ? (
        <Skeleton className="h-32 w-full" />
      ) : region.error ? (
        <InfoError error={region.error} onRetry={() => region.refetch()} />
      ) : !region.data?.available ? (
        <InfoDisabled />
      ) : items.length === 0 ? (
        <EmptyState
          title={t("intel.infoItems.emptyTitle")}
          description={t("intel.infoItems.emptyDescription")}
        />
      ) : (
        <div className="space-y-3">
          <InfoNotice
            truncated={region.data.truncated}
            incomplete={region.data.incomplete}
            gapReason={region.data.gap_reason}
          />
          {items.map((item) => (
            <InfoItemCard
              key={`${item.scan_id}:${item.id}`}
              item={item}
              onAssociate={associate}
            />
          ))}
        </div>
      )}

      {seedId && (
        <div className="space-y-3 border-t border-border pt-3">
          <div className="flex flex-wrap items-center gap-2">
            <Network className="size-4 text-muted-foreground" aria-hidden />
            <span className="text-sm font-medium">
              {t("intel.infoItems.associationTitle")}
            </span>
            <span className="font-mono text-xs text-muted-foreground">
              {seedId}
            </span>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="ml-auto h-7 px-2 text-xs text-muted-foreground"
              onClick={() => setSeedId(null)}
            >
              {t("intel.infoItems.associationClose")}
            </Button>
          </div>
          {association.isLoading && !association.data ? (
            <Skeleton className="h-64 w-full" />
          ) : association.error ? (
            <InfoError
              error={association.error}
              onRetry={() => association.refetch()}
            />
          ) : !association.data?.available ? (
            <InfoDisabled />
          ) : association.data.hits.length === 0 ? (
            <EmptyState
              title={t("intel.infoItems.assocEmptyTitle")}
              description={t("intel.infoItems.assocEmptyDescription")}
            />
          ) : (
            <div className="space-y-3">
              <InfoNotice
                truncated={association.data.truncated}
                incomplete={association.data.incomplete}
                gapReason={association.data.gap_reason}
              />
              <AssociationGraph hits={association.data.hits} />
            </div>
          )}
        </div>
      )}
    </div>
  );
}
