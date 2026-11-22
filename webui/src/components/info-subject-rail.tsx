import { useMemo } from "react";
import { Layers } from "lucide-react";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type { InfoItem } from "@/types/api";

interface SubjectEntry {
  key: string;
  identifier: string;
  kind: string;
  count: number;
  latest: string;
}

/**
 * Region lighting made navigable: the charter's "subject" dimension is a
 * deterministic property filter (CORE §4.1), so the left rail lists subjects
 * aggregated from the loaded page instead of asking the operator to type one.
 *
 * Honest scope: the rail only knows the rows already fetched, so the header
 * says so; it never claims a project-wide subject inventory.
 */
export function InfoSubjectRail({
  items,
  activeSubject,
  onSelect,
}: {
  items: InfoItem[];
  activeSubject: string;
  onSelect: (subject: string, kind: string) => void;
}) {
  const { t } = useI18n();
  const subjects = useMemo(() => {
    const byKey = new Map<string, SubjectEntry>();
    for (const item of items) {
      const identifier = item.coordinates.subject.identifier;
      if (!identifier) continue;
      const kind = item.coordinates.subject.kind ?? "";
      const key = `${kind}\u0000${identifier}`;
      const existing = byKey.get(key);
      if (existing) {
        existing.count += 1;
        if (item.coordinates.observed_at > existing.latest) {
          existing.latest = item.coordinates.observed_at;
        }
      } else {
        byKey.set(key, {
          key,
          identifier,
          kind,
          count: 1,
          latest: item.coordinates.observed_at,
        });
      }
    }
    return [...byKey.values()].sort(
      (a, b) => b.count - a.count || a.identifier.localeCompare(b.identifier),
    );
  }, [items]);

  return (
    <div className="flex min-h-0 flex-col gap-2">
      <div className="flex items-center gap-1.5">
        <Layers className="size-3.5 text-muted-foreground" aria-hidden />
        <span className="text-xs font-medium">{t("subjectRail.title")}</span>
        <span className="font-mono text-[10px] tabular-nums text-muted-foreground">
          {subjects.length}
        </span>
      </div>
      <p className="text-[10px] leading-4 text-muted-foreground">
        {t("subjectRail.scopeNote")}
      </p>
      <div className="-mx-1 min-h-0 flex-1 overflow-y-auto px-1">
        <button
          type="button"
          onClick={() => onSelect("", "")}
          className={cn(
            "flex w-full items-center gap-2 rounded-sm px-2.5 py-1.5 text-left text-xs transition-colors",
            activeSubject === ""
              ? "bg-accent text-foreground"
              : "text-muted-foreground hover:bg-accent/40 hover:text-foreground",
          )}
          aria-pressed={activeSubject === ""}
        >
          {t("subjectRail.all")}
          <span className="ml-auto font-mono text-[10px] tabular-nums">
            {items.length}
          </span>
        </button>
        <ul className="mt-1 space-y-0.5">
          {subjects.map((subject) => {
            const active = activeSubject === subject.identifier;
            return (
              <li key={subject.key}>
                <button
                  type="button"
                  onClick={() => onSelect(subject.identifier, subject.kind)}
                  aria-pressed={active}
                  title={`${subject.identifier} · ${subject.latest}`}
                  className={cn(
                    "flex w-full items-start gap-2 rounded-sm px-2.5 py-1.5 text-left transition-colors",
                    active
                      ? "bg-accent text-foreground"
                      : "text-muted-foreground hover:bg-accent/40 hover:text-foreground",
                  )}
                >
                  <span className="min-w-0 flex-1">
                    <span className="block truncate font-mono text-[11px]">
                      {subject.identifier}
                    </span>
                    {subject.kind && (
                      <span className="block truncate text-[10px] uppercase tracking-wide opacity-70">
                        {subject.kind}
                      </span>
                    )}
                  </span>
                  <span className="shrink-0 font-mono text-[10px] tabular-nums">
                    {subject.count}
                  </span>
                </button>
              </li>
            );
          })}
        </ul>
      </div>
    </div>
  );
}
