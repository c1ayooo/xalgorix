import { useMemo } from "react";
import { useI18n } from "@/i18n";
import {
  INFO_REL_STROKE,
  countRelations,
  layoutLayered,
  lineSegment,
  relStroke,
} from "@/lib/layered-graph";
import { cn } from "@/lib/utils";
import type { InfoAssociationHit, InfoItem } from "@/types/api";

interface AssocNode {
  id: string;
  label: string;
  column: number;
  depth: number;
}

const LAYOUT = {
  columnWidth: 220,
  rowHeight: 86,
  marginX: 90,
  headerHeight: 50,
  cardWidth: 0,
  cardHeight: 0,
};

/**
 * CORE §4.2 relation vocabulary. Every kind stays listed even when the engine
 * cannot produce it yet; the count is what the data actually contains, so a
 * zero count is an honest "not produced", not a hidden feature.
 */
const EDGE_LEGEND: { kind: string; key: string; dot: string }[] = [
  { kind: "supports", key: "info.association.edge.supports", dot: "bg-emerald-500" },
  { kind: "contradicts", key: "info.association.edge.contradicts", dot: "bg-red-500" },
  { kind: "references", key: "info.association.edge.references", dot: "bg-sky-500" },
  { kind: "same_session", key: "info.association.edge.sameSession", dot: "bg-amber-500" },
  { kind: "same_subject", key: "info.association.edge.sameSubject", dot: "bg-fuchsia-500" },
];

/**
 * Deterministic depth-layered SVG graph (no graph library). Edge kinds come
 * from the server's per-hop `rels`; when the server omits them the edge is
 * neutral — the graph never infers kinds from `item.supports`, because that
 * turns a guess into a displayed fact.
 */
export function AssociationGraph({
  hits,
  onSelectNode,
}: {
  hits: InfoAssociationHit[];
  /** Clicking a node pivots the traversal seed / region subject. */
  onSelectNode?: (id: string) => void;
}) {
  const { t } = useI18n();
  const { positioned, edges, width, height, relCounts } = useMemo(() => {
    const nodeList: AssocNode[] = [];
    const seen = new Set<string>();
    const itemByID = new Map<string, InfoItem>();
    const startIDs = new Set<string>();
    for (const hit of hits) {
      if (hit.path.length > 0) startIDs.add(hit.path[0]);
      itemByID.set(hit.item.id, hit.item);
    }
    // Synthetic depth-0 nodes keep the path visible instead of disconnected dots.
    for (const id of startIDs) {
      if (seen.has(id)) continue;
      seen.add(id);
      nodeList.push({ id, label: id, column: 0, depth: 0 });
    }
    for (const hit of hits) {
      if (seen.has(hit.item.id)) continue;
      seen.add(hit.item.id);
      const item = hit.item;
      nodeList.push({
        id: item.id,
        label:
          item.coordinates.subject.identifier ||
          item.coordinates.content.text.slice(0, 28) ||
          item.id,
        column: Math.max(1, hit.depth),
        depth: Math.max(1, hit.depth),
      });
    }
    const layout = layoutLayered(nodeList, LAYOUT);

    const edgeMap = new Map<
      string,
      { from: (typeof layout.positioned)[number]; to: (typeof layout.positioned)[number]; rel: string | null }
    >();
    for (const hit of hits) {
      for (let i = 0; i + 1 < hit.path.length; i++) {
        const from = layout.byID.get(hit.path[i]);
        const to = layout.byID.get(hit.path[i + 1]);
        if (!from || !to) continue;
        // rels[i] is the type of path[i] → path[i+1]; absent means untyped.
        const rel = hit.rels?.[i] ?? null;
        const key = `${hit.path[i]}\u0000${hit.path[i + 1]}\u0000${rel ?? ""}`;
        if (edgeMap.has(key)) continue;
        edgeMap.set(key, { from, to, rel });
      }
    }
    const edges = [...edgeMap.values()];
    return {
      positioned: layout.positioned,
      edges,
      width: layout.width,
      height: layout.height,
      relCounts: countRelations(edges),
    };
  }, [hits]);

  if (hits.length === 0) return null;
  const typed = edges.some((edge) => edge.rel);

  return (
    <div className="space-y-3">
      <div className="overflow-x-auto rounded-md border border-border bg-background/40">
        <svg
          role="img"
          aria-label={t("info.association.graphLabel")}
          viewBox={`0 0 ${width} ${height}`}
          className="min-w-[640px]"
        >
          {edges.map((edge, i) => {
            const segment = lineSegment(edge.from, edge.to, LAYOUT);
            return (
              <line
                key={`e-${i}`}
                {...segment}
                stroke={relStroke(edge.rel, INFO_REL_STROKE)}
                strokeWidth={1.25}
                strokeOpacity={edge.rel ? 0.9 : 0.6}
              />
            );
          })}
          {positioned.map((entry) => {
            const node = entry.node;
            return (
              <g
                key={node.id}
                tabIndex={0}
                role="button"
                aria-label={`${node.label} · depth ${node.depth}`}
                className={cn("outline-none", onSelectNode && "cursor-pointer")}
                onClick={() => onSelectNode?.(node.id)}
                onKeyDown={(event) => {
                  if (!onSelectNode) return;
                  if (event.key === "Enter" || event.key === " ") {
                    event.preventDefault();
                    onSelectNode(node.id);
                  }
                }}
              >
                <circle
                  cx={entry.x}
                  cy={entry.y}
                  r={5.5}
                  className="fill-brand/70 stroke-brand"
                />
                <text
                  x={entry.x + 12}
                  y={entry.y + 4}
                  className="fill-foreground text-[10px]"
                >
                  {node.label.length > 28 ? `${node.label.slice(0, 28)}…` : node.label}
                </text>
                <text
                  x={entry.x - 26}
                  y={entry.y + 4}
                  className="fill-muted-foreground text-[9px]"
                >
                  d{node.depth}
                </text>
              </g>
            );
          })}
        </svg>
      </div>

      {/* Legend: the vocabulary is the contract; zero counts stay listed. */}
      <div className="border-t border-border pt-3">
        <div
          className="flex flex-wrap items-center gap-x-4 gap-y-1.5"
          aria-label={t("info.association.legendLabel")}
        >
          {EDGE_LEGEND.map((entry) => {
            const count = relCounts[entry.kind] ?? 0;
            return (
              <span
                key={entry.kind}
                className="inline-flex items-center gap-1.5 text-xs text-muted-foreground"
              >
                <span
                  aria-hidden
                  className={cn(
                    "size-1.5 rounded-full",
                    entry.dot,
                    count === 0 && "opacity-40",
                  )}
                />
                {t(entry.key)}
                <span className="font-mono text-[10px] tabular-nums">{count}</span>
              </span>
            );
          })}
        </div>
        {edges.length > 0 && !typed && (
          <p className="mt-2 text-[11px] text-muted-foreground">
            {t("info.association.legendNote")}
          </p>
        )}
      </div>

      <ul className="space-y-1.5" aria-label={t("info.association.listLabel")}>
        {hits.map((hit) => (
          <li
            key={hit.item.id}
            className="flex flex-wrap items-center gap-2 rounded-sm border border-border/70 px-2.5 py-1.5 text-xs"
          >
            <span className="font-mono text-[10px] text-muted-foreground">
              d{hit.depth}
            </span>
            <span className="truncate text-foreground">
              {hit.item.coordinates.content.text}
            </span>
            {hit.rels && hit.rels.length > 0 && (
              <span className="font-mono text-[10px] text-muted-foreground">
                {hit.rels.join(" → ")}
              </span>
            )}
            <span className="ml-auto font-mono text-[10px] text-muted-foreground">
              {hit.path.join(" → ")}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}
