package intelaccess

import (
	"errors"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
)

func TestDeriveItemKindMapping(t *testing.T) {
	cases := []struct {
		item     intelligence.Item
		wantKind string
		wantOK   bool
	}{
		{intelligence.Item{Fingerprint: "a1", Kind: intelligence.KindCredential, Endpoint: "10.0.177.5:22"}, KindCredential, true},
		{intelligence.Item{Fingerprint: "a2", Kind: intelligence.KindLead, Endpoint: "https://app.test/login"}, KindPath, true},
		{intelligence.Item{Fingerprint: "a3", Kind: intelligence.KindSensitiveData, Endpoint: "10.0.0.1:443"}, "", false},
		{intelligence.Item{Fingerprint: "", Kind: intelligence.KindCredential}, "", false},
	}
	for _, tc := range cases {
		d, ok := DeriveItem(tc.item)
		if ok != tc.wantOK {
			t.Fatalf("DeriveItem(%s) ok=%v want %v", tc.item.Fingerprint, ok, tc.wantOK)
		}
		if ok && d.Kind != tc.wantKind {
			t.Fatalf("DeriveItem(%s) kind=%q want %q", tc.item.Fingerprint, d.Kind, tc.wantKind)
		}
	}
}

func TestDeriveItemTargetsAndSubject(t *testing.T) {
	d, ok := DeriveItem(intelligence.Item{
		Fingerprint: "fp1",
		Kind:        intelligence.KindCredential,
		Endpoint:    "https://App.Example.com/admin",
	})
	if !ok {
		t.Fatal("credential must derive")
	}
	if len(d.Targets) != 1 || d.Targets[0] != "app.example.com:443" {
		t.Fatalf("targets = %v, want [app.example.com:443]", d.Targets)
	}
	if d.Subject != "app.example.com" {
		t.Fatalf("subject = %q, want app.example.com", d.Subject)
	}

	// An unportable endpoint is still a fact with no grants: the row exists so
	// triage can attach operator grants later.
	d, ok = DeriveItem(intelligence.Item{
		Fingerprint: "fp2",
		Kind:        intelligence.KindCredential,
		Endpoint:    "the admin panel",
	})
	if !ok || len(d.Targets) != 0 || d.Subject != "" {
		t.Fatalf("unportable endpoint derived %+v ok=%v, want no grants", d, ok)
	}
}

func TestNormalizeAllRejectsWholeSet(t *testing.T) {
	if _, err := normalizeAll([]string{"10.0.0.1:22", "not a target"}); err == nil {
		t.Fatal("an invalid grant must fail the whole set")
	} else {
		var invalid *InvalidTargetError
		if !errors.As(err, &invalid) {
			t.Fatalf("error type = %T, want *InvalidTargetError", err)
		}
	}
	got, err := normalizeAll([]string{"10.0.0.1:22", "10.0.0.1:22", "https://app.test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "10.0.0.1:22" || got[1] != "app.test:443" {
		t.Fatalf("normalized grants = %v", got)
	}
}
