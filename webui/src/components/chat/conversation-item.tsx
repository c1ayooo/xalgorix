import { useEffect, useRef, useState } from "react";
import {
  Archive,
  ArchiveRestore,
  MoreHorizontal,
  Pencil,
  Pin,
  PinOff,
  ShieldAlert,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import type { ConversationRow } from "@/types/api";
import { fmtRelative, statusTone } from "@/components/chat/format";
import { useI18n } from "@/i18n";

/**
 * One sidebar row. Conversation identity comes from the run's title (auto
 * "task · first message" or an operator rename); the instance id is only a
 * tooltip fallback so a UUID never becomes the label.
 */
export function ConversationItem({
  row,
  selected,
  onSelect,
  onRename,
  onTogglePin,
  onToggleArchive,
}: {
  row: ConversationRow;
  selected: boolean;
  onSelect: () => void;
  onRename: (title: string) => void;
  onTogglePin: () => void;
  onToggleArchive: () => void;
}) {
  const { t } = useI18n();
  const [renaming, setRenaming] = useState(false);
  const [draft, setDraft] = useState(row.title ?? "");
  const cancelled = useRef(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const tone = statusTone(row.status);

  useEffect(() => {
    if (!renaming) return;
    cancelled.current = false;
    inputRef.current?.focus();
    inputRef.current?.select();
  }, [renaming]);

  function commit() {
    const next = draft.trim();
    setRenaming(false);
    if (!cancelled.current && next && next !== (row.title ?? "")) onRename(next);
  }

  return (
    <div
      className={cn(
        "group flex min-w-0 items-center gap-1 rounded-md pr-1 transition-colors",
        selected ? "bg-accent text-accent-foreground" : "hover:bg-accent/50",
      )}
    >
      {renaming ? (
        <input
          ref={inputRef}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={commit}
          onKeyDown={(e) => {
            if (e.key === "Enter") commit();
            if (e.key === "Escape") {
              e.preventDefault();
              cancelled.current = true;
              setDraft(row.title ?? "");
              setRenaming(false);
            }
          }}
          className="min-w-0 flex-1 rounded-md border border-input bg-background px-2 py-1 text-sm"
        />
      ) : (
        <button
          type="button"
          onClick={onSelect}
          onDoubleClick={() => {
            setDraft(row.title ?? "");
            setRenaming(true);
          }}
          title={row.instance_id ?? row.id}
          className="min-w-0 flex-1 rounded-md px-2 py-1.5 text-left"
        >
          <div className="flex min-w-0 items-center gap-1.5">
            <span className={cn("size-1.5 shrink-0 rounded-full", tone.dot)} aria-hidden />
            <span className="truncate text-sm">{row.title || t("chat.untitled")}</span>
            {row.pinned && <Pin className="size-3 shrink-0 text-primary" aria-label={t("chat.pin")} />}
          </div>
          <div className="mt-0.5 flex min-w-0 items-center gap-1.5 pl-3 text-[11px] text-muted-foreground">
            <span className="min-w-0 truncate">{row.task_name}</span>
            <span aria-hidden>·</span>
            <span className="shrink-0">{fmtRelative(row.started_at || row.finished_at, t)}</span>
            {typeof row.vuln_count === "number" && row.vuln_count > 0 && (
              <span className="ml-auto flex shrink-0 items-center gap-0.5 text-amber-600 dark:text-amber-400">
                <ShieldAlert className="size-3" aria-hidden />
                {row.vuln_count}
              </span>
            )}
          </div>
        </button>
      )}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            className="size-7 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100 data-[state=open]:opacity-100"
            aria-label={t("chat.actions")}
          >
            <MoreHorizontal />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem
            onSelect={() => {
              setDraft(row.title ?? "");
              setRenaming(true);
            }}
          >
            <Pencil />
            {t("chat.rename")}
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={onTogglePin}>
            {row.pinned ? <PinOff /> : <Pin />}
            {row.pinned ? t("chat.unpin") : t("chat.pin")}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            className="text-red-600 dark:text-red-400"
            onSelect={onToggleArchive}
          >
            {row.archived ? <ArchiveRestore /> : <Archive />}
            {row.archived ? t("chat.unarchive") : t("chat.archive")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}
