import { memo, useEffect, useRef, useState } from "react";
import {
  Archive,
  ArchiveRestore,
  Loader2,
  MoreHorizontal,
  Pencil,
  Pin,
  PinOff,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import type { ConversationRow } from "@/types/api";
import { fmtListDate } from "@/components/chat/format";
import { useI18n } from "@/i18n";

/**
 * One row in the chat rail (参照 ConversationItem)：标题行（置顶针 + 标题 +
 * 运行中章）+ 元信息行（任务 · 时间 · #实例短 id），行内重命名，紧凑菜单。
 * 分组内的行隐藏任务名（showTask=false）——组头已经说明归属。
 */
export const ConversationItem = memo(function ConversationItem({
  row,
  showTask = true,
  active,
  selectionMode,
  selected,
  renaming,
  renameText,
  onSelect,
  onStartRename,
  onRenameText,
  onCommitRename,
  onCancelRename,
  onTogglePinned,
  onToggleArchive,
  onSelectedChange,
}: {
  row: ConversationRow;
  showTask?: boolean;
  active: boolean;
  selectionMode?: boolean;
  selected?: boolean;
  renaming?: boolean;
  renameText?: string;
  onSelect: () => void;
  onStartRename: () => void;
  onRenameText: (v: string) => void;
  onCommitRename: (title: string) => void;
  onCancelRename: () => void;
  onTogglePinned: () => void;
  onToggleArchive: () => void;
  onSelectedChange: (checked: boolean) => void;
}) {
  const { t } = useI18n();
  const renameInputRef = useRef<HTMLInputElement>(null);
  const cancelRenameRef = useRef(false);
  const running = row.status === "running";
  const shortId = (row.instance_id ?? row.id).slice(0, 8);

  useEffect(() => {
    if (!renaming) return;
    cancelRenameRef.current = false;
    renameInputRef.current?.focus();
    renameInputRef.current?.select();
  }, [renaming]);

  return (
    <div
      className={cn(
        "group flex min-w-0 items-center gap-1 rounded-md pr-1 transition-colors",
        active ? "bg-accent text-accent-foreground" : "hover:bg-accent/50",
      )}
    >
      {selectionMode && (
        <input
          type="checkbox"
          checked={!!selected}
          onChange={(e) => onSelectedChange(e.target.checked)}
          aria-label={`${t("chat.selectAria")} ${row.title || t("chat.untitled")}`}
          className="ml-1 h-4 w-4 shrink-0 rounded border-border bg-input accent-primary focus:outline-none focus:ring-1 focus:ring-ring"
        />
      )}
      {renaming ? (
        <input
          ref={renameInputRef}
          value={renameText ?? ""}
          onChange={(e) => onRenameText(e.target.value)}
          onBlur={() => {
            if (!cancelRenameRef.current) onCommitRename(renameText ?? "");
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") onCommitRename(renameText ?? "");
            if (e.key === "Escape") {
              e.preventDefault();
              cancelRenameRef.current = true;
              onCancelRename();
            }
          }}
          className="min-w-0 flex-1 rounded-md border border-input bg-background px-2 py-1 text-sm"
        />
      ) : (
        <button
          type="button"
          onClick={onSelect}
          onDoubleClick={onStartRename}
          title={t("chat.doubleClickRename")}
          className="min-w-0 flex-1 rounded-md px-2 py-1.5 text-left"
        >
          <div className="flex min-w-0 items-center gap-1.5">
            {row.pinned && <Pin className="size-3 shrink-0 text-primary" aria-label={t("chat.pin")} />}
            <div className="truncate text-sm">{row.title || t("chat.untitled")}</div>
            {running && (
              <Badge variant="muted" className="shrink-0 gap-1" title={t("chat.status.running")}>
                <Loader2 className="size-3 animate-spin" aria-hidden />
                {t("chat.status.running")}
              </Badge>
            )}
          </div>
          <div className="flex min-w-0 items-center gap-1 text-muted-foreground text-xs">
            {showTask && (
              <>
                <span className="min-w-0 truncate">{row.task_name}</span>
                <span className="shrink-0" aria-hidden>
                  ·
                </span>
              </>
            )}
            <span className="shrink-0">{fmtListDate(row.started_at || row.finished_at)}</span>
            <span className="shrink-0 opacity-60">#{shortId}</span>
          </div>
        </button>
      )}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            className="size-7 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100 data-[state=open]:opacity-100"
            aria-label={`${t("chat.actions")} ${row.title || t("chat.untitled")}`}
          >
            <MoreHorizontal />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuGroup>
            <DropdownMenuItem onSelect={onStartRename}>
              <Pencil />
              {t("chat.rename")}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={onTogglePinned}>
              {row.pinned ? <PinOff /> : <Pin />}
              {row.pinned ? t("chat.unpin") : t("chat.pin")}
            </DropdownMenuItem>
          </DropdownMenuGroup>
          <DropdownMenuSeparator />
          <DropdownMenuGroup>
            <DropdownMenuItem onSelect={onToggleArchive}>
              {row.archived ? <ArchiveRestore /> : <Archive />}
              {row.archived ? t("chat.unarchive") : t("chat.archive")}
            </DropdownMenuItem>
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
});
