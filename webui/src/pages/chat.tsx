import { useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ConversationSidebar } from "@/components/chat/conversation-sidebar";
import { ChatPanel } from "@/components/chat/chat-panel";
import { DraftChat } from "@/components/chat/draft-chat";
import { useProjectConversations, useSendTaskGuidance, useStartTaskRun, useTasks, useUpdateConversation } from "@/api/queries";
import { useActiveProjectId } from "@/lib/use-active-project";
import { useI18n } from "@/i18n";
import { newRequestId } from "@/lib/guidance";
import type { ConversationRow } from "@/types/api";

/**
 * Chat — 项目级会话中心（1:1 复刻，docs/SESSIONS_CHAT_1TO1_PLAN.md §6）。
 *
 * 左栏：新建对话 + 任务筛选 + 计数/多选 + 置顶 + 按任务分组（折叠持久化）
 * 右栏：无 ?run= 时 DraftChat（首条消息才创建 Run）；选中后 ChatView。
 * URL 是唯一权威：/chat?run=&view=full|human&archived=1；不再自动选第一条。
 */
export default function ChatPage() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const projectId = useActiveProjectId();
  const archived = searchParams.get("archived") === "1";
  const view = searchParams.get("view") === "human" ? "human" : "full";

  const { data: tasksData } = useTasks(projectId ?? undefined);
  const tasks = useMemo(() => tasksData?.tasks ?? [], [tasksData]);
  const { data: convData, isLoading } = useProjectConversations(projectId ?? undefined, archived);
  const rows = useMemo<ConversationRow[]>(() => convData?.items ?? [], [convData]);
  const update = useUpdateConversation();
  const startRun = useStartTaskRun();
  const send = useSendTaskGuidance();

  const runParam = searchParams.get("run") ?? undefined;
  const selected = runParam ? (rows.find((row) => row.instance_id === runParam) ?? null) : null;
  const [bulkPending, setBulkPending] = useState(false);
  const [starting, setStarting] = useState(false);
  const [requestId, setRequestId] = useState(newRequestId);
  const queryClient = useQueryClient();

  function writeParams(mutate: (sp: URLSearchParams) => void) {
    const sp = new URLSearchParams(searchParams);
    mutate(sp);
    setSearchParams(sp, { replace: true });
  }

  function selectRun(instanceId: string) {
    writeParams((sp) => sp.set("run", instanceId));
  }

  function newDraft() {
    writeParams((sp) => sp.delete("run"));
  }

  function patchConversation(instanceId: string, patch: { title?: string; pinned?: boolean; archived?: boolean }) {
    return new Promise<void>((resolve, reject) => {
      update.mutate(
        { instanceId, patch },
        {
          onSuccess: () => resolve(),
          onError: (error: Error) => {
            toast.error(`${t("chat.updateFailed")}: ${error.message}`);
            reject(error);
          },
        },
      );
    });
  }

  async function archiveMany(ids: string[]) {
    if (ids.length === 0) return;
    setBulkPending(true);
    const results = await Promise.allSettled(ids.map((id) => patchConversation(id, { archived: true })));
    setBulkPending(false);
    const failed = results.filter((r) => r.status === "rejected").length;
    if (failed === 0) toast.success(t("chat.archivedMany").replace("{n}", String(ids.length)));
    else
      toast.error(
        t("chat.archivedPartial").replace("{failed}", String(failed)).replace("{total}", String(ids.length)),
      );
  }

  /** 草稿首条消息：选任务 → 创建 Run → 发 guidance → 刷新列表 → 写 ?run= 切详情。 */
  async function startDraft(taskId: string, message: string) {
    setStarting(true);
    try {
      const res = await startRun.mutateAsync({ id: taskId });
      const instanceId = res.instance_id;
      if (!instanceId) throw new Error(t("chat.startFailed"));
      await new Promise<void>((resolve, reject) => {
        send.mutate(
          { id: taskId, message, requestId, instanceId },
          {
            onSuccess: (result) => {
              toast.success(result.status === "injected" ? t("chat.guidanceInjected") : t("chat.guidanceQueued"));
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
      // 先让列表拿到新 Run 再写 ?run=：否则 selected 解析不到、页面停在草稿态
      // 干等 8s 轮询（参照的乐观插入语义，用缓存失效等价实现）。
      await queryClient.invalidateQueries({ queryKey: ["conversation"] });
      selectRun(instanceId);
    } finally {
      setStarting(false);
    }
  }

  return (
    // 视口定高（全断点）：面板内部滚动、composer 钉底，页面级不滚。
    // 高度预算 = 100svh − topbar 3.5 − OpsPulse 2.25；内边距随断点（参照 p-3/sm:p-4/md:p-6）。
    <div className="flex h-[calc(100svh-5.75rem)] min-w-0 flex-col overflow-hidden p-3 sm:p-4 md:p-6">
      <div className="grid min-h-0 min-w-0 flex-1 grid-cols-1 grid-rows-[minmax(10rem,15rem)_minmax(0,1fr)] gap-3 md:grid-cols-[18rem_minmax(0,1fr)] md:grid-rows-[minmax(0,1fr)] md:gap-4">
        <ConversationSidebar
          rows={rows}
          loading={isLoading}
          selectedInstanceId={selected?.instance_id}
          includeArchived={archived}
          bulkPending={bulkPending}
          onSelect={selectRun}
          onNew={newDraft}
          onRename={(instanceId, title) => void patchConversation(instanceId, { title })}
          onTogglePinned={(row) =>
            row.instance_id && void patchConversation(row.instance_id, { pinned: !row.pinned })
          }
          onToggleArchive={(row) =>
            row.instance_id && void patchConversation(row.instance_id, { archived: !row.archived })
          }
          onArchiveMany={archiveMany}
          onToggleArchived={() =>
            writeParams((sp) => {
              if (archived) sp.delete("archived");
              else sp.set("archived", "1");
            })
          }
        />

        <div className="flex min-h-0 min-w-0 flex-col overflow-hidden rounded-lg border border-border bg-card">
          {selected ? (
            <ChatPanel
              key={selected.instance_id}
              run={selected}
              view={view}
              onViewChange={(next) => writeParams((sp) => sp.set("view", next))}
              onRename={(title) =>
                selected.instance_id ? patchConversation(selected.instance_id, { title }) : Promise.resolve()
              }
            />
          ) : (
            <DraftChat tasks={tasks} starting={starting} onSend={startDraft} />
          )}
        </div>
      </div>
    </div>
  );
}
