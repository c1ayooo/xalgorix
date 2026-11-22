import { cn } from "@/lib/utils";
import { useI18n } from "@/i18n";

// Known stop reasons only get semantics (color + localized label); anything
// else is the engine's free-form abort reason and renders verbatim in mono —
// never translated, never mapped to a "close enough" bucket. Vocabulary per
// internal/web/scan_session.go + internal/agent/completion.go + D11
// (goal_achieved).
const STYLES: Record<string, { className: string; key: string }> = {
  // D11: the engine judged every required criterion met — the one positive
  // terminal reason.
  goal_achieved: {
    className:
      "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-500/30",
    key: "stopReason.goal_achieved",
  },
  // Neutral: operator- or platform-driven ends, no assessment judgment.
  user_stopped: {
    className:
      "bg-neutral-500/10 text-neutral-600 dark:text-neutral-300 border-neutral-500/30",
    key: "stopReason.user_stopped",
  },
  user_paused: {
    className:
      "bg-neutral-500/10 text-neutral-600 dark:text-neutral-300 border-neutral-500/30",
    key: "stopReason.user_paused",
  },
  server_shutdown: {
    className:
      "bg-neutral-500/10 text-neutral-600 dark:text-neutral-300 border-neutral-500/30",
    key: "stopReason.server_shutdown",
  },
  // Amber: the run ended with obligations left open — must stay visible, not
  // quietly folded into a clean finish.
  finish_gate_exhausted: {
    className:
      "bg-amber-500/10 text-amber-700 dark:text-amber-400 border-amber-500/30",
    key: "stopReason.finish_gate_exhausted",
  },
  goal_unmet: {
    className:
      "bg-amber-500/10 text-amber-700 dark:text-amber-400 border-amber-500/30",
    key: "stopReason.goal_unmet",
  },
  target_unresponsive: {
    className:
      "bg-amber-500/10 text-amber-700 dark:text-amber-400 border-amber-500/30",
    key: "stopReason.target_unresponsive",
  },
  untested_at_finish: {
    className:
      "bg-amber-500/10 text-amber-700 dark:text-amber-400 border-amber-500/30",
    key: "stopReason.untested_at_finish",
  },
};

export function StopReasonBadge({
  reason,
  className,
}: {
  reason?: string;
  className?: string;
}) {
  const { t } = useI18n();
  const raw = (reason || "").trim();
  if (!raw) return null;
  const known = STYLES[raw];
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 whitespace-nowrap rounded-md border px-2 py-0.5 text-[11px] font-medium",
        known
          ? known.className
          : "bg-neutral-500/10 text-neutral-600 dark:text-neutral-300 border-neutral-500/30",
        className,
      )}
    >
      <span className="h-1.5 w-1.5 rounded-full bg-current" aria-hidden />
      {known ? (
        t(known.key)
      ) : (
        <span className="font-mono normal-case tracking-normal">{raw}</span>
      )}
    </span>
  );
}
