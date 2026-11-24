package web

// Task-level HITL/audit switch tests: the process default is only a fallback;
// a task's audit_mode decides the run's posture for BOTH gates (command tools
// and the MITM wire), including the cross combinations.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/engine"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/trafficproxy"
)

func TestEffectiveTaskAuditMode(t *testing.T) {
	off := &config.Config{ApprovalMode: "off"}
	enforce := &config.Config{ApprovalMode: "enforce"}
	cases := []struct {
		name string
		task *platform.Task
		cfg  *config.Config
		want string
	}{
		{"no task + global off", nil, off, platform.TaskAuditOff},
		{"no task + global enforce", nil, enforce, platform.TaskAuditEnforce},
		{"inherit + global enforce", &platform.Task{AuditMode: platform.TaskAuditInherit}, enforce, platform.TaskAuditEnforce},
		{"inherit + global off", &platform.Task{AuditMode: platform.TaskAuditInherit}, off, platform.TaskAuditOff},
		{"enforce overrides global off", &platform.Task{AuditMode: platform.TaskAuditEnforce}, off, platform.TaskAuditEnforce},
		{"off overrides global enforce", &platform.Task{AuditMode: platform.TaskAuditOff}, enforce, platform.TaskAuditOff},
		{"empty stored value inherits", &platform.Task{AuditMode: ""}, enforce, platform.TaskAuditEnforce},
	}
	for _, tc := range cases {
		if got := effectiveTaskAuditMode(tc.task, tc.cfg); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestTaskAuditEnforceCreatesCommandApprovalWithGlobalOff: a task with
// audit=enforce gates terminal_execute even when XALGORIX_APPROVAL_MODE=off.
func TestTaskAuditEnforceCreatesCommandApprovalWithGlobalOff(t *testing.T) {
	cfg := &config.Config{ApprovalMode: "off", ApprovalTimeoutSeconds: 10, ApprovalMaxPending: 1}
	s, store := newApprovalsTestServer(t, cfg)
	t.Cleanup(func() { trafficproxy.SetHighRiskGate(nil) })

	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{
		Name: "audit-on task", Goal: "prove HITL", AuditMode: platform.TaskAuditEnforce,
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.AuditMode != platform.TaskAuditEnforce {
		t.Fatalf("stored audit mode = %q", task.AuditMode)
	}

	sctx := scanctx.New("task-audit-scan", t.TempDir())
	scanctx.Activate(sctx)
	t.Cleanup(func() {
		scanctx.Deactivate(sctx.ID)
		sctx.Close()
	})
	sctx.TaskID = task.ID
	sctx.AuditMode = effectiveTaskAuditMode(task, s.cfg)

	broker := s.einoApprovalBroker("task-audit-inst", sctx)
	if broker == nil {
		t.Fatal("task audit=enforce did not create the tool approval broker with global off")
	}
	id, err := broker.RequestApproval(ctx, engine.ApprovalRequest{
		ToolName: "terminal_execute",
		Args:     map[string]string{"command": "touch hitl-probe"},
	})
	if err != nil || id == "" {
		t.Fatalf("RequestApproval = %q, %v — want a pending row", id, err)
	}
	row, err := store.GetApproval(ctx, id)
	if err != nil || row == nil || row.Status != platform.ApprovalPending || row.Kind != platform.ApprovalKindCommand {
		t.Fatalf("approval row = %+v err=%v", row, err)
	}
	if !strings.Contains(row.Payload.Command, "touch hitl-probe") {
		t.Fatalf("row command = %q", row.Payload.Command)
	}
	if _, err := s.approvalService().Decide(ctx, id, platform.ApprovalRejected, "tester", "cleanup"); err != nil {
		t.Fatal(err)
	}

	// The same task switched to off must keep the adapter installed (so a later
	// enforce switch reaches existing workers) but must not gate the call.
	sctx.AuditMode = platform.TaskAuditOff
	b := s.einoApprovalBroker("task-audit-inst", sctx)
	if b == nil {
		t.Fatal("task audit=off dropped the approval adapter; off→enforce could not apply to running workers")
	}
	if id, err := b.RequestApproval(ctx, engine.ApprovalRequest{
		ToolName: "terminal_execute",
		Args:     map[string]string{"command": "touch hot-switch-off"},
	}); err != nil || id != "" {
		t.Fatalf("off run gated a call: id=%q err=%v", id, err)
	}

	// Flipping back to enforce applies to the SAME adapter on the next call.
	sctx.AuditMode = platform.TaskAuditEnforce
	id2, err := b.RequestApproval(ctx, engine.ApprovalRequest{
		ToolName: "terminal_execute",
		Args:     map[string]string{"command": "touch hot-switch-on"},
	})
	if err != nil || id2 == "" {
		t.Fatalf("enforce switch did not gate the next call: id=%q err=%v", id2, err)
	}
	if _, err := s.approvalService().Decide(ctx, id2, platform.ApprovalRejected, "tester", "cleanup"); err != nil {
		t.Fatal(err)
	}
}

// TestTaskAuditEnforceGatesProxyWithGlobalOff: a subprocess high-risk HTTP
// request through the MITM proxy is gated by the task switch even when the
// process default is off.
func TestTaskAuditEnforceGatesProxyWithGlobalOff(t *testing.T) {
	cfg := &config.Config{ApprovalMode: "off", ApprovalTimeoutSeconds: 10, ApprovalMaxPending: 1}
	s, store, sc, client, token := newProxyGateRigWithConfig(t, cfg)
	sc.AuditMode = platform.TaskAuditEnforce

	var hits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "task-enforced-ok")
	}))
	defer target.Close()

	req, err := http.NewRequest(http.MethodPost, target.URL+"/admin/delete", strings.NewReader("role=viewer"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Proxy-Authorization", basicAuthFor(token))
	type result struct {
		status int
		body   string
	}
	resCh := make(chan result, 1)
	go func() {
		resp, err := client.Do(req)
		if err != nil {
			resCh <- result{0, err.Error()}
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		resCh <- result{resp.StatusCode, string(body)}
	}()

	row := waitForPendingApproval(t, store)
	if row.Kind != platform.ApprovalKindHTTP {
		t.Fatalf("row kind = %q, want http", row.Kind)
	}
	if hits.Load() != 0 {
		t.Fatalf("target hit while the task-enforced HTTP call was pending: %d", hits.Load())
	}
	if _, err := s.approvalService().Decide(context.Background(), row.ID, platform.ApprovalApproved, "tester", "go"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	select {
	case res := <-resCh:
		if res.status != http.StatusOK || res.body != "task-enforced-ok" {
			t.Fatalf("after approval: status=%d body=%q", res.status, res.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("request never completed after approval")
	}
	if hits.Load() != 1 {
		t.Fatalf("target hits = %d, want 1", hits.Load())
	}
}

// TestTaskAuditOffSkipsProxyGateWithGlobalEnforce: a task with audit=off
// downgrades a process-wide enforce default for its own run.
func TestTaskAuditOffSkipsProxyGateWithGlobalEnforce(t *testing.T) {
	cfg := &config.Config{ApprovalMode: "enforce", ApprovalTimeoutSeconds: 10, ApprovalMaxPending: 1}
	_, store, sc, client, token := newProxyGateRigWithConfig(t, cfg)
	sc.AuditMode = platform.TaskAuditOff

	var hits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "observe-only-task")
	}))
	defer target.Close()

	req, err := http.NewRequest(http.MethodPost, target.URL+"/admin/delete", strings.NewReader("role=viewer"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Proxy-Authorization", basicAuthFor(token))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "observe-only-task" || hits.Load() != 1 {
		t.Fatalf("audit=off did not forward: status=%d body=%q hits=%d", resp.StatusCode, string(body), hits.Load())
	}
	rows, err := store.ListApprovals(context.Background(), platform.ApprovalFilter{Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("audit=off created %d pending approvals", len(rows))
	}
}
