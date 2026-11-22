import { useMemo } from "react";

export interface RunRef {
  id: string;
  instance_id?: string;
  status: string;
}

/** 最新活跃 Run：running 优先，其次任意带实例的 run，最后兜底第一条。 */
export function pickLatestRun<T extends RunRef>(runs: T[]): T | undefined {
  return (
    runs.find((r) => r.status === "running") ??
    runs.find((r) => r.instance_id) ??
    runs[0]
  );
}

/**
 * 会话选择（对话页与任务详情页共用，4zreco sessions-tab 的深链语义）：
 * URL 上的 run 参数是唯一权威——合法则选中，非法/缺省回退最新活跃并自动
 * 跟随；手动选择即写回 URL，跟随随之停止。同一份启发式两页零分叉。
 */
export function useRunSelection(
  runs: RunRef[],
  urlRun: string | null | undefined,
  onUrlRun: (instanceId: string | undefined) => void,
) {
  const latest = useMemo(() => pickLatestRun(runs), [runs]);
  const urlValid = !!urlRun && runs.some((r) => r.instance_id === urlRun);
  const selectedInstanceId = (urlValid ? urlRun : latest?.instance_id) ?? undefined;
  return {
    selectedInstanceId,
    /** 最新活跃 run 的 instance_id（无 URL 参数时的自动跟随目标）。 */
    latestRunId: latest?.instance_id,
    selectRun: onUrlRun,
  };
}
