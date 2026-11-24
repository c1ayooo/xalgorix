package graph

import "testing"

func TestParseContract_AbsentAndInvalid(t *testing.T) {
	if c := ParseContract(""); c.Version != 0 || c.NeedsConfirmation() {
		t.Fatalf("empty payload contract = %+v", c)
	}
	if c := ParseContract("not json"); c.Version != 0 {
		t.Fatalf("invalid payload contract = %+v", c)
	}
	if c := ParseContract(`{"task_id":"x"}`); c.Version != 0 || len(c.Preconditions) != 0 {
		t.Fatalf("payload without contract = %+v", c)
	}
}

func TestParseContract_RoundTrip(t *testing.T) {
	payload := `{"task_id":"x","contract":{
		"version":1,
		"preconditions":[{"ref_kind":"fact","ref":"cred:admin","need_state":"confirmed"}],
		"yields":[{"kind":"fact"}],
		"timebox_s":900,
		"requires_confirmation":true,
		"confirmed_at":"2026-11-23T10:00:00Z",
		"confirmed_by":"operator"
	}}`
	c := ParseContract(payload)
	if err := ValidateContract(c); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !c.Confirmed() || c.NeedsConfirmation() {
		t.Fatalf("confirmation state = confirmed:%v needs:%v", c.Confirmed(), c.NeedsConfirmation())
	}
	if got := c.YieldKinds(); len(got) != 1 || got[0] != KindFact {
		t.Fatalf("yields = %v", got)
	}
	if len(c.Preconditions) != 1 || c.Preconditions[0].Ref != "cred:admin" {
		t.Fatalf("preconditions = %+v", c.Preconditions)
	}
}

func TestParseContractStrict_RejectsMalformedShape(t *testing.T) {
	cases := []string{
		`{"contract":"not-an-object"}`,
		`{"contract":{"version":1,"preconditions":"nope"}}`,
		`{"contract":{"version":1,"yields":[{"kind":"belief"}]}}`,
		`[]`,
		`null`,
	}
	for i, payload := range cases {
		if _, err := ParseContractStrict(payload); err == nil {
			t.Fatalf("case %d accepted malformed contract %s", i, payload)
		}
	}
	if _, err := ParseContractStrict(`{"task_id":"x"}`); err != nil {
		t.Fatalf("absent contract must be valid legacy: %v", err)
	}
}

func TestValidateContract_RejectsBadShapes(t *testing.T) {
	cases := []Contract{
		{Version: 99},
		{Preconditions: []Precondition{{RefKind: KindGoal, Ref: "g", NeedState: StateMet}}},
		{Preconditions: []Precondition{{RefKind: KindFact, Ref: "", NeedState: StateConfirmed}}},
		{Preconditions: []Precondition{{RefKind: KindFact, Ref: "x", NeedState: StateProven}}},
		{Yields: []Yield{{Kind: KindBelief}}},
		{TimeboxSeconds: MaxContractTimeboxSeconds + 1},
		{ConfirmedAt: "yesterday"},
	}
	for i, c := range cases {
		if err := ValidateContract(c); err == nil {
			t.Fatalf("case %d accepted invalid contract %+v", i, c)
		}
	}
}
