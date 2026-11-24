package agent

import (
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
)

// R1 progressive recon: the engine no longer owes a wordlist pass by default.
// It is owed for an explicit operator request or a loud posture only; the model
// may still PROPOSE one, and the proposal gate decides.

func TestContentDiscoveryOwedByPosture(t *testing.T) {
	if contentDiscoveryOwed(NewScanState()) {
		t.Fatal("balanced default must not owe an automatic wordlist pass")
	}
	if contentDiscoveryOwed(&ScanState{NoiseProfile: config.NoiseQuiet}) {
		t.Fatal("quiet must not owe content discovery")
	}
	if !contentDiscoveryOwed(&ScanState{NoiseProfile: config.NoiseLoud}) {
		t.Fatal("loud must owe bounded content discovery")
	}
	explicit := NewScanState()
	explicit.OperatorRequestedDiscovery = true
	if !contentDiscoveryOwed(explicit) {
		t.Fatal("an explicit operator request must owe content discovery")
	}
	// Operator request overrides quiet.
	quietExplicit := &ScanState{NoiseProfile: config.NoiseQuiet, OperatorRequestedDiscovery: true}
	if !contentDiscoveryOwed(quietExplicit) {
		t.Fatal("an explicit operator request must override quiet")
	}
	if perHostContentDiscoveryOwed(NewScanState()) {
		t.Fatal("balanced must not owe per-host wordlist passes")
	}
}

func TestContentDiscoveryProposalGate(t *testing.T) {
	thin := NewScanState()
	thin.DiscoveredEndpoints = []string{"/", "/login"}
	if !contentDiscoveryProposalAllowed(thin).Owed {
		t.Fatal("balanced + thin inventory must admit a model-proposed bounded pass")
	}
	mature := NewScanState()
	for _, ep := range []string{"/", "/login", "/a", "/b", "/c", "/d", "/e", "/f"} {
		mature.DiscoveredEndpoints = append(mature.DiscoveredEndpoints, ep)
	}
	if contentDiscoveryProposalAllowed(mature).Owed {
		t.Fatal("mature inventory must reject a generic wordlist proposal")
	}
	api := NewScanState()
	api.DiscoveredEndpoints = []string{"/openapi.json"}
	if contentDiscoveryProposalAllowed(api).Owed {
		t.Fatal("API description must reject generic path enumeration")
	}
	quiet := &ScanState{NoiseProfile: config.NoiseQuiet}
	quiet.DiscoveredEndpoints = []string{"/"}
	if contentDiscoveryProposalAllowed(quiet).Owed {
		t.Fatal("quiet must reject proposals unless the operator asked")
	}
	quiet.OperatorRequestedDiscovery = true
	if !contentDiscoveryProposalAllowed(quiet).Owed {
		t.Fatal("operator request overrides quiet for proposals")
	}
}

func TestPortDiscoveryObligation(t *testing.T) {
	deep := NewScanState()
	deep.DeepReconRequired = true
	deep.PortDiscoveryAuthorized = true
	deep.RequestBudgetConfigured = true
	if serviceDiscoveryOwed(deep) {
		t.Fatal("the engine must not owe port discovery for a model-initiated direction")
	}
	deep.OperatorRequestedPortScan = true
	if !serviceDiscoveryOwed(deep) {
		t.Fatal("operator-requested port discovery with authorization and budget must be owed")
	}
	for name, mutate := range map[string]func(*ScanState){
		"no budget":      func(s *ScanState) { s.RequestBudgetConfigured = false },
		"not authorized": func(s *ScanState) { s.PortDiscoveryAuthorized = false },
		"not deep":       func(s *ScanState) { s.DeepReconRequired = false },
		"no operator":    func(s *ScanState) { s.OperatorRequestedPortScan = false },
	} {
		s := NewScanState()
		s.DeepReconRequired = true
		s.PortDiscoveryAuthorized = true
		s.RequestBudgetConfigured = true
		s.OperatorRequestedPortScan = true
		mutate(s)
		if serviceDiscoveryOwed(s) {
			t.Errorf("%s: port discovery must not be owed", name)
		}
	}
	// An explicit operator request overrides quiet, but never the
	// authorization/budget gates.
	quietExplicit := NewScanState()
	quietExplicit.NoiseProfile = config.NoiseQuiet
	quietExplicit.DeepReconRequired = true
	quietExplicit.PortDiscoveryAuthorized = true
	quietExplicit.RequestBudgetConfigured = true
	quietExplicit.OperatorRequestedPortScan = true
	if !serviceDiscoveryOwed(quietExplicit) {
		t.Fatal("operator request must override quiet for port discovery")
	}
}

func TestParameterAndHistoricalPosture(t *testing.T) {
	deep := &ScanState{DeepReconRequired: true}
	if !parameterDiscoveryOwed(deep) {
		t.Fatal("balanced must keep parameter discovery when a parameterized surface exists")
	}
	if parameterDiscoveryOwed(&ScanState{NoiseProfile: config.NoiseQuiet}) {
		t.Fatal("quiet must not owe parameter mining")
	}
	if historicalURLsOwed(&ScanState{NoiseProfile: config.NoiseQuiet}) {
		t.Fatal("quiet must not owe archival URL harvesting")
	}
}

func TestBalancedDropsDirbustFromEnginePlan(t *testing.T) {
	if p := AutoPlanFromState(NewScanState()); p.Get("dirbust") != nil {
		t.Fatal("balanced plan must not carry an automatic content-discovery task")
	}
	loud := NewScanState()
	loud.NoiseProfile = config.NoiseLoud
	if p := AutoPlanFromState(loud); p.Get("dirbust") == nil {
		t.Fatal("loud plan must keep the bounded content-discovery task")
	}
	explicit := NewScanState()
	explicit.OperatorRequestedDiscovery = true
	if p := AutoPlanFromState(explicit); p.Get("dirbust") == nil {
		t.Fatal("an explicit operator request must restore the content-discovery task")
	}
}

func TestReconcileSkipsDirbustWhenNotOwed(t *testing.T) {
	for name, state := range map[string]*ScanState{
		"quiet":    {NoiseProfile: config.NoiseQuiet},
		"balanced": {},
	} {
		state.Plan = NewPlan()
		state.Plan.add(&Task{ID: "dirbust", Phase: 3, VulnClass: "dirbusting", Status: TaskPending})
		reconcilePlan(state)
		task := state.Plan.Get("dirbust")
		if task == nil {
			t.Fatalf("%s: dirbust task disappeared", name)
		}
		if task.Status != TaskSkipped || task.Disposition != DispositionNotApplicable {
			t.Fatalf("%s: dirbust status=%q disposition=%q, want skipped/not_applicable", name, task.Status, task.Disposition)
		}
	}
	loud := &ScanState{NoiseProfile: config.NoiseLoud}
	loud.Plan = NewPlan()
	loud.Plan.add(&Task{ID: "dirbust", Phase: 3, VulnClass: "dirbusting", Status: TaskPending})
	reconcilePlan(loud)
	if got := loud.Plan.Get("dirbust").Status; got != TaskPending {
		t.Fatalf("loud dirbust status=%q, want pending", got)
	}
}
