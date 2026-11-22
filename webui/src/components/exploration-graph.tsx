import { useMemo, useState } from "react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState, ErrorState } from "@/components/states";
import { useScanExploration } from "@/api/queries";
import { useI18n } from "@/i18n";
import { explorationDot, explorationShortID } from "@/lib/exploration";
import { cn } from "@/lib/utils";
import type { ExplorationKind, ExplorationNode, ExplorationView } from "@/types/api";

/**
 * 「探索链路」tab 装配（Run 级，跟随 ?run= 选择）：三态 + 投影诚实标志 +
 * 拓扑图。数据与播报板同源（同一查询缓存），图是拓扑视角。
 */
export function ExplorationTab({
  scanId,
  running,
}: {
  scanId?: string;
  running?: boolean;
}) {
  const { t } = useI18n();
  const { data, isLoading, error, refetch } = useScanExploration(scanId ?? "", running);

  if (!scanId) {
    return (
      <EmptyState title={t("explore.noRunTitle")} description={t("ledger.noRunDescription")} />
    );
  }
  if (error) {
    return (
      <ErrorState
        title={t("explore.loadFailed")}
        description={error instanceof Error ? error.message : ""}
        action={
          <button
            type="button"
            onClick={() => refetch()}
            className="text-xs text-muted-foreground transition-colors hover:text-foreground"
          >
            {t("common.retry")}
          </button>
        }
      />
    );
  }
  if (isLoading && !data) {
    return <Skeleton className="h-72 w-full" />;
  }
  if (!data) return null;

  return (
    <div className="space-y-3">
      {data.incomplete && (
        <p className="rounded-sm border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-600 dark:text-amber-400">
          {data.note || t("explore.incomplete")}
        </p>
      )}
      {data.nodes.length === 0 ? (
        <EmptyState
          title={t("explore.emptyTitle")}
          description={data.journal_present ? t("explore.emptyDescription") : t("explore.absentDescription")}
        />
      ) : (
        <ExplorationGraph view={data} />
      )}
    </div>
  );
}

/**
 * 探索链路图（拓扑视角）——探索投影的 node/edge 模型的分层 SVG 渲染
 * （association-graph 房规：无新图库、确定性布局、legend 全列零计数置灰、
 * 无障碍平行列表）。固定五列 = 七环的领域链路：起点 → 执行(Eino) → 假设 →
 * 事实 → 发现；只画投影里结构存在的边，不软匹配不编造。
 */

const COL_W = 230;
const CARD_W = 196;
const CARD_H = 62;
const ROW_H = 78;
const MARGIN_X = 24;
const HEADER_H = 34;

/** 固定列序（observation 与 evidence 同居事实列）。 */
const COL_ORDER: ExplorationKind[] = [
  "goal",
  "agent",
  "hypothesis",
  "evidence",
  "finding",
];

const REL_STROKE: Record<string, string> = {
  claim: "#0ea5e9",
  yields: "#10b981",
  proves: "#f59e0b",
  supports: "#8b5cf6",
};

const REL_LEGEND: { rel: string; key: string }[] = [
  { rel: "claim", key: "explore.rel.claim" },
  { rel: "yields", key: "explore.rel.yields" },
  { rel: "proves", key: "explore.rel.proves" },
  { rel: "supports", key: "explore.rel.supports" },
];

interface Positioned extends ExplorationNode {
  col: number;
  x: number;
  y: number;
}

export function ExplorationGraph({ view }: { view: ExplorationView }) {
  const { t } = useI18n();
  const [selectedID, setSelectedID] = useState<string | null>(null);

  const { positioned, edges, width, height, relCounts } = useMemo(() => {
    const colOf = (kind: ExplorationKind): number => {
      if (kind === "observation") return COL_ORDER.indexOf("evidence");
      return COL_ORDER.indexOf(kind);
    };
    const byCol = new Map<number, ExplorationNode[]>();
    for (const node of view.nodes) {
      const col = colOf(node.kind);
      if (col < 0) continue;
      const list = byCol.get(col) ?? [];
      list.push(node);
      byCol.set(col, list);
    }
    const positioned: Positioned[] = [];
    for (const [col, list] of byCol) {
      list.forEach((node, i) => {
        positioned.push({
          ...node,
          col,
          x: MARGIN_X + col * COL_W,
          y: HEADER_H + i * ROW_H,
        });
      });
    }
    const posByID = new Map(positioned.map((n) => [n.id, n]));
    const edges = view.edges
      .map((edge) => {
        const from = posByID.get(edge.src);
        const to = posByID.get(edge.dst);
        if (!from || !to) return null;
        return { from, to, rel: edge.rel };
      })
      .filter((e): e is NonNullable<typeof e> => e !== null);
    const relCounts: Record<string, number> = {};
    for (const e of edges) relCounts[e.rel] = (relCounts[e.rel] ?? 0) + 1;
    const maxRows = Math.max(1, ...COL_ORDER.map((_, col) => (byCol.get(col)?.length ?? 0)));
    const width = MARGIN_X + COL_ORDER.length * COL_W + 24;
    const height = Math.max(200, HEADER_H + maxRows * ROW_H + 8);
    return { positioned, edges, width, height, relCounts };
  }, [view]);

  const posByID = useMemo(() => new Map(positioned.map((n) => [n.id, n])), [positioned]);
  const selected = selectedID ? posByID.get(selectedID) : null;
  const neighbors = useMemo(() => {
    if (!selected) return { up: [] as ExplorationNode[], down: [] as ExplorationNode[] };
    const up: ExplorationNode[] = [];
    const down: ExplorationNode[] = [];
    for (const e of view.edges) {
      if (e.dst === selected.id) {
        const n = posByID.get(e.src);
        if (n) up.push(n);
      }
      if (e.src === selected.id) {
        const n = posByID.get(e.dst);
        if (n) down.push(n);
      }
    }
    return { up, down };
  }, [selected, posByID, view.edges]);

  return (
    <div className="space-y-3">
      <div className="overflow-x-auto rounded-md border border-border bg-background/40">
        <svg
          role="img"
          aria-label={t("explore.graphLabel")}
          viewBox={`0 0 ${width} ${height}`}
          className="min-w-[880px]"
        >
          {/* 列头 = 领域链路阶段。 */}
          {COL_ORDER.map((kind, col) => (
            <text
              key={kind}
              x={MARGIN_X + col * COL_W + 8}
              y={20}
              className="fill-muted-foreground text-[10px] uppercase tracking-wide"
            >
              {t(`explore.col.${kind}`)}
            </text>
          ))}
          {edges.map((edge, i) => {
            // 右缘中点 → 左缘中点的三次贝塞尔（同列/相邻列均可读）。
            const x1 = edge.from.x + CARD_W;
            const y1 = edge.from.y + CARD_H / 2;
            const x2 = edge.to.x;
            const y2 = edge.to.y + CARD_H / 2;
            const mid = (x1 + x2) / 2;
            return (
              <path
                key={`e-${i}`}
                d={`M ${x1} ${y1} C ${mid} ${y1}, ${mid} ${y2}, ${x2} ${y2}`}
                fill="none"
                stroke={REL_STROKE[edge.rel] ?? "#94a3b8"}
                strokeWidth={1.4}
                strokeOpacity={0.75}
              />
            );
          })}
          {positioned.map((node) => (
            <g
              key={node.id}
              tabIndex={0}
              role="listitem"
              aria-label={`${node.title} · ${t(`explore.kind.${node.kind}`)}`}
              className="cursor-pointer outline-none"
              onClick={() => setSelectedID(node.id)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  setSelectedID(node.id);
                }
              }}
            >
              <rect
                x={node.x}
                y={node.y}
                width={CARD_W}
                height={CARD_H}
                rx={6}
                className="fill-card stroke-border"
                strokeWidth={1}
              />
              <circle cx={node.x + 12} cy={node.y + 14} r={4} fill={explorationDot(node)} />
              <text x={node.x + 22} y={node.y + 18} className="fill-foreground text-[11px] font-medium">
                {truncate(node.title || explorationShortID(node), 20)}
              </text>
              <text x={node.x + 10} y={node.y + 36} className="fill-muted-foreground font-mono text-[9px]">
                {truncate(explorationShortID(node), 24)}
              </text>
              <text x={node.x + 10} y={node.y + 52} className="fill-muted-foreground text-[9px]">
                {truncate(
                  [
                    node.state && node.kind !== "finding" ? t(`explore.state.${node.state}`) : "",
                    node.kind === "finding" && node.severity ? node.severity : "",
                    node.up > 0 ? `↑${node.up}` : "",
                    node.down > 0 ? `↓${node.down}` : "",
                  ]
                    .filter(Boolean)
                    .join(" · "),
                  30,
                )}
              </text>
            </g>
          ))}
        </svg>
      </div>

      {/* Legend: 边词表全列，零计数置灰（词表即契约——association-graph 同规）。 */}
      <div className="border-t border-border pt-3">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5" aria-label={t("explore.legendLabel")}>
          {REL_LEGEND.map((entry) => {
            const count = relCounts[entry.rel] ?? 0;
            return (
              <span
                key={entry.rel}
                className="inline-flex items-center gap-1.5 text-xs text-muted-foreground"
              >
                <span
                  aria-hidden
                  className={cn("h-0.5 w-3 rounded-full", count === 0 && "opacity-40")}
                  style={{ background: REL_STROKE[entry.rel] }}
                />
                {t(entry.key)}
                <span className="font-mono text-[10px] tabular-nums">{count}</span>
              </span>
            );
          })}
        </div>
      </div>

      {/* 无障碍平行列表（图的主键盘/读屏面）。 */}
      <ul className="space-y-1.5" aria-label={t("explore.listLabel")}>
        {view.nodes.map((node) => (
          <li key={node.id}>
            <button
              type="button"
              onClick={() => setSelectedID(node.id)}
              className="flex w-full flex-wrap items-center gap-2 rounded-sm border border-border/70 px-2.5 py-1.5 text-left text-xs transition-colors hover:bg-accent/40"
            >
              <span aria-hidden className={cn("size-1.5 shrink-0 rounded-full")} style={{ background: explorationDot(node) }} />
              <span className="text-[10px] uppercase tracking-wide text-muted-foreground">
                {t(`explore.kind.${node.kind}`)}
              </span>
              <span className="min-w-0 flex-1 truncate">{node.title || explorationShortID(node)}</span>
              <span className="font-mono text-[10px] text-muted-foreground">{explorationShortID(node)}</span>
            </button>
          </li>
        ))}
      </ul>

      {/* 节点详情：属性 + 上游/下游（可点击切换选中）。 */}
      <Dialog open={!!selected} onOpenChange={(open) => !open && setSelectedID(null)}>
        <DialogContent className="sm:max-w-md">
          {selected && (
            <>
              <DialogHeader>
                <DialogTitle className="flex items-center gap-2">
                  <span aria-hidden className="size-2 rounded-full" style={{ background: explorationDot(selected) }} />
                  {selected.title || explorationShortID(selected)}
                </DialogTitle>
                <DialogDescription className="font-mono text-[11px]">
                  {explorationShortID(selected)} · {t(`explore.kind.${selected.kind}`)}
                </DialogDescription>
              </DialogHeader>
              <div className="space-y-2 text-xs">
                <dl className="space-y-1">
                  {selected.state && (
                    <DetailRow label={t("explore.detail.state")} value={t(`explore.state.${selected.state}`)} />
                  )}
                  {selected.severity && (
                    <DetailRow label={t("explore.detail.severity")} value={selected.severity} />
                  )}
                  {selected.nature && (
                    <DetailRow
                      label={t("explore.detail.nature")}
                      value={t(selected.nature === "observed" ? "info.nature.observed" : "info.nature.inferred")}
                    />
                  )}
                  {selected.actor && (
                    <DetailRow label={t("explore.detail.actor")} value={selected.actor} mono />
                  )}
                  <DetailRow label={t("explore.detail.seq")} value={`#${selected.seq}`} mono />
                  <DetailRow label={t("explore.detail.at")} value={new Date(selected.at).toLocaleString()} mono />
                </dl>
                {selected.summary && (
                  <p className="whitespace-pre-wrap rounded-sm border border-border/70 bg-muted/30 p-2 leading-relaxed">
                    {selected.summary}
                  </p>
                )}
                {(neighbors.up.length > 0 || neighbors.down.length > 0) && (
                  <div className="space-y-1 border-t border-border pt-2">
                    {neighbors.up.length > 0 && (
                      <NeighborRow
                        label={t("explore.detail.upstream")}
                        nodes={neighbors.up}
                        onSelect={setSelectedID}
                      />
                    )}
                    {neighbors.down.length > 0 && (
                      <NeighborRow
                        label={t("explore.detail.downstream")}
                        nodes={neighbors.down}
                        onSelect={setSelectedID}
                      />
                    )}
                  </div>
                )}
              </div>
            </>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}

function DetailRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex gap-3">
      <span className="w-16 shrink-0 text-muted-foreground">{label}</span>
      <span className={cn("min-w-0 break-all", mono && "font-mono text-[11px]")}>{value}</span>
    </div>
  );
}

function NeighborRow({
  label,
  nodes,
  onSelect,
}: {
  label: string;
  nodes: ExplorationNode[];
  onSelect: (id: string) => void;
}) {
  const { t } = useI18n();
  return (
    <p className="flex flex-wrap items-center gap-1.5">
      <span className="text-muted-foreground">{label}</span>
      {nodes.map((n) => (
        <button
          key={n.id}
          type="button"
          onClick={() => onSelect(n.id)}
          className="rounded-sm border border-border/70 px-1.5 py-0.5 font-mono text-[10px] transition-colors hover:bg-accent/40"
          title={n.title || n.id}
        >
          {explorationShortID(n)}
        </button>
      ))}
      <span className="font-mono text-[10px] text-muted-foreground">
        {nodes.length} {t("explore.detail.nodes")}
      </span>
    </p>
  );
}

function truncate(s: string, max: number): string {
  return s.length > max ? `${s.slice(0, max)}…` : s;
}
