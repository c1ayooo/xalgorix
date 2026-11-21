import { Network } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { useI18n } from "@/i18n";
import type { InfoItem } from "@/types/api";

/**
 * One InfoItem in the information space. Observed items use the sky semantic
 * chip; inferred items use violet. Confidence, coordinates and the source scan
 * stay mono because they are machine data.
 */
export function InfoItemCard({
  item,
  onAssociate,
}: {
  item: InfoItem;
  onAssociate?: (item: InfoItem) => void;
}) {
  const { t } = useI18n();
  const observed = item.nature === "observed";
  return (
    <Card className="rounded-md">
      <CardContent className="space-y-3 p-4">
        <div className="flex flex-wrap items-center gap-2">
          <Badge
            variant="outline"
            className={
              observed
                ? "border-sky-500/30 bg-sky-500/10 text-sky-600 dark:text-sky-400"
                : "border-violet-500/30 bg-violet-500/10 text-violet-600 dark:text-violet-400"
            }
          >
            <span
              aria-hidden
              className={
                observed
                  ? "mr-1.5 size-1.5 rounded-full bg-sky-500"
                  : "mr-1.5 size-1.5 rounded-full bg-violet-500"
              }
            />
            {t(observed ? "info.nature.observed" : "info.nature.inferred")}
          </Badge>
          <span className="font-mono text-[11px] text-muted-foreground">
            {item.scan_id}
          </span>
          {typeof item.confidence === "number" && (
            <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
              {t("info.confidence")} {(item.confidence * 100).toFixed(0)}%
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
