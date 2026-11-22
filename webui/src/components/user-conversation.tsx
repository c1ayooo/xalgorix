import { useEffect, useMemo, useRef } from "react";
import { User } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState, ErrorState } from "@/components/states";
import { useTaskGuidance } from "@/api/queries";
import { useAuth } from "@/store/auth";
import { useEventsStore } from "@/store/events";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type { Guidance } from "@/types/api";

const GUIDANCE_EVENTS = new Set([
  "guidance_injected",
  "guidance_queued",
  "guidance_consumed",
]);

/**
 * Human-only conversation projection (CORE_PHILOSOPHY §3.2/§8 and
 * REDESIGN §5.1). The initial REST response is a snapshot; updates arrive
 * exclusively from /api/stream. Agent output, tools, plans and lifecycle
 * telemetry stay in their dedicated operational views.
 */
export function UserConversation({
  taskId,
  instanceId,
  runId,
  className,
  onEmptyChange,
}: {
  taskId: string;
  instanceId: string;
  runId?: string;
  className?: string;
  /** 空态上报（对话页据此把 composer 居中）；缺省不报告。 */
  onEmptyChange?: (empty: boolean) => void;
}) {
  const { t } = useI18n();
  const { data, isLoading, error, refetch } = useTaskGuidance(taskId);
  const events = useEventsStore((state) => state.events);
  const authEnabled = useAuth((state) => state.authEnabled);
  const authStatus = useAuth((state) => state.status);
  const user = useAuth((state) => state.user);
  const scrollRef = useRef<HTMLDivElement>(null);
  const stayNearBottom = useRef(true);

  const messages = useMemo(() => {
    const belongsToRun = (item: Guidance) =>
      (!!instanceId && item.instance_id === instanceId) ||
      (!!runId && item.run_id === runId);
    const isOwn = (item: Guidance) =>
      !authEnabled || (!!user?.username && item.created_by === user.username);
    const merged = new Map<string, Guidance>();

    for (const item of data?.items ?? []) {
      if (belongsToRun(item) && isOwn(item)) merged.set(item.id, item);
    }
    for (const event of events) {
      const item = event.guidance;
      if (
        item &&
        GUIDANCE_EVENTS.has(event.type) &&
        item.task_id === taskId &&
        belongsToRun(item) &&
        isOwn(item)
      ) {
        merged.set(item.id, item);
      }
    }
    return Array.from(merged.values()).sort((a, b) => {
      const byTime = Date.parse(a.created_at) - Date.parse(b.created_at);
      return byTime || a.id.localeCompare(b.id);
    });
  }, [authEnabled, data?.items, events, instanceId, runId, taskId, user?.username]);

  useEffect(() => {
    if (!stayNearBottom.current) return;
    const frame = requestAnimationFrame(() => {
      const node = scrollRef.current;
      if (node) node.scrollTop = node.scrollHeight;
    });
    return () => cancelAnimationFrame(frame);
  }, [messages]);

  // 空态上报须在早退 return（skeleton/error）之前声明，保证各分支都在报告。
  const isEmpty = !isLoading && authStatus !== "loading" && !error && messages.length === 0;
  useEffect(() => {
    onEmptyChange?.(isEmpty);
  }, [isEmpty, onEmptyChange]);

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
      {messages.length === 0 ? (
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
            <li key={message.id} className="flex flex-col items-end">
              <div className="max-w-[92%] whitespace-pre-wrap break-words rounded-lg rounded-tr-sm bg-primary px-3.5 py-2.5 text-primary-foreground text-sm leading-6 sm:max-w-[80%]">
                {message.message}
              </div>
              <div className="mt-1 flex items-center gap-2 text-[10px] text-muted-foreground">
                <span>{t(`guidance.status.${message.status}`)}</span>
                <time className="font-mono tabular-nums" dateTime={message.created_at}>
                  {new Date(message.created_at).toLocaleString()}
                </time>
              </div>
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}
