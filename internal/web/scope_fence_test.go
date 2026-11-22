package web

// Scope fence tests: the runtime (HITL) half of the project authorization
// boundary. Three layers are pinned here —
//
//  1. the pure boundary logic (fenceViolation / commandDestinations),
//  2. the gate integration (an out-of-fence call is closed as rejected with
//     decided_by="fence" and never becomes actionable),
//  3. the project page API (GET/PUT/validate on /api/projects/{id}/scope).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/approval"
	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func fencedScope() platform.Scope {
	return platform.Scope{
		Targets:    []string{"acme.example", "*.acme.example"},
		AllowPorts: []int{443, 8443},
		Fence: &platform.Fence{
			Enforce:             true,
			DenyTargets:         []string{"billing.acme.example"},
			AllowHTTPMethods:    []string{"POST", "PUT"},
			DenyCommandPatterns: []string{"msfconsole", "sudo"},
		},
	}
}

func TestFenceViolationBoundary(t *testing.T) {
	scope := fencedScope()
	cases := []struct {
		name    string
		verdict approval.Verdict
		want    string // substring of the reason; "" = inside the fence
	}{
		{
			name:    "command inside the scope",
			verdict: commandVerdict("curl -sS https://api.acme.example/login"),
			want:    "",
		},
		{
			name:    "command outside the scope",
			verdict: commandVerdict("curl -sS https://evil.example/x"),
			want:    "outside the project scope targets",
		},
		{
			name:    "deny target overrides allow",
			verdict: commandVerdict("curl https://billing.acme.example/pay"),
			want:    "excluded by the project fence",
		},
		{
			name:    "deny pattern",
			verdict: commandVerdict("sudo -u root cat /etc/shadow"),
			want:    "deny_command_patterns",
		},
		{
			name:    "command port outside allow_ports",
			verdict: commandVerdict("nmap -sV -p 22 acme.example:22"),
			want:    "not in the project allow_ports",
		},
		{
			name:    "loopback proxy hop is exempt",
			verdict: commandVerdict("curl -x http://127.0.0.1:8080 https://acme.example/"),
			want:    "",
		},
		{
			name: "http method not allowed",
			verdict: approval.Verdict{
				Kind: approval.KindHTTP, Method: "DELETE", URL: "https://acme.example/user/1",
			},
			want: "allow_http_methods",
		},
		{
			name: "http host outside the scope",
			verdict: approval.Verdict{
				Kind: approval.KindHTTP, Method: "POST", URL: "https://thirdparty.example/api",
			},
			want: "outside the project scope targets",
		},
		{
			name: "http inside the scope",
			verdict: approval.Verdict{
				Kind: approval.KindHTTP, Method: "POST", URL: "https://api.acme.example:8443/admin",
			},
			want: "",
		},
		{
			name: "http port outside allow_ports",
			verdict: approval.Verdict{
				Kind: approval.KindHTTP, Method: "POST", URL: "https://acme.example:9000/admin",
			},
			want: "not in the project allow_ports",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fenceViolation(scope, tc.verdict)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("fenceViolation = %q, want inside-fence", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("fenceViolation = %q, want substring %q", got, tc.want)
			}
		})
	}

	// A fence that is off must not change anything.
	off := fencedScope()
	off.Fence.Enforce = false
	if got := fenceViolation(off, commandVerdict("curl https://evil.example")); got != "" {
		t.Fatalf("fence off: fenceViolation = %q, want empty", got)
	}
}

func TestCommandDestinationsExtraction(t *testing.T) {
	cases := []struct {
		command string
		want    []string
	}{
		{"curl -sS https://api.acme.example:8443/x", []string{"api.acme.example:8443"}},
		{"nmap -sV -p 443 acme.example", []string{"acme.example"}},
		{"dig @8.8.8.8 evil.example", []string{"8.8.8.8", "evil.example"}},
		// File names must not read as destinations.
		{"nuclei -u https://acme.example -o report.pdf -jsonl", []string{"acme.example"}},
	}
	for _, tc := range cases {
		got := commandDestinations(tc.command)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("commandDestinations(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
}

// TestFenceDenialIsNeverActionable pins the gate integration: an out-of-fence
// call is recorded (audit) but closed immediately with decided_by="fence", so
// it never reaches the operator as a pending row.
func TestFenceDenialIsNeverActionable(t *testing.T) {
	s, store := newEnforceGateServer(t, 1, 1)
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Fenced", fencedScope())
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "fenced task", Goal: "probe acme"})
	if err != nil {
		t.Fatal(err)
	}
	instanceID := "inst-fenced"
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: instanceID, Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	registerGateInstance(t, s, instanceID)
	sctx := scanctx.New(instanceID, t.TempDir())
	defer sctx.Cancel()

	outcome := gateServiceFor(t, s).submit(instanceID, sctx, commandVerdict("curl https://evil.example/x"), nil)
	if outcome.Approved {
		t.Fatalf("out-of-fence call was approved: %+v", outcome)
	}
	if outcome.Status != platform.ApprovalRejected {
		t.Fatalf("status = %q, want rejected", outcome.Status)
	}
	if outcome.Row == nil || outcome.Row.DecidedBy != "fence" {
		t.Fatalf("row = %+v, want decided_by=fence", outcome.Row)
	}
	msg := approvalDenialMessage(outcome)
	for _, want := range []string{"⛔", "FENCE DENIAL", "outside the project scope targets", "Do NOT retry"} {
		if !strings.Contains(msg, want) {
			t.Errorf("denial text missing %q:\n%s", want, msg)
		}
	}
	// The audit row exists and is terminal — nothing is pending.
	pending, err := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending rows = %d, want 0 (fence denials are never actionable)", len(pending))
	}
	decided, err := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "decided", ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(decided) != 1 || decodedDecidedBy(t, decided[0]) != "fence" {
		t.Fatalf("decided rows = %+v, want exactly one fence denial", decided)
	}
	svc := gateServiceFor(t, s)
	svc.mu.Lock()
	pendingSlots := svc.pendingCount[instanceID]
	svc.mu.Unlock()
	if pendingSlots != 0 {
		t.Fatalf("pending slot count = %d, want 0 (the denial released its slot)", pendingSlots)
	}
}

func decodedDecidedBy(t *testing.T, row platform.Approval) string {
	t.Helper()
	return row.DecidedBy
}

// TestProjectScopeAPI covers the project page surface: read, save, dry-run.
func TestProjectScopeAPI(t *testing.T) {
	s, _ := newApprovalsTestServer(t, &config.Config{ApprovalMode: "enforce"})

	rr := doJSON(t, s, http.MethodPost, "/api/projects",
		`{"name":"Fence API","scope":{"targets":["acme.example","*.acme.example"],"allow_ports":[443]}}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project code = %d; body=%s", rr.Code, rr.Body.String())
	}
	projectID, _ := decodeBody(t, rr)["id"].(string)

	// GET → the stored boundary (fence off until the operator turns it on).
	rr = doJSON(t, s, http.MethodGet, "/api/projects/"+projectID+"/scope", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET scope code = %d; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeBody(t, rr)
	if body["approval_mode"] != "enforce" {
		t.Errorf("approval_mode = %v, want enforce", body["approval_mode"])
	}
	if body["fence_bound"] != false {
		t.Errorf("fence_bound = %v, want false while the fence is off", body["fence_bound"])
	}

	// PUT an enforced fence with no targets → fail-closed 400.
	rr = doJSON(t, s, http.MethodPut, "/api/projects/"+projectID+"/scope",
		`{"targets":[],"fence":{"enforce":true}}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PUT empty-target fence code = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}

	// A port-carrying deny entry is rejected at write time.
	rr = doJSON(t, s, http.MethodPut, "/api/projects/"+projectID+"/scope",
		`{"targets":["acme.example"],"fence":{"enforce":true,"deny_targets":["billing.acme.example:443"]}}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PUT port-carrying deny target code = %d, want 400", rr.Code)
	}

	// A valid enforced fence saves and reports it as bound (HITL is enforcing).
	rr = doJSON(t, s, http.MethodPut, "/api/projects/"+projectID+"/scope",
		`{"targets":["acme.example","*.acme.example"],"allow_ports":[443,8443],
		  "fence":{"enforce":true,"deny_targets":["billing.acme.example"],
		           "allow_http_methods":["POST"],"deny_command_patterns":["msfconsole"]}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT scope code = %d; body=%s", rr.Code, rr.Body.String())
	}
	body = decodeBody(t, rr)
	if body["fence_bound"] != true {
		t.Errorf("fence_bound = %v, want true", body["fence_bound"])
	}
	fence, _ := body["fence"].(map[string]any)
	if fence["enforce"] != true {
		t.Errorf("fence = %v", fence)
	}
	if _, hasWarning := body["warning"]; hasWarning {
		t.Errorf("warning should be absent with no active runs: %v", body["warning"])
	}

	// The saved scope survives a re-read (state consistency between the form
	// and the dispatch path).
	rr = doJSON(t, s, http.MethodGet, "/api/projects/"+projectID+"/scope", "")
	body = decodeBody(t, rr)
	scope, _ := body["scope"].(map[string]any)
	if targets, _ := scope["targets"].([]any); len(targets) != 2 {
		t.Fatalf("re-read targets = %v, want 2", scope["targets"])
	}

	// Dry-run: one in-scope target, one out-of-scope, one denied, one bad port.
	rr = doJSON(t, s, http.MethodPost, "/api/projects/"+projectID+"/scope/validate",
		`{"targets":["https://api.acme.example/x","https://evil.example","https://billing.acme.example","https://acme.example:9000"]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("validate code = %d; body=%s", rr.Code, rr.Body.String())
	}
	body = decodeBody(t, rr)
	if body["ok"] != true {
		t.Errorf("validate ok = %v, want true (candidate scope itself is valid)", body["ok"])
	}
	entries, _ := body["entries"].([]any)
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(entries))
	}
	wantInScope := []bool{true, false, false, false}
	for i, raw := range entries {
		entry, _ := raw.(map[string]any)
		if entry["in_scope"] != wantInScope[i] {
			t.Errorf("entry %d (%v) in_scope = %v, want %v", i, entry["target"], entry["in_scope"], wantInScope[i])
		}
		if !wantInScope[i] {
			reason, _ := entry["reason"].(string)
			if reason == "" {
				t.Errorf("entry %d (%v) has no reason", i, entry["target"])
			}
		}
	}

	// Unknown project → 404 (platform error mapping preserved).
	rr = doJSON(t, s, http.MethodGet, "/api/projects/does-not-exist/scope", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown project scope code = %d, want 404", rr.Code)
	}
}

// TestScopeValidateRejectsMalformedCandidate pins the dry-run's own validation
// path: a malformed candidate is reported as problems, never persisted.
func TestScopeValidateRejectsMalformedCandidate(t *testing.T) {
	s, _ := newApprovalsTestServer(t, &config.Config{})
	rr := doJSON(t, s, http.MethodPost, "/api/projects",
		`{"name":"Malformed","scope":{"targets":["acme.example"]}}`)
	projectID, _ := decodeBody(t, rr)["id"].(string)

	rr = doJSON(t, s, http.MethodPost, "/api/projects/"+projectID+"/scope/validate",
		`{"scope":{"targets":["acme.example:443"]}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("validate code = %d; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		OK       bool     `json:"ok"`
		Problems []string `json:"problems"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK || len(resp.Problems) == 0 {
		t.Fatalf("malformed candidate accepted: %+v", resp)
	}
}
