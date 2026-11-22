package scanctx

import (
	"math"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
)

// MaxVOIWeight bounds how much an uncalibrated VOI estimate may alter the
// deterministic confidence ordering.
const MaxVOIWeight = 0.30

// SetVOIWeight clamps the configured ranking weight to [0, MaxVOIWeight].
func (ls *LedgerStore) SetVOIWeight(weight float64) {
	if math.IsNaN(weight) || math.IsInf(weight, 0) || weight < 0 {
		weight = 0
	}
	if weight > MaxVOIWeight {
		weight = MaxVOIWeight
	}
	ls.mu.Lock()
	ls.voiWeight = weight
	ls.mu.Unlock()
}

// VOIWeight returns the effective weight.
func (ls *LedgerStore) VOIWeight() float64 {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return ls.voiWeight
}

// SetVOIAs records an optional VOI estimate and audits the change.
func (ls *LedgerStore) SetVOIAs(hypID string, voi float64, estimated bool, actor domainjournal.Actor) error {
	if !estimated {
		_, err := ls.mutateHypothesisAs(hypID, actor, domainjournal.OpSetVOI, "mark VOI unestimated", func(h *Hypothesis) {
			h.VOI = 0
			h.VOIEstimated = false
		})
		return err
	}
	if math.IsNaN(voi) || math.IsInf(voi, 0) {
		voi = 0
	}
	voi = clampConfidence(voi)
	_, err := ls.mutateHypothesisAs(hypID, actor, domainjournal.OpSetVOI, "set VOI", func(h *Hypothesis) {
		h.VOI = voi
		h.VOIEstimated = true
	})
	return err
}

func hypothesisRankScore(h *Hypothesis, weight float64) float64 {
	voi := 0.0
	if h.VOIEstimated {
		voi = h.VOI
	}
	return (1-weight)*h.Confidence + weight*voi
}

func betterHypothesis(candidate, current *Hypothesis, weight float64) bool {
	if candidate == nil {
		return false
	}
	if current == nil {
		return true
	}
	candidateScore := hypothesisRankScore(candidate, weight)
	currentScore := hypothesisRankScore(current, weight)
	if candidateScore != currentScore {
		return candidateScore > currentScore
	}
	if candidate.Status != current.Status {
		return candidate.Status == HypothesisQueued
	}
	return false
}
