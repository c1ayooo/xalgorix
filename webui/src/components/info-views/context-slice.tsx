import { ExternalLink } from "lucide-react";
import { Link } from "react-router-dom";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { InfoSourceChip } from "@/components/info-source-chip";
import { NatureBadge } from "@/components/nature-badge";
import { SectionLabel } from "@/components/info-views/common";
import { useScanLedger, useScanTraffic } from "@/api/queries";
import { useI18n } from "@/i18n";
import type { InfoItem } from "@/types/api";

/**
 * Context slice (CORE §4.3): fix a few dimensions, then project the *mixed*
 * view — observations, inferences, the Run's finding references and its traffic
 * archive — instead of repeating the region card list.
 *
 * Honest scope: the endpoint is single-Run by contract, so the header states the
 * Run and the page never presents this as a cross-Run slice.
 */
export function ContextSlice({
  scanId,
  scanSelect,
  items,
  subject,
  since,
  until,
  onSubjectChange,
  onSinceChange,
  onUntilChange,
}: {
  scanId: string;
  scanSelect: React.ReactNode;
  items: InfoItem[];
  subject: string;
  since: string;
  until: string;
  onSubjectChange: (value: string) => void;
  onSinceChange: (value: string) => void;
  onUntilChange: (value: string) => void;
}) {
  const { t } = useI18n();
  const observed = items.filter((item) => item.nature === "observed");
  const inferred = items.filter((item) => item.nature !== "observed");

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-card p-3">
        {scanSelect}
        <Input
          value={subject}
          onChange={(event) => onSubjectChange(event.target.value)}
          placeholder={t("info.filter.subjectPlaceholder")}
          className="h-9 w-48"
        />
        {/* URL keeps the raw local wall-clock value; the page converts it to
            RFC3339 at query time so the input never drifts by timezone. */}
        <Input
          type="datetime-local"
          value={since}
          onChange={(event) => onSinceChange(event.target.value)}
          aria-label={t("info.context.since")}
          className="h-9 w-52"
        />
        <Input
          type="datetime-local"
          value={until}
          onChange={(event) => onUntilChange(event.target.value)}
          aria-label={t("info.context.until")}
          className="h-9 w-52"
        />
        <span className="ml-auto font-mono text-[10px] text-muted-foreground">
          {scanId}
        </span>
      </div>

      <section className="space-y-2">
        <SectionLabel>
          {t("contextSlice.observed")} <span className="font-mono tabular-nums">{observed.length}</span>
        </SectionLabel>
        <MixedList items={observed} emptyKey="contextSlice.observedEmpty" />
      </section>

      <section className="space-y-2">
        <SectionLabel>
          {t("contextSlice.inferred")} <span className="font-mono tabular-nums">{inferred.length}</span>
        </SectionLabel>
        <MixedList items={inferred} emptyKey="contextSlice.inferredEmpty" showSupports />
      </section>

      <section className="space-y-2">
        <SectionLabel>{t("contextSlice.findings")}</SectionLabel>
        <FindingRefSection scanId={scanId} />
      </section>

      <section className="space-y-2">
        <SectionLabel>{t("contextSlice.traffic")}</SectionLabel>
        <TrafficSection scanId={scanId} subject={subject} />
      </section>
    </div>
  );
}

function MixedList({
  items,
  emptyKey,
  showSupports,
}: {
  items: InfoItem[];
  emptyKey: string;
  showSupports?: boolean;
}) {
  const { t } = useI18n();
  if (items.length === 0) {
    return <p className="text-[11px] text-muted-foreground">{t(emptyKey)}</p>;
  }
  return (
    <ul className="divide-y divide-border rounded-md border border-border bg-card">
      {items.map((item) => (
        <li key={`${item.scan_id}:${item.id}`} className="space-y-1 px-3 py-2">
          <div className="flex flex-wrap items-center gap-2">
            <NatureBadge nature={item.nature} size="xs" />
            <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-muted-foreground">
              {item.coordinates.subject.identifier || item.id}
            </span>
            <InfoSourceChip source={item.coordinates.source} />
            <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
              {new Date(item.coordinates.observed_at).toLocaleTimeString()}
            </span>
          </div>
          <p className="whitespace-pre-wrap break-words text-xs leading-5">
            {item.coordinates.content.text}
          </p>
          {showSupports && (item.supports?.length ?? 0) > 0 && (
            <p className="font-mono text-[10px] text-muted-foreground">
              {t("info.supports")} {item.supports!.join(", ")}
            </p>
          )}
        </li>
      ))}
    </ul>
  );
}

/** The Run's finding references, read off the ledger projection (no new API). */
function FindingRefSection({ scanId }: { scanId: string }) {
  const { t } = useI18n();
  const { data, isLoading } = useScanLedger(scanId);
  if (isLoading && !data) {
    return <p className="text-[11px] text-muted-foreground">{t("common.loading")}</p>;
  }
  const refs =
    data?.hypotheses.flatMap((hypothesis) =>
      (hypothesis.evidence ?? [])
        .filter((evidence) => evidence.kind === "finding_ref" || evidence.finding_id)
        .map((evidence) => ({ hypothesis, evidence })),
    ) ?? [];
  if (refs.length === 0) {
    return <p className="text-[11px] text-muted-foreground">{t("contextSlice.findingsEmpty")}</p>;
  }
  return (
    <ul className="space-y-1">
      {refs.map(({ hypothesis, evidence }) => (
        <li
          key={`${hypothesis.id}:${evidence.id}`}
          className="flex flex-wrap items-center gap-2 rounded-sm border border-border/70 px-2.5 py-1.5 text-xs"
        >
          <span className="shrink-0 rounded-sm border border-border px-1.5 font-mono text-[10px]">
            {evidence.kind}
          </span>
          <span className="min-w-0 flex-1 truncate">{evidence.summary}</span>
          <span className="font-mono text-[10px] text-muted-foreground">
            {hypothesis.id}
          </span>
          {evidence.finding_id && (
            <Badge variant="outline" className="shrink-0 font-mono">
              {evidence.finding_id}
            </Badge>
          )}
        </li>
      ))}
    </ul>
  );
}

/** Traffic archive for the Run, filtered by the pinned subject when present. */
function TrafficSection({ scanId, subject }: { scanId: string; subject: string }) {
  const { t } = useI18n();
  const { data, isLoading } = useScanTraffic(scanId);
  if (isLoading && !data) {
    return <p className="text-[11px] text-muted-foreground">{t("common.loading")}</p>;
  }
  const needle = subject.trim().toLowerCase();
  const exchanges = (data?.exchanges ?? []).filter(
    (exchange) => !needle || exchange.url.toLowerCase().includes(needle),
  );
  if (exchanges.length === 0) {
    return <p className="text-[11px] text-muted-foreground">{t("contextSlice.trafficEmpty")}</p>;
  }
  return (
    <ul className="space-y-1">
      {exchanges.slice(0, 8).map((exchange) => (
        <li
          key={exchange.seq}
          className="flex flex-wrap items-center gap-2 rounded-sm border border-border/70 px-2.5 py-1.5 text-xs"
        >
          <span className="font-mono text-[10px] uppercase text-muted-foreground">
            {exchange.method}
          </span>
          <span className="min-w-0 flex-1 truncate font-mono text-[11px]">
            {exchange.url}
          </span>
          {typeof exchange.status === "number" && (
            <span className="font-mono text-[10px] tabular-nums text-muted-foreground">
              {exchange.status}
            </span>
          )}
        </li>
      ))}
      <li>
        <Link
          to={`/scans/${scanId}`}
          className="inline-flex items-center gap-1 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
        >
          {t("contextSlice.openRunTraffic")}
          <ExternalLink className="size-3" aria-hidden />
        </Link>
      </li>
    </ul>
  );
}

