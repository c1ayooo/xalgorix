package agent

// delegation_claim.go — deterministic spawn dedup (WorkUnit claim, stage B2
// of docs/WORK_UNIT_DESIGN.md, 2026-11-23).
//
// During the TSec run the model spawned two workers for the same challenge
// (a-17 and c-09 each had a running and a queued agent) because spawn_agent's
// assignment is free-form text with no exclusivity. The planner's missing
// piece is a UNIT and a CLAIM: a delegated agent should own a unit
// (challenge:<unique_code> or endpoint:<ip[:port]>), and a second spawn for a
// unit an active worker already holds must be refused deterministically —
// independent of how the model words the task.
//
// This is the first slice of the WorkUnit protocol: unit key extraction +
// active-claim refusal. Leases (timeout reclaim) and planner-side unit
// generation follow the staged plan in the design doc.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/tools/agentsgraph"
)

var (
	// claimUniqueCode matches "unique_code=c-09", 'unique_code: a-17',
	// "unique_code=\"x\"" and the prose form "unique_code = x".
	claimUniqueCode = regexp.MustCompile(`(?i)unique_code\s*[=:"'\x60\s]+([A-Za-z0-9_-]{2,64})`)
	// claimEndpoint matches private IPv4 endpoints (challenge containers).
	claimEndpoint = regexp.MustCompile(`\b(?:10|172|192)\.\d{1,3}\.\d{1,3}\.\d{1,3}(?::\d{1,5})?\b`)
)

// delegationUnitKey derives the claim key a delegated assignment asks for:
// challenge:<unique_code> when the text names one, else endpoint:<ip[:port]>
// when it names a container address. "" means the assignment identifies no
// unit (free-form work cannot be deduplicated).
func delegationUnitKey(task, target string) string {
	combined := strings.TrimSpace(task) + "\n" + strings.TrimSpace(target)
	if m := claimUniqueCode.FindStringSubmatch(combined); m != nil {
		return "challenge:" + strings.ToLower(m[1])
	}
	if m := claimEndpoint.FindString(combined); m != "" {
		return "endpoint:" + m
	}
	return ""
}

// duplicateSpawnBlocked reports the agent that already holds key among the
// active (running/queued) delegated agents. Completed/failed agents release
// their claim implicitly — a later retry of the same unit is legitimate.
func duplicateSpawnBlocked(key string, agents []agentsgraph.AgentState) (owner string, blocked bool) {
	if key == "" {
		return "", false
	}
	for _, ag := range agents {
		if ag.Status != "running" && ag.Status != "queued" {
			continue
		}
		if delegationUnitKey(ag.Task, ag.Target) == key {
			return ag.AgentID, true
		}
	}
	return "", false
}

// shouldBlockForDelegationDuplicate refuses a spawn_agent/create_agent call
// whose unit is already claimed by an active worker.
func (a *Agent) shouldBlockForDelegationDuplicate(toolName string, toolArgs map[string]string) (bool, string) {
	if a == nil || a.agentGraph == nil {
		return false, ""
	}
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "spawn_agent", "create_agent":
	default:
		return false, ""
	}
	key := delegationUnitKey(toolArgs["task"]+" "+toolArgs["instruction"], toolArgs["target"])
	if key == "" {
		return false, ""
	}
	state := a.agentGraph.Snapshot()
	if owner, blocked := duplicateSpawnBlocked(key, state.Agents); blocked {
		return true, fmt.Sprintf(
			"%s is already claimed by worker %s. Do not spawn a second worker for the same unit — pick an uncovered one (the Workers projection lists every active owner).",
			key, owner)
	}
	return false, ""
}

// allTargetsControlPlane reports whether every scan target is a declared
// control plane (XALGORIX_CONTROL_PLANE_HOSTS): API endpoints are never scan
// targets, so the deterministic auto-delegation wave must not spawn recon
// lanes against them.
func (a *Agent) allTargetsControlPlane(targets []string) bool {
	if a == nil || len(a.controlPlaneHosts) == 0 || len(targets) == 0 {
		return false
	}
	declared := make(map[string]bool, len(a.controlPlaneHosts))
	for _, h := range a.controlPlaneHosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" {
			declared[h] = true
		}
	}
	if len(declared) == 0 {
		return false
	}
	for _, t := range targets {
		host, _ := targetHostPort(t)
		if host == "" || !declared[host] {
			return false
		}
	}
	return true
}
