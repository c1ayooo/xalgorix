import type { InfoHeatmapRow, InfoRelationEdge } from "@/types/api";

/**
 * Deterministic nebula layout for the project information map.
 *
 * One subject = one point. Distance from the centre, point size and colour all
 * encode information volume (the user's chosen heat meaning). Angles and jitter
 * come from a stable hash of the subject, so the same snapshot always draws the
 * same nebula — no `Math.random()`, no reshuffling on refresh.
 */

export interface NebulaPoint {
  row: InfoHeatmapRow;
  x: number;
  y: number;
  /** CSS-pixel radius. */
  r: number;
  color: string;
  alpha: number;
  /** Normalised volume in [0.08, 1]. */
  norm: number;
}

/** Stable node identity shared by the map, edges and selection. */
export function subjectKey(subject: string, kind: string): string {
  return `${kind}\u0000${subject}`;
}

/** Cold neutral → brand red. Data-viz ramp, not chrome colour. */
const RAMP: { stop: number; rgb: [number, number, number] }[] = [
  { stop: 0, rgb: [122, 128, 142] },
  { stop: 0.25, rgb: [148, 128, 152] },
  { stop: 0.5, rgb: [176, 100, 124] },
  { stop: 0.75, rgb: [200, 62, 76] },
  { stop: 1, rgb: [205, 38, 48] },
];

export const HEAT_RAMP_CSS = `linear-gradient(90deg, ${RAMP.map(
  (entry) => `rgb(${entry.rgb[0]}, ${entry.rgb[1]}, ${entry.rgb[2]}) ${entry.stop * 100}%`,
).join(", ")})`;

/**
 * Fixed log-power scale: new information grows a point outward without
 * rescaling the rest, and the power curve keeps the low-volume tail close to
 * the centre so the nebula has a dense cold core.
 */
export function volumeNorm(total: number): number {
  const ratio = Math.log1p(Math.max(0, total)) / Math.log1p(50);
  return Math.min(1, Math.max(0.06, Math.pow(ratio, 1.6)));
}

export function heatColor(norm: number): string {
  const value = Math.min(1, Math.max(0, norm));
  for (let i = 1; i < RAMP.length; i += 1) {
    const upper = RAMP[i];
    if (value <= upper.stop) {
      const lower = RAMP[i - 1];
      const f = (value - lower.stop) / (upper.stop - lower.stop || 1);
      const rgb = lower.rgb.map((v, k) => Math.round(v + (upper.rgb[k] - v) * f));
      return `rgb(${rgb[0]}, ${rgb[1]}, ${rgb[2]})`;
    }
  }
  return "rgb(205, 38, 48)";
}

function fnv1a(input: string): number {
  let hash = 0x811c9dc5;
  for (let i = 0; i < input.length; i += 1) {
    hash ^= input.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return hash >>> 0;
}

/** Deterministic side for an edge's slight bow, so curves do not all lean one way. */
export function edgeBowSign(srcKey: string, dstKey: string): number {
  return (fnv1a(`${srcKey}|${dstKey}`) & 1) === 0 ? 1 : -1;
}

export function layoutNebula(
  rows: InfoHeatmapRow[],
  edges: InfoRelationEdge[],
  width: number,
  height: number,
): NebulaPoint[] {
  if (width <= 0 || height <= 0 || rows.length === 0) return [];
  const cx = width / 2;
  const cy = height / 2;
  // A circle, not an ellipse: on wide panels the nebula keeps its shape and
  // centres itself instead of stretching with the viewport.
  const radius = Math.max(1, Math.min(width, height) / 2 - 20);

  const points: NebulaPoint[] = rows.map((row) => {
    const hash = fnv1a(`${row.subject_kind ?? ""}\u0000${row.subject}`);
    // Angle grid: deterministic spread around the circle.
    const angle = ((hash % 3600) / 3600) * Math.PI * 2;
    const norm = volumeNorm(row.total);
    // Radius follows volume; the hash jitter avoids clean concentric rings.
    const jitter = ((hash >>> 12) % 1000) / 1000;
    const radial = Math.min(0.98, norm * (0.86 + jitter * 0.2));
    const angleJitter = (((hash >>> 22) % 1000) / 1000 - 0.5) * 0.5;
    const theta = angle + angleJitter;
    return {
      row,
      x: cx + Math.cos(theta) * radial * radius,
      y: cy + Math.sin(theta) * radial * radius,
      r: 1.4 + norm * 6.2,
      color: heatColor(norm),
      alpha: 0.55 + norm * 0.35,
      norm,
    };
  });

  // Relation-aware relaxation: connected subjects drift toward each other in
  // angle so edges stay short, while a radial spring keeps the volume distance
  // (the heat meaning) intact. Deterministic: no randomness, bounded steps.
  const index = new Map<string, number>();
  points.forEach((point, i) =>
    index.set(subjectKey(point.row.subject, point.row.subject_kind ?? ""), i),
  );
  const pairs: [number, number][] = [];
  for (const edge of edges) {
    const a = index.get(subjectKey(edge.src_subject, edge.src_kind ?? ""));
    const b = index.get(subjectKey(edge.dst_subject, edge.dst_kind ?? ""));
    if (a !== undefined && b !== undefined && a !== b) pairs.push([a, b]);
  }
  if (pairs.length === 0) return points;

  const n = points.length;
  const targetR = new Float64Array(n);
  const px = new Float64Array(n);
  const py = new Float64Array(n);
  points.forEach((point, i) => {
    px[i] = point.x;
    py[i] = point.y;
    targetR[i] = Math.hypot(point.x - cx, point.y - cy);
  });

  const ITERATIONS = 110;
  const REPEL = 1200;
  const SPRING = 0.06;
  const RADIAL = 0.08;
  const MAX_STEP = 3;

  for (let iter = 0; iter < ITERATIONS; iter += 1) {
    const fx = new Float64Array(n);
    const fy = new Float64Array(n);

    for (let i = 0; i < n; i += 1) {
      for (let j = i + 1; j < n; j += 1) {
        let dx = px[j] - px[i];
        let dy = py[j] - py[i];
        let d2 = dx * dx + dy * dy;
        if (d2 < 1) {
          dx = (i - j) || 1;
          dy = 1;
          d2 = 1;
        }
        const d = Math.sqrt(d2);
        const force = REPEL / d2;
        const ux = dx / d;
        const uy = dy / d;
        fx[i] -= ux * force;
        fy[i] -= uy * force;
        fx[j] += ux * force;
        fy[j] += uy * force;
      }
    }

    for (const [a, b] of pairs) {
      const dx = px[b] - px[a];
      const dy = py[b] - py[a];
      const d = Math.hypot(dx, dy) || 1;
      const force = SPRING * (d - 22);
      const ux = dx / d;
      const uy = dy / d;
      fx[a] += ux * force;
      fy[a] += uy * force;
      fx[b] -= ux * force;
      fy[b] -= uy * force;
    }

    for (let i = 0; i < n; i += 1) {
      const rx = px[i] - cx;
      const ry = py[i] - cy;
      const r = Math.hypot(rx, ry) || 1;
      const radialForce = RADIAL * (targetR[i] - r);
      fx[i] += (rx / r) * radialForce;
      fy[i] += (ry / r) * radialForce;

      const step = Math.hypot(fx[i], fy[i]) || 1;
      const scale = Math.min(MAX_STEP, step) / step;
      px[i] += fx[i] * scale;
      py[i] += fy[i] * scale;

      // Keep the volume radius within ±20%: edges get shorter, the heat map
      // keeps its outward ordering without letting a cluster collapse inward.
      const nrx = px[i] - cx;
      const nry = py[i] - cy;
      const nr = Math.hypot(nrx, nry) || 1;
      const clamped = Math.min(
        radius,
        Math.max(targetR[i] * 0.8, Math.min(targetR[i] * 1.2, nr)),
      );
      px[i] = cx + (nrx / nr) * clamped;
      py[i] = cy + (nry / nr) * clamped;
    }
  }

  points.forEach((point, i) => {
    point.x = px[i];
    point.y = py[i];
  });
  return points;
}

/** Small points first; hot/big points paint last so they stay on top. */
export function sortForPaint(points: NebulaPoint[]): NebulaPoint[] {
  return [...points].sort(
    (a, b) => a.norm - b.norm || a.row.subject.localeCompare(b.row.subject),
  );
}

export function hitTest(
  points: NebulaPoint[],
  x: number,
  y: number,
): NebulaPoint | null {
  let best: NebulaPoint | null = null;
  let bestDistance = Number.POSITIVE_INFINITY;
  for (const point of points) {
    const distance = Math.hypot(x - point.x, y - point.y);
    const limit = Math.max(12, point.r + 7);
    if (distance <= limit && distance < bestDistance) {
      best = point;
      bestDistance = distance;
    }
  }
  return best;
}
