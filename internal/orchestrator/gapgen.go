package orchestrator

// gapgen.go — IG5: deterministic, bounded gap→intent candidates.
//
// The generator is pure: it consumes a coverage snapshot plus the ledger's
// schedulable (queued) hypotheses and returns advisory candidates. It never
// writes graph state, never calls an LLM, and never invents a target: a
// candidate without a concrete dispatch subject is dropped, so the planner can
// only emit an intent for a real coordinate. The planner still decides whether
// (and with which phase) to turn a candidate into an intent, through the normal
// apply() validation, control-plane filter and dedupe.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/gaps"
	"github.com/c1ayooo/xalgorix/v4/internal/promptrender"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

const (
	// maxGapCandidates bounds one generation. The overview renders every
	// candidate, so this is also the block's line budget.
	maxGapCandidates = 8
	// gapScoreUnscheduledHypothesis ranks a queued ledger belief whose
	// (endpoint, class) has no open/running intent above a plain unverified
	// endpoint: the belief already carries a vuln class and evidence context.
	gapScoreUnscheduledHypothesis = 0.8
	// gapScoreUnverifiedEndpoint ranks an endpoint that was probed but never
	// reached by a deterministic verifier.
	gapScoreUnverifiedEndpoint = 0.5
)

// gapCandidate is one advisory gap→intent coordinate. Subject is a concrete
// dispatch target; Endpoint is the path/route inside it; Purpose is the vuln
// class ("" = unclassified, the planner must classify before emitting).
type gapCandidate struct {
	Subject  string
	Endpoint string
	Purpose  string
	Score    float64
	Refs     []string
}

// gapCandidateInput is the immutable snapshot the generator runs on.
type gapCandidateInput struct {
	// Target is the run target. It is the only subject source: an empty,
	// placeholder, symbolic ("task:...") or control-plane target suppresses
	// all candidates.
	Target string
	// Probed / Verified are the coverage matrix snapshot (endpoint -> classes).
	Probed   map[string][]string
	Verified map[string][]string
	// Queued is the ledger's schedulable pool (status=queued).
	Queued []scanctx.Hypothesis
	// CoveredIntents holds "endpoint|class" keys already covered by an open,
	// running or paused intent.
	CoveredIntents map[string]bool
	// ControlPlane lists hosts that must never become a candidate subject.
	ControlPlane []string
	// Max overrides the bound (default and hard cap: maxGapCandidates).
	Max int
}

// generateGapCandidates returns at most maxGapCandidates candidates, ordered
// deterministically by score desc, endpoint asc, purpose asc, refs asc. Same
// snapshot => same output; no clock, no randomness, no map-order dependence.
func generateGapCandidates(in gapCandidateInput) []gapCandidate {
	subject := strings.TrimSpace(in.Target)
	if subject == "" || isPlaceholderTarget(subject) || isSymbolicTaskTarget(subject) ||
		isControlPlaneTarget(subject, in.ControlPlane) {
		return nil // no concrete target: never emit a placeholder candidate
	}
	limit := in.Max
	if limit <= 0 || limit > maxGapCandidates {
		limit = maxGapCandidates
	}
	covered := in.CoveredIntents
	if covered == nil {
		covered = map[string]bool{}
	}

	seen := make(map[string]bool)
	out := make([]gapCandidate, 0, limit)
	add := func(c gapCandidate) {
		ep := strings.TrimSpace(c.Endpoint)
		if ep == "" || isPlaceholderTarget(ep) || isControlPlaneTarget(ep, in.ControlPlane) {
			return
		}
		key := strings.ToLower(ep) + "|" + strings.ToLower(strings.TrimSpace(c.Purpose))
		if seen[key] {
			return
		}
		seen[key] = true
		c.Subject = subject
		c.Endpoint = ep
		c.Purpose = strings.ToLower(strings.TrimSpace(c.Purpose))
		out = append(out, c)
	}

	// Rule 1: ledger schedulable pool. A queued hypothesis is already a claim
	// with a class; it is a gap only when no open/running/paused intent covers
	// its (endpoint, class) coordinate.
	queuedEndpoints := make(map[string]bool)
	for _, h := range in.Queued {
		ep := strings.TrimSpace(h.Endpoint)
		if ep == "" {
			ep = strings.TrimSpace(h.Target)
		}
		if ep == "" || isPlaceholderTarget(ep) {
			continue
		}
		class := strings.ToLower(strings.TrimSpace(h.VulnClass))
		queuedEndpoints[strings.ToLower(ep)] = true
		if gapEndpointCovered(covered, ep, class) {
			continue
		}
		refs := []string{ep}
		if id := strings.TrimSpace(h.ID); id != "" {
			refs = append([]string{id}, refs...)
		}
		add(gapCandidate{Endpoint: ep, Purpose: class, Score: gapScoreUnscheduledHypothesis, Refs: refs})
	}

	// Rule 2: coverage gaps. An endpoint the coverage matrix has seen that no
	// deterministic verifier ever touched is a gap; it is skipped when rule 1
	// already carried the same endpoint (the belief names the class).
	endpoints := make([]string, 0, len(in.Probed))
	for ep := range in.Probed {
		if len(in.Verified[ep]) > 0 {
			continue
		}
		if strings.TrimSpace(ep) == "" || isPlaceholderTarget(ep) {
			continue
		}
		endpoints = append(endpoints, ep)
	}
	sort.Strings(endpoints)
	for _, ep := range endpoints {
		if queuedEndpoints[strings.ToLower(ep)] {
			continue
		}
		add(gapCandidate{Endpoint: ep, Score: gapScoreUnverifiedEndpoint, Refs: []string{ep}})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Endpoint != out[j].Endpoint {
			return out[i].Endpoint < out[j].Endpoint
		}
		if out[i].Purpose != out[j].Purpose {
			return out[i].Purpose < out[j].Purpose
		}
		return strings.Join(out[i].Refs, ",") < strings.Join(out[j].Refs, ",")
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// gapEndpointCovered reports whether an open intent already covers the
// coordinate. An empty class means "any class on this endpoint".
func gapEndpointCovered(covered map[string]bool, endpoint, class string) bool {
	if key := gaps.IntentKey(endpoint, class); key != "" && covered[key] {
		return true
	}
	if strings.TrimSpace(class) != "" {
		return false
	}
	prefix := strings.ToLower(strings.TrimSpace(endpoint)) + "|"
	for key := range covered {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// gapCandidatesBlock renders the candidates as an advisory, bounded overview
// block (<= maxGapCandidates lines). It never writes graph state.
func (p *Planner) gapCandidatesBlock(snap coverageSnapshot) string {
	if p.cfg.ScanCtx == nil && strings.TrimSpace(p.cfg.Target) == "" {
		return ""
	}
	candidates := generateGapCandidates(gapCandidateInput{
		Target:         p.cfg.Target,
		Probed:         snap.probed,
		Verified:       snap.verified,
		Queued:         snap.queued,
		CoveredIntents: snap.openIntents,
		ControlPlane:   p.cfg.ControlPlaneHosts,
	})
	if len(candidates) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nGap candidates (advisory; subject=%s; you decide whether to emit an intent):\n",
		promptrender.Cell(candidates[0].Subject, 120))
	for _, c := range candidates {
		purpose := c.Purpose
		if purpose == "" {
			purpose = "unclassified"
		}
		fmt.Fprintf(&b, "- endpoint=%s purpose=%s refs=%s\n",
			promptrender.Cell(c.Endpoint, 120), promptrender.Cell(purpose, 40),
			promptrender.Cell(strings.Join(c.Refs, ","), 120))
	}
	return b.String()
}
