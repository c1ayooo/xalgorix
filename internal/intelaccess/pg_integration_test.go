package intelaccess

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
)

// PG integration: the SQL is the production authority, so the shape rules
// (unique identity, state never downgraded, expiry at read time, grants only
// grow) must be asserted against a real database.
func TestPGStoreDeriveConfirmList(t *testing.T) {
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	scanID := fmt.Sprintf("intelaccess-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM intel_access WHERE scan_id = $1`, scanID)
	})

	item := intelligence.Item{
		Fingerprint: "fp-derive-1",
		Kind:        intelligence.KindCredential,
		Endpoint:    "https://App.Example.com/admin",
	}
	if n, err := store.DeriveScan(ctx, scanID, []intelligence.Item{item}); err != nil || n != 1 {
		t.Fatalf("derive = %d, %v", n, err)
	}
	// Idempotent: a second derivation keeps one row.
	if _, err := store.DeriveScan(ctx, scanID, []intelligence.Item{item}); err != nil {
		t.Fatalf("re-derive: %v", err)
	}
	access, ok, err := store.Get(ctx, scanID, item.Fingerprint)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if access.State != StateProposed || len(access.Targets) != 1 || access.Targets[0] != "app.example.com:443" {
		t.Fatalf("derived access = %+v", access)
	}

	// Proposed rows are not usable.
	if usable, err := store.ListUsable(ctx, scanID, "", 10); err != nil || len(usable) != 0 {
		t.Fatalf("usable before confirm = %+v err=%v", usable, err)
	}

	if _, err := store.Confirm(ctx, ConfirmInput{
		ScanID: scanID, Fingerprint: item.Fingerprint, Kind: KindCredential,
		By: "operator", Targets: []string{"10.0.177.9:22"},
	}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	usable, err := store.ListUsable(ctx, scanID, "", 10)
	if err != nil || len(usable) != 1 {
		t.Fatalf("usable after confirm = %+v err=%v", usable, err)
	}
	if !containsTarget(usable[0].Targets, "app.example.com:443") || !containsTarget(usable[0].Targets, "10.0.177.9:22") {
		t.Fatalf("confirmed targets = %v", usable[0].Targets)
	}

	// Re-derivation must not downgrade the operator's endorsement.
	if _, err := store.DeriveScan(ctx, scanID, []intelligence.Item{item}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := store.Get(ctx, scanID, item.Fingerprint); got.State != StateConfirmed {
		t.Fatalf("re-derive downgraded state: %+v", got)
	}

	// Expiry is evaluated at read time.
	past := time.Now().UTC().Add(-time.Minute)
	if _, err := store.Confirm(ctx, ConfirmInput{
		ScanID: scanID, Fingerprint: item.Fingerprint, Kind: KindCredential, By: "operator", ExpiresAt: &past,
	}); err != nil {
		t.Fatal(err)
	}
	if usable, _ := store.ListUsable(ctx, scanID, "", 10); len(usable) != 0 {
		t.Fatalf("expired row still usable: %+v", usable)
	}

	// Revoke needs a reason and is inert afterwards.
	if err := store.Revoke(ctx, scanID, item.Fingerprint, ""); err == nil {
		t.Fatal("revoke without reason must be rejected")
	}
	if err := store.Revoke(ctx, scanID, item.Fingerprint, "credential rotated"); err != nil {
		t.Fatal(err)
	}
	if usable, _ := store.ListUsable(ctx, scanID, "", 10); len(usable) != 0 {
		t.Fatalf("revoked row usable: %+v", usable)
	}
}
