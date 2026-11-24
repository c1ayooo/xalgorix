import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Boxes, Network } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { InfoItemDetail } from "@/components/info-item-detail";
import { NatureBadge } from "@/components/nature-badge";
import {
  InfoDisabled,
  InfoLoading,
  InfoNotice,
  InfoQueryError,
} from "@/components/info-views/common";
import { LOOP_STAGES, STAGE_LEVEL, stageOf, stageStyle } from "@/lib/stage-heat";
import { subjectKey } from "@/lib/nebula";
import { subjectKindLabel } from "@/lib/subject-kind";
import { useInfoRegion } from "@/api/queries";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type {
  InfoHeatmapRow,
  InfoItem,
  InfoRegionParams,
  InfoRelationEdge,
  InfoType,
} from "@/types/api";

/**
 * Detail for one nebula point: the loop distribution plus the evidence itself.
 */
export function SubjectDetailDialog({
  open,
  onOpenChange,
  subject,
  subjectKind,
  row,
  projectId,
  taskId,
  scanId,
  after,
  edges,
  onOpenSubject,
  onAssociate,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  subject: string;
  subjectKind: string;
  row: InfoHeatmapRow | null;
  projectId: string;
  taskId?: string;
  scanId?: string;
  after?: string;
  /** Edges visible in the current map view (chain only unless toggled). */
  edges: InfoRelationEdge[];
  onOpenSubject: (subject: string, subjectKind: string) => void;
  onAssociate: (item: InfoItem) => void;
}) {
  const { t } = useI18n();
  const [stage, setStage] = useState<InfoType | null>(null);
  const [detail, setDetail] = useState<InfoItem | null>(null);

  useEffect(() => {
    if (!open) return;
    setStage(null);
    setDetail(null);
  }, [open, subject, subjectKind]);

  const params = useMemo(
    () => regionParams(subject, subjectKind, stage, { projectId, taskId, scanId, after }),
    [subject, subjectKind, stage, projectId, taskId, scanId, after],
  );
  const region = useInfoRegion(params, open && !!subject);
  const items = region.data?.items ?? [];
  const level = row ? stageOf(row.counts) : null;
  const style = level ? stageStyle(level) : null;
  const currentKey = subjectKey(subject, subjectKind);
  const relationEdges = useMemo(
    () =>
      edges.filter(
        (edge) =>
          subjectKey(edge.src_subject, edge.src_kind ?? "") === currentKey ||
          subjectKey(edge.dst_subject, edge.dst_kind ?? "") === currentKey,
      ),
    [edges, currentKey],
  );

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle className="flex flex-wrap items-center gap-2">
            <span className="break-all font-mono text-sm">{subject}</span>
            {subjectKind && (
              <Badge variant="outline">{subjectKindLabel(t, subjectKind)}</Badge>
            )}
            {style && (
              <span
                className="rounded-sm px-1.5 py-0.5 text-[10px] font-medium"
                style={{ background: style.bg, color: style.fg }}
              >
                {t(style.labelKey)}
              </span>
            )}
          </DialogTitle>
          <DialogDescription className="font-mono text-[11px] tabular-nums">
            {row
              ? `${row.total} ${t("info.heatmap.items")} · ${t("info.map.lastObserved")} ${
                  row.last_observed ?? "—"
                }`
              : subject}
          </DialogDescription>
        </DialogHeader>

        <div className="max-h-[70vh] space-y-3 overflow-y-auto pr-0.5">
          {row && (
            <div className="flex flex-wrap items-center gap-1">
              <StageChip
                active={stage === null}
                label={t("info.map.allStages")}
                onClick={() => setStage(null)}
              />
              {LOOP_STAGES.map((entry) => {
                const count = row.counts[entry] ?? 0;
                const chipStyle = stageStyle(STAGE_LEVEL[entry]);
                return (
                  <button
                    key={entry}
                    type="button"
                    onClick={() => setStage(stage === entry ? null : entry)}
                    aria-pressed={stage === entry}
                    className={cn(
                      "inline-flex items-center gap-1 rounded-sm border px-1.5 py-0.5 text-[10px] transition-colors",
                      stage === entry
                        ? "border-ring bg-accent text-foreground"
                        : count > 0
                          ? "border-border text-foreground hover:bg-accent/40"
                          : "border-border/60 text-muted-foreground/60 hover:bg-accent/20",
                    )}
                    title={t(chipStyle.labelKey)}
                  >
                    <span
                      className="size-2 rounded-[2px]"
                      style={{ background: chipStyle.bg }}
                      aria-hidden
                    />
                    {t(`info.stage.${entry}`)}
                    <span className="font-mono tabular-nums">{count}</span>
                  </button>
                );
              })}
            </div>
          )}

          <section>
            <h4 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              {t("info.map.relations")}
            </h4>
            {relationEdges.length === 0 ? (
              <p className="text-xs text-muted-foreground">{t("info.map.noRelations")}</p>
            ) : (
              <ul className="space-y-1">
                {relationEdges.map((edge, i) => {
                  const outgoing =
                    subjectKey(edge.src_subject, edge.src_kind ?? "") === currentKey;
                  const otherSubject = outgoing ? edge.dst_subject : edge.src_subject;
                  const otherKind = (outgoing ? edge.dst_kind : edge.src_kind) ?? "";
                  const otherTitle = outgoing ? edge.dst_title : edge.src_title;
                  return (
                    <li
                      key={`${edge.src_subject}-${edge.rel}-${edge.dst_subject}-${i}`}
                      className="flex items-center gap-1.5 text-[11px]"
                    >
                      <span className="w-3 shrink-0 text-center text-muted-foreground">
                        {outgoing ? "→" : "←"}
                      </span>
                      <Badge variant="muted">{relLabel(t, edge.rel)}</Badge>
                      <button
                        type="button"
                        className="min-w-0 flex-1 truncate text-left font-mono hover:underline"
                        title={otherTitle || otherSubject}
                        onClick={() => onOpenSubject(otherSubject, otherKind)}
                      >
                        {otherSubject}
                      </button>
                      <span className="font-mono tabular-nums text-muted-foreground">
                        {edge.count}×
                      </span>
                    </li>
                  );
                })}
              </ul>
            )}
          </section>

          {!scanId && (
            <Link
              to={`/assets?q=${encodeURIComponent(subject)}`}
              className="inline-flex w-fit items-center gap-1 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
            >
              <Boxes className="size-3.5" />
              {t("info.heatmap.findAsset")}
            </Link>
          )}

          {region.isLoading && !region.data ? (
            <InfoLoading rows={3} />
          ) : region.error ? (
            <InfoQueryError error={region.error} onRetry={() => region.refetch()} />
          ) : !region.data?.available ? (
            <InfoDisabled />
          ) : (
            <div className="space-y-2">
              <InfoNotice
                truncated={region.data.truncated}
                incomplete={region.data.incomplete}
                gapReason={region.data.gap_reason}
              />
              {items.length === 0 ? (
                <p className="text-xs text-muted-foreground">
                  {t("info.heatmap.cellEmpty")}
                </p>
              ) : (
                <ul className="space-y-2">
                  {items.map((item) => (
                    <li key={item.id} className="rounded-sm border border-border p-2.5">
                      <div className="flex items-center gap-2">
                        <NatureBadge nature={item.nature} size="xs" />
                        <span className="truncate font-mono text-[10px] text-muted-foreground">
                          {item.coordinates.observed_at}
                        </span>
                      </div>
                      <button
                        type="button"
                        className="mt-1 block w-full text-left"
                        onClick={() => setDetail(item)}
                      >
                        <p className="line-clamp-3 text-xs leading-5 text-foreground">
                          {item.coordinates.content.text ||
                            item.coordinates.subject.identifier}
                        </p>
                      </button>
                      <div className="mt-1.5 flex items-center gap-1.5">
                        <span className="min-w-0 flex-1 truncate font-mono text-[10px] text-muted-foreground">
                          {item.coordinates.purpose || "—"}
                        </span>
                        <Button
                          size="sm"
                          variant="ghost"
                          className="h-6 px-1.5 text-[10px]"
                          onClick={() => setDetail(item)}
                        >
                          {t("common.view")}
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          className="h-6 px-1.5 text-[10px]"
                          onClick={() => onAssociate(item)}
                        >
                          <Network className="size-3" />
                          {t("info.associate.action")}
                        </Button>
                      </div>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </div>

        <Dialog open={detail !== null} onOpenChange={(next) => !next && setDetail(null)}>
          <DialogContent className="max-w-2xl">
            <DialogHeader>
              <DialogTitle className="font-mono text-sm">
                {detail?.coordinates.subject.identifier || t("info.detail.nature")}
              </DialogTitle>
            </DialogHeader>
            {detail && <InfoItemDetail item={detail} />}
          </DialogContent>
        </Dialog>
      </DialogContent>
    </Dialog>
  );
}

function StageChip({
  active,
  label,
  onClick,
}: {
  active: boolean;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "rounded-sm border px-1.5 py-0.5 text-[10px] transition-colors",
        active
          ? "border-ring bg-accent text-foreground"
          : "border-border text-muted-foreground hover:bg-accent/40",
      )}
    >
      {label}
    </button>
  );
}

function relLabel(t: (key: string) => string, rel: string): string {
  const key = `info.rel.${rel}`;
  const label = t(key);
  return label === key ? rel : label;
}

function regionParams(
  subject: string,
  subjectKind: string,
  stage: InfoType | null,
  scope: {
    projectId: string;
    taskId?: string;
    scanId?: string;
    after?: string;
  },
): InfoRegionParams {
  const base: InfoRegionParams = {
    projectId: scope.projectId,
    taskId: scope.taskId,
    scanId: scope.scanId,
    subject,
    subjectKind: subjectKind || undefined,
    after: scope.after,
    limit: 50,
  };
  switch (stage) {
    case "observation":
      return { ...base, layer: "atom", nature: "observed" };
    case "inference":
      return { ...base, layer: "atom", nature: "inferred" };
    case "belief":
    case "intent":
    case "fact":
    case "finding":
      return { ...base, layer: "aggregate", kind: [stage] };
    default:
      return base;
  }
}
