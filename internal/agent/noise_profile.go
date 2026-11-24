package agent

// noise_profile.go — noise posture → which ACTIVE discovery dimensions the
// engine may DEMAND or ADMIT (R1 progressive recon).
//
// The engine used to owe every target a wordlist content-discovery pass and (in
// deep mode) a service/port sweep. That is legitimate for a lab or a loud
// engagement, but on a real production target a default wordlist + port sweep
// is traffic the owner did not ask for. The policy itself lives in
// internal/discovery so the engine plan, the recon predicate, the finish gate
// and the R7 planner all make the same decision; this file only projects
// ScanState onto its inputs.
//
//   quiet    — evidence-led only: HTTP probe, fingerprint, crawl/robots/sitemap,
//              JavaScript analysis, API artifacts, and targeted probes of routes
//              the surface actually exposed. No automatic wordlist, no port
//              sweep, no parameter brute force (operator request still wins).
//   balanced — evidence-led default: no AUTOMATIC wordlist/port obligation. The
//              model may propose bounded discovery; the gate admits it only
//              while the inventory is thin and no API description is known.
//   loud     — bounded active discovery is owed (wordlist per application host;
//              deep service discovery still requires authorization + budget).
//
// The posture NEVER blocks an operator instruction or a tool call; it only
// changes what the completion contract demands and what proposals are admitted.
// A worker that explicitly runs ffuf still gets credit (recon_results.go).

import (
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/discovery"
)

// noiseProfileOf normalizes the per-scan posture with a safe default.
func noiseProfileOf(state *ScanState) string {
	if state == nil {
		return config.NoiseBalanced
	}
	return config.NormalizeNoiseProfile(state.NoiseProfile)
}

// discoveryInput projects the scan state onto the policy inputs.
func discoveryInput(state *ScanState) discovery.Input {
	if state == nil {
		return discovery.Input{}
	}
	return discovery.Input{
		Posture:               noiseProfileOf(state),
		OperatorRequested:     state.OperatorRequestedDiscovery,
		OperatorRequestedPort: state.OperatorRequestedPortScan,
		Deep:                  state.DeepReconRequired,
		AuthorizedHostTarget:  state.PortDiscoveryAuthorized,
		RequestBudget:         state.RequestBudgetConfigured,
		InventoryRoutes:       len(state.DiscoveredEndpoints) + len(state.SeededSurface),
		JSAnalyzed:            state.ReconCoverage.JSAnalyzed,
		APISpec:               apiSpecPresent(state),
	}
}

// apiSpecPresent reports whether a machine-readable API description is part of
// the known surface. When it is, generic path enumeration is redundant: the
// documented interface should be analyzed instead.
func apiSpecPresent(state *ScanState) bool {
	if state == nil {
		return false
	}
	if state.DetectedTechs["graphql"] && state.ReconCoverage.APISurfaceDiscovered {
		return true
	}
	for _, ep := range state.DiscoveredEndpoints {
		if apiSpecPath(ep) {
			return true
		}
	}
	for _, se := range state.SeededSurface {
		if apiSpecPath(se.Path) {
			return true
		}
	}
	return false
}

func apiSpecPath(raw string) bool {
	l := strings.ToLower(strings.TrimSpace(raw))
	return strings.Contains(l, "swagger") || strings.Contains(l, "openapi") ||
		strings.Contains(l, "api-docs") || strings.Contains(l, "/api-docs") ||
		strings.Contains(l, "graphql") && strings.Contains(l, "schema")
}

// contentDiscoveryOwed reports whether the engine may DEMAND a wordlist content
// discovery pass. R1: balanced/quiet do not; only loud or an explicit operator
// request do.
func contentDiscoveryOwed(state *ScanState) bool {
	return discovery.ContentDiscoveryOwed(discoveryInput(state)).Owed
}

// contentDiscoveryProposalAllowed reports whether a MODEL-PROPOSED content
// discovery intent may be admitted.
func contentDiscoveryProposalAllowed(state *ScanState) discovery.Decision {
	return discovery.ContentDiscoveryProposalAllowed(discoveryInput(state))
}

// serviceDiscoveryOwed reports whether service/port enumeration is a required
// recon dimension. R1: only an explicit operator request that also satisfies
// host/IP authorization and a configured request budget is owed.
func serviceDiscoveryOwed(state *ScanState) bool {
	return discovery.PortDiscoveryOwed(discoveryInput(state)).Owed
}

// parameterDiscoveryOwed reports whether active parameter mining (arjun/x8) is
// a required dimension. Quiet mode treats parameter names harvested from HTML
// forms, query strings and JS as sufficient evidence-led discovery.
func parameterDiscoveryOwed(state *ScanState) bool {
	return noiseProfileOf(state) != config.NoiseQuiet
}

// historicalURLsOwed reports whether archival URL discovery is a required
// dimension. It is passive (third-party archives) but still adds reconnaissance
// noise and time; quiet engagements skip it unless the operator asks.
func historicalURLsOwed(state *ScanState) bool {
	return noiseProfileOf(state) != config.NoiseQuiet
}

// perHostContentDiscoveryOwed gates the "every distinct application host owes a
// wordlist pass" obligation. Quiet never owes it; balanced owes it only while
// the inventory is thin; loud always owes it.
func perHostContentDiscoveryOwed(state *ScanState) bool {
	return contentDiscoveryOwed(state)
}
