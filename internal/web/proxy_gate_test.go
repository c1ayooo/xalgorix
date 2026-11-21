package web

// Web adapter acceptance for the traffic-proxy high-risk gate (R2b closed
// loop): the adapter runs the SAME ApprovalService machinery as the registry
// gate — real rows in the file store, real WS events on the instance buffer,
// deadline timer (timeout = rejection), fail-open without a store, and the
// proxy-side observe-only posture when mode=off.
//
// These tests drive a REAL loopback MITM proxy (trafficproxy.Configure) and
// a subprocess-shaped Basic-auth request, end to end: proxy → adapter →
// store → operator decision → forwarded/denied. Sequential by design (the
// hook + proxy manager are process-global; no t.Parallel).

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/approval"
	"github.com/xalgord/xalgorix/v4/internal/config"
	"github.com/xalgord/xalgorix/v4/internal/platform"
	"github.com/xalgord/xalgorix/v4/internal/proxy"
	"github.com/xalgord/xalgorix/v4/internal/scanctx"
	"github.com/xalgord/xalgorix/v4/internal/scopeguard"
	"github.com/xalgord/xalgorix/v4/internal/trafficproxy"
)

// newProxyGateRig wires everything the closed loop touches: an enforce-mode
// server (NewServer installs the trafficproxy hook), a live scan context,
// the attribution registry entry, a registered instance (WS replay buffer),
// and a TLS target behind the MITM proxy. Returns the client that routes
// through the loopback proxy.
func newProxyGateRig(t *testing.T, timeoutSec int) (*Server, platform.Repository, *scanctx.ScanContext, *http.Client, string) {
	t.Helper()
	cfg := &config.Config{
		ApprovalMode:           "enforce",
		ApprovalTimeoutSeconds: timeoutSec,
		ApprovalMaxPending:     1,
	}
	s, store := newApprovalsTestServer(t, cfg)
	// NewServer installed the enforce adapter into the process-global hook;
	// drop it again when the test ends so later tests start clean.
	t.Cleanup(func() { trafficproxy.SetHighRiskGate(nil) })

	_ = proxy.Close() // no upstream pool: direct forwarding
	setProxyGateDialGuard(t)
	if err := trafficproxy.Configure(true, filepath.Join(t.TempDir(), "ca"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trafficproxy.Close() })

	sc := scanctx.New("proxy-gate-scan", t.TempDir())
	scanctx.Activate(sc)
	t.Cleanup(func() {
		scanctx.Deactivate(sc.ID)
		sc.Close()
	})
	const token = "1a2b3c4d5e6f708192a3b4c5d6e7f809"
	trafficproxy.RegisterRun(token, "proxy-gate-inst", sc.ID, scopeguard.Config{AllowLocalTargets: true})
	t.Cleanup(func() { trafficproxy.UnregisterRun(token, sc.ID) })

	registerGateInstance(t, s, "proxy-gate-inst")

	localURL, err := trafficproxy.LocalURL()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(localURL)
	if err != nil {
		t.Fatal(err)
	}
	pool := trafficproxy.RootCAs()
	if pool == nil {
		t.Fatal("CA pool missing under MITM")
	}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(parsed),
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
		Timeout: 15 * time.Second,
	}
	return s, store, sc, client, token
}

func basicAuthFor(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(token+":"))
}

// waitForPendingApproval polls the store until a pending row exists.
func waitForPendingApproval(t *testing.T, store platform.Repository) *platform.Approval {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "pending"})
		if err == nil && len(rows) > 0 {
			return &rows[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("pending approval row never appeared")
	return nil
}

// instanceEvents returns the buffered events of an instance.
func instanceEvents(t *testing.T, s *Server, instanceID string) []WSEvent {
	t.Helper()
	s.instancesMu.RLock()
	inst := s.instances[instanceID]
	s.instancesMu.RUnlock()
	if inst == nil {
		t.Fatalf("instance %s not registered", instanceID)
	}
	inst.mu.RLock()
	defer inst.mu.RUnlock()
	return append([]WSEvent(nil), inst.events...)
}

// TestProxyGateApprovedFlowBroadcastsMaskedPayload is case ①+⑥ at the web
// layer: a subprocess high-risk POST pends, approval_requested is buffered
// with a redacted payload (URL query masked — the payload rides redactURL),
// the operator approves through the same Decide path as the REST surface,
// the request forwards and approval_decided lands.
func TestProxyGateApprovedFlowBroadcastsMaskedPayload(t *testing.T) {
	s, store, _, client, token := newProxyGateRig(t, 10)

	var hits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "approved-ok")
	}))
	defer target.Close()

	req, err := http.NewRequest(http.MethodPost, target.URL+"/admin/delete?token=hunter2", strings.NewReader("role=viewer"))
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
			t.Errorf("proxied request: %v", err)
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		resCh <- result{resp.StatusCode, string(body)}
	}()

	row := waitForPendingApproval(t, store)
	if row.Kind != platform.ApprovalKindHTTP {
		t.Fatalf("row kind = %q", row.Kind)
	}
	if row.Payload.Method != http.MethodPost || !strings.Contains(row.Payload.URL, "/admin/delete") {
		t.Fatalf("row payload = %+v", row.Payload)
	}
	// Payload redaction (case ⑥): the URL the operator sees carries the
	// masked query value — verdict URLs come from the observation's
	// redactURL output, so credentials never enter the store/WS/API.
	if strings.Contains(row.Payload.URL, "hunter2") {
		t.Fatalf("approval payload URL leaked the credential: %q", row.Payload.URL)
	}
	if !strings.Contains(row.Payload.URL, "token=****") && !strings.Contains(row.Payload.URL, "token=%2A%2A%2A%2A") {
		t.Fatalf("approval payload URL not masked: %q", row.Payload.URL)
	}
	if hits.Load() != 0 {
		t.Fatalf("target hit while approval was pending: %d", hits.Load())
	}

	// approval_requested is buffered for the instance live feed.
	var requested *WSEvent
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && requested == nil {
		for i := range instanceEvents(t, s, "proxy-gate-inst") {
			evt := instanceEvents(t, s, "proxy-gate-inst")[i]
			if evt.Type == "approval_requested" && evt.Approval != nil && evt.Approval.ID == row.ID {
				requested = &evt
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if requested == nil {
		t.Fatalf("no approval_requested event among %d events", len(instanceEvents(t, s, "proxy-gate-inst")))
	}
	if strings.Contains(requested.Approval.Payload.URL, "hunter2") {
		t.Fatalf("WS payload leaked the credential: %+v", requested.Approval.Payload)
	}

	if _, err := s.approvalService().Decide(context.Background(), row.ID, platform.ApprovalApproved, "operator", "go ahead"); err != nil {
		t.Fatalf("operator decision: %v", err)
	}

	select {
	case res := <-resCh:
		if res.status != http.StatusOK || res.body != "approved-ok" {
			t.Fatalf("after approval: status=%d body=%q", res.status, res.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("request never completed after approval")
	}

	// The row is terminal and approval_decided is buffered.
	decided, err := store.GetApproval(context.Background(), row.ID)
	if err != nil || decided.Status != platform.ApprovalApproved {
		t.Fatalf("row after decision: %+v err=%v", decided, err)
	}
	var decidedEvt *WSEvent
	for _, evt := range instanceEvents(t, s, "proxy-gate-inst") {
		if evt.Type == "approval_decided" && evt.Approval != nil && evt.Approval.ID == row.ID {
			decidedEvt = &evt
		}
	}
	if decidedEvt == nil {
		t.Fatal("no approval_decided event buffered")
	}
}

// TestProxyGateTimeoutIs403Rejection is case ③ at the web layer: a 1s
// deadline elapses with no operator → timeout = rejection → the proxy client
// gets a 403 whose body names the approval and the timeout semantics, and
// the row lands as timeout.
func TestProxyGateTimeoutIs403Rejection(t *testing.T) {
	_, store, _, client, token := newProxyGateRig(t, 1)

	var hits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "must-not-reach")
	}))
	defer target.Close()

	req, err := http.NewRequest(http.MethodPost, target.URL+"/admin/reset", strings.NewReader("x=1"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Proxy-Authorization", basicAuthFor(token))
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("timeout denial should return a response: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("timeout status = %d, want 403", resp.StatusCode)
	}
	if !strings.Contains(string(body), "APPROVAL TIMEOUT") {
		t.Fatalf("403 body lost the timeout text: %q", body)
	}
	if hits.Load() != 0 {
		t.Fatalf("upstream received %d requests after timeout denial", hits.Load())
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("denial returned after %s — deadline was not honored", elapsed)
	}

	// The store's status filter only knows pending/decided/all (REST
	// contract), so list all and match the terminal status client-side.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows, _ := store.ListApprovals(context.Background(), platform.ApprovalFilter{Status: "all"})
		for _, r := range rows {
			if r.Status == platform.ApprovalTimeout {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("row never landed as timeout")
}

// TestProxyGateFailsOpenWithoutStore pins the no-DSN red line: enforce mode
// but no platform store → submit approves without a row, the request
// forwards, nothing is parked.
func TestProxyGateFailsOpenWithoutStore(t *testing.T) {
	cfg := &config.Config{ApprovalMode: "enforce", ApprovalTimeoutSeconds: 10, ApprovalMaxPending: 1}
	s := newTestServer(t, cfg) // no SetPlatformStore — store stays nil
	t.Cleanup(func() { trafficproxy.SetHighRiskGate(nil) })
	if svc := s.approvalService(); svc == nil || svc.policy.Mode != approval.ModeEnforce {
		t.Fatal("enforce approval service missing without a store")
	}

	_ = proxy.Close()
	setProxyGateDialGuard(t)
	if err := trafficproxy.Configure(true, filepath.Join(t.TempDir(), "ca"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trafficproxy.Close() })

	const token = "9f8e7d6c5b4a3928170f1e2d3c4b5a69"
	trafficproxy.RegisterRun(token, "failopen-inst", "failopen-scan", scopeguard.Config{AllowLocalTargets: true})
	t.Cleanup(func() { trafficproxy.UnregisterRun(token, "failopen-scan") })

	var hits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "failopen-ok")
	}))
	defer target.Close()

	localURL, err := trafficproxy.LocalURL()
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(localURL)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(parsed),
			TLSClientConfig: &tls.Config{RootCAs: trafficproxy.RootCAs()},
		},
		Timeout: 10 * time.Second,
	}
	req, err := http.NewRequest(http.MethodPost, target.URL+"/admin/delete", strings.NewReader("x=1"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Proxy-Authorization", basicAuthFor(token))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "failopen-ok" || hits.Load() != 1 {
		t.Fatalf("fail-open did not forward: status=%d body=%q hits=%d", resp.StatusCode, body, hits.Load())
	}
}

// TestProxyGateModeOffKeepsProxyObserveOnly pins the default posture: mode
// off clears the hook — a subprocess high-risk POST forwards untouched, and
// the adapter would deny if it were ever consulted (it is not).
func TestProxyGateModeOffKeepsProxyObserveOnly(t *testing.T) {
	cfg := &config.Config{ApprovalMode: "off"}
	s := newTestServer(t, cfg)
	t.Cleanup(func() { trafficproxy.SetHighRiskGate(nil) })
	if s.approvalService().policy.Mode != approval.ModeOff {
		t.Fatalf("mode = %q", s.approvalService().policy.Mode)
	}

	_ = proxy.Close()
	setProxyGateDialGuard(t)
	if err := trafficproxy.Configure(true, filepath.Join(t.TempDir(), "ca"), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trafficproxy.Close() })

	const token = "0d0c0b0a191817161514131211100f0e"
	trafficproxy.RegisterRun(token, "modeoff-inst", "modeoff-scan", scopeguard.Config{AllowLocalTargets: true})
	t.Cleanup(func() { trafficproxy.UnregisterRun(token, "modeoff-scan") })

	var hits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "observe-only")
	}))
	defer target.Close()

	localURL, err := trafficproxy.LocalURL()
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(localURL)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(parsed),
			TLSClientConfig: &tls.Config{RootCAs: trafficproxy.RootCAs()},
		},
		Timeout: 10 * time.Second,
	}
	req, err := http.NewRequest(http.MethodPost, target.URL+"/admin/delete", strings.NewReader("x=1"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Proxy-Authorization", basicAuthFor(token))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || hits.Load() != 1 {
		t.Fatalf("mode off did not forward: status=%d hits=%d", resp.StatusCode, hits.Load())
	}
}

// TestProxyGateAdapterRejectsNonSubprocessChannel is defense in depth: the
// adapter must never create a verdict for Bearer/unattributed traffic even
// if a future proxy bug routed it here.
func TestProxyGateAdapterRejectsNonSubprocessChannel(t *testing.T) {
	cfg := &config.Config{ApprovalMode: "enforce", ApprovalTimeoutSeconds: 10}
	s, store := newApprovalsTestServer(t, cfg)
	gate := &proxyHighRiskGate{svc: s.approvalService()}

	verdict := approvalVerdictForTest()
	for _, ch := range []trafficproxy.Channel{trafficproxy.ChannelBearer, trafficproxy.ChannelNone} {
		approved, _, err := gate.Adjudicate(trafficproxy.Attribution{
			InstanceID: "inst",
			ScanCtxID:  "scan",
			Channel:    ch,
		}, verdict)
		if err != nil || !approved {
			t.Fatalf("channel %d was adjudicated: approved=%v err=%v", ch, approved, err)
		}
	}
	ctx := context.Background()
	rows, _ := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "pending"})
	if len(rows) != 0 {
		t.Fatalf("non-subprocess channel created approval rows: %+v", rows)
	}
}

// setProxyGateDialGuard allows the loopback httptest targets these tests
// dial through the proxy's direct-forwarding path (restored on cleanup).
func setProxyGateDialGuard(t *testing.T) {
	t.Helper()
	trafficproxy.SetDialGuardBase(scopeguard.Config{BindAddr: "127.0.0.1", Port: 1, AllowLocalTargets: true})
	t.Cleanup(func() { trafficproxy.SetDialGuardBase(scopeguard.Config{}) })
}

// approvalVerdictForTest is a minimal high-risk HTTP verdict.
func approvalVerdictForTest() approval.Verdict {
	return approval.Verdict{
		Decision: approval.Require,
		Kind:     approval.KindHTTP,
		Reason:   "admin panel path",
		Method:   http.MethodPost,
		URL:      "https://target.example/admin",
	}
}

// TestSetApprovalServiceReinstallsProxyHook pins the L-4 wiring seam: the
// trafficproxy hook always wraps the CURRENT service — swapping between
// enforce/off (or clearing) re-installs it, never leaving a stale adapter.
func TestSetApprovalServiceReinstallsProxyHook(t *testing.T) {
	cfg := &config.Config{ApprovalMode: "enforce", ApprovalTimeoutSeconds: 10, ApprovalMaxPending: 1}
	s := newTestServer(t, cfg)
	t.Cleanup(func() { trafficproxy.SetHighRiskGate(nil) })

	enforceSvc := s.approvalService()
	if !trafficproxy.HighRiskGateInstalled() {
		t.Fatal("enforce NewServer did not install the proxy hook")
	}

	// Swap in an off-mode service: the hook must be cleared.
	offSvc := &ApprovalService{srv: s, policy: approval.Policy{Mode: approval.ModeOff}}
	s.SetApprovalService(offSvc)
	if trafficproxy.HighRiskGateInstalled() {
		t.Fatal("hook survived a swap to an off-mode service")
	}

	// Swap back to the enforce service: the hook wraps it again.
	s.SetApprovalService(enforceSvc)
	if !trafficproxy.HighRiskGateInstalled() {
		t.Fatal("hook not re-installed after swapping back to enforce")
	}

	// Clearing the service disables the pipeline end to end.
	s.SetApprovalService(nil)
	if trafficproxy.HighRiskGateInstalled() {
		t.Fatal("hook survived clearing the approval service")
	}
}
