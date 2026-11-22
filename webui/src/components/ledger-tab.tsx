import { useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/states";
import { ErrorState } from "@/components/states";
import { useScanLedger } from "@/api/queries";
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

function EvidenceRow({ ev }: { ev: LedgerEvidence }) {
  const { t } = useI18n();
  const observed = ev.nature === "observed";
  const [open, setOpen] = useState(false);
  const hasBody = Boolean(ev.request || ev.response);
  return (
    <li className="border-t border-border first:border-t-0">
      <button
        type="button"
        onClick={() => hasBody && setOpen((v) => !v)}
        className={cn(
          "flex w-full items-center gap-2 px-3 py-1.5 text-left",
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
        <span
          className={cn(
            "inline-flex shrink-0 items-center gap-1 rounded-full border px-1.5 text-[10px] uppercase tracking-wide",
            observed
              ? "border-sky-500/30 bg-sky-500/10 text-sky-600 dark:text-sky-400"
              : "border-violet-500/30 bg-violet-500/10 text-violet-600 dark:text-violet-400",
          )}
        >
          <span
            aria-hidden
            className={cn(
              "size-1.5 rounded-full",
              observed ? "bg-sky-500" : "bg-violet-500",
            )}
          />
          {observed ? t("ledger.natureObserved") : t("ledger.natureInferred")}
        </span>
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

function HypothesisRow({ h }: { h: LedgerHypothesis }) {
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
              <EvidenceRow key={ev.id} ev={ev} />
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
}: {
  scanId?: string;
  running?: boolean;
}) {
  const { t } = useI18n();
  const { data, isLoading, error, refetch } = useScanLedger(scanId, running);

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
      {/* 运行中标志 + 状态计数（六桶零填充，后端契约保证）。 */}
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
                <HypothesisRow key={h.id} h={h} />
              ))}
            </ul>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
