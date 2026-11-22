import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useTaskObjective } from "@/api/queries";
import { useI18n } from "@/i18n";
import type { ObjectiveCriterionState } from "@/types/api";
import { cn } from "@/lib/utils";

/**
 * Task objective panel — the honest attainment view (REDESIGN §3.1).
 *
 * It renders what the report pipeline already accepted: every criterion with
 * its state and evidence reference, the aggregate attainment (display only),
 * the loop position of the latest run with its derivation basis, the open
 * gaps, and the VOI-weighted schedulable pool.
 *
 * "Not structured" is a first-class answer: a task without an objective shows
 * that instead of an empty panel or an invented score.
 */

const stateStyles: Record<ObjectiveCriterionState, string> = {
  met: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-300",
  unmet: "bg-amber-500/10 text-amber-700 dark:text-amber-300",
  undetermined: "bg-muted text-muted-foreground",
  manual_pending: "bg-sky-500/10 text-sky-700 dark:text-sky-300",
};

// Shared with the run-level objective card (run-objective-card.tsx) so both
// surfaces render criterion states with one set of semantics.
export const criterionStateStyles = stateStyles;

export function criterionStateKey(state: ObjectiveCriterionState): string {
  return stateKey(state);
}

export function TaskObjectivePanel({ taskId }: { taskId?: string }) {
  const { t } = useI18n();
  const { data, isLoading } = useTaskObjective(taskId);

  // Go nil slice 序列化为 null（report.criteria / gaps / schedulable）——
  // 统一归一为数组，渲染分支禁止对可空字段直接 .length/.map。
  const report = data?.report
    ? { ...data.report, criteria: data.report.criteria ?? [] }
    : null;
  const gaps = data?.gaps ?? [];
  const schedulable = data?.schedulable ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("taskDetail.objectiveTitle")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {isLoading && !data ? (
          <p className="text-xs text-muted-foreground">…</p>
        ) : !data || !data.structured ? (
          <p className="text-xs text-muted-foreground">
            {t("taskDetail.objectiveNotStructured")}
          </p>
        ) : !report ? (
          // objective 已结构化但验收报告尚未产出（Run 未完成/被中断）：
          // 首选一等空态语义，不编造分数。
          <p className="text-xs text-muted-foreground">
            {t("taskDetail.objectiveReportPending")}
          </p>
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <span className="mono text-muted-foreground">
                {t("taskDetail.objectiveAttainment")}{" "}
                {Math.round((report.attainment ?? 0) * 100)}%
              </span>
              <span
                className={cn(
                  "rounded-sm px-1.5 py-0.5",
                  stateStyles.met,
                )}
              >
                {t("taskDetail.objectiveMet")} {report.met}
              </span>
              <span
                className={cn(
                  "rounded-sm px-1.5 py-0.5",
                  stateStyles.unmet,
                )}
              >
                {t("taskDetail.objectiveUnmet")} {report.unmet}
              </span>
              <span
                className={cn(
                  "rounded-sm px-1.5 py-0.5",
                  stateStyles.undetermined,
                )}
              >
                {t("taskDetail.objectiveUndetermined")} {report.undetermined}
              </span>
              {report.manual_pending > 0 && (
                <span
                  className={cn(
                    "rounded-sm px-1.5 py-0.5",
                    stateStyles.manual_pending,
                  )}
                >
                  {t("taskDetail.objectiveManualPending")}{" "}
                  {report.manual_pending}
                </span>
              )}
            </div>

            {report.criteria.length === 0 ? (
              <p className="text-xs text-muted-foreground">
                {t("taskDetail.objectiveNoCriteria")}
              </p>
            ) : (
              <ul className="space-y-2">
                {report.criteria.map((criterion) => (
                  <li
                    key={criterion.id}
                    className="rounded-sm border border-border/60 p-2"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="mono text-xs text-muted-foreground">
                        {criterion.id}
                      </span>
                      <span className="flex items-center gap-1.5">
                        {/* R-goal：required 且引擎可判定的判据未达成 = 收工义务，
                            run 不主动结束——如实标注，不渲染成失败。 */}
                        {criterion.required &&
                          criterion.verify_by !== "manual" &&
                          criterion.state !== "met" && (
                            <span className="rounded-sm border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 text-[11px] text-amber-700 dark:text-amber-300">
                              {t("taskDetail.objectiveBlocking")}
                            </span>
                          )}
                        <span
                          className={cn(
                            "rounded-sm px-1.5 py-0.5 text-[11px]",
                            stateStyles[criterion.state],
                          )}
                        >
                          {t(`taskDetail.objective${stateKey(criterion.state)}`)}
                        </span>
                      </span>
                    </div>
                    <p className="mt-1 text-xs">
                      {criterion.kind === "finding_class"
                        ? criterion.vuln_class
                        : criterion.kind === "capability"
                          ? criterion.capability
                          : t("tasks.objectiveCriterionCoverage")}
                    </p>
                    {criterion.reason && (
                      <p className="mt-0.5 text-[11px] text-muted-foreground">
                        {criterion.reason}
                      </p>
                    )}
                    {criterion.evidence_finding_id && (
                      <p className="mt-0.5 text-[11px] text-muted-foreground">
                        {t("taskDetail.objectiveEvidence")}:{" "}
                        <span className="mono">
                          {criterion.evidence_finding_id}
                        </span>
                        {criterion.evidence_tag
                          ? ` (${criterion.evidence_tag})`
                          : ""}
                      </p>
                    )}
                  </li>
                ))}
              </ul>
            )}

            <div className="space-y-1 border-t border-border pt-2 text-xs">
              <p>
                <span className="text-muted-foreground">
                  {t("taskDetail.objectiveRing")}:{" "}
                </span>
                <span className="mono">{t(`taskDetail.ring${ringKey(data.ring ?? "")}`)}</span>
                <span className="ml-2 text-muted-foreground">
                  ({t("taskDetail.objectiveRingBasis")}: {data.ring_basis})
                </span>
              </p>
              {gaps.length > 0 && (
                <div>
                  <span className="text-muted-foreground">
                    {t("taskDetail.objectiveGaps")}:
                  </span>
                  <ul className="mt-0.5 list-inside list-disc">
                    {gaps.map((gap) => (
                      <li key={gap}>{gap}</li>
                    ))}
                  </ul>
                </div>
              )}
              {schedulable.length > 0 && (
                <div>
                  <span className="text-muted-foreground">
                    {t("taskDetail.objectiveSchedulable")}:
                  </span>
                  <ul className="mt-0.5 space-y-0.5">
                    {schedulable.map((h) => (
                      <li key={h.id} className="mono text-[11px]">
                        {h.id}
                        {h.vuln_class ? ` · ${h.vuln_class}` : ""}
                        {h.endpoint ? ` · ${h.endpoint}` : ""}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}

function stateKey(state: ObjectiveCriterionState): string {
  switch (state) {
    case "met":
      return "Met";
    case "unmet":
      return "Unmet";
    case "manual_pending":
      return "ManualPending";
    default:
      return "Undetermined";
  }
}

function ringKey(ring: string): string {
  switch (ring) {
    case "observe":
    case "believe":
    case "obligation":
    case "act":
    case "evidence":
    case "verdict":
    case "close":
      return ring.charAt(0).toUpperCase() + ring.slice(1);
    default:
      return "Unknown";
  }
}
