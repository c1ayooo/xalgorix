import { useEffect, useMemo, useState } from "react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { AssociationView } from "@/components/info-views/association-view";
import { InfoDisabled, InfoLoading, InfoNotice, InfoQueryError } from "@/components/info-views/common";
import { useInfoAssociation } from "@/api/queries";
import { useI18n } from "@/i18n";

/**
 * Association drill-down for one InfoItem selected in the heatmap drawer.
 * This is CORE §4.2 — a bounded relation walk (depth ≤ 3) over explicit edges,
 * never an inference from text similarity.
 */
export function AssociationDialog({
  open,
  projectId,
  taskId,
  scanId,
  seed,
  onOpenChange,
}: {
  open: boolean;
  projectId: string;
  taskId?: string;
  scanId?: string;
  seed: string | null;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useI18n();
  const [seeds, setSeeds] = useState<string[]>(seed ? [seed] : []);
  const [edgeTypes, setEdgeTypes] = useState<string[]>([]);
  const [depth, setDepth] = useState(2);

  useEffect(() => {
    if (open) setSeeds(seed ? [seed] : []);
  }, [open, seed]);

  const params = useMemo(
    () => ({
      projectId,
      taskId,
      // Keep the walk inside the visible scope: a run-scoped map must not
      // silently traverse other runs' edges.
      scanId,
      ids: seeds.join(","),
      edgeTypes: edgeTypes.length ? edgeTypes : undefined,
      maxDepth: depth,
      limit: 100,
    }),
    [projectId, taskId, scanId, seeds, edgeTypes, depth],
  );
  const assoc = useInfoAssociation(params, open && seeds.length > 0);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-5xl">
        <DialogHeader>
          <DialogTitle>{t("info.association.graphLabel")}</DialogTitle>
          <DialogDescription>{t("info.heatmap.associationHint")}</DialogDescription>
        </DialogHeader>
        {seeds.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("info.association.empty.description")}</p>
        ) : assoc.isLoading && !assoc.data ? (
          <InfoLoading rows={3} />
        ) : assoc.error ? (
          <InfoQueryError error={assoc.error} onRetry={() => assoc.refetch()} />
        ) : assoc.data && !assoc.data.available ? (
          <InfoDisabled />
        ) : (
          <div className="space-y-3">
            {assoc.data && (
              <InfoNotice
                truncated={assoc.data.truncated}
                incomplete={assoc.data.incomplete}
                gapReason={assoc.data.gap_reason}
              />
            )}
            <AssociationView
              hits={assoc.data?.hits ?? []}
              seeds={seeds}
              edgeTypes={edgeTypes}
              depth={depth}
              onSeedsChange={setSeeds}
              onEdgeTypesChange={setEdgeTypes}
              onDepthChange={setDepth}
              onSeedNode={(id) => setSeeds([id])}
            />
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
