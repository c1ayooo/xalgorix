import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { RotateCcw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";
import {
  edgeBowSign,
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

/**
 * Project information nebula (CORE cold/hot, volume heat) + subject graph.
 *
 * One subject = one point. Distance from the centre, size and colour encode
 * information volume; edges are real relations from the unified space. Nodes
 * are draggable, the canvas pans on empty space and the wheel zooms. The canvas
 * repaints from refs during gestures (no React state per pointer move); hover
 * only moves a DOM tooltip.
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
  const dragRef = useRef<DragState>(null);
  const hoveredRef = useRef<NebulaPoint | null>(null);
  const hoverFrameRef = useRef<number | null>(null);
  const hoverPointRef = useRef<{ x: number; y: number } | null>(null);
  const drawFrameRef = useRef<number | null>(null);
  const drawRef = useRef<() => void>(() => {});
  const entranceRef = useRef<{ start: number; active: boolean }>({
    start: 0,
    active: false,
  });
  const enteredRef = useRef(false);
  const animFrameRef = useRef<number | null>(null);

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

  function hitAt(screenX: number, screenY: number): NebulaPoint | null {
    const view = viewRef.current;
    const wx = (screenX - view.x) / view.k;
    const wy = (screenY - view.y) / view.k;
    let best: NebulaPoint | null = null;
    let bestDistance = Number.POSITIVE_INFINITY;
    for (const point of basePoints) {
      const pos = positionOf(point);
      const distance = Math.hypot(wx - pos.x, wy - pos.y);
      const limit = Math.max(12, point.r + 7);
      if (distance <= limit && distance < bestDistance) {
        best = point;
        bestDistance = distance;
      }
    }
    return best;
  }

  // Draw reads positions/view/selection from refs so gestures never re-render.
  const draw = () => {
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
    ctx.lineCap = "round";

    const selectedKey = selected
      ? subjectKey(selected.subject, selected.subjectKind)
      : null;

    // Entrance: points bloom out of the centre (cold core first, hot spikes
    // last). After the first snapshot it never replays on a refresh.
    const entrance = entranceRef.current;
    const progress = entrance.active
      ? Math.min(1, (performance.now() - entrance.start) / 520)
      : 1;
    const centerX = size.w / 2;
    const centerY = size.h / 2;
    const rendered = new Map<NebulaPoint, { x: number; y: number; t: number }>();
    const reveal = (point: NebulaPoint) => {
      const cached = rendered.get(point);
      if (cached) return cached;
      const pos = positionOf(point);
      let t = 1;
      if (entrance.active) {
        const raw = Math.min(1, Math.max(0, (progress - 0.15 * point.norm) / 0.85));
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
    }
    ctx.setLineDash([]);

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
    }
  };
  drawRef.current = draw;

  // First non-empty snapshot: bloom from the core for ~520ms.
  useEffect(() => {
    if (enteredRef.current || basePoints.length === 0) return;
    enteredRef.current = true;
    const reduce =
      typeof window !== "undefined" &&
      window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
    if (reduce) {
      entranceRef.current = { start: 0, active: false };
      drawRef.current();
      return;
    }
    entranceRef.current = { start: performance.now(), active: true };
    const tick = () => {
      if (!entranceRef.current.active) return;
      drawRef.current();
      if (performance.now() - entranceRef.current.start >= 620) {
        entranceRef.current.active = false;
        animFrameRef.current = null;
        drawRef.current();
        return;
      }
      animFrameRef.current = window.requestAnimationFrame(tick);
    };
    animFrameRef.current = window.requestAnimationFrame(tick);
  }, [basePoints.length]);

  useEffect(() => {
    drawRef.current();
  }, [basePoints, edges, size.w, size.h, selected]);

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
      if (animFrameRef.current !== null) {
        window.cancelAnimationFrame(animFrameRef.current);
        animFrameRef.current = null;
      }
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
      const view = viewRef.current;
      const wx = (x - view.x) / view.k;
      const wy = (y - view.y) / view.k;
      dragRef.current = {
        type: "node",
        key,
        offsetX: wx - pos.x,
        offsetY: wy - pos.y,
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
      const view = viewRef.current;
      const wx = (x - view.x) / view.k;
      const wy = (y - view.y) / view.k;
      overridesRef.current.set(drag.key, {
        x: wx - drag.offsetX,
        y: wy - drag.offsetY,
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
