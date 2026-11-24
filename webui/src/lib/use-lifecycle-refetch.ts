import { useEffect, useRef } from "react";
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
  // 新发现是投影（/api/findings 读持久记录），推送帧本身不带完整列表：
  // 收到带 vulns 的事件就失效发现/任务投影，下一跳拉全量。没有这条桥时，
  // 发现页只能等轮询——标签页被浏览器节流/切走后会长时间停在旧快照上。
  const vulnRevision = useEventsStore((s) => s.vulnRevision);
  const vulnTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const vulnLast = useRef(0);

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

  useEffect(() => {
    if (vulnRevision === 0) return;
    if (vulnTimer.current) return; // a trailing flush is already scheduled
    const flush = () => {
      vulnLast.current = Date.now();
      vulnTimer.current = null;
      // ["findings"] 覆盖 list/summary 的所有项目作用域；["task"] 跟上任务级
      // 发现 tab（run_findings 冷路径投影由服务端在记录变更时重投影）。
      qc.invalidateQueries({ queryKey: ["findings"] });
      qc.invalidateQueries({ queryKey: ["task"] });
    };
    const delay = Math.max(0, REFETCH_THROTTLE_MS - (Date.now() - vulnLast.current));
    vulnTimer.current = setTimeout(flush, delay);
  }, [vulnRevision, qc]);

  useEffect(
    () => () => {
      if (vulnTimer.current) clearTimeout(vulnTimer.current);
    },
    [],
  );
}
