import { useEffect, useMemo, useRef } from "react";
import { Bot, Copy, Loader2, Pencil, User } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState, ErrorState } from "@/components/states";
import { IntentProposalCard } from "@/components/chat/intent-proposal-card";
import { Markdown } from "@/components/markdown";
import { useTranscript } from "@/api/queries";
import { useAuth } from "@/store/auth";
import { useEventsStore } from "@/store/events";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type { TranscriptItem } from "@/types/api";

const LIVE_TURN_EVENTS = new Set([
  "chat_question",
  "chat_reply",
  "guidance_injected",
  "guidance_queued",
  "guidance_consumed",
  // Main-agent replies and intent drafts are part of the human conversation.
  "message",
  "assistant",
  "assistant_done",
  "intent_proposed",
  "intent_proposal_resolved",
]);

/**
 * 人工对话投影（CORE_PHILOSOPHY §3.2/§8，REDESIGN §5.1）。
 *
 * 干净是结构保证的：只读同一份持久化转录的人工切片（`?human=1`）——人工回合
 * （guidance 与旁路提问）、旁路回答、主 Agent 回复（role=main）与意图提案卡。
 * 工具调用、计划、worker、生命周期行永远不会进入这一页。新回合经 /api/stream
 * （chat_question / chat_reply / guidance_* / message / intent_proposed）触发防抖
 * refetch 到达，不做乐观回显。
 */
export function UserConversation({
  instanceId,
  taskId,
  pending,
  className,
  onEmptyChange,
  onEditMessage,
}: {
  instanceId: string;
  /** Platform task id, passed to receipt cards for the worker deep link. */
  taskId?: string;
  /** 旁路提问在途：回答尚未回流时占一行，避免"发出去没反应"的观感。 */
  pending?: boolean;
  className?: string;
  /** 空态上报（对话页据此把 composer 居中）；undefined=加载中，位置未决。 */
  onEmptyChange?: (empty: boolean | undefined) => void;
  /** Load a user turn back into the composer for editing and re-sending. */
  onEditMessage?: (text: string) => void;
}) {
  const { t } = useI18n();
  const { data, isLoading, error, refetch } = useTranscript(instanceId, undefined, 200, true);
  const events = useEventsStore((state) => state.events);
  const streaming = useEventsStore((state) => state.streaming);
  // Root/main deltas carry no agent_id (worker deltas do). Show the main
  // agent's live text here so the human conversation has a typewriter effect;
  // the persisted projection replaces it when assistant_done triggers refetch.
  const liveMain = useMemo(
    () =>
      Object.values(streaming).filter(
        (message) => message.instance_id === instanceId && !message.agent_id && message.text,
      ),
    [streaming, instanceId],
  );
  const authEnabled = useAuth((state) => state.authEnabled);
  const authStatus = useAuth((state) => state.status);
  const user = useAuth((state) => state.user);
  const scrollRef = useRef<HTMLDivElement>(null);
  const stayNearBottom = useRef(true);

  // Live bridge: SSE never renders directly here (no optimistic echo) — a new
  // chat/guidance turn invalidates the projection and the refetch renders it.
  const liveCount = useMemo(
    () => events.filter((event) => event.instance_id === instanceId && LIVE_TURN_EVENTS.has(event.type)).length,
    [events, instanceId],
  );
  const seenLive = useRef(liveCount);
  useEffect(() => {
    if (liveCount <= seenLive.current) {
      seenLive.current = liveCount;
      return;
    }
    seenLive.current = liveCount;
    const timer = setTimeout(() => void refetch(), 350);
    return () => clearTimeout(timer);
  }, [liveCount, refetch]);

  const messages = useMemo(() => {
    const isOwn = (createdBy?: string) =>
      !authEnabled || !createdBy || (!!user?.username && createdBy === user.username);
    return (data?.items ?? []).filter((item) => {
      // Agent output (main reply / proposal) is not attributed to an operator.
      if (item.kind === "proposal" || item.role === "main") return true;
      if (item.kind === "human" || item.role === "chat") return isOwn(item.created_by);
      return false;
    });
  }, [authEnabled, data?.items, user?.username]);

  useEffect(() => {
    if (!stayNearBottom.current) return;
    const frame = requestAnimationFrame(() => {
      const node = scrollRef.current;
      if (node) node.scrollTop = node.scrollHeight;
    });
    return () => cancelAnimationFrame(frame);
  }, [messages, pending, liveMain]);

  // 空态上报须在早退 return（skeleton/error）之前声明，保证各分支都在报告。
  // 口径：只看人工对话内容（提问/回答/指令），run 的执行内容与本页无关；
  // 加载中上报 undefined（位置未决），避免"先居中再跳底"。
  const loaded = !isLoading && authStatus !== "loading";
  const emptiness: boolean | undefined = !loaded
    ? undefined
    : error
      ? false
      : messages.length === 0 && liveMain.length === 0 && !pending;
  useEffect(() => {
    onEmptyChange?.(emptiness);
  }, [emptiness, onEmptyChange]);

  if (isLoading || authStatus === "loading") {
    return (
      <div className={cn("space-y-3 p-4", className)} aria-label={t("common.loading")}>
        <Skeleton className="ml-auto h-16 w-3/4" />
        <Skeleton className="ml-auto h-20 w-2/3" />
      </div>
    );
  }
  if (error) {
    return (
      <ErrorState
        className={cn("m-4", className)}
        title={t("conversation.loadFailed")}
        description={error instanceof Error ? error.message : ""}
        action={
          <Button size="sm" variant="outline" onClick={() => refetch()}>
            {t("common.retry")}
          </Button>
        }
      />
    );
  }

  return (
    <div
      ref={scrollRef}
      className={cn("min-h-0 overflow-y-auto", className)}
      role="log"
      aria-live="polite"
      aria-label={t("conversation.title")}
      onScroll={(event) => {
        const node = event.currentTarget;
        stayNearBottom.current =
          node.scrollHeight - node.scrollTop - node.clientHeight < 48;
      }}
    >
      {messages.length === 0 && liveMain.length === 0 && !pending ? (
        // 提示贴滚动区底部，正好落在居中 composer 上方（文案本身说「在下方发送」）。
        <div className="flex min-h-full flex-col items-center justify-end p-4 pb-3">
          <EmptyState
            className="w-full max-w-lg"
            icon={<User className="size-5" aria-hidden />}
            title={t("conversation.emptyTitle")}
            description={t("conversation.emptyDescription")}
          />
        </div>
      ) : (
        <ol className="mx-auto flex w-full max-w-3xl flex-col gap-4 p-4 sm:p-6">
          {messages.map((message) => (
            <ConversationTurn
              key={message.seq}
              item={message}
              instanceId={instanceId}
              taskId={taskId}
              onEdit={onEditMessage}
            />
          ))}
          {liveMain.map((message) => (
            <li key={message.id} className="flex flex-col items-start">
              <div className="max-w-[92%] whitespace-pre-wrap break-words rounded-lg rounded-tl-sm bg-muted px-3.5 py-2.5 text-foreground text-sm leading-6 sm:max-w-[80%]">
                {message.text}
                <span className="ml-0.5 inline-block animate-pulse text-primary">▌</span>
              </div>
              <div className="mt-1 flex items-center gap-2 text-[10px] text-muted-foreground">
                <Bot className="size-3" aria-hidden />
                <Loader2 className="size-3 animate-spin" aria-hidden />
                {t("conversation.answerPending")}
              </div>
            </li>
          ))}
          {pending && (
            <li className="flex items-center gap-2 text-muted-foreground text-sm">
              <Bot className="size-4 shrink-0" aria-hidden />
              <Loader2 className="size-3.5 animate-spin" aria-hidden />
              {t("conversation.answerPending")}
            </li>
          )}
        </ol>
      )}
    </div>
  );
}

/** One turn: the operator on the right, agent output on the left. */
function ConversationTurn({
  item,
  instanceId,
  taskId,
  onEdit,
}: {
  item: TranscriptItem;
  instanceId: string;
  taskId?: string;
  onEdit?: (text: string) => void;
}) {
  const { t } = useI18n();
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(item.text ?? "");
      toast.success(t("chat.copied"));
    } catch {
      toast.error(t("chat.copied"));
    }
  };
  if (item.kind === "proposal") {
    return (
      <li className="flex min-w-0 flex-col items-stretch">
        {item.proposal ? (
          <IntentProposalCard instanceId={instanceId} taskId={taskId} proposal={item.proposal} />
        ) : null}
      </li>
    );
  }
  // The main agent's own reply (R7 chat) — distinct from the tool-less
  // side-question answer, but both are left-aligned agent output.
  if (item.role === "main") {
    return (
      <li className="flex flex-col items-start">
        <div className="max-w-[92%] break-words rounded-lg rounded-tl-sm bg-muted px-3.5 py-2.5 text-foreground text-sm leading-6 sm:max-w-[80%]">
          <Markdown source={item.text} />
        </div>
        <div className="mt-1 flex items-center gap-2 text-[10px] text-muted-foreground">
          <Bot className="size-3" aria-hidden />
          <time className="font-mono tabular-nums" dateTime={item.at}>
            {new Date(item.at).toLocaleString()}
          </time>
        </div>
      </li>
    );
  }
  if (item.role === "chat") {
    const failed = item.status === "failed";
    return (
      <li className="flex flex-col items-start">
        <div
          className={cn(
            "max-w-[92%] break-words rounded-lg rounded-tl-sm px-3.5 py-2.5 text-sm leading-6 sm:max-w-[80%]",
            failed
              ? "border border-red-500/40 bg-red-500/5 text-red-700 dark:text-red-400"
              : "bg-muted text-foreground",
          )}
        >
          {failed ? `${t("conversation.answerFailed")}: ${item.text}` : <Markdown source={item.text} />}
        </div>
        <div className="mt-1 flex items-center gap-2 text-[10px] text-muted-foreground">
          <Bot className="size-3" aria-hidden />
          <time className="font-mono tabular-nums" dateTime={item.at}>
            {new Date(item.at).toLocaleString()}
          </time>
        </div>
      </li>
    );
  }
  return (
    <li className="flex flex-col items-end">
      <div className="max-w-[92%] whitespace-pre-wrap break-words rounded-lg rounded-tr-sm bg-primary px-3.5 py-2.5 text-primary-foreground text-sm leading-6 sm:max-w-[80%]">
        {item.text}
      </div>
      <div className="mt-1 flex items-center gap-2 text-[10px] text-muted-foreground">
        {item.status && <span>{t(`guidance.status.${item.status}`)}</span>}
        <button
          type="button"
          className="inline-flex items-center gap-0.5 rounded px-1 py-0.5 transition-colors hover:bg-accent/40 hover:text-foreground"
          title={t("chat.copyMessage")}
          aria-label={t("chat.copyMessage")}
          onClick={() => void copy()}
        >
          <Copy className="size-3" aria-hidden />
        </button>
        {onEdit && (
          <button
            type="button"
            className="inline-flex items-center gap-0.5 rounded px-1 py-0.5 transition-colors hover:bg-accent/40 hover:text-foreground"
            title={t("chat.editMessage")}
            aria-label={t("chat.editMessage")}
            onClick={() => onEdit(item.text ?? "")}
          >
            <Pencil className="size-3" aria-hidden />
          </button>
        )}
        <time className="font-mono tabular-nums" dateTime={item.at}>
          {new Date(item.at).toLocaleString()}
        </time>
      </div>
    </li>
  );
}
