import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { ArrowUp, Loader2, Paperclip, Square, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { api } from "@/api/client";
import { useI18n } from "@/i18n";
import { toast } from "sonner";
import { fmtBytes } from "@/components/chat/format";
import type { ChatAttachment } from "@/types/api";

const MAX_CHARS = 4000;

/**
 * Chat composer (对话页语义，docs/SESSIONS_CHAT_1TO1_PLAN.md §6.5): Enter
 * sends (IME-composition safe), Shift+Enter breaks the line; while a run is
 * in flight the send button becomes a stop button. Attachments are stored in
 * the run's scan directory and their paths ride along with the message.
 * `onSend` is awaited: resolve clears the draft, reject restores text and
 * attachments so a failed send never eats the operator's input.
 */
export function Composer({
  instanceId,
  running,
  disabled,
  placeholder,
  leftSlot,
  onSend,
  onStop,
  stopping,
  className,
}: {
  instanceId?: string;
  running?: boolean;
  disabled?: boolean;
  placeholder?: string;
  /** Draft picker etc. — full width on tiny screens (reference leftSlot). */
  leftSlot?: ReactNode;
  onSend: (message: string, attachments: ChatAttachment[]) => Promise<void> | void;
  onStop?: () => void;
  stopping?: boolean;
  className?: string;
}) {
  const { t } = useI18n();
  const [draft, setDraft] = useState("");
  const [attachments, setAttachments] = useState<ChatAttachment[]>([]);
  const [uploading, setUploading] = useState(false);
  const [sending, setSending] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    setAttachments([]);
  }, [instanceId]);

  // Grow with content up to ~10 lines, then scroll (reference max-h-40).
  useEffect(() => {
    const node = textareaRef.current;
    if (!node) return;
    node.style.height = "auto";
    node.style.height = `${Math.min(node.scrollHeight, 160)}px`;
  }, [draft]);

  const codePoints = Array.from(draft).length;
  const over = codePoints > MAX_CHARS;
  const canSend = !over && !disabled && !sending && (draft.trim().length > 0 || attachments.length > 0);

  async function send() {
    if (!canSend || sending) return;
    const body = draft.trim();
    const atts = attachments;
    const refs = atts.map((a) => `附件：${a.path}`);
    const message = [body, ...refs].filter(Boolean).join("\n\n");
    setSending(true);
    try {
      await onSend(message, atts);
      setDraft("");
      setAttachments([]);
    } catch {
      // onSend already surfaced the error; keep text + attachments for retry.
    } finally {
      setSending(false);
    }
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
    <div className={cn("border-t border-border p-3", className)}>
      {attachments.length > 0 && (
        <div className="mb-2 flex flex-wrap gap-1.5">
          {attachments.map((a) => (
            <div
              key={a.path}
              className="flex items-center gap-1.5 rounded-md border border-border bg-muted/50 px-2 py-1 text-xs"
              title={a.path}
            >
              <Paperclip className="size-3 shrink-0 text-primary" aria-hidden />
              <span className="max-w-[160px] truncate">{a.name}</span>
              <span className="text-muted-foreground">{fmtBytes(a.size)}</span>
              <button
                type="button"
                className="ml-0.5 text-muted-foreground hover:text-foreground"
                aria-label={`${t("chat.removeAttachment")} ${a.name}`}
                onClick={() => setAttachments((prev) => prev.filter((x) => x.path !== a.path))}
              >
                <X className="size-3" />
              </button>
            </div>
          ))}
        </div>
      )}
      <div className="flex flex-wrap items-center gap-2">
        {leftSlot ? <div className="w-full sm:w-auto">{leftSlot}</div> : null}
        {instanceId && (
          <>
            <input ref={fileRef} type="file" multiple hidden onChange={(e) => void pick(e.target.files)} />
            <Button
              size="icon"
              variant="ghost"
              disabled={uploading}
              onClick={() => fileRef.current?.click()}
              aria-label={t("chat.attachments")}
              title={t("chat.attachments")}
            >
              {uploading ? <Loader2 className="size-4 animate-spin" /> : <Paperclip className="size-4" />}
            </Button>
          </>
        )}
        <textarea
          ref={textareaRef}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
              e.preventDefault();
              void send();
            }
          }}
          rows={1}
          placeholder={placeholder ?? (running ? t("chat.placeholderRunning") : t("chat.placeholderIdle"))}
          aria-invalid={over || undefined}
          className="max-h-40 min-h-10 min-w-0 flex-1 resize-none bg-transparent py-2 text-sm outline-none placeholder:text-muted-foreground"
          aria-label={t("chat.messageInput")}
        />
        <span
          className={cn(
            "shrink-0 self-end pb-2 font-mono text-[10px] tabular-nums text-muted-foreground",
            over && "text-red-600 dark:text-red-400",
          )}
        >
          {codePoints}/{MAX_CHARS}
        </span>
        {running ? (
          <Button
            size="icon"
            variant="destructive"
            onClick={onStop}
            disabled={stopping}
            aria-label={t("chat.stopThisRun")}
            title={t("chat.stopThisRun")}
          >
            <Square className="size-3.5 fill-current" />
          </Button>
        ) : (
          <Button
            size="icon"
            onClick={() => void send()}
            disabled={!canSend}
            aria-label={t("chat.send")}
            title={t("chat.send")}
          >
            {sending ? <Loader2 className="size-4 animate-spin" /> : <ArrowUp className="size-4" />}
          </Button>
        )}
      </div>
    </div>
  );
}
