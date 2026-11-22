import { useEffect, useMemo, useRef, useState } from "react";
import { Bot, ChevronRight, ListChecks, Loader2, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { ConversationItem } from "@/components/chat/conversation-item";
import { cn } from "@/lib/utils";
import { useI18n } from "@/i18n";
import type { ConversationRow } from "@/types/api";

const LIST_PAGE = 100;
const COLLAPSED_KEY = "xalgorix.chat.collapsed-tasks";

type TaskGroup = { key: string; label: string; rows: ConversationRow[]; runningCount: number };

/** 三态表头勾选（全选/半选/空），native input + indeterminate ref。 */
function HeaderCheckbox({ state, onChange, ...rest }: { state: boolean | "mixed"; onChange: (v: boolean) => void } & Omit<React.InputHTMLAttributes<HTMLInputElement>, "checked" | "onChange">) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (ref.current) ref.current.indeterminate = state === "mixed";
  }, [state]);
  return (
    <input
      ref={ref}
      type="checkbox"
      checked={state === true}
      onChange={(e) => onChange(e.target.checked)}
      className={cn("h-4 w-4 rounded border-border bg-input accent-primary focus:outline-none focus:ring-1 focus:ring-ring", rest.className)}
      {...rest}
    />
  );
}

/**
 * Chat rail（参照左栏）：新建对话 → 任务筛选 → 计数/多选 → 置顶块 →
 * 按任务分组的会话（组头吸顶可折叠，折叠态持久化）→ 加载更多。
 * 分组按任务而非 Agent 是有意为之：run 只携带任务归属（方案 §7）。
 */
export function ConversationSidebar({
  rows,
  loading,
  selectedInstanceId,
  includeArchived,
  bulkPending,
  onSelect,
  onNew,
  onRename,
  onTogglePinned,
  onToggleArchive,
  onArchiveMany,
  onToggleArchived,
}: {
  rows: ConversationRow[];
  loading: boolean;
  selectedInstanceId?: string;
  includeArchived: boolean;
  bulkPending?: boolean;
  onSelect: (instanceId: string) => void;
  onNew: () => void;
  onRename: (instanceId: string, title: string) => void;
  onTogglePinned: (row: ConversationRow) => void;
  onToggleArchive: (row: ConversationRow) => void;
  onArchiveMany: (ids: string[]) => Promise<void>;
  onToggleArchived: () => void;
}) {
  const { t } = useI18n();
  const [taskFilter, setTaskFilter] = useState<string | null>(null);
  const [selectionMode, setSelectionMode] = useState(false);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(() => new Set());
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [renameText, setRenameText] = useState("");
  const [visibleCount, setVisibleCount] = useState(LIST_PAGE);
  const [collapsed, setCollapsed] = useState<Set<string>>(() => {
    try {
      const raw = localStorage.getItem(COLLAPSED_KEY);
      const keys = raw ? (JSON.parse(raw) as unknown) : [];
      return Array.isArray(keys) ? new Set(keys.filter((k): k is string => typeof k === "string")) : new Set();
    } catch {
      return new Set();
    }
  });

  function persistCollapsed(next: Set<string>) {
    try {
      localStorage.setItem(COLLAPSED_KEY, JSON.stringify([...next]));
    } catch {
      /* storage unavailable — collapse state stays session-local */
    }
  }

  function toggleCollapsed(key: string) {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      persistCollapsed(next);
      return next;
    });
  }

  // Drop stale selection ids when the list shrinks (archived away / deleted).
  useEffect(() => {
    setSelectedIds((current) => {
      if (current.size === 0) return current;
      const live = new Set(rows.map((r) => r.instance_id ?? r.id));
      const next = new Set([...current].filter((id) => live.has(id)));
      return next.size === current.size ? current : next;
    });
  }, [rows]);

  const filtered = useMemo(
    () => (taskFilter === null ? rows : rows.filter((r) => r.task_id === taskFilter)),
    [rows, taskFilter],
  );
  const visible = useMemo(() => filtered.slice(0, visibleCount), [filtered, visibleCount]);
  const pinned = useMemo(() => visible.filter((r) => r.pinned), [visible]);
  const groups = useMemo<TaskGroup[]>(() => {
    const byTask = new Map<string, TaskGroup>();
    for (const row of visible) {
      if (row.pinned) continue;
      const group = byTask.get(row.task_id) ?? {
        key: row.task_id,
        label: row.task_name || "",
        rows: [],
        runningCount: 0,
      };
      group.rows.push(row);
      if (row.status === "running") group.runningCount++;
      byTask.set(row.task_id, group);
    }
    return [...byTask.values()].sort((a, b) => a.label.localeCompare(b.label, "zh-CN"));
  }, [visible]);

  const taskOptions = useMemo(() => {
    const counts = new Map<string, number>();
    for (const row of rows) counts.set(row.task_id, (counts.get(row.task_id) ?? 0) + 1);
    const keys = new Set([...groups.map((g) => g.key), ...counts.keys()]);
    if (taskFilter !== null) keys.add(taskFilter);
    return [...keys]
      .map((key) => ({
        key,
        name: rows.find((r) => r.task_id === key)?.task_name || key,
        count: counts.get(key) ?? 0,
      }))
      .sort((a, b) => a.name.localeCompare(b.name, "zh-CN"));
  }, [rows, groups, taskFilter]);

  const countLabel =
    taskFilter === null
      ? t("chat.countAll").replace("{n}", String(rows.length))
      : t("chat.countFiltered").replace("{selected}", String(filtered.length)).replace("{total}", String(rows.length));

  function changeTaskFilter(key: string | null) {
    setTaskFilter(key);
    setVisibleCount(LIST_PAGE);
    setSelectedIds(new Set());
    setRenamingId(null);
  }

  function exitSelectionMode() {
    setSelectionMode(false);
    setSelectedIds(new Set());
  }

  async function archiveSelected() {
    const ids = [...selectedIds];
    if (ids.length === 0) return;
    await onArchiveMany(ids);
    exitSelectionMode();
  }

  const selectedCount = selectedIds.size;
  const allSelected = filtered.length > 0 && selectedCount === filtered.length;
  const headerState: boolean | "mixed" = allSelected ? true : selectedCount > 0 ? "mixed" : false;

  function itemProps(row: ConversationRow, showTask: boolean) {
    return {
      row,
      showTask,
      active: row.instance_id === selectedInstanceId,
      selectionMode,
      selected: selectedIds.has(row.instance_id ?? row.id),
      renaming: renamingId === row.id,
      renameText: renamingId === row.id ? renameText : "",
      onSelect: () => row.instance_id && onSelect(row.instance_id),
      onStartRename: () => {
        setRenamingId(row.id);
        setRenameText(row.title ?? "");
      },
      onRenameText: setRenameText,
      onCommitRename: (title: string) => {
        const next = title.trim();
        setRenamingId(null);
        if (next && next !== (row.title ?? "")) onRename(row.instance_id ?? "", next);
      },
      onCancelRename: () => setRenamingId(null),
      onTogglePinned: () => onTogglePinned(row),
      onToggleArchive: () => onToggleArchive(row),
      onSelectedChange: (checked: boolean) =>
        setSelectedIds((current) => {
          const next = new Set(current);
          const id = row.instance_id ?? row.id;
          if (checked) next.add(id);
          else next.delete(id);
          return next;
        }),
    };
  }

  return (
    <div className="flex min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-card">
      <div className="flex flex-col gap-2 border-b border-border p-2">
        <Button size="sm" className="w-full" onClick={onNew}>
          <Plus className="h-4 w-4" />
          {t("chat.newConversation")}
        </Button>
        <Select
          value={taskFilter === null ? "all" : `task:${taskFilter}`}
          onValueChange={(value) => changeTaskFilter(value === "all" ? null : value.slice(5))}
          disabled={bulkPending}
        >
          <SelectTrigger className="h-8 w-full min-w-0 text-xs" aria-label={t("chat.filterByTask")}>
            <Bot className="size-3.5" aria-hidden />
            <SelectValue placeholder={t("chat.allTasks")} />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value="all">{t("chat.allTasks")}</SelectItem>
              {taskOptions.map((task) => (
                <SelectItem key={task.key} value={`task:${task.key}`}>
                  {task.name}（{task.count}）
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        {rows.length > 0 &&
          (selectionMode ? (
            <div className="flex items-center gap-2 px-1">
              <HeaderCheckbox
                state={headerState}
                onChange={(checked) =>
                  setSelectedIds(
                    checked ? new Set(filtered.map((r) => r.instance_id ?? r.id)) : new Set(),
                  )
                }
                aria-label={t("chat.selectAll")}
                disabled={filtered.length === 0 || bulkPending}
              />
              <span className="min-w-0 flex-1 text-muted-foreground text-xs tabular-nums">
                {selectedCount > 0
                  ? t("chat.selectedCount").replace("{n}", String(selectedCount))
                  : countLabel}
              </span>
              {selectedCount > 0 && (
                <Button size="sm" variant="destructive" disabled={bulkPending} onClick={() => void archiveSelected()}>
                  <ListChecks className="mr-1 size-3.5" aria-hidden />
                  {t("chat.bulkArchive")}
                </Button>
              )}
              <Button size="sm" variant="ghost" onClick={exitSelectionMode}>
                {t("chat.exitSelection")}
              </Button>
            </div>
          ) : (
            <div className="flex items-center gap-2 px-1">
              <span className="min-w-0 flex-1 text-muted-foreground text-xs tabular-nums">{countLabel}</span>
              <Button
                size="sm"
                variant="ghost"
                className="text-muted-foreground"
                onClick={() => setSelectionMode(true)}
                disabled={filtered.length === 0}
              >
                <ListChecks className="mr-1 size-3.5" aria-hidden />
                {t("chat.multiSelect")}
              </Button>
            </div>
          ))}
      </div>

      <div className="min-h-0 min-w-0 flex-1 overflow-y-auto">
        <div className="flex min-w-0 flex-col gap-0.5 p-2">
          {loading && rows.length === 0 && (
            <div className="space-y-2 p-1">
              {Array.from({ length: 5 }).map((_, i) => (
                <Skeleton key={i} className="h-11 w-full" />
              ))}
            </div>
          )}
          {!loading && filtered.length === 0 && (
            <p className="px-2 py-6 text-center text-muted-foreground text-xs">
              {taskFilter === null ? t("chat.emptyTitle") : t("chat.filterEmpty")}
            </p>
          )}
          {pinned.map((row) => (
            <ConversationItem key={row.id} {...itemProps(row, true)} />
          ))}
          {groups.map((group) => {
            const isCollapsed = collapsed.has(group.key);
            return (
              <div key={group.key} className="mt-1">
                <button
                  type="button"
                  onClick={() => toggleCollapsed(group.key)}
                  aria-expanded={!isCollapsed}
                  title={isCollapsed ? `${t("chat.expandGroup")} ${group.label}` : `${t("chat.collapseGroup")} ${group.label}`}
                  className={cn(
                    "sticky top-0 z-10 flex min-w-0 items-center gap-1.5 rounded-md bg-card px-1.5 py-1 text-left font-medium text-xs transition-colors hover:bg-accent/50",
                    isCollapsed && group.rows.some((r) => r.instance_id === selectedInstanceId)
                      ? "text-foreground"
                      : "text-muted-foreground",
                  )}
                >
                  <ChevronRight
                    className={cn("size-3 shrink-0 transition-transform", !isCollapsed && "rotate-90")}
                    aria-hidden
                  />
                  <span className="min-w-0 flex-1 truncate">{group.label || t("chat.untitled")}</span>
                  {isCollapsed &&
                    group.rows.some((r) => r.instance_id === selectedInstanceId) && (
                      <span className="size-1.5 shrink-0 rounded-full bg-primary" aria-hidden />
                    )}
                  {group.runningCount > 0 && (
                    <Loader2 className="size-3 shrink-0 animate-spin" aria-label={t("chat.status.running")} />
                  )}
                  <span className="shrink-0 tabular-nums opacity-60">{group.rows.length}</span>
                </button>
                {!isCollapsed && (
                  <div className="mt-0.5 flex flex-col gap-0.5">
                    {group.rows.map((row) => (
                      <ConversationItem key={row.id} {...itemProps(row, false)} />
                    ))}
                  </div>
                )}
              </div>
            );
          })}
          {visibleCount < filtered.length && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="mt-1 w-full"
              onClick={() => setVisibleCount((count) => count + LIST_PAGE)}
            >
              {t("chat.loadMore")}
            </Button>
          )}
        </div>
      </div>

      <button
        type="button"
        onClick={onToggleArchived}
        className="flex items-center justify-center gap-1.5 border-t border-border px-2 py-1.5 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
      >
        {includeArchived ? t("chat.hideArchived") : t("chat.showArchived")}
      </button>
    </div>
  );
}
