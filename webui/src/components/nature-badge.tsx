import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type { InfoNature } from "@/types/api";

/**
 * nature is a core epistemic type (CORE §3.2/§3.3): observed = raw request,
 * response, tool output or human input; inferred = an interpretation formed by
 * a model or a rule. Missing/legacy values normalize to inferred so unknown
 * data is never silently promoted to a raw observation.
 *
 * This is the single source of the observed=sky / inferred=violet semantics —
 * every surface (info card, ledger evidence, graphs) must render through it
 * instead of repeating literal colour classes.
 */
export type NatureValue = InfoNature | "" | undefined | null;

export function normalizeNature(value: NatureValue): InfoNature {
  return value === "observed" ? "observed" : "inferred";
}

export function natureClasses(value: NatureValue): {
  chip: string;
  dot: string;
} {
  return normalizeNature(value) === "observed"
    ? {
        chip: "border-sky-500/30 bg-sky-500/10 text-sky-600 dark:text-sky-400",
        dot: "bg-sky-500",
      }
    : {
        chip:
          "border-violet-500/30 bg-violet-500/10 text-violet-600 dark:text-violet-400",
        dot: "bg-violet-500",
      };
}

export function NatureBadge({
  nature,
  className,
  size = "sm",
}: {
  nature: NatureValue;
  className?: string;
  /** `xs` is the dense table/ledger variant, `sm` the card variant. */
  size?: "xs" | "sm";
}) {
  const { t } = useI18n();
  const normalized = normalizeNature(nature);
  const legacy = !nature;
  const meta = natureClasses(normalized);
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center gap-1.5 rounded-full border uppercase tracking-wide",
        size === "xs" ? "px-1.5 text-[10px]" : "px-2 py-0.5 text-[11px]",
        meta.chip,
        className,
      )}
      title={legacy ? t("info.nature.legacy") : undefined}
    >
      <span aria-hidden className={cn("size-1.5 rounded-full", meta.dot)} />
      {normalized === "observed"
        ? t("info.nature.observed")
        : t("info.nature.inferred")}
    </span>
  );
}
