package gaps

import "testing"

func TestCompute_HighRiskQueuedAndUnverified(t *testing.T) {
	in := Input{
		Endpoints: []string{"/login", "/admin"},
		Verified:  map[string][]string{"/admin": {"idor", "sqli"}},
		Queued: []Hypothesis{
			{ID: "H-1", VulnClass: "sqli", Endpoint: "/login"},
			{ID: "H-2", VulnClass: "xss", Endpoint: "/search"},
			{ID: "H-3", VulnClass: "csrf", Endpoint: "/admin"},
		},
		OpenIntents: map[string]bool{
			IntentKey("/login", "sqli"): true,
		},
	}
	out := Compute(in)
	if len(out) == 0 {
		t.Fatal("expected gaps")
	}
	var sawUnverified, sawH2, sawH3 bool
	for _, g := range out {
		switch g.Area {
		case "endpoint-without-verifier: /login":
			sawUnverified = true
		case "unscheduled-hypothesis: H-2":
			sawH2 = true
		case "unscheduled-hypothesis: H-3":
			sawH3 = true
		}
		if g.Area == "unscheduled-hypothesis: H-1" {
			t.Fatal("H-1 is already covered by an open intent")
		}
	}
	if !sawUnverified || !sawH2 || !sawH3 {
		t.Fatalf("missing gaps: %+v", out)
	}
	if out[0].Risk != "high" || out[0].Score < 0.8 {
		t.Fatalf("ordering/risk = %+v", out[0])
	}
}

func TestCompute_DeterministicAndBounded(t *testing.T) {
	in := Input{Endpoints: []string{"/b", "/a", "/c"}}
	first := Compute(in)
	second := Compute(in)
	if len(first) != len(second) {
		t.Fatalf("lengths differ: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Area != second[i].Area {
			t.Fatalf("non-deterministic at %d: %q vs %q", i, first[i].Area, second[i].Area)
		}
	}
	if first[0].Area != "endpoint-without-verifier: /a" {
		t.Fatalf("sort = %+v", first)
	}
}
