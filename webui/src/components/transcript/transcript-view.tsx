import { Fragment, useState } from "react";
import type { ReactNode } from "react";
import {
  AlertTriangle,
  Check,
  ChevronRight,
  Copy,
  MessageSquare,
  Pencil,
  ShieldAlert,
  Terminal,
  User,
} from "lucide-react";
import { toast } from "sonner";
import { Link } from "react-router-dom";
import { Markdown } from "@/components/markdown";
import { IntentProposalCard } from "@/components/chat/intent-proposal-card";
import { cn } from "@/lib/utils";
import { useI18n } from "@/i18n";
import type { TranscriptItem } from "@/types/api";

/**
 * 纯展示转录层（docs/SESSIONS_CHAT_1TO1_PLAN.md §5.2）：只吃归一后的
 * TranscriptItem[]，不持有数据、不做分页与滚动。七类行逐项对齐参照：
 * human 右气泡 / assistant 左气泡 / 工具折叠卡 / 审批卡 / 发现卡 / 系统行。
 * 会话页与对话页经各自的数据组件（chat/transcript.tsx）适配后共用。
 */

/** 相邻同名工具的 use/result 合成一张折叠卡（服务端无配对 ID，邻接即配对）。 */
type ToolGroup = { use?: TranscriptItem; result?: TranscriptItem };

function groupToolRows(items: TranscriptItem[]): (TranscriptItem | ToolGroup)[] {
  const out: (TranscriptItem | ToolGroup)[] = [];
  for (let i = 0; i < items.length; i++) {
    const item = items[i];
    if (item.kind === "tool_use") {
      const next = items[i + 1];
      if (next && next.kind === "tool_result" && next.tool === item.tool) {
        out.push({ use: item, result: next });
        i++;
        continue;
      }
      out.push({ use: item });
      continue;
    }
    if (item.kind === "tool_result") {
      out.push({ result: item });
      continue;
    }
    out.push(item);
  }
  return out;
}

/** 单行时间戳（参照口径：MM/DD HH:mm tabular-nums）。 */
function ActivityTime({ at }: { at?: string }) {
  const date = at ? new Date(at) : null;
  if (!at || !date || Number.isNaN(date.getTime())) return null;
  return (
    <time
      dateTime={date.toISOString()}
      title={date.toLocaleString()}
      className="text-xs text-muted-foreground tabular-nums"
    >
      {date.toLocaleDateString("zh-CN", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      })}
    </time>
  );
}

/** guidance 生命周期章（injected/queued=violet、consumed=emerald，对齐
 * live-feed TYPE_COLOR 先例；UI_DESIGN §3.3 胶囊形态：10% 底 + 30% 边）。 */
function GuidanceStatusChip({ status }: { status: string }) {
  const { t } = useI18n();
  const consumed = status === "consumed";
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full border px-1.5 py-0.5 text-[10px] leading-none",
        consumed
          ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
          : "border-violet-500/30 bg-violet-500/10 text-violet-600 dark:text-violet-400",
      )}
    >
      <span
        className={cn(
          "size-1 rounded-full",
          consumed ? "bg-emerald-500" : "bg-violet-500",
        )}
        aria-hidden
      />
      {t(`guidance.status.${status}`)}
    </span>
  );
}

/** 人工回合：右对齐品牌色气泡 + 头像（参照 UserRow）。guidance 行带状态章。 */
function HumanRow({ item, onEdit }: { item: TranscriptItem; onEdit?: (text: string) => void }) {
  const { t } = useI18n();
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(item.text ?? "");
      toast.success(t("chat.copied"));
    } catch {
      toast.error(t("chat.copied"));
    }
  };
  return (
    <div className="mt-3 mb-2 flex min-w-0 justify-end gap-2">
      <div className="flex min-w-0 max-w-[85%] flex-col items-end gap-1.5">
        <div className="min-w-0 max-w-full whitespace-pre-wrap rounded-lg rounded-tr-sm bg-primary px-3 py-1.5 text-primary-foreground text-sm [overflow-wrap:anywhere]">
          {item.text}
        </div>
        <div className="flex items-center gap-1.5">
          {item.status && <GuidanceStatusChip status={item.status} />}
          <button
            type="button"
            className="inline-flex items-center rounded p-0.5 text-muted-foreground transition-colors hover:bg-accent/40 hover:text-foreground"
            title={t("chat.copyMessage")}
            aria-label={t("chat.copyMessage")}
            onClick={() => void copy()}
          >
            <Copy className="size-3" aria-hidden />
          </button>
          {onEdit && (
            <button
              type="button"
              className="inline-flex items-center rounded p-0.5 text-muted-foreground transition-colors hover:bg-accent/40 hover:text-foreground"
              title={t("chat.editMessage")}
              aria-label={t("chat.editMessage")}
              onClick={() => onEdit(item.text ?? "")}
            >
              <Pencil className="size-3" aria-hidden />
            </button>
          )}
          <ActivityTime at={item.at} />
        </div>
      </div>
      <div className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full bg-primary/10">
        <User className="size-3.5 text-primary" aria-hidden />
      </div>
    </div>
  );
}

/**
 * assistant 结论块（参照 AnswerBlock）：通栏 muted 卡片，Markdown 全文展开。
 * role="chat" 是旁路提问的应答（不经扫描主循环）：加来源章区分主循环叙述；
 * status="failed" 是诚实失败行，destructive 呈现。
 */
function AssistantRow({ item }: { item: TranscriptItem }) {
  const { t } = useI18n();
  const failed = item.status === "failed";
  return (
    <div className="mt-1 mb-2 flex min-w-0 flex-col gap-1">
      {item.role === "chat" && (
        <div className="flex items-center gap-1 pl-1 text-muted-foreground text-[11px]">
          <MessageSquare className="size-3" aria-hidden />
          {failed ? t("chat.answerFailed") : t("chat.sideAnswer")}
        </div>
      )}
      <div
        className={cn(
          "min-w-0 break-words rounded-lg px-3 py-2 text-sm",
          failed
            ? "border border-red-500/40 bg-red-500/5 text-red-600 dark:text-red-400"
            : "bg-muted",
        )}
      >
        <Markdown source={item.text ?? ""} />
      </div>
      <ActivityTime at={item.at} />
    </div>
  );
}

/** 工具折叠卡（参照 ToolBlock）：折叠=摘要+状态，展开=参数与输出。 */
function ToolCard({ group }: { group: ToolGroup }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const tool = group.use?.tool || group.result?.tool || t("chat.toolCall");
  const running = !group.result;
  const summary = firstLine(group.use?.text || stringifyArgs(group.use?.args));
  const output = firstLine(group.result?.output);
  const statusTone = running
    ? "text-muted-foreground"
    : "text-emerald-600 dark:text-emerald-400";
  return (
    <section className="text-xs">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex w-full items-start gap-2 py-1 text-left hover:bg-muted/40"
      >
        <ChevronRight
          className={cn("mt-0.5 size-3 shrink-0 text-muted-foreground transition-transform", open && "rotate-90")}
          aria-hidden
        />
        <Terminal
          className={cn("mt-0.5 size-3.5 shrink-0", running ? "text-sky-500" : statusTone)}
          aria-hidden
        />
        <span className="shrink-0 font-medium text-sky-600 dark:text-sky-400">{tool}</span>
        <span className="min-w-0 flex-1 truncate font-mono text-muted-foreground">{summary}</span>
        <span className={cn("ml-auto inline-flex shrink-0 items-center gap-0.5 font-medium", statusTone)}>
          {running ? (
            t("chat.toolRunning")
          ) : (
            <>
              <Check className="size-3" aria-hidden />
              <span className="sr-only">{t("chat.status.finished")}</span>
            </>
          )}
        </span>
      </button>
      {open && (
        <pre className="mb-1 ml-7 max-h-72 overflow-auto whitespace-pre-wrap break-all rounded bg-muted/50 p-2 font-mono text-xs leading-relaxed">
          {group.use && (
            <>
              <span className="text-muted-foreground">{"【" + t("chat.toolInputLabel") + "】\n"}</span>
              {prettyJSON(group.use.args) || group.use.text || "—"}
              {"\n\n"}
            </>
          )}
          {group.result && (
            <>
              <span className="text-muted-foreground">{"【" + t("chat.toolOutputLabel") + "】\n"}</span>
              {group.result.output || "—"}
              {group.result.truncated && (
                <span className="text-muted-foreground">
                  {"\n"}
                  {t("chat.truncated")}
                </span>
              )}
            </>
          )}
          {!group.use && !group.result && "—"}
        </pre>
      )}
      {!open && output && !running && (
        <p className="mb-1 ml-7 truncate font-mono text-[10px] text-muted-foreground">{output}</p>
      )}
    </section>
  );
}

/** 审批卡（amber，参照 InterceptCard 语义；决策走 /approvals 队列）。 */
function ApprovalCard({ item }: { item: TranscriptItem }) {
  const { t } = useI18n();
  const approval = item.approval;
  const headline = approval
    ? `${t("chat.approvalRequest")} · ${approval.kind}`
    : item.text || t("chat.approvalRequest");
  const decided = approval?.decided;
  const outcome = approval
    ? decided
      ? [approval.status, approval.decided_by, approval.reason].filter(Boolean).join(" · ")
      : t("chat.awaitingDecision")
    : item.status;
  return (
    <div className="my-2 flex flex-wrap items-start justify-between gap-2 rounded-lg border border-amber-500/50 bg-amber-500/10 p-3 text-xs">
      <div className="flex min-w-0 items-start gap-2">
        <ShieldAlert className="mt-0.5 size-3.5 shrink-0 text-amber-600 dark:text-amber-400" aria-hidden />
        <div className="min-w-0 space-y-0.5">
          <p className="break-words font-medium">{headline}</p>
          {item.text && approval && <p className="break-words text-muted-foreground">{item.text}</p>}
          {outcome && <p className="font-mono text-[10px] text-muted-foreground">{outcome}</p>}
        </div>
      </div>
      {!decided && (
        <Link
          to="/approvals"
          className="inline-flex h-6 shrink-0 items-center rounded-md bg-primary px-2 text-xs font-medium text-primary-foreground transition-colors hover:bg-primary/90"
        >
          {t("approval.openQueue")}
        </Link>
      )}
    </div>
  );
}

/** 发现卡（amber 描边，参照 finding 语义）。 */
function FindingCard({ item }: { item: TranscriptItem }) {
  return (
    <div className="my-2 flex gap-2 rounded-lg border border-amber-500/40 bg-amber-500/5 px-3 py-2 text-sm">
      <AlertTriangle className="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" aria-hidden />
      <div className="min-w-0">
        <p className="break-words">{item.text}</p>
        {item.status && <p className="mt-0.5 font-mono text-[10px] text-muted-foreground">{item.status}</p>}
        <ActivityTime at={item.at} />
      </div>
    </div>
  );
}

export function TranscriptView({
  items,
  instanceId,
  live,
  staleFor,
  unobserved,
  empty,
  taskId,
  onEditMessage,
}: {
  items: TranscriptItem[];
  /** Run id for proposal-card actions (confirm/reject endpoints). */
  instanceId?: string;
  /** 流式中：5s 内有真实事件到达（use-instance-activity）。 */
  live?: boolean;
  /** 运行中但事件停滞：预格式化的停滞时长（"2 分钟"）。 */
  staleFor?: string | null;
  /** 运行中但本页会话尚未观测到任何事件：中性等待行，不冒充流式。 */
  unobserved?: boolean;
  empty?: ReactNode;
  /** Platform task id for proposal receipt worker links. */
  taskId?: string;
  /** Load a human turn back into the composer for editing and re-sending. */
  onEditMessage?: (text: string) => void;
}) {
  const { t } = useI18n();
  if (items.length === 0) {
    return <>{empty ?? <p className="py-10 text-center text-muted-foreground text-sm">{t("chat.noMessages")}</p>}</>;
  }
  let lastPhase = 0;
  return (
    <div className="flex flex-col gap-1">
      {groupToolRows(items).map((entry, index) => {
        // 阶段分段：工具卡的 inferred phase 变化处插一条分隔线（Track C），
        // 让上一轮的终态与新一轮工作在视觉上分开。
        const phase =
          "use" in entry
            ? entry.use?.phase ?? 0
            : "result" in entry
              ? entry.result?.phase ?? 0
              : 0;
        const phaseChanged = phase > 0 && phase !== lastPhase;
        if (phaseChanged) lastPhase = phase;
        return (
          <Fragment key={`row-${index}`}>
            {phaseChanged && (
              <div className="my-2 flex items-center gap-2 font-medium text-muted-foreground text-[11px]">
                <span className="h-px flex-1 bg-border" />
                {t("chat.phaseDivider").replace("{n}", String(phase))}
                <span className="h-px flex-1 bg-border" />
              </div>
            )}
            {renderEntry(entry, index, instanceId, onEditMessage, taskId)}
          </Fragment>
        );
      })}
      {(live || staleFor || unobserved) && (
        <div className="flex items-center gap-2 pt-1 pl-2 text-xs text-muted-foreground">
          <span
            className={cn(
              "size-1.5 rounded-full",
              live
                ? "animate-pulse bg-emerald-500"
                : staleFor
                  ? "animate-pulse bg-amber-500"
                  : "bg-muted-foreground/40",
            )}
            aria-hidden
          />
          {live
            ? t("chat.agentWorking")
            : staleFor
              ? t("chat.staleNoOutput").replace("{duration}", staleFor)
              : t("chat.noLiveEvents")}
        </div>
      )}
    </div>
  );
}

function renderEntry(
  entry: TranscriptItem | ToolGroup,
  index: number,
  instanceId?: string,
  onEditMessage?: (text: string) => void,
  taskId?: string,
): ReactNode {
  if ("kind" in entry) {
    switch (entry.kind) {
      case "human":
        return <HumanRow key={entry.seq} item={entry} onEdit={onEditMessage} />;
      case "assistant":
        return <AssistantRow key={entry.seq} item={entry} />;
      case "proposal":
        return entry.proposal ? (
          <IntentProposalCard
            key={entry.seq}
            instanceId={instanceId}
            taskId={taskId}
            proposal={entry.proposal}
          />
        ) : (
          <Fragment key={entry.seq} />
        );
      case "approval":
        return <ApprovalCard key={entry.seq} item={entry} />;
      case "finding":
        return <FindingCard key={entry.seq} item={entry} />;
      case "system":
        return (
          <p key={entry.seq} className="my-1 text-center text-[11px] text-muted-foreground">
            {entry.text}
          </p>
        );
      default:
        return <Fragment key={entry.seq} />;
    }
  }
  return <ToolCard key={`${entry.use?.seq ?? entry.result?.seq ?? index}`} group={entry} />;
}

function stringifyArgs(args: unknown): string {
  if (args == null) return "";
  if (typeof args === "string") return args;
  try {
    return JSON.stringify(args);
  } catch {
    return "";
  }
}

function prettyJSON(value: unknown): string {
  if (value == null) return "";
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function firstLine(text?: string): string {
  if (!text) return "";
  const line = text.split("\n").find((l) => l.trim() !== "") ?? "";
  return line.length > 80 ? `${line.slice(0, 80)}…` : line;
}
