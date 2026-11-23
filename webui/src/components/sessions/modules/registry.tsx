import { RoleEventStream } from "@/components/sessions/role-event-stream";
import { isDecisionEvent, type SessionRole } from "@/components/sessions/role-events";
import { ObligationModule } from "@/components/sessions/modules/obligation-module";
import { PlanModule } from "@/components/sessions/modules/plan-module";
import { WorkersModule } from "@/components/sessions/modules/workers-module";
import type { ContentModule, ModuleData, RailEntry } from "@/components/sessions/modules/types";

/**
 * 会话页内容模块注册表。
 *
 * 设计目标（方案 §3.3）：页面不再被"三个固定角色组"锁死。新增一个模块 =
 * 在这里登记一条记录（id / 七环 / 权威 / 可见性 / 空态 / 渲染），左栏与主区
 * 自动获得入口，不改骨架代码。
 *
 * 纪律：
 *  - 模块默认常驻；只有"不适用"（无 Run、无数据源）才隐藏。
 *  - "引擎没产生"必须显示 absent 空态，不允许分组凭空消失。
 *  - 每个模块必须声明 ring（七环）与 authority（字段权威），见 docs/SESSIONS_MODULE_MAP.md。
 */

function decisionCount(data: ModuleData): number {
  return data.feedEvents.filter(isDecisionEvent).length;
}

const SYSTEM_EVENT_TYPES = new Set([
  "phase",
  "paused",
  "resumed",
  "stopped",
  "error",
  "finished",
  "recovery",
]);

function systemCount(data: ModuleData): number {
  return data.feedEvents.filter((event) => SYSTEM_EVENT_TYPES.has(event.type)).length;
}

function planBadge(data: ModuleData): string | undefined {
  const counters = data.plan?.counters;
  if (!counters?.total) return undefined;
  return `${counters.completed + counters.skipped}/${counters.total}`;
}

function workerBadge(data: ModuleData): string | undefined {
  const view = data.agents;
  if (!view?.available) return undefined;
  const limit = view.concurrency.limit > 0 ? `/${view.concurrency.limit}` : "";
  const queued = view.concurrency.queued > 0 ? ` +${view.concurrency.queued}` : "";
  return `${view.concurrency.running}${limit}${queued}`;
}

function roleModule(role: SessionRole, options: {
  id: string;
  order: number;
  labelKey: string;
  ring: 1 | 2 | 3 | 4 | 5 | 6 | 7;
  count: (data: ModuleData) => number;
}): ContentModule {
  return {
    id: options.id,
    slots: ["rail", "main"],
    order: options.order,
    ring: options.ring,
    authority: "telemetry",
    labelKey: options.labelKey,
    deepLink: true,
    visible: (data) => options.count(data) > 0,
    rail: (data) => {
      const count = options.count(data);
      return { labelKey: options.labelKey, badge: count > 0 ? String(count) : undefined };
    },
    render: (data) => (
      <RoleEventStream instanceId={data.instanceId!} role={role} running={data.running} />
    ),
  };
}

const PLAN_MODULE: ContentModule = {
  id: "plan",
  slots: ["rail", "main"],
  order: 10,
  ring: 2, // 信念环（PlanTask = Intent）
  authority: "engine",
  labelKey: "sessions.groupPlan",
  titleKey: "sessions.planItemTitle",
  deepLink: true,
  visible: (data) => !!data.instanceId,
  rail: (data) => ({ labelKey: "sessions.groupPlan", badge: planBadge(data) }),
  render: (data) => (
    <PlanModule
      view={data.plan}
      loading={data.planLoading}
      error={data.planError}
      running={data.running}
    />
  ),
};

const DECISION_MODULE = roleModule("decision", {
  id: "decision",
  order: 20,
  labelKey: "sessions.groupDecision",
  ring: 2,
  count: decisionCount,
});

const OBLIGATION_MODULE: ContentModule = {
  id: "obligation",
  slots: ["rail", "main"],
  order: 30,
  ring: 3, // 义务环
  authority: "engine",
  labelKey: "sessions.groupObligation",
  titleKey: "sessions.obligationItemTitle",
  deepLink: true,
  visible: (data) => !!data.instanceId && !!data.plan?.present,
  rail: (data) => {
    const unfinished = data.plan?.counters.unfinished ?? 0;
    return { labelKey: "sessions.groupObligation", badge: unfinished > 0 ? String(unfinished) : undefined };
  },
  render: (data) => (
    <ObligationModule plan={data.plan} agents={data.agents} running={data.running} />
  ),
};

const WORKERS_MODULE: ContentModule = {
  id: "workers",
  slots: ["rail", "main"],
  order: 40,
  ring: 4, // 行动环（并行 lane）
  authority: "engine",
  labelKey: "sessions.groupWorker",
  deepLink: true,
  visible: (data) => !!data.instanceId,
  rail: (data) => {
    const view = data.agents;
    // 一个 worker 一行（含已结束的历史行）：组不是"再点一次才看到 worker"
    // 的入口，点击具体行直接打开该 agent 的转录。
    const children = (view?.agents ?? []).map((worker) => ({
      id: worker.agent_id,
      label: worker.lane || worker.task || worker.agent_id,
      meta: worker.agent_id,
      status: worker.status,
      active: data.lane === worker.agent_id,
    }));
    return {
      labelKey: "sessions.groupWorker",
      badge: workerBadge(data),
      live: (view?.concurrency.running ?? 0) > 0,
      children,
    };
  },
  render: (data) => (
    <WorkersModule
      view={data.agents}
      loading={data.agentsLoading}
      error={data.agentsError}
      onSelectLane={data.onSelectLane}
    />
  ),
};

const SYSTEM_MODULE = roleModule("system", {
  id: "system",
  order: 50,
  labelKey: "sessions.groupSystem",
  ring: 5, // 证据/审计投影
  count: systemCount,
});

/** 注册顺序即左栏顺序（order 升序）。 */
export const SESSIONS_MODULES: readonly ContentModule[] = [
  PLAN_MODULE,
  DECISION_MODULE,
  OBLIGATION_MODULE,
  WORKERS_MODULE,
  SYSTEM_MODULE,
];

/** 当前数据下应显示在左栏的模块（按 order 排序）。 */
export function railModules(data: ModuleData): { module: ContentModule; entry: RailEntry }[] {
  return SESSIONS_MODULES.filter((module) => module.slots.includes("rail") && module.visible(data))
    .sort((a, b) => a.order - b.order)
    .map((module) => ({ module, entry: module.rail?.(data) ?? { labelKey: module.labelKey } }));
}

/** ?agent= 值 → 模块（worker 泳道返回 undefined，由转录按 agent_id 过滤）。 */
export function findModule(lane?: string): ContentModule | undefined {
  if (!lane) return undefined;
  return SESSIONS_MODULES.find((module) => module.id === lane);
}

/**
 * ?agent= 是否是 worker 泳道（agent_id）而非模块 id。
 * 用于让左栏 Workers 组在查看具体 worker 时保持高亮。
 */
export function isWorkerLane(lane?: string): boolean {
  if (!lane) return false;
  return !SESSIONS_MODULES.some((module) => module.id === lane);
}
