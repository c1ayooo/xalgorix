// Package reportdelivery builds and renders the manual report deliverable.
//
// The package owns the canonical ReportSpec (facts + narrative), the HTTP raw
// sanitizer used by every export, the deterministic risk-scoring base, and the
// Markdown / HTML / PDF renderers. It never reads scan state itself: the web
// layer assembles a Spec from persisted records and traffic archives, then
// hands it to a renderer. Findings acceptance, verification labels and
// severities are input facts and are never recomputed here.
package reportdelivery

import (
	"strings"
	"time"
)

// SpecVersion is the on-disk ReportSpec schema version.
const SpecVersion = 1

// Report kinds. A total report covers every target of a task generation; a
// sub report covers exactly one target/subdomain and is an attachment of the
// total report.
const (
	KindTotal   = "total"
	KindSub     = "sub"
	KindFinding = "finding"
)

// Generator modes.
const (
	GeneratorTemplate = "template"
	GeneratorAgent    = "agent"
)

// Risk bands (deterministic base; an agent may move a finding one band with a
// written rationale, never rewrite the finding's own severity).
const (
	BandImmediate = "immediate"
	BandHigh      = "high"
	BandMedium    = "medium"
	BandLow       = "low"
)

// Attack-chain confidence labels.
const (
	ConfidenceConfirmed   = "confirmed"
	ConfidenceLikely      = "likely"
	ConfidenceSpeculative = "speculative"
)

// Spec is the canonical report document. The same Spec drives PDF, Markdown
// and HTML output; the single-finding export reuses Findings entries.
type Spec struct {
	SpecVersion int       `json:"spec_version"`
	Meta        Meta      `json:"meta"`
	Facts       Facts     `json:"facts"`
	Narrative   Narrative `json:"narrative"`
}

// Meta carries identity, scope and generation provenance.
type Meta struct {
	ReportID          string     `json:"report_id"`
	Kind              string     `json:"kind"`
	TaskID            string     `json:"task_id,omitempty"`
	TaskName          string     `json:"task_name,omitempty"`
	ProjectID         string     `json:"project_id,omitempty"`
	Revision          int        `json:"revision,omitempty"`
	RunIDs            []string   `json:"run_ids,omitempty"`
	ScanIDs           []string   `json:"scan_ids,omitempty"`
	Targets           []string   `json:"targets,omitempty"`
	ScopeTarget       string     `json:"scope_target,omitempty"`
	Title             string     `json:"title"`
	Company           string     `json:"company,omitempty"`
	Classification    string     `json:"classification,omitempty"`
	Language          string     `json:"language"`
	Template          string     `json:"template"`
	Generator         string     `json:"generator"`
	Model             string     `json:"model,omitempty"`
	PromptVersion     string     `json:"prompt_version,omitempty"`
	GeneratedAt       time.Time  `json:"generated_at"`
	TestingWindow     TimeWindow `json:"testing_window"`
	AuthorizationNote string     `json:"authorization_note,omitempty"`
}

// TimeWindow is the operator-visible testing window.
type TimeWindow struct {
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
}

// Facts is the deterministic half of the report. Every field here comes from
// persisted records or archives; narrative generation never writes it.
type Facts struct {
	SeverityCounts map[string]int `json:"severity_counts"`
	RiskScore      int            `json:"risk_score"`
	Assets         []Asset        `json:"assets,omitempty"`
	Findings       []Finding      `json:"findings,omitempty"`
	Coverage       Coverage       `json:"coverage"`
	RiskScores     []RiskScore    `json:"risk_scores,omitempty"`
	Limitations    []string       `json:"limitations,omitempty"`
	// SubReports lists the target/subdomain sub-report attachments of a total
	// report (empty on a sub report).
	SubReports []string `json:"sub_reports,omitempty"`
}

// Asset is one target's observed attack surface.
type Asset struct {
	Target string   `json:"target"`
	IP     string   `json:"ip,omitempty"`
	Ports  []string `json:"ports,omitempty"`
	Tech   []string `json:"tech,omitempty"`
	URLs   []string `json:"urls,omitempty"`
}

// Finding is the report projection of one accepted finding plus its resolved
// (and sanitized) raw HTTP evidence.
type Finding struct {
	ID                 string            `json:"id"`
	ScanID             string            `json:"scan_id,omitempty"`
	Title              string            `json:"title"`
	Severity           string            `json:"severity"`
	OriginalSeverity   string            `json:"original_severity,omitempty"`
	CVSS               float64           `json:"cvss"`
	CVSSVector         string            `json:"cvss_vector,omitempty"`
	CWE                string            `json:"cwe,omitempty"`
	OWASP              string            `json:"owasp,omitempty"`
	CVE                string            `json:"cve,omitempty"`
	Target             string            `json:"target,omitempty"`
	Endpoint           string            `json:"endpoint,omitempty"`
	Method             string            `json:"method,omitempty"`
	Description        string            `json:"description,omitempty"`
	Impact             string            `json:"impact,omitempty"`
	TechnicalAnalysis  string            `json:"technical_analysis,omitempty"`
	PoCDescription     string            `json:"poc_description,omitempty"`
	PoCScript          string            `json:"poc_script,omitempty"`
	ExploitationProof  string            `json:"exploitation_proof,omitempty"`
	Remediation        string            `json:"remediation,omitempty"`
	Fix                string            `json:"fix,omitempty"`
	VerificationMethod string            `json:"verification_method,omitempty"`
	Verified           bool              `json:"verified"`
	Tags               []string          `json:"tags,omitempty"`
	DiscoveredAt       string            `json:"discovered_at,omitempty"`
	Traffic            []TrafficEvidence `json:"traffic,omitempty"`
	EvidenceRefs       []string          `json:"evidence_refs,omitempty"`
	InfoItemIDs        []string          `json:"info_item_ids,omitempty"`
}

// TrafficEvidence is one resolved raw HTTP exchange. Raw is sanitized before
// it is placed here; MissingReason is set when no archive entry could be
// resolved.
type TrafficEvidence struct {
	Session           string `json:"session,omitempty"`
	Seq               int64  `json:"seq,omitempty"`
	CapturedAt        string `json:"captured_at,omitempty"`
	Channel           string `json:"channel,omitempty"`
	RequestRaw        string `json:"request_raw,omitempty"`
	ResponseRaw       string `json:"response_raw,omitempty"`
	RequestTruncated  bool   `json:"request_truncated,omitempty"`
	ResponseTruncated bool   `json:"response_truncated,omitempty"`
	Available         bool   `json:"available"`
	MissingReason     string `json:"missing_reason,omitempty"`
}

// Coverage is the honest testing-coverage projection.
type Coverage struct {
	Phases          []PhaseStatus `json:"phases,omitempty"`
	TestedEndpoints int           `json:"tested_endpoints,omitempty"`
	Limitations     []string      `json:"limitations,omitempty"`
}

// PhaseStatus is one methodology phase disposition.
type PhaseStatus struct {
	ID     int    `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// RiskScore is the deterministic ranking base for one finding.
type RiskScore struct {
	FindingID string  `json:"finding_id"`
	Base      float64 `json:"base"`
	Band      string  `json:"band"`
	Rationale string  `json:"rationale,omitempty"`
}

// Narrative is the generated half of the report. The template generator and
// the optional report agent both produce this shape.
type Narrative struct {
	ExecutiveSummary  ExecutiveSummary   `json:"executive_summary"`
	RiskOverview      RiskOverview       `json:"risk_overview"`
	FindingNarratives []FindingNarrative `json:"finding_narratives,omitempty"`
	AttackChains      []AttackChain      `json:"attack_chains,omitempty"`
	RemediationPlan   []RemediationItem  `json:"remediation_plan,omitempty"`
}

// ExecutiveSummary is chapter 1.
type ExecutiveSummary struct {
	Background  string       `json:"background,omitempty"`
	Conclusion  string       `json:"conclusion,omitempty"`
	KeyFindings []KeyFinding `json:"key_findings,omitempty"`
}

// KeyFinding is one cited highlight.
type KeyFinding struct {
	FindingID string `json:"finding_id"`
	Text      string `json:"text"`
}

// RiskOverview is chapter 2's narrative.
type RiskOverview struct {
	Narrative string `json:"narrative,omitempty"`
}

// FindingNarrative is the optional agent-written prose for one finding.
type FindingNarrative struct {
	FindingID    string `json:"finding_id"`
	Description  string `json:"description,omitempty"`
	Impact       string `json:"impact,omitempty"`
	Reproduction string `json:"reproduction,omitempty"`
	Remediation  string `json:"remediation,omitempty"`
}

// AttackChain is chapter 4.
type AttackChain struct {
	ID         string       `json:"id"`
	Title      string       `json:"title"`
	Confidence string       `json:"confidence"`
	Steps      []AttackStep `json:"steps,omitempty"`
	Impact     string       `json:"impact,omitempty"`
}

// AttackStep cites one finding in a chain.
type AttackStep struct {
	FindingID string `json:"finding_id"`
	Action    string `json:"action,omitempty"`
}

// RemediationItem is chapter 5.
type RemediationItem struct {
	Priority     string   `json:"priority"`
	FindingIDs   []string `json:"finding_ids,omitempty"`
	Immediate    string   `json:"immediate,omitempty"`
	RootFix      string   `json:"root_fix,omitempty"`
	Verification string   `json:"verification,omitempty"`
	SLA          string   `json:"sla,omitempty"`
}

// FindingIDs returns the id set of Facts.Findings.
func (f Facts) FindingIDs() map[string]struct{} {
	ids := make(map[string]struct{}, len(f.Findings))
	for _, finding := range f.Findings {
		ids[finding.ID] = struct{}{}
	}
	return ids
}

// Finding returns the finding with id, if present.
func (f Facts) Finding(id string) (*Finding, bool) {
	for i := range f.Findings {
		if f.Findings[i].ID == id {
			return &f.Findings[i], true
		}
	}
	return nil, false
}

// TrafficAvailable reports whether at least one resolved evidence entry has a
// request body. The report agent only receives a summary when this is true.
func (f Finding) TrafficAvailable() bool {
	for _, traffic := range f.Traffic {
		if traffic.Available && strings.TrimSpace(traffic.RequestRaw) != "" {
			return true
		}
	}
	return false
}
