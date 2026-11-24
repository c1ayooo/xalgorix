package intelaccess

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
)

// fakeScope is a ScopeChecker with explicit answers, so the judge tests pin the
// decision order without depending on the agent matcher.
type fakeScope struct {
	allowed    map[string]bool
	denied     map[string]bool
	configured bool
}

func (f fakeScope) DestinationAllowed(raw string) (bool, string) {
	if f.allowed[raw] {
		return true, ""
	}
	return false, "outside the scope"
}

func (f fakeScope) DestinationDenied(raw string) (bool, string) {
	if f.denied[raw] {
		return true, "denied by the fence"
	}
	return false, ""
}

func (f fakeScope) Configured() bool { return f.configured }

// judgeFixture derives one confirmed credential whose grants include the
// out-of-scope target 10.0.177.9:22.
func judgeFixture(t *testing.T) (*MemoryStore, string, int64) {
	t.Helper()
	ctx := context.Background()
	store := NewMemoryStore()
	const scanID = "judge-scan"
	item := intelligence.Item{
		Fingerprint: "fp1", Kind: intelligence.KindCredential, Endpoint: "10.0.177.5:22",
	}
	if _, err := store.DeriveScan(ctx, scanID, []intelligence.Item{item}); err != nil {
		t.Fatal(err)
	}
	access, err := store.Confirm(ctx, ConfirmInput{
		ScanID: scanID, Fingerprint: "fp1", Kind: KindCredential,
		Targets: []string{"10.0.177.9:22"}, By: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, scanID, access.ID
}

func TestJudgeFenceOffAllows(t *testing.T) {
	store, scanID, _ := judgeFixture(t)
	judge := NewJudge(store, nil, false)
	decision, err := judge.Decide(context.Background(), DecisionInput{
		ScanID: scanID, IntentID: 1, Target: "10.0.177.9:22",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allow || decision.Reason != ReasonFenceOff {
		t.Fatalf("fence-off decision = %+v", decision)
	}
	// A scope intent without an enforced scope list stays undispatchable.
	decision, _ = judge.Decide(context.Background(), DecisionInput{
		ScanID: scanID, IntentID: 1, Target: "10.0.177.9:22", RequireScope: true,
	})
	if decision.Allow || decision.Reason != ReasonNoScope {
		t.Fatalf("scope intent without scope list = %+v", decision)
	}
}

func TestJudgeInScopeAndDenyTarget(t *testing.T) {
	store, scanID, _ := judgeFixture(t)
	scope := fakeScope{
		allowed:    map[string]bool{"10.0.177.9:22": true},
		denied:     map[string]bool{"10.0.177.5:22": true},
		configured: true,
	}
	judge := NewJudge(store, scope, true)

	decision, _ := judge.Decide(context.Background(), DecisionInput{ScanID: scanID, IntentID: 1, Target: "10.0.177.9:22"})
	if !decision.Allow || decision.Reason != ReasonInScope {
		t.Fatalf("in-scope decision = %+v", decision)
	}
	// DenyTargets is a hard stop even when the target is otherwise in scope.
	scope.allowed["10.0.177.5:22"] = true
	decision, _ = judge.Decide(context.Background(), DecisionInput{ScanID: scanID, IntentID: 1, Target: "10.0.177.5:22"})
	if decision.Allow || decision.Reason != ReasonDenyTarget {
		t.Fatalf("deny-target decision = %+v", decision)
	}
}

func TestJudgeNeedsCapabilityThenConsumesOneUse(t *testing.T) {
	ctx := context.Background()
	store, scanID, accessID := judgeFixture(t)
	judge := NewJudge(store, fakeScope{configured: true}, true)

	decision, err := judge.Decide(ctx, DecisionInput{ScanID: scanID, IntentID: 7, Target: "10.0.177.9:22"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allow || decision.Reason != ReasonNeedsCapability || decision.ViaAccess != "fp1" {
		t.Fatalf("without capability = %+v", decision)
	}

	if _, err := store.IssueCapabilities(ctx, IssueInput{
		ScanID: scanID, IntentID: 7, AccessID: accessID,
		Targets: []string{"10.0.177.9:22"}, UsesLeft: 1, TTL: time.Minute, IssuedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	decision, err = judge.Decide(ctx, DecisionInput{ScanID: scanID, IntentID: 7, Target: "10.0.177.9:22"})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allow || decision.Reason != ReasonCapability || decision.CapabilityID == 0 {
		t.Fatalf("with capability = %+v", decision)
	}
	// The counters moved with the consumption.
	row, _, _ := store.Get(ctx, scanID, "fp1")
	if row.UseCount != 1 || row.LastUsedAt == nil || row.State != StateActive {
		t.Fatalf("access after use = %+v", row)
	}
	// One use means the next dispatch asks again.
	decision, _ = judge.Decide(ctx, DecisionInput{ScanID: scanID, IntentID: 7, Target: "10.0.177.9:22"})
	if decision.Allow || decision.Reason != ReasonNeedsCapability {
		t.Fatalf("exhausted capability = %+v", decision)
	}
	// Another intent cannot ride the first intent's capability.
	decision, _ = judge.Decide(ctx, DecisionInput{ScanID: scanID, IntentID: 8, Target: "10.0.177.9:22"})
	if decision.Allow {
		t.Fatalf("capability leaked across intents: %+v", decision)
	}
}

func TestJudgeExpiredCapabilityAndOutOfScope(t *testing.T) {
	ctx := context.Background()
	store, scanID, accessID := judgeFixture(t)
	judge := NewJudge(store, fakeScope{configured: true}, true)

	// No grant at all → final refusal.
	decision, _ := judge.Decide(ctx, DecisionInput{ScanID: scanID, IntentID: 1, Target: "10.9.9.9:22"})
	if decision.Allow || decision.Reason != ReasonOutOfScope {
		t.Fatalf("no-grant decision = %+v", decision)
	}
	// A bare host has no canonical form to bind a capability to → final refusal.
	decision, _ = judge.Decide(ctx, DecisionInput{ScanID: scanID, IntentID: 1, Target: "10.0.177.9"})
	if decision.Allow || decision.Reason != ReasonOutOfScope {
		t.Fatalf("bare-host decision = %+v", decision)
	}
	// An instantly-expired capability is inert.
	if _, err := store.IssueCapabilities(ctx, IssueInput{
		ScanID: scanID, IntentID: 1, AccessID: accessID,
		Targets: []string{"10.0.177.9:22"}, UsesLeft: 3, TTL: time.Nanosecond, IssuedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	decision, _ = judge.Decide(ctx, DecisionInput{ScanID: scanID, IntentID: 1, Target: "10.0.177.9:22"})
	if decision.Allow || decision.Reason != ReasonNeedsCapability {
		t.Fatalf("expired capability = %+v", decision)
	}
}

func TestJudgeCIDRGrantCoversAddress(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	const scanID = "judge-cidr"
	item := intelligence.Item{Fingerprint: "fp-cidr", Kind: intelligence.KindCredential, Endpoint: "10.0.177.5:22"}
	if _, err := store.DeriveScan(ctx, scanID, []intelligence.Item{item}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Confirm(ctx, ConfirmInput{
		ScanID: scanID, Fingerprint: "fp-cidr", Kind: KindCredential,
		Targets: []string{"10.0.177.0/24"}, By: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	judge := NewJudge(store, fakeScope{configured: true}, true)
	decision, _ := judge.Decide(ctx, DecisionInput{ScanID: scanID, IntentID: 1, Target: "10.0.177.42:22"})
	if decision.Reason != ReasonNeedsCapability {
		t.Fatalf("CIDR grant did not cover the address: %+v", decision)
	}
}

func TestIssueCapabilitiesNormalizesAndBatches(t *testing.T) {
	ctx := context.Background()
	store, scanID, accessID := judgeFixture(t)
	caps, err := store.IssueCapabilities(ctx, IssueInput{
		ScanID: scanID, IntentID: 3, AccessID: accessID,
		Targets:  []string{"https://10.0.177.9:8443/x", "10.0.177.9:22"},
		UsesLeft: 2, TTL: time.Minute, IssuedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) != 2 || caps[0].BatchID == "" || caps[0].BatchID != caps[1].BatchID {
		t.Fatalf("capabilities = %+v (want one batch)", caps)
	}
	for _, capability := range caps {
		if strings.HasPrefix(capability.Target, "https://") {
			t.Fatalf("target not canonical: %q", capability.Target)
		}
	}
	if _, err := store.IssueCapabilities(ctx, IssueInput{
		ScanID: scanID, IntentID: 3, AccessID: accessID,
		Targets: []string{"not a target"}, UsesLeft: 1, TTL: time.Minute, IssuedBy: "operator",
	}); err == nil {
		t.Fatal("invalid target must be rejected")
	}
	if _, err := store.IssueCapabilities(ctx, IssueInput{
		ScanID: scanID, IntentID: 3, AccessID: accessID,
		Targets: []string{"10.0.177.9:22"}, UsesLeft: 0, TTL: time.Minute, IssuedBy: "operator",
	}); err == nil {
		t.Fatal("uses_left=0 must be rejected")
	}
}

// Every decision leaves an audit row; the newest first order is what the panel
// and the report read.
func TestJudgeWritesAuditRows(t *testing.T) {
	ctx := context.Background()
	store, scanID, accessID := judgeFixture(t)
	judge := NewJudge(store, fakeScope{configured: true}, true)

	if _, err := judge.Decide(ctx, DecisionInput{ScanID: scanID, IntentID: 5, Target: "10.0.177.9:22"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IssueCapabilities(ctx, IssueInput{
		ScanID: scanID, IntentID: 5, AccessID: accessID,
		Targets: []string{"10.0.177.9:22"}, UsesLeft: 1, TTL: time.Minute, IssuedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := judge.Decide(ctx, DecisionInput{ScanID: scanID, IntentID: 5, Target: "10.0.177.9:22"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUse(ctx, UseRecord{
		ScanID: scanID, Fingerprint: "fp1", Decision: DecisionDetailFetch, Reason: "detail_fetch",
	}); err != nil {
		t.Fatal(err)
	}

	uses, err := store.ListUses(ctx, scanID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(uses) != 3 {
		t.Fatalf("audit rows = %d, want 3 (needs_capability, allow, detail_fetch)", len(uses))
	}
	if uses[0].Decision != DecisionDetailFetch {
		t.Fatalf("newest row = %+v, want detail_fetch", uses[0])
	}
	if uses[1].Decision != DecisionAllow || uses[1].Reason != string(ReasonCapability) || uses[1].CapabilityID == 0 {
		t.Fatalf("allow row = %+v", uses[1])
	}
	if uses[2].Decision != DecisionNeedsCapability || uses[2].Fingerprint != "fp1" {
		t.Fatalf("needs_capability row = %+v", uses[2])
	}
}

// Repeated dispatch failures retire a credential so later intents stop trying a
// known-bad door.
func TestRecordFailureRevokesAtThreshold(t *testing.T) {
	ctx := context.Background()
	store, scanID, _ := judgeFixture(t)

	for attempt := 1; attempt <= 3; attempt++ {
		access, revoked, err := store.RecordFailure(ctx, scanID, "fp1", 3)
		if err != nil {
			t.Fatal(err)
		}
		if access.FailCount != attempt {
			t.Fatalf("fail_count = %d, want %d", access.FailCount, attempt)
		}
		if wantRevoked := attempt == 3; revoked != wantRevoked {
			t.Fatalf("attempt %d revoked = %v, want %v", attempt, revoked, wantRevoked)
		}
	}
	access, _, _ := store.Get(ctx, scanID, "fp1")
	if access.State != StateRevoked || access.RevokeReason == "" {
		t.Fatalf("revoked access = %+v", access)
	}
	if usable, _ := store.ListUsable(ctx, scanID, "", 10); len(usable) != 0 {
		t.Fatalf("revoked access still usable: %+v", usable)
	}
	if _, _, err := store.RecordFailure(ctx, scanID, "missing", 3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown fingerprint = %v, want ErrNotFound", err)
	}
}
