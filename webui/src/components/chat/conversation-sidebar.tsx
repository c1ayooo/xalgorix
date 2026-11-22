import { useMemo, useState } from "react";
import {
  ChevronDown,
  ChevronRight,
  MessageSquare,
  Pin,
  Plus,
  Search,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/states";
import { cn } from "@/lib/utils";
import type { ConversationRow } from "@/types/api";
import { ConversationItem } from "@/components/chat/conversation-item";
import { useI18n } from "@/i18n";

type Group = { key: string; label: string; rows: ConversationRow[] };

/**
 * Left column of the chat page: search, pinned block, then one collapsible
 * group per task. Grouping by task (rather than agent) is deliberate — a run
 * carries the task it belongs to; the agent split lives inside the transcript.
 */
export function ConversationSidebar({
  rows,
  loading,
  selectedInstanceId,
  includeArchived,
  onSelect,
  onRename,
  onTogglePin,
  onToggleArchive,
  onToggleArchived,
  onNew,
}: {
  rows: ConversationRow[];
  loading: boolean;
  selectedInstanceId?: string;
  includeArchived: boolean;
  onSelect: (instanceId: string) => void;
  onRename: (instanceId: string, title: string) => void;
  onTogglePin: (row: ConversationRow) => void;
  onToggleArchive: (row: ConversationRow) => void;
  onToggleArchived: () => void;
  onNew: () => void;
}) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set());

  const { pinned, groups } = useMemo(() => {
    const q = query.trim().toLowerCase();
    const visible = rows.filter((row) => {
      if (!q) return true;
      return (
        (row.title ?? "").toLowerCase().includes(q) ||
        (row.task_name ?? "").toLowerCase().includes(q)
      );
    });
    const pinnedRows = visible.filter((row) => row.pinned);
    const rest = visible.filter((row) => !row.pinned);
    const byTask = new Map<string, Group>();
    for (const row of rest) {
      const key = row.task_id;
      const group = byTask.get(key) ?? {
        key,
        label: row.task_name || "",
        rows: [],
      };
      group.rows.push(row);
      byTask.set(key, group);
    }
    const ordered = Array.from(byTask.values()).sort((a, b) => a.label.localeCompare(b.label, "zh-CN"));
    return { pinned: pinnedRows, groups: ordered };
  }, [rows, query]);

  function toggle(key: string) {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  return (
    <div className="flex min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-card">
      <div className="space-y-2 border-b border-border p-2">
        <Button size="sm" className="w-full" onClick={onNew}>
          <Plus className="h-4 w-4" />
          {t("chat.newConversation")}
        </Button>
        <div className="relative">
          <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("chat.search")}
            className="h-8 pl-7 text-xs"
            aria-label={t("chat.search")}
          />
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto p-2">
        {loading ? (
          <div className="space-y-2">
            {Array.from({ length: 5 }).map((_, i) => (
              <Skeleton key={i} className="h-12 w-full" />
            ))}
          </div>
        ) : rows.length === 0 ? (
          <EmptyState title={t("chat.emptyTitle")} description={t("chat.emptyDescription")} />
        ) : (
          <div className="space-y-3">
            {pinned.length > 0 && (
              <section>
                <div className="mb-1 flex items-center gap-1.5 px-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                  <Pin className="size-3" aria-hidden />
                  {t("chat.pinned")}
                </div>
                <div className="space-y-0.5">
                  {pinned.map((row) => (
                    <ConversationItem
                      key={row.id}
                      row={row}
                      selected={row.instance_id === selectedInstanceId}
                      onSelect={() => row.instance_id && onSelect(row.instance_id)}
                      onRename={(title) => row.instance_id && onRename(row.instance_id, title)}
                      onTogglePin={() => onTogglePin(row)}
                      onToggleArchive={() => onToggleArchive(row)}
                    />
                  ))}
                </div>
              </section>
            )}

            {groups.map((group) => {
              const isCollapsed = collapsed.has(group.key);
              return (
                <section key={group.key}>
                  <button
                    type="button"
                    onClick={() => toggle(group.key)}
                    className="mb-1 flex w-full items-center gap-1 px-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground hover:text-foreground"
                    aria-expanded={!isCollapsed}
                  >
                    {isCollapsed ? (
                      <ChevronRight className="size-3" aria-hidden />
                    ) : (
                      <ChevronDown className="size-3" aria-hidden />
                    )}
                    <span className="min-w-0 truncate">{group.label || t("chat.untitled")}</span>
                    <span className="ml-auto tabular-nums opacity-70">{group.rows.length}</span>
                  </button>
                  {!isCollapsed && (
                    <div className="space-y-0.5">
                      {group.rows.map((row) => (
                        <ConversationItem
                          key={row.id}
                          row={row}
                          selected={row.instance_id === selectedInstanceId}
                          onSelect={() => row.instance_id && onSelect(row.instance_id)}
                          onRename={(title) => row.instance_id && onRename(row.instance_id, title)}
                          onTogglePin={() => onTogglePin(row)}
                          onToggleArchive={() => onToggleArchive(row)}
                        />
                      ))}
                    </div>
                  )}
                </section>
              );
            })}
          </div>
        )}
      </div>

      <button
        type="button"
        onClick={onToggleArchived}
        className={cn(
          "flex items-center justify-center gap-1.5 border-t border-border px-2 py-1.5 text-[11px] text-muted-foreground hover:text-foreground",
        )}
      >
        <MessageSquare className="size-3" aria-hidden />
        {includeArchived ? t("chat.hideArchived") : t("chat.showArchived")}
      </button>
    </div>
  );
}
