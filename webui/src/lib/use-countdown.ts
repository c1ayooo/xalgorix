import { useEffect, useState } from "react";

/**
 * Ticking countdown to an RFC3339 deadline, refreshed once per second.
 * Returns null once the deadline has passed (or when absent).
 */
export function useCountdown(deadline?: string): {
  remainingMs: number | null;
  expired: boolean;
} {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (!deadline) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [deadline]);

  if (!deadline) return { remainingMs: null, expired: false };
  const remainingMs = new Date(deadline).getTime() - now;
  return { remainingMs, expired: remainingMs <= 0 };
}

/** "mm:ss" (under an hour) or "h:mm:ss" formatting for a millisecond span. */
export function formatCountdown(ms: number): string {
  const totalSec = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(totalSec / 3600);
  const m = Math.floor((totalSec % 3600) / 60);
  const s = totalSec % 60;
  const mm = String(m).padStart(2, "0");
  const ss = String(s).padStart(2, "0");
  return h > 0 ? `${h}:${mm}:${ss}` : `${mm}:${ss}`;
}
