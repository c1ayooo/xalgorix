import { useEffect, useState } from "react";
import { toast } from "sonner";
import { Sparkles } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Input as TextInput } from "@/components/ui/input";
import { useCreateTask, useUpdateTask } from "@/api/queries";
import { api } from "@/api/client";
import { useI18n } from "@/i18n";
import type {
  Objective,
  ObjectiveCriterion,
  ObjectiveCriterionKind,
  ObjectiveKind,
  ObjectiveVerifyBy,
  Task,
  TaskSchedule,
} from "@/types/api";

/**
 * Create / edit a task. The task is the Goal+Context host: goal is required,
 * instruction / phase policy / schedule are optional refinements that the
 * engine folds into each run's briefing.
 */
export function TaskFormDialog({
  open,
  onOpenChange,
  projectId,
  task,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  projectId: string;
  task?: Task | null;
  onCreated?: (task: Task) => void;
}) {
  const { t } = useI18n();
  const create = useCreateTask();
  const update = useUpdateTask();
  const editing = !!task;

  const [name, setName] = useState("");
  const [goal, setGoal] = useState("");
  const [instruction, setInstruction] = useState("");
  const [scanMode, setScanMode] = useState("single");
  const [intensity, setIntensity] = useState("active");
  const [scheduleOn, setScheduleOn] = useState(false);
  const [interval, setIntervalValue] = useState<TaskSchedule["interval"]>("daily");
  const [time, setTime] = useState("02:00");
  // Structured objective (REDESIGN §3.1). Off = "not structured": the task
  // keeps its free-text goal and no attainment is claimed.
  const [objectiveOn, setObjectiveOn] = useState(false);
  const [objectiveKind, setObjectiveKind] = useState<ObjectiveKind>("engagement");
  const [objectiveStatement, setObjectiveStatement] = useState("");
  const [criteria, setCriteria] = useState<ObjectiveCriterion[]>([]);
  // 操作约束（may/may-not，每行一条）：分析草案带入，随 objective 落盘并进
  // 提示词小节；只收窄不放大。
  const [constraints, setConstraints] = useState("");
  // R-goal 意图分析：「分析目标」按钮调预览端点，草案直接预填结构化目标
  // 区块供编辑确认；分析 notes 作为一行灰字提示（不落盘，仅会话内可见）。
  const [analyzing, setAnalyzing] = useState(false);
  const [goalNotes, setGoalNotes] = useState("");

  useEffect(() => {
    if (!open) return;
    setName(task?.name ?? "");
    setGoal(task?.goal ?? "");
    setInstruction(task?.instruction ?? "");
    setScanMode(task?.phase_policy?.scan_mode ?? "single");
    setIntensity(task?.phase_policy?.intensity ?? "active");
    if (task?.schedule) {
      setScheduleOn(true);
      setIntervalValue(task.schedule.interval);
      setTime(task.schedule.time ?? "02:00");
    } else {
      setScheduleOn(false);
    }
    if (task?.objective) {
      setObjectiveOn(true);
      setObjectiveKind(task.objective.kind);
      setObjectiveStatement(task.objective.statement ?? "");
      setCriteria(task.objective.criteria ?? []);
      setConstraints((task.objective.constraints ?? []).join("\n"));
    } else {
      setObjectiveOn(false);
      setObjectiveKind("engagement");
      setObjectiveStatement("");
      setCriteria([]);
      setConstraints("");
    }
    setGoalNotes("");
    setAnalyzing(false);
  }, [open, task]);

  // 分析目标（R-goal）：一次 LLM 意图分析，草案预填 objective 区块。
  // 失败只 toast，不阻塞手填（fail-open 与后端一致）。
  function analyzeGoal() {
    if (!goal.trim() || analyzing) return;
    setAnalyzing(true);
    api
      .analyzeGoal(projectId, { goal: goal.trim(), instruction: instruction.trim() || undefined })
      .then(({ proposal }) => {
        setObjectiveOn(true);
        setObjectiveKind("engagement");
        setObjectiveStatement(proposal.statement ?? "");
        setCriteria(
          (proposal.criteria ?? []).map((c) => ({
            kind: c.kind,
            vuln_class: c.vuln_class,
            capability: c.capability,
            verify_by: c.verify_by,
            required: c.required,
          })),
        );
        setGoalNotes(proposal.notes ?? "");
        setConstraints((proposal.constraints ?? []).join("\n"));
        toast.success(t("toast.goalAnalyzed"));
      })
      .catch((err: Error) =>
        toast.error(`${t("toast.goalAnalysisFailed")}: ${err.message}`),
      )
      .finally(() => setAnalyzing(false));
  }

  function submit() {
    if (!name.trim() || !goal.trim()) return;
    if (objectiveOn && !objectiveStatement.trim()) return;
    const objective: Objective | null = objectiveOn
      ? {
          kind: objectiveKind,
          statement: objectiveStatement.trim(),
          criteria: criteria.map((c) => ({ ...c })),
          constraints: constraints
            .split("\n")
            .map((s) => s.trim())
            .filter(Boolean),
        }
      : null;
    const req = {
      name: name.trim(),
      goal: goal.trim(),
      instruction: instruction.trim() || undefined,
      phase_policy: {
        scan_mode: scanMode as "single" | "wildcard" | "dast",
        intensity: intensity as "active" | "passive",
      },
      schedule: scheduleOn
        ? { interval, time, timezone: Intl.DateTimeFormat().resolvedOptions().timeZone }
        : null,
      // null clears the objective on PATCH; undefined would leave it as is.
      objective,
    };
    const mutation = editing
      ? update.mutateAsync({ id: task!.id, req })
      : create.mutateAsync({ projectId, req });
    mutation
      .then((saved) => {
        toast.success(editing ? t("toast.taskUpdated") : t("toast.taskCreated"));
        onOpenChange(false);
        if (!editing) onCreated?.(saved);
      })
      .catch((err: Error) =>
        toast.error(`${t("toast.taskFailed")}: ${err.message}`),
      );
  }

  function updateCriterion(index: number, patch: Partial<ObjectiveCriterion>) {
    setCriteria((prev) =>
      prev.map((c, i) => (i === index ? { ...c, ...patch } : c)),
    );
  }

  function addCriterion() {
    setCriteria((prev) => [
      ...prev,
      { kind: "finding_class", verify_by: "verifier", required: true },
    ]);
  }

  function removeCriterion(index: number) {
    setCriteria((prev) => prev.filter((_, i) => i !== index));
  }

  const pending = create.isPending || update.isPending;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {editing ? t("tasks.editTitle") : t("tasks.createTitle")}
          </DialogTitle>
          <DialogDescription>{t("tasks.formDescription")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="task-name">{t("tasks.fieldName")}</Label>
            <Input id="task-name" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <div className="flex items-center justify-between gap-2">
              <Label htmlFor="task-goal">{t("tasks.fieldGoal")}</Label>
              <Button
                variant="outline"
                size="sm"
                onClick={analyzeGoal}
                disabled={analyzing || !goal.trim()}
                aria-label={t("tasks.analyzeGoal")}
              >
                <Sparkles />
                {analyzing ? t("tasks.analyzing") : t("tasks.analyzeGoal")}
              </Button>
            </div>
            <Textarea
              id="task-goal"
              rows={2}
              value={goal}
              onChange={(e) => setGoal(e.target.value)}
              placeholder={t("tasks.goalPlaceholder")}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="task-instruction">{t("tasks.fieldInstruction")}</Label>
            <Textarea
              id="task-instruction"
              rows={3}
              value={instruction}
              onChange={(e) => setInstruction(e.target.value)}
              placeholder={t("tasks.instructionPlaceholder")}
            />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label>{t("tasks.fieldScanMode")}</Label>
              <Select value={scanMode} onValueChange={setScanMode}>
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="single">{t("newScan.modeSingle")}</SelectItem>
                  <SelectItem value="wildcard">{t("newScan.modeWildcard")}</SelectItem>
                  <SelectItem value="dast">{t("newScan.modeDast")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label>{t("tasks.fieldIntensity")}</Label>
              <Select value={intensity} onValueChange={setIntensity}>
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="active">{t("newScan.intensityActive")}</SelectItem>
                  <SelectItem value="passive">{t("newScan.intensityPassive")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="space-y-2 rounded-md border border-border p-3">
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={objectiveOn}
                onChange={(e) => setObjectiveOn(e.target.checked)}
                className="h-4 w-4 rounded border-border bg-input accent-primary"
              />
              {t("tasks.fieldObjective")}
            </label>
            {objectiveOn && (
              <div className="space-y-3 pt-1">
                <p className="text-xs text-muted-foreground">
                  {t("tasks.objectiveHint")}
                </p>
                {goalNotes && (
                  <p className="rounded-sm border border-border/60 bg-muted/40 px-2 py-1 text-[11px] text-muted-foreground">
                    {goalNotes}
                  </p>
                )}
                <div className="grid grid-cols-2 gap-3">
                  <div className="space-y-1.5">
                    <Label>{t("tasks.objectiveKind")}</Label>
                    <Select
                      value={objectiveKind}
                      onValueChange={(v) => setObjectiveKind(v as ObjectiveKind)}
                    >
                      <SelectTrigger><SelectValue /></SelectTrigger>
                      <SelectContent>
                        <SelectItem value="engagement">{t("tasks.objectiveKindEngagement")}</SelectItem>
                        <SelectItem value="capability">{t("tasks.objectiveKindCapability")}</SelectItem>
                        <SelectItem value="finding_class">{t("tasks.objectiveKindFindingClass")}</SelectItem>
                        <SelectItem value="coverage">{t("tasks.objectiveKindCoverage")}</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                </div>
                <div className="space-y-1.5">
                  <Label>{t("tasks.objectiveStatement")}</Label>
                  <Textarea
                    rows={2}
                    value={objectiveStatement}
                    onChange={(e) => setObjectiveStatement(e.target.value)}
                    placeholder={t("tasks.objectiveStatementPlaceholder")}
                  />
                </div>
                <div className="space-y-2">
                  <Label>{t("tasks.objectiveConstraints")}</Label>
                  <Textarea
                    rows={2}
                    value={constraints}
                    onChange={(e) => setConstraints(e.target.value)}
                    placeholder={t("tasks.objectiveConstraintsPlaceholder")}
                  />
                  <p className="text-[11px] text-muted-foreground">
                    {t("tasks.objectiveConstraintsHint")}
                  </p>
                </div>
                <div className="space-y-2">
                  <Label>{t("tasks.objectiveCriteria")}</Label>
                  {criteria.map((criterion, index) => (
                    <div
                      key={index}
                      className="grid grid-cols-2 gap-2 rounded-sm border border-border/60 p-2"
                    >
                      <Select
                        value={criterion.kind}
                        onValueChange={(v) =>
                          updateCriterion(index, {
                            kind: v as ObjectiveCriterionKind,
                          })
                        }
                      >
                        <SelectTrigger aria-label={t("tasks.objectiveCriterionKind")}>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="finding_class">
                            {t("tasks.objectiveCriterionFindingClass")}
                          </SelectItem>
                          <SelectItem value="capability">
                            {t("tasks.objectiveCriterionCapability")}
                          </SelectItem>
                          <SelectItem value="coverage">
                            {t("tasks.objectiveCriterionCoverage")}
                          </SelectItem>
                        </SelectContent>
                      </Select>
                      {criterion.kind === "coverage" ? (
                        <div className="col-span-2 text-xs text-muted-foreground">
                          {t("tasks.objectiveCriterionCoverageHint")}
                        </div>
                      ) : (
                        <Input
                          value={
                            criterion.kind === "finding_class"
                              ? criterion.vuln_class ?? ""
                              : criterion.capability ?? ""
                          }
                          onChange={(e) =>
                            updateCriterion(
                              index,
                              criterion.kind === "finding_class"
                                ? { vuln_class: e.target.value }
                                : { capability: e.target.value },
                            )
                          }
                          placeholder={
                            criterion.kind === "finding_class"
                              ? t("tasks.objectiveVulnClassPlaceholder")
                              : t("tasks.objectiveCapabilityPlaceholder")
                          }
                        />
                      )}
                      <Select
                        value={criterion.verify_by}
                        onValueChange={(v) =>
                          updateCriterion(index, {
                            verify_by: v as ObjectiveVerifyBy,
                          })
                        }
                      >
                        <SelectTrigger aria-label={t("tasks.objectiveVerifyBy")}>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="deterministic">
                            {t("tasks.objectiveVerifyDeterministic")}
                          </SelectItem>
                          <SelectItem value="verifier">
                            {t("tasks.objectiveVerifyVerifier")}
                          </SelectItem>
                          <SelectItem value="manual">
                            {t("tasks.objectiveVerifyManual")}
                          </SelectItem>
                        </SelectContent>
                      </Select>
                      <div className="flex items-center justify-between gap-2">
                        <label className="flex items-center gap-1.5 text-xs">
                          <input
                            type="checkbox"
                            checked={!!criterion.required}
                            onChange={(e) =>
                              updateCriterion(index, { required: e.target.checked })
                            }
                            className="h-3.5 w-3.5 rounded border-border bg-input accent-primary"
                          />
                          {t("tasks.objectiveRequired")}
                        </label>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => removeCriterion(index)}
                          aria-label={t("tasks.objectiveRemoveCriterion")}
                        >
                          {t("common.delete")}
                        </Button>
                      </div>
                    </div>
                  ))}
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={addCriterion}
                    disabled={criteria.length >= 12}
                  >
                    {t("tasks.objectiveAddCriterion")}
                  </Button>
                </div>
              </div>
            )}
          </div>

          <div className="space-y-2 rounded-md border border-border p-3">
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={scheduleOn}
                onChange={(e) => setScheduleOn(e.target.checked)}
                className="h-4 w-4 rounded border-border bg-input accent-primary"
              />
              {t("tasks.fieldSchedule")}
            </label>
            {scheduleOn && (
              <div className="grid grid-cols-2 gap-3 pt-1">
                <Select
                  value={interval}
                  onValueChange={(v) => setIntervalValue(v as TaskSchedule["interval"])}
                >
                  <SelectTrigger aria-label={t("tasks.fieldSchedule")}><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="hourly">{t("tasks.intervalHourly")}</SelectItem>
                    <SelectItem value="daily">{t("tasks.intervalDaily")}</SelectItem>
                    <SelectItem value="weekly">{t("tasks.intervalWeekly")}</SelectItem>
                    <SelectItem value="monthly">{t("tasks.intervalMonthly")}</SelectItem>
                  </SelectContent>
                </Select>
                <TextInput
                  type="time"
                  value={time}
                  onChange={(e) => setTime(e.target.value)}
                  aria-label={t("tasks.scheduleTime")}
                />
              </div>
            )}
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" size="sm" onClick={() => onOpenChange(false)} disabled={pending}>
            {t("common.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={submit}
            disabled={
              pending ||
              !name.trim() ||
              !goal.trim() ||
              (objectiveOn && !objectiveStatement.trim())
            }
          >
            {editing ? t("common.save") : t("common.create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
