/**
 * Shared layered-SVG primitives for the two read-only graph surfaces
 * (association lighting and the exploration chain).
 *
 * House rules (UI_DESIGN §6 association-graph entry): no graph library,
 * deterministic layout, legend lists the whole vocabulary with zero counts
 * dimmed, and an accessible parallel list accompanies every SVG. Keeping the
 * geometry and stroke palette here stops the two graphs from drifting apart.
 */

export interface ColumnNode {
  id: string;
  /** 0-based column index; nodes with equal columns stack top to bottom. */
  column: number;
}

export interface LayeredOptions {
  columnWidth: number;
  rowHeight: number;
  marginX: number;
  headerHeight: number;
  /** Rendered card size; pass 0 for dot nodes (straight centre-to-centre edges). */
  cardWidth: number;
  cardHeight: number;
}

export interface Positioned<T> {
  node: T;
  column: number;
  x: number;
  y: number;
}

export interface LayeredLayout<T> {
  positioned: Positioned<T>[];
  byID: Map<string, Positioned<T>>;
  width: number;
  height: number;
  maxRows: number;
  columns: number;
}

/** Deterministic column/row layout: input order defines the row order. */
export function layoutLayered<T extends ColumnNode>(
  nodes: T[],
  opts: LayeredOptions,
): LayeredLayout<T> {
  const byColumn = new Map<number, T[]>();
  for (const node of nodes) {
    const list = byColumn.get(node.column) ?? [];
    list.push(node);
    byColumn.set(node.column, list);
  }
  const positioned: Positioned<T>[] = [];
  const byID = new Map<string, Positioned<T>>();
  for (const [column, list] of byColumn) {
    list.forEach((node, row) => {
      const entry: Positioned<T> = {
        node,
        column,
        x: opts.marginX + column * opts.columnWidth,
        y: opts.headerHeight + row * opts.rowHeight,
      };
      positioned.push(entry);
      if (!byID.has(node.id)) byID.set(node.id, entry);
    });
  }
  const columns = Math.max(1, ...[...byColumn.keys()].map((c) => c + 1));
  const maxRows = Math.max(
    1,
    ...[...byColumn.values()].map((list) => list.length),
  );
  return {
    positioned,
    byID,
    width: opts.marginX + columns * opts.columnWidth + 24,
    height: Math.max(200, opts.headerHeight + maxRows * opts.rowHeight + 8),
    maxRows,
    columns,
  };
}

/** Straight centre-to-centre segment (dot-node graphs). */
export function lineSegment(
  from: { x: number; y: number },
  to: { x: number; y: number },
  opts: LayeredOptions,
): { x1: number; y1: number; x2: number; y2: number } {
  const halfW = opts.cardWidth / 2;
  const halfH = opts.cardHeight / 2;
  return {
    x1: from.x + halfW,
    y1: from.y + halfH,
    x2: to.x + halfW,
    y2: to.y + halfH,
  };
}

/** Cubic path from the source card's right edge to the target's left edge. */
export function curvePath(
  from: { x: number; y: number },
  to: { x: number; y: number },
  opts: LayeredOptions,
): string {
  const x1 = from.x + opts.cardWidth;
  const y1 = from.y + opts.cardHeight / 2;
  const x2 = to.x;
  const y2 = to.y + opts.cardHeight / 2;
  const mid = (x1 + x2) / 2;
  return `M ${x1} ${y1} C ${mid} ${y1}, ${mid} ${y2}, ${x2} ${y2}`;
}

/** Counts by relation kind; the legend renders the full vocabulary. */
export function countRelations(
  edges: { rel?: string | null }[],
): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const edge of edges) {
    if (!edge.rel) continue;
    counts[edge.rel] = (counts[edge.rel] ?? 0) + 1;
  }
  return counts;
}

export function relStroke(
  rel: string | null | undefined,
  palette: Record<string, string>,
): string {
  if (!rel) return NEUTRAL_STROKE;
  return palette[rel] ?? NEUTRAL_STROKE;
}

/** Quiet neutral stroke for untyped/unknown relations — never a fake kind. */
export const NEUTRAL_STROKE = "#71717a";

/** CORE §4.2 relation vocabulary (association lighting). */
export const INFO_REL_STROKE: Record<string, string> = {
  supports: "#10b981",
  contradicts: "#ef4444",
  references: "#0ea5e9",
  same_session: "#f59e0b",
  same_subject: "#a855f7",
};

/** Exploration-chain edge vocabulary (claim/yields/proves/supports). */
export const EXPLORATION_REL_STROKE: Record<string, string> = {
  claim: "#0ea5e9",
  yields: "#10b981",
  proves: "#f59e0b",
  supports: "#8b5cf6",
};
