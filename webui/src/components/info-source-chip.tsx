import { Bot, Terminal, User, Wrench } from "lucide-react";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type { InfoSource } from "@/types/api";

/**
 * Source dimension of an InfoItem. `human` is the charter's only expression of
 * operator input (CORE §8: 人工输入 = source=human 的 InfoItem), so it gets the
 * emerald semantic already used by the approval slice; every other producer
 * stays quiet neutral so it never competes with nature/severity colours.
 */
export function sourceMeta(kind: string | undefined): {
  label: string;
  chip: string;
  dot: string;
  Icon: typeof User;
} {
  switch ((kind ?? "").toLowerCase()) {
    case "human":
      return {
        label: "human",
        chip:
          "border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
        dot: "bg-emerald-500",
        Icon: User,
      };
    case "trafficproxy":
      return {
        label: "trafficproxy",
        chip:
          "border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400",
        dot: "bg-amber-500",
        Icon: Terminal,
      };
    case "tool":
      return {
        label: "tool",
        chip: "border-sky-500/30 bg-sky-500/10 text-sky-600 dark:text-sky-400",
        dot: "bg-sky-500",
        Icon: Wrench,
      };
    case "agent":
      return {
        label: "agent",
        chip: "border-border bg-muted text-muted-foreground",
        dot: "bg-muted-foreground/60",
        Icon: Bot,
      };
    default:
      return {
        label: kind && kind.trim() !== "" ? kind : "engine",
        chip: "border-border bg-muted text-muted-foreground",
        dot: "bg-muted-foreground/60",
        Icon: Wrench,
      };
  }
}

/**
 * Renders the source kind plus the producer identity when the engine reported
 * one. Machine identifiers stay mono.
 */
export function InfoSourceChip({
  source,
  className,
}: {
  source: InfoSource | undefined;
  className?: string;
}) {
  const { t } = useI18n();
  if (!source || (!source.kind && !source.name)) {
    return (
      <span className="font-mono text-[10px] text-muted-foreground">—</span>
    );
  }
  const meta = sourceMeta(source.kind);
  const Icon = meta.Icon;
  const human = (source.kind ?? "").toLowerCase() === "human";
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-1.5", className)}>
      <span
        className={cn(
          "inline-flex shrink-0 items-center gap-1 rounded-full border px-1.5 text-[10px] uppercase tracking-wide",
          meta.chip,
        )}
        title={t("info.sourceKind")}
      >
        <Icon className="size-3" aria-hidden />
        {meta.label}
      </span>
      {source.name && (
        <span
          className={cn(
            "truncate font-mono text-[10px]",
            human ? "text-emerald-700 dark:text-emerald-300" : "text-muted-foreground",
          )}
          title={source.name}
        >
          {source.name}
        </span>
      )}
    </span>
  );
}
