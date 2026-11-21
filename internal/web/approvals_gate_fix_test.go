package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xalgord/xalgorix/v4/internal/config"
	"github.com/xalgord/xalgorix/v4/internal/platform"
	"github.com/xalgord/xalgorix/v4/internal/scanctx"
	"github.com/xalgord/xalgorix/v4/internal/tools"
)

// utf8ReplacementChar mirrors utf8.RuneError for the validity probe.
const utf8ReplacementChar = utf8.RuneError

// ── HIGH-1: by-value credential redaction across all payload surfaces ────────

// TestApprovalPayloadByValueRedaction pins the targetAuthB leak class: the
// operator's second-account credential appears in the request the model
// sends (body / command / header / URL userinfo), and the approval payload
// must be redacted on ALL THREE surfaces — the persisted row, the WS event
// buffered for replay, and the GET /api/approvals response — before anything
// else can observe it.
func TestApprovalPayloadByValueRedaction(t *testing.T) {
	s, store := newEnforceGateServer(t, 5, 1)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inst := registerGateInstance(t, s, "inst-secret")
	sctx := scanctx.New("gate-secret", t.TempDir())
	executed := 0

	// Bind the operator's credential registry — the same list
	// agent.prepareScanEnvironment registers for targetAuthB telemetry
	// redaction (httpclient.ParseAuthHeaders("Bearer <token>") yields this
	// exact value). Bind on the adapter the registry actually holds.
	const credential = "Bearer supersecret-b-token-99"
	reg, gate := gateRegistry(t, s, "inst-secret", sctx, &executed)
	gate.BindSecrets(func() []string { return []string{credential} })

	// One regulated call whose args carry the credential in every payload
	// surface the approval row can show, plus an embedded-credential URL.
	resCh := make(chan tools.Result, 1)
	go func() {
		res, err := reg.Execute("terminal_execute", map[string]string{
			"command": `curl -X POST https://admin:sup3rsecret@example.com/wp-login.php -H "Authorization: ` + credential + `" -d 'password=` + credential + `&x=1'`,
		})
		if err != nil {
			t.Errorf("execute: %v", err)
		}
		resCh <- res
	}()
	waitForPendingRows(t, store, project.ID, 1, "redacted submit")

	// ── surface 1: the persisted row ──
	rows, _ := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "pending", ProjectID: project.ID})
	row := rows[0]
	for _, surface := range []string{row.Payload.Command, row.Payload.BodyExcerpt} {
		if strings.Contains(surface, "supersecret-b-token-99") {
			t.Errorf("credential leaked in stored payload: %q", surface)
		}
		if strings.Contains(surface, "admin:sup3rsecret@") {
			t.Errorf("URL userinfo not masked in stored payload: %q", surface)
		}
	}
	// The command carries both the credential (by-value) and the credential
	// userinfo (structural) — both masked.
	if !strings.Contains(row.Payload.Command, redactionMarker) {
		t.Errorf("by-value redaction marker missing in stored command: %q", row.Payload.Command)
	}
	// Header value: by-name mask (Authorization is secret-class) AND the
	// credential value must be gone from it.
	auth := row.Payload.Headers["authorization"]
	if strings.Contains(auth, credential) || strings.Contains(auth, "supersecret") {
		t.Errorf("credential leaked in stored header: %q", auth)
	}

	// ── surface 2: the WS event replayed to the live feed ──
	// (Poll: the broadcast lands right after CreateApproval — under full-suite
	// disk load the store row can become visible a beat earlier. The buffer
	// is guarded by inst.mu — the same lock broadcastToInstance appends under,
	// ws_hub.go:296-317.)
	found := false
	for i := 0; i < 300 && !found; i++ {
		inst.mu.RLock()
		events := append([]WSEvent(nil), inst.events...)
		inst.mu.RUnlock()
		for _, evt := range events {
			if evt.Type == "approval_requested" && evt.Approval != nil && evt.Approval.ID == row.ID {
				found = true
				for _, surface := range []string{evt.Approval.Payload.Command, evt.Approval.Payload.BodyExcerpt, evt.Approval.Payload.URL, evt.Approval.Payload.Headers["authorization"]} {
					if strings.Contains(surface, "supersecret-b-token-99") {
						t.Errorf("credential leaked in WS event payload: %q", surface)
					}
				}
			}
		}
		if !found {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !found {
		t.Fatal("approval_requested event missing from the instance buffer")
	}

	// ── surface 3: the API response ──
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/approvals?status=pending&project_id="+project.ID, nil)
	s.handleApprovals(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list code = %d", rr.Code)
	}
	var body struct {
		Approvals []platform.Approval `json:"approvals"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Approvals) != 1 {
		t.Fatalf("api list = %d rows, want 1", len(body.Approvals))
	}
	api := body.Approvals[0]
	for _, surface := range []string{api.Payload.Command, api.Payload.BodyExcerpt, api.Payload.URL, api.Payload.Headers["authorization"]} {
		if strings.Contains(surface, "supersecret-b-token-99") {
			t.Errorf("credential leaked in API response: %q", surface)
		}
	}

	// The whole thing flows through the operator decision normally.
	if _, err := gateServiceFor(t, s).Decide(ctx, row.ID, platform.ApprovalApproved, "operator", ""); err != nil {
		t.Fatalf("decide: %v", err)
	}
	select {
	case res := <-resCh:
		if !res.Success {
			t.Errorf("approved call must execute: %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("execute never resumed after the approval")
	}
}

// ── MEDIUM-1: startup reconciliation of orphaned approvals ──────────────────

// TestReconcilePendingStartup pins the ghost-row cleanup: rows from a previous
// process (no live waiter can exist) flip at startup — deadline elapsed →
// timeout (decided_by=timeout), everything else → cancelled
// (decided_by=system, reason "startup reconciliation"). A second call is a
// no-op (one-shot guard).
func TestReconcilePendingStartup(t *testing.T) {
	s, store := newApprovalsTestServer(t, &config.Config{})
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Seed rows FIRST, then a FRESH service (the constructor built one that
	// already reconciled the empty store during SetPlatformStore wiring).
	expired := seedApproval(t, store, project.ID, "", "inst-dead",
		platform.ApprovalKindCommand, time.Now().Add(-time.Minute).Format(time.RFC3339))
	fresh := seedApproval(t, store, project.ID, "", "inst-ghost",
		platform.ApprovalKindHTTP, time.Now().Add(time.Hour).Format(time.RFC3339))
	svc := newApprovalService(s)
	s.SetApprovalService(svc)

	svc.ReconcilePending()

	got, err := store.GetApproval(ctx, expired.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != platform.ApprovalTimeout || got.DecidedBy != "timeout" || got.DecidedAt == nil {
		t.Errorf("expired row = %+v, want timeout decided_by=timeout", got)
	}
	got, err = store.GetApproval(ctx, fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != platform.ApprovalCancelled || got.DecidedBy != "system" || got.Reason != "startup reconciliation" {
		t.Errorf("orphan row = %+v, want cancelled decided_by=system reason=startup reconciliation", got)
	}

	// One-shot: a second reconcile must not touch rows again.
	rowsAfter, _ := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "pending"})
	svc.ReconcilePending()
	rowsAgain, _ := store.ListApprovals(ctx, platform.ApprovalFilter{Status: "pending"})
	if len(rowsAfter) != 0 || len(rowsAgain) != 0 {
		t.Errorf("reconcile left %d/%d pending rows", len(rowsAfter), len(rowsAgain))
	}

	// pending_total is clean (no OpsPulse pollution).
	summary, err := store.PendingSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 0 {
		t.Errorf("pending_total after reconcile = %d, want 0", summary.Total)
	}
}

// ── MEDIUM-3: a wedged store must fail the gate open ─────────────────────────

// blockingStore wraps a real store and blocks CreateApproval until its
// context dies — the "PostgreSQL black hole" scenario.
type blockingStore struct {
	platform.Repository
	release chan struct{}
}

func (b *blockingStore) CreateApproval(ctx context.Context, in platform.ApprovalInput) (*platform.Approval, error) {
	select {
	case <-b.release:
		return b.Repository.CreateApproval(ctx, in)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestGateFailsOpenWhenStoreBlocks pins the charter §6.1 posture: a store
// call that wedges must fail the gate OPEN within the store-call deadline —
// never park the tool call until the operator timeout.
func TestGateFailsOpenWhenStoreBlocks(t *testing.T) {
	s, store := newEnforceGateServer(t, 600, 1)
	original := approvalStoreCallTimeout
	approvalStoreCallTimeout = 150 * time.Millisecond
	t.Cleanup(func() { approvalStoreCallTimeout = original })

	release := make(chan struct{})
	s.SetPlatformStore(&blockingStore{Repository: store, release: release})
	t.Cleanup(func() { close(release) })

	registerGateInstance(t, s, "inst-blocked")
	sctx := scanctx.New("gate-blocked", t.TempDir())

	start := time.Now()
	outcome := gateServiceFor(t, s).submit("inst-blocked", sctx, commandVerdict("nmap -sV target"), nil)
	if !outcome.Approved {
		t.Fatalf("wedged store must fail OPEN, got %+v", outcome)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("fail-open took %s, want ~%s", elapsed, approvalStoreCallTimeout)
	}
	// The reserved slot was released on the failed create — a second submit
	// proceeds immediately instead of queueing behind a phantom slot.
	outcome2 := gateServiceFor(t, s).submit("inst-blocked", sctx, commandVerdict("dig example.com"), nil)
	if !outcome2.Approved {
		t.Fatalf("second submit after fail-open must also proceed (slot released), got %+v", outcome2)
	}
}

// ── L3: rune-safe byte-bounded truncation ────────────────────────────────────

func TestTruncateRunesByteBound(t *testing.T) {
	// 5000 CJK code points = 15000 bytes; the excerpt must stay ≤2048 BYTES
	// (not ~6144 like a code-point cut) and remain valid UTF-8.
	cjk := strings.Repeat("字", 5000)
	got := truncateRunes(cjk, maxApprovalBodyBytes)
	if len(got) > maxApprovalBodyBytes {
		t.Errorf("CJK excerpt = %d bytes, want ≤%d (byte bound, not code points)", len(got), maxApprovalBodyBytes)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncation marker missing")
	}
	for _, r := range got {
		if r == utf8ReplacementChar {
			t.Fatalf("truncation produced invalid UTF-8: %q", got[len(got)-20:])
		}
	}
	// ASCII long input: bound and marker hold.
	ascii := strings.Repeat("A", 5000)
	got = truncateRunes(ascii, maxApprovalBodyBytes)
	if len(got) != maxApprovalBodyBytes {
		t.Errorf("ASCII excerpt = %d bytes, want exactly %d", len(got), maxApprovalBodyBytes)
	}
	if !strings.HasSuffix(got, "...") {
		t.Error("ASCII truncation marker missing")
	}
	// Short input passes through untouched.
	if got := truncateRunes("short", maxApprovalBodyBytes); got != "short" {
		t.Errorf("short input changed: %q", got)
	}
	// Tiny ceiling stays byte-bounded.
	if got := truncateRunes("字字字", 4); len(got) > 4 {
		t.Errorf("tiny ceiling excerpt = %d bytes, want ≤4", len(got))
	}
}
