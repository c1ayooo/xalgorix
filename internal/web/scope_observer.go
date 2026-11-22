package web

// scope_observer.go — the scope observer agent (v4.6).
//
// The observer is the automatic half of HITL: while a run is live (scheduled or
// operator-triggered — the observer does not distinguish trigger kind) it
// adjudicates every approval-gated call against the bound project before a
// human is asked. Three outcomes:
//
//	allow  a matching allow rule (operator "approve & remember") exempts the
//	       call — no approval row, audited as approval_rule_hit;
//	deny   a matching deny rule, or one of the built-in baseline rules
//	       (bulk destruction, uploads, OSS object writes), auto-rejects the
//	       call — a terminal row with decided_by="scope-observer" is the audit
//	       record, and the model receives the ⛔ denial text;
//	defer  everything else keeps the normal operator queue (the observer never
//	       widens a human obligation, and never overrides the project fence,
//	       which runs before it).
//
// Failure posture matches the rest of the gate: nil store / store error /
// unknown project all defer to the normal approval flow — a failed rule read
// must never auto-allow NOR auto-deny a call.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/approval"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

// scopeObservation is the observer's verdict for one call.
type scopeObservation struct {
	Decision string // approval.ScopeAllow / approval.ScopeDeny / approval.ScopeDefer
	Rule     *platform.ApprovalRule
	Reason   string
}

// scope decision vocabulary (aligns with approval.Scope* constants).
const (
	scopeAllow = "allow"
	scopeDeny  = "deny"
	scopeDefer = "defer"
)

// observeScope adjudicates one Require verdict for the instance's project.
func (s *ApprovalService) observeScope(instanceID string, verdict approval.Verdict) scopeObservation {
	if s.policy.Mode != approval.ModeEnforce {
		return scopeObservation{Decision: scopeDefer}
	}
	if verdict.Kind != approval.KindCommand && verdict.Kind != approval.KindHTTP {
		return scopeObservation{Decision: scopeDefer}
	}
	store := s.srv.currentPlatformStore()
	if store == nil {
		return scopeObservation{Decision: scopeDefer}
	}
	ctx, cancel := storeCtx()
	defer cancel()
	projectID, _, _ := s.resolveOwnership(ctx, store, instanceID)
	if projectID == "" {
		return scopeObservation{Decision: scopeDefer}
	}

	// 1. The operator's own rules — deny outranks allow regardless of listing
	//    order (a row created later must not resurrect a denied intent).
	rules, err := store.ListApprovalRules(ctx, platform.ApprovalRuleFilter{ProjectID: projectID, Kind: verdict.Kind})
	if err != nil {
		log.Printf("[scope-observer] rule read for run %s failed: %v — deferring to operator", instanceID, err)
		return scopeObservation{Decision: scopeDefer}
	}
	now := time.Now()
	var allow *platform.ApprovalRule
	for i := range rules {
		r := &rules[i]
		if r.ExpiresAt != nil && !now.Before(*r.ExpiresAt) {
			continue
		}
		if !ruleMatchesVerdict(s.policy, verdict, r) {
			continue
		}
		if r.DecisionOrAllow() == platform.ApprovalRuleDeny {
			return s.ruleDecision(instanceID, r, scopeDeny)
		}
		if allow == nil {
			allow = r
		}
	}
	if allow != nil {
		return s.ruleDecision(instanceID, allow, scopeAllow)
	}

	// 2. Built-in baseline for HTTP: the target's own control plane (bulk
	//    destruction / upload / OSS writes) is never exercised automatically.
	if verdict.Kind == approval.KindHTTP {
		if m := approval.EvaluateScopeRules(approval.DefaultScopeRules(), verdict.Method, verdict.URL, verdict.Body); m.Decision == approval.ScopeDeny {
			return scopeObservation{Decision: scopeDeny, Reason: m.Reason}
		}
	}
	return scopeObservation{Decision: scopeDefer}
}

// ruleDecision records the hit (best effort), broadcasts the audit projection
// and returns the observation.
func (s *ApprovalService) ruleDecision(instanceID string, r *platform.ApprovalRule, decision string) scopeObservation {
	ctx, cancel := storeCtx()
	if err := s.srv.currentPlatformStore().RecordApprovalRuleHit(ctx, r.ID); err != nil {
		log.Printf("[scope-observer] rule hit bookkeeping for %s: %v", r.ID, err)
	}
	cancel()
	verb := "allowed without an approval row"
	if decision == scopeDeny {
		verb = "auto-rejected by the scope observer"
	}
	s.srv.broadcastToInstance(instanceID, WSEvent{
		Type:    "approval_rule_hit",
		Content: fmt.Sprintf("Scope observer rule matched — %s %s (%s)", ruleDisplayText(r), verb, r.ID),
		Rule:    r,
	})
	return scopeObservation{
		Decision: decision,
		Rule:     r,
		Reason:   fmt.Sprintf("scope observer rule %q: %s", ruleDisplayText(r), r.Reason),
	}
}

// ruleMatchesVerdict reports whether one persisted rule matches a verdict.
func ruleMatchesVerdict(policy approval.Policy, verdict approval.Verdict, r *platform.ApprovalRule) bool {
	switch verdict.Kind {
	case approval.KindCommand:
		return policy.MatchCommandRule(verdict.Command, r.Match.Program, r.Match.Contains)
	case approval.KindHTTP:
		return approval.MatchHTTPRule(verdict.Method, verdict.URL, verdict.Body,
			r.Match.Method, r.Match.Host, r.Match.PathPrefix, r.Match.Contains)
	}
	return false
}

// submitScopeDenial records and immediately closes a call the observer denied.
// Shape mirrors submitFenceDenial: the row is the audit record, the closed state
// is what wakes the waiting tool call, and the finalize path releases the slot.
func (s *ApprovalService) submitScopeDenial(instanceID string, verdict approval.Verdict, reason string, secrets []string) (*platform.Approval, error) {
	store := s.srv.currentPlatformStore()
	if store == nil {
		return nil, fmt.Errorf("approval store unavailable")
	}
	ctx, cancel := storeCtx()
	defer cancel()
	projectID, taskID, runID := s.resolveOwnership(ctx, store, instanceID)
	input := platform.ApprovalInput{
		ProjectID:  projectID,
		TaskID:     taskID,
		RunID:      runID,
		InstanceID: instanceID,
		Kind:       verdict.Kind,
		Payload:    buildApprovalPayload(verdict, secrets),
		DeadlineAt: time.Now().Add(s.policy.Timeout),
	}
	input.Payload.Reason = reason
	row, err := store.CreateApproval(ctx, input)
	if err != nil {
		return nil, err
	}
	requested := WSEvent{
		Type:     "approval_requested",
		Content:  fmt.Sprintf("Scope observer review: %s", reason),
		Approval: row,
	}
	s.srv.broadcastToInstance(instanceID, requested)
	s.srv.appendConversationEvent(instanceID, requested)
	final, ferr := s.finalize(row, platform.ApprovalDecision{
		Decision:  platform.ApprovalRejected,
		DecidedBy: "scope-observer",
		Reason:    reason,
	})
	if ferr != nil {
		if adopted, adoptErr := s.adoptConflict(row, ferr); adoptErr == nil {
			return adopted, nil
		}
		return row, ferr
	}
	return final, nil
}

// ── preset rules ────────────────────────────────────────────────────────────

// presetRuleAuthor marks seeded baseline rows so re-seeding is idempotent and
// the UI can distinguish factory baselines from operator-authored rules.
const presetRuleAuthor = "preset:scope-observer"

// PresetScopeApprovalRules materializes the built-in baseline as ordinary
// approval_rules rows for one project, one row per (scope target host × rule),
// so the operator can see, tune, expire or delete exactly what the observer
// applies. HTTP rules need a host (match semantics), so a project without scope
// targets gets no presets — the built-in classifier still applies at runtime.
func PresetScopeApprovalRules(projectID string, scope platform.Scope) []platform.ApprovalRuleInput {
	if strings.TrimSpace(projectID) == "" || len(scope.Targets) == 0 {
		return nil
	}
	// Cap the seeded hosts: the baseline is per host × rule × method, and a
	// large wildcard scope must not flood the rules dialog. The runtime
	// classifier still covers every target.
	const maxPresetHosts = 5
	hosts := make([]string, 0, len(scope.Targets))
	seen := map[string]bool{}
	for _, target := range scope.Targets {
		host := presetTargetHost(target)
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		hosts = append(hosts, host)
		if len(hosts) >= maxPresetHosts {
			break
		}
	}
	if len(hosts) == 0 {
		return nil
	}
	// One representative row per (host × rule × write method). The full
	// feature vocabulary stays in the built-in classifier (which always runs);
	// these rows exist so the operator can see and tune the baseline in the
	// rules dialog, with the first path feature as the visible coordinate.
	presetMethods := []string{"DELETE", "POST", "PUT", "PATCH"}
	out := make([]platform.ApprovalRuleInput, 0, len(hosts)*len(approval.DefaultScopeRules())*len(presetMethods))
	for _, host := range hosts {
		for _, rule := range approval.DefaultScopeRules() {
			// require-class baselines stay in the built-in classifier: the
			// approval_rules vocabulary is allow|deny, and seeding a human
			// obligation as "allow" would silently exempt it.
			if rule.Decision != approval.ScopeDeny {
				continue
			}
			if len(rule.PathFeatures) == 0 {
				continue
			}
			feature := strings.TrimSpace(rule.PathFeatures[0])
			trimmed := strings.Trim(feature, "/")
			if trimmed == "" {
				continue
			}
			match := platform.ApprovalRuleMatch{Host: host}
			if strings.HasPrefix(feature, "/") {
				match.PathPrefix = trimmed
			} else {
				match.Contains = feature
			}
			for _, method := range presetMethods {
				match.Method = method
				out = append(out, platform.ApprovalRuleInput{
					ProjectID: projectID,
					Kind:      platform.ApprovalKindHTTP,
					Decision:  platform.NormalizeApprovalRuleDecision(string(rule.Decision)),
					Match:     match,
					Reason:    rule.Name + " (preset scope-observer baseline)",
					CreatedBy: presetRuleAuthor,
				})
			}
		}
	}
	return out
}

// presetTargetHost extracts the bare host of a scope target entry.
func presetTargetHost(target string) string {
	host, _ := targetHostPort(target)
	return strings.ToLower(strings.TrimSpace(host))
}

// seedAllScopeApprovalRules seeds every existing project once at startup. The
// first boot of a migrated deployment is the "rules table is empty" state the
// observer baseline is meant to fill.
func (s *Server) seedAllScopeApprovalRules() {
	store := s.currentPlatformStore()
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	projects, err := store.ListProjects(ctx)
	if err != nil {
		log.Printf("[scope-observer] startup preset seed: list projects: %v", err)
		return
	}
	total := 0
	for _, project := range projects {
		total += s.seedScopeApprovalRules(ctx, store, project.ID)
	}
	if total > 0 {
		log.Printf("[scope-observer] seeded %d baseline rule(s) across %d project(s)", total, len(projects))
	}
}

// seedScopeApprovalRules idempotently writes the preset rows for one project.
// Existing baseline rows with the same (decision, match) are left untouched.
func (s *Server) seedScopeApprovalRules(ctx context.Context, store platform.Repository, projectID string) int {
	if store == nil || strings.TrimSpace(projectID) == "" {
		return 0
	}
	project, err := store.GetProject(ctx, projectID)
	if err != nil {
		return 0
	}
	existing, err := store.ListApprovalRules(ctx, platform.ApprovalRuleFilter{ProjectID: projectID, Kind: platform.ApprovalKindHTTP})
	if err != nil {
		log.Printf("[scope-observer] preset seed: list rules for project %s: %v", projectID, err)
		return 0
	}
	type key struct {
		decision string
		match    string
	}
	have := make(map[key]bool, len(existing))
	for _, r := range existing {
		have[key{r.DecisionOrAllow(), r.Match.Method + "|" + r.Match.Host + "|" + r.Match.PathPrefix + "|" + r.Match.Contains}] = true
	}
	created := 0
	for _, in := range PresetScopeApprovalRules(projectID, project.Scope) {
		k := key{platform.NormalizeApprovalRuleDecision(in.Decision), in.Match.Method + "|" + in.Match.Host + "|" + in.Match.PathPrefix + "|" + in.Match.Contains}
		if have[k] {
			continue
		}
		if _, err := store.CreateApprovalRule(ctx, in); err != nil {
			log.Printf("[scope-observer] preset seed for project %s: %v", projectID, err)
			continue
		}
		have[k] = true
		created++
	}
	return created
}
