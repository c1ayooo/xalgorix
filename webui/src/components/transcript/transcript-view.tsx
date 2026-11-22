import { Fragment, useState } from "react";
import type { ReactNode } from "react";
import {
  AlertTriangle,
  Check,
  ChevronRight,
  ShieldAlert,
  Terminal,
  User,
} from "lucide-react";
import { Link } from "react-router-dom";
import { Markdown } from "@/components/markdown";
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

/** 人工回合：右对齐品牌色气泡 + 头像（参照 UserRow）。 */
function HumanRow({ item }: { item: TranscriptItem }) {
  return (
    <div className="mt-3 mb-2 flex min-w-0 justify-end gap-2">
      <div className="flex min-w-0 max-w-[85%] flex-col items-end gap-1.5">
        <div className="min-w-0 max-w-full whitespace-pre-wrap rounded-lg rounded-tr-sm bg-primary px-3 py-1.5 text-primary-foreground text-sm [overflow-wrap:anywhere]">
          {item.text}
        </div>
        {item.status && item.status !== "injected" && (
          <p className="font-mono text-[10px] text-muted-foreground">{item.status}</p>
        )}
        <ActivityTime at={item.at} />
      </div>
      <div className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full bg-primary/10">
        <User className="size-3.5 text-primary" aria-hidden />
      </div>
    </div>
  );
}

/** agent 结论块（参照 AnswerBlock）：通栏 muted 卡片，Markdown 全文展开不折叠。 */
function AssistantRow({ item }: { item: TranscriptItem }) {
  return (
    <div className="mt-1 mb-2 flex min-w-0 flex-col gap-1">
      <div className="min-w-0 break-words rounded-lg bg-muted px-3 py-2 text-sm">
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
  live,
  empty,
}: {
  items: TranscriptItem[];
  live?: boolean;
  empty?: ReactNode;
}) {
  const { t } = useI18n();
  if (items.length === 0) {
    return <>{empty ?? <p className="py-10 text-center text-muted-foreground text-sm">{t("chat.noMessages")}</p>}</>;
  }
  return (
    <div className="flex flex-col gap-1">
      {groupToolRows(items).map((entry, index) => {
        if ("kind" in entry) {
          switch (entry.kind) {
            case "human":
              return <HumanRow key={entry.seq} item={entry} />;
            case "assistant":
              return <AssistantRow key={entry.seq} item={entry} />;
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
      })}
      {live && (
        <div className="flex items-center gap-2 pt-1 pl-2 text-xs text-muted-foreground">
          <span className="size-1.5 animate-pulse rounded-full bg-emerald-500" aria-hidden />
          {t("chat.agentWorking")}
        </div>
      )}
    </div>
  );
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
