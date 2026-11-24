import { useEffect, useState } from "react";
import { toast } from "sonner";
import { Loader2, Pause, Play } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { api } from "@/api/client";
import {
  usePauseInstance,
  useResumeInstance,
  useStartTaskRun,
} from "@/api/queries";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type { Run, Task } from "@/types/api";

/** 活跃 run 判定：pending（入队等待 admission）也算占用任务。 */
const ACTIVE_STATUSES = new Set(["running", "pending", "paused"]);

/**
 * 任务运行态（用户裁决：一个任务只有一个 run，内容跑过就不再裸跑）。
 *
 *   runnable  → 从未跑 / 已编辑（内容与上次派发不同）→ 「运行」
 *   pending   → 排队等槽位（禁用）
 *   running   → 「暂停」
 *   paused    → 「恢复」
 *   completed → 运行已把双图走完（completion=full），且内容未变 → 不显示运行
 *   unmet     → 运行停下但意图未达成（completion=partial），且内容未变 → 不显示运行
 *
 * 「内容未变」的权威是后端投影 `content_changed_since_run`：它比较的是任务
 * 可运行内容的修订值（目标/指令/结构化目标/阶段策略/上下文文件/模型链），
 * 不是时间戳——改名、改定时、改审批模式都不解锁重跑。终态判定也不看
 * failed：任务只有「已完成」和「未达成意图」两种读法。
 */
export type TaskRunState =
  | "runnable"
  | "completed"
  | "unmet"
  | "pending"
  | "running"
  | "paused";

export function taskRunIsActive(state: TaskRunState): boolean {
  return state === "running" || state === "pending" || state === "paused";
}

export function taskRunState(task: Task, runs?: Run[]): TaskRunState {
  if (runs) {
    const active = runs.find((r) => ACTIVE_STATUSES.has(r.status));
    if (active) return active.status as TaskRunState;
  } else {
    const status = (task.last_run_status ?? "").toLowerCase();
    if (ACTIVE_STATUSES.has(status)) return status as TaskRunState;
  }
  // Settled (or never run): a plain run exists only while the runnable
  // content has not been consumed by a dispatch.
  if (task.content_changed_since_run === false) {
    return task.last_run_completion === "full" ? "completed" : "unmet";
  }
  return "runnable";
}

/** 运行态章：详情页头部与任务列表共用同一派生来源。 */
export function TaskRunStateBadge({
  task,
  runs,
  className,
}: {
  task: Task;
  runs?: Run[];
  className?: string;
}) {
  const { t } = useI18n();
  const state = taskRunState(task, runs);
  const variant =
    state === "running"
      ? "success"
      : state === "pending" || state === "paused" || state === "unmet"
        ? "warning"
        : "muted";
  return (
    <Badge variant={variant} className={cn("shrink-0 gap-1", className)}>
      {state === "running" && (
        <span className="size-1 rounded-full bg-emerald-500 pulse-dot" aria-hidden />
      )}
      {t(`taskDetail.state.${state}`)}
    </Badge>
  );
}

export function TaskRunButton({ task, runs }: { task: Task; runs?: Run[] }) {
  const { t } = useI18n();
  const startRun = useStartTaskRun();
  const pause = usePauseInstance();
  const resume = useResumeInstance();
  const [pendingAction, setPendingAction] = useState<"pause" | "resume" | null>(
    null,
  );
  // 启动 ack 已确认 run 被接受：立即进入 pending 形态，不等轮询投影回填。
  const [optimisticStart, setOptimisticStart] = useState(false);

  const derived = taskRunState(task, runs);
  // 乐观态只在真实投影尚未显示活跃 run 时兜底（轮询/WS 桥会很快收敛）。
  // 旧投影此刻可能仍读作 completed/unmet，同样要盖成排队中。
  const state =
    optimisticStart && !taskRunIsActive(derived) ? "pending" : derived;

  useEffect(() => {
    if (taskRunIsActive(derived)) setOptimisticStart(false);
  }, [derived]);

  const busy =
    startRun.isPending || pause.isPending || resume.isPending || pendingAction !== null;

  async function controlRun(action: "pause" | "resume") {
    setPendingAction(action);
    try {
      const { items } = await api.taskRuns(task.id);
      const expected = action === "pause" ? ["running", "pending"] : ["paused"];
      const active =
        items.find((r) => expected.includes(r.status)) ??
        items.find((r) => r.instance_id);
      if (!active?.instance_id) {
        toast.error(t("toast.runControlFailed"));
        return;
      }
      if (action === "pause") await api.pauseInstance(active.instance_id);
      else await api.resumeInstance(active.instance_id);
      toast.success(action === "pause" ? t("toast.runPaused") : t("toast.runResumed"));
    } catch (err) {
      toast.error(
        `${t("toast.runControlFailed")}: ${err instanceof Error ? err.message : ""}`,
      );
    } finally {
      setPendingAction(null);
    }
  }

  function start() {
    startRun.mutate(
      { id: task.id },
      {
        onSuccess: () => {
          setOptimisticStart(true);
          toast.success(`${t("toast.runStarted")}: ${task.name}`);
        },
        onError: (err: Error) =>
          toast.error(`${t("toast.runFailed")}: ${err.message}`),
      },
    );
  }

  if (state === "running") {
    return (
      <Button
        size={runs ? "default" : "sm"}
        variant="outline"
        disabled={busy}
        onClick={() => void controlRun("pause")}
      >
        {pendingAction === "pause" ? (
          <Loader2 className={runs ? "h-4 w-4 animate-spin" : "h-3.5 w-3.5 animate-spin"} />
        ) : (
          <Pause className={runs ? "h-4 w-4" : "h-3.5 w-3.5"} />
        )}
        {t("tasks.pause")}
      </Button>
    );
  }

  if (state === "paused") {
    return (
      <Button
        size={runs ? "default" : "sm"}
        disabled={busy}
        onClick={() => void controlRun("resume")}
      >
        {pendingAction === "resume" ? (
          <Loader2 className={runs ? "h-4 w-4 animate-spin" : "h-3.5 w-3.5 animate-spin"} />
        ) : (
          <Play className={runs ? "h-4 w-4" : "h-3.5 w-3.5"} />
        )}
        {t("tasks.resume")}
      </Button>
    );
  }

  if (state === "pending") {
    return (
      <Button size={runs ? "default" : "sm"} disabled>
        <Loader2 className={runs ? "h-4 w-4 animate-spin" : "h-3.5 w-3.5 animate-spin"} />
        {t("tasks.pending")}
      </Button>
    );
  }

  // completed / unmet: this content already ran. No plain run button — the
  // operator continues by editing the task (「新目标」/「编辑」), which flips
  // content_changed_since_run back to true.
  if (state !== "runnable") return null;

  return (
    <Button size={runs ? "default" : "sm"} disabled={busy} onClick={start}>
      <Play className={runs ? "h-4 w-4" : "h-3.5 w-3.5"} />
      {t("tasks.run")}
    </Button>
  );
}
