import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { toast } from "sonner";
import { Bot, ListTodo, MessageSquare, Pause, Pencil, Play, ShieldAlert, Square, Eye } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
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
import { UserConversation } from "@/components/user-conversation";
import { Transcript } from "@/components/chat/transcript";
import { Composer } from "@/components/chat/composer";
import { useLiveSessionKeys } from "@/components/session-item";
import { ScanStatusPill } from "@/components/scan-status-pill";
import { fmtTokens } from "@/components/chat/format";
import { useI18n } from "@/i18n";
import { usePauseInstance, useResumeInstance, useSendTaskGuidance, useStopInstance } from "@/api/queries";
import { newRequestId } from "@/lib/guidance";
import type { ConversationRow } from "@/types/api";

type ViewMode = "full" | "human";

/**
 * ChatView（参照 ChatView）：选中会话的右栏。头部 = 标题 + 状态 + 实时章 +
 * token/发现 meta；次行 = 关联任务 + full/human 双投影切换 + 运行控制；
 * 主体 = 完整转录或仅人工投影；底部 = Composer（运行中变停止键）。
 */
export function ChatPanel({
  run,
  view,
  onViewChange,
  onRename,
}: {
  run: ConversationRow;
  view: ViewMode;
  onViewChange: (view: ViewMode) => void;
  onRename: (title: string) => Promise<void> | void;
}) {
  const { t } = useI18n();
  const instanceId = run.instance_id;
  const liveKeys = useLiveSessionKeys();
  const status = run.status ?? "";
  const running = status === "running";
  const paused = status === "paused";

  const send = useSendTaskGuidance();
  const pause = usePauseInstance();
  const resume = useResumeInstance();
  const stop = useStopInstance();
  const [requestId, setRequestId] = useState(newRequestId);
  const [stopOpen, setStopOpen] = useState(false);
  const [stopReason, setStopReason] = useState("");
  const [renaming, setRenaming] = useState(false);
  const [title, setTitle] = useState(run.title ?? "");

  useEffect(() => {
    setTitle(run.title ?? "");
    setRenaming(false);
  }, [run.id, run.title]);

  function commitTitle() {
    const next = title.trim();
    setRenaming(false);
    if (next && next !== (run.title ?? "")) void onRename(next);
    else setTitle(run.title ?? "");
  }

  if (!instanceId) return null;

  return (
    <div className="flex min-h-0 min-w-0 flex-col overflow-hidden rounded-lg border border-border bg-card">
      {/* 头部：标题 + 状态 + 实时 + meta（参照 px-4 py-2.5 语法）。 */}
      <div className="flex min-w-0 flex-wrap items-center gap-2 border-b border-border px-4 py-2.5">
        <Bot className="size-4 shrink-0 text-muted-foreground" aria-hidden />
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
            className="min-w-0 max-w-48 flex-1 rounded-md border border-input bg-background px-2 py-1 text-sm"
          />
        ) : (
          <button
            type="button"
            className="group flex min-w-0 items-center gap-1.5 text-left"
            onDoubleClick={() => setRenaming(true)}
            title={t("chat.doubleClickRename")}
          >
            <span className="min-w-0 max-w-48 truncate font-medium text-sm">{run.title || t("chat.untitled")}</span>
            <Pencil
              className="size-3 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100"
              aria-hidden
            />
          </button>
        )}
        <span className="hidden shrink-0 font-mono text-muted-foreground text-xs sm:inline">
          #{(run.instance_id ?? run.id).slice(0, 8)}
        </span>
        <ScanStatusPill status={status} />
        {liveKeys.has(instanceId) && (
          <span className="inline-flex items-center gap-1 rounded bg-emerald-500/10 px-1.5 py-0.5 font-medium text-xs text-emerald-600 dark:text-emerald-400">
            <span className="size-1 animate-pulse rounded-full bg-emerald-500" aria-hidden />
            {t("session.live")}
          </span>
        )}
        <div className="ml-auto flex min-w-0 max-w-full items-center justify-end gap-x-3 gap-y-1 text-muted-foreground text-xs max-sm:w-full max-sm:flex-wrap">
          <span className="flex items-center gap-1 tabular-nums" title={t("sessions.tokens")}>
            {fmtTokens(run.total_tokens)}
          </span>
          <span className="flex items-center gap-1 tabular-nums" title={t("nav.findings")}>
            <ShieldAlert className="size-3" aria-hidden />
            {run.vuln_count ?? 0}
          </span>
        </div>
      </div>

      {/* 次行：关联任务 + 双投影切换 + 运行控制。 */}
      <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 border-b border-border px-4 pb-1.5 pt-1.5">
        <span className="flex min-w-0 shrink-0 items-center gap-1 px-1 text-xs">
          <ListTodo className="size-3 shrink-0 text-muted-foreground/70" aria-hidden />
          <Link
            to={`/tasks/${run.task_id}`}
            className="min-w-0 truncate text-muted-foreground transition-colors hover:text-foreground"
            title={run.task_name}
          >
            {run.task_name || run.task_id}
          </Link>
        </span>
        <div className="ml-auto flex min-w-0 items-center gap-1.5">
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

      {view === "full" ? (
        <Transcript
          instanceId={instanceId}
          running={running}
          empty={
            <div className="py-10 text-center text-muted-foreground text-sm">
              {t("chat.draftGreeting").replace("{name}", run.task_name || t("chat.untitled"))}
            </div>
          }
        />
      ) : (
        <div className="min-h-0 flex-1 overflow-hidden">
          <UserConversation
            taskId={run.task_id}
            instanceId={instanceId}
            runId={run.id}
            className="h-full bg-muted/10"
          />
        </div>
      )}

      <Composer
        instanceId={instanceId}
        running={running}
        onSend={async (message) => {
          await new Promise<void>((resolve, reject) => {
            send.mutate(
              { id: run.task_id, message, requestId, instanceId },
              {
                onSuccess: (result) => {
                  toast.success(
                    result.status === "injected" ? t("chat.guidanceInjected") : t("chat.guidanceQueued"),
                  );
                  setRequestId(newRequestId());
                  resolve();
                },
                onError: (error: Error) => {
                  toast.error(`${t("chat.sendFailed")}: ${error.message}`);
                  reject(error);
                },
              },
            );
          });
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
