import { useEffect, useRef, useState } from "react";
import { CornerDownLeft, Loader2, Paperclip, Square, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { api } from "@/api/client";
import { useI18n } from "@/i18n";
import { toast } from "sonner";
import { fmtBytes } from "@/components/chat/format";
import type { ChatAttachment } from "@/types/api";

const MAX_CHARS = 4000;

/**
 * Chat composer. Enter sends, Shift+Enter breaks the line; while a run is in
 * flight the send button becomes a stop button (stopping this run only, the
 * queue keeps going). Attachments are stored in the scan directory and their
 * paths ride along with the message so tools can open them.
 */
export function Composer({
  instanceId,
  running,
  disabled,
  onSend,
  onStop,
}: {
  instanceId?: string;
  running: boolean;
  disabled?: boolean;
  onSend: (message: string, attachments: ChatAttachment[]) => void;
  onStop: () => void;
}) {
  const { t } = useI18n();
  const [draft, setDraft] = useState("");
  const [attachments, setAttachments] = useState<ChatAttachment[]>([]);
  const [uploading, setUploading] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    setAttachments([]);
  }, [instanceId]);

  // Grow with content up to ~10 lines, then scroll.
  useEffect(() => {
    const node = textareaRef.current;
    if (!node) return;
    node.style.height = "auto";
    node.style.height = `${Math.min(node.scrollHeight, 200)}px`;
  }, [draft]);

  const codePoints = Array.from(draft).length;
  const over = codePoints > MAX_CHARS;
  const canSend = !over && (draft.trim().length > 0 || attachments.length > 0) && !disabled;

  function send() {
    if (!canSend) return;
    const body = draft.trim();
    const refs = attachments.map((a) => `附件：${a.path}`);
    const message = [body, ...refs].filter(Boolean).join("\n\n");
    onSend(message, attachments);
    setDraft("");
    setAttachments([]);
  }

  async function pick(files: FileList | null) {
    if (!files?.length || !instanceId) return;
    setUploading(true);
    try {
      for (const file of Array.from(files)) {
        const stored = await api.uploadChatAttachment(instanceId, file);
        setAttachments((prev) => [...prev, stored]);
      }
    } catch (err) {
      toast.error(`${t("chat.attachFailed")}: ${(err as Error).message}`);
    } finally {
      setUploading(false);
      if (fileRef.current) fileRef.current.value = "";
    }
  }

  return (
    <div className="border-t border-border">
      <div className="px-3 py-2">
        {attachments.length > 0 && (
          <div className="mb-2 flex flex-wrap gap-1.5">
            {attachments.map((a) => (
              <span
                key={a.path}
                className="flex items-center gap-1.5 rounded-md border border-border bg-muted/40 px-2 py-1 text-xs"
                title={a.path}
              >
                <Paperclip className="size-3 text-primary" aria-hidden />
                <span className="max-w-[160px] truncate">{a.name}</span>
                <span className="text-muted-foreground">{fmtBytes(a.size)}</span>
                <button
                  type="button"
                  className="text-muted-foreground hover:text-foreground"
                  aria-label={`${t("chat.removeAttachment")} ${a.name}`}
                  onClick={() => setAttachments((prev) => prev.filter((x) => x.path !== a.path))}
                >
                  <X className="size-3" />
                </button>
              </span>
            ))}
          </div>
        )}
        <div
          className={cn(
            "flex items-end gap-1.5 rounded-lg border border-input px-2 py-1.5 focus-within:border-ring",
            running && "border-emerald-500/40",
          )}
        >
          <input
            ref={fileRef}
            type="file"
            multiple
            hidden
            onChange={(e) => void pick(e.target.files)}
          />
          <Button
            size="icon"
            variant="ghost"
            className="size-7 shrink-0"
            disabled={!instanceId || uploading}
            onClick={() => fileRef.current?.click()}
            aria-label={t("chat.attachments")}
            title={t("chat.attachments")}
          >
            {uploading ? <Loader2 className="size-4 animate-spin" /> : <Paperclip className="size-4" />}
          </Button>
          <textarea
            ref={textareaRef}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault();
                send();
              }
            }}
            rows={1}
            placeholder={running ? t("chat.placeholderRunning") : t("chat.placeholderIdle")}
            className="max-h-[200px] min-h-7 flex-1 resize-none bg-transparent py-1 text-sm outline-none placeholder:text-muted-foreground"
            aria-label={t("chat.messageInput")}
          />
          <span
            className={cn(
              "shrink-0 self-end pb-1 font-mono text-[10px] tabular-nums text-muted-foreground",
              over && "text-red-600 dark:text-red-400",
            )}
          >
            {codePoints}/{MAX_CHARS}
          </span>
          {running ? (
            <Button
              size="icon"
              variant="destructive"
              className="size-7 shrink-0"
              onClick={onStop}
              aria-label={t("chat.stopThisRun")}
              title={t("chat.stopThisRun")}
            >
              <Square className="size-3 fill-current" />
            </Button>
          ) : (
            <Button
              size="icon"
              className="size-7 shrink-0"
              disabled={!canSend}
              onClick={send}
              aria-label={t("chat.send")}
              title={t("chat.send")}
            >
              <CornerDownLeft className="size-4" />
            </Button>
          )}
        </div>
        <p className="mt-1 px-1 text-[10px] text-muted-foreground">
          {t("chat.sendHint")}
          {running ? ` · ${t("chat.sendHintRunning")}` : ""}
        </p>
      </div>
    </div>
  );
}
