import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type { ColdPathState, ColdPathSyncState } from "@/types/api";

/**
 * Cold-path sync badge (shared: /info header, ledger tab, runs list). Five
 * states, one meaning each: disabled is the no-DSN normal state (gray, not
 * red) — only failed carries the destructive semantic. Counts stay
 * mono/tabular per the machine-data rule. Extracted from info-space.tsx so
 * the五态语义 everywhere comes from one definition.
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

export function ColdPathBadge({ state }: { state: ColdPathSyncState }) {
  const { t } = useI18n();
  const meta = COLDPATH_BADGE[state.state] ?? COLDPATH_BADGE.disabled;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-[11px]",
        meta.className,
      )}
      aria-label={t(`info.coldpath.${state.state}`)}
    >
      <span aria-hidden className={cn("size-1.5 rounded-full", meta.dot)} />
      {t(`info.coldpath.${state.state}`)}
      <span className="font-mono text-[10px] tabular-nums opacity-80">
        {state.applied_seq}/{state.source_seq}
      </span>
    </span>
  );
}
