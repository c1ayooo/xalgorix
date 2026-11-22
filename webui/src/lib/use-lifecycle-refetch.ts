import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useEventsStore } from "@/store/events";
import { qk } from "@/api/queries";

/**
 * WS→查询失效桥：run 生命周期事件（实例状态机流转，后端对未订阅客户端
 * 全局广播）到达时失效任务/实例投影查询，运行按钮状态机与列表状态秒级
 * 跟随。轮询节奏保持不变（UI_DESIGN §9），本桥只做「提前」；1.5s 尾沿
 * 节流防事件风暴。挂载一次（AppShell）。
 */
const REFETCH_THROTTLE_MS = 1500;

export function useLifecycleRefetch() {
  const qc = useQueryClient();
  const onLifecycle = useEventsStore((s) => s.onLifecycle);

  useEffect(() => {
    let last = 0;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const flush = () => {
      last = Date.now();
      timer = null;
      qc.invalidateQueries({ queryKey: qk.projects });
      qc.invalidateQueries({ queryKey: qk.instances });
      qc.invalidateQueries({ queryKey: qk.status });
      // ["task"] 前缀覆盖 task / task runs / task findings / task objective。
      qc.invalidateQueries({ queryKey: ["task"] });
    };
    const unsubscribe = onLifecycle(() => {
      if (timer) return;
      timer = setTimeout(flush, Math.max(0, REFETCH_THROTTLE_MS - (Date.now() - last)));
    });
    return () => {
      unsubscribe();
      if (timer) clearTimeout(timer);
    };
  }, [qc, onLifecycle]);
}
