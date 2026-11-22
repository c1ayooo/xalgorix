import { useState } from "react";
import { ChevronDown, ChevronRight, History } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState, ErrorState } from "@/components/states";
import { useScanAudit } from "@/api/queries";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type {
  DomainAuditActor,
  DomainAuditOperation,
  DomainAuditRecord,
  DomainAuditState,
} from "@/types/api";

/**
 * 审计时间线（宪章 M2 只读投影，GET /api/scans/{id}/audit）——账本 tab 的
 * 历史面：黑板当前状态由 ledger 视图渲染，这里渲染"谁在何时对黑板做了什么"。
 * 投影不参与判定；缺口/乱序/重复/损坏必须如实显示（note 照搬后端），
 * timeline 客户端截断最近 N 条并诚实给出总数，完整正序历史以 journal 为权威。
 */

const TIMELINE_CAP = 50;

const OP_DOT: Record<string, string> = {
  // 假设生命周期
  seed_hypothesis: "bg-muted-foreground/60",
  record_hypothesis: "bg-muted-foreground/60",
  claim_hypothesis: "bg-amber-500",
  assign_hypothesis: "bg-amber-500",
  set_status: "bg-amber-500",
  merge_ledger: "bg-amber-500",
  reset_ledger: "bg-amber-500",
  // 证据与判定
  add_evidence: "bg-sky-500",
  record_attempt: "bg-sky-500",
  set_voi: "bg-sky-500",
  // 信息层投影
  info_item_enriched: "bg-violet-500",
  info_item_projected: "bg-violet-500",
  observe_traffic: "bg-violet-500",
  record_objective: "bg-emerald-500",
  finding_recorded: "bg-emerald-500",
  tool_observation: "bg-violet-500",
  // 集成调用审计
  skill_load: "bg-muted-foreground/60",
  agent_delegate: "bg-muted-foreground/60",
  mcp_call: "bg-muted-foreground/60",
};

function ActorChip({ actor }: { actor: DomainAuditActor }) {
  const { t } = useI18n();
  return (
    <span className="inline-flex shrink-0 items-center gap-1 rounded-full border border-border bg-muted/40 px-1.5 text-[10px] uppercase tracking-wide text-muted-foreground">
      {t(`audit.actor.${actor.kind}`)}
      {actor.id && (
        <span className="font-mono text-[10px] normal-case tracking-normal">
          {actor.id}
        </span>
      )}
    </span>
  );
}

function opDot(op: DomainAuditOperation): string {
  return OP_DOT[op] ?? "bg-muted-foreground/60";
}

function stateDelta(
  before: DomainAuditState | undefined,
  after: DomainAuditState | undefined,
): string {
  const parts: string[] = [];
  const push = (label: string, b?: number | string, a?: number | string) => {
    if (b === undefined && a === undefined) return;
    const fmt = (v: number | string | undefined) =>
      v === undefined ? "—" : typeof v === "number" ? v.toFixed(2) : v;
    if (b === a) {
      parts.push(`${label}=${fmt(a)}`);
    } else {
      parts.push(`${label} ${fmt(b)}→${fmt(a)}`);
    }
  };
  push("status", before?.status, after?.status);
  push("conf", before?.confidence, after?.confidence);
  push("voi", before?.voi, after?.voi);
  push("try", before?.attempts, after?.attempts);
  return parts.join(" · ");
}

function TimelineRow({ rec }: { rec: DomainAuditRecord }) {
  const { t } = useI18n();
  const delta = stateDelta(rec.before, rec.after);
  return (
    <li className="border-t border-border first:border-t-0">
      <div className="flex flex-wrap items-center gap-2 px-3 py-1.5">
        <span className="w-14 shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
          #{rec.seq}
        </span>
        <span
          aria-hidden
          className={cn("size-1.5 shrink-0 rounded-full", opDot(rec.operation))}
        />
        <span className="shrink-0 font-mono text-[11px]">
          {t(`audit.op.${rec.operation}`)}
        </span>
        {rec.hypothesis_id && (
          <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
            {rec.hypothesis_id}
          </span>
        )}
        <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
          {rec.summary}
        </span>
        <ActorChip actor={rec.actor} />
        <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
          {new Date(rec.at).toLocaleTimeString()}
        </span>
      </div>
      {delta && (
        <p className="truncate px-3 pb-1.5 pl-[4.75rem] font-mono text-[10px] tabular-nums text-muted-foreground">
          {delta}
        </p>
      )}
    </li>
  );
}

export function AuditTimeline({
  scanId,
  running,
}: {
  scanId?: string;
  running?: boolean;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const { data, isLoading, error, refetch } = useScanAudit(scanId ?? "", running);

  if (!scanId) return null;
  if (error) {
    return (
      <ErrorState
        title={t("audit.loadFailed")}
        description={error instanceof Error ? error.message : ""}
        action={
          <button
            type="button"
            onClick={() => refetch()}
            className="text-xs text-muted-foreground transition-colors hover:text-foreground"
          >
            {t("common.retry")}
          </button>
        }
      />
    );
  }
  if (isLoading && !data) {
    return <Skeleton className="h-32 w-full" />;
  }
  if (!data) return null;

  // 无 journal 的历史扫描 / 损坏尾巴：诚实显示，不折叠成"干净历史"。
  if (!data.journal_present || !data.projection) {
    return (
      <div className="space-y-2">
        <p className="rounded-sm border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-600 dark:text-amber-400">
          {data.note || t("audit.absent")}
        </p>
      </div>
    );
  }

  const proj = data.projection;
  const claims = proj.claims ?? [];
  // timeline 是 journal 正序（最旧在前）；渲染倒序（最新在上）。
  const all = proj.timeline ?? [];
  const records = [...all].reverse();
  const shown = open ? records : records.slice(0, TIMELINE_CAP);

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <History className="size-3.5 text-muted-foreground" aria-hidden />
        <span className="text-xs font-medium">{t("audit.title")}</span>
        <span className="font-mono text-[11px] tabular-nums text-muted-foreground">
          {t("audit.lastSeq")} #{proj.last_seq}
        </span>
        <Badge variant="outline" className="gap-1">
          {t("audit.claims")}
          <span className="font-mono tabular-nums">{claims.length}</span>
        </Badge>
        <Badge variant="outline" className="gap-1">
          {t("audit.records")}
          <span className="font-mono tabular-nums">{all.length}</span>
        </Badge>
      </div>

      {(data.journal_damaged || data.incomplete || proj.disordered) && (
        <p className="rounded-sm border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-600 dark:text-amber-400">
          {data.note || t("audit.incomplete")}
        </p>
      )}
      {(proj.gaps?.length ?? 0) > 0 && (
        <p className="rounded-sm border border-amber-500/30 bg-amber-500/10 px-3 py-2 font-mono text-xs tabular-nums text-amber-600 dark:text-amber-400">
          {t("audit.gaps")}{" "}
          {proj
            .gaps!.map((g) => `#${g.from}–#${g.to}`)
            .join(", ")}
        </p>
      )}
      {(proj.duplicate_seqs?.length ?? 0) > 0 && (
        <p className="rounded-sm border border-amber-500/30 bg-amber-500/10 px-3 py-2 font-mono text-xs tabular-nums text-amber-600 dark:text-amber-400">
          {t("audit.duplicates")} {proj.duplicate_seqs!.join(", ")}
        </p>
      )}

      {claims.length > 0 && (
        <Card>
          <CardContent className="p-0">
            <p className="border-b border-border px-3 py-2 text-[11px] uppercase tracking-wide text-muted-foreground">
              {t("audit.claimsTitle")}
            </p>
            <ul>
              {claims.map((c) => (
                <li
                  key={`${c.seq}:${c.hypothesis_id}`}
                  className="flex flex-wrap items-center gap-2 border-t border-border first:border-t-0 px-3 py-1.5"
                >
                  <span className="w-14 shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
                    #{c.seq}
                  </span>
                  <span className="shrink-0 font-mono text-[11px] text-amber-600 dark:text-amber-400">
                    {t("audit.claimedVerb")}
                  </span>
                  <span className="shrink-0 font-mono text-[11px]">
                    {c.hypothesis_id}
                  </span>
                  <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
                    {c.summary}
                  </span>
                  <ActorChip actor={c.actor} />
                  <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
                    {new Date(c.at).toLocaleTimeString()}
                  </span>
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardContent className="p-0">
          <p className="border-b border-border px-3 py-2 text-[11px] uppercase tracking-wide text-muted-foreground">
            {t("audit.timelineTitle")}
          </p>
          {shown.length === 0 ? (
            <p className="px-3 py-4 text-xs text-muted-foreground">
              {t("audit.empty")}
            </p>
          ) : (
            <ul>
              {shown.map((rec) => (
                <TimelineRow key={rec.seq} rec={rec} />
              ))}
            </ul>
          )}
          {records.length > TIMELINE_CAP && (
            <div className="border-t border-border px-3 py-2">
              <button
                type="button"
                onClick={() => setOpen((v) => !v)}
                className="flex items-center gap-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
                aria-expanded={open}
              >
                {open ? (
                  <ChevronDown className="size-3" />
                ) : (
                  <ChevronRight className="size-3" />
                )}
                {open
                  ? t("audit.showLess")
                  : `${t("audit.showMore")} ${records.length - TIMELINE_CAP}`}
                <span className="font-mono tabular-nums">
                  {t("audit.totalCount")} {records.length}
                </span>
              </button>
              {!open && (
                <span className="ml-2 text-[10px] text-muted-foreground">
                  {t("audit.capNote")}
                </span>
              )}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
