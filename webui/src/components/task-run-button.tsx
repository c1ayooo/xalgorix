import { useEffect, useState } from "react";
import { toast } from "sonner";
import { Loader2, Pause, Play } from "lucide-react";
import { Button } from "@/components/ui/button";
import { api } from "@/api/client";
import {
  usePauseInstance,
  useResumeInstance,
  useStartTaskRun,
} from "@/api/queries";
import { useI18n } from "@/i18n";
import type { Run, Task } from "@/types/api";

/** 活跃 run 判定：pending（入队等待 admission）也算占用任务。 */
const ACTIVE_STATUSES = new Set(["running", "pending", "paused"]);

/**
 * 任务运行状态机（用户裁决：任务一次性，点击运行后按钮变暂停）：
 *
 *   idle（无活跃 Run）       → 运行（Play，发起 Run；ack 成功即乐观翻转为排队）
 *   pending（排队等待槽位）   → 排队中（禁用，避免误认为可再发起）
 *   running                  → 暂停（Pause，pause 活跃 Run 的实例）
 *   paused                   → 恢复（Resume，resume 实例）
 *
 * 状态源：优先 runs 投影（详情页传 `runs`），否则 task.last_run_status
 * （列表轮询投影）。状态变化经 WS 生命周期桥 + 既有轮询收敛（UI_DESIGN §9）。
 */
export function taskRunState(task: Task, runs?: Run[]): string {
  if (runs) {
    const active = runs.find((r) => ACTIVE_STATUSES.has(r.status));
    if (active) return active.status;
    return "idle";
  }
  const status = task.last_run_status ?? "";
  return ACTIVE_STATUSES.has(status) ? status : "idle";
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
  const state =
    optimisticStart && derived === "idle" ? "pending" : derived;

  useEffect(() => {
    if (derived !== "idle") setOptimisticStart(false);
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

  return (
    <Button size={runs ? "default" : "sm"} disabled={busy} onClick={start}>
      <Play className={runs ? "h-4 w-4" : "h-3.5 w-3.5"} />
      {t("tasks.run")}
    </Button>
  );
}
