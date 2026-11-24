package reporting

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/evidence"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// reportEvidence is the evidence a report call explicitly names. It is the
// model-facing contract: without at least one resolvable reference the report
// is rejected (only an engine-authoritative proof may attach refs itself).
type reportEvidence struct {
	Refs     []scanctx.TrafficRef
	Outputs  []string
	Explicit bool
	Skipped  []string
	// CaptureUnavailable means this run's MITM archive holds no records at all,
	// so no reference could ever resolve. The gate accepts the finding and
	// records the gap instead of silently losing every report in an environment
	// where capture is off or broken (charter: a real finding is never lost).
	CaptureUnavailable bool
}

// resolveReportEvidence parses and validates traffic_refs / evidence_outputs.
// It returns a non-empty problem string when the caller must reject the
// report. Missing/invalid individual refs are reported in Skipped so a typo in
// one id never discards the other valid references.
func resolveReportEvidence(contextID, scanDir, refsRaw, outputsRaw string) (reportEvidence, string) {
	var out reportEvidence
	if strings.TrimSpace(refsRaw) != "" || strings.TrimSpace(outputsRaw) != "" {
		out.Explicit = true
	}
	base := filepath.Base(filepath.Clean(scanDir))
	if refs, problems := evidence.ParseTrafficRefs(scanDir, refsRaw); len(refs) > 0 || len(problems) > 0 {
		for i := range refs {
			if refs[i].Session == "" {
				refs[i].Session = base
			}
		}
		out.Refs = refs
		out.Skipped = append(out.Skipped, problems...)
	}
	for _, id := range splitEvidenceIDs(outputsRaw) {
		sc := scanctx.Get(contextID)
		if sc == nil || sc.ToolOutputs == nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf("tool output %s: scan context unavailable", id))
			continue
		}
		if _, ok := sc.ToolOutputs.Get(id); !ok {
			out.Skipped = append(out.Skipped, fmt.Sprintf("tool output %s does not exist in this run", id))
			continue
		}
		out.Outputs = append(out.Outputs, id)
	}
	if out.Explicit && len(out.Refs) == 0 && len(out.Outputs) == 0 {
		return out, "none of the supplied evidence references resolved"
	}
	return out, ""
}

func splitEvidenceIDs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})
	var out []string
	seen := map[string]bool{}
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// evidenceRejection is the structured refusal shown to the model when a
// non-info finding names no usable evidence.
func evidenceRejection(title string) string {
	return fmt.Sprintf(`❌ REJECTED: '%s' carries no resolvable wire evidence.

Every non-informational finding must name the captured traffic packet that proves it:
1. Call list_requests (or its seq=<n> form) to get the exchange sequence number of the
   request/response that demonstrated the issue.
2. Re-call report_vulnerability with traffic_refs="<seq>" (or "<session>:<seq>" when the
   packet belongs to a sibling session directory).
3. Non-HTTP findings (service banners, command output) may instead cite
   evidence_outputs="<id>" from read_tool_output.

Engine-owned deterministic proof (verify_* bridges, browser XSS nonce) is attached
automatically and does not need to be repeated.`, title)
}

// recordFindingEvidence copies the referenced bytes into the durable evidence
// store. It returns the new binding version. Failures are reported to the
// caller but never drop the finding: a disk/PG fault must not lose a proven
// vulnerability.
func recordFindingEvidence(contextID, scanDir, findingID string, ev reportEvidence) (int, error) {
	store := evidence.Default()
	if store == nil {
		return 0, fmt.Errorf("evidence store is not configured")
	}
	ctx := context.Background()
	version, err := store.Record(ctx, scanDir, findingID, ev.Refs, ev.Outputs)
	if err != nil {
		return 0, err
	}
	return version, nil
}
