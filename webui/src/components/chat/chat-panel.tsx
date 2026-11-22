import { useEffect, useState } from "react";
import { toast } from "sonner";
import {
  Coins,
  Eye,
  MessageSquare,
  Pause,
  Pencil,
  Play,
  ShieldAlert,
  Square,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
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
import { UserConversation } from "@/components/user-conversation";
import { Transcript } from "@/components/chat/transcript";
import { Composer } from "@/components/chat/composer";
import { statusTone } from "@/components/chat/format";
import { useI18n } from "@/i18n";
import { useInstances, usePauseInstance, useResumeInstance, useSendTaskGuidance, useStopInstance } from "@/api/queries";
import { newRequestId } from "@/lib/guidance";
import { cn } from "@/lib/utils";
import type { Run } from "@/types/api";

type ViewMode = "full" | "human";

/**
 * Right pane: one conversation. The header carries identity and run controls,
 * the body is either the full transcript (human + agent + tools + findings) or
 * the charter-preserving human-only projection.
 */
export function ChatPanel({
  taskId,
  run,
  view,
  onViewChange,
  onRename,
}: {
  taskId?: string;
  run?: Run;
  view: ViewMode;
  onViewChange: (view: ViewMode) => void;
  onRename: (title: string) => void;
}) {
  const { t } = useI18n();
  const instanceId = run?.instance_id;
  const { data: instances } = useInstances();
  const live = instances?.instances.find((item) => item.id === instanceId);
  const status = live?.status ?? run?.status ?? "";
  const running = status === "running";
  const paused = status === "paused";
  const tone = statusTone(status);

  const send = useSendTaskGuidance();
  const pause = usePauseInstance();
  const resume = useResumeInstance();
  const stop = useStopInstance();
  const [requestId, setRequestId] = useState(newRequestId);
  const [stopOpen, setStopOpen] = useState(false);
  const [stopReason, setStopReason] = useState("");
  const [renaming, setRenaming] = useState(false);
  const [title, setTitle] = useState(run?.title ?? "");

  useEffect(() => {
    setTitle(run?.title ?? "");
    setRenaming(false);
  }, [run?.id, run?.title]);

  function commitTitle() {
    const next = title.trim();
    setRenaming(false);
    if (next && next !== (run?.title ?? "")) onRename(next);
    else setTitle(run?.title ?? "");
  }

  if (!run || !instanceId) {
    return (
      <div className="grid h-full place-items-center rounded-lg border border-border bg-card p-8 text-center">
        <div className="space-y-1">
          <MessageSquare className="mx-auto size-5 text-muted-foreground" aria-hidden />
          <p className="text-sm font-medium">{t("chat.pickOne")}</p>
          <p className="text-xs text-muted-foreground">{t("chat.pickOneHint")}</p>
        </div>
      </div>
    );
  }

  return (
    <div className="flex min-h-0 min-w-0 flex-col overflow-hidden rounded-lg border border-border bg-card">
      <div className="border-b border-border px-3 py-2">
        <div className="flex min-w-0 items-center gap-2">
          {renaming ? (
            <input
              autoFocus
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              onBlur={commitTitle}
              onKeyDown={(e) => {
                if (e.key === "Enter") commitTitle();
                if (e.key === "Escape") {
                  setTitle(run.title ?? "");
                  setRenaming(false);
                }
              }}
              className="min-w-0 flex-1 rounded-md border border-input bg-background px-2 py-1 text-sm"
            />
          ) : (
            <button
              type="button"
              className="group flex min-w-0 items-center gap-1.5 text-left"
              onDoubleClick={() => setRenaming(true)}
              title={t("chat.doubleClickRename")}
            >
              <span className={cn("size-2 shrink-0 rounded-full", tone.dot)} aria-hidden />
              <span className="truncate text-sm font-medium">{run.title || t("chat.untitled")}</span>
              <Pencil className="size-3 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100" aria-hidden />
            </button>
          )}
          <Badge variant="muted" className="shrink-0 text-[10px]">
            {tone.labelKey ? t(tone.labelKey) : status || "—"}
          </Badge>
          <div className="ml-auto flex shrink-0 items-center gap-2 text-[11px] text-muted-foreground">
            <span className="flex items-center gap-1">
              <Coins className="size-3" aria-hidden />
              {formatTokens(run.total_tokens)}
            </span>
            <span className="flex items-center gap-1">
              <ShieldAlert className="size-3" aria-hidden />
              {run.vuln_count ?? 0}
            </span>
          </div>
        </div>
        <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
          <Tabs value={view} onValueChange={(v) => onViewChange(v as ViewMode)}>
            <TabsList className="h-7">
              <TabsTrigger value="full" className="h-6 px-2 text-[11px]">
                <Eye className="mr-1 size-3" aria-hidden />
                {t("chat.viewFull")}
              </TabsTrigger>
              <TabsTrigger value="human" className="h-6 px-2 text-[11px]">
                <MessageSquare className="mr-1 size-3" aria-hidden />
                {t("chat.viewHuman")}
              </TabsTrigger>
            </TabsList>
          </Tabs>
          <div className="ml-auto flex items-center gap-1.5">
            {paused && (
              <Button
                size="sm"
                variant="outline"
                className="h-7 px-2 text-xs"
                disabled={resume.isPending}
                onClick={() => resume.mutate(instanceId, { onSuccess: () => toast.success(t("chat.resumed")) })}
              >
                <Play className="size-3.5" aria-hidden />
                {t("chat.resume")}
              </Button>
            )}
            {running && (
              <Button
                size="sm"
                variant="outline"
                className="h-7 px-2 text-xs"
                disabled={pause.isPending}
                onClick={() => pause.mutate(instanceId, { onSuccess: () => toast.success(t("chat.pausedToast")) })}
              >
                <Pause className="size-3.5" aria-hidden />
                {t("chat.pause")}
              </Button>
            )}
            {(running || paused) && (
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
                {t("chat.stop")}
              </Button>
            )}
          </div>
        </div>
      </div>

      {view === "full" ? (
        <Transcript instanceId={instanceId} running={running} />
      ) : (
        <div className="min-h-0 flex-1 overflow-hidden">
          <UserConversation
            taskId={taskId ?? run.task_id}
            instanceId={instanceId}
            runId={run.id}
            className="h-full bg-muted/10"
          />
        </div>
      )}

      <Composer
        instanceId={instanceId}
        running={running}
        onSend={(message) => {
          send.mutate(
            { id: taskId ?? run.task_id, message, requestId, instanceId },
            {
              onSuccess: (result) => {
                toast.success(result.status === "injected" ? t("chat.guidanceInjected") : t("chat.guidanceQueued"));
                setRequestId(newRequestId());
              },
              onError: (error: Error) => toast.error(`${t("chat.sendFailed")}: ${error.message}`),
            },
          );
        }}
        onStop={() => setStopOpen(true)}
      />

      <AlertDialog open={stopOpen} onOpenChange={setStopOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("chat.stopTitle")}</AlertDialogTitle>
            <AlertDialogDescription>{t("chat.stopDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <div className="space-y-1.5">
            <Label htmlFor="chat-stop-reason">{t("chat.stopReason")}</Label>
            <textarea
              id="chat-stop-reason"
              rows={2}
              value={stopReason}
              onChange={(e) => setStopReason(e.target.value)}
              className="w-full resize-y rounded-md border border-border bg-input/40 px-3 py-2 text-sm outline-none focus:ring-1 focus:ring-ring"
              placeholder={t("chat.stopReasonPlaceholder")}
            />
          </div>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                stop.mutate(
                  { id: instanceId, reason: stopReason.trim() || undefined },
                  {
                    onSuccess: () => {
                      toast.success(t("chat.stoppedToast"));
                      setStopOpen(false);
                    },
                    onError: (error: Error) => toast.error(`${t("chat.stopFailed")}: ${error.message}`),
                  },
                );
              }}
            >
              {t("chat.stop")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function formatTokens(n?: number): string {
  if (!n) return "0";
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)}k`;
  return `${(n / 1_000_000).toFixed(2)}M`;
}
