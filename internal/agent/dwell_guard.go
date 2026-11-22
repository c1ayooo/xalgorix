package agent

// dwell_guard.go — benchmark dwell budget (obligation → action ring,
// 2026-11-23). The rotation protocol lives in the benchmark instruction, but
// observed twice that the model rat-holes on one container for 15-20+ minutes
// despite it (sunk-cost behavior). This gate makes the budget deterministic:
// each endpoint (private IPv4[:port] appearing in tool args) may consume at
// most dwellMaxCalls tool calls; once exhausted, calls that reference it are
// refused with an explicit rotate instruction.
//
// Env-gated and OFF by default (XALGORIX_DWELL_MAX_CALLS=0), so ordinary
// scans are byte-identical to before. Idle decay (dwellDecay) resets a
// counter after the endpoint goes quiet, so a recycled container IP starts
// with a fresh budget.

import (
	"fmt"
	"regexp"
	"time"
)

// dwellEndpointPattern matches private IPv4 endpoints used as scan targets
// (challenge containers). The platform host is a domain and never matches;
// the operator machine is excluded by the earlier scopeguard gate anyway.
var dwellEndpointPattern = regexp.MustCompile(`\b(?:10|172|192)\.\d{1,3}\.\d{1,3}\.\d{1,3}(?::\d{1,5})?\b`)

// dwellDecay resets a per-endpoint counter after this much silence: the
// platform recycles container IPs, so a fresh instance on an old address
// must not inherit the previous challenge's exhausted budget.
const dwellDecay = 5 * time.Minute

type dwellStat struct {
	calls    int
	lastSeen time.Time
}

// shouldBlockForDwellBudget refuses calls that push a single endpoint over
// its call budget. See the package note for scope and decay semantics.
func (a *Agent) shouldBlockForDwellBudget(toolName string, toolArgs map[string]string) (bool, string) {
	max := a.dwellMaxCalls
	if max <= 0 {
		return false, ""
	}
	seen := make(map[string]bool, 2)
	for _, v := range toolArgs {
		for _, ep := range dwellEndpointPattern.FindAllString(v, -1) {
			seen[ep] = true
		}
	}
	if len(seen) == 0 {
		return false, ""
	}
	now := time.Now()
	a.dwellMu.Lock()
	defer a.dwellMu.Unlock()
	if a.dwellStats == nil {
		a.dwellStats = make(map[string]*dwellStat)
	}
	for ep := range seen {
		st := a.dwellStats[ep]
		if st == nil || now.Sub(st.lastSeen) > dwellDecay {
			st = &dwellStat{}
			a.dwellStats[ep] = st
		}
		if st.calls >= max {
			return true, fmt.Sprintf(
				"%s has already consumed %d tool calls (budget %d). Close that container and rotate to the next challenge NOW — do not probe it further, no matter how close the last lead looks.",
				ep, st.calls, max)
		}
	}
	for ep := range seen {
		st := a.dwellStats[ep]
		st.calls++
		st.lastSeen = now
	}
	return false, ""
}
