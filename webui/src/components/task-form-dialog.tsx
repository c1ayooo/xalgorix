import { useEffect, useState } from "react";
import { toast } from "sonner";
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
import { useI18n } from "@/i18n";
import type { Task, TaskSchedule } from "@/types/api";

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
  }, [open, task]);

  function submit() {
    if (!name.trim() || !goal.trim()) return;
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
            <Label htmlFor="task-goal">{t("tasks.fieldGoal")}</Label>
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
          <Button size="sm" onClick={submit} disabled={pending || !name.trim() || !goal.trim()}>
            {editing ? t("common.save") : t("common.create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
