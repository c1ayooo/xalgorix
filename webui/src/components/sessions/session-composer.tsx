import { useEffect, useRef, useState } from "react";
import { ArrowUp, Loader2, Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { useI18n } from "@/i18n";
import { toast } from "sonner";
import { useSendTaskGuidance } from "@/api/queries";
import { MAX_GUIDANCE_CHARS, newRequestId } from "@/lib/guidance";

/**
 * 会话页 Composer（guidance 语义，方案 §1：与对话页 Composer 各自独立）：
 * 自适应高度、IME 安全的 Enter 发送、码点计数；运行中提供停止入口
 * （停止走 AlertDialog，由工作区持有）。
 */
export function SessionComposer({
  taskId,
  instanceId,
  running,
  paused,
  onStop,
  stopping,
}: {
  taskId: string;
  instanceId: string;
  running: boolean;
  paused?: boolean;
  onStop: () => void;
  stopping?: boolean;
}) {
  const { t } = useI18n();
  const sendGuidance = useSendTaskGuidance();
  const [draft, setDraft] = useState("");
  const [requestId, setRequestId] = useState(newRequestId);
  const [sending, setSending] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    const node = textareaRef.current;
    if (!node) return;
    node.style.height = "auto";
    node.style.height = `${Math.min(node.scrollHeight, 144)}px`;
  }, [draft]);

  const codePoints = Array.from(draft).length;
  const over = codePoints > MAX_GUIDANCE_CHARS;

  async function submit() {
    const trimmed = draft.trim();
    if (!trimmed || over || sending) return;
    setSending(true);
    try {
      const result = await new Promise<{ status: string }>((resolve, reject) => {
        sendGuidance.mutate(
          { id: taskId, message: trimmed, requestId, instanceId },
          {
            onSuccess: resolve,
            onError: reject,
          },
        );
      });
      toast.success(result.status === "injected" ? t("toast.guidanceInjected") : t("toast.guidanceQueued"));
      setDraft("");
      setRequestId(newRequestId());
    } catch (error) {
      // 失败恢复：保留草稿（发送失败不吞文本）。
      toast.error(`${t("toast.guidanceFailed")}: ${(error as Error).message}`);
    } finally {
      setSending(false);
    }
  }

  return (
    <div className="border-t border-border p-3">
      <textarea
        ref={textareaRef}
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
            event.preventDefault();
            void submit();
          }
        }}
        rows={1}
        aria-label={t("console.modeInject")}
        aria-invalid={over || undefined}
        placeholder={t("console.placeholderInject")}
        className={cn(
          "max-h-36 min-h-9 w-full resize-none bg-transparent py-2 text-sm outline-none placeholder:text-muted-foreground",
          paused && "bg-amber-500/5 rounded-md",
        )}
      />
      <div className="mt-1 flex items-center gap-2">
        <span
          className={cn(
            "px-1 font-mono text-xs text-muted-foreground tabular-nums",
            over && "text-red-600 dark:text-red-400",
          )}
        >
          {codePoints}/{MAX_GUIDANCE_CHARS}
        </span>
        <div className="ml-auto flex items-center gap-1.5">
          {running && (
            <Button
              size="icon"
              variant="destructive"
              className="size-8"
              onClick={onStop}
              disabled={stopping}
              aria-label={t("console.stop")}
              title={t("console.stop")}
            >
              {stopping ? <Loader2 className="size-4 animate-spin" /> : <Square className="size-3.5 fill-current" />}
            </Button>
          )}
          <Button
            size="icon"
            className="size-8"
            disabled={!draft.trim() || over || sending}
            onClick={() => void submit()}
            aria-label={t("console.send")}
            title={t("console.send")}
          >
            {sending ? <Loader2 className="size-4 animate-spin" /> : <ArrowUp className="size-4" />}
          </Button>
        </div>
      </div>
    </div>
  );
}
