package intelaccess

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
)

func testItem(fp, kind, endpoint string) intelligence.Item {
	return intelligence.Item{Fingerprint: fp, Kind: kind, Endpoint: endpoint}
}

func deriveOne(t *testing.T, store *MemoryStore, scanID string, item intelligence.Item) {
	t.Helper()
	if _, err := store.DeriveScan(context.Background(), scanID, []intelligence.Item{item}); err != nil {
		t.Fatalf("derive: %v", err)
	}
}

func TestMemoryDeriveIsIdempotentAndNeverDowngrades(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	item := testItem("fp1", intelligence.KindCredential, "10.0.177.5:22")

	deriveOne(t, store, "scan-a", item)
	deriveOne(t, store, "scan-a", item)

	access, ok, err := store.Get(ctx, "scan-a", "fp1")
	if err != nil || !ok {
		t.Fatalf("get after derive: ok=%v err=%v", ok, err)
	}
	if access.State != StateProposed || access.Kind != KindCredential {
		t.Fatalf("derived access = %+v", access)
	}
	if len(access.Targets) != 1 || access.Targets[0] != "10.0.177.5:22" {
		t.Fatalf("derived targets = %v", access.Targets)
	}

	// Confirmation is a human decision; a later re-derivation (the next
	// conclusion) must not downgrade it.
	if _, err := store.Confirm(ctx, ConfirmInput{
		ScanID: "scan-a", Fingerprint: "fp1", Kind: KindCredential,
		Subject: access.Subject, Targets: access.Targets, By: "operator",
	}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	deriveOne(t, store, "scan-a", item)
	access, _, _ = store.Get(ctx, "scan-a", "fp1")
	if access.State != StateConfirmed || access.ConfirmedBy != "operator" {
		t.Fatalf("re-derive downgraded state: %+v", access)
	}
	if access.ConfirmedAt == nil {
		t.Fatal("confirmed row lost confirmed_at")
	}
}

func TestMemoryUsableFiltersStatesAndExpiry(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Now().UTC()
	past := now.Add(-time.Minute)

	deriveOne(t, store, "scan-a", testItem("ok-1", intelligence.KindCredential, "10.0.0.1:22"))
	deriveOne(t, store, "scan-a", testItem("expired-1", intelligence.KindCredential, "10.0.0.2:22"))
	deriveOne(t, store, "scan-a", testItem("proposed-1", intelligence.KindCredential, "10.0.0.4:22"))

	if _, err := store.Confirm(ctx, ConfirmInput{ScanID: "scan-a", Fingerprint: "ok-1", Kind: KindCredential, By: "operator"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Confirm(ctx, ConfirmInput{ScanID: "scan-a", Fingerprint: "expired-1", Kind: KindCredential, By: "operator", ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}

	usable, err := store.ListUsable(ctx, "scan-a", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(usable) != 1 || usable[0].Fingerprint != "ok-1" {
		t.Fatalf("usable = %+v, want only ok-1", usable)
	}

	// Target narrowing is exact.
	usable, _ = store.ListUsable(ctx, "scan-a", "10.0.0.1:22", 10)
	if len(usable) != 1 {
		t.Fatalf("target filter = %+v", usable)
	}
	usable, _ = store.ListUsable(ctx, "scan-a", "10.0.0.9:22", 10)
	if len(usable) != 0 {
		t.Fatalf("non-matching target returned %+v", usable)
	}

	// Expiry is judged at read time, not by a sweeper.
	if _, err := store.Confirm(ctx, ConfirmInput{ScanID: "scan-a", Fingerprint: "ok-1", Kind: KindCredential, By: "operator", ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	usable, _ = store.ListUsable(ctx, "scan-a", "", 10)
	if len(usable) != 0 {
		t.Fatalf("expired row still usable: %+v", usable)
	}
}

func TestMemoryRevokeAndGrants(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	deriveOne(t, store, "scan-a", testItem("fp1", intelligence.KindCredential, "10.0.0.1:22"))

	if err := store.Revoke(ctx, "scan-a", "missing", "operator dismissed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke missing = %v, want ErrNotFound", err)
	}
	if err := store.Revoke(ctx, "scan-a", "fp1", "operator dismissed"); err != nil {
		t.Fatal(err)
	}
	access, _, _ := store.Get(ctx, "scan-a", "fp1")
	if access.State != StateRevoked || access.RevokeReason != "operator dismissed" {
		t.Fatalf("revoked row = %+v", access)
	}

	// Confirm revives it with an explicit endorsement (operator changed their
	// mind) and clears the revoke reason.
	access, err := store.Confirm(ctx, ConfirmInput{ScanID: "scan-a", Fingerprint: "fp1", Kind: KindCredential, By: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if access.State != StateConfirmed || access.RevokeReason != "" {
		t.Fatalf("re-confirmed row = %+v", access)
	}

	if _, err := store.AddGrants(ctx, "scan-a", "fp1", []string{"https://10.0.0.9:8443/x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddGrants(ctx, "scan-a", "fp1", []string{"not a target"}); err == nil {
		t.Fatal("invalid grant must error")
	}
	access, _, _ = store.Get(ctx, "scan-a", "fp1")
	if !containsTarget(access.Targets, "10.0.0.9:8443") {
		t.Fatalf("operator grant missing: %v", access.Targets)
	}

	// Re-derivation must not delete operator grants.
	deriveOne(t, store, "scan-a", testItem("fp1", intelligence.KindCredential, "10.0.0.1:22"))
	access, _, _ = store.Get(ctx, "scan-a", "fp1")
	if !containsTarget(access.Targets, "10.0.0.9:8443") || !containsTarget(access.Targets, "10.0.0.1:22") {
		t.Fatalf("re-derive lost grants: %v", access.Targets)
	}
}

func TestMemoryStoreIsScanScoped(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	item := testItem("fp1", intelligence.KindCredential, "10.0.0.1:22")
	deriveOne(t, store, "scan-a", item)
	deriveOne(t, store, "scan-b", item)

	if _, err := store.Confirm(ctx, ConfirmInput{ScanID: "scan-a", Fingerprint: "fp1", Kind: KindCredential, By: "operator"}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ListUsable(ctx, "scan-b", "", 10); err != nil || len(got) != 0 {
		t.Fatalf("scan-b usable = %+v err=%v; confirmation must not cross scans (D8)", got, err)
	}
	if got, err := store.ListUsable(ctx, "scan-a", "", 10); err != nil || len(got) != 1 {
		t.Fatalf("scan-a usable = %+v err=%v", got, err)
	}
}
