import { useEffect, useMemo, useRef, useState } from "react";
import {
  Brain,
  Check,
  ChevronRight,
  Globe,
  ListChecks,
  Loader2,
  Terminal,
  Wrench,
  X,
} from "lucide-react";
import { SeverityBadge } from "@/components/severity-badge";
import { ApprovalInlineBar } from "@/components/approval-inline-bar";
import { Markdown } from "@/components/markdown";
import { EmptyState } from "@/components/states";
import { cn } from "@/lib/utils";
import { useI18n } from "@/i18n";
import {
  buildTrace,
  planEventLabel,
  toolRunning,
  toolSummary,
  type TraceItem,
} from "@/lib/execution-trace";
import type { FeedEvent, StreamingMessage } from "@/store/events";

function formatTime(ms: number): string {
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleTimeString(undefined, { hour12: false });
}

function toolIcon(name?: string) {
  if (name === "terminal_execute" || name === "python_action") return Terminal;
  if (name === "send_request" || name === "http_request") return Globe;
  return Wrench;
}

function roundLabel(label: string, iteration: string, phase: string): string {
  const iter = /^Iteration\s+(\d+)(?:\/(\d+))?$/i.exec(label.trim());
  if (iter) {
    return `${iteration} ${iter[1]}${iter[2] ? `/${iter[2]}` : ""}`;
  }
  const ph = /^phase\s+(\d+)$/i.exec(label.trim());
  if (ph) return `${phase} ${ph[1]}`;
  return label;
}

/** Localize "taskId · engineStatus" from an update_plan call. */
function planDetailLabel(detail: string, t: (key: string) => string): string {
  const idx = detail.lastIndexOf(" · ");
  if (idx < 0) return detail;
  const id = detail.slice(0, idx);
  const status = detail.slice(idx + 3).trim();
  const key = `plan.status.${status}`;
  const label = t(key);
  return `${id} · ${label === key ? status : label}`;
}

const NOTICE_TONE: Record<string, string> = {
  error: "text-red-600 dark:text-red-400",
  vuln: "text-orange-600 dark:text-orange-400",
  vuln_found: "text-orange-600 dark:text-orange-400",
  vulns: "text-orange-600 dark:text-orange-400",
  approval_decided: "text-emerald-600 dark:text-emerald-400",
  decision: "text-violet-600 dark:text-violet-300",
  report_ready: "text-emerald-600 dark:text-emerald-400",
  target_started: "text-emerald-600 dark:text-emerald-400",
  target_completed: "text-emerald-600 dark:text-emerald-400",
  queue_started: "text-emerald-600 dark:text-emerald-400",
  queue_finished: "text-emerald-600 dark:text-emerald-400",
  stopped: "text-red-600 dark:text-red-300",
  paused: "text-amber-700 dark:text-amber-300",
  resumed: "text-emerald-600 dark:text-emerald-300",
  guidance_consumed: "text-emerald-600 dark:text-emerald-400",
  recovery: "text-sky-600 dark:text-sky-300",
};

function ToolStep({
  item,
  showAgent,
  agentLabel,
  active,
}: {
  item: Extract<TraceItem, { kind: "tool" }>;
  showAgent: boolean;
  agentLabel: (e: FeedEvent) => string;
  active: boolean;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const use = item.use;
  const result = item.result;
  const toolName = use?.tool_name || result?.tool_name || "tool";
  const Icon = toolIcon(toolName);
  const running = toolRunning(item, active);
  const awaiting = !active && !result;
  const failed = !!result && (!!result.error || result.type === "tool_error");
  const summary = toolSummary(use ?? result);
  const args = use?.tool_args ?? {};
  const output = result?.output;
  const hasDetail =
    Object.keys(args).length > 0 || !!output || !!result?.error || !!use?.content;

  return (
    <div className="border-b border-border/50 last:border-0">
      <button
        type="button"
        onClick={() => hasDetail && setOpen((v) => !v)}
        aria-expanded={hasDetail ? open : undefined}
        className={cn(
          "flex w-full items-start gap-2 px-3 py-1.5 text-left transition-colors",
          hasDetail && "hover:bg-accent/40",
        )}
      >
        <ChevronRight
          className={cn(
            "mt-0.5 size-3 shrink-0 text-muted-foreground transition-transform",
            open && "rotate-90",
            !hasDetail && "opacity-0",
          )}
          aria-hidden
        />
        <Icon
          className={cn(
            "mt-0.5 size-3.5 shrink-0",
            running
              ? "text-sky-600 dark:text-sky-400"
              : failed
                ? "text-red-600 dark:text-red-400"
                : "text-muted-foreground",
          )}
          aria-hidden
        />
        {showAgent && (
          <span className="mono mt-0.5 max-w-24 shrink-0 truncate rounded-sm bg-muted px-1 text-[10px] text-muted-foreground">
            {agentLabel(use ?? result!)}
          </span>
        )}
        <span className="mt-0.5 shrink-0 font-mono text-[11px] font-medium text-sky-700 dark:text-sky-300">
          {toolName}
        </span>
        {summary && (
          <span className="mono mt-0.5 min-w-0 flex-1 truncate text-[11px] text-foreground/80">
            {summary.replace(/\s+/g, " ")}
          </span>
        )}
        {!summary && <span className="flex-1" />}
        <span
          className={cn(
            "mt-0.5 inline-flex shrink-0 items-center gap-1 text-[10px] font-medium",
            running
              ? "text-sky-600 dark:text-sky-400"
              : failed
                ? "text-red-600 dark:text-red-400"
                : awaiting
                  ? "text-muted-foreground"
                  : "text-emerald-600 dark:text-emerald-400",
          )}
        >
          {running ? (
            <>
              <Loader2 className="size-3 animate-spin" aria-hidden />
              {t("trace.running")}
            </>
          ) : failed ? (
            <>
              <X className="size-3" aria-hidden />
              {t("trace.failed")}
            </>
          ) : awaiting ? (
            <span>{t("trace.noResult")}</span>
          ) : (
            <>
              <Check className="size-3" aria-hidden />
              <span className="sr-only">{t("trace.done")}</span>
            </>
          )}
        </span>
        <span className="mt-0.5 hidden w-16 shrink-0 text-right text-[10px] text-muted-foreground mono tabular-nums sm:block">
          {formatTime(item.at)}
        </span>
      </button>
      {open && hasDetail && (
        <div className="space-y-1.5 px-3 pb-2 pl-9">
          {Object.keys(args).length > 0 && (
            <div>
              <div className="mb-0.5 text-[10px] uppercase tracking-wider text-muted-foreground">
                {t("trace.args")}
              </div>
              <pre className="max-h-56 overflow-auto whitespace-pre-wrap break-words rounded-sm border border-border bg-muted/40 p-2 font-mono text-[11px] leading-5">
                {JSON.stringify(args, null, 2)}
              </pre>
            </div>
          )}
          {use?.content && Object.keys(args).length === 0 && (
            <pre className="whitespace-pre-wrap break-words rounded-sm border border-border bg-muted/40 p-2 font-mono text-[11px] leading-5">
              {use.content}
            </pre>
          )}
          {result?.error && (
            <div>
              <div className="mb-0.5 text-[10px] uppercase tracking-wider text-red-600 dark:text-red-400">
                {t("trace.error")}
              </div>
              <pre className="max-h-56 overflow-auto whitespace-pre-wrap break-words rounded-sm border border-red-500/30 bg-red-500/5 p-2 font-mono text-[11px] leading-5 text-red-700 dark:text-red-300">
                {result.error}
              </pre>
            </div>
          )}
          {output && (
            <div>
              <div className="mb-0.5 text-[10px] uppercase tracking-wider text-muted-foreground">
                {t("trace.output")}
              </div>
              <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-sm border border-border bg-muted/40 p-2 font-mono text-[11px] leading-5">
                {output}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

/**
 * ExecutionTrace — the transcript half of the execution visualisation.
 *
 * Same data as the raw event feed, but grouped the way the agent worked:
 * round separators for thinking/phase, one row per tool call (args + result
 * bound together, expandable), plan revisions, assistant replies, human
 * intents and inline approvals. Approval and guidance rendering reuse the
 * existing domain components so the semantics stay identical to the feed.
 */
export function ExecutionTrace({
  events,
  streaming = [],
  instanceId,
  active = false,
  emptyTitle,
  emptyDescription,
  className,
}: {
  events: FeedEvent[];
  streaming?: StreamingMessage[];
  instanceId?: string;
  /** Run is currently executing — drives the live footer indicator. */
  active?: boolean;
  emptyTitle?: string;
  emptyDescription?: string;
  className?: string;
}) {
  const { t } = useI18n();
  const items = useMemo(() => buildTrace(events), [events]);
  const scrollRef = useRef<HTMLDivElement>(null);
  const stickToBottom = useRef(true);

  const agents = useMemo(() => {
    const set = new Set<string>();
    for (const e of events) {
      if (e.agent_id) set.add(e.agent_id);
    }
    return set;
  }, [events]);
  const showAgent = agents.size > 1;

  const agentLabel = (e: FeedEvent) => {
    const id = e.agent_id ?? "";
    if (instanceId && id === instanceId) return t("console.coordinator");
    return id.replace(`${instanceId}/`, "").replace(/^.*\//, "") || "agent";
  };

  const hasRunning = items.some((i) => i.kind === "tool" && toolRunning(i, active));
  const live = active && (hasRunning || streaming.length > 0);

  useEffect(() => {
    const el = scrollRef.current;
    if (el && stickToBottom.current) {
      el.scrollTop = el.scrollHeight;
    }
  }, [items.length, streaming.length]);

  if (items.length === 0 && streaming.length === 0) {
    return (
      <EmptyState
        title={emptyTitle ?? t("trace.emptyTitle")}
        description={emptyDescription ?? t("trace.emptyDescription")}
        className="m-3"
      />
    );
  }

  return (
    <div
      ref={scrollRef}
      onScroll={(e) => {
        const el = e.currentTarget;
        stickToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
      }}
      className={cn("min-h-0 min-w-0 flex-1 overflow-auto bg-background/40", className)}
    >
      {items.map((item) => {
        if (item.kind === "round") {
          return (
            <div
              key={item.key}
              className="my-2 flex items-center gap-2 px-3 text-[10px] font-medium tracking-wide text-muted-foreground"
            >
              <span className="h-px flex-1 bg-border" aria-hidden />
              <span className="mono min-w-0 max-w-[75%] truncate">
                {roundLabel(item.label, t("trace.iteration"), t("trace.phase"))}
              </span>
              <span className="h-px flex-1 bg-border" aria-hidden />
            </div>
          );
        }

        if (item.kind === "thought") {
          return (
            <div
              key={item.key}
              className="flex items-start gap-2 px-3 py-1 text-[11px] text-muted-foreground"
            >
              <Brain
                className="mt-0.5 size-3 shrink-0 text-violet-600 dark:text-violet-300"
                aria-hidden
              />
              <span className="min-w-0 flex-1 line-clamp-2" title={item.text}>
                {item.text}
              </span>
              <span className="mt-0.5 hidden shrink-0 text-[10px] tabular-nums mono sm:block">
                {formatTime(item.at)}
              </span>
            </div>
          );
        }

        if (item.kind === "tool") {
          return (
            <ToolStep
              key={item.key}
              item={item}
              showAgent={showAgent}
              agentLabel={agentLabel}
              active={active}
            />
          );
        }

        if (item.kind === "plan") {
          const built = item.event.tool_name === "build_plan";
          const detail = planEventLabel(item.event);
          return (
            <div
              key={item.key}
              className="my-1.5 flex items-start gap-2 rounded-sm border border-violet-500/30 bg-violet-500/10 px-2.5 py-1.5 text-xs"
            >
              <ListChecks
                className="mt-0.5 size-3.5 shrink-0 text-violet-600 dark:text-violet-300"
                aria-hidden
              />
              <span className="shrink-0 font-medium text-violet-700 dark:text-violet-300">
                {t(built ? "trace.planBuilt" : "trace.planUpdated")}
              </span>
              {detail && (
                <span className="mono min-w-0 flex-1 truncate text-muted-foreground">
                  {built
                    ? `${detail} ${t("trace.planTaskCount")}`
                    : planDetailLabel(detail, t)}
                </span>
              )}
              <span className="ml-auto hidden shrink-0 text-[10px] text-muted-foreground mono tabular-nums sm:block">
                {formatTime(item.at)}
              </span>
            </div>
          );
        }

        if (item.kind === "answer") {
          return (
            <div key={item.key} className="px-3 pb-2">
              <div className="max-w-[85%] rounded-lg rounded-tl-sm border border-border bg-card px-3 py-2 text-sm [overflow-wrap:anywhere]">
                <div className="mb-1 flex items-center gap-2 text-[10px] uppercase tracking-wider text-muted-foreground">
                  {item.event.agent_id && (
                    <span className="mono">{agentLabel(item.event)}</span>
                  )}
                  <span className="mono tabular-nums">{formatTime(item.at)}</span>
                </div>
                <div className="text-foreground">
                  <Markdown source={item.event.content} />
                </div>
              </div>
            </div>
          );
        }

        if (item.kind === "human") {
          const g = item.event.guidance;
          const queued = item.event.type === "guidance_queued";
          return (
            <div key={item.key} className="flex justify-end px-3 pb-2">
              <div
                className={cn(
                  "max-w-[75%] rounded-lg rounded-tr-sm px-3 py-1.5 text-sm [overflow-wrap:anywhere]",
                  queued
                    ? "border border-violet-500/40 bg-violet-500/10 text-foreground"
                    : "bg-primary text-primary-foreground",
                )}
              >
                {g?.message}
                <div
                  className={cn(
                    "mt-1 flex items-center gap-1.5 text-[10px] uppercase tracking-wider",
                    queued ? "text-muted-foreground" : "opacity-80",
                  )}
                >
                  {t(queued ? "guidance.status.queued" : "guidance.status.injected")}
                  {g?.run_id && <span className="mono">{g.run_id}</span>}
                </div>
              </div>
            </div>
          );
        }

        if (item.kind === "approval") {
          return (
            <div key={item.key} className="px-3 pb-2">
              <div className="mb-1 text-[10px] uppercase tracking-wider text-muted-foreground">
                {t("trace.approvalRequested")}{" "}
                <span className="mono tabular-nums">{formatTime(item.at)}</span>
              </div>
              {item.event.approval && (
                <ApprovalInlineBar approval={item.event.approval} />
              )}
            </div>
          );
        }

        // notice
        const e = item.event;
        const tone = NOTICE_TONE[e.type] ?? "text-muted-foreground";
        const message =
          e.content || e.error || e.output || e.tool_name || e.type;
        return (
          <div
            key={item.key}
            className="flex items-start gap-2 border-b border-border/40 px-3 py-1.5 text-xs last:border-0"
          >
            <span className="mt-0.5 w-16 shrink-0 text-[10px] text-muted-foreground tabular-nums mono">
              {formatTime(item.at)}
            </span>
            <span
              className={cn(
                "mt-0.5 w-24 shrink-0 truncate text-[10px] font-medium uppercase tracking-wide mono",
                tone,
              )}
            >
              {e.type || "event"}
            </span>
            <span className="min-w-0 flex-1">
              {e.vulns && e.vulns.length > 0 ? (
                <span className="flex flex-wrap items-center gap-1.5">
                  {e.vulns.slice(0, 3).map((v) => (
                    <span key={v.id} className="flex min-w-0 items-center gap-1.5">
                      <SeverityBadge severity={v.severity} />
                      <span className="truncate">{v.title}</span>
                    </span>
                  ))}
                  {e.vulns.length > 3 && (
                    <span className="text-muted-foreground">
                      +{e.vulns.length - 3}
                    </span>
                  )}
                </span>
              ) : (
                <span className="line-clamp-2 break-words">{message}</span>
              )}
            </span>
          </div>
        );
      })}

      {streaming.map((m) => (
        <div key={m.id} className="px-3 pb-2">
          <div className="max-w-[85%] rounded-lg rounded-tl-sm border border-ring/40 bg-card px-3 py-2 text-sm [overflow-wrap:anywhere]">
            <div className="mb-1 flex items-center gap-2 text-[10px] uppercase tracking-wider text-muted-foreground">
              {m.agent_id && <span className="mono">{m.agent_id}</span>}
              <span className="text-emerald-600 dark:text-emerald-400">
                {t("trace.streaming")}
              </span>
            </div>
            <div className="whitespace-pre-wrap text-foreground">
              {m.text}
              <span className="ml-0.5 inline-block animate-pulse text-primary">▌</span>
            </div>
          </div>
        </div>
      ))}

      {live && (
        <div className="flex items-center gap-2 px-3 py-2 pl-9 text-[10px] text-muted-foreground">
          <span className="size-1.5 animate-pulse rounded-full bg-sky-500" aria-hidden />
          {t("trace.streaming")}
        </div>
      )}
    </div>
  );
}

/** Small ghost button used by the console to switch between trace and raw. */
export function TraceViewToggle({
  view,
  onChange,
}: {
  view: "trace" | "raw";
  onChange: (v: "trace" | "raw") => void;
}) {
  const { t } = useI18n();
  return (
    <div
      role="tablist"
      aria-label={t("trace.viewLabel")}
      className="flex shrink-0 rounded-md border border-border p-0.5"
    >
      {(["trace", "raw"] as const).map((v) => (
        <button
          key={v}
          type="button"
          role="tab"
          aria-selected={view === v}
          onClick={() => onChange(v)}
          className={cn(
            "rounded-sm px-2 py-0.5 text-[11px] transition-colors",
            view === v
              ? "bg-accent text-accent-foreground"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          {t(v === "trace" ? "trace.viewTrace" : "trace.viewRaw")}
        </button>
      ))}
    </div>
  );
}
