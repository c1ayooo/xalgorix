package objective

import (
	"fmt"
	"regexp"
	"strings"
)

// Evaluate scores every criterion of the objective against the signal
// snapshot. It is a pure function: no IO, no writes, no global state.
//
// Semantics (deterministic, table-driven in evaluate_test.go):
//
//	finding_class — met when an accepted finding matches the class AND carries
//	                a strong verification tag; unmet when candidates exist but
//	                none is strongly verified (e.g. only
//	                needs-manual-verification); undetermined when no candidate
//	                exists at all.
//	capability   — same matching, plus the verification mode decides what
//	                counts: deterministic requires concrete exploitation
//	                evidence, verifier requires the verified tag, manual always
//	                stays manual_pending.
//	coverage     — met when planned > 0 and completed >= planned; undetermined
//	                when nothing was planned.
func Evaluate(spec *Spec, sig Signal) Report {
	if spec == nil {
		// criteria is a non-omitempty wire field: keep the empty-array contract
		// so unstructured tasks don't marshal it as null.
		return Report{Structured: false, Criteria: []CriterionStatus{}}
	}
	report := Report{Structured: true, Criteria: []CriterionStatus{}}
	decided := 0
	for _, c := range spec.Criteria {
		status := evaluateCriterion(c, sig)
		report.Criteria = append(report.Criteria, status)
		switch status.State {
		case StateMet:
			report.Met++
			decided++
		case StateUnmet:
			report.Unmet++
			decided++
		case StateManualPending:
			report.ManualPending++
		default:
			report.Undetermined++
		}
	}
	if decided+report.Undetermined > 0 {
		report.Attainment = float64(report.Met) / float64(decided+report.Undetermined)
	}
	return report
}

func evaluateCriterion(c SpecCriterion, sig Signal) CriterionStatus {
	status := CriterionStatus{
		ID:         c.ID,
		Kind:       c.Kind,
		VulnClass:  c.VulnClass,
		Capability: c.Capability,
		VerifyBy:   c.VerifyBy,
		Required:   c.Required,
	}
	switch c.Kind {
	case KindCoverage:
		return evaluateCoverage(c, sig, status)
	case KindFindingClass:
		return evaluateFindingClass(c, sig, status)
	case KindCapability:
		return evaluateCapability(c, sig, status)
	default:
		status.State = StateUndetermined
		status.Reason = fmt.Sprintf("unsupported criterion kind %q", c.Kind)
		return status
	}
}

func evaluateCoverage(c SpecCriterion, sig Signal, status CriterionStatus) CriterionStatus {
	if sig.CoveragePlanned <= 0 {
		status.State = StateUndetermined
		status.Reason = "no coverage plan recorded for this run"
		return status
	}
	if sig.CoverageCompleted >= sig.CoveragePlanned {
		status.State = StateMet
		status.Reason = fmt.Sprintf("coverage completed %d/%d", sig.CoverageCompleted, sig.CoveragePlanned)
		return status
	}
	status.State = StateUnmet
	status.Reason = fmt.Sprintf("coverage completed %d/%d", sig.CoverageCompleted, sig.CoveragePlanned)
	return status
}

func evaluateFindingClass(c SpecCriterion, sig Signal, status CriterionStatus) CriterionStatus {
	candidates := matchFindings(c.VulnClass, sig.Findings)
	if len(candidates) == 0 {
		status.State = StateUndetermined
		status.Reason = fmt.Sprintf("no accepted finding matches %q", c.VulnClass)
		return status
	}
	strong, tag := strongest(candidates)
	if strong == nil {
		status.State = StateUnmet
		status.Reason = fmt.Sprintf("%d matching finding(s) exist but none is verified (%s)",
			len(candidates), TagNeedsManualVerif)
		return status
	}
	status.State = StateMet
	status.EvidenceFindingID = strong.ID
	status.EvidenceTag = tag
	status.Reason = fmt.Sprintf("accepted finding %s (%s)", strong.ID, tag)
	return status
}

func evaluateCapability(c SpecCriterion, sig Signal, status CriterionStatus) CriterionStatus {
	if c.VerifyBy == VerifyManual {
		status.State = StateManualPending
		status.Reason = "criterion requires manual confirmation"
		return status
	}
	candidates := matchCapabilityFindings(c.Capability, sig.Findings)
	if len(candidates) == 0 {
		status.State = StateUndetermined
		status.Reason = fmt.Sprintf("no accepted finding evidences capability %q", c.Capability)
		return status
	}
	switch c.VerifyBy {
	case VerifyDeterministic:
		for _, f := range candidates {
			if strings.TrimSpace(f.ExploitProof) == "" {
				continue
			}
			if f.Tag != TagExploitProven && f.Tag != TagVerified {
				continue
			}
			status.State = StateMet
			status.EvidenceFindingID = f.ID
			status.EvidenceTag = f.Tag
			status.Reason = fmt.Sprintf("accepted finding %s carries exploitation evidence (%s)", f.ID, f.Tag)
			return status
		}
		status.State = StateUnmet
		status.Reason = fmt.Sprintf("%d candidate finding(s) but none carries concrete exploitation evidence", len(candidates))
		return status
	case VerifyVerifier:
		strong, tag := strongest(candidates)
		if strong == nil {
			status.State = StateUnmet
			status.Reason = fmt.Sprintf("%d candidate finding(s) but none reached the verified tag", len(candidates))
			return status
		}
		status.State = StateMet
		status.EvidenceFindingID = strong.ID
		status.EvidenceTag = tag
		status.Reason = fmt.Sprintf("independent verifier accepted finding %s", strong.ID)
		return status
	default:
		status.State = StateUndetermined
		status.Reason = fmt.Sprintf("unsupported verify_by %q", c.VerifyBy)
		return status
	}
}

// strongest returns the candidate with the strongest verification tag.
// Verified beats exploit-proven (independent reproduction outranks a
// self-contained exploit proof); needs-manual-verification never qualifies.
func strongest(candidates []FindingSignal) (*FindingSignal, string) {
	var best *FindingSignal
	bestRank := -1
	for i := range candidates {
		rank := tagRank(candidates[i].Tag)
		if rank > bestRank {
			best = &candidates[i]
			bestRank = rank
		}
	}
	if best == nil || bestRank < 0 {
		return nil, ""
	}
	return best, best.Tag
}

func tagRank(tag string) int {
	switch tag {
	case TagVerified:
		return 2
	case TagExploitProven:
		return 1
	default:
		return -1
	}
}

// capabilityAliases maps an operator-facing capability name to the finding
// tokens that may evidence it. The mapping is deliberately small and
// explicit: an unknown capability falls back to strict token matching, so an
// unknown name can never be satisfied by an unrelated finding. Aliases only
// decide *which* findings are candidates — the verification mode still
// decides whether one of them counts as evidence.
var capabilityAliases = map[string][]string{
	"shell": {"shell", "rce", "command injection", "command execution", "remote code execution", "code execution"},
}

// matchCapabilityFindings returns the findings that may evidence a
// capability: alias match for known capabilities (any token), strict
// all-token match otherwise.
func matchCapabilityFindings(capability string, findings []FindingSignal) []FindingSignal {
	key := strings.ToLower(strings.TrimSpace(capability))
	aliases, ok := capabilityAliases[key]
	if !ok {
		return matchFindings(capability, findings)
	}
	out := make([]FindingSignal, 0, len(findings))
	for _, f := range findings {
		haystack := strings.ToLower(strings.Join([]string{f.VulnClass, f.Title, f.CWE}, " "))
		for _, alias := range aliases {
			if strings.Contains(haystack, alias) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// matchFindings returns the findings whose class/title/CWE mention every
// token of the query. Token matching is intentionally conservative: a
// criterion must not be satisfied by an unrelated finding.
func matchFindings(query string, findings []FindingSignal) []FindingSignal {
	tokens := tokenize(query)
	if len(tokens) == 0 {
		return nil
	}
	out := make([]FindingSignal, 0, len(findings))
	for _, f := range findings {
		haystack := strings.ToLower(strings.Join([]string{f.VulnClass, f.Title, f.CWE}, " "))
		if containsAllTokens(haystack, tokens) {
			out = append(out, f)
		}
	}
	return out
}

var tokenSplit = regexp.MustCompile(`[^a-z0-9]+`)

func tokenize(s string) []string {
	parts := tokenSplit.Split(strings.ToLower(s), -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func containsAllTokens(haystack string, tokens []string) bool {
	for _, tok := range tokens {
		if !strings.Contains(haystack, tok) {
			return false
		}
	}
	return true
}
