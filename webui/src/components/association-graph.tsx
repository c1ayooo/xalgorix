import { useMemo } from "react";
import { useI18n } from "@/i18n";
import type { InfoAssociationHit } from "@/types/api";

interface Node {
  id: string;
  label: string;
  depth: number;
  x: number;
  y: number;
}

/**
 * Deterministic depth-layered SVG graph. No graph library: nodes are grouped by
 * hop depth, laid out in columns, and connected along the returned paths. The
 * accessible list below the SVG is the primary keyboard/screen-reader surface.
 */
export function AssociationGraph({ hits }: { hits: InfoAssociationHit[] }) {
  const { t } = useI18n();
  const { nodes, edges, width, height } = useMemo(() => {
    const byDepth = new Map<number, InfoAssociationHit[]>();
    for (const hit of hits) {
      const depth = Math.max(1, hit.depth);
      const list = byDepth.get(depth) ?? [];
      list.push(hit);
      byDepth.set(depth, list);
    }
    const nodeList: Node[] = [];
    const index = new Map<string, Node>();
    // Paths start at the selected Region item, which is not part of the hit
    // list. Add a synthetic depth-0 node for each start id so the graph edges
    // remain visible instead of rendering disconnected dots.
    const startIDs = new Set<string>();
    for (const hit of hits) {
      if (hit.path.length > 0) startIDs.add(hit.path[0]);
    }
    [...startIDs].forEach((id, i) => {
      const node: Node = { id, label: id, depth: 0, x: 90, y: 50 + i * 86 };
      nodeList.push(node);
      index.set(id, node);
    });
    for (const [depth, list] of [...byDepth.entries()].sort((a, b) => a[0] - b[0])) {
      list.forEach((hit, i) => {
        if (index.has(hit.item.id)) return;
        const node: Node = {
          id: hit.item.id,
          label: hit.item.coordinates.subject.identifier || hit.item.id,
          depth,
          x: 90 + depth * 220,
          y: 50 + i * 86,
        };
        nodeList.push(node);
        index.set(node.id, node);
      });
    }
    const maxDepth = Math.max(1, ...byDepth.keys());
    const maxRows = Math.max(
      1,
      startIDs.size,
      ...Array.from(byDepth.values()).map((v) => v.length),
    );
    const edgeList: { from: Node; to: Node }[] = [];
    for (const hit of hits) {
      for (let i = 0; i + 1 < hit.path.length; i++) {
        const from = index.get(hit.path[i]);
        const to = index.get(hit.path[i + 1]);
        if (from && to) edgeList.push({ from, to });
      }
    }
    return {
      nodes: nodeList,
      edges: edgeList,
      width: 90 + maxDepth * 220 + 180,
      height: Math.max(220, 50 + maxRows * 86 + 40),
    };
  }, [hits]);

  if (hits.length === 0) return null;
  return (
    <div className="space-y-3">
      <div className="overflow-x-auto rounded-md border border-border bg-background/40">
        <svg
          role="img"
          aria-label={t("info.association.graphLabel")}
          viewBox={`0 0 ${width} ${height}`}
          className="min-w-[640px]"
        >
          {edges.map((edge, i) => (
            <line
              key={`e-${i}`}
              x1={edge.from.x}
              y1={edge.from.y}
              x2={edge.to.x}
              y2={edge.to.y}
              className="stroke-border"
              strokeWidth={1.25}
            />
          ))}
          {nodes.map((node) => (
            <g key={node.id} tabIndex={0} role="listitem" aria-label={`${node.label} · depth ${node.depth}`}>
              <circle cx={node.x} cy={node.y} r={5.5} className="fill-brand/70 stroke-brand" />
              <text x={node.x + 12} y={node.y + 4} className="fill-foreground text-[10px]">
                {node.label.length > 28 ? `${node.label.slice(0, 28)}…` : node.label}
              </text>
              <text x={node.x - 26} y={node.y + 4} className="fill-muted-foreground text-[9px]">
                d{node.depth}
              </text>
            </g>
          ))}
        </svg>
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
            <span className="ml-auto font-mono text-[10px] text-muted-foreground">
              {hit.path.join(" → ")}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}
