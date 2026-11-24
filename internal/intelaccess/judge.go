// judge.go — the single authorization decision for one dispatch (INTEL_HANDOFF_SPEC
// M3 / D3 / D9).
//
// Decision order is fixed and evaluated once, before a worker is dispatched:
//
//  1. DenyTargets      → deny (no application is ever offered for these).
//  2. fence off        → allow. Without an enforced fence there is no boundary
//     to cross, which is the pre-M3 behavior byte-for-byte.
//  3. scope hit        → allow (reason in_scope).
//  4. usable intel grant + live capability → allow and consume one use.
//  5. usable intel grant without capability → needs_capability (the caller
//     parks the intent and the operator decides).
//  6. otherwise        → deny out_of_scope.
//
// The judge never widens scope: a capability is bound to (intent, exact
// target), and capacity/expiry are re-checked at consume time.

package intelaccess

import (
	"context"
	"net"
	"strings"
	"time"
)

// Reason is the auditable outcome of one decision.
type Reason string

const (
	ReasonInScope         Reason = "in_scope"
	ReasonCapability      Reason = "capability"
	ReasonNeedsCapability Reason = "needs_capability"
	ReasonOutOfScope      Reason = "out_of_scope"
	ReasonDenyTarget      Reason = "deny_target"
	ReasonNoScope         Reason = "no_scope"
	ReasonFenceOff        Reason = "fence_off"
)

// Decision is one judgement. Allow=false with ReasonNeedsCapability means
// "park and ask"; every other deny is final for this dispatch.
type Decision struct {
	Allow        bool   `json:"allow"`
	Reason       Reason `json:"reason"`
	Detail       string `json:"detail,omitempty"`
	ViaAccess    string `json:"via_access,omitempty"`
	AccessID     int64  `json:"access_id,omitempty"`
	CapabilityID int64  `json:"capability_id,omitempty"`
}

// ScopeChecker is the project boundary predicate. agent.EngagementScope
// implements all three through its single matcher, so the judge, the dispatch
// path and the agent guard cannot disagree (XALG020).
type ScopeChecker interface {
	DestinationAllowed(raw string) (bool, string)
	DestinationDenied(raw string) (bool, string)
	// Configured reports whether an allow list exists at all. A scope intent
	// without an enforced scope list stays undispatchable (pre-M3 rule).
	Configured() bool
}

// DecisionInput is one dispatch target to judge.
type DecisionInput struct {
	ScanID   string
	IntentID int64
	Target   string
	// RequireScope is true for target_kind=scope intents, which may not be
	// dispatched without an enforced scope list.
	RequireScope bool
}

// Judge is the one authorization implementation for dispatch. It is safe for
// concurrent use; the store enforces its own locking.
type Judge struct {
	store   Store
	scope   ScopeChecker
	enforce bool
}

// NewJudge builds a judge. enforce mirrors scope.fence.enforce for this run;
// a nil scope with enforce=true means "no allow list", so everything out of
// the (empty) scope needs a capability.
func NewJudge(store Store, scope ScopeChecker, enforce bool) *Judge {
	return &Judge{store: store, scope: scope, enforce: enforce}
}

// Decide evaluates one target and records the outcome in the audit projection.
// A nil judge (no projection store) allows: the pre-M3 path must keep working
// in harnesses without a DSN.
func (j *Judge) Decide(ctx context.Context, in DecisionInput) (Decision, error) {
	decision, err := j.decide(ctx, in)
	if err == nil {
		j.audit(ctx, in, decision)
	}
	return decision, err
}

func (j *Judge) decide(ctx context.Context, in DecisionInput) (Decision, error) {
	if j == nil || j.store == nil {
		return Decision{Allow: true, Reason: ReasonFenceOff}, nil
	}
	target := strings.TrimSpace(in.Target)
	if target == "" {
		return Decision{Reason: ReasonOutOfScope, Detail: "intent names no dispatchable target"}, nil
	}
	if j.scope != nil {
		if denied, why := j.scope.DestinationDenied(target); denied {
			return Decision{Reason: ReasonDenyTarget, Detail: why}, nil
		}
	}
	if !j.enforce {
		if in.RequireScope && (j.scope == nil || !j.scope.Configured()) {
			// A scope intent without an enforced scope list cannot be
			// dispatched; this is the pre-M3 rule, kept verbatim.
			return Decision{Reason: ReasonNoScope, Detail: "no enforced scope targets for this run"}, nil
		}
		return Decision{Allow: true, Reason: ReasonFenceOff}, nil
	}
	if j.scope != nil {
		if ok, _ := j.scope.DestinationAllowed(target); ok {
			return Decision{Allow: true, Reason: ReasonInScope}, nil
		}
	}
	canonical, ok := NormalizeTarget(target)
	if !ok {
		// Without a structured target no capability can be bound to it, so
		// parking the intent would ask the operator an unanswerable question.
		return Decision{Reason: ReasonOutOfScope, Detail: "target is not a host:port or CIDR"}, nil
	}
	access, ok, err := j.store.FindGrant(ctx, in.ScanID, target)
	if err != nil {
		return Decision{}, err
	}
	if !ok {
		return Decision{Reason: ReasonOutOfScope, Detail: "no usable intel grants this target"}, nil
	}
	capability, ok, err := j.store.ConsumeCapability(ctx, in.ScanID, in.IntentID, canonical)
	if err != nil {
		return Decision{}, err
	}
	if ok {
		return Decision{
			Allow: true, Reason: ReasonCapability,
			ViaAccess: access.Fingerprint, AccessID: access.ID, CapabilityID: capability.ID,
		}, nil
	}
	return Decision{
		Reason: ReasonNeedsCapability, ViaAccess: access.Fingerprint, AccessID: access.ID,
		Detail: "a capability is required for " + canonical,
	}, nil
}

// audit writes one projection row. It is best-effort by contract: the receipt
// is not the authority, so a broken audit table must never block a dispatch.
func (j *Judge) audit(ctx context.Context, in DecisionInput, decision Decision) {
	if j == nil || j.store == nil {
		return
	}
	kind := DecisionDeny
	switch {
	case decision.Allow:
		kind = DecisionAllow
	case decision.Reason == ReasonNeedsCapability:
		kind = DecisionNeedsCapability
	}
	_ = j.store.RecordUse(ctx, UseRecord{
		ScanID:       in.ScanID,
		IntentID:     in.IntentID,
		Fingerprint:  decision.ViaAccess,
		AccessID:     decision.AccessID,
		Target:       strings.TrimSpace(in.Target),
		Decision:     kind,
		Reason:       string(decision.Reason),
		CapabilityID: decision.CapabilityID,
	})
}

// grantMatches reports whether one canonical target is covered by a grant:
// exact match, or a CIDR grant containing the target's address.
func grantMatches(targets []string, canonical string) bool {
	if canonical == "" {
		return false
	}
	host := hostOf(canonical)
	ip := net.ParseIP(host)
	for _, t := range targets {
		if t == canonical {
			return true
		}
		if ip == nil || !strings.Contains(t, "/") {
			continue
		}
		if _, network, err := net.ParseCIDR(t); err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

// UseRecord is one audited authorization decision (or detail fetch). It is a
// projection: writing it never blocks the decision itself.
type UseRecord struct {
	ID            int64     `json:"id,omitempty"`
	ScanID        string    `json:"scan_id"`
	IntentID      int64     `json:"intent_id,omitempty"`
	Fingerprint   string    `json:"fingerprint,omitempty"`
	AccessID      int64     `json:"access_id,omitempty"`
	Target        string    `json:"target,omitempty"`
	Decision      string    `json:"decision"`
	Reason        string    `json:"reason"`
	CapabilityID  int64     `json:"capability_id,omitempty"`
	UsesLeftAfter *int      `json:"uses_left_after,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// Audit decisions (the vocabulary the audit table's CHECK enforces).
const (
	DecisionAllow           = "allow"
	DecisionDeny            = "deny"
	DecisionNeedsCapability = "needs_capability"
	DecisionDetailFetch     = "detail_fetch"
)
