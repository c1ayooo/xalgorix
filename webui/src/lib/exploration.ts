import type { ExplorationKind, ExplorationNode } from "@/types/api";

/**
 * 探索投影的共享视觉语义（拓扑图 + 播报板双视图唯一定义处）。
 * 色值语义：假设六态沿用账本章语义；severity 固定色（UI_DESIGN §5.1 禁改）；
 * evidence observed=sky / inferred=violet。
 */

/** 假设六态点色（proven=emerald / testing=amber / 其余=slate）。 */
export const HYP_STATE_DOT: Record<string, string> = {
  queued: "#94a3b8",
  testing: "#f59e0b",
  proven: "#10b981",
  rejected: "#94a3b8",
  blocked: "#94a3b8",
  exhausted: "#94a3b8",
};

/** severity 固定色。 */
export const SEVERITY_DOT: Record<string, string> = {
  critical: "#dc2626",
  high: "#ea580c",
  medium: "#ca8a04",
  low: "#2563eb",
  info: "#525252",
};

const KIND_DOT: Record<ExplorationKind, string> = {
  goal: "#10b981",
  agent: "#8b5cf6",
  hypothesis: "#94a3b8",
  evidence: "#0ea5e9",
  finding: "#ea580c",
  observation: "#0ea5e9",
};

export function explorationDot(node: ExplorationNode): string {
  if (node.kind === "hypothesis") {
    return HYP_STATE_DOT[node.state ?? ""] ?? KIND_DOT.hypothesis;
  }
  if (node.kind === "finding") {
    return SEVERITY_DOT[(node.severity ?? "").toLowerCase()] ?? KIND_DOT.finding;
  }
  if ((node.kind === "evidence" || node.kind === "observation") && node.nature === "inferred") {
    return "#8b5cf6";
  }
  return KIND_DOT[node.kind];
}

/** hyp:H-1 / ev:EV-3 / finding:XALG-7 → 去前缀的 mono 短 id。 */
export function explorationShortID(node: ExplorationNode): string {
  const i = node.id.indexOf(":");
  return i >= 0 ? node.id.slice(i + 1) : node.id;
}

/** 板的 kind 过滤组（evidence 与 observation 同属"事实"）。 */
export type ExplorationFilter = "all" | "goal" | "agent" | "hypothesis" | "fact" | "finding";

export function matchesExplorationFilter(node: ExplorationNode, filter: ExplorationFilter): boolean {
  if (filter === "all") return true;
  if (filter === "fact") return node.kind === "evidence" || node.kind === "observation";
  return node.kind === filter;
}
