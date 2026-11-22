import { useEffect, useMemo, useState } from "react";
import { useEventsStore } from "@/store/events";

/**
 * 实例级事件活跃度 —— 诚实流式状态的数据源。返回该实例（含 agent 通道）
 * 最近一次事件的客户端接收时刻；本页会话内尚未收到过任何事件时为 null。
 * 调用方据此区分「流式中 / 停滞 / 未观测」三态，而不是拿 running 投影
 * 冒充流式（running 只说明 run 未结束，不说明有数据在流动）。
 * 内置 2s 重渲染节拍，让年龄随时间自然增长。
 */
export function useInstanceActivity(instanceId: string | undefined): number | null {
  const events = useEventsStore((s) => s.events);
  const [, setTick] = useState(0);
  const lastAt = useMemo(() => {
    let latest = 0;
    for (const e of events) {
      if (e.instance_id !== instanceId && e.agent_id !== instanceId) continue;
      const at = e._receivedAt || 0;
      if (at > latest) latest = at;
    }
    return latest > 0 ? latest : null;
  }, [events, instanceId]);
  useEffect(() => {
    const t = setInterval(() => setTick((n) => n + 1), 2000);
    return () => clearInterval(t);
  }, []);
  return lastAt;
}
