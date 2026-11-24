import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { RotateCcw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";
import { useTheme } from "@/store/theme";
import {
  edgeBowSign,
  edgePhaseOffset,
  heatRGB,
  layoutNebula,
  sortForPaint,
  subjectKey,
  type NebulaPoint,
} from "@/lib/nebula";
import { stageOf, stageStyle } from "@/lib/stage-heat";
import { subjectKindLabel } from "@/lib/subject-kind";
import type { InfoHeatmapRow, InfoRelationEdge } from "@/types/api";

type View = { x: number; y: number; k: number };

type DragState =
  | {
      type: "node";
      key: string;
      offsetX: number;
      offsetY: number;
      startX: number;
      startY: number;
      moved: boolean;
    }
  | {
      type: "pan";
      startX: number;
      startY: number;
      viewX: number;
      viewY: number;
      moved: boolean;
    }
  | null;

const CHAIN_STROKE = "rgb(205, 90, 70)";
const INFO_STROKE = "rgb(138, 143, 153)";
const GLOW_BUCKETS = 12;
const ROTATION_PER_MS = 0.00003; // ~1.7°/s, one revolution every ~3.5 min
const FRAME_MS = 33; // ambient loop at ~30fps
const glowCache = new Map<number, HTMLCanvasElement>();

function glowSprite(bucket: number): HTMLCanvasElement | null {
  if (typeof document === "undefined") return null;
  const cached = glowCache.get(bucket);
  if (cached) return cached;
  const size = 64;
  const canvas = document.createElement("canvas");
  canvas.width = size;
  canvas.height = size;
  const ctx = canvas.getContext("2d");
  if (!ctx) return null;
  const [r, g, b] = heatRGB(bucket / (GLOW_BUCKETS - 1));
  const gradient = ctx.createRadialGradient(size / 2, size / 2, 0, size / 2, size / 2, size / 2);
  gradient.addColorStop(0, `rgba(${r}, ${g}, ${b}, 0.85)`);
  gradient.addColorStop(0.35, `rgba(${r}, ${g}, ${b}, 0.28)`);
  gradient.addColorStop(1, `rgba(${r}, ${g}, ${b}, 0)`);
  ctx.fillStyle = gradient;
  ctx.fillRect(0, 0, size, size);
  glowCache.set(bucket, canvas);
  return canvas;
}

function prefersReducedMotion(): boolean {
  return (
    typeof window !== "undefined" &&
    !!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches
  );
}

/**
 * Project information nebula (CORE cold/hot, volume heat) + subject graph.
 *
 * Hot is the core: distance to the centre, size and colour all encode
 * information volume, and higher volume sits closer in. Nodes are draggable;
 * the canvas pans, zooms and rotates very slowly. Action/evidence edges carry
 * travelling light (direction without arrows), hot points glow in the dark
 * theme, and the first snapshot blooms out of the core.
 */
export function NebulaMap({
  rows,
  edges,
  layoutEdges,
  selected,
  onSelect,
}: {
  rows: InfoHeatmapRow[];
  edges: InfoRelationEdge[];
  /** All relations, including hidden info edges: layout must not move when the toggle changes. */
  layoutEdges: InfoRelationEdge[];
  selected: { subject: string; subjectKind: string } | null;
  onSelect: (row: InfoHeatmapRow) => void;
}) {
  const { t } = useI18n();
  const { resolved } = useTheme();
  const dark = resolved === "dark";
  const reduceMotionRef = useRef(prefersReducedMotion());

  const wrapRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });
  const [hover, setHover] = useState<{
    point: NebulaPoint;
    x: number;
    y: number;
  } | null>(null);
  const [cursor, setCursor] = useState<"default" | "pointer" | "grab">("default");

  const overridesRef = useRef<Map<string, { x: number; y: number }>>(new Map());
  const viewRef = useRef<View>({ x: 0, y: 0, k: 1 });
  const rotationRef = useRef(0);
  const dragRef = useRef<DragState>(null);
  const hoveredRef = useRef<NebulaPoint | null>(null);
  const hoverFrameRef = useRef<number | null>(null);
  const hoverPointRef = useRef<{ x: number; y: number } | null>(null);
  const drawFrameRef = useRef<number | null>(null);
  const drawRef = useRef<(now?: number) => void>(() => {});
  const entranceRef = useRef<{ start: number; active: boolean }>({
    start: 0,
    active: false,
  });
  const enteredRef = useRef(false);
  const rippleRef = useRef<{ start: number; key: string } | null>(null);

  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const observer = new ResizeObserver((entries) => {
      const rect = entries[0]?.contentRect;
      if (!rect) return;
      setSize((prev) =>
        Math.abs(prev.w - rect.width) > 1 || Math.abs(prev.h - rect.height) > 1
          ? { w: rect.width, h: rect.height }
          : prev,
      );
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  const basePoints = useMemo(
    () => layoutNebula(rows, layoutEdges, size.w, size.h),
    [rows, layoutEdges, size.w, size.h],
  );
  const pointByKey = useMemo(
    () =>
      new Map(
        basePoints.map((point) => [
          subjectKey(point.row.subject, point.row.subject_kind ?? ""),
          point,
        ]),
      ),
    [basePoints],
  );

  function positionOf(point: NebulaPoint) {
    const key = subjectKey(point.row.subject, point.row.subject_kind ?? "");
    return overridesRef.current.get(key) ?? { x: point.x, y: point.y };
  }

  const scheduleDraw = useCallback(() => {
    if (drawFrameRef.current !== null) return;
    drawFrameRef.current = window.requestAnimationFrame(() => {
      drawFrameRef.current = null;
      drawRef.current();
    });
  }, []);

  /** Screen → logical world, undoing pan/zoom and the slow rotation. */
  function logicalPoint(screenX: number, screenY: number) {
    const view = viewRef.current;
    let x = (screenX - view.x) / view.k;
    let y = (screenY - view.y) / view.k;
    const rotation = rotationRef.current;
    if (rotation) {
      const cx = size.w / 2;
      const cy = size.h / 2;
      const dx = x - cx;
      const dy = y - cy;
      const cos = Math.cos(-rotation);
      const sin = Math.sin(-rotation);
      x = cx + dx * cos - dy * sin;
      y = cy + dx * sin + dy * cos;
    }
    return { x, y };
  }

  function hitAt(screenX: number, screenY: number): NebulaPoint | null {
    const pointer = logicalPoint(screenX, screenY);
    let best: NebulaPoint | null = null;
    let bestDistance = Number.POSITIVE_INFINITY;
    for (const point of basePoints) {
      const pos = positionOf(point);
      const distance = Math.hypot(pointer.x - pos.x, pointer.y - pos.y);
      const limit = Math.max(12, point.r + 7);
      if (distance <= limit && distance < bestDistance) {
        best = point;
        bestDistance = distance;
      }
    }
    return best;
  }

  const draw = (now = performance.now()) => {
    const canvas = canvasRef.current;
    if (!canvas || size.w <= 0 || size.h <= 0) return;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    const pixelW = Math.max(1, Math.round(size.w * dpr));
    const pixelH = Math.max(1, Math.round(size.h * dpr));
    if (canvas.width !== pixelW) canvas.width = pixelW;
    if (canvas.height !== pixelH) canvas.height = pixelH;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, size.w, size.h);

    const view = viewRef.current;
    ctx.setTransform(dpr * view.k, 0, 0, dpr * view.k, dpr * view.x, dpr * view.y);
    const centerX = size.w / 2;
    const centerY = size.h / 2;
    if (rotationRef.current) {
      ctx.translate(centerX, centerY);
      ctx.rotate(rotationRef.current);
      ctx.translate(-centerX, -centerY);
    }
    ctx.lineCap = "round";

    const selectedKey = selected
      ? subjectKey(selected.subject, selected.subjectKind)
      : null;
    const reduce = reduceMotionRef.current;

    // Entrance: points bloom out of the centre (cold rim first, hot core last).
    const entrance = entranceRef.current;
    if (entrance.active && now - entrance.start >= 620) entrance.active = false;
    const progress = entrance.active
      ? Math.min(1, (now - entrance.start) / 520)
      : 1;
    const rendered = new Map<NebulaPoint, { x: number; y: number; t: number }>();
    const reveal = (point: NebulaPoint) => {
      const cached = rendered.get(point);
      if (cached) return cached;
      const pos = positionOf(point);
      let t = 1;
      if (entrance.active) {
        const raw = Math.min(1, Math.max(0, (progress - 0.15 * (1 - point.norm)) / 0.85));
        t = 1 - (1 - raw) * (1 - raw);
      }
      const out = {
        x: centerX + (pos.x - centerX) * t,
        y: centerY + (pos.y - centerY) * t,
        t,
      };
      rendered.set(point, out);
      return out;
    };

    const chainTotal = edges.reduce(
      (count, edge) => count + (edge.category === "chain" ? 1 : 0),
      0,
    );
    const particleStride = Math.max(1, Math.ceil(chainTotal / 80));
    let chainSeen = 0;

    for (const edge of edges) {
      const srcKey = subjectKey(edge.src_subject, edge.src_kind ?? "");
      const dstKey = subjectKey(edge.dst_subject, edge.dst_kind ?? "");
      const src = pointByKey.get(srcKey);
      const dst = pointByKey.get(dstKey);
      if (!src || !dst) continue;
      const a = reveal(src);
      const b = reveal(dst);
      const revealAlpha = Math.min(a.t, b.t);
      if (revealAlpha <= 0.02) continue;
      const incident = !!selectedKey && (srcKey === selectedKey || dstKey === selectedKey);
      const chain = edge.category === "chain";
      if (chain) chainSeen += 1;

      // Start/end at the node boundary, bow slightly, and fade long chords so
      // the nebula reads as clusters instead of wires across the whole circle.
      const dx = b.x - a.x;
      const dy = b.y - a.y;
      const length = Math.hypot(dx, dy) || 1;
      const ux = dx / length;
      const uy = dy / length;
      const radiusA = src.r * (0.55 + 0.45 * a.t);
      const radiusB = dst.r * (0.55 + 0.45 * b.t);
      const startX = a.x + ux * (radiusA + 2);
      const startY = a.y + uy * (radiusA + 2);
      const endX = b.x - ux * (radiusB + 2);
      const endY = b.y - uy * (radiusB + 2);
      const midX = (startX + endX) / 2;
      const midY = (startY + endY) / 2;
      const bow = Math.min(48, length * 0.22) * edgeBowSign(srcKey, dstKey);
      const controlX = midX - uy * bow;
      const controlY = midY + ux * bow;
      const distanceFade = Math.min(
        1,
        Math.max(0.28, 1.05 - length / (Math.min(size.w, size.h) * 0.8)),
      );

      const baseAlpha = selectedKey && !incident ? 0.06 : incident ? 0.9 : chain ? 0.4 : 0.22;
      ctx.globalAlpha = baseAlpha * revealAlpha * distanceFade;
      ctx.strokeStyle = chain ? CHAIN_STROKE : INFO_STROKE;
      ctx.lineWidth = incident ? 1.7 : 1;
      ctx.setLineDash(chain ? [] : [5, 4]);
      ctx.beginPath();
      ctx.moveTo(startX, startY);
      ctx.quadraticCurveTo(controlX, controlY, endX, endY);
      ctx.stroke();
      ctx.setLineDash([]);

      // Travelling light: direction without arrows, chain edges only.
      if (
        chain &&
        !reduce &&
        revealAlpha > 0.35 &&
        (!selectedKey || incident) &&
        chainSeen % particleStride === 0
      ) {
        const phase = (now * 0.00045 + edgePhaseOffset(srcKey, dstKey)) % 1;
        const mt = 1 - phase;
        const px = mt * mt * startX + 2 * mt * phase * controlX + phase * phase * endX;
        const py = mt * mt * startY + 2 * mt * phase * controlY + phase * phase * endY;
        const sprite = glowSprite(GLOW_BUCKETS - 1);
        if (sprite) {
          ctx.globalCompositeOperation = "lighter";
          ctx.globalAlpha = 0.55 * revealAlpha;
          ctx.drawImage(sprite, px - 5, py - 5, 10, 10);
          ctx.globalCompositeOperation = "source-over";
        }
        ctx.globalAlpha = revealAlpha;
        ctx.fillStyle = "rgba(255, 236, 225, 0.95)";
        ctx.beginPath();
        ctx.arc(px, py, 1.5, 0, Math.PI * 2);
        ctx.fill();
      }
    }
    ctx.setLineDash([]);

    // Hot core glows in the dark theme; light theme stays flat and readable.
    if (dark) {
      ctx.globalCompositeOperation = "lighter";
      for (const point of basePoints) {
        if (point.norm < 0.3) continue;
        const pos = reveal(point);
        if (pos.t <= 0.05) continue;
        const sprite = glowSprite(Math.round(point.norm * (GLOW_BUCKETS - 1)));
        if (!sprite) continue;
        const radius = point.r * (0.55 + 0.45 * pos.t) * 4.2;
        ctx.globalAlpha = (0.1 + 0.28 * point.norm) * pos.t;
        ctx.drawImage(sprite, pos.x - radius, pos.y - radius, radius * 2, radius * 2);
      }
      ctx.globalCompositeOperation = "source-over";
    }
    ctx.globalAlpha = 1;

    for (const point of sortForPaint(basePoints)) {
      const pos = reveal(point);
      ctx.globalAlpha = point.alpha * pos.t;
      ctx.fillStyle = point.color;
      ctx.beginPath();
      ctx.arc(pos.x, pos.y, point.r * (0.55 + 0.45 * pos.t), 0, Math.PI * 2);
      ctx.fill();
    }
    ctx.globalAlpha = 1;

    if (selectedKey) {
      const point = pointByKey.get(selectedKey);
      if (point) {
        const pos = reveal(point);
        const ring = point.r * (0.55 + 0.45 * pos.t) + 4;
        ctx.globalAlpha = Math.max(0.2, pos.t);
        ctx.strokeStyle = "rgba(0, 0, 0, 0.55)";
        ctx.lineWidth = 4;
        ctx.beginPath();
        ctx.arc(pos.x, pos.y, ring, 0, Math.PI * 2);
        ctx.stroke();
        ctx.strokeStyle = "rgba(255, 255, 255, 0.95)";
        ctx.lineWidth = 1.6;
        ctx.beginPath();
        ctx.arc(pos.x, pos.y, ring, 0, Math.PI * 2);
        ctx.stroke();
        ctx.globalAlpha = 1;
      }

      const ripple = rippleRef.current;
      if (!reduce && ripple && ripple.key === selectedKey) {
        const elapsed = now - ripple.start;
        if (elapsed >= 0 && elapsed < 700) {
          const point = pointByKey.get(selectedKey);
          if (point) {
            const pos = reveal(point);
            const p = elapsed / 700;
            ctx.globalAlpha = (1 - p) * 0.45;
            ctx.strokeStyle = "rgba(255, 255, 255, 0.9)";
            ctx.lineWidth = 1.5;
            ctx.beginPath();
            ctx.arc(pos.x, pos.y, point.r + 4 + p * 34, 0, Math.PI * 2);
            ctx.stroke();
            ctx.globalAlpha = 1;
          }
        }
      }
    }
  };
  drawRef.current = draw;

  // First non-empty snapshot: mark the entrance; the ambient loop draws it.
  useEffect(() => {
    if (enteredRef.current || basePoints.length === 0) return;
    enteredRef.current = true;
    if (reduceMotionRef.current) {
      entranceRef.current = { start: 0, active: false };
      drawRef.current();
      return;
    }
    entranceRef.current = { start: performance.now(), active: true };
  }, [basePoints.length]);

  useEffect(() => {
    drawRef.current();
  }, [basePoints, edges, size.w, size.h, selected, resolved]);

  // Selection ripple (one-shot; skipped under reduced motion).
  useEffect(() => {
    if (!selected) {
      rippleRef.current = null;
      return;
    }
    rippleRef.current = {
      start: performance.now(),
      key: subjectKey(selected.subject, selected.subjectKind),
    };
  }, [selected?.subject, selected?.subjectKind]);

  // Ambient loop: slow rotation + travelling light + ripple. ~30fps, paused
  // while the tab is hidden or a node is being dragged; absent under reduced
  // motion.
  useEffect(() => {
    if (prefersReducedMotion()) return;
    let raf = 0;
    let last = performance.now();
    let lastDraw = 0;
    const tick = (now: number) => {
      raf = window.requestAnimationFrame(tick);
      const dt = Math.min(64, Math.max(0, now - last));
      last = now;
      if (document.hidden) return;
      if (!dragRef.current) {
        rotationRef.current = (rotationRef.current + dt * ROTATION_PER_MS) % (Math.PI * 2);
      }
      if (now - lastDraw < FRAME_MS) return;
      lastDraw = now;
      drawRef.current(now);
    };
    raf = window.requestAnimationFrame(tick);
    return () => window.cancelAnimationFrame(raf);
  }, []);

  // Drop drag overrides for subjects that left the snapshot.
  useEffect(() => {
    const keys = new Set(pointByKey.keys());
    for (const key of [...overridesRef.current.keys()]) {
      if (!keys.has(key)) overridesRef.current.delete(key);
    }
  }, [pointByKey]);

  useEffect(
    () => () => {
      if (drawFrameRef.current !== null) window.cancelAnimationFrame(drawFrameRef.current);
      if (hoverFrameRef.current !== null) window.cancelAnimationFrame(hoverFrameRef.current);
    },
    [],
  );

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const onWheel = (event: WheelEvent) => {
      event.preventDefault();
      const rect = canvas.getBoundingClientRect();
      const sx = event.clientX - rect.left;
      const sy = event.clientY - rect.top;
      const view = viewRef.current;
      const wx = (sx - view.x) / view.k;
      const wy = (sy - view.y) / view.k;
      const next = Math.min(2.5, Math.max(0.5, view.k * Math.exp(-event.deltaY * 0.0015)));
      view.x = sx - wx * next;
      view.y = sy - wy * next;
      view.k = next;
      scheduleDraw();
    };
    canvas.addEventListener("wheel", onWheel, { passive: false });
    return () => canvas.removeEventListener("wheel", onWheel);
  }, [scheduleDraw]);

  function locate(event: React.PointerEvent<HTMLCanvasElement>) {
    const rect = event.currentTarget.getBoundingClientRect();
    return { x: event.clientX - rect.left, y: event.clientY - rect.top };
  }

  function scheduleHover(x: number, y: number) {
    hoverPointRef.current = { x, y };
    if (hoverFrameRef.current !== null) return;
    hoverFrameRef.current = window.requestAnimationFrame(() => {
      hoverFrameRef.current = null;
      const pointer = hoverPointRef.current;
      if (!pointer) return;
      const hit = hitAt(pointer.x, pointer.y);
      if (hit !== hoveredRef.current) hoveredRef.current = hit;
      setHover(hit ? { point: hit, x: pointer.x, y: pointer.y } : null);
      setCursor(hit ? "pointer" : "default");
    });
  }

  function handlePointerDown(event: React.PointerEvent<HTMLCanvasElement>) {
    event.preventDefault();
    const { x, y } = locate(event);
    event.currentTarget.setPointerCapture(event.pointerId);
    const hit = hitAt(x, y);
    setHover(null);
    hoveredRef.current = null;
    if (hit) {
      const key = subjectKey(hit.row.subject, hit.row.subject_kind ?? "");
      const pos = positionOf(hit);
      const world = logicalPoint(x, y);
      dragRef.current = {
        type: "node",
        key,
        offsetX: world.x - pos.x,
        offsetY: world.y - pos.y,
        startX: x,
        startY: y,
        moved: false,
      };
    } else {
      const view = viewRef.current;
      dragRef.current = {
        type: "pan",
        startX: x,
        startY: y,
        viewX: view.x,
        viewY: view.y,
        moved: false,
      };
    }
    setCursor("grab");
  }

  function handlePointerMove(event: React.PointerEvent<HTMLCanvasElement>) {
    const { x, y } = locate(event);
    const drag = dragRef.current;
    if (!drag) {
      scheduleHover(x, y);
      return;
    }
    if (Math.hypot(x - drag.startX, y - drag.startY) > 3) drag.moved = true;
    if (drag.type === "node") {
      const world = logicalPoint(x, y);
      overridesRef.current.set(drag.key, {
        x: world.x - drag.offsetX,
        y: world.y - drag.offsetY,
      });
    } else {
      const view = viewRef.current;
      view.x = drag.viewX + (x - drag.startX);
      view.y = drag.viewY + (y - drag.startY);
    }
    scheduleDraw();
  }

  function handlePointerUp() {
    const drag = dragRef.current;
    dragRef.current = null;
    setCursor("default");
    if (drag?.type === "node" && !drag.moved) {
      const point = pointByKey.get(drag.key);
      if (point) onSelect(point.row);
    }
    if (drag) scheduleDraw();
  }

  function handlePointerCancel() {
    dragRef.current = null;
    setCursor("default");
    scheduleDraw();
  }

  function handleLeave() {
    if (dragRef.current) return;
    if (hoverFrameRef.current !== null) {
      window.cancelAnimationFrame(hoverFrameRef.current);
      hoverFrameRef.current = null;
    }
    hoveredRef.current = null;
    setHover(null);
    setCursor("default");
  }

  function resetView() {
    overridesRef.current.clear();
    viewRef.current = { x: 0, y: 0, k: 1 };
    rotationRef.current = 0;
    scheduleDraw();
  }

  const hoverRelations = hover ? relationCounts(hover.point, edges) : null;
  const hoverStyle = hover ? stageStyle(stageOf(hover.point.row.counts)) : null;
  const tooltipLeft = hover
    ? Math.min(Math.max(hover.x + 14, 8), Math.max(8, size.w - 238))
    : 0;
  const tooltipTop = hover
    ? Math.min(Math.max(hover.y + 14, 8), Math.max(8, size.h - 96))
    : 0;

  return (
    <div
      ref={wrapRef}
      className="relative mx-auto h-[clamp(420px,calc(100vh_-_19rem),900px)] w-full max-w-[880px] overflow-hidden rounded-md border border-border bg-card"
    >
      <canvas
        ref={canvasRef}
        role="img"
        aria-label={t("info.map.canvasLabel")}
        className="absolute inset-0 h-full w-full touch-none"
        style={{ cursor }}
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={handlePointerUp}
        onPointerCancel={handlePointerCancel}
        onPointerLeave={handleLeave}
      />

      <Button
        type="button"
        size="sm"
        variant="ghost"
        className="absolute right-2 top-2 z-10 h-7 gap-1 px-2 text-[10px]"
        onClick={resetView}
      >
        <RotateCcw className="size-3.5" aria-hidden />
        {t("info.map.resetLayout")}
      </Button>

      {hover && hoverStyle && (
        <div
          className="pointer-events-none absolute z-20 w-[230px] rounded-md border border-border bg-card px-2.5 py-2 shadow-lg"
          style={{ left: tooltipLeft, top: tooltipTop }}
        >
          <div className="truncate font-mono text-[11px] text-foreground">
            {hover.point.row.subject}
          </div>
          <div className="mt-0.5 flex items-center gap-2 text-[10px] text-muted-foreground">
            <span className="truncate">
              {subjectKindLabel(t, hover.point.row.subject_kind ?? "")}
            </span>
            <span className="font-mono tabular-nums">
              {hover.point.row.total} {t("info.heatmap.items")}
            </span>
          </div>
          <div className="mt-1 flex items-center gap-1 text-[10px]">
            <span
              className="size-2 rounded-full"
              style={{ background: hoverStyle.bg }}
              aria-hidden
            />
            <span>{t(hoverStyle.labelKey)}</span>
            {hoverRelations && hoverRelations.total > 0 && (
              <span className="ml-auto font-mono tabular-nums text-muted-foreground">
                {hoverRelations.total} {t("info.map.relations")}
              </span>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function relationCounts(point: NebulaPoint, edges: InfoRelationEdge[]) {
  const key = subjectKey(point.row.subject, point.row.subject_kind ?? "");
  let incoming = 0;
  let outgoing = 0;
  for (const edge of edges) {
    if (subjectKey(edge.src_subject, edge.src_kind ?? "") === key) outgoing += 1;
    if (subjectKey(edge.dst_subject, edge.dst_kind ?? "") === key) incoming += 1;
  }
  return { incoming, outgoing, total: incoming + outgoing };
}
