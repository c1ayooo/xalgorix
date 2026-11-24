import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ArrowDown } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/states";
import { TranscriptView } from "@/components/transcript/transcript-view";
import { cn } from "@/lib/utils";
import { useInstanceActivity } from "@/lib/use-instance-activity";
import { fmtDuration } from "@/components/chat/format";
import { useTranscript } from "@/api/queries";
import { useEventsStore } from "@/store/events";
import { useI18n } from "@/i18n";
import type { TranscriptItem } from "@/types/api";

/** 转录行会因这些 SSE 事件而变化 → 防抖 refetch（契约 §0/§3.1：无乐观回显）。 */
const TRANSCRIPT_EVENT_TYPES = new Set([
  "assistant",
  "assistant_done",
  "message",
  "tool_call",
  "tool_result",
  "vuln_found",
  "vulns",
  "approval_requested",
  "approval_decided",
  "guidance_injected",
  "guidance_queued",
  "guidance_consumed",
  "chat_question",
  "chat_reply",
  "intent_proposed",
  "intent_proposal_resolved",
  "finished",
]);

function mergeBySeq(current: TranscriptItem[], incoming: TranscriptItem[]): TranscriptItem[] {
  const bySeq = new Map<number, TranscriptItem>();
  for (const item of current) bySeq.set(item.seq, item);
  for (const item of incoming) bySeq.set(item.seq, item);
  return [...bySeq.values()].sort((a, b) => a.seq - b.seq);
}

/**
 * Conversation transcript (data owner): newest page on open, scroll-up pulls
 * older pages while preserving the visual position, and live rows arrive by
 * debounced refetches driven by the SSE stream — never optimistic echoes.
 * Rendering is delegated to the shared pure view (transcript/transcript-view).
 * `filter` narrows the projection (sessions page agent lanes / system rows).
 */
export function Transcript({
  instanceId,
  running,
  limit = 80,
  filter,
  transform,
  agent,
  unit,
  human = false,
  liveAgentIds,
  className,
  empty,
  taskId,
  onEmptyChange,
  onEditMessage,
}: {
  instanceId?: string;
  running?: boolean;
  /** 页大小；与会话页 rail 共用 500 以命中同一查询缓存。 */
  limit?: number;
  filter?: (item: TranscriptItem) => boolean;
  /**
   * Worker lane: ask the server for this agent's rows only (filtered before
   * pagination). Without it, the newest page may contain nothing from a quiet
   * worker and the lane reads as "no activity" even though it ran.
   */
  agent?: string;
  /**
   * Work-unit lane: ask the server for every agent that ran this intent
   * (union, filtered before pagination). One unit = one worker across retries;
   * takes precedence over `agent` when both are present.
   */
  unit?: string;
  /**
   * Server-side human projection (human=1). The main-agent lane uses it so
   * pagination happens over conversation rows, not over worker tool noise.
   */
  human?: boolean;
  /**
   * Work-unit lane: the agent ids that belong to it. Live delta bubbles are
   * restricted to these ids, so a unit lane never shows another unit's typing.
   */
  liveAgentIds?: ReadonlySet<string>;
  /**
   * 行变换（过滤之后）：隐藏某类行，或把工具结果折叠成一条紧凑结果行。
   * 返回 null 表示不渲染该行。会话页主 Agent 栏用它保持"纯对话"。
   */
  transform?: (item: TranscriptItem) => TranscriptItem | null;
  className?: string;
  empty?: React.ReactNode;
  /** Platform task id for proposal receipt worker links. */
  taskId?: string;
  /** 空态上报（对话页据此把 composer 居中）；undefined=加载中，位置未决。 */
  onEmptyChange?: (empty: boolean | undefined) => void;
  /** Load a human turn back into the composer for editing and re-sending. */
  onEditMessage?: (text: string) => void;
}) {
  const { t } = useI18n();
  const [beforeSeq, setBeforeSeq] = useState<number | undefined>(undefined);
  const [pages, setPages] = useState<TranscriptItem[]>([]);
  const [jumpPill, setJumpPill] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);
  const atBottomRef = useRef(true);
  const seenCountRef = useRef(0);
  const newCountRef = useRef(0);
  const [newCount, setNewCount] = useState(0);
  const restoreRef = useRef<{ prevH: number; prevTop: number } | null>(null);
  const loadingMoreRef = useRef(false);
  const { data, isLoading, error, refetch } = useTranscript(
    instanceId,
    beforeSeq,
    limit,
    human,
    unit ? undefined : agent,
    unit,
  );

  // Reset when switching conversations or lanes: the older pages belong to
  // another run/agent/unit, and seq is renumbered per filtered view.
  useEffect(() => {
    setBeforeSeq(undefined);
    setPages([]);
    atBottomRef.current = true;
    seenCountRef.current = 0;
    newCountRef.current = 0;
    setNewCount(0);
    setJumpPill(false);
  }, [instanceId, agent, unit]);

  useEffect(() => {
    if (!data?.items?.length) return;
    setPages((prev) => mergeBySeq(prev, data.items));
  }, [data]);

  // Prepend restore: keep the visual position after older pages land.
  useLayoutEffect(() => {
    const restore = restoreRef.current;
    if (restore) {
      restoreRef.current = null;
      const node = scrollRef.current;
      if (node) node.scrollTop = restore.prevTop + (node.scrollHeight - restore.prevH);
    }
    const node = scrollRef.current;
    if (!node) return;
    if (atBottomRef.current) {
      node.scrollTop = node.scrollHeight;
      seenCountRef.current = pages.length;
    } else if (pages.length > seenCountRef.current) {
      newCountRef.current += pages.length - seenCountRef.current;
      seenCountRef.current = pages.length;
      setNewCount(newCountRef.current);
    }
  }, [pages]);

  // Scroll-up loads one older page (near top), preserving position; the pill
  // shows when the user scrolls away from the live edge (60px threshold).
  function handleScroll() {
    const node = scrollRef.current;
    if (!node) return;
    const nearBottom = node.scrollHeight - node.scrollTop - node.clientHeight < 60;
    atBottomRef.current = nearBottom;
    if (nearBottom) {
      newCountRef.current = 0;
      setNewCount(0);
    }
    setJumpPill(!nearBottom);
    if (node.scrollTop <= 80) void loadEarlier();
  }

  async function loadEarlier() {
    // beforeSeq === pages[0].seq means the request for that anchor is in flight.
    if (loadingMoreRef.current || !data?.has_more || pages.length === 0) return;
    if (beforeSeq !== undefined && beforeSeq === pages[0].seq) return;
    const node = scrollRef.current;
    if (!node) return;
    loadingMoreRef.current = true;
    restoreRef.current = { prevH: node.scrollHeight, prevTop: node.scrollTop };
    setBeforeSeq(pages[0].seq);
  }

  // beforeSeq is only ever set by loadEarlier, so "set and still fetching" = prepend hint.
  const loadingMore = beforeSeq !== undefined && isLoading;
  useEffect(() => {
    if (!isLoading) loadingMoreRef.current = false;
  }, [isLoading]);

  // SSE → debounced refetch bridge (runs only while mounted on this instance).
  const events = useEventsStore((s) => s.events);
  const seenEventsRef = useRef<number | null>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const refetchRef = useRef(refetch);
  refetchRef.current = refetch;
  useEffect(() => {
    if (!instanceId) return;
    let count = 0;
    for (const e of events) {
      const owner = e.instance_id || e.agent_id;
      if (owner === instanceId && TRANSCRIPT_EVENT_TYPES.has(e.type)) count++;
    }
    if (seenEventsRef.current === null) {
      seenEventsRef.current = count; // buffered history is covered by the initial fetch
      return;
    }
    if (count === seenEventsRef.current) return;
    seenEventsRef.current = count;
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => void refetchRef.current(), 300);
  }, [events, instanceId]);
  useEffect(
    () => () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
    },
    [],
  );

  const visible = useMemo(() => {
    const base = filter ? pages.filter(filter) : pages;
    if (!transform) return base;
    const out: TranscriptItem[] = [];
    for (const item of base) {
      const mapped = transform(item);
      if (mapped) out.push(mapped);
    }
    return out;
  }, [pages, filter, transform]);
  // 诚实流式三态（use-instance-activity）：running 投影只说明 run 未结束，
  // 不说明有数据在流动——指示器跟随真实事件年龄，而不是状态投影。
  const runInFlight = !!(running || data?.running);
  const lastActivityAt = useInstanceActivity(instanceId);
  const activityAgeMs = lastActivityAt !== null ? Date.now() - lastActivityAt : null;
  const streaming = runInFlight && activityAgeMs !== null && activityAgeMs <= 5000;
  const staleFor =
    runInFlight && activityAgeMs !== null && activityAgeMs > 5000
      ? fmtDuration(activityAgeMs)
      : null;
  const unobserved = runInFlight && activityAgeMs === null;
  const hasMore = data?.has_more ?? false;

  // 空态上报须在早退 return（skeleton/error）之前声明，保证各分支都在报告。
  // 首屏加载期间上报 undefined（位置未决），避免"先居中再跳底"。
  const emptiness: boolean | undefined =
    isLoading && pages.length === 0 ? undefined : !error && visible.length === 0;
  useEffect(() => {
    onEmptyChange?.(emptiness);
  }, [emptiness, onEmptyChange]);

  if (!instanceId) return null;
  if (isLoading && pages.length === 0) {
    return (
      <div className={cn("space-y-3 p-4", className)}>
        {Array.from({ length: 5 }).map((_, i) => (
          <Skeleton key={i} className={cn("h-12", i % 2 ? "w-2/3" : "w-4/5")} />
        ))}
      </div>
    );
  }
  if (error && pages.length === 0) {
    return (
      <div className={cn("p-6", className)}>
        <ErrorState
          description={(error as Error).message}
          action={
            <Button size="sm" variant="outline" onClick={() => void refetch()}>
              {t("common.retry")}
            </Button>
          }
        />
      </div>
    );
  }

  return (
    <div className={cn("relative flex min-h-0 flex-1 flex-col", className)}>
      <div ref={scrollRef} onScroll={handleScroll} className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
        {loadingMore && (
          <div className="flex items-center justify-center gap-2 pb-2 text-muted-foreground text-xs">
            {t("chat.loadingEarlier")}
          </div>
        )}
        {!loadingMore && hasMore && (
          <div className="pb-2 text-center text-muted-foreground/70 text-xs">{t("chat.scrollForEarlier")}</div>
        )}
        <TranscriptView
          items={visible}
          instanceId={instanceId}
          live={streaming}
          staleFor={staleFor}
          unobserved={unobserved}
          empty={empty}
          taskId={taskId}
          onEditMessage={onEditMessage}
        />
        <LiveAssistantTail instanceId={instanceId} filter={filter} agentIds={liveAgentIds} />
      </div>
      {jumpPill && (
        <div className="pointer-events-none absolute inset-x-0 bottom-2 flex justify-center">
          <Button
            size="sm"
            variant="secondary"
            className="pointer-events-auto h-7 rounded-full px-3 text-xs shadow"
            onClick={() => {
              atBottomRef.current = true;
              seenCountRef.current = pages.length;
              newCountRef.current = 0;
              setNewCount(0);
              setJumpPill(false);
              const node = scrollRef.current;
              if (node) node.scrollTop = node.scrollHeight;
            }}
          >
            <ArrowDown className="mr-1 size-3" aria-hidden />
            {t("chat.backToBottom")}
            {newCount > 0 && (
              <span className="ml-1 inline-flex min-w-4 items-center justify-center rounded-full bg-primary/15 px-1 text-[10px] font-medium tabular-nums">
                {newCount > 99 ? "99+" : newCount}
              </span>
            )}
          </Button>
        </div>
      )}
    </div>
  );
}

/**
 * Live streaming tail: the store's per-turn delta buffer (keyed by message_id)
 * rendered under the persisted rows. It disappears when assistant_done lands
 * and the transcript's own row takes over. The lane filter is the same one the
 * persisted rows use, so a worker's live text never leaks into the main view
 * (or into another worker's lane).
 */
function LiveAssistantTail({
  instanceId,
  filter,
  agentIds,
}: {
  instanceId?: string;
  filter?: (item: TranscriptItem) => boolean;
  agentIds?: ReadonlySet<string>;
}) {
  const streaming = useEventsStore((s) => s.streaming);
  const live = useMemo(() => {
    if (!instanceId) return [];
    return Object.values(streaming).filter((m) => {
      if (m.done || m.instance_id !== instanceId || !m.text.trim()) return false;
      if (agentIds) return !!m.agent_id && agentIds.has(m.agent_id);
      if (!filter) return true;
      const probe: TranscriptItem = {
        seq: 0,
        kind: "assistant",
        role: "agent",
        text: m.text,
        agent_id: m.agent_id,
        at: "",
      };
      return filter(probe);
    });
  }, [streaming, instanceId, filter]);

  const tail = live.length > 0 ? live[live.length - 1].text : "";
  const nodeRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const node = nodeRef.current;
    const scroller = node?.parentElement;
    if (!node || !scroller) return;
    const nearBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 120;
    if (nearBottom) scroller.scrollTop = scroller.scrollHeight;
  }, [tail]);

  if (live.length === 0) return null;
  return (
    <div ref={nodeRef} className="mt-1 mb-2 flex min-w-0 flex-col gap-1">
      {live.map((m) => (
        <div
          key={m.id}
          className="max-w-full whitespace-pre-wrap break-words rounded-md border border-border bg-muted/40 px-3 py-2 text-sm leading-6"
        >
          {m.text}
          <span className="ml-0.5 inline-block animate-pulse text-muted-foreground" aria-hidden>
            ▍
          </span>
        </div>
      ))}
    </div>
  );
}
