import { useMemo, useState } from "react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { ErrorState } from "@/components/states";
import { useTokenUsage } from "@/api/queries";
import { useI18n } from "@/i18n";
import type {
  TokenAgentBreakdown,
  TokenDiagnostics,
  TokenRequestPoint,
  TokenUsageResponse,
} from "@/types/token-usage";

const AGENT_LABELS: Record<string, string> = {
  "coordinator/root": "Root",
  "authz-logic": "Authz",
  "injection-serverside": "Injection",
  "client-source": "Client/source",
  verifier: "Verifier",
  other: "Other",
};

const AGENT_COLORS: Record<string, string> = {
  "coordinator/root": "#3b82f6",
  "authz-logic": "#f59e0b",
  "injection-serverside": "#ef4444",
  "client-source": "#8b5cf6",
  verifier: "#10b981",
  other: "#6b7280",
};

function agentLabel(t: string): string {
  return AGENT_LABELS[t] ?? t;
}

function fmtInt(n: number | null | undefined): string {
  if (n === null || n === undefined) return "—";
  if (Math.abs(n) >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (Math.abs(n) >= 1_000) return `${(n / 1_000).toFixed(n >= 100_000 ? 0 : 1)}K`;
  return `${n}`;
}

function fmtBytes(n: number | null | undefined): string {
  if (n === null || n === undefined) return "—";
  if (Math.abs(n) >= 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  if (Math.abs(n) >= 1024) return `${(n / 1024).toFixed(n >= 512 * 1024 ? 0 : 1)} KB`;
  return `${n} B`;
}

function fmtPct(n: number | null | undefined): string {
  if (n === null || n === undefined) return "—";
  return `${(n * 100).toFixed(1)}%`;
}

function fmtRatio(n: number): string {
  if (!n) return "—";
  return `${n.toFixed(1)}x`;
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-[11px] uppercase tracking-wide text-muted-foreground">{label}</span>
      <span className="font-mono text-sm font-semibold">{value}</span>
    </div>
  );
}

/** Prompt tokens per LLM request, one line per agent role, inline SVG. */
function PromptTokensChart({
  series,
  agentTypes,
  filter,
}: {
  series: TokenRequestPoint[];
  agentTypes: string[];
  filter: string;
}) {
  const W = 800;
  const H = 240;
  const PAD_L = 56;
  const PAD_B = 24;
  const PAD_T = 12;

  const shown = useMemo(
    () => (filter === "all" ? series : series.filter((p) => p.agent_type === filter)),
    [series, filter],
  );

  const maxPrompt = useMemo(
    () => Math.max(1, ...shown.map((p) => p.prompt)),
    [shown],
  );
  const maxSeq = useMemo(() => Math.max(1, series.length), [series.length]);

  const x = (seq: number) => PAD_L + ((seq - 1) / Math.max(1, maxSeq - 1)) * (W - PAD_L - 8);
  const y = (prompt: number) => PAD_T + (1 - prompt / maxPrompt) * (H - PAD_T - PAD_B);

  const lines = useMemo(() => {
    const byAgent = new Map<string, TokenRequestPoint[]>();
    for (const p of shown) {
      const arr = byAgent.get(p.agent_type) ?? [];
      arr.push(p);
      byAgent.set(p.agent_type, arr);
    }
    return [...byAgent.entries()].map(([agentType, pts]) => {
      const sorted = [...pts].sort((a, b) => a.seq - b.seq);
      const d = sorted
        .map((p, i) => `${i === 0 ? "M" : "L"}${x(p.seq).toFixed(1)},${y(p.prompt).toFixed(1)}`)
        .join(" ");
      return { agentType, d, pts: sorted };
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shown, maxPrompt, maxSeq]);

  const yTicks = [0, 0.5, 1].map((f) => f * maxPrompt);

  if (shown.length === 0) {
    return <div className="py-10 text-center text-sm text-muted-foreground">—</div>;
  }

  return (
    <div className="w-full overflow-x-auto">
      <svg viewBox={`0 0 ${W} ${H}`} className="h-56 w-full min-w-[480px]">
        {yTicks.map((v) => (
          <g key={v}>
            <line x1={PAD_L} x2={W - 8} y1={y(v)} y2={y(v)} stroke="currentColor" strokeOpacity={0.12} />
            <text x={PAD_L - 6} y={y(v) + 3} textAnchor="end" fontSize={10} fill="currentColor" fillOpacity={0.55}>
              {fmtInt(v)}
            </text>
          </g>
        ))}
        {lines.map(({ agentType, d }) => (
          <path
            key={agentType}
            d={d}
            fill="none"
            stroke={AGENT_COLORS[agentType] ?? "#6b7280"}
            strokeWidth={1.6}
            strokeLinejoin="round"
            strokeLinecap="round"
          />
        ))}
      </svg>
      <div className="mt-1 flex flex-wrap gap-2 text-[11px] text-muted-foreground">
        {agentTypes.map((at) => (
          <span key={at} className="inline-flex items-center gap-1">
            <span
              className="inline-block h-2 w-2 rounded-full"
              style={{ background: AGENT_COLORS[at] ?? "#6b7280" }}
            />
            {agentLabel(at)}
          </span>
        ))}
      </div>
    </div>
  );
}

function AgentRow({ a }: { a: TokenAgentBreakdown }) {
  return (
    <tr className="border-b border-border/60 last:border-0">
      <td className="py-1.5 pr-2 font-medium">{agentLabel(a.agent_type)}</td>
      <td className="py-1.5 pr-2 font-mono text-xs">{a.requests}</td>
      <td className="py-1.5 pr-2 font-mono text-xs">{fmtInt(a.prompt_tokens)}</td>
      <td className="py-1.5 pr-2 font-mono text-xs">{fmtInt(a.completion_tokens)}</td>
      <td className="py-1.5 pr-2 font-mono text-xs">{fmtInt(a.cached_input_tokens)}</td>
      <td className="py-1.5 pr-2 font-mono text-xs">{fmtInt(a.uncached_input_tokens)}</td>
      <td className="py-1.5 pr-2 font-mono text-xs">{fmtInt(a.average_prompt_tokens)}</td>
      <td className="py-1.5 pr-2 font-mono text-xs">{fmtInt(a.max_prompt_tokens)}</td>
      <td className="py-1.5 pr-2 font-mono text-xs">{fmtBytes(a.tool_result_bytes)}</td>
      <td className="py-1.5 pr-2 font-mono text-xs">{fmtBytes(a.skill_result_bytes)}</td>
      <td className="py-1.5 font-mono text-xs">{a.iterations}</td>
    </tr>
  );
}

const TH = "px-0 pr-2 pb-1 text-left text-[10px] font-semibold uppercase tracking-wide text-muted-foreground";

function AgentFilter({
  agentTypes,
  value,
  onChange,
}: {
  agentTypes: string[];
  value: string;
  onChange: (v: string) => void;
}) {
  const { t } = useI18n();
  const options = ["all", ...agentTypes];
  return (
    <div className="flex flex-wrap gap-1.5">
      {options.map((opt) => (
        <button
          key={opt}
          type="button"
          onClick={() => onChange(opt)}
          className={`rounded-md border px-2 py-0.5 text-[11px] transition-colors ${
            value === opt
              ? "border-primary bg-primary text-primary-foreground"
              : "border-border text-muted-foreground hover:border-primary/40"
          }`}
        >
          {opt === "all" ? t("token.all") : agentLabel(opt)}
        </button>
      ))}
    </div>
  );
}

function SourceBars({
  label,
  system,
  assistant,
  tool,
  skill,
  userOther,
}: {
  label: string;
  system: number;
  assistant: number;
  tool: number;
  skill: number;
  userOther: number;
}) {
  const { t } = useI18n();
  const total = Math.max(1, system + assistant + tool + userOther);
  const seg = (v: number) => `${((v / total) * 100).toFixed(2)}%`;
  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between text-xs">
        <span className="font-medium">{label}</span>
        <span className="font-mono text-muted-foreground">{fmtBytes(total)}</span>
      </div>
      <div className="flex h-2.5 w-full overflow-hidden rounded-full bg-muted">
        <div style={{ width: seg(system), background: "#60a5fa" }} title={`${t("token.src.system")} ${fmtBytes(system)}`} />
        <div style={{ width: seg(assistant), background: "#a78bfa" }} title={`${t("token.src.assistant")} ${fmtBytes(assistant)}`} />
        <div style={{ width: seg(tool), background: "#f87171" }} title={`${t("token.src.tool")} ${fmtBytes(tool)}`} />
        <div style={{ width: seg(userOther), background: "#9ca3af" }} title={`${t("token.src.otherUser")} ${fmtBytes(userOther)}`} />
      </div>
      <div className="flex flex-wrap gap-x-3 gap-y-0.5 text-[10px] text-muted-foreground">
        <span>{t("token.src.system")} {fmtBytes(system)}</span>
        <span>{t("token.src.assistantHistory")} {fmtBytes(assistant)}</span>
        <span>{t("token.src.tool")} {fmtBytes(tool)}</span>
        <span>{t("token.src.skill")} {fmtBytes(skill)}</span>
        <span>{t("token.src.otherUserHistory")} {fmtBytes(userOther)}</span>
      </div>
    </div>
  );
}

export function TokenUsageTab({ scanId, running }: { scanId: string; running: boolean }) {
  const { t } = useI18n();
  const { data, isLoading, error } = useTokenUsage(scanId, running);
  const [filter, setFilter] = useState("all");

  if (error) {
    return <ErrorState title={t("scanDetail.token.unavailable")} />;
  }
  if (isLoading || !data) {
    return (
      <Card>
        <CardContent className="py-10 text-center text-sm text-muted-foreground">
          {t("scanDetail.token.loading")}
        </CardContent>
      </Card>
    );
  }

  return <TokenUsageBody resp={data} filter={filter} setFilter={setFilter} />;
}

function TokenUsageBody({
  resp,
  filter,
  setFilter,
}: {
  resp: TokenUsageResponse;
  filter: string;
  setFilter: (v: string) => void;
}) {
  const { t } = useI18n();
  const d: TokenDiagnostics = resp.diagnostics;
  const agentTypes = useMemo(
    () => resp.series.map((p) => p.agent_type).filter((v, i, arr) => arr.indexOf(v) === i),
    [resp.series],
  );
  const rec = d.recovery;
  const recoveryTokens = rec.retry_tokens + rec.no_tool_tokens + rec.malformed_tool_tokens + rec.finish_rejection_tokens;

  return (
    <div className="space-y-3">
      {/* TOTAL / REQUESTS */}
      <div className="grid gap-3 md:grid-cols-2">
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm">{t("scanDetail.token.total")}</CardTitle>
          </CardHeader>
          <CardContent className="grid grid-cols-3 gap-3">
            <Metric label={t("token.total")} value={fmtInt(d.total_tokens)} />
            <Metric label={t("token.prompt")} value={fmtInt(d.prompt_tokens)} />
            <Metric label={t("token.output")} value={fmtInt(d.completion_tokens)} />
            <Metric label={t("token.cached")} value={d.cached_input_tokens === null ? t("token.unknown") : fmtInt(d.cached_input_tokens)} />
            <Metric label={t("token.uncached")} value={d.uncached_input_tokens === null ? t("token.unknown") : fmtInt(d.uncached_input_tokens)} />
            <Metric label={t("token.cacheHit")} value={fmtPct(d.cache_hit_rate)} />
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm">{t("scanDetail.token.requests")}</CardTitle>
          </CardHeader>
          <CardContent className="grid grid-cols-3 gap-3">
            <Metric label={t("token.llmRequests")} value={`${d.total_llm_requests}`} />
            <Metric label={t("token.avgPrompt")} value={fmtInt(d.average_prompt_tokens_per_request)} />
            <Metric label={t("token.maxPrompt")} value={fmtInt(d.largest_prompt_tokens)} />
            <Metric label={t("token.cacheReported")} value={`${d.cache_reported_requests}/${d.total_llm_requests}`} />
            <Metric label={t("token.largestConv")} value={fmtBytes(d.largest_conversation_bytes)} />
            <Metric label={t("token.outputAvg")} value={fmtInt(d.average_completion_tokens_per_request)} />
          </CardContent>
        </Card>
      </div>

      {/* CONTEXT GROWTH + CHART */}
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm">{t("scanDetail.token.context")}</CardTitle>
          <CardDescription className="text-xs">
            {t("scanDetail.token.contextHint")}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="grid grid-cols-2 gap-3 md:grid-cols-6">
            <Metric label={t("token.firstPrompt")} value={fmtInt(d.growth.first_prompt_tokens)} />
            <Metric label={t("token.lastPrompt")} value={fmtInt(d.growth.last_prompt_tokens)} />
            <Metric label={t("token.growth")} value={fmtRatio(d.growth.prompt_growth_ratio)} />
            <Metric label={t("token.median")} value={fmtInt(d.growth.median_prompt_tokens)} />
            <Metric label="P95" value={fmtInt(d.growth.p95_prompt_tokens)} />
            <Metric label={t("token.cumulative")} value={fmtInt(d.growth.cumulative_prompt_tokens)} />
          </div>
          <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
            <Metric label={t("token.toolBytes")} value={fmtBytes(d.tool_result_bytes)} />
            <Metric label={t("token.skillBytes")} value={fmtBytes(d.skill_result_bytes)} />
            <Metric label={t("token.contextSent")} value={fmtBytes(d.context.cumulative_context_bytes_sent)} />
            <Metric label={t("token.resendAmp")} value={fmtRatio(d.context.resend_amplification_ratio)} />
          </div>
          <AgentFilter agentTypes={agentTypes} value={filter} onChange={setFilter} />
          <PromptTokensChart series={resp.series} agentTypes={agentTypes} filter={filter} />
        </CardContent>
      </Card>

      {/* AGENTS */}
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-sm">{t("scanDetail.token.agents")}</CardTitle>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          <table className="w-full min-w-[720px] text-xs">
            <thead>
              <tr>
                <th className={TH}>{t("token.agent")}</th>
                <th className={TH}>{t("token.reqs")}</th>
                <th className={TH}>{t("token.prompt")}</th>
                <th className={TH}>{t("token.output")}</th>
                <th className={TH}>{t("token.cached")}</th>
                <th className={TH}>{t("token.uncached")}</th>
                <th className={TH}>{t("token.avgPrompt")}</th>
                <th className={TH}>{t("token.maxPrompt")}</th>
                <th className={TH}>{t("token.toolBytesShort")}</th>
                <th className={TH}>{t("token.skillBytesShort")}</th>
                <th className={TH}>{t("token.iters")}</th>
              </tr>
            </thead>
            <tbody>
              {d.agents.map((a) => (
                <AgentRow key={a.agent_type} a={a} />
              ))}
              {d.agents.length === 0 && (
                <tr>
                  <td colSpan={11} className="py-6 text-center text-muted-foreground">
                    —
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </CardContent>
      </Card>

      {/* SOURCES + RECOVERY */}
      <div className="grid gap-3 md:grid-cols-2">
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm">{t("scanDetail.token.sources")}</CardTitle>
            <CardDescription className="text-xs">
              {t("scanDetail.token.sourcesHint")}
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            {d.sources.latest.map((c) => (
              <SourceBars
                key={c.agent_id}
                label={`${agentLabel(c.agent_type)} (${t("token.iterWord")} ${c.iteration}, ${c.message_count} ${t("token.msgsWord")})`}
                system={c.system_bytes}
                assistant={c.assistant_bytes}
                tool={c.tool_result_bytes}
                skill={c.skill_result_bytes}
                userOther={c.user_other_bytes}
              />
            ))}
            {d.sources.latest.length === 0 && (
              <div className="py-6 text-center text-sm text-muted-foreground">—</div>
            )}
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm">{t("scanDetail.token.recovery")}</CardTitle>
          </CardHeader>
          <CardContent className="grid grid-cols-2 gap-3">
            <Metric label={t("token.retries")} value={`${rec.retry_requests} / ${fmtInt(rec.retry_tokens)}`} />
            <Metric label={t("token.noToolRecovery")} value={`${rec.no_tool_requests} / ${fmtInt(rec.no_tool_tokens)}`} />
            <Metric label={t("token.malformedRecovery")} value={`${rec.malformed_tool_requests} / ${fmtInt(rec.malformed_tool_tokens)}`} />
            <Metric label={t("token.finishRejection")} value={`${rec.finish_rejection_requests} / ${fmtInt(rec.finish_rejection_tokens)}`} />
            <Metric label={t("token.recoveryTokens")} value={fmtInt(recoveryTokens)} />
            <Metric label={t("token.recoveryShare")} value={d.total_tokens > 0 ? fmtPct(recoveryTokens / d.total_tokens) : "—"} />
          </CardContent>
          <CardContent className="flex flex-wrap gap-1.5 pt-0">
            {Object.entries(d.category_tokens).map(([cat, tok]) => (
              <Badge key={cat} variant="outline" className="font-mono text-[10px]">
                {cat}: {fmtInt(tok)}
              </Badge>
            ))}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
