package platform

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestFileStoreApprovalRuleLifecycle pins the CRUD contract of the
// "approve & remember" store: validation, project scoping, expiry round-trip,
// hit bookkeeping, snapshot survival, delete.
func TestFileStoreApprovalRuleLifecycle(t *testing.T) {
	store := newApprovalsFileStore(t)
	ctx := context.Background()

	project, err := store.CreateProject(ctx, "Rules P", Scope{Targets: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateProject(ctx, "Rules Other", Scope{Targets: []string{"other.com"}})
	if err != nil {
		t.Fatal(err)
	}

	expires := time.Now().Add(24 * time.Hour).UTC()
	rule, err := store.CreateApprovalRule(ctx, ApprovalRuleInput{
		ProjectID:             project.ID,
		Kind:                  ApprovalKindCommand,
		Match:                 ApprovalRuleMatch{Program: "curl", Contains: "target-x.com"},
		Reason:                "curl write flags approved once",
		CreatedBy:             "operator:admin",
		CreatedFromApprovalID: "some-approval-id",
		ExpiresAt:             &expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rule.HitCount != 0 || rule.LastHitAt != nil {
		t.Fatalf("fresh rule hit bookkeeping = %d/%v, want zero", rule.HitCount, rule.LastHitAt)
	}
	if rule.CreatedAt.IsZero() || rule.ExpiresAt == nil || !rule.ExpiresAt.Equal(expires) {
		t.Fatalf("rule timestamps not persisted: created=%v expires=%v", rule.CreatedAt, rule.ExpiresAt)
	}

	httpRule, err := store.CreateApprovalRule(ctx, ApprovalRuleInput{
		ProjectID: project.ID,
		Kind:      ApprovalKindHTTP,
		Match:     ApprovalRuleMatch{Method: "POST", Host: "api.target-x.com", PathPrefix: "api/login"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// List filters: by project and by kind.
	all, err := store.ListApprovalRules(ctx, ApprovalRuleFilter{ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("project list = %d rules, want 2", len(all))
	}
	cmdOnly, err := store.ListApprovalRules(ctx, ApprovalRuleFilter{ProjectID: project.ID, Kind: ApprovalKindCommand})
	if err != nil {
		t.Fatal(err)
	}
	if len(cmdOnly) != 1 || cmdOnly[0].ID != rule.ID {
		t.Fatalf("kind filter = %v, want the command rule", cmdOnly)
	}
	otherRules, err := store.ListApprovalRules(ctx, ApprovalRuleFilter{ProjectID: other.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(otherRules) != 0 {
		t.Fatalf("other project must see no rules, got %d", len(otherRules))
	}

	// Hit bookkeeping: increments and stamps, idempotent on missing rows.
	if err := store.RecordApprovalRuleHit(ctx, rule.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordApprovalRuleHit(ctx, rule.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordApprovalRuleHit(ctx, "missing-rule-id"); err != nil {
		t.Fatalf("hit on missing rule must be a no-op, got %v", err)
	}
	updated, err := store.ListApprovalRules(ctx, ApprovalRuleFilter{ProjectID: project.ID, Kind: ApprovalKindCommand})
	if err != nil {
		t.Fatal(err)
	}
	if updated[0].HitCount != 2 || updated[0].LastHitAt == nil {
		t.Fatalf("hit bookkeeping = %d/%v, want 2/non-nil", updated[0].HitCount, updated[0].LastHitAt)
	}

	// Snapshot round-trip: reopen the store and check everything survives.
	dir := store.path[:len(store.path)-len("/_platform/platform.json")]
	reopened, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	survived, err := reopened.ListApprovalRules(ctx, ApprovalRuleFilter{ProjectID: project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(survived) != 2 {
		t.Fatalf("snapshot round-trip lost rules: %+v", survived)
	}
	var cmdRule *ApprovalRule
	for i := range survived {
		if survived[i].ID == rule.ID {
			cmdRule = &survived[i]
		}
	}
	if cmdRule == nil || cmdRule.HitCount != 2 || cmdRule.Match.Program != "curl" || cmdRule.LastHitAt == nil {
		t.Fatalf("snapshot round-trip lost rule state: %+v", survived)
	}

	// Delete: second delete is ErrNotFound.
	if err := reopened.DeleteApprovalRule(ctx, httpRule.ID); err != nil {
		t.Fatal(err)
	}
	if err := reopened.DeleteApprovalRule(ctx, httpRule.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete = %v, want ErrNotFound", err)
	}
}

// TestFileStoreApprovalRuleValidation pins the kind/match coherence gate.
func TestFileStoreApprovalRuleValidation(t *testing.T) {
	store := newApprovalsFileStore(t)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}

	cases := []ApprovalRuleInput{
		{ProjectID: project.ID, Kind: "other", Match: ApprovalRuleMatch{Program: "curl"}},
		{ProjectID: "", Kind: ApprovalKindCommand, Match: ApprovalRuleMatch{Program: "curl"}},
		{ProjectID: project.ID, Kind: ApprovalKindCommand, Match: ApprovalRuleMatch{}},
		{ProjectID: project.ID, Kind: ApprovalKindHTTP, Match: ApprovalRuleMatch{Method: "POST"}},
		{ProjectID: project.ID, Kind: ApprovalKindHTTP, Match: ApprovalRuleMatch{Host: "x.com"}},
		{ProjectID: "no-such-project", Kind: ApprovalKindCommand, Match: ApprovalRuleMatch{Program: "curl"}},
	}
	for i, in := range cases {
		if _, err := store.CreateApprovalRule(ctx, in); !errors.Is(err, ErrValidation) && !errors.Is(err, ErrNotFound) {
			t.Errorf("case %d: err = %v, want ErrValidation/ErrNotFound", i, err)
		}
	}
	// Expired-at creation is allowed (match-time semantics, operator sees it).
	past := time.Now().Add(-time.Hour)
	if _, err := store.CreateApprovalRule(ctx, ApprovalRuleInput{
		ProjectID: project.ID,
		Kind:      ApprovalKindCommand,
		Match:     ApprovalRuleMatch{Program: "curl"},
		ExpiresAt: &past,
	}); err != nil {
		t.Errorf("expired-at creation must be accepted, got %v", err)
	}
}
