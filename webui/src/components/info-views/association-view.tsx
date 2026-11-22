import { useState } from "react";
import { Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { AssociationGraph } from "@/components/association-graph";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type { InfoAssociationHit } from "@/types/api";

/** CORE §4.2 vocabulary; colours match the graph legend dots. */
export const EDGE_KINDS: { kind: string; key: string; dot: string }[] = [
  { kind: "supports", key: "info.association.edge.supports", dot: "bg-emerald-500" },
  { kind: "contradicts", key: "info.association.edge.contradicts", dot: "bg-red-500" },
  { kind: "references", key: "info.association.edge.references", dot: "bg-sky-500" },
  { kind: "same_session", key: "info.association.edge.sameSession", dot: "bg-amber-500" },
  { kind: "same_subject", key: "info.association.edge.sameSubject", dot: "bg-fuchsia-500" },
];

export function AssociationView({
  hits,
  seeds,
  edgeTypes,
  depth,
  onSeedsChange,
  onEdgeTypesChange,
  onDepthChange,
  onSeedNode,
}: {
  hits: InfoAssociationHit[];
  seeds: string[];
  edgeTypes: string[];
  depth: number;
  onSeedsChange: (seeds: string[]) => void;
  onEdgeTypesChange: (types: string[]) => void;
  onDepthChange: (depth: number) => void;
  onSeedNode: (id: string) => void;
}) {
  const { t } = useI18n();
  const [draft, setDraft] = useState("");

  const addSeed = () => {
    const id = draft.trim();
    if (!id) return;
    if (!seeds.includes(id)) onSeedsChange([...seeds, id]);
    setDraft("");
  };
  const removeSeed = (id: string) =>
    onSeedsChange(seeds.filter((seed) => seed !== id));
  const toggleKind = (kind: string) =>
    onEdgeTypesChange(
      edgeTypes.includes(kind)
        ? edgeTypes.filter((value) => value !== kind)
        : [...edgeTypes, kind],
    );

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-card p-3">
        <span className="text-[10px] uppercase tracking-wider text-muted-foreground">
          {t("association.seeds")}
        </span>
        {seeds.map((seed) => (
          <span
            key={seed}
            className="inline-flex items-center gap-1 rounded-full border border-border bg-muted/40 px-2 py-0.5 font-mono text-[10px]"
          >
            {seed}
            <button
              type="button"
              onClick={() => removeSeed(seed)}
              aria-label={t("association.removeSeed")}
              className="text-muted-foreground transition-colors hover:text-foreground"
            >
              <X className="size-3" aria-hidden />
            </button>
          </span>
        ))}
        <span className="flex items-center gap-1">
          <Input
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                addSeed();
              }
            }}
            placeholder={t("info.association.idsPlaceholder")}
            className="h-8 w-56 font-mono text-xs"
            aria-label={t("association.addSeed")}
          />
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="h-8"
            onClick={addSeed}
          >
            <Plus className="size-3.5" aria-hidden />
            {t("association.addSeed")}
          </Button>
        </span>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[10px] uppercase tracking-wider text-muted-foreground">
          {t("info.association.edgeTypes")}
        </span>
        <button
          type="button"
          onClick={() => onEdgeTypesChange([])}
          aria-pressed={edgeTypes.length === 0}
          className={cn(
            "rounded-full border px-2.5 py-0.5 text-xs transition-colors",
            edgeTypes.length === 0
              ? "border-foreground bg-foreground text-background"
              : "border-border text-muted-foreground hover:text-foreground",
          )}
        >
          {t("association.allKinds")}
        </button>
        {EDGE_KINDS.map((entry) => {
          const active = edgeTypes.includes(entry.kind);
          return (
            <button
              key={entry.kind}
              type="button"
              onClick={() => toggleKind(entry.kind)}
              aria-pressed={active}
              className={cn(
                "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs transition-colors",
                active
                  ? "border-foreground text-foreground"
                  : "border-border text-muted-foreground hover:text-foreground",
              )}
            >
              <span
                aria-hidden
                className={cn("size-1.5 rounded-full", entry.dot, !active && "opacity-40")}
              />
              {t(entry.key)}
            </button>
          );
        })}
        <span className="mx-1 h-4 w-px bg-border" aria-hidden />
        <Select value={String(depth)} onValueChange={(v) => onDepthChange(Number(v))}>
          <SelectTrigger className="h-8 w-32 text-xs" aria-label={t("association.depth")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {[1, 2, 3].map((n) => (
              <SelectItem key={n} value={String(n)}>
                {t("association.hops").replace("{n}", String(n))}
                {n === 3 ? ` ${t("association.hopsMax")}` : ""}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      <AssociationGraph hits={hits} onSelectNode={onSeedNode} />
    </div>
  );
}
