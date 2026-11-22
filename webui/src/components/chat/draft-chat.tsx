import { useState } from "react";
import { Bot } from "lucide-react";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Composer } from "@/components/chat/composer";
import { useI18n } from "@/i18n";
import type { Task } from "@/types/api";

/**
 * DraftChat（参照同名词）：未选中会话时的默认右栏 —— 任务选择器 + 空态引导 +
 * Composer。对话（Run）在首条消息发送时才创建（ChatGPT 式懒创建），创建失败
 * 或发送失败都留在草稿态、文本不丢。附件需要已存在的 run 目录，草稿态不提供
 * （已知差异，见 docs/SESSIONS_CHAT_1TO1_ACCEPTANCE.md）。
 */
export function DraftChat({
  tasks,
  starting,
  onSend,
}: {
  tasks: Task[];
  starting?: boolean;
  onSend: (taskId: string, message: string) => Promise<void>;
}) {
  const { t } = useI18n();
  const [taskId, setTaskId] = useState(tasks[0]?.id ?? "");
  const value = tasks.some((task) => task.id === taskId) ? taskId : (tasks[0]?.id ?? "");
  const selected = tasks.find((task) => task.id === value);

  const picker = (
    <Select value={value} onValueChange={setTaskId}>
      <SelectTrigger className="w-full sm:w-44" aria-label={t("chat.pickTask")}>
        <SelectValue placeholder={t("chat.draftPickTask")} />
      </SelectTrigger>
      <SelectContent>
        <SelectGroup>
          {tasks.map((task) => (
            <SelectItem key={task.id} value={task.id}>
              <span className="flex min-w-0 items-center gap-2">
                <Bot className="size-3.5 shrink-0" aria-hidden />
                <span className="min-w-0 truncate">{task.name}</span>
              </span>
            </SelectItem>
          ))}
        </SelectGroup>
      </SelectContent>
    </Select>
  );

  return (
    // 草稿恒为空态：composer 垂直居中（ChatGPT 新会话式），引导文案贴在其上方。
    // 前后两块等高 flex-1 夹住 composer，保证严格居中。
    <>
      <div className="flex min-h-0 flex-1 flex-col items-center justify-end gap-2 px-4 pb-3 text-center">
        <div className="flex size-12 items-center justify-center rounded-full bg-primary/10">
          <Bot className="size-6 text-primary" aria-hidden />
        </div>
        <div className="font-medium text-sm">
          {selected ? t("chat.draftGreeting").replace("{name}", selected.name) : t("chat.draftEmptyTitle")}
        </div>
        {selected?.goal && <p className="max-w-md text-muted-foreground text-xs">{selected.goal}</p>}
        {tasks.length === 0 && (
          <p className="max-w-md text-muted-foreground text-xs">{t("chat.noTasksHint")}</p>
        )}
      </div>
      <Composer
        disabled={tasks.length === 0 || starting || !value}
        placeholder={t("chat.draftPlaceholder")}
        leftSlot={picker}
        className="mx-auto w-full max-w-3xl rounded-lg border border-border"
        onSend={async (message) => {
          if (!value) return;
          await onSend(value, message);
        }}
      />
      <div aria-hidden className="flex-1" />
    </>
  );
}
