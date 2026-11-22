import { Network } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { InfoSourceChip } from "@/components/info-source-chip";
import { NatureBadge } from "@/components/nature-badge";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type { InfoItem } from "@/types/api";

/**
 * Compact InfoItem summary (task-scoped摘要 + any one-line context). The full
 * seven-dimension view lives in `info-item-detail`; this card deliberately
 * stays scannable. nature/source colours come from their single sources so no
 * surface repeats literal sky/violet/emerald classes.
 */
export function InfoItemCard({
  item,
  onAssociate,
  focused = false,
}: {
  item: InfoItem;
  onAssociate?: (item: InfoItem) => void;
  focused?: boolean;
}) {
  const { t } = useI18n();
  return (
    <Card className={cn("rounded-md", focused && "ring-1 ring-ring/40")}>
      <CardContent className="space-y-3 p-4">
        <div className="flex flex-wrap items-center gap-2">
          <NatureBadge nature={item.nature} />
          <InfoSourceChip source={item.coordinates.source} />
          <span className="font-mono text-[11px] text-muted-foreground">
            {item.scan_id}
          </span>
          {typeof item.confidence === "number" && item.confidence > 0 && (
            <span
              className="font-mono text-[11px] text-muted-foreground tabular-nums"
              title={t("info.detail.coordinateConfidence")}
            >
              {t("info.detail.coordinateConfidence")}{" "}
              {(item.confidence * 100).toFixed(0)}%
            </span>
          )}
          {item.coordinate_status && (
            <span className="font-mono text-[10px] uppercase tracking-wide text-muted-foreground">
              {item.coordinate_status}
            </span>
          )}
          {onAssociate && (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="ml-auto h-7 px-2 text-xs"
              onClick={() => onAssociate(item)}
            >
              <Network className="size-3.5" />
              {t("info.associate.action")}
            </Button>
          )}
        </div>

        <p className="text-sm leading-6 text-foreground">
          {item.coordinates.content.text}
        </p>

        <div className="grid grid-cols-1 gap-1.5 text-xs sm:grid-cols-3">
          <div className="min-w-0">
            <span className="uppercase tracking-wide text-muted-foreground">
              {t("info.subject")}
            </span>
            <p className="truncate font-mono text-[11px] text-foreground">
              {item.coordinates.subject.identifier || "—"}
            </p>
          </div>
          <div className="min-w-0">
            <span className="uppercase tracking-wide text-muted-foreground">
              {t("info.purpose")}
            </span>
            <p className="truncate text-[11px] text-foreground">
              {item.coordinates.purpose || "—"}
            </p>
          </div>
          <div className="min-w-0">
            <span className="uppercase tracking-wide text-muted-foreground">
              {t("info.observedAt")}
            </span>
            <p className="truncate font-mono text-[11px] text-foreground tabular-nums">
              {item.coordinates.observed_at}
            </p>
          </div>
        </div>

        {item.supports && item.supports.length > 0 && (
          <p className="text-[11px] text-muted-foreground">
            {t("info.supports")}{" "}
            <span className="font-mono">{item.supports.join(", ")}</span>
          </p>
        )}
      </CardContent>
    </Card>
  );
}
