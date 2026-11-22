import type { VulnSummary } from "@/types/api";
import { normalizeSeverity } from "@/lib/utils";

/**
 * A finding flattened across all scans, augmented with the owning scan's
 * identity so the row can be linked to its scan and deleted in place.
 */
export interface FlatFinding extends VulnSummary {
  scan_id: string;
  scan_target: string;
  scan_started_at: string;
}

/**
 * Numeric ordering for severities. critical > high > medium > low > info.
 */
export function severityRank(sev: string): number {
  const s = normalizeSeverity(sev);
  switch (s) {
    case "critical":
      return 4;
    case "high":
      return 3;
    case "medium":
      return 2;
    case "low":
      return 1;
    default:
      return 0;
  }
}

/**
 * Collapse a value the same way the server's `normalizeSummaryPart` does
 * (lowercase, trim, collapse inner whitespace) so client and server dedup keys
 * agree on real payloads — not just on well-formed ones.
 */
function normalizeKeyPart(value?: string): string {
  return (value ?? "").toLowerCase().trim().split(/\s+/).filter(Boolean).join(" ");
}

/**
 * Deduplicate findings across all scans by the finding's content identity:
 * (title, target, endpoint, method, cve) — mirroring the server's
 * `findingContentKey`.
 *
 * Severity is deliberately excluded: the report gate may recompute it (CVSS
 * recalc / severity cap), so keying on it used to keep one row per rating for
 * the same finding while the scan record held a single entry. The surviving row
 * is the most recent one (so delete and navigation point at the latest scan that
 * produced the finding).
 *
 * Output is sorted by severity rank desc, then `scan_started_at` desc.
 */
export function dedupFindings(findings: FlatFinding[]): FlatFinding[] {
  const map = new Map<string, FlatFinding>();
  for (const f of findings) {
    const key =
      `${normalizeKeyPart(f.title)}|` +
      `${normalizeKeyPart(f.target || f.scan_target)}|` +
      `${normalizeKeyPart(f.endpoint)}|` +
      `${normalizeKeyPart(f.method)}|` +
      `${normalizeKeyPart(f.cve)}`;
    const existing = map.get(key);
    if (!existing || (f.scan_started_at ?? "") > (existing.scan_started_at ?? "")) {
      map.set(key, f);
    }
  }
  const out = Array.from(map.values());
  out.sort((a, b) => {
    const sevDiff = severityRank(b.severity) - severityRank(a.severity);
    if (sevDiff !== 0) return sevDiff;
    return (b.scan_started_at ?? "").localeCompare(a.scan_started_at ?? "");
  });
  return out;
}
