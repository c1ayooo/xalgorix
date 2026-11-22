/**
 * Shared guidance-composer primitives (contract §3.1). One human intent turn
 * = one POST with a fresh request_id; retries of the same request_id return
 * the first outcome, never a duplicate turn.
 */
export const MAX_GUIDANCE_CHARS = 4000;

export function newRequestId(): string {
  return typeof crypto !== "undefined" && "randomUUID" in crypto
    ? crypto.randomUUID()
    : `guidance-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
}
