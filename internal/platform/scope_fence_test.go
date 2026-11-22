package platform

// Fence shape validation lives in the repository layer so BOTH storage modes
// (file snapshot + PostgreSQL jsonb) refuse the same malformed boundary at the
// same point — the runtime check in internal/web must never receive a fence it
// would have to interpret loosely.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestValidateScopeFence(t *testing.T) {
	targets := []string{"acme.example"}
	cases := []struct {
		name    string
		scope   Scope
		wantErr bool
	}{
		{name: "no fence", scope: Scope{Targets: targets}},
		{
			name:  "enforced fence with targets",
			scope: Scope{Targets: targets, Fence: &Fence{Enforce: true}},
		},
		{
			name:    "enforced fence without targets",
			scope:   Scope{Targets: []string{}, Fence: &Fence{Enforce: true}},
			wantErr: true,
		},
		{
			name:    "port-carrying deny target",
			scope:   Scope{Targets: targets, Fence: &Fence{Enforce: true, DenyTargets: []string{"billing.acme.example:443"}}},
			wantErr: true,
		},
		{
			name:    "deny target with a path",
			scope:   Scope{Targets: targets, Fence: &Fence{Enforce: true, DenyTargets: []string{"acme.example/internal"}}},
			wantErr: true,
		},
		{
			name:    "unknown http method",
			scope:   Scope{Targets: targets, Fence: &Fence{Enforce: true, AllowHTTPMethods: []string{"TRACE2"}}},
			wantErr: true,
		},
		{
			name:  "lower-case method is accepted",
			scope: Scope{Targets: targets, Fence: &Fence{Enforce: true, AllowHTTPMethods: []string{"post"}}},
		},
		{
			name:    "over-long deny pattern",
			scope:   Scope{Targets: targets, Fence: &Fence{Enforce: true, DenyCommandPatterns: []string{strings.Repeat("x", MaxFencePatternLength+1)}}},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateScope(tc.scope)
			if tc.wantErr {
				if !errors.Is(err, ErrValidation) {
					t.Fatalf("validateScope(%+v) = %v, want ErrValidation", tc.scope, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateScope(%+v) = %v, want nil", tc.scope, err)
			}
		})
	}
}

func TestNormalizeScopeFence(t *testing.T) {
	scope := normalizeScope(Scope{
		Targets: []string{"acme.example"},
		Fence: &Fence{
			Enforce:             true,
			DenyTargets:         []string{" billing.acme.example ", "", "Billing.Acme.Example"},
			AllowHTTPMethods:    []string{"post", "POST", " put "},
			DenyCommandPatterns: []string{" msfconsole ", ""},
		},
	})
	if got := scope.Fence.DenyTargets; len(got) != 1 || got[0] != "billing.acme.example" {
		t.Fatalf("deny_targets = %v, want one trimmed entry", got)
	}
	if got := scope.Fence.AllowHTTPMethods; len(got) != 2 || got[0] != "POST" || got[1] != "PUT" {
		t.Fatalf("allow_http_methods = %v, want [POST PUT]", got)
	}
	if got := scope.Fence.DenyCommandPatterns; len(got) != 1 || got[0] != "msfconsole" {
		t.Fatalf("deny_command_patterns = %v", got)
	}

	// An all-empty fence normalises to nil arrays so the JSON shape stays small.
	empty := normalizeScope(Scope{Targets: []string{}, Fence: &Fence{Enforce: false, DenyTargets: []string{" "}}})
	if empty.Fence == nil || empty.Fence.DenyTargets != nil {
		t.Fatalf("empty fence = %+v, want nil arrays", empty.Fence)
	}

	// JSON round-trip (file snapshot and PG jsonb share this marshaling).
	raw, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	var back Scope
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Fence == nil || !back.Fence.Enforce || len(back.Fence.AllowHTTPMethods) != 2 {
		t.Fatalf("round-trip lost the fence: %s", raw)
	}
	// A scope written before the fence existed must still decode.
	var legacy Scope
	if err := json.Unmarshal([]byte(`{"targets":["acme.example"],"allow_ports":[443]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Fence != nil {
		t.Fatalf("legacy scope grew a fence: %+v", legacy.Fence)
	}
}

func TestValidateScopeExportedWrapper(t *testing.T) {
	// The web layer's dry-run uses ValidateScope on an un-normalised candidate.
	err := ValidateScope(Scope{
		Targets: []string{"acme.example"},
		Fence:   &Fence{Enforce: true, AllowHTTPMethods: []string{"PATCH "}},
	})
	if err != nil {
		t.Fatalf("ValidateScope = %v, want nil (trailing space is normalised)", err)
	}
}
