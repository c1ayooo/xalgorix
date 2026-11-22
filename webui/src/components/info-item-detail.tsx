import { Link } from "react-router-dom";
import { AlertTriangle, ArrowUpRight } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { InfoSourceChip } from "@/components/info-source-chip";
import { NatureBadge, normalizeNature } from "@/components/nature-badge";
import { useScanLedger } from "@/api/queries";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type { InfoItem, LedgerEvidence, LedgerHypothesis } from "@/types/api";

/**
 * Full seven-dimension view of one InfoItem (CORE §3.2): content / source /
 * time / subject / purpose / context / relation. Machine values stay mono.
 *
 * The "projected hypotheses" block answers the other half of §3.1 — an
 * InfoItem is only meaningful through the hypotheses it supports, and the
 * ledger already carries `evidence.info_item_id`, so the join needs no new
 * backend endpoint and reuses the shared `["scan", id, "ledger"]` cache.
 */
export function InfoItemDetail({
  item,
  onSelectRelation,
  className,
}: {
  item: InfoItem;
  /** Relation ids and subjects can pivot the next query. */
  onSelectRelation?: (id: string) => void;
  className?: string;
}) {
  const { t } = useI18n();
  const observed = normalizeNature(item.nature) === "observed";
  const missingSupports =
    !observed && (item.supports?.length ?? 0) === 0;

  return (
    <div className={cn("space-y-3 text-xs", className)}>
      <p className="whitespace-pre-wrap break-words text-sm leading-6 text-foreground">
        {item.coordinates.content.text}
      </p>

      {missingSupports && (
        <p className="flex items-start gap-1.5 rounded-sm border border-amber-500/30 bg-amber-500/10 px-2.5 py-1.5 text-[11px] text-amber-700 dark:text-amber-300">
          <AlertTriangle className="mt-0.5 size-3 shrink-0" aria-hidden />
          {t("info.detail.inferredWithoutSupports")}
        </p>
      )}

      <dl className="grid grid-cols-1 gap-x-4 gap-y-2 sm:grid-cols-2 lg:grid-cols-3">
        <Field label={t("info.detail.nature")}>
          <NatureBadge nature={item.nature} size="xs" />
        </Field>
        <Field label={t("info.detail.source")}>
          <InfoSourceChip source={item.coordinates.source} />
        </Field>
        <Field label={t("info.detail.sourceAgent")} mono>
          {item.coordinates.source?.agent_id || "—"}
        </Field>
        <Field label={t("info.detail.sourceEvidence")} mono>
          {item.coordinates.source?.evidence_id || "—"}
        </Field>
        <Field label={t("info.observedAt")} mono>
          {item.coordinates.observed_at || "—"}
        </Field>
        <Field label={t("info.detail.validity")} mono>
          {formatValidity(item.valid_from, item.valid_until, t)}
        </Field>
        <Field label={t("info.subject")} mono>
          {item.coordinates.subject.kind
            ? `${item.coordinates.subject.kind}:${item.coordinates.subject.identifier}`
            : item.coordinates.subject.identifier || "—"}
        </Field>
        <Field label={t("info.purpose")}>{item.coordinates.purpose || "—"}</Field>
        <Field label={t("info.detail.coordinateStatus")} mono>
          {item.coordinate_status || "—"}
        </Field>
        <Field label={t("info.detail.coordinateConfidence")} mono>
          {typeof item.confidence === "number" && item.confidence > 0
            ? `${(item.confidence * 100).toFixed(0)}%`
            : t("info.detail.unannotated")}
        </Field>
        <Field label={t("info.detail.scan")} mono>
          {item.scan_id}
          <span className="ml-1 text-muted-foreground">
            #{item.source_seq}
          </span>
        </Field>
        <Field label={t("info.detail.relations")}>
          {item.coordinates.relation_ids?.length ? (
            <span className="flex flex-wrap gap-1">
              {item.coordinates.relation_ids.map((id) => (
                <button
                  key={id}
                  type="button"
                  onClick={() => onSelectRelation?.(id)}
                  className="rounded-sm border border-border px-1.5 py-0.5 font-mono text-[10px] transition-colors hover:bg-accent/40"
                >
                  {id}
                </button>
              ))}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          )}
        </Field>
      </dl>

      <ContextBlock context={item.coordinates.context} />

      {(item.supports?.length ?? 0) > 0 && (
        <p className="text-[11px] text-muted-foreground">
          {t("info.supports")}{" "}
          <span className="font-mono">{item.supports!.join(", ")}</span>
        </p>
      )}

      <ProjectedHypotheses scanId={item.scan_id} itemId={item.id} />
    </div>
  );
}

function Field({
  label,
  children,
  mono,
}: {
  label: string;
  children: React.ReactNode;
  mono?: boolean;
}) {
  return (
    <div className="min-w-0">
      <dt className="text-[10px] uppercase tracking-wider text-muted-foreground">
        {label}
      </dt>
      <dd
        className={cn(
          "mt-0.5 break-words text-foreground",
          mono && "font-mono text-[11px] tabular-nums",
        )}
      >
        {children}
      </dd>
    </div>
  );
}

function ContextBlock({ context }: { context?: Record<string, string> }) {
  const { t } = useI18n();
  const entries = Object.entries(context ?? {});
  if (entries.length === 0) return null;
  return (
    <div>
      <p className="text-[10px] uppercase tracking-wider text-muted-foreground">
        {t("info.detail.context")}
      </p>
      <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1 font-mono text-[10px] text-muted-foreground">
        {entries.map(([key, value]) => (
          <span key={key} className="min-w-0">
            <span className="text-foreground">{key}</span>
            <span aria-hidden> = </span>
            <span className="break-all">{value}</span>
          </span>
        ))}
      </div>
    </div>
  );
}

function formatValidity(
  from: string | undefined,
  until: string | undefined,
  t: (key: string) => string,
): string {
  if (!from && !until) return "—";
  return `${from ? new Date(from).toLocaleString() : "—"} → ${
    until ? new Date(until).toLocaleString() : t("info.detail.openEnded")
  }`;
}

/**
 * Client-side join of the ledger projection: hypotheses whose evidence links
 * this InfoItem. The ledger is the authority for hypothesis lifecycle, so the
 * block is read-only and never recomputes status.
 */
function ProjectedHypotheses({
  scanId,
  itemId,
}: {
  scanId: string;
  itemId: string;
}) {
  const { t } = useI18n();
  const { data, isLoading, error } = useScanLedger(scanId);
  if (isLoading && !data) {
    return (
      <p className="text-[11px] text-muted-foreground">
        {t("info.detail.hypothesesLoading")}
      </p>
    );
  }
  if (error) {
    return (
      <p className="text-[11px] text-muted-foreground">
        {t("info.detail.hypothesesUnavailable")}
      </p>
    );
  }
  const matches: { hypothesis: LedgerHypothesis; evidence: LedgerEvidence[] }[] = [];
  for (const hypothesis of data?.hypotheses ?? []) {
    const evidence = (hypothesis.evidence ?? []).filter(
      (ev) => ev.info_item_id === itemId,
    );
    if (evidence.length > 0) matches.push({ hypothesis, evidence });
  }
  return (
    <div className="border-t border-border pt-2">
      <p className="text-[10px] uppercase tracking-wider text-muted-foreground">
        {t("info.detail.hypotheses")}
      </p>
      {matches.length === 0 ? (
        <p className="mt-1 text-[11px] text-muted-foreground">
          {data?.ledger_damaged || data?.incomplete
            ? data?.note || t("ledger.incomplete")
            : t("info.detail.hypothesesEmpty")}
        </p>
      ) : (
        <ul className="mt-1 space-y-1">
          {matches.map(({ hypothesis, evidence }) => (
            <li
              key={hypothesis.id}
              className="flex flex-wrap items-center gap-2 rounded-sm border border-border/70 px-2 py-1"
            >
              <span className="font-mono text-[10px] text-muted-foreground">
                {hypothesis.id}
              </span>
              <Badge variant="outline" className="shrink-0">
                {t(`ledger.status.${hypothesis.status}`)}
              </Badge>
              <span className="min-w-0 flex-1 truncate">{hypothesis.title}</span>
              <span className="font-mono text-[10px] text-muted-foreground">
                {evidence.map((ev) => ev.kind).join(", ")}
              </span>
              <Link
                to={`/scans/${scanId}`}
                className="inline-flex shrink-0 items-center gap-0.5 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
              >
                {t("info.detail.openRun")}
                <ArrowUpRight className="size-3" aria-hidden />
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
