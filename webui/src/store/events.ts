import { create } from "zustand";
import type { WSEvent } from "@/types/api";

export type WSStatus =
  | "idle"
  | "connecting"
  | "connected"
  | "disconnected"
  | "reconnecting";

/** 正在生成的 assistant 消息（delta 累积缓冲，done 后固化为事件）。 */
export interface StreamingMessage {
  id: string;
  text: string;
  agent_id?: string;
  instance_id?: string;
  done: boolean;
  updatedAt: number;
}

export interface FeedEvent extends WSEvent {
  // client-side dedupe key + ordering
  _key: string;
  _receivedAt: number;
}

/**
 * run 生命周期事件（实例域，后端 broadcastDashboard 对未订阅客户端全局广播）。
 * 驱动「WS→查询失效」桥（lib/use-lifecycle-refetch），让运行按钮状态机与
 * 任务投影秒级跟随，轮询节奏只作兜底（UI_DESIGN §9 契约不变）。
 */
const LIFECYCLE_EVENT_TYPES = new Set([
  "instance_started",
  "instance_updated",
  "paused",
  "resumed",
  "stopped",
  "queue_started",
  "queue_finished",
  // R-goal 第 0 轮目标分析完成：任务 objective 被引擎落盘，["task"] 前缀
  // 失效让达成度面板/任务列表秒级跟随（轮询只作兜底）。
  "goal_analyzed",
]);

export type LifecycleListener = (e: WSEvent) => void;

interface StreamState {
  status: WSStatus;
  lastError?: string;
  events: FeedEvent[];
  paused: boolean;
  streaming: Record<string, StreamingMessage>;
  maxEvents: number;
  subscribedInstance?: string;
  lifecycleListeners: Set<LifecycleListener>;

  // actions
  setStatus: (s: WSStatus, err?: string) => void;
  pushEvent: (e: WSEvent) => void;
  clearEvents: () => void;
  setPaused: (p: boolean) => void;
  subscribe: (instanceId: string) => void;
  unsubscribe: () => void;
  /** 订阅 run 生命周期事件（实例状态机流转）；返回退订函数。 */
  onLifecycle: (cb: LifecycleListener) => () => void;

  // internals
  _source?: EventSource;
  connect: () => void;
  disconnect: () => void;
  /** 关闭当前连接并按 subscribedInstance 重建（SSE 过滤走 URL 参数）。 */
  _reopen: () => void;
}

export function parsedEventTimestampMs(timestamp?: string): number | undefined {
  if (!timestamp) return undefined;
  const ms = Date.parse(timestamp);
  return Number.isFinite(ms) ? ms : undefined;
}

export function eventOrderMs(event: FeedEvent): number {
  return parsedEventTimestampMs(event.timestamp) ?? event._receivedAt ?? 0;
}

export function eventDedupeKey(e: WSEvent, fallbackTs = 0): string {
  const ts = e.timestamp || String(fallbackTs || "");
  const tool = e.tool_name || "";
  const msg = e.content || e.output || e.error || "";
  const instance = e.instance_id || "";
  // Truncate message so the dedupe key stays small
  return `${ts}|${instance}|${e.type}|${tool}|${msg.slice(0, 80)}`;
}

export function toFeedEvent(
  e: WSEvent,
  source = "event",
  index = 0,
): FeedEvent {
  const receivedAt = parsedEventTimestampMs(e.timestamp) ?? Date.now();
  const dedupeKey = eventDedupeKey(e, receivedAt);
  return {
    ...e,
    _key: `${source}:${index}:${dedupeKey}`,
    _receivedAt: receivedAt,
  };
}

export function mergeFeedEvents(...streams: FeedEvent[][]): FeedEvent[] {
  const seen = new Set<string>();
  const out: FeedEvent[] = [];
  for (const events of streams) {
    for (const event of events) {
      const key = eventDedupeKey(event, event._receivedAt);
      if (seen.has(key)) continue;
      seen.add(key);
      out.push(event);
    }
  }
  return out.sort((a, b) => {
    const byTime = eventOrderMs(a) - eventOrderMs(b);
    if (byTime !== 0) return byTime;
    const byReceived = (a._receivedAt || 0) - (b._receivedAt || 0);
    if (byReceived !== 0) return byReceived;
    return a._key.localeCompare(b._key);
  });
}

// SSE 事件流端点（原 /ws WebSocket 于 2026-11-22 移除）。同源相对路径：
// EventSource 自动携带 session cookie，重连自动回传 Last-Event-ID，
// 服务端按 seq 从环形缓冲补放断线窗口（internal/web/sse_hub.go）。
const STREAM_URL = "/api/stream";

// The server emits `event: <WSEvent.type>`. EventSource.onmessage only receives
// unnamed/message frames, so every named contract event needs an explicit
// listener. Keep this registry in sync with API_CONTRACT §0 when adding types.
const NAMED_STREAM_EVENT_TYPES = [
  "instance_started",
  "instance_updated",
  "queue_started",
  "queue_finished",
  "target_started",
  "target_completed",
  "subdomains_discovered",
  "scan_started",
  "paused",
  "resumed",
  "stopped",
  "report_ready",
  "error",
  "finished",
  "approval_requested",
  "approval_decided",
  "approval_rule_hit",
  "approval_rules_changed",
  "guidance_injected",
  "guidance_queued",
  "guidance_consumed",
  "chat_question",
  "chat_reply",
  "goal_analyzed",
  "skills_changed",
  "agent_profiles_changed",
  "mcp_servers_changed",
  "assistant",
  "assistant_delta",
  "assistant_done",
  "thinking",
  "thought",
  "phase",
  "decision",
  "recovery",
  "tool_call",
  "tool_result",
  "tool_output",
  "tool_error",
  "vuln",
  "vuln_found",
  "vulns",
  "http",
  "request",
  "response",
  "llm",
  "token",
  "llm_output",
] as const;

export const useEventsStore = create<StreamState>((set, get) => ({
  status: "idle",
  events: [],
  streaming: {},
  paused: false,
  maxEvents: 1000,
  lifecycleListeners: new Set<LifecycleListener>(),

  setStatus: (s, err) => set({ status: s, lastError: err }),

  pushEvent: (e) => {
    if (get().paused) return;
    // 生命周期事件旁路给查询失效桥（与事件流同受 paused 丢弃契约约束）。
    if (LIFECYCLE_EVENT_TYPES.has(e.type)) {
      for (const cb of get().lifecycleListeners) {
        try {
          cb(e);
        } catch {
          /* listener 异常不阻断事件流 */
        }
      }
    }
    // 对话流：assistant_delta 累积进缓冲（不进事件数组）；
    // assistant_done 用全文固化为一条 assistant 事件（4zreco transcript 模式：
    // 人工气泡右对齐、assistant 气泡左对齐，工具行穿插其间）。
    if (e.type === "assistant_delta" && e.message_id) {
      const msgId: string = e.message_id;
      const delta = e.delta ?? "";
      const agentId = e.agent_id;
      const instanceId = e.instance_id;
      set((state) => {
        const prev = state.streaming[msgId];
        return {
          streaming: {
            ...state.streaming,
            [msgId]: {
              id: msgId,
              text: (prev?.text ?? "") + delta,
              agent_id: agentId,
              instance_id: instanceId,
              done: false,
              updatedAt: Date.now(),
            },
          },
        };
      });
      return;
    }
    if (e.type === "assistant_done" && e.message_id) {
      const doneId: string = e.message_id;
      set((state) => {
        const streaming = { ...state.streaming };
        delete streaming[doneId];
        return { streaming };
      });
      const receivedAt = Date.now();
      const key = eventDedupeKey(e, receivedAt);
      set((state) => {
        // 归一 type 为 assistant：气泡渲染分支按此匹配。
        const next: FeedEvent = {
          ...e,
          type: "assistant",
          _key: key,
          _receivedAt: receivedAt,
        };
        const events = [...state.events, next];
        if (events.length > state.maxEvents) {
          events.splice(0, events.length - state.maxEvents);
        }
        return { events };
      });
      return;
    }
    const receivedAt = Date.now();
    const key = eventDedupeKey(e, receivedAt);
    set((state) => {
      // dedupe by key (last 200 keys are enough)
      const recent = state.events.slice(-200);
      if (recent.some((x) => x._key === key)) return state;
      const next: FeedEvent = { ...e, _key: key, _receivedAt: receivedAt };
      const events = [...state.events, next];
      // cap size
      if (events.length > state.maxEvents) {
        events.splice(0, events.length - state.maxEvents);
      }
      return { events };
    });
  },

  clearEvents: () => set({ events: [], streaming: {} }),
  setPaused: (p) => set({ paused: p }),

  onLifecycle: (cb) => {
    get().lifecycleListeners.add(cb);
    return () => {
      get().lifecycleListeners.delete(cb);
    };
  },

  subscribe: (instanceId) => {
    set({ subscribedInstance: instanceId });
    get()._reopen();
  },

  unsubscribe: () => {
    set({ subscribedInstance: undefined });
    get()._reopen();
  },

  connect: () => {
    if (typeof window === "undefined") return;
    const existing = get()._source;
    if (existing && existing.readyState !== EventSource.CLOSED) {
      return;
    }

    set({ status: "connecting", lastError: undefined });

    // 实例过滤等价于原 WS 的 subscribe 帧：直接体现在 URL 上，重连由
    // EventSource 原生负责（自动回传 Last-Event-ID，服务端补放缺口）。
    const inst = get().subscribedInstance;
    const url = inst
      ? `${STREAM_URL}?instance=${encodeURIComponent(inst)}`
      : STREAM_URL;
    let source: EventSource;
    try {
      source = new EventSource(url);
    } catch (err) {
      set({
        status: "disconnected",
        lastError: err instanceof Error ? err.message : "EventSource error",
      });
      return;
    }

    source.onopen = () => {
      set({ status: "connected", lastError: undefined });
    };

    const handleEvent = (ev: MessageEvent) => {
      try {
        const data = JSON.parse(ev.data) as WSEvent;
        get().pushEvent(data);
      } catch {
        // ignore malformed events
      }
    };
    source.onmessage = handleEvent;
    for (const type of NAMED_STREAM_EVENT_TYPES) {
      source.addEventListener(type, handleEvent);
    }

    source.onerror = () => {
      // EventSource 原生自动重连：CONNECTING = 重连已排队；CLOSED 才是
      // 真断（浏览器放弃或服务端拒绝）。
      if (source.readyState === EventSource.CLOSED) {
        set({ status: "disconnected", lastError: "Connection error" });
        source.close();
        if (get()._source === source) set({ _source: undefined });
      } else {
        set({ status: "reconnecting", lastError: "Connection lost" });
      }
    };

    set({ _source: source });
  },

  disconnect: () => {
    const source = get()._source;
    if (source) {
      source.onopen = null;
      source.onmessage = null;
      source.onerror = null;
      source.close();
    }
    set({
      _source: undefined,
      status: "idle",
    });
  },

  _reopen: () => {
    const source = get()._source;
    if (!source) {
      // 尚未连接（connect 由 AppShell 挂载时统一调用）：只记录过滤条件，
      // connect() 会带上。
      return;
    }
    source.onopen = null;
    source.onmessage = null;
    source.onerror = null;
    source.close();
    set({ _source: undefined });
    get().connect();
  },
}));

/**
 * Select only the events relevant to a given scan instance.
 * Backend broadcasts events keyed to the subscribed instance, but dashboard
 * events have agent_id set to the originating scan when available.
 */
export function filterEventsForInstance(
  events: FeedEvent[],
  instanceId?: string,
): FeedEvent[] {
  if (!instanceId) return events;
  return events.filter((e) => {
    if (e.type === "instance_started" || e.type === "instance_updated") {
      return false;
    }
    if (e.instance_id) {
      return e.instance_id === instanceId;
    }
    if (e.agent_id) {
      return e.agent_id === instanceId;
    }
    return false;
  });
}
