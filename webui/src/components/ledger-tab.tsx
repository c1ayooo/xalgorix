import { useState } from "react";
import { Link } from "react-router-dom";
import { ChevronDown, ChevronRight, ExternalLink } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/states";
import { ErrorState } from "@/components/states";
import { ColdPathBadge } from "@/components/coldpath-badge";
import { AuditTimeline } from "@/components/audit-timeline";
import { NatureBadge } from "@/components/nature-badge";
import { useColdPathSync, useScanLedger } from "@/api/queries";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type {
  LedgerEvidence,
  LedgerHypothesis,
  LedgerHypothesisStatus,
} from "@/types/api";

/**
 * Run 账本投影（GET /api/scans/{id}/ledger，契约 §3.6）——hypothesis/evidence
 * 只读快照，即 4zreco 探索链路的 xalgorix 原生形态。状态权威 = ledger.json，
 * 这里只渲染；缺失/损坏的诚实标志照搬后端 note，不折叠为"干净空账本"。
 */

const STATUS_VARIANT: Record<
  LedgerHypothesisStatus,
  "default" | "outline" | "success" | "warning" | "muted"
> = {
  queued: "default",
  testing: "warning",
  proven: "success",
  rejected: "muted",
  blocked: "outline",
  exhausted: "muted",
};

const STATUS_ORDER: LedgerHypothesisStatus[] = [
  "testing",
  "queued",
  "blocked",
  "proven",
  "rejected",
  "exhausted",
];

function EvidenceRow({
  ev,
  projectId,
}: {
  ev: LedgerEvidence;
  projectId?: string;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const hasBody = Boolean(ev.request || ev.response);
  // Evidence is a projection of an InfoItem (CORE §3.1/§3.3); the stable id
  // already arrives from the ledger, so the link only needs the URL contract.
  const infoHref = ev.info_item_id
    ? `/info?tab=association&seed=${encodeURIComponent(ev.info_item_id)}${
        projectId ? `&project=${encodeURIComponent(projectId)}` : ""
      }`
    : null;
  return (
    <li className="border-t border-border first:border-t-0">
      <div className="flex items-center">
        <button
          type="button"
          onClick={() => hasBody && setOpen((v) => !v)}
          className={cn(
            "flex min-w-0 flex-1 items-center gap-2 px-3 py-1.5 text-left",
            hasBody ? "transition-colors hover:bg-accent/40" : "cursor-default",
          )}
          aria-expanded={hasBody ? open : undefined}
        >
        {hasBody ? (
          open ? (
            <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground" />
          ) : (
            <ChevronRight className="h-3 w-3 shrink-0 text-muted-foreground" />
          )
        ) : (
          <span className="h-3 w-3 shrink-0" />
        )}
        <NatureBadge nature={ev.nature} size="xs" />
        <span className="w-20 shrink-0 truncate font-mono text-[11px] text-muted-foreground">
          {ev.kind}
        </span>
        <span className="min-w-0 flex-1 truncate text-xs">{ev.summary}</span>
        {ev.source && (
          <span
            className="shrink-0 font-mono text-[10px] text-muted-foreground"
            title={t("ledger.engineSource")}
          >
            {ev.source}
          </span>
        )}
        {typeof ev.confidence === "number" && ev.confidence > 0 && (
          <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
            {ev.confidence.toFixed(2)}
          </span>
        )}
        </button>
        {infoHref && (
          <Link
            to={infoHref}
            className="mr-2 inline-flex shrink-0 items-center gap-0.5 rounded-sm px-1 text-[10px] text-muted-foreground transition-colors hover:bg-accent/40 hover:text-foreground"
            aria-label={t("ledger.openInfoItem")}
            title={`${t("ledger.openInfoItem")} · ${ev.info_item_id}`}
          >
            <ExternalLink className="size-3" aria-hidden />
          </Link>
        )}
      </div>
      {open && hasBody && (
        <div className="space-y-2 px-3 pb-2">
          {ev.request && (
            <pre className="max-h-48 overflow-auto rounded-sm border border-border bg-muted/40 p-2 font-mono text-[11px] leading-relaxed">
              {ev.request}
            </pre>
          )}
          {ev.response && (
            <pre className="max-h-48 overflow-auto rounded-sm border border-border bg-muted/40 p-2 font-mono text-[11px] leading-relaxed">
              {ev.response}
            </pre>
          )}
        </div>
      )}
    </li>
  );
}

function HypothesisRow({
  h,
  projectId,
}: {
  h: LedgerHypothesis;
  projectId?: string;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const evidence = h.evidence ?? [];
  const surface = [h.endpoint || h.target, h.parameter, h.role]
    .filter(Boolean)
    .join(" · ");
  return (
    <li className="border-b border-border last:border-b-0">
      <div className="flex flex-wrap items-center gap-2 px-3 py-2.5">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className="flex min-w-0 flex-1 items-center gap-2 text-left"
          aria-expanded={open}
        >
          {open ? (
            <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          )}
          <span className="w-12 shrink-0 truncate font-mono text-[11px] text-muted-foreground">
            {h.id}
          </span>
          <Badge variant={STATUS_VARIANT[h.status]} className="shrink-0">
            {t(`ledger.status.${h.status}`)}
          </Badge>
          <span className="min-w-0 flex-1 truncate text-sm font-medium">
            {h.title}
          </span>
        </button>
        <span className="flex shrink-0 items-center gap-3 font-mono text-[10px] tabular-nums text-muted-foreground">
          {h.voi_estimated && <span>VOI {h.voi?.toFixed(2)}</span>}
          <span>{t("ledger.confidence")} {h.confidence.toFixed(2)}</span>
          <span>
            {evidence.length} {t("ledger.evidence")}
          </span>
        </span>
      </div>
      {surface && (
        <p className="truncate px-3 pb-1.5 pl-[4.25rem] font-mono text-[11px] text-muted-foreground">
          {surface}
        </p>
      )}
      {open && (
        <div className="space-y-2 px-3 pb-3 pl-[4.25rem]">
          {h.next_action && (
            <p className="text-xs text-muted-foreground">
              <span className="font-medium text-foreground">
                {t("ledger.nextAction")}
              </span>{" "}
              {h.next_action}
            </p>
          )}
          <ul className="overflow-hidden rounded-sm border border-border">
            {evidence.map((ev) => (
              <EvidenceRow key={ev.id} ev={ev} projectId={projectId} />
            ))}
          </ul>
        </div>
      )}
    </li>
  );
}

export function LedgerTab({
  scanId,
  running,
  projectId,
}: {
  scanId?: string;
  running?: boolean;
  /** Owner project of the Run — used by the InfoItem deep link. */
  projectId?: string;
}) {
  const { t } = useI18n();
  const { data, isLoading, error, refetch } = useScanLedger(scanId, running);
  // 冷热分层:热路径 = 本视图(ledger.json 权威);冷路径 = PG 副本同步健康。
  // 端点失败静默降级——徽标不渲染,不阻塞账本。
  const coldpath = useColdPathSync(scanId ?? "", !!scanId);

  if (!scanId) {
    return (
      <EmptyState
        title={t("ledger.noRunTitle")}
        description={t("ledger.noRunDescription")}
      />
    );
  }
  if (error) {
    return (
      <ErrorState
        title={t("ledger.loadFailed")}
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
    return <Skeleton className="h-64 w-full" />;
  }
  if (!data) return null;

  const hypotheses = data.hypotheses ?? [];
  return (
    <div className="space-y-3">
      {/* 运行中标志 + 状态计数（六桶零填充，后端契约保证）+ 冷路径同步健康。 */}
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-[11px] text-muted-foreground">
          {data.scan_id}
        </span>
        {STATUS_ORDER.map((st) => (
          <Badge key={st} variant={STATUS_VARIANT[st]} className="gap-1">
            {t(`ledger.status.${st}`)}
            <span className="font-mono tabular-nums">
              {data.counts?.[st] ?? 0}
            </span>
          </Badge>
        ))}
        {coldpath.data && !coldpath.error && <ColdPathBadge state={coldpath.data} />}
      </div>

      {(data.ledger_damaged || data.incomplete) && (
        <p className="rounded-sm border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-600 dark:text-amber-400">
          {data.note || t("ledger.incomplete")}
        </p>
      )}

      {hypotheses.length === 0 ? (
        <EmptyState
          title={t("ledger.emptyTitle")}
          description={
            data.ledger_present
              ? t("ledger.emptyDescription")
              : t("ledger.absentDescription")
          }
        />
      ) : (
        <Card>
          <CardContent className="p-0">
            <ul>
              {hypotheses.map((h) => (
                <HypothesisRow key={h.id} h={h} projectId={projectId} />
              ))}
            </ul>
          </CardContent>
        </Card>
      )}

      {/* 黑板的历史面：领域审计投影（认领历史 + 时间线），只读、不参与判定。 */}
      <AuditTimeline scanId={scanId} running={running} />
    </div>
  );
}
