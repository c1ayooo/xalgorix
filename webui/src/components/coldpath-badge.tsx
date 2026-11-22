import { useState } from "react";
import { ChevronDown } from "lucide-react";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type { ColdPathState, ColdPathSyncState } from "@/types/api";

/**
 * Cold-path sync badge (shared: /info header, ledger tab, runs list, overview).
 * Five states, one meaning each: disabled is the no-DSN normal state (gray, not
 * red) — only failed carries the destructive semantic. Counts stay mono/tabular
 * per the machine-data rule.
 *
 * The badge is also the drill-down entry: expanding it shows the watermark pair,
 * the last error and the update time, so a `gap` is never just two opaque
 * numbers (CORE §6.4: gaps must stay visible, not silently collapsed).
 */
const COLDPATH_BADGE: Record<
  ColdPathState,
  { className: string; dot: string }
> = {
  applied: {
    className:
      "border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
    dot: "bg-emerald-500",
  },
  pending: {
    className:
      "border-sky-500/30 bg-sky-500/10 text-sky-600 dark:text-sky-400",
    dot: "bg-sky-500",
  },
  gap: {
    className:
      "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300",
    dot: "bg-amber-500",
  },
  failed: {
    className:
      "border-red-500/30 bg-red-500/10 text-red-600 dark:text-red-400",
    dot: "bg-red-500",
  },
  disabled: {
    className: "border-border bg-muted text-muted-foreground",
    dot: "bg-muted-foreground/60",
  },
};

export function ColdPathBadge({
  state,
  expandable = true,
}: {
  state: ColdPathSyncState;
  /** Set false in very dense rows where the disclosure would break the line. */
  expandable?: boolean;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const meta = COLDPATH_BADGE[state.state] ?? COLDPATH_BADGE.disabled;
  const label = t(`info.coldpath.${state.state}`);

  const chip = (
    <>
      <span aria-hidden className={cn("size-1.5 rounded-full", meta.dot)} />
      {label}
      <span className="font-mono text-[10px] tabular-nums opacity-80">
        {state.applied_seq}/{state.source_seq}
      </span>
      {expandable && (
        <ChevronDown
          aria-hidden
          className={cn("size-3 transition-transform", open && "rotate-180")}
        />
      )}
    </>
  );

  return (
    <span className="inline-flex flex-col items-start">
      {expandable ? (
        <button
          type="button"
          onClick={() => setOpen((value) => !value)}
          aria-expanded={open}
          aria-label={`${label} ${state.applied_seq}/${state.source_seq}`}
          className={cn(
            "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-[11px] transition-colors hover:brightness-110",
            meta.className,
          )}
        >
          {chip}
        </button>
      ) : (
        <span
          className={cn(
            "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-[11px]",
            meta.className,
          )}
          aria-label={`${label} ${state.applied_seq}/${state.source_seq}`}
        >
          {chip}
        </span>
      )}

      {expandable && open && (
        <span className="mt-1 block min-w-56 space-y-0.5 rounded-sm border border-border bg-card px-2.5 py-1.5 text-left text-[10px] text-muted-foreground">
          <span className="block">
            {t("info.coldpath.detail.state")}{" "}
            <span className="font-mono text-foreground">{state.state}</span>
          </span>
          <span className="block">
            {t("info.coldpath.detail.watermark")}{" "}
            <span className="font-mono tabular-nums text-foreground">
              applied {state.applied_seq} / source {state.source_seq}
            </span>
          </span>
          <span className="block">
            {t("info.coldpath.detail.endpoint")}{" "}
            <span className="font-mono text-foreground">{state.cold_path}</span>
          </span>
          {state.updated_at && (
            <span className="block">
              {t("info.coldpath.detail.updated")}{" "}
              <span className="font-mono tabular-nums text-foreground">
                {new Date(state.updated_at).toLocaleString()}
              </span>
            </span>
          )}
          {state.last_error && (
            <span className="block break-words text-red-600 dark:text-red-400">
              {t("info.coldpath.detail.lastError")}{" "}
              <span className="font-mono">{state.last_error}</span>
            </span>
          )}
          {state.state === "disabled" && (
            <span className="block">{t("info.coldpath.disabledNote")}</span>
          )}
        </span>
      )}
    </span>
  );
}
