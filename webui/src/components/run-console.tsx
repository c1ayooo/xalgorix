import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { toast } from "sonner";
import {
  CornerDownLeft,
  Hourglass,
  MessageSquareQuote,
  Pause,
  Play,
  Radio,
  Square,
} from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { LiveFeed, type FeedFilter } from "@/components/live-feed";
import { EmptyState } from "@/components/states";
import { useI18n } from "@/i18n";
import {
  useInstanceEvents,
  useInstances,
  usePauseInstance,
  useResumeInstance,
  useSendTaskGuidance,
  useStopInstance,
} from "@/api/queries";
import { useWSStore, toFeedEvent, mergeFeedEvents, filterEventsForInstance } from "@/store/ws";
import { api } from "@/api/client";
import { cn } from "@/lib/utils";
import type { ScanInstance } from "@/types/api";
import type { FeedEvent } from "@/store/ws";

const MAX_GUIDANCE_CHARS = 4000;

function newRequestId(): string {
  return typeof crypto !== "undefined" && "randomUUID" in crypto
    ? crypto.randomUUID()
    : `guidance-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
}

interface BypassExchange {
  question: string;
  answer: string;
  at: number;
}

/**
 * Session console — the left column of the task detail (4zreco sessions-tab
 * 功能等价物): run picker + run controls (pause/resume/stop), the live
 * transcript (with inline approvals and right-aligned human-turn bubbles),
 * and a docked dual-mode composer:
 *  - 注入意图 → guidance API (enters the loop; queued when no active run)
 *  - 提问 → /api/chat bypass (never enters the loop; Q&A shown locally)
 * plus a "直接恢复" ghost action while the selected run is paused.
 */
export function RunConsole({
  taskId,
  runs,
  selectedInstanceId,
  onSelectRun,
}: {
  taskId: string;
  runs: { id: string; instance_id?: string; status: string }[];
  selectedInstanceId?: string;
  onSelectRun: (instanceId: string) => void;
}) {
  const { t } = useI18n();
  const { data: instancesData } = useInstances();
  const allInstances = instancesData?.instances ?? [];
  const instance = allInstances.find((i) => i.id === selectedInstanceId);

  const sendGuidance = useSendTaskGuidance();
  const pause = usePauseInstance();
  const resume = useResumeInstance();
  const stop = useStopInstance();

  const [mode, setMode] = useState<"inject" | "ask">("inject");
  const [draft, setDraft] = useState("");
  const [requestId, setRequestId] = useState(newRequestId);
  const [asking, setAsking] = useState(false);
  const [bypass, setBypass] = useState<BypassExchange[]>([]);
  // 停止软取消：弹窗收集 reason，供引擎下一 Run briefing 参考。
  const [stopOpen, setStopOpen] = useState(false);
  const [stopReason, setStopReason] = useState("");
  // 会话视角：All agents / 单个 agent（coordinator 或 specialist）。
  const [agentScope, setAgentScope] = useState<string>("all");

  // 历史自管：一次性拉全量分页历史（静态），实时增量靠 WS；"加载更早"
  // 按 offset 继续向前翻（events_truncated 的真实后端同样适用）。
  const [history, setHistory] = useState<FeedEvent[]>([]);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [hasMore, setHasMore] = useState(false);
  const wsEvents = useWSStore((s) => s.events);
  const streamingAll = useWSStore((s) => s.streaming);
  const [filter, setFilter] = useState<FeedFilter>("all");

  useEffect(() => {
    if (!selectedInstanceId) {
      setHistory([]);
      setHasMore(false);
      return;
    }
    setHistoryLoading(true);
    api
      .runEventsPage(selectedInstanceId, 0, 100)
      .then((page) => {
        setHistory(page.map((e, i) => toFeedEvent(e, `task-run:${selectedInstanceId}`, i)));
        setHasMore(page.length >= 100);
      })
      .catch(() => setHistory([]))
      .finally(() => setHistoryLoading(false));
  }, [selectedInstanceId]);

  function loadEarlier() {
    if (!selectedInstanceId || historyLoading) return;
    setHistoryLoading(true);
    api
      .runEventsPage(selectedInstanceId, history.length, 100)
      .then((page) => {
        setHistory((prev) => [
          ...page.map((e, i) =>
            toFeedEvent(e, `task-run:${selectedInstanceId}`, prev.length + i),
          ),
          ...prev,
        ]);
        setHasMore(page.length >= 100);
      })
      .catch(() => setHasMore(false))
      .finally(() => setHistoryLoading(false));
  }

  const rawEvents: FeedEvent[] = useMemo(() => {
    const persisted = history;
    const live = selectedInstanceId
      ? filterEventsForInstance(wsEvents, selectedInstanceId)
      : [];
    return mergeFeedEvents(persisted, live);
  }, [history, wsEvents, selectedInstanceId]);

  // 会话 chips（4zreco 会话列表的紧凑形态）：coordinator（主循环）+
  // specialists，按 agent_id 从流中提取；specialist 短名去掉实例前缀。
  const sessions = useMemo(() => {
    const set = new Set<string>();
    for (const e of rawEvents) {
      if (e.agent_id) set.add(e.agent_id);
    }
    return Array.from(set)
      .sort()
      .map((id) => ({
        id,
        coordinator: id === selectedInstanceId,
        label: id === selectedInstanceId ? t("console.coordinator") : id.replace(`${selectedInstanceId}/`, ""),
      }));
  }, [rawEvents, selectedInstanceId, t]);

  const events = useMemo(
    () =>
      agentScope === "all"
        ? rawEvents
        : rawEvents.filter((e) => (e.agent_id ?? "") === agentScope),
    [rawEvents, agentScope],
  );

  // 正在生成的 assistant 消息（本 Run、本视角），渲染为流尾打字气泡。
  const streaming = useMemo(
    () =>
      Object.values(streamingAll)
        .filter(
          (m) =>
            m.instance_id === selectedInstanceId &&
            (agentScope === "all" || m.agent_id === agentScope),
        )
        .sort((a, b) => a.updatedAt - b.updatedAt),
    [streamingAll, selectedInstanceId, agentScope],
  );

  const status = instance?.status ?? "";
  const paused = status === "paused";
  const running = status === "running";
  const codePoints = Array.from(draft).length;
  const over = codePoints > MAX_GUIDANCE_CHARS;

  function submit() {
    const trimmed = draft.trim();
    if (!trimmed) return;
    if (mode === "inject") {
      if (over || sendGuidance.isPending) return;
      sendGuidance.mutate(
        { id: taskId, message: trimmed, requestId, instanceId: selectedInstanceId },
        {
          onSuccess: (res) => {
            toast.success(
              res.status === "injected"
                ? t("toast.guidanceInjected")
                : t("toast.guidanceQueued"),
            );
            setDraft("");
            setRequestId(newRequestId());
          },
          onError: (err: Error) =>
            toast.error(`${t("toast.guidanceFailed")}: ${err.message}`),
        },
      );
    } else {
      // 旁路提问：/api/chat（in-scan chat 通道），永不进 loop。
      setAsking(true);
      api
        .chat(trimmed, selectedInstanceId)
        .then((res) => {
          setBypass((prev) =>
            [{ question: trimmed, answer: res.response || res.reply || "", at: Date.now() }, ...prev].slice(0, 3),
          );
          setDraft("");
        })
        .catch((err: Error) =>
          toast.error(`${t("toast.askFailed")}: ${err.message}`),
        )
        .finally(() => setAsking(false));
    }
  }

  return (
    <Card className="flex h-full min-h-0 flex-col overflow-hidden">
      {/* Console head: run picker + controls */}
      <div className="flex flex-wrap items-center gap-2 border-b border-border px-2 py-1.5">
        <Select
          value={selectedInstanceId ?? undefined}
          onValueChange={onSelectRun}
        >
          <SelectTrigger
            className="h-8 w-44 border-0 bg-transparent px-2 text-xs shadow-none focus:ring-0"
            aria-label={t("console.runPicker")}
          >
            <SelectValue placeholder={t("console.noRun")} />
          </SelectTrigger>
          <SelectContent>
            {runs
              .filter((r) => r.instance_id)
              .map((r) => (
                <SelectItem key={r.id} value={r.instance_id!}>
                  <span className="mono">{r.id}</span>
                  <span className="ml-2 text-muted-foreground">{r.status}</span>
                </SelectItem>
              ))}
          </SelectContent>
        </Select>

        {sessions.length > 0 && (
          <div
            className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto"
            role="tablist"
            aria-label={t("console.agentPicker")}
          >
            <SessionChip
              active={agentScope === "all"}
              label={t("console.agentAll")}
              onClick={() => setAgentScope("all")}
            />
            {sessions.map((ses) => (
              <SessionChip
                key={ses.id}
                active={agentScope === ses.id}
                label={ses.label}
                coordinator={ses.coordinator}
                onClick={() => setAgentScope(ses.id)}
              />
            ))}
          </div>
        )}

        {instance && (
          <div className="ml-auto flex items-center gap-1">
            {paused && (
              <Button
                size="sm"
                variant="outline"
                className="h-7 px-2 text-xs"
                disabled={resume.isPending}
                onClick={() =>
                  resume.mutate(instance.id, {
                    onSuccess: () => toast.success(t("toast.runResumed")),
                  })
                }
              >
                <Play className="h-3 w-3" />
                {t("console.resume")}
              </Button>
            )}
            {running && (
              <Button
                size="sm"
                variant="ghost"
                className="h-7 px-2 text-xs"
                disabled={pause.isPending}
                onClick={() =>
                  pause.mutate(instance.id, {
                    onSuccess: () => toast.success(t("toast.runPaused")),
                  })
                }
              >
                <Pause className="h-3 w-3" />
                {t("console.pause")}
              </Button>
            )}
            {(running || paused) && (
              <Button
                size="sm"
                variant="ghost"
                className="h-7 px-2 text-red-600 hover:text-red-700 dark:text-red-400"
                onClick={() => {
                  setStopReason("");
                  setStopOpen(true);
                }}
              >
                <Square className="h-3 w-3" />
                {t("console.stop")}
              </Button>
            )}
          </div>
        )}
      </div>

      {/* Transcript */}
      <CardContent className="min-h-0 flex-1 p-0">
        {selectedInstanceId ? (
          <div className="flex h-full min-h-0 flex-col">
            {hasMore && (
              <div className="border-b border-border px-3 py-1.5 text-center">
                <button
                  type="button"
                  onClick={loadEarlier}
                  disabled={historyLoading}
                  className="text-xs text-muted-foreground transition-colors hover:text-foreground disabled:opacity-50"
                >
                  {historyLoading ? t("console.loadingEarlier") : t("console.loadEarlier")}
                </button>
              </div>
            )}
            <LiveFeed
              events={events}
              filter={filter}
              onFilterChange={setFilter}
              showControls
              fillHeight
              streaming={streaming}
              emptyTitle={t("taskDetail.noEventsTitle")}
              emptyDescription={t("taskDetail.noEventsDescription")}
            />
          </div>
        ) : (
          <div className="grid h-full place-items-center p-6">
            <EmptyState
              title={t("console.noRunTitle")}
              description={t("console.noRunDescription")}
            />
          </div>
        )}
      </CardContent>

      {/* Docked composer: bypass Q&A + mode switch + input */}
      <div className="border-t border-border">
        {bypass.length > 0 && (
          <div className="max-h-40 space-y-1.5 overflow-y-auto border-b border-border px-3 py-2">
            {bypass.map((ex, i) => (
              <div
                key={ex.at}
                className={cn(
                  "rounded-sm border border-violet-500/30 bg-violet-500/5 p-2 text-xs",
                  i > 0 && "opacity-70",
                )}
              >
                <div className="mb-1 flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-violet-700 dark:text-violet-300">
                  <MessageSquareQuote className="h-3 w-3" aria-hidden />
                  {t("console.bypassTag")}
                </div>
                <div className="text-foreground">{ex.question}</div>
                <div className="mt-1 whitespace-pre-wrap text-muted-foreground">
                  {ex.answer}
                </div>
              </div>
            ))}
          </div>
        )}

        <div
          className={cn(
            "flex items-end gap-2 px-2 py-2",
            paused && "bg-amber-500/5",
          )}
        >
          {/* Mode chips */}
          <div
            role="tablist"
            aria-label={t("console.modeLabel")}
            className="flex shrink-0 rounded-md border border-border p-0.5"
          >
            {(["inject", "ask"] as const).map((m) => (
              <button
                key={m}
                type="button"
                role="tab"
                aria-selected={mode === m}
                onClick={() => setMode(m)}
                className={cn(
                  "rounded-sm px-2 py-1 text-[11px] transition-colors",
                  mode === m
                    ? "bg-accent text-accent-foreground"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                {t(m === "inject" ? "console.modeInject" : "console.modeAsk")}
              </button>
            ))}
          </div>

          <textarea
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                submit();
              }
            }}
            rows={1}
            aria-label={t("console.modeLabel")}
            aria-invalid={mode === "inject" && over || undefined}
            placeholder={
              mode === "inject"
                ? t("console.placeholderInject")
                : t("console.placeholderAsk")
            }
            className="min-h-8 max-h-32 flex-1 resize-y bg-transparent px-2 py-1.5 text-sm outline-none placeholder:text-muted-foreground"
          />

          {paused && (
            <Button
              size="sm"
              variant="outline"
              className="h-8 shrink-0"
              disabled={resume.isPending}
              onClick={() =>
                instance &&
                resume.mutate(instance.id, {
                  onSuccess: () => toast.success(t("toast.runResumed")),
                })
              }
            >
              <Hourglass className="h-3.5 w-3.5" />
              {t("console.resumeOnly")}
            </Button>
          )}

          <Button
            size="icon"
            className="h-8 w-8 shrink-0"
            disabled={
              !draft.trim() ||
              (mode === "inject" ? over || sendGuidance.isPending : asking)
            }
            onClick={submit}
            aria-label={t("console.send")}
          >
            <CornerDownLeft className="h-4 w-4" />
          </Button>
        </div>
      {/* 停止软取消：reason 作为人工转向的结构化反馈落 stop_reason。 */}
      <AlertDialog open={stopOpen} onOpenChange={setStopOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("console.stopTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("console.stopDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="space-y-1.5">
            <Label htmlFor="stop-reason">{t("console.stopReasonLabel")}</Label>
            <textarea
              id="stop-reason"
              rows={2}
              value={stopReason}
              onChange={(e) => setStopReason(e.target.value)}
              placeholder={t("console.stopReasonPlaceholder")}
              className="w-full resize-y rounded-md border border-border bg-input/40 px-3 py-2 text-sm outline-none placeholder:text-muted-foreground focus:ring-1 focus:ring-ring"
            />
          </div>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (!instance) return;
                stop.mutate(
                  { id: instance.id, reason: stopReason.trim() || undefined },
                  {
                    onSuccess: () => {
                      toast.success(t("toast.runStopped"));
                      setStopOpen(false);
                    },
                    onError: (err: Error) =>
                      toast.error(`${t("console.stopFailed")}: ${err.message}`),
                  },
                );
              }}
            >
              {t("console.stopConfirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

        <div className="flex items-center gap-2 border-t border-border px-3 py-1 text-[10px] text-muted-foreground">
          {running ? (
            <span className="flex items-center gap-1 text-emerald-700 dark:text-emerald-300">
              <Radio className="h-3 w-3" aria-hidden />
              {mode === "inject" ? t("console.hintInjectRunning") : t("console.hintAsk")}
            </span>
          ) : paused ? (
            <span className="flex items-center gap-1">
              <Hourglass className="h-3 w-3" aria-hidden />
              {t("console.hintPaused")}
            </span>
          ) : (
            <span className="flex items-center gap-1">
              <Hourglass className="h-3 w-3" aria-hidden />
              {t("console.hintIdle")}
            </span>
          )}
          {mode === "inject" && (
            <span
              className={cn(
                "ml-auto tabular-nums mono",
                over && "text-red-600 dark:text-red-400",
              )}
            >
              {codePoints}/{MAX_GUIDANCE_CHARS}
            </span>
          )}
        </div>
      </div>
    </Card>
  );
}

/** 选中 Run 的实例对象（供 task-detail 头部等处使用）。 */
export function pickInstance(
  all: ScanInstance[] | undefined,
  instanceId?: string,
): ScanInstance | undefined {
  return all?.find((i) => i.id === instanceId);
}

/** Compact session chip (coordinator / specialist) in the console header. */
function SessionChip({
  active,
  label,
  coordinator = false,
  onClick,
}: {
  active: boolean;
  label: string;
  coordinator?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onClick}
      className={cn(
        "shrink-0 rounded-sm px-2 py-1 text-[11px] transition-colors",
        active
          ? "bg-accent text-accent-foreground"
          : "text-muted-foreground hover:bg-accent/40 hover:text-foreground",
      )}
    >
      {coordinator && (
        <span className="mr-1 inline-block h-1.5 w-1.5 rounded-full bg-emerald-500 align-middle" aria-hidden />
      )}
      <span className={coordinator ? "" : "mono"}>{label}</span>
    </button>
  );
}
