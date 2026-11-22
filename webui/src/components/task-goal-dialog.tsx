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
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Loader2 } from "lucide-react";
import { useStartTaskRun, useUpdateTask } from "@/api/queries";
import { useI18n } from "@/i18n";
import type { Task } from "@/types/api";

/**
 * 一键「新目标」（用户裁决）：上一轮目标跑完后改写目标并立即发起下一次
 * 运行——保存即续驱，一步完成。只暴露目标字段；策略 / 结构化目标 /
 * 定时仍走完整编辑弹窗（列表页「编辑」）。目标挂在 task 上，dispatch 时
 * 快照进新 run（handlers_platform 的既有语义），无需新契约。
 */
export function TaskGoalDialog({
  open,
  onOpenChange,
  task,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  task: Task;
}) {
  const { t } = useI18n();
  const update = useUpdateTask();
  const startRun = useStartTaskRun();
  const [goal, setGoal] = useState("");
  const [phase, setPhase] = useState<"idle" | "saving" | "starting">("idle");

  useEffect(() => {
    if (open) {
      setGoal(task.goal);
      setPhase("idle");
    }
  }, [open, task.goal]);

  const busy = phase !== "idle";

  function submit() {
    const next = goal.trim();
    if (!next || busy) return;
    setPhase("saving");
    update
      .mutateAsync({ id: task.id, req: { goal: next } })
      .then(() => {
        setPhase("starting");
        return startRun.mutateAsync({ id: task.id });
      })
      .then(() => {
        toast.success(`${t("toast.runStarted")}: ${task.name}`);
        onOpenChange(false);
      })
      .catch((err: Error) =>
        toast.error(`${t("toast.runFailed")}: ${err.message}`),
      )
      .finally(() => setPhase("idle"));
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !busy && onOpenChange(v)}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("tasks.newGoalTitle")}</DialogTitle>
          <DialogDescription>{t("tasks.newGoalDescription")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-1.5">
          <Label htmlFor="task-new-goal">{t("tasks.fieldGoal")}</Label>
          <Textarea
            id="task-new-goal"
            rows={3}
            value={goal}
            onChange={(e) => setGoal(e.target.value)}
            placeholder={t("tasks.goalPlaceholder")}
            disabled={busy}
          />
        </div>
        <DialogFooter>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            {t("common.cancel")}
          </Button>
          <Button size="sm" onClick={submit} disabled={busy || !goal.trim()}>
            {phase === "starting" && (
              <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />
            )}
            {t("tasks.saveAndContinue")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
