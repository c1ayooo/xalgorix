import { useState } from "react";
import { toast } from "sonner";
import {
  ArrowUp,
  CheckCircle2,
  ChevronDown,
  Clock,
  Hourglass,
  Loader2,
  Pause,
  Play,
  Radio,
  Square,
  User,
  WifiOff,
  XCircle,
} from "lucide-react";
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
import { EmptyState } from "@/components/states";
import { UserConversation } from "@/components/user-conversation";
import { useI18n } from "@/i18n";
import {
  useInstances,
  usePauseInstance,
  useResumeInstance,
  useSendTaskGuidance,
  useStopInstance,
} from "@/api/queries";
import { cn } from "@/lib/utils";
import { MAX_GUIDANCE_CHARS, newRequestId } from "@/lib/guidance";
import { useStreamBadge, useLiveSessionKeys } from "@/components/session-item";
import type { Run } from "@/types/api";

/**
 * Task-detail session tab. Structure follows REDESIGN §7.2 and UI_DESIGN §6;
 * content follows CORE_PHILOSOPHY §3.2/§8: one Run is one main session and the
 * transcript contains only the current operator's human input. There are no
 * synthetic Planner/system-audit sessions or worker event transcripts here.
 */
export function SessionsTab({
  taskId,
  runs,
  selectedInstanceId,
  onSelectRun,
}: {
  taskId: string;
  runs: Run[];
  selectedInstanceId?: string;
  onSelectRun: (instanceId: string) => void;
}) {
  const { t } = useI18n();
  const [listOpen, setListOpen] = useState(false);
  const liveKeys = useLiveSessionKeys();
  const { live: sseLive, reconnecting: sseReconnecting } = useStreamBadge();
  const selectedRun = runs.find((run) => run.instance_id === selectedInstanceId);

  return (
    <div
      className={cn(
        "grid h-[calc(100svh-13rem)] min-h-0 grid-cols-1 gap-4",
        listOpen
          ? "grid-rows-[minmax(0,40svh)_minmax(0,1fr)]"
          : "grid-rows-[auto_minmax(0,1fr)]",
        "lg:h-full lg:grid-cols-[14rem_minmax(0,1fr)] lg:grid-rows-[minmax(0,1fr)]",
      )}
    >
      <div className="flex min-h-0 flex-col overflow-hidden rounded-md border bg-card">
        <div className="border-b px-3 py-2">
          <div className="flex items-center justify-between gap-2">
            <button
              type="button"
              onClick={() => setListOpen((value) => !value)}
              aria-expanded={listOpen}
              aria-controls="session-list-panel"
              className="flex min-w-0 flex-1 items-center gap-1 text-left text-xs font-medium text-muted-foreground transition-colors hover:text-foreground lg:pointer-events-none"
            >
              <ChevronDown
                className={cn(
                  "size-3.5 shrink-0 transition-transform lg:hidden",
                  !listOpen && "-rotate-90",
                )}
              />
              <span className="shrink-0">{t("sessions.list")}</span>
            </button>
            <span
              role="status"
              title={sseLive ? t("sessions.liveTip") : t("sessions.reconnectTip")}
              className={cn(
                "inline-flex items-center gap-1 text-xs",
                sseLive
                  ? "text-emerald-600 dark:text-emerald-400"
                  : sseReconnecting
                    ? "text-amber-600 dark:text-amber-400"
                    : "text-muted-foreground",
              )}
            >
              {sseLive ? (
                <>
                  <span className="size-1 rounded-full bg-emerald-500 pulse-dot" aria-hidden />
                  {t("session.live")}
                </>
              ) : sseReconnecting ? (
                <>
                  <WifiOff className="size-3" aria-hidden />
                  {t("session.reconnecting")}
                </>
              ) : null}
            </span>
          </div>
          {(() => {
            const total = runs.reduce((sum, run) => sum + (run.total_tokens ?? 0), 0);
            if (!total) return null;
            return (
              <div className="mt-2 flex items-center gap-1 text-xs text-muted-foreground">
                <span>{t("sessions.totalTokens")}</span>
                <span className="rounded bg-muted px-1.5 py-0.5 font-mono tabular-nums">
                  {total.toLocaleString()}
                </span>
              </div>
            );
          })()}
        </div>

        <div
          id="session-list-panel"
          className={cn("min-h-0 flex-1 overflow-y-auto", !listOpen && "max-lg:hidden")}
        >
          <div className="p-2">
            <RailGroup icon={<User className="size-3.5" />} label={t("sessions.groupMain")}>
              {runs.map((run) =>
                run.instance_id ? (
                  <RailItem
                    key={run.id}
                    active={selectedInstanceId === run.instance_id}
                    status={run.status}
                    live={liveKeys.has(run.instance_id)}
                    title={run.id}
                    onClick={() => {
                      onSelectRun(run.instance_id!);
                      setListOpen(false);
                    }}
                  />
                ) : null,
              )}
            </RailGroup>
          </div>
        </div>
      </div>

      {selectedInstanceId ? (
        <Workspace
          taskId={taskId}
          selectedInstanceId={selectedInstanceId}
          selectedRunId={selectedRun?.id}
        />
      ) : (
        <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-md border bg-card">
          <div className="grid h-full place-items-center p-6">
            <EmptyState
              title={t("console.noRunTitle")}
              description={t("console.noRunDescription")}
            />
          </div>
        </div>
      )}
    </div>
  );
}

function RailGroup({
  icon,
  label,
  children,
}: {
  icon: React.ReactNode;
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-col gap-0.5">
      <div className="flex items-center gap-1.5 px-2 py-1 text-xs font-medium text-muted-foreground">
        {icon}
        {label}
      </div>
      {children}
    </div>
  );
}

function RailStatusIcon({ status }: { status?: string }) {
  const key = (status || "").toLowerCase();
  const cls = "size-3.5 shrink-0";
  switch (key) {
    case "running":
      return <Loader2 className={cn(cls, "animate-spin text-emerald-600 dark:text-emerald-400")} aria-hidden />;
    case "pending":
    case "queued":
      return <Clock className={cn(cls, "text-muted-foreground")} aria-hidden />;
    case "paused":
      return <Pause className={cn(cls, "text-amber-600 dark:text-amber-400")} aria-hidden />;
    case "failed":
      return <XCircle className={cn(cls, "text-red-600 dark:text-red-400")} aria-hidden />;
    case "stopped":
    case "cancelled":
      return <Clock className={cn(cls, "text-amber-600 dark:text-amber-400")} aria-hidden />;
    case "finished":
    case "completed":
    case "saved":
      return <CheckCircle2 className={cn(cls, "text-emerald-600 dark:text-emerald-400")} aria-hidden />;
    default:
      return <Clock className={cn(cls, "text-muted-foreground")} aria-hidden />;
  }
}

function RailItem({
  active,
  status,
  live,
  title,
  onClick,
}: {
  active: boolean;
  status?: string;
  live?: boolean;
  title: string;
  onClick: () => void;
}) {
  const { t } = useI18n();
  return (
    <div
      className={cn(
        "group/session flex w-full items-center rounded-sm transition-colors",
        active ? "bg-accent text-accent-foreground" : "hover:bg-accent/50",
      )}
    >
      <button
        type="button"
        onClick={onClick}
        className="flex min-w-0 flex-1 items-center gap-1.5 px-2 py-1.5 text-left text-sm"
        aria-current={active ? "true" : undefined}
        title={title}
      >
        <RailStatusIcon status={status} />
        <span className="min-w-0 flex-1 truncate font-mono text-xs">{title}</span>
        {live && (
          <span className="inline-flex shrink-0 items-center gap-1 rounded bg-emerald-500/10 px-1.5 py-0.5 text-[10px] font-medium text-emerald-600 dark:text-emerald-400">
            <span className="size-1 rounded-full bg-emerald-500 pulse-dot" aria-hidden />
            {t("session.live")}
          </span>
        )}
      </button>
    </div>
  );
}

function Workspace({
  taskId,
  selectedInstanceId,
  selectedRunId,
}: {
  taskId: string;
  selectedInstanceId: string;
  selectedRunId?: string;
}) {
  const { t } = useI18n();
  const { data: instancesData } = useInstances();
  const instance = instancesData?.instances.find((item) => item.id === selectedInstanceId);
  const sendGuidance = useSendTaskGuidance();
  const pause = usePauseInstance();
  const resume = useResumeInstance();
  const stop = useStopInstance();
  const [draft, setDraft] = useState("");
  const [requestId, setRequestId] = useState(newRequestId);
  const [stopOpen, setStopOpen] = useState(false);
  const [stopReason, setStopReason] = useState("");

  const status = instance?.status ?? "";
  const paused = status === "paused";
  const running = status === "running";
  const codePoints = Array.from(draft).length;
  const over = codePoints > MAX_GUIDANCE_CHARS;

  function submit() {
    const trimmed = draft.trim();
    if (!trimmed || over || sendGuidance.isPending) return;
    sendGuidance.mutate(
      { id: taskId, message: trimmed, requestId, instanceId: selectedInstanceId },
      {
        onSuccess: (result) => {
          toast.success(
            result.status === "injected"
              ? t("toast.guidanceInjected")
              : t("toast.guidanceQueued"),
          );
          setDraft("");
          setRequestId(newRequestId());
        },
        onError: (error: Error) =>
          toast.error(`${t("toast.guidanceFailed")}: ${error.message}`),
      },
    );
  }

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-md border bg-card">
      <div className="flex min-w-0 flex-wrap items-center gap-2 border-b px-3 py-2 sm:px-4">
        <div className="min-w-0">
          <div className="truncate text-sm font-medium">{t("conversation.title")}</div>
          <div className="truncate font-mono text-[10px] text-muted-foreground">
            {selectedRunId ?? selectedInstanceId}
          </div>
        </div>
        <div className="ml-auto flex shrink-0 items-center gap-1.5">
          {instance && paused && (
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
              <Play className="size-3.5" aria-hidden />
              {t("console.resume")}
            </Button>
          )}
          {instance && running && (
            <Button
              size="sm"
              variant="outline"
              className="h-7 px-2 text-xs"
              disabled={pause.isPending}
              onClick={() =>
                pause.mutate(instance.id, {
                  onSuccess: () => toast.success(t("toast.runPaused")),
                })
              }
            >
              <Pause className="size-3.5" aria-hidden />
              {t("console.pause")}
            </Button>
          )}
          {instance && (running || paused) && (
            <Button
              size="sm"
              variant="ghost"
              className="h-7 px-2 text-xs text-red-600 hover:text-red-700 dark:text-red-400"
              onClick={() => {
                setStopReason("");
                setStopOpen(true);
              }}
            >
              <Square className="size-3.5" aria-hidden />
              {t("console.stop")}
            </Button>
          )}
        </div>
      </div>

      <UserConversation
        taskId={taskId}
        instanceId={selectedInstanceId}
        runId={selectedRunId}
        className="flex-1 bg-muted/10"
      />

      <div className="border-t p-3">
        <div
          className={cn(
            "flex w-full flex-col rounded-md border border-input transition-colors focus-within:border-ring",
            paused && "bg-amber-500/5",
          )}
        >
          <textarea
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter" && !event.shiftKey) {
                event.preventDefault();
                submit();
              }
            }}
            rows={2}
            aria-label={t("console.modeInject")}
            aria-invalid={over || undefined}
            placeholder={t("console.placeholderInject")}
            className="max-h-36 min-h-14 flex-1 resize-none bg-transparent px-3 py-2 text-sm outline-none placeholder:text-muted-foreground"
          />
          <div className="flex items-center gap-2 px-2.5 pb-2 pt-1">
            <span
              className={cn(
                "text-xs text-muted-foreground tabular-nums",
                over && "text-red-600 dark:text-red-400",
              )}
            >
              {codePoints}/{MAX_GUIDANCE_CHARS}
            </span>
            <div className="ml-auto flex items-center gap-2 text-[10px] text-muted-foreground">
              {running ? (
                <span className="flex items-center gap-1 text-emerald-700 dark:text-emerald-300">
                  <Radio className="size-3" aria-hidden />
                  {t("console.hintInjectRunning")}
                </span>
              ) : paused ? (
                <span className="flex items-center gap-1">
                  <Hourglass className="size-3" aria-hidden />
                  {t("console.hintPaused")}
                </span>
              ) : (
                <span className="flex items-center gap-1">
                  <Hourglass className="size-3" aria-hidden />
                  {t("console.hintIdle")}
                </span>
              )}
              <Button
                size="icon"
                className="size-7"
                disabled={!draft.trim() || over || sendGuidance.isPending}
                onClick={submit}
                aria-label={t("console.send")}
                title={t("console.send")}
              >
                <ArrowUp className="size-4" aria-hidden />
              </Button>
            </div>
          </div>
        </div>
      </div>

      <AlertDialog open={stopOpen} onOpenChange={setStopOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("console.stopTitle")}</AlertDialogTitle>
            <AlertDialogDescription>{t("console.stopDescription")}</AlertDialogDescription>
          </AlertDialogHeader>
          <div className="space-y-1.5">
            <Label htmlFor="stop-reason">{t("console.stopReasonLabel")}</Label>
            <textarea
              id="stop-reason"
              rows={2}
              value={stopReason}
              onChange={(event) => setStopReason(event.target.value)}
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
                    onError: (error: Error) =>
                      toast.error(`${t("console.stopFailed")}: ${error.message}`),
                  },
                );
              }}
            >
              {t("console.stopConfirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
