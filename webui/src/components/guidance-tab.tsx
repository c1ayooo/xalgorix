import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { CornerDownLeft, Hourglass, MessageSquarePlus, Radio } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/states";
import { useSendTaskGuidance, useTaskGuidance, useTaskRuns } from "@/api/queries";
import { useI18n } from "@/i18n";
import { cn, timeAgo } from "@/lib/utils";
import type { Guidance } from "@/types/api";

const MAX_CHARS = 4000;

function newRequestId(): string {
  return typeof crypto !== "undefined" && "randomUUID" in crypto
    ? crypto.randomUUID()
    : `guidance-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
}

/**
 * Human-in-the-loop intent injection for a task (契约 §3.1).
 *
 * 4zreco semantics, adapted: sending is a synchronous accept — the backend
 * persists the human turn before returning, so the list below renders purely
 * from server data (no optimistic echo). While a run is active the message is
 * injected into the agent's next turn; without an active run it queues onto
 * the task and rides the next run's briefing. Idempotency: the request_id is
 * generated per draft and held until success.
 */
export function GuidanceTab({ taskId }: { taskId: string }) {
  const { t } = useI18n();
  const { data, isLoading } = useTaskGuidance(taskId);
  const { data: runsData } = useTaskRuns(taskId);
  const send = useSendTaskGuidance();

  const [draft, setDraft] = useState("");
  const [requestId, setRequestId] = useState(newRequestId);
  const taRef = useRef<HTMLTextAreaElement>(null);

  const items = data?.items ?? [];
  const activeRun = runsData?.items.find((r) => r.status === "running");
  const codePoints = Array.from(draft).length;
  const over = codePoints > MAX_CHARS;

  useEffect(() => {
    if (send.isSuccess) {
      setDraft("");
      setRequestId(newRequestId());
    }
  }, [send.isSuccess]);

  function submit() {
    const trimmed = draft.trim();
    if (!trimmed || over || send.isPending) return;
    send.mutate(
      { id: taskId, message: trimmed, requestId },
      {
        onSuccess: (res) => {
          toast.success(
            res.status === "injected"
              ? t("toast.guidanceInjected")
              : t("toast.guidanceQueued"),
          );
        },
        onError: (err: Error) =>
          toast.error(`${t("toast.guidanceFailed")}: ${err.message}`),
      },
    );
  }

  return (
    <div className="space-y-4 pt-4">
      {/* Composer */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <MessageSquarePlus className="h-4 w-4 text-muted-foreground" aria-hidden />
            {t("guidance.composerTitle")}
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          <div
            className={cn(
              "rounded-md border bg-input/40 transition-colors focus-within:ring-1",
              over ? "border-red-500/60 focus-within:ring-red-500/50" : "border-border focus-within:ring-ring",
            )}
          >
            <textarea
              ref={taRef}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault();
                  submit();
                }
              }}
              rows={3}
              maxLength={MAX_CHARS * 2}
              aria-invalid={over || undefined}
              aria-label={t("guidance.composerTitle")}
              placeholder={t("guidance.placeholder")}
              className="w-full resize-y bg-transparent px-3 py-2 text-sm outline-none placeholder:text-muted-foreground"
            />
            <div className="flex items-center gap-3 border-t border-border px-3 py-1.5 text-[11px] text-muted-foreground">
              {activeRun ? (
                <span className="flex items-center gap-1 text-emerald-700 dark:text-emerald-300">
                  <Radio className="h-3 w-3" aria-hidden />
                  {t("guidance.willInject")}
                </span>
              ) : (
                <span className="flex items-center gap-1">
                  <Hourglass className="h-3 w-3" aria-hidden />
                  {t("guidance.willQueue")}
                </span>
              )}
              <span className={cn("ml-auto tabular-nums mono", over && "text-red-600 dark:text-red-400")}>
                {codePoints}/{MAX_CHARS}
              </span>
              <kbd className="rounded-sm border border-border bg-muted px-1 py-0.5 text-[10px] mono">
                <CornerDownLeft className="inline h-3 w-3" aria-hidden /> Enter
              </kbd>
            </div>
          </div>
          <p className="text-[11px] text-muted-foreground">{t("guidance.hint")}</p>
        </CardContent>
      </Card>

      {/* Injected intents, newest first */}
      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 2 }).map((_, i) => (
            <Skeleton key={i} className="h-14 w-full" />
          ))}
        </div>
      ) : items.length === 0 ? (
        <EmptyState
          title={t("guidance.emptyTitle")}
          description={t("guidance.emptyDescription")}
        />
      ) : (
        <div className="space-y-2">
          {items.map((g: Guidance) => (
            <GuidanceRow key={g.id} guidance={g} />
          ))}
        </div>
      )}
    </div>
  );
}

function GuidanceRow({ guidance }: { guidance: Guidance }) {
  const { t } = useI18n();
  return (
    <div className="flex items-start justify-end gap-2">
      <span className="mt-1.5 shrink-0 text-[10px] text-muted-foreground mono">
        {timeAgo(guidance.created_at)}
      </span>
      <div
        className={cn(
          "max-w-[80%] rounded-lg rounded-tr-sm px-3 py-1.5 text-sm [overflow-wrap:anywhere]",
          "bg-primary text-primary-foreground",
        )}
      >
        {guidance.message}
        <div className="mt-1 flex items-center gap-1.5 text-[10px] uppercase tracking-wider opacity-80">
          <GuidanceStatusChip status={guidance.status} />
          {guidance.run_id && <span className="mono">{guidance.run_id}</span>}
        </div>
      </div>
    </div>
  );
}

function GuidanceStatusChip({ status }: { status: Guidance["status"] }) {
  const { t } = useI18n();
  return (
    <span className="rounded-sm border border-current/30 px-1 py-px">
      {t(`guidance.status.${status}`)}
    </span>
  );
}
