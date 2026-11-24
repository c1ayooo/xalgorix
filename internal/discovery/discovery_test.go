package discovery

import "testing"

func TestContentDiscoveryOwed(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want bool
	}{
		{"quiet default", Input{Posture: PostureQuiet}, false},
		{"balanced default", Input{Posture: PostureBalanced}, false},
		{"loud", Input{Posture: PostureLoud}, true},
		{"operator request under quiet", Input{Posture: PostureQuiet, OperatorRequested: true}, true},
		{"operator request under balanced", Input{Posture: PostureBalanced, OperatorRequested: true}, true},
	}
	for _, tc := range cases {
		if got := ContentDiscoveryOwed(tc.in); got.Owed != tc.want {
			t.Errorf("%s: Owed=%v want %v (%s)", tc.name, got.Owed, tc.want, got.Reason)
		}
	}
}

func TestContentDiscoveryProposalAllowed(t *testing.T) {
	thin := Input{Posture: PostureBalanced, InventoryRoutes: 2}
	if !ContentDiscoveryProposalAllowed(thin).Owed {
		t.Fatal("balanced + thin inventory must admit a model-proposed bounded pass")
	}
	if ContentDiscoveryProposalAllowed(Input{Posture: PostureQuiet, InventoryRoutes: 0}).Owed {
		t.Fatal("quiet must reject proposals unless the operator asked")
	}
	if ContentDiscoveryProposalAllowed(Input{Posture: PostureQuiet, OperatorRequested: true}).Owed == false {
		t.Fatal("operator request overrides quiet")
	}
	mature := Input{Posture: PostureBalanced, InventoryRoutes: matureInventoryRoutes}
	if ContentDiscoveryProposalAllowed(mature).Owed {
		t.Fatal("mature inventory must reject a generic wordlist proposal")
	}
	apiSpec := Input{Posture: PostureBalanced, InventoryRoutes: 2, APISpec: true}
	if ContentDiscoveryProposalAllowed(apiSpec).Owed {
		t.Fatal("API description must reject generic path enumeration")
	}
	js := Input{Posture: PostureBalanced, InventoryRoutes: 3, JSAnalyzed: true}
	if ContentDiscoveryProposalAllowed(js).Owed {
		t.Fatal("JS/crawl inventory must reject a generic wordlist proposal")
	}
	loud := Input{Posture: PostureLoud, InventoryRoutes: matureInventoryRoutes}
	if !ContentDiscoveryProposalAllowed(loud).Owed {
		t.Fatal("loud posture accepts bounded discovery even with an inventory")
	}
}

func TestPortDiscoveryGates(t *testing.T) {
	base := Input{Posture: PostureBalanced, Deep: true, AuthorizedHostTarget: true, RequestBudget: true}
	if !PortProposalAllowed(base).Owed {
		t.Fatalf("authorized deep run with budget must allow a port proposal: %s", PortProposalAllowed(base).Reason)
	}
	if PortDiscoveryOwed(base).Owed {
		t.Fatal("the engine must not owe port discovery for a model-initiated direction")
	}
	explicit := base
	explicit.OperatorRequestedPort = true
	if !PortDiscoveryOwed(explicit).Owed {
		t.Fatal("an explicit operator request with authorization and budget must be owed")
	}

	for name, mutate := range map[string]func(*Input){
		"quiet":         func(in *Input) { in.Posture = PostureQuiet },
		"standard":      func(in *Input) { in.Deep = false },
		"url only":      func(in *Input) { in.AuthorizedHostTarget = false },
		"no budget":     func(in *Input) { in.RequestBudget = false },
		"operator only": func(in *Input) { in.OperatorRequested = true },
	} {
		in := base
		mutate(&in)
		if PortProposalAllowed(in).Owed && name != "operator only" {
			t.Errorf("%s: proposal must be rejected", name)
		}
	}
}

func TestAuthorizedHostTarget(t *testing.T) {
	cases := map[string]bool{
		"10.0.0.5":                true,
		"10.0.0.0/24":             true,
		"10.0.0.5:8080":           true,
		"app.example.com:443":     true,
		"https://app.example.com": false,
		"app.example.com":         false,
		"":                        false,
	}
	for target, want := range cases {
		if got := AuthorizedHostTarget([]string{target}); got != want {
			t.Errorf("AuthorizedHostTarget(%q)=%v want %v", target, got, want)
		}
	}
}

func TestOperatorRequestDetection(t *testing.T) {
	if !OperatorRequestedContentDiscovery("做一次目录扫描，找隐藏路径") {
		t.Fatal("Chinese content-discovery request not detected")
	}
	if !OperatorRequestedContentDiscovery("run ffuf with a wordlist") {
		t.Fatal("English content-discovery request not detected")
	}
	if OperatorRequestedContentDiscovery("获取服务器 shell") {
		t.Fatal("a generic goal must not be read as a content-discovery request")
	}
	if !OperatorRequestedPortDiscovery("先做全端口扫描再打") {
		t.Fatal("Chinese port-discovery request not detected")
	}
	if !OperatorRequestedPortDiscovery("nmap the host") {
		t.Fatal("English port-discovery request not detected")
	}
	if OperatorRequestedPortDiscovery("获取服务器 shell") {
		t.Fatal("a generic goal must not be read as a port request")
	}
}

func TestIntentClassification(t *testing.T) {
	if !LooksLikeContentDiscoveryIntent("Bounded content discovery — gap-driven wordlist pass", "", "") {
		t.Fatal("content discovery intent not classified")
	}
	if !LooksLikeContentDiscoveryIntent("目录枚举与隐藏路径", "", "") {
		t.Fatal("Chinese content discovery intent not classified")
	}
	if LooksLikeContentDiscoveryIntent("IDOR on /api/orders", "idor", "") {
		t.Fatal("normal endpoint test misclassified as content discovery")
	}
	if LooksLikeContentDiscoveryIntent("Directory traversal on /download", "path-traversal", "") {
		t.Fatal("path traversal must not be reclassified as content discovery")
	}
	if !LooksLikePortDiscoveryIntent("Full TCP port + service/version discovery", "recon", "") {
		t.Fatal("port discovery intent not classified")
	}
	if LooksLikePortDiscoveryIntent("SSTI on /guestbook", "ssti", "") {
		t.Fatal("normal injection test misclassified as port discovery")
	}
	if LooksLikePortDiscoveryIntent("Report service findings", "", "") {
		t.Fatal("a report task must not be reclassified as port discovery")
	}
}
