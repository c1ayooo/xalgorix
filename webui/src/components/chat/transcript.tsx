import { useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import {
  AlertTriangle,
  ArrowDown,
  Bot,
  ChevronRight,
  Loader2,
  ShieldCheck,
  Terminal,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState, ErrorState } from "@/components/states";
import { Markdown } from "@/components/markdown";
import { cn } from "@/lib/utils";
import { useTranscript } from "@/api/queries";
import { useI18n } from "@/i18n";
import type { TranscriptItem } from "@/types/api";

/**
 * Conversation transcript: human turns, agent narration, tool cards, findings
 * and terminal markers in one time-ordered list. Pagination reads backwards
 * from the newest page so opening a long conversation lands on the result.
 */
export function Transcript({
  instanceId,
  running,
  className,
}: {
  instanceId?: string;
  running?: boolean;
  className?: string;
}) {
  const { t } = useI18n();
  const [beforeSeq, setBeforeSeq] = useState<number | undefined>(undefined);
  const [pages, setPages] = useState<TranscriptItem[]>([]);
  const [atBottom, setAtBottom] = useState(true);
  const scrollRef = useRef<HTMLDivElement>(null);
  const { data, isLoading, error, refetch } = useTranscript(instanceId, beforeSeq);

  // Reset when switching conversations: the older pages belong to another run.
  useEffect(() => {
    setBeforeSeq(undefined);
    setPages([]);
    setAtBottom(true);
  }, [instanceId]);

  useEffect(() => {
    if (!data?.items?.length) return;
    setPages((prev) => {
      const bySeq = new Map<number, TranscriptItem>();
      for (const item of [...prev, ...data.items]) bySeq.set(item.seq, item);
      return Array.from(bySeq.values()).sort((a, b) => a.seq - b.seq);
    });
  }, [data]);

  useEffect(() => {
    if (!atBottom) return;
    const node = scrollRef.current;
    if (!node) return;
    node.scrollTop = node.scrollHeight;
  }, [pages, atBottom]);

  const hasMore = data?.has_more ?? false;
  const earliest = pages[0]?.seq;

  const groups = useMemo(() => groupByDay(pages, t), [pages, t]);

  if (!instanceId) {
    return (
      <div className="grid h-full place-items-center p-6">
        <EmptyState title={t("chat.pickOne")} description={t("chat.pickOneHint")} />
      </div>
    );
  }
  if (isLoading && pages.length === 0) {
    return (
      <div className="space-y-3 p-4">
        {Array.from({ length: 5 }).map((_, i) => (
          <Skeleton key={i} className={cn("h-12", i % 2 ? "w-2/3" : "w-4/5")} />
        ))}
      </div>
    );
  }
  if (error && pages.length === 0) {
    return (
      <div className="p-6">
        <ErrorState
          description={(error as Error).message}
          action={
            <Button size="sm" variant="outline" onClick={() => void refetch()}>
              {t("chat.retry")}
            </Button>
          }
        />
      </div>
    );
  }

  return (
    <div className={cn("relative flex min-h-0 flex-1 flex-col", className)}>
      <div
        ref={scrollRef}
        onScroll={(e) => {
          const node = e.currentTarget;
          setAtBottom(node.scrollHeight - node.scrollTop - node.clientHeight < 48);
        }}
        className="min-h-0 flex-1 space-y-4 overflow-y-auto px-4 py-3"
      >
        {hasMore && (
          <div className="flex justify-center">
            <Button
              size="sm"
              variant="ghost"
              className="text-xs text-muted-foreground"
              onClick={() => earliest && setBeforeSeq(earliest)}
            >
              <Loader2 className="mr-1 size-3" aria-hidden />
              {t("chat.loadEarlier")}
            </Button>
          </div>
        )}
        {pages.length === 0 && (
          <p className="py-8 text-center text-xs text-muted-foreground">
            {t("chat.noMessages")}
          </p>
        )}
        {groups.map((group) => (
          <section key={group.day} className="space-y-3">
            <div className="flex items-center gap-2">
              <div className="h-px flex-1 bg-border" />
              <span className="text-[10px] uppercase tracking-wider text-muted-foreground">
                {group.day}
              </span>
              <div className="h-px flex-1 bg-border" />
            </div>
            {group.items.map((item) => (
              <TranscriptRow key={item.seq} item={item} />
            ))}
          </section>
        ))}
        {running && (
          <div className="flex items-center gap-2 pl-1 text-xs text-muted-foreground">
            <Loader2 className="size-3 animate-spin" aria-hidden />
            {t("chat.agentWorking")}
          </div>
        )}
      </div>
      {!atBottom && (
        <div className="pointer-events-none absolute inset-x-0 bottom-2 flex justify-center">
          <Button
            size="sm"
            variant="secondary"
            className="pointer-events-auto h-7 rounded-full px-3 text-xs shadow"
            onClick={() => {
              setAtBottom(true);
              const node = scrollRef.current;
              if (node) node.scrollTop = node.scrollHeight;
            }}
          >
            <ArrowDown className="mr-1 size-3" aria-hidden />
            {t("chat.backToBottom")}
          </Button>
        </div>
      )}
    </div>
  );
}

function TranscriptRow({ item }: { item: TranscriptItem }) {
  const { t } = useI18n();
  switch (item.kind) {
    case "human":
      return (
        <div className="flex justify-end">
          <div className="max-w-[85%] rounded-2xl rounded-br-sm bg-primary px-3 py-2 text-sm text-primary-foreground">
            <p className="whitespace-pre-wrap break-words">{item.text}</p>
            {item.status && item.status !== "injected" && (
              <p className="mt-1 text-[10px] opacity-70">状态：{item.status}</p>
            )}
          </div>
        </div>
      );
    case "assistant":
      return (
        <div className="flex gap-2">
          <div className="mt-0.5 grid size-6 shrink-0 place-items-center rounded-full bg-muted">
            <Bot className="size-3.5 text-muted-foreground" aria-hidden />
          </div>
          <div className="min-w-0 max-w-[85%] rounded-2xl rounded-bl-sm bg-muted/60 px-3 py-2 text-sm">
            <Markdown source={item.text ?? ""} />
          </div>
        </div>
      );
    case "tool_use":
      return (
        <Collapsible
          icon={<Terminal className="size-3.5 text-muted-foreground" aria-hidden />}
          title={item.tool || t("chat.toolCall")}
          hint={summarizeArgs(item.args)}
          body={item.args ? prettyJSON(item.args) : item.text}
        />
      );
    case "tool_result":
      return (
        <Collapsible
          icon={<Terminal className="size-3.5 text-muted-foreground" aria-hidden />}
          title={`${item.tool || t("chat.toolCall")} · ${t("chat.toolOutput")}`}
          hint={firstLine(item.output)}
          body={item.output}
          truncated={item.truncated}
          tone="muted"
        />
      );
    case "approval": {
      const approval = item.approval;
      const headline = approval
        ? `${t("chat.approvalRequest")} · ${approval.kind}`
        : item.text || t("chat.approvalRequest");
      const outcome = approval
        ? approval.decided
          ? [approval.status, approval.decided_by, approval.reason].filter(Boolean).join(" · ")
          : t("chat.awaitingDecision")
        : item.status;
      return (
        <div className="flex gap-2 rounded-lg border border-sky-500/40 bg-sky-500/5 px-3 py-2">
          <ShieldCheck className="mt-0.5 size-4 shrink-0 text-sky-600 dark:text-sky-400" aria-hidden />
          <div className="min-w-0 text-sm">
            <p className="break-words">{headline}</p>
            {outcome && (
              <p className="mt-0.5 font-mono text-[10px] text-muted-foreground">{outcome}</p>
            )}
          </div>
        </div>
      );
    }
    case "finding":
      return (
        <div className="flex gap-2 rounded-lg border border-amber-500/40 bg-amber-500/5 px-3 py-2">
          <AlertTriangle className="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" aria-hidden />
          <div className="min-w-0 text-sm">
            <p className="break-words">{item.text}</p>
            {item.status && (
              <p className="mt-0.5 font-mono text-[10px] text-muted-foreground">{item.status}</p>
            )}
          </div>
        </div>
      );
    case "system":
      return (
        <p className="text-center text-[11px] text-muted-foreground">{item.text}</p>
      );
    default:
      return null;
  }
}

function Collapsible({
  icon,
  title,
  hint,
  body,
  truncated,
  tone,
}: {
  icon: ReactNode;
  title: string;
  hint?: string;
  body?: string;
  truncated?: boolean;
  tone?: "muted";
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  return (
    <div
      className={cn(
        "rounded-lg border border-border text-xs",
        tone === "muted" ? "bg-muted/30" : "bg-card",
      )}
    >
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 px-3 py-2 text-left"
        aria-expanded={open}
      >
        <ChevronRight
          className={cn("size-3 shrink-0 text-muted-foreground transition-transform", open && "rotate-90")}
          aria-hidden
        />
        {icon}
        <span className="shrink-0 font-medium">{title}</span>
        {hint && <span className="min-w-0 truncate font-mono text-[10px] text-muted-foreground">{hint}</span>}
      </button>
      {open && body && (
        <pre className="max-h-80 overflow-auto border-t border-border px-3 py-2 font-mono text-[11px] leading-relaxed whitespace-pre-wrap break-words">
          {body}
          {truncated && <span className="text-muted-foreground">{"\n"}{t("chat.truncated")}</span>}
        </pre>
      )}
    </div>
  );
}

function summarizeArgs(args: unknown): string {
  if (!args) return "";
  if (typeof args === "string") return args.slice(0, 80);
  try {
    const text = JSON.stringify(args);
    return text.length > 80 ? `${text.slice(0, 80)}…` : text;
  } catch {
    return "";
  }
}

function prettyJSON(value: unknown): string {
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

function groupByDay(
  items: TranscriptItem[],
  t: (key: string) => string,
): { day: string; items: TranscriptItem[] }[] {
  const out: { day: string; items: TranscriptItem[] }[] = [];
  for (const item of items) {
    const d = new Date(item.at);
    const day = Number.isNaN(d.getTime())
      ? t("chat.unknownTime")
      : d.toLocaleDateString("zh-CN", { month: "numeric", day: "numeric" });
    const last = out[out.length - 1];
    if (last && last.day === day) last.items.push(item);
    else out.push({ day, items: [item] });
  }
  return out;
}
