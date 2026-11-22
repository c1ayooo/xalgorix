import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useTaskObjective } from "@/api/queries";
import { criterionStateKey, criterionStateStyles } from "@/components/task-objective-panel";
import { useI18n } from "@/i18n";
import type { ObjectiveReport } from "@/types/api";
import { cn } from "@/lib/utils";

/**
 * Run-level objective disposition (REDESIGN §3.1, D10/D11).
 *
 * Renders only this run's row of the task objective report
 * (GET /api/tasks/{id}/objective → runs[]), never the task-level ring/gaps/
 * VOI pool — that is the task-detail overview's job (§7.2). Honesty rules:
 * "not structured" is stated, not inferred; a run missing from the report
 * renders nothing instead of a fabricated zero.
 */

function AttainmentRow({ report }: { report: ObjectiveReport }) {
  const { t } = useI18n();
  return (
    <div className="flex flex-wrap items-center gap-2 text-xs">
      <span className="mono text-muted-foreground">
        {t("taskDetail.objectiveAttainment")}{" "}
        {Math.round((report.attainment ?? 0) * 100)}%
      </span>
      <span className={cn("rounded-sm px-1.5 py-0.5", criterionStateStyles.met)}>
        {t("taskDetail.objectiveMet")} {report.met}
      </span>
      <span className={cn("rounded-sm px-1.5 py-0.5", criterionStateStyles.unmet)}>
        {t("taskDetail.objectiveUnmet")} {report.unmet}
      </span>
      <span className={cn("rounded-sm px-1.5 py-0.5", criterionStateStyles.undetermined)}>
        {t("taskDetail.objectiveUndetermined")} {report.undetermined}
      </span>
      {report.manual_pending > 0 && (
        <span className={cn("rounded-sm px-1.5 py-0.5", criterionStateStyles.manual_pending)}>
          {t("taskDetail.objectiveManualPending")} {report.manual_pending}
        </span>
      )}
    </div>
  );
}

export function RunObjectiveCard({
  taskId,
  runId,
  scanId,
}: {
  taskId?: string;
  runId?: string;
  scanId: string;
}) {
  const { t } = useI18n();
  const { data, isLoading } = useTaskObjective(taskId);

  if (!taskId) return null;

  // Go nil slice → null；统一归一后再取长度（与任务面板同一纪律）。
  const report = data?.report
    ? { ...data.report, criteria: data.report.criteria ?? [] }
    : null;
  const runRow =
    data?.runs?.find((r) => (runId && r.run_id === runId) || r.scan_id === scanId) ??
    null;
  const runReport = runRow
    ? { ...runRow.report, criteria: runRow.report.criteria ?? [] }
    : null;

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("runDetail.objectiveTitle")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {isLoading && !data ? (
          <p className="text-xs text-muted-foreground">…</p>
        ) : !data || !data.structured ? (
          // 结构化目标缺席：如实说明，不推断达成度（REDESIGN §3.1）。
          <p className="text-xs text-muted-foreground">
            {t("taskDetail.objectiveNotStructured")}
          </p>
        ) : !runReport ? (
          // 目标已结构化，但报告里还没有这一 run 的行：不编造零分行。
          <p className="text-xs text-muted-foreground">
            {t("runDetail.objectiveRunPending")}
          </p>
        ) : (
          <>
            <AttainmentRow report={runReport} />
            {runReport.criteria.length === 0 ? (
              <p className="text-xs text-muted-foreground">
                {t("taskDetail.objectiveNoCriteria")}
              </p>
            ) : (
              <ul className="space-y-2">
                {runReport.criteria.map((criterion) => (
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
                            如实标注，不渲染成失败（与任务面板同一语义）。 */}
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
                            criterionStateStyles[criterion.state],
                          )}
                        >
                          {t(`taskDetail.objective${criterionStateKey(criterion.state)}`)}
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
                        <span className="mono">{criterion.evidence_finding_id}</span>
                        {criterion.evidence_tag ? ` (${criterion.evidence_tag})` : ""}
                      </p>
                    )}
                  </li>
                ))}
              </ul>
            )}
            {report && (
              <p className="border-t border-border pt-2 text-[11px] text-muted-foreground">
                {t("runDetail.objectiveTaskAgg")}{" "}
                {Math.round((report.attainment ?? 0) * 100)}%
              </p>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}
