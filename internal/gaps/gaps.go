// Package gaps computes deterministic, read-only coverage gaps.
//
// It is deliberately pure: it consumes a snapshot of the coverage matrix,
// queued hypotheses and open intents, and returns advisory gaps. It never
// writes intents, never calls an LLM, and never changes scheduling. A planner
// or operator reads the result and decides what (if anything) to do.
package gaps

import (
	"sort"
	"strings"
)

// MaxGaps bounds one computation.
const MaxGaps = 20

// Hypothesis is one queued ledger hypothesis.
type Hypothesis struct {
	ID        string
	VulnClass string
	Endpoint  string
}

// Input is the immutable snapshot gaps is computed from.
type Input struct {
	// Endpoints are the endpoints the coverage matrix has seen.
	Endpoints []string
	// Verified maps endpoint -> classes a deterministic verifier executed.
	Verified map[string][]string
	// Queued are ledger hypotheses still awaiting work.
	Queued []Hypothesis
	// OpenIntents holds "endpoint|class" keys already covered by an open,
	// running or paused intent.
	OpenIntents map[string]bool
}

// Gap is one advisory finding.
type Gap struct {
	Area     string   `json:"area"`
	Risk     string   `json:"risk"`
	Question string   `json:"question"`
	Refs     []string `json:"refs,omitempty"`
	Score    float64  `json:"score"`
}

// Compute returns at most MaxGaps gaps, ordered deterministically by score
// desc, area asc, then refs.
func Compute(in Input) []Gap {
	seen := map[string]bool{}
	var out []Gap
	add := func(g Gap) {
		key := strings.ToLower(g.Area) + "|" + strings.Join(g.Refs, ",")
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, g)
	}

	for _, raw := range in.Endpoints {
		ep := strings.TrimSpace(raw)
		if ep == "" {
			continue
		}
		if len(in.Verified[ep]) > 0 {
			continue
		}
		add(Gap{
			Area:     "endpoint-without-verifier: " + ep,
			Risk:     "medium",
			Question: "No deterministic verifier has executed against this endpoint; is it only probed and never verified?",
			Refs:     []string{ep},
			Score:    0.5,
		})
	}

	for _, h := range in.Queued {
		ep := strings.TrimSpace(h.Endpoint)
		class := strings.ToLower(strings.TrimSpace(h.VulnClass))
		if ep == "" || class == "" {
			continue
		}
		key := strings.ToLower(ep) + "|" + class
		if in.OpenIntents[key] {
			continue
		}
		add(Gap{
			Area:     "unscheduled-hypothesis: " + strings.TrimSpace(h.ID),
			Risk:     "high",
			Question: "Queued hypothesis has no open intent: " + class + " @ " + ep,
			Refs:     []string{strings.TrimSpace(h.ID), ep},
			Score:    0.8,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Area != out[j].Area {
			return out[i].Area < out[j].Area
		}
		return strings.Join(out[i].Refs, ",") < strings.Join(out[j].Refs, ",")
	})
	if len(out) > MaxGaps {
		out = out[:MaxGaps]
	}
	return out
}

// IntentKey normalizes the endpoint|class key used by OpenIntents.
func IntentKey(endpoint, class string) string {
	ep := strings.ToLower(strings.TrimSpace(endpoint))
	class = strings.ToLower(strings.TrimSpace(class))
	if ep == "" || class == "" {
		return ""
	}
	return ep + "|" + class
}
