import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import { stageOf, stageStyle } from "@/lib/stage-heat";
import { subjectKindLabel } from "@/lib/subject-kind";
import type { InfoHeatmapRow } from "@/types/api";

/**
 * Accessible fallback for the nebula canvas. Not the default view: the map is
 * the primary surface, and this list exists so keyboard/screen-reader users can
 * traverse the same subjects and open the same detail dialog.
 */
export function SubjectList({
  rows,
  selected,
  onSelect,
}: {
  rows: InfoHeatmapRow[];
  selected: { subject: string; subjectKind: string } | null;
  onSelect: (row: InfoHeatmapRow) => void;
}) {
  const { t } = useI18n();
  return (
    <div className="h-[clamp(420px,calc(100vh_-_19rem),900px)] overflow-auto rounded-md border border-border bg-card">
      <table className="w-full text-sm">
        <thead className="sticky top-0 z-10 bg-card text-[10px] uppercase tracking-wider text-muted-foreground">
          <tr className="border-b border-border">
            <th className="px-3 py-2 text-left">{t("info.subject")}</th>
            <th className="px-3 py-2 text-left">{t("info.map.kindColumn")}</th>
            <th className="px-3 py-2 text-left">{t("info.map.stageColumn")}</th>
            <th className="px-3 py-2 text-right">{t("info.heatmap.items")}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => {
            const style = stageStyle(stageOf(row.counts));
            const isSelected =
              selected?.subject === row.subject &&
              (selected?.subjectKind ?? "") === (row.subject_kind ?? "");
            return (
              <tr
                key={`${row.subject_kind ?? ""}\u0000${row.subject}`}
                className={cn(
                  "border-b border-border/60 last:border-0 hover:bg-accent/40",
                  isSelected && "bg-accent/50",
                )}
              >
                <td className="px-3 py-1.5">
                  <button
                    type="button"
                    className="block max-w-[34rem] truncate text-left font-mono text-[11px] hover:underline"
                    onClick={() => onSelect(row)}
                    title={row.subject}
                  >
                    {row.subject}
                  </button>
                </td>
                <td className="px-3 py-1.5 text-[11px] text-muted-foreground">
                  {subjectKindLabel(t, row.subject_kind ?? "")}
                </td>
                <td className="px-3 py-1.5">
                  <span className="inline-flex items-center gap-1 text-[10px]">
                    <span
                      className="size-2 rounded-full"
                      style={{ background: style.bg }}
                      aria-hidden
                    />
                    {t(style.labelKey)}
                  </span>
                </td>
                <td className="px-3 py-1.5 text-right font-mono text-[11px] tabular-nums">
                  {row.total}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
