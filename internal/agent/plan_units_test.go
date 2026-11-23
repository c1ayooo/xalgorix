package agent

// plan_units_test.go — B3 acceptance: the engine plan is a deterministic
// projection of the evidence (endpoint inventory + applicability), and
// control-plane hosts never produce work units.

import (
	"reflect"
	"testing"
)

func unitsTestState() *ScanState {
	st := NewScanState()
	st.ScanTargets = []string{"https://shop.example.com"}
	st.DiscoveredEndpoints = []string{
		"https://shop.example.com/login",
		"https://shop.example.com/search",
		"https://shop.example.com/api/items",
	}
	st.DetectedTechs = map[string]bool{"php": true}
	st.AllowedPhases = nil
	return st
}

func planIDs(p *Plan) []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.Tasks))
	for _, t := range p.Tasks {
		out = append(out, t.ID)
	}
	return out
}

func TestAutoPlanDeterministic(t *testing.T) {
	st := unitsTestState()
	a := AutoPlanFromState(st)
	b := AutoPlanFromState(st)
	if !reflect.DeepEqual(planIDs(a), planIDs(b)) {
		t.Fatalf("plan order is not deterministic:\n A=%v\n B=%v", planIDs(a), planIDs(b))
	}
	if len(a.Tasks) == 0 {
		t.Fatal("evidence plan is empty for a populated surface")
	}
}

func TestAutoPlanSkipsControlPlaneOnly(t *testing.T) {
	st := unitsTestState()
	st.ScanTargets = []string{"https://tsecbench.zc.tencent.com", "http://10.0.100.58"}
	st.ControlPlaneHosts = []string{"tsecbench.zc.tencent.com", "10.0.100.58"}
	st.DiscoveredEndpoints = []string{"https://tsecbench.zc.tencent.com/login"}
	if p := AutoPlanFromState(st); !p.IsEmpty() {
		t.Fatalf("control-plane-only scan produced %d tasks: %v", len(p.Tasks), planIDs(p))
	}
}

func TestAutoPlanFiltersControlPlaneEndpoints(t *testing.T) {
	st := unitsTestState()
	st.ControlPlaneHosts = []string{"tsecbench.zc.tencent.com"}
	st.DiscoveredEndpoints = append(st.DiscoveredEndpoints,
		"https://tsecbench.zc.tencent.com/console/api")
	p := AutoPlanFromState(st)
	if p.IsEmpty() {
		t.Fatal("mixed surface produced no plan")
	}
	for _, task := range p.Tasks {
		if task.Endpoint == "https://tsecbench.zc.tencent.com/console/api" {
			t.Fatalf("control-plane endpoint leaked into a task: %+v", task)
		}
		if containsStr(task.Notes, "tsecbench.zc.tencent.com") {
			t.Fatalf("control-plane endpoint leaked into task notes: %q", task.Notes)
		}
	}
}

func containsStr(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(stringsContains(haystack, needle))
}

func stringsContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
