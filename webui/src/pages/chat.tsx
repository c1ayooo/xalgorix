import { useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { toast } from "sonner";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { ConversationSidebar } from "@/components/chat/conversation-sidebar";
import { ChatPanel } from "@/components/chat/chat-panel";
import {
  useProjectConversations,
  useStartTaskRun,
  useTasks,
  useUpdateConversation,
} from "@/api/queries";
import { useActiveProjectId } from "@/lib/use-active-project";
import { useI18n } from "@/i18n";
import type { ConversationRow, Task } from "@/types/api";

/**
 * Chat — 项目级会话中心（docs/CHAT_PAGE_REDESIGN.md）。
 *
 * 左栏：搜索 + 置顶 + 按任务分组的会话列表；会话有标题，可置顶、可归档。
 * 右栏：完整转录（人工 + agent + 工具 + finding）或「仅人工」视图，底部
 * composer 既可补指令也可停掉本次运行。
 *
 * URL 是唯一权威：/chat?run=&view=full|human&archived=1。
 */
export default function ChatPage() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const projectId = useActiveProjectId();
  const archived = searchParams.get("archived") === "1";
  const view = searchParams.get("view") === "human" ? "human" : "full";

  const { data: tasksData } = useTasks(projectId ?? undefined);
  const tasks = tasksData?.tasks ?? [];
  const { data: convData, isLoading } = useProjectConversations(projectId ?? undefined, archived);
  const rows = useMemo<ConversationRow[]>(() => convData?.items ?? [], [convData]);
  const update = useUpdateConversation();

  const runParam = searchParams.get("run") ?? undefined;
  const selected = rows.find((row) => row.instance_id === runParam) ?? rows[0];

  function writeParams(mutate: (sp: URLSearchParams) => void) {
    const sp = new URLSearchParams(searchParams);
    mutate(sp);
    setSearchParams(sp, { replace: true });
  }

  // Keep the URL pointing at a real conversation so a reload restores it.
  useEffect(() => {
    if (!selected?.instance_id || selected.instance_id === runParam) return;
    writeParams((sp) => sp.set("run", selected.instance_id!));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selected?.instance_id, runParam]);

  const [newChatOpen, setNewChatOpen] = useState(false);

  function patchConversation(instanceId: string, patch: { title?: string; pinned?: boolean; archived?: boolean }) {
    update.mutate(
      { instanceId, patch },
      {
        onError: (error: Error) => toast.error(`${t("chat.updateFailed")}: ${error.message}`),
      },
    );
  }

  return (
    <div className="grid items-stretch gap-4 lg:h-[calc(100vh-11.5rem)] lg:grid-cols-[20rem_minmax(0,1fr)] lg:grid-rows-[minmax(0,1fr)]">
      <ConversationSidebar
        rows={rows}
        loading={isLoading}
        selectedInstanceId={selected?.instance_id}
        includeArchived={archived}
        onSelect={(instanceId) =>
          writeParams((sp) => {
            sp.set("run", instanceId);
          })
        }
        onRename={(instanceId, title) => patchConversation(instanceId, { title })}
        onTogglePin={(row) =>
          row.instance_id && patchConversation(row.instance_id, { pinned: !row.pinned })
        }
        onToggleArchive={(row) =>
          row.instance_id &&
          patchConversation(row.instance_id, { archived: !row.archived })
        }
        onToggleArchived={() =>
          writeParams((sp) => {
            if (archived) sp.delete("archived");
            else sp.set("archived", "1");
          })
        }
        onNew={() => setNewChatOpen(true)}
      />

      <ChatPanel
        taskId={selected?.task_id}
        run={selected}
        view={view}
        onViewChange={(next) =>
          writeParams((sp) => {
            sp.set("view", next);
          })
        }
        onRename={(title) => selected?.instance_id && patchConversation(selected.instance_id, { title })}
      />

      <NewConversationDialog
        open={newChatOpen}
        onOpenChange={setNewChatOpen}
        tasks={tasks}
        onStarted={(instanceId) => {
          writeParams((sp) => {
            sp.set("run", instanceId);
          });
        }}
      />
    </div>
  );
}

/** 发起对话：选任务 → 发起新 Run。对话的宿主是 Run，弹窗不做扫描配置。 */
function NewConversationDialog({
  open,
  onOpenChange,
  tasks,
  onStarted,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  tasks: Task[];
  onStarted: (instanceId: string) => void;
}) {
  const { t } = useI18n();
  const startRun = useStartTaskRun();
  const [picked, setPicked] = useState<string>(tasks[0]?.id ?? "");
  const value = tasks.some((task) => task.id === picked) ? picked : tasks[0]?.id ?? "";

  function start() {
    if (!value || startRun.isPending) return;
    startRun.mutate(
      { id: value },
      {
        onSuccess: (res) => {
          toast.success(t("chat.chatStarted"));
          onStarted(res.instance_id);
          onOpenChange(false);
        },
        onError: (err: Error) => toast.error(`${t("chat.startChatFailed")}: ${err.message}`),
      },
    );
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle>{t("chat.newConversation")}</DialogTitle>
          <DialogDescription>{t("chat.newChatHint")}</DialogDescription>
        </DialogHeader>
        {tasks.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            {t("chat.noTasks")}{" "}
            <Link to="/tasks" className="text-primary hover:underline">
              {t("chat.createTask")}
            </Link>
          </p>
        ) : (
          <Select value={value} onValueChange={setPicked}>
            <SelectTrigger aria-label={t("chat.pickTask")}>
              <SelectValue placeholder={t("chat.pickTask")} />
            </SelectTrigger>
            <SelectContent>
              {tasks.map((task) => (
                <SelectItem key={task.id} value={task.id}>
                  {task.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button onClick={start} disabled={tasks.length === 0 || !value || startRun.isPending}>
            {startRun.isPending ? t("chat.startingChat") : t("chat.startChat")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
