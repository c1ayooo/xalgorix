package web

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/approval"
	"github.com/xalgord/xalgorix/v4/internal/config"
	"github.com/xalgord/xalgorix/v4/internal/platform"
	"github.com/xalgord/xalgorix/v4/internal/scanctx"
	"github.com/xalgord/xalgorix/v4/internal/tools"
)

// newEnforceGateServer builds an enforce-mode server with a short timeout so
// the deadline tests stay fast, a file store, and the given max_pending.
func newEnforceGateServer(t *testing.T, timeoutSec, maxPending int) (*Server, platform.Repository) {
	t.Helper()
	cfg := &config.Config{
		ApprovalMode:           "enforce",
		ApprovalTimeoutSeconds: timeoutSec,
		ApprovalMaxPending:     maxPending,
	}
	return newApprovalsTestServer(t, cfg)
}

// registerGateInstance registers a live-looking instance so WS broadcasts
// buffer into it (ws_hub.go broadcastToInstance replay path).
func registerGateInstance(t *testing.T, s *Server, instanceID string) *ScanInstance {
	t.Helper()
	inst := &ScanInstance{ID: instanceID, Status: "running"}
	s.instancesMu.Lock()
	s.instances[instanceID] = inst
	s.instancesMu.Unlock()
	return inst
}

// gateRegistry returns a registry with a regulated tool plus the bound
// approval gate, mirroring the scan_session wiring. The tool is named
// terminal_execute so the policy classifies it (unknown tools are
// deliberately unregulated — REDESIGN §10). The SAME adapter instance is
// returned so tests can BindSecrets on the adapter the registry actually
// holds (a second approvalGateFor call would bind a different struct).
func gateRegistry(t *testing.T, s *Server, instanceID string, sctx *scanctx.ScanContext, executed *int) (*tools.Registry, *registryGate) {
	t.Helper()
	reg := tools.NewRegistry()
	reg.SetScanContextID(sctx.ID)
	reg.Register(&tools.Tool{
		Name:       "terminal_execute",
		Parameters: []tools.Parameter{{Name: "command", Required: true}},
		Execute: func(args map[string]string) (tools.Result, error) {
			*executed++
			return tools.Result{Output: "ran"}, nil
		},
	})
	gate := s.approvalGateFor(instanceID, sctx)
	if gate == nil {
		t.Fatal("enforce mode must install the gate")
	}
	reg.SetApprovalGate(gate)
	return reg, gate
}

func commandVerdict(cmd string) approval.Verdict {
	return approval.Verdict{
		Decision: approval.Require,
		Kind:     approval.KindCommand,
		Reason:   "shell command execution requires approval",
		Command:  cmd,
	}
}

// TestGateEndToEndApprovedFlow pins the full happy path: Execute suspends →
// approval_requested lands in the instance buffer → operator decides via
// svc.Decide → Execute resumes and the tool runs; the row is approved and
// approval_decided is buffered.
func TestGateEndToEndApprovedFlow(t *testing.T) {
	s, store := newEnforceGateServer(t, 5, 1)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	registerGateInstance(t, s, "inst-e2e")

	sctx := scanctx.New("gate-e2e", t.TempDir())
	executed := 0
	reg, _ := gateRegistry(t, s, "inst-e2e", sctx, &executed)

	resCh := make(chan tools.Result, 1)
	go func() {
		res, err := reg.Execute("terminal_execute", map[string]string{"command": "sqlmap -u http://target"})
		if err != nil {
			t.Errorf("execute: %v", err)
		}
		resCh <- res
	}()

	// Wait for the pending row to appear (Execute is parked in the gate).
	var row *platform.Approval
	for i := 0; i < 300; i++ {
		rows, _ := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "pending", ProjectID: project.ID})
		if len(rows) == 1 {
			row = &rows[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if row == nil {
		t.Fatal("pending approval row never appeared")
	}
	if executed != 0 {
		t.Fatal("tool ran while approval was pending")
	}

	// approval_requested is buffered for the live feed (broadcast lands right
	// after CreateApproval — poll briefly instead of racing it).
	s.instancesMu.RLock()
	inst := s.instances["inst-e2e"]
	s.instancesMu.RUnlock()
	requested := false
	for i := 0; i < 300; i++ {
		inst.mu.RLock()
		for _, evt := range inst.events {
			if evt.Type == "approval_requested" && evt.Approval != nil && evt.Approval.ID == row.ID {
				requested = true
			}
		}
		inst.mu.RUnlock()
		if requested {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !requested {
		t.Error("approval_requested WS event missing from the instance buffer")
	}

	final, err := gateServiceFor(t, s).Decide(ctx, row.ID, platform.ApprovalApproved, "operator:admin", "")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if final.Status != platform.ApprovalApproved {
		t.Fatalf("final status = %s", final.Status)
	}

	select {
	case res := <-resCh:
		if !res.Success || res.Output != "ran" {
			t.Errorf("tool result after approval = %+v, want a clean run", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute never resumed after the approval")
	}
	if executed != 1 {
		t.Errorf("tool executed %d times, want 1", executed)
	}
	assertApprovalDecidedEvents(t, s, "inst-e2e", 1)
}

// TestGateDenialMessageShape pins the denial text contract: status + reason +
// the suggested alternative path, styled after the existing ⛔ block messages.
func TestGateDenialMessageShape(t *testing.T) {
	s, store := newEnforceGateServer(t, 1, 1)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	registerGateInstance(t, s, "inst-deny")
	sctx := scanctx.New("gate-deny", t.TempDir())

	// Nothing decides this one — it times out on its own (timeout=1s),
	// which is also the shape the timeout denial needs.
	outcome := gateServiceFor(t, s).submit("inst-deny", sctx, commandVerdict("rm -rf /"), nil)
	if outcome.Approved || outcome.Status != platform.ApprovalTimeout {
		t.Fatalf("outcome = %+v, want a timeout rejection", outcome)
	}
	rows, _ := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "decided", ProjectID: project.ID})
	if len(rows) != 1 {
		t.Fatalf("decided rows = %d, want 1", len(rows))
	}
	outcome.Row = &rows[0]
	msg := approvalDenialMessage(outcome)
	for _, want := range []string{"⛔", "APPROVAL TIMEOUT", rows[0].ID, "approval deadline", "Do NOT retry", "read-only allowlist"} {
		if !strings.Contains(msg, want) {
			t.Errorf("denial text missing %q:\n%s", want, msg)
		}
	}
	httpMsg := approvalDenialMessage(submitOutcome{
		Status: platform.ApprovalTimeout,
		Row: &platform.Approval{
			ID: "row-http", Kind: platform.ApprovalKindHTTP, Status: platform.ApprovalTimeout,
			Reason: "admin panel path",
		},
	})
	if !strings.Contains(httpMsg, "APPROVAL TIMEOUT") || !strings.Contains(httpMsg, "GET/HEAD/OPTIONS") {
		t.Errorf("http timeout denial missing the timeout status + alternative path:\n%s", httpMsg)
	}
}

// TestGateTimeoutMeansRejection pins the D5 hard gate: an unanswered approval
// flips to timeout at its deadline, the tool call receives a rejection-shaped
// verdict mentioning the timeout, and the row is terminal in the store.
func TestGateTimeoutMeansRejection(t *testing.T) {
	s, store := newEnforceGateServer(t, 1, 1)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	registerGateInstance(t, s, "inst-timeout")
	sctx := scanctx.New("gate-timeout", t.TempDir())

	start := time.Now()
	outcome := gateServiceFor(t, s).submit("inst-timeout", sctx, commandVerdict("reboot now"), nil)
	if outcome.Approved {
		t.Fatal("timeout must be a rejection, not an approval")
	}
	if outcome.Status != platform.ApprovalTimeout {
		t.Fatalf("status = %s, want timeout", outcome.Status)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("timeout took %s, want ~1s", elapsed)
	}
	rows, _ := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "decided", ProjectID: project.ID})
	if len(rows) != 1 || rows[0].Status != platform.ApprovalTimeout {
		t.Fatalf("rows = %+v, want one timeout row", rows)
	}
	if rows[0].DecidedBy != "timeout" {
		t.Errorf("decided_by = %q, want timeout", rows[0].DecidedBy)
	}
	// The verdict the model sees must carry the timeout semantics.
	msg := approvalDenialMessage(outcome)
	if !strings.Contains(msg, "TIMEOUT") || !strings.Contains(msg, "Do NOT retry") {
		t.Errorf("timeout denial text:\n%s", msg)
	}
	assertApprovalDecidedEvents(t, s, "inst-timeout", 1)
}

// TestGateScanCtxCancel pins the M-11 interaction: cancelling the scan
// context while an approval pends returns a cancelled verdict immediately
// (no goroutine leak), and the row flips to cancelled.
func TestGateScanCtxCancel(t *testing.T) {
	s, store := newEnforceGateServer(t, 60, 1)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	registerGateInstance(t, s, "inst-cancel")
	sctx := scanctx.New("gate-cancel", t.TempDir())

	done := make(chan submitOutcome, 1)
	go func() {
		done <- gateServiceFor(t, s).submit("inst-cancel", sctx, commandVerdict("sleep 999"), nil)
	}()
	time.Sleep(100 * time.Millisecond) // let the row land
	sctx.Cancel()                      // the scan stop path

	select {
	case outcome := <-done:
		if outcome.Approved || outcome.Status != platform.ApprovalCancelled {
			t.Fatalf("outcome = %+v, want cancelled", outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("submit did not return after scan cancellation (goroutine leak)")
	}
	rows, _ := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "decided", ProjectID: project.ID})
	if len(rows) != 1 || rows[0].Status != platform.ApprovalCancelled {
		t.Fatalf("rows = %+v, want one cancelled row", rows)
	}
}

// TestGateSlotQueue pins the anti-storm semantics: max_pending per run, and
// overflow requests QUEUE for a free slot instead of failing (REDESIGN §5.3).
func TestGateSlotQueue(t *testing.T) {
	s, store := newEnforceGateServer(t, 5, 1)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	registerGateInstance(t, s, "inst-slot")
	sctx := scanctx.New("gate-slot", t.TempDir())
	svc := gateServiceFor(t, s)

	firstDone := make(chan submitOutcome, 1)
	go func() { firstDone <- svc.submit("inst-slot", sctx, commandVerdict("first"), nil) }()
	waitForPendingRows(t, store, project.ID, 1, "first submit")

	secondDone := make(chan submitOutcome, 1)
	go func() { secondDone <- svc.submit("inst-slot", sctx, commandVerdict("second"), nil) }()

	// The overflow submit must be parked: still exactly one pending row.
	time.Sleep(150 * time.Millisecond)
	rows, _ := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "pending", ProjectID: project.ID})
	if len(rows) != 1 {
		t.Fatalf("overflow created a row: %d pending, want 1 (queued for the slot)", len(rows))
	}
	select {
	case out := <-secondDone:
		t.Fatalf("overflow submit returned early: %+v", out)
	default:
	}

	// Deciding the first releases the slot; the second creates its own row.
	if _, err := svc.Decide(ctx, rows[0].ID, platform.ApprovalApproved, "operator", ""); err != nil {
		t.Fatal(err)
	}
	out1 := <-firstDone
	if !out1.Approved {
		t.Fatalf("first submit = %+v, want approved", out1)
	}
	waitForPendingRows(t, store, project.ID, 1, "second submit after slot release")
	pendingRows, _ := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "pending", ProjectID: project.ID})
	if _, err := svc.Decide(ctx, pendingRows[0].ID, platform.ApprovalApproved, "operator", ""); err != nil {
		t.Fatal(err)
	}
	out2 := <-secondDone
	if !out2.Approved {
		t.Fatalf("second submit = %+v, want approved once the slot freed", out2)
	}
	if n, _ := store.CountPendingByInstance(ctx, "inst-slot"); n != 0 {
		t.Errorf("pending after both decisions = %d, want 0", n)
	}
}

func waitForPendingRows(t *testing.T, store platform.Repository, projectID string, want int, what string) {
	t.Helper()
	for i := 0; i < 300; i++ {
		rows, _ := store.ListApprovals(context.Background(), platform.ApprovalFilter{Status: "pending", ProjectID: projectID})
		if len(rows) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pending rows for %s never reached %d", what, want)
}

// TestGateModeOffInstallsNoGate pins the factory default: mode=off (the
// default) leaves the registry ungated — approvalGateFor returns nil so the
// registry keeps its byte-identical legacy path, and no rows appear.
func TestGateModeOffInstallsNoGate(t *testing.T) {
	s, store := newApprovalsTestServer(t, &config.Config{}) // mode off (default)
	sctx := scanctx.New("gate-off", t.TempDir())
	if gate := s.approvalGateFor("inst-off", sctx); gate != nil {
		t.Fatal("mode=off must not install a gate")
	}
	executed := 0
	reg := tools.NewRegistry()
	reg.Register(&tools.Tool{
		Name:       "probe_tool",
		Parameters: []tools.Parameter{{Name: "command", Required: true}},
		Execute: func(args map[string]string) (tools.Result, error) {
			executed++
			return tools.Result{Output: "ran"}, nil
		},
	})
	res, err := reg.Execute("probe_tool", map[string]string{"command": "anything"})
	if err != nil || !res.Success || executed != 1 {
		t.Fatalf("off-mode execution changed: %+v err=%v executed=%d", res, err, executed)
	}
	rows, _ := store.ListApprovals(context.Background(), platform.ApprovalFilter{Status: "all"})
	if len(rows) != 0 {
		t.Errorf("off mode created %d approval rows, want 0", len(rows))
	}
}

// TestGatePayloadMasking pins the no-secrets red line: secret-class header
// values are masked through the shared redaction policy before persistence
// and the body excerpt is truncated to 2 KiB (API_CONTRACT §3).
func TestGatePayloadMasking(t *testing.T) {
	s, store := newEnforceGateServer(t, 5, 1)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	registerGateInstance(t, s, "inst-mask")
	sctx := scanctx.New("gate-mask", t.TempDir())

	verdict := approval.Verdict{
		Decision: approval.Require,
		Kind:     approval.KindHTTP,
		Reason:   "auth mutation endpoint or payload",
		Method:   "POST",
		URL:      "http://example.com/wp-login.php",
		Headers: map[string]string{
			"authorization": "Bearer supersecret-token-value",
			"accept":        "application/json",
		},
		Body: strings.Repeat("A", 5000),
	}
	go gateServiceFor(t, s).submit("inst-mask", sctx, verdict, nil)
	waitForPendingRows(t, store, project.ID, 1, "masked submit")

	rows, _ := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "pending", ProjectID: project.ID})
	row := rows[0]
	maskedAuth := row.Payload.Headers["authorization"]
	if maskedAuth == "Bearer supersecret-token-value" {
		t.Error("authorization header stored in the clear (API never returns credential material)")
	}
	if !strings.HasPrefix(maskedAuth, "****") {
		t.Errorf("masked authorization = %q, want the ****… redaction shape", maskedAuth)
	}
	if row.Payload.Headers["accept"] != "application/json" {
		t.Errorf("non-secret header lost: %q", row.Payload.Headers["accept"])
	}
	if len(row.Payload.BodyExcerpt) > maxApprovalBodyBytes || !strings.HasSuffix(row.Payload.BodyExcerpt, "...") {
		t.Errorf("body excerpt = %d bytes, want ≤%d with the truncation marker", len(row.Payload.BodyExcerpt), maxApprovalBodyBytes)
	}
	if row.Payload.URL != "http://example.com/wp-login.php" || row.Payload.Method != "POST" {
		t.Errorf("request material lost: %+v", row.Payload)
	}
}

// TestGateServiceWarnsOnOversizedTimeout pins the config-validation warning:
// an approval timeout beyond the smallest gated tool ceiling (browser_action
// 10m + 30s grace) is loudly flagged at startup — the agent's own tool
// hard-timeout would otherwise win the race (documented in approvals_gate.go).
func TestGateServiceWarnsOnOversizedTimeout(t *testing.T) {
	s := newTestServer(t, &config.Config{
		ApprovalMode:           "enforce",
		ApprovalTimeoutSeconds: 3600,
	})
	logs := captureLogsForApprovals(func() {
		s.SetApprovalService(newApprovalService(s))
	})
	if !strings.Contains(logs, "WARNING") || !strings.Contains(logs, "XALGORIX_APPROVAL_TIMEOUT") {
		t.Errorf("oversized timeout warning missing:\n%s", logs)
	}
	// A sane timeout stays silent.
	s2 := newTestServer(t, &config.Config{
		ApprovalMode:           "enforce",
		ApprovalTimeoutSeconds: 600,
	})
	logs2 := captureLogsForApprovals(func() {
		s2.SetApprovalService(newApprovalService(s2))
	})
	if strings.Contains(logs2, "WARNING") {
		t.Errorf("default timeout must not warn:\n%s", logs2)
	}
}

// captureLogsForApprovals captures the standard logger while fn runs.
func captureLogsForApprovals(fn func()) string {
	var buf bytes.Buffer
	original := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(original)
	fn()
	return buf.String()
}

// TestGateVerifierRegistryUngated documents the verifier decision: the
// verifier builds its own registry and never installs an approval gate — a
// report-gate approval wait would race the 15-minute watchdog (verifier.go
// invariant). Pinned indirectly here by asserting the gate installation is
// scan_session-only: approvalGateFor requires a service AND enforce mode.
func TestGateVerifierRegistryUngated(t *testing.T) {
	s, _ := newEnforceGateServer(t, 5, 1)
	sctx := scanctx.New("gate-verifier", t.TempDir())
	gate := s.approvalGateFor("inst-verifier", sctx)
	if gate == nil {
		t.Fatal("enforce mode gate must be available for scan sessions")
	}
	// The verifier's registry simply never calls SetApprovalGate — verified
	// by construction: a fresh registry has a nil gate.
	reg := tools.NewRegistry()
	if reg.ApprovalGate() != nil {
		t.Fatal("fresh registry must be ungated")
	}
}
