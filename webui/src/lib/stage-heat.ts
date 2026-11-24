import type { InfoType } from "@/types/api";

/**
 * Loop stages in CORE order. The map colours a subject by the furthest ring it
 * reached: observation → inference → belief → action (intent) → evidence
 * (fact) → verdict (finding). "Cold" means only observations; "hot" means the
 * subject reached a verdict/finding.
 */
export const LOOP_STAGES: InfoType[] = [
  "observation",
  "inference",
  "belief",
  "intent",
  "fact",
  "finding",
];

export const STAGE_LEVEL: Record<InfoType, StageLevel> = {
  observation: 1,
  inference: 2,
  belief: 3,
  intent: 4,
  fact: 5,
  finding: 6,
};

export type StageLevel = 1 | 2 | 3 | 4 | 5 | 6;

/** Furthest stage reached, from the per-stage counts. */
export function stageOf(
  counts: Partial<Record<InfoType, number>> | undefined,
): StageLevel {
  if (!counts) return 1;
  if ((counts.finding ?? 0) > 0) return 6;
  if ((counts.fact ?? 0) > 0) return 5;
  if ((counts.intent ?? 0) > 0) return 4;
  if ((counts.belief ?? 0) > 0) return 3;
  if ((counts.inference ?? 0) > 0) return 2;
  return 1;
}

/**
 * Data-visualisation scale (not chrome): cold blue → cyan → amber → orange →
 * red. Text colour flips to stay readable on the amber stops. The hottest stop
 * is the brand token so the "reached a finding" case reads as the product
 * accent.
 */
export const STAGE_STYLE: Record<
  StageLevel,
  { bg: string; fg: string; labelKey: string }
> = {
  1: {
    bg: "oklch(0.32 0.03 245)",
    fg: "oklch(0.90 0.02 245)",
    labelKey: "info.stage.observation",
  },
  2: {
    bg: "oklch(0.40 0.07 220)",
    fg: "oklch(0.96 0.02 220)",
    labelKey: "info.stage.inference",
  },
  3: {
    bg: "oklch(0.60 0.11 85)",
    fg: "oklch(0.14 0.03 85)",
    labelKey: "info.stage.belief",
  },
  4: {
    bg: "oklch(0.68 0.14 60)",
    fg: "oklch(0.14 0.04 60)",
    labelKey: "info.stage.intent",
  },
  5: {
    bg: "oklch(0.56 0.18 35)",
    fg: "oklch(0.99 0.01 35)",
    labelKey: "info.stage.fact",
  },
  6: {
    bg: "var(--brand)",
    fg: "var(--brand-foreground)",
    labelKey: "info.stage.finding",
  },
};

export function stageStyle(level: StageLevel) {
  return STAGE_STYLE[level];
}
