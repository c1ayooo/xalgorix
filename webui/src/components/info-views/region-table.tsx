import { Fragment, useMemo, useState } from "react";
import { ChevronDown, ChevronRight, Network } from "lucide-react";
import { InfoSourceChip } from "@/components/info-source-chip";
import { InfoItemDetail } from "@/components/info-item-detail";
import { NatureBadge } from "@/components/nature-badge";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type { InfoItem } from "@/types/api";

const COLUMNS = 8;

/**
 * Region lighting as a dense table (UI_DESIGN §7: handwritten <table>, columns
 * drop right-to-left on narrow viewports). One row is one InfoItem; expanding
 * it renders the full seven-dimension view whose "projected hypotheses" block
 * closes the InfoItem↔Evidence loop.
 */
export function RegionTable({
  items,
  focusedId,
  onSeed,
  onSelectSubject,
}: {
  items: InfoItem[];
  focusedId?: string | null;
  /** Start an association traversal from this InfoItem id. */
  onSeed: (id: string) => void;
  onSelectSubject: (identifier: string, kind: string) => void;
}) {
  const { t } = useI18n();
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const rows = useMemo(
    () => items.map((item) => ({ item, key: `${item.scan_id}:${item.id}` })),
    [items],
  );

  const toggle = (key: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });

  return (
    <div className="overflow-x-auto rounded-md border border-border bg-card">
      <table className="w-full min-w-[640px] border-collapse text-sm">
        <thead>
          <tr className="border-b border-border text-left">
            <th className="px-3 py-2 text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
              {t("region.colNature")}
            </th>
            <th className="px-3 py-2 text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
              {t("region.colContent")}
            </th>
            <th className="px-3 py-2 text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
              {t("info.subject")}
            </th>
            <th className="hidden px-3 py-2 text-[10px] font-medium uppercase tracking-wider text-muted-foreground lg:table-cell">
              {t("info.purpose")}
            </th>
            <th className="hidden px-3 py-2 text-[10px] font-medium uppercase tracking-wider text-muted-foreground md:table-cell">
              {t("info.detail.source")}
            </th>
            <th className="px-3 py-2 text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
              {t("info.observedAt")}
            </th>
            <th className="hidden px-3 py-2 text-right text-[10px] font-medium uppercase tracking-wider text-muted-foreground xl:table-cell">
              {t("region.colRelations")}
            </th>
            <th className="w-8 px-1 py-2" />
          </tr>
        </thead>
        <tbody>
          {rows.map(({ item, key }) => {
            const open = expanded.has(key);
            const focused = focusedId === item.id;
            const relationCount = item.coordinates.relation_ids?.length ?? 0;
            return (
              <Fragment key={key}>
                <tr
                  className={cn(
                    "border-b border-border transition-colors hover:bg-accent/30",
                    focused && "bg-accent/40",
                  )}
                >
                  <td className="px-3 py-2 align-top">
                    <NatureBadge nature={item.nature} size="xs" />
                  </td>
                  <td className="max-w-[22rem] px-3 py-2 align-top">
                    <span className="line-clamp-2 text-xs text-foreground">
                      {item.coordinates.content.text}
                    </span>
                    <span className="mt-0.5 block font-mono text-[10px] text-muted-foreground">
                      {item.scan_id}
                    </span>
                  </td>
                  <td className="max-w-[14rem] px-3 py-2 align-top">
                    <button
                      type="button"
                      onClick={() =>
                        onSelectSubject(
                          item.coordinates.subject.identifier,
                          item.coordinates.subject.kind ?? "",
                        )
                      }
                      className="block max-w-full truncate text-left font-mono text-[11px] text-foreground transition-colors hover:text-primary"
                      title={t("region.filterBySubject")}
                    >
                      {item.coordinates.subject.identifier || "—"}
                    </button>
                  </td>
                  <td className="hidden max-w-[16rem] px-3 py-2 align-top text-xs text-muted-foreground lg:table-cell">
                    <span className="line-clamp-1">
                      {item.coordinates.purpose || "—"}
                    </span>
                  </td>
                  <td className="hidden px-3 py-2 align-top md:table-cell">
                    <InfoSourceChip source={item.coordinates.source} />
                  </td>
                  <td className="whitespace-nowrap px-3 py-2 align-top font-mono text-[11px] tabular-nums text-muted-foreground">
                    {item.coordinates.observed_at
                      ? new Date(item.coordinates.observed_at).toLocaleString()
                      : "—"}
                  </td>
                  <td className="hidden px-3 py-2 align-top text-right xl:table-cell">
                    <span className="inline-flex items-center gap-1 font-mono text-[10px] tabular-nums text-muted-foreground">
                      <Network className="size-3" aria-hidden />
                      {relationCount}
                    </span>
                  </td>
                  <td className="px-1 py-2 align-top">
                    <button
                      type="button"
                      onClick={() => toggle(key)}
                      aria-expanded={open}
                      aria-controls={`info-detail-${key}`}
                      aria-label={t(
                        open ? "region.collapseRow" : "region.expandRow",
                      )}
                      className="flex size-6 items-center justify-center rounded-sm text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground"
                    >
                      {open ? (
                        <ChevronDown className="size-3.5" aria-hidden />
                      ) : (
                        <ChevronRight className="size-3.5" aria-hidden />
                      )}
                    </button>
                  </td>
                </tr>
                {open && (
                  <tr className="border-b border-border">
                    <td colSpan={COLUMNS} id={`info-detail-${key}`} className="bg-muted/20 p-3">
                      <div className="border-l-2 border-border pl-3">
                        <InfoItemDetail item={item} onSelectRelation={onSeed} />
                      </div>
                    </td>
                  </tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
