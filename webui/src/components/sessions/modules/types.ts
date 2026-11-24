import type { ReactNode } from "react";
import type { FeedEvent } from "@/store/events";
import type { InstanceAgentsView, Run, ScanPlanView } from "@/types/api";

/** 七环归属（宪章 §2）：每个模块必须回答它落在哪一环。 */
export type ModuleRing = 1 | 2 | 3 | 4 | 5 | 6 | 7;

/** 已登记的会话模块 id（左栏分组图标按它选择）。 */
export type SessionsModuleId = "plan" | "decision" | "obligation" | "workers" | "system";

/**
 * 字段权威（宪章 §3.5）：模块展示的数据由谁说了算。
 * engine=引擎状态 / ledger=账本 / report=报告门 / telemetry=事件 / human=人工输入。
 */
export type ModuleAuthority = "engine" | "ledger" | "report" | "telemetry" | "human";

/** 模块占用槽位：rail=左栏导航项，main=右栏主体（两者可同时占用）。 */
export type ModuleSlot = "rail" | "main";

/** 同一次渲染内所有模块共享的数据上下文（一次查询、多模块消费）。 */
export interface ModuleData {
  run?: Run;
  instanceId?: string;
  running: boolean;
  /** 当前 ?agent= 值：undefined=主会话；模块 id；worker 泳道 unit:<unit_id> 或 legacy agent_id。 */
  lane?: string;
  plan?: ScanPlanView;
  planLoading: boolean;
  planError: boolean;
  agents?: InstanceAgentsView;
  agentsLoading: boolean;
  agentsError: boolean;
  feedEvents: FeedEvent[];
  /** 写回 ?agent=（深链）：worker 行点击后查看该 agent 的转录。 */
  onSelectLane: (lane?: string) => void;
}

/** 左栏子行：worker 组按「一个工作单元一行」展开，点击直达该 unit 的转录。 */
export interface RailChild {
  /** ?agent= 深链值（unit:<unit_id>；legacy spawn 为 agent_id）。 */
  id: string;
  label: string;
  /** 行尾的稳定标识（unit_id / agent_id），等宽小字。 */
  meta?: string;
  /** queued | running | paused | completed | failed —— 决定状态点。 */
  status?: string;
  active?: boolean;
}

/** 左栏导航项（由模块自己决定是否出现、徽标与活跃状态）。 */
export interface RailEntry {
  labelKey: string;
  /** 计数徽标，例如 "2/3" 或 "排队 1"；undefined 不渲染。 */
  badge?: string;
  live?: boolean;
  /** 组内子行（Workers 组：每个 worker 一行，含历史）。 */
  children?: RailChild[];
}

export interface ContentModule {
  /** 稳定 id：同时是 ?agent= 深链值（worker 泳道用 agent_id）。 */
  id: string;
  slots: ModuleSlot[];
  /** 左栏排序（升序）。 */
  order: number;
  ring: ModuleRing;
  authority: ModuleAuthority;
  labelKey: string;
  /** 主区标题 i18n key（缺省用 labelKey）。 */
  titleKey?: string;
  /** 是否参与 ?agent= 深链。 */
  deepLink: boolean;
  /** 不适用（无 Run / 无权限）时隐藏；"引擎没产生"必须显示空态，不隐藏。 */
  visible: (data: ModuleData) => boolean;
  rail?: (data: ModuleData) => RailEntry;
  render: (data: ModuleData) => ReactNode;
}
