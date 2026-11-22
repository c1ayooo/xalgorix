import type { FeedEvent } from "@/store/events";

/**
 * Restrict the live SSE store to the events of one project's runs.
 *
 * `/api/stream` 没有 project 维度（服务端只支持 `?instance=` 过滤），而 store 是
 * 全进程共享的：不裁剪时，其他项目的运行事件会混进当前项目的实时动态/首页
 * 动态流，同时本项目的发现却仍按项目过滤——两处口径互相矛盾。
 *
 * 归属判定只看 `instance_id`：`agent_id` 在委派场景下是子 agent 标识（形如
 * `<instance>/<agent>`），拿它匹配实例 id 会误删本项目的合法事件。没有
 * `instance_id` 的事件（审批、guidance、run 生命周期广播）不属于任何一次运行，
 * 保留。
 *
 * 取舍：`projectInstanceIds` 来自 `/api/instances?project_id=` 的内存实例列表，
 * 实例被逐出内存后其迟到事件会被丢弃。实时动态本来就只面向活跃运行，这个损失
 * 可接受；已完成运行的事件历史请走 run 详情/任务详情。
 *
 * 空集合（实例列表尚未返回、或该项目确实没有实例）时不做裁剪——宁可多显示，
 * 也不要把整个事件流清空。
 */
export function scopeEventsToProject(
  events: FeedEvent[],
  projectInstanceIds: ReadonlySet<string>,
): FeedEvent[] {
  if (projectInstanceIds.size === 0) return events;
  return events.filter(
    (event) => !event.instance_id || projectInstanceIds.has(event.instance_id),
  );
}
