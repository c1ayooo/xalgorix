// Package goalanalysis turns a vague operator task goal into a structured,
// decidable objective draft with a single LLM call (REDESIGN §3.1; same
// discipline as 4zreco's round-0 goal decomposer). It is a leaf package: the
// LLM is injected as a plain function so callers own provider selection, and
// every degenerate reply is an ordinary error — the caller falls back to
// running the raw goal (fail-open), never on an invented objective.
package goalanalysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/xalgord/xalgorix/v4/internal/platform"
)

// AnalyzeInput carries everything the analyzer may look at. Goal is required;
// Instruction and ScopeTargets are optional context the prompt shows as
// background and authorization boundary respectively.
type AnalyzeInput struct {
	Goal         string
	Instruction  string
	ScopeTargets []string
}

// LLMFunc performs one system+user chat completion. The web layer adapts its
// llm.Client to this; tests inject fakes.
type LLMFunc func(ctx context.Context, systemPrompt, userMessage string) (string, error)

// Criterion is one proposed acceptance line before server-side normalization.
type Criterion struct {
	Kind       string
	VulnClass  string
	Capability string
	VerifyBy   string
	Required   bool
	Rationale  string
}

// Proposal is the analyzer's structured output. Statement, criteria and
// constraints mirror the operator's own words (rewritten where vague);
// Rationale/Notes exist for the UI preview and are dropped on conversion.
type Proposal struct {
	Statement   string
	Criteria    []Criterion
	Constraints []string
	Notes       string
}

// Objective converts the proposal into a platform objective of kind
// engagement. Criterion IDs stay empty — the server assigns stable C1..Cn on
// normalization, clients never choose them.
func (p *Proposal) Objective() *platform.Objective {
	if p == nil {
		return nil
	}
	obj := &platform.Objective{
		Kind:        platform.ObjectiveKindEngagement,
		Statement:   p.Statement,
		Constraints: p.Constraints,
	}
	for _, c := range p.Criteria {
		obj.Criteria = append(obj.Criteria, platform.Criterion{
			Kind:       c.Kind,
			VulnClass:  c.VulnClass,
			Capability: c.Capability,
			VerifyBy:   c.VerifyBy,
			Required:   c.Required,
		})
	}
	return obj
}

// MaxConstraints bounds the operator-stated constraint list so the prompt
// section it renders into stays readable. Normalization enforces the same
// cap (platform.MaxObjectiveConstraints).
const MaxConstraints = platform.MaxObjectiveConstraints

// systemPrompt is the analyzer discipline: final outcomes, never attack steps;
// vague goals rewritten to verifiable form; nothing invented; constraints
// self-contained. The JSON contract is restated at the end because the parser
// depends on it.
const systemPrompt = `You are a penetration-testing goal analyst. From the operator's task goal (plus optional execution notes and the authorized scope), extract the FINAL outcomes to be achieved and express them as measurable acceptance criteria. You are NOT planning the engagement: reconnaissance, scanning, exploitation steps and verification procedures are methodology, never outcomes.

Hard rules:
- An outcome is something the delivered report can settle with engine-observable evidence: an accepted finding of a vulnerability class, a proven capability, or the planned methodology coverage. Nothing else qualifies.
- Rewrite vague goals into their nearest concrete, verifiable form (e.g. "assess the overall security of acme.com" becomes "deliver at least one independently verified exploitable finding on acme.com"). Record every rewrite in "notes".
- NEVER invent goals, vulnerability classes, capabilities or constraints that the operator's text does not state. When the text under-specifies, say so in "notes" instead of guessing.
- Constraints are operational boundaries the operator EXPLICITLY stated (may/may-not rules such as "no brute forcing", "passive recon only", "only port 443"). Replace pronouns ("the target", "this port") with the concrete values from the text so each constraint is self-contained. Only list constraints actually stated; when unsure, phrase them as deny (conservative). An empty constraint list is normal — do not pad it.
- Criteria must prove harm, not presence: prefer wording a command, response or number can settle over "find vulnerabilities".
- Write statement, rationale, constraints and notes in the operator's language (mirror the language of the goal text). Keep kind, vuln_class, capability and verify_by as fixed lowercase English tokens.

Respond with ONLY a JSON object — no prose, no markdown fences:
{"statement": "...", "criteria": [{"kind": "finding_class|capability|coverage", "vuln_class": "...", "capability": "...", "verify_by": "deterministic|verifier|manual", "required": true, "rationale": "..."}], "constraints": ["..."], "notes": "..."}

Criteria rules: 1-12 items, each independently decidable from engine-observable evidence. kind=finding_class requires vuln_class — use the lowercase words that would literally appear in the finding title (e.g. "sql injection", "command injection", "stored xss"); bare abbreviations like "sqli" or "rce" may never match a title, so spell them out. kind=capability requires capability (short name such as shell, admin-access). kind=coverage means the planned methodology coverage itself. verify_by: "deterministic" = concrete exploitation proof suffices; "verifier" = the engine's independent verifier must reproduce it (the default); "manual" = only for outcomes the engine can never prove itself (avoid unless clearly manual). required defaults to true; set false only for nice-to-have outcomes the operator did not demand.`

// Analyze runs one LLM call and parses the reply into a proposal. It returns
// an error for an empty goal (caller bug), an LLM failure, an unparseable or
// empty reply, or a reply with no decidable criterion — callers fall back to
// the raw goal on any error.
func Analyze(ctx context.Context, in AnalyzeInput, llm LLMFunc) (*Proposal, error) {
	goal := strings.TrimSpace(in.Goal)
	if goal == "" {
		return nil, errors.New("goalanalysis: empty goal")
	}
	if llm == nil {
		return nil, errors.New("goalanalysis: nil LLM function")
	}
	reply, err := llm(ctx, systemPrompt, buildUserMessage(goal, in))
	if err != nil {
		return nil, fmt.Errorf("goalanalysis: LLM call failed: %w", err)
	}
	wire, err := parseReply(reply)
	if err != nil {
		return nil, err
	}
	return validate(wire)
}

func buildUserMessage(goal string, in AnalyzeInput) string {
	var b strings.Builder
	b.WriteString("Task goal (the operator's words, may be vague):\n")
	b.WriteString(goal)
	b.WriteString("\n\nExecution notes (background context; reference only — never invent content from these):\n")
	if s := strings.TrimSpace(in.Instruction); s != "" {
		b.WriteString(s)
	} else {
		b.WriteString("(none)")
	}
	b.WriteString("\n\nAuthorized scope (targets the project authorizes; criteria and constraints must not exceed it):\n")
	if len(in.ScopeTargets) == 0 {
		b.WriteString("(project scope carries no explicit targets)")
	}
	for _, t := range in.ScopeTargets {
		b.WriteString("- " + t + "\n")
	}
	return b.String()
}

var thinkTagRe = regexp.MustCompile(`(?is)<think>.*?</think>`)

// criterionWire / proposalWire mirror the JSON contract in systemPrompt.
// Required is a pointer so "absent" (default true) differs from explicit false.
type criterionWire struct {
	Kind       string `json:"kind"`
	VulnClass  string `json:"vuln_class"`
	Capability string `json:"capability"`
	VerifyBy   string `json:"verify_by"`
	Required   *bool  `json:"required"`
	Rationale  string `json:"rationale"`
}

type proposalWire struct {
	Statement   string          `json:"statement"`
	Criteria    []criterionWire `json:"criteria"`
	Constraints []string        `json:"constraints"`
	Notes       string          `json:"notes"`
}

// parseReply extracts the outermost JSON object from a model reply that may
// carry think tags, prose preamble or code fences.
func parseReply(reply string) (*proposalWire, error) {
	content := strings.TrimSpace(thinkTagRe.ReplaceAllString(reply, ""))
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return nil, errors.New("goalanalysis: reply contains no JSON object")
	}
	var wire proposalWire
	if err := json.Unmarshal([]byte(content[start:end+1]), &wire); err != nil {
		return nil, fmt.Errorf("goalanalysis: reply is not valid JSON: %w", err)
	}
	return &wire, nil
}

// validate trims, applies defaults (verify_by=verifier, required=true), drops
// criteria that could never be decided, caps the list at
// platform.MaxObjectiveCriteria and refuses to return a proposal with no
// decidable criterion or an empty statement.
func validate(wire *proposalWire) (*Proposal, error) {
	statement := strings.TrimSpace(wire.Statement)
	if statement == "" {
		return nil, errors.New("goalanalysis: reply has no objective statement")
	}
	out := &Proposal{Statement: statement, Notes: strings.TrimSpace(wire.Notes)}
	for _, c := range wire.Criteria {
		parsed, ok := parseCriterion(c)
		if ok {
			out.Criteria = append(out.Criteria, parsed)
		}
		if len(out.Criteria) == platform.MaxObjectiveCriteria {
			break
		}
	}
	if len(out.Criteria) == 0 {
		return nil, errors.New("goalanalysis: reply has no decidable criterion")
	}
	for _, c := range wire.Constraints {
		if s := strings.TrimSpace(c); s != "" {
			out.Constraints = append(out.Constraints, s)
		}
		if len(out.Constraints) == MaxConstraints {
			break
		}
	}
	return out, nil
}

func parseCriterion(c criterionWire) (Criterion, bool) {
	kind := strings.TrimSpace(c.Kind)
	out := Criterion{
		Kind:       kind,
		VulnClass:  strings.TrimSpace(c.VulnClass),
		Capability: strings.TrimSpace(c.Capability),
		Rationale:  strings.TrimSpace(c.Rationale),
	}
	switch kind {
	case platform.CriterionKindFindingClass:
		if out.VulnClass == "" {
			return Criterion{}, false
		}
	case platform.CriterionKindCapability:
		if out.Capability == "" {
			return Criterion{}, false
		}
	case platform.CriterionKindCoverage:
	default:
		return Criterion{}, false
	}
	out.VerifyBy = strings.TrimSpace(c.VerifyBy)
	if out.VerifyBy == "" {
		out.VerifyBy = platform.CriterionVerifyVerifier
	}
	switch out.VerifyBy {
	case platform.CriterionVerifyDeterministic, platform.CriterionVerifyVerifier, platform.CriterionVerifyManual:
	default:
		return Criterion{}, false
	}
	out.Required = c.Required == nil || *c.Required
	return out, true
}
