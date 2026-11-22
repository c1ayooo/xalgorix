import { useEffect, useMemo, useRef, useState } from "react";
import { ArrowUpToLine, Pause, Play, Search } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState, ErrorState } from "@/components/states";
import { useScanExploration } from "@/api/queries";
import { useI18n } from "@/i18n";
import {
  explorationDot,
  explorationShortID,
  matchesExplorationFilter,
  type ExplorationFilter,
} from "@/lib/exploration";
import { cn } from "@/lib/utils";
import type { ExplorationNode, ExplorationView } from "@/types/api";

/**
 * 「播报板」tab 装配（Run 级）：三态 + 播报流水。数据与探索链路图同源
 * （同一查询缓存），板是流水视角。
 */
export function BroadcastTab({
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
      <EmptyState title={t("board.noRunTitle")} description={t("ledger.noRunDescription")} />
    );
  }
  if (error) {
    return (
      <ErrorState
        title={t("board.loadFailed")}
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
      {data.nodes.length === 0 ? (
        <EmptyState
          title={t("board.emptyTitle")}
          description={data.journal_present ? t("board.emptyDescription") : t("explore.absentDescription")}
        />
      ) : (
        <BroadcastBoard view={data} />
      )}
    </div>
  );
}

/**
 * 播报板（流水视角）——与探索链路图读同一份 node/edge 投影：图是拓扑视角，
 * 板是时间轴流水。移植 4zreco 播报板的直播位纪律：自动刷新开（直播位）时
 * 新节点直接并入 + 「新」徽标高亮；暂停后轮询只累计未读数，渲染列表冻结
 * 不动（翻历史时内容不会在脚下变），点「回到最新」一次性追平。
 */

const PAGE_STEP = 100;

const FILTER_CHIPS: { key: ExplorationFilter; labelKey: string }[] = [
  { key: "all", labelKey: "board.filter.all" },
  { key: "goal", labelKey: "explore.kind.goal" },
  { key: "agent", labelKey: "explore.kind.agent" },
  { key: "hypothesis", labelKey: "explore.kind.hypothesis" },
  { key: "fact", labelKey: "board.filter.fact" },
  { key: "finding", labelKey: "explore.kind.finding" },
];

interface RowProps {
  node: ExplorationNode;
  fresh: boolean;
  neighbors: { up: ExplorationNode[]; down: ExplorationNode[] };
  onSelect: (id: string) => void;
}

function BoardRow({ node, fresh, neighbors, onSelect }: RowProps) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const time = new Date(node.at);
  return (
    <li>
      <div
        className={cn(
          "grid grid-cols-[4.25rem_1.5rem_1fr] gap-x-1 px-3",
          fresh && "bg-primary/5",
        )}
      >
        {/* 时间列 */}
        <div className="py-2 text-right">
          <p className="font-mono text-[10px] tabular-nums text-muted-foreground">
            {time.toLocaleTimeString("zh-CN", { hour12: false })}
          </p>
        </div>
        {/* 时间轴凹槽 */}
        <div className="relative flex justify-center">
          <span aria-hidden className="absolute inset-y-0 w-px bg-border" />
          <span
            aria-hidden
            className={cn(
              "relative z-10 mt-3 size-2.5 rounded-full ring-4 ring-background",
            )}
            style={{ background: explorationDot(node) }}
          />
        </div>
        {/* 内容列 */}
        <div className="min-w-0 py-2">
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            className="flex w-full flex-wrap items-center gap-1.5 text-left"
            aria-expanded={open}
          >
            <span className="rounded-sm border border-border/70 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-muted-foreground">
              {t(`explore.kind.${node.kind}`)}
            </span>
            <span className="font-mono text-[10px] text-muted-foreground">
              #{explorationShortID(node)}
            </span>
            {node.state && node.kind === "hypothesis" && (
              <span className="rounded-sm border border-border/70 px-1.5 py-0.5 text-[10px] text-muted-foreground">
                {t(`explore.state.${node.state}`)}
              </span>
            )}
            {node.kind === "finding" && node.severity && (
              <span className="rounded-sm border border-border/70 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
                {node.severity}
              </span>
            )}
            {fresh && (
              <span className="rounded-sm bg-primary px-1.5 py-0.5 text-[10px] font-medium text-primary-foreground">
                {t("board.fresh")}
              </span>
            )}
            <span className="ml-auto font-mono text-[10px] text-muted-foreground">
              {node.actor || ""}
            </span>
          </button>
          <p className={cn("mt-0.5 text-xs leading-relaxed", !open && "line-clamp-2")}>
            {node.title || node.summary || explorationShortID(node)}
          </p>
          {node.summary && node.title && open && (
            <p className="mt-1 whitespace-pre-wrap rounded-sm border border-border/70 bg-muted/30 p-2 text-xs leading-relaxed">
              {node.summary}
            </p>
          )}
          {open && (
            <div className="mt-1.5 space-y-1 border-l-2 border-border pl-2.5 text-[11px] text-muted-foreground">
              <p className="font-mono">
                {t("board.meta.seq")} #{node.seq} ·{" "}
                {time.toLocaleString()}
              </p>
              {neighbors.up.length > 0 && (
                <p>
                  {t("board.meta.upstream")}{" "}
                  {neighbors.up.map((n) => (
                    <button
                      key={n.id}
                      type="button"
                      onClick={() => onSelect(n.id)}
                      className="mr-1 rounded-sm border border-border/70 px-1 py-0.5 font-mono text-[10px] transition-colors hover:text-foreground"
                    >
                      #{explorationShortID(n)}
                    </button>
                  ))}
                </p>
              )}
              {neighbors.down.length > 0 && (
                <p>
                  {t("board.meta.downstream")}{" "}
                  {neighbors.down.map((n) => (
                    <button
                      key={n.id}
                      type="button"
                      onClick={() => onSelect(n.id)}
                      className="mr-1 rounded-sm border border-border/70 px-1 py-0.5 font-mono text-[10px] transition-colors hover:text-foreground"
                    >
                      #{explorationShortID(n)}
                    </button>
                  ))}
                </p>
              )}
            </div>
          )}
        </div>
      </div>
    </li>
  );
}

export function BroadcastBoard({ view }: { view: ExplorationView }) {
  const { t } = useI18n();
  // 直播位纪律状态：frozen = 自动刷新关（渲染列表冻结）；seenRef 追踪已渲染
  // 节点，diff 出「新」徽标；暂停期间轮询只累计 pending 不动列表。
  const [frozen, setFrozen] = useState(false);
  const [rendered, setRendered] = useState<ExplorationNode[]>([]);
  const [freshIDs, setFreshIDs] = useState<Set<string>>(() => new Set());
  const [pending, setPending] = useState(0);
  const seenRef = useRef<Set<string>>(new Set());
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<ExplorationFilter>("all");
  const [windowSize, setWindowSize] = useState(PAGE_STEP);

  const nodes = useMemo(() => [...view.nodes].sort((a, b) => b.seq - a.seq), [view.nodes]);
  const byID = useMemo(() => new Map(view.nodes.map((n) => [n.id, n])), [view.nodes]);

  useEffect(() => {
    if (frozen) {
      // 暂停位：轮询只累计未读数，渲染列表原地不动。
      const fresh = nodes.filter((n) => !seenRef.current.has(n.id));
      setPending(fresh.length);
      return;
    }
    // 直播位：整体替换 + diff 出新节点。
    const fresh = new Set(nodes.filter((n) => !seenRef.current.has(n.id)).map((n) => n.id));
    for (const n of nodes) seenRef.current.add(n.id);
    setRendered(nodes);
    setPending(0);
    if (fresh.size > 0) setFreshIDs(fresh);
  }, [nodes, frozen]);

  function backToLive() {
    setFrozen(false);
  }

  // 首次渲染基线：首包全部视为已读，不标「新」。
  useEffect(() => {
    if (seenRef.current.size === 0 && view.nodes.length > 0) {
      for (const n of view.nodes) seenRef.current.add(n.id);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const q = query.trim().toLowerCase();
  const filtered = rendered.filter(
    (n) =>
      matchesExplorationFilter(n, filter) &&
      (q === "" ||
        n.title.toLowerCase().includes(q) ||
        (n.summary ?? "").toLowerCase().includes(q) ||
        explorationShortID(n).toLowerCase().includes(q)),
  );
  const visible = filtered.slice(0, windowSize);

  // 日期分组（顺序遍历，跨天插组头）。
  const groups: { day: string; items: ExplorationNode[] }[] = [];
  for (const node of visible) {
    const day = new Date(node.at).toLocaleDateString(undefined, {
      month: "long",
      day: "numeric",
    });
    const last = groups[groups.length - 1];
    if (last && last.day === day) last.items.push(node);
    else groups.push({ day, items: [node] });
  }

  const neighborIDs = (node: ExplorationNode) => {
    const up: ExplorationNode[] = [];
    const down: ExplorationNode[] = [];
    for (const e of view.edges) {
      if (e.dst === node.id) {
        const n = byID.get(e.src);
        if (n) up.push(n);
      }
      if (e.src === node.id) {
        const n = byID.get(e.dst);
        if (n) down.push(n);
      }
    }
    return { up, down };
  };

  return (
    <div className="space-y-3">
      {/* 工具栏：搜索 + kind 过滤 + 自动刷新开关。 */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative">
          <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" aria-hidden />
          <Input
            className="h-8 w-56 pl-7"
            placeholder={t("board.searchPlaceholder")}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        </div>
        {FILTER_CHIPS.map((chip) => (
          <button
            key={chip.key}
            type="button"
            onClick={() => setFilter(chip.key)}
            className={cn(
              "rounded-full border px-2.5 py-0.5 text-xs transition-colors",
              filter === chip.key
                ? "border-foreground bg-foreground text-background"
                : "border-border text-muted-foreground hover:text-foreground",
            )}
          >
            {t(chip.labelKey)}
          </button>
        ))}
        <Button
          type="button"
          size="sm"
          variant={frozen ? "outline" : "ghost"}
          className={cn("ml-auto h-7 gap-1 px-2 text-xs", frozen && "text-muted-foreground")}
          onClick={() => (frozen ? backToLive() : setFrozen(true))}
          aria-pressed={frozen}
        >
          {frozen ? <Play className="size-3" aria-hidden /> : <Pause className="size-3" aria-hidden />}
          {frozen ? t("board.live.paused") : t("board.live.on")}
        </Button>
      </div>

      {/* 未读横幅：暂停位轮询只累计计数，点按一次性追平。 */}
      {frozen && pending > 0 && (
        <button
          type="button"
          onClick={backToLive}
          className="flex w-full items-center gap-2 rounded-sm border border-primary/30 bg-primary/10 px-3 py-2 text-xs text-primary transition-colors hover:bg-primary/15"
        >
          <ArrowUpToLine className="size-3.5" aria-hidden />
          {pending > 99 ? "99+" : pending} {t("board.pending")}
        </button>
      )}

      {/* 诚实标志（与投影端点同源）。 */}
      {view.incomplete && (
        <p className="rounded-sm border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-600 dark:text-amber-400">
          {view.note || t("explore.incomplete")}
        </p>
      )}

      {/* 日期分组时间轴。 */}
      {visible.length === 0 ? (
        <p className="px-1 py-6 text-center text-xs text-muted-foreground">
          {rendered.length === 0 ? t("board.empty") : t("board.filterEmpty")}
        </p>
      ) : (
        <div className="overflow-hidden rounded-md border border-border">
          {groups.map((group) => (
            <div key={group.day}>
              <p className="border-b border-border bg-muted/30 px-4 py-1.5 text-[11px] text-muted-foreground">
                {group.day}
              </p>
              <ul>
                {group.items.map((node) => (
                  <BoardRow
                    key={node.id}
                    node={node}
                    fresh={freshIDs.has(node.id)}
                    neighbors={neighborIDs(node)}
                    onSelect={(id) => {
                      // 点击关联节点 = 回到直播位并清过滤，让目标行可见。
                      setQuery("");
                      setFilter("all");
                      if (frozen) backToLive();
                      if (!seenRef.current.has(id)) seenRef.current.add(id);
                    }}
                  />
                ))}
              </ul>
            </div>
          ))}
        </div>
      )}

      {/* 页脚：窗口分页计数 + 加载更早。 */}
      {filtered.length > 0 && (
        <div className="flex items-center justify-between text-[11px] text-muted-foreground">
          <span className="font-mono tabular-nums">
            {t("board.showing")} {visible.length} / {filtered.length}
          </span>
          {filtered.length > visible.length && (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="h-6 px-2 text-xs text-muted-foreground"
              onClick={() => setWindowSize((w) => w + PAGE_STEP)}
            >
              {t("board.loadEarlier")}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
