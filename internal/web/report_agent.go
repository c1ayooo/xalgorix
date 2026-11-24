package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/reportdelivery"
)

const (
	reportAgentMaxFindings = 80
	reportAgentMaxText     = 6000
	reportAgentMaxTotal    = 160_000
)

// agentNarrativeOutput is the only shape the report agent may return. It has
// no severity/CVSS/CWE/verification fields: those are facts and remain owned
// by the report gate.
type agentNarrativeOutput struct {
	ExecutiveSummary  reportdelivery.ExecutiveSummary   `json:"executive_summary"`
	RiskOverview      reportdelivery.RiskOverview       `json:"risk_overview"`
	FindingNarratives []reportdelivery.FindingNarrative `json:"finding_narratives"`
	AttackChains      []reportdelivery.AttackChain      `json:"attack_chains"`
	RemediationPlan   []reportdelivery.RemediationItem  `json:"remediation_plan"`
	RiskAdjustments   []agentRiskAdjustment             `json:"risk_adjustments"`
}

type agentRiskAdjustment struct {
	FindingID string `json:"finding_id"`
	Band      string `json:"band"`
	Rationale string `json:"rationale"`
}

type agentFactPack struct {
	Meta       reportdelivery.Meta        `json:"meta"`
	RiskScore  int                        `json:"risk_score"`
	Findings   []agentFactFinding         `json:"findings"`
	RiskScores []reportdelivery.RiskScore `json:"risk_scores"`
	Assets     []reportdelivery.Asset     `json:"assets"`
	Coverage   reportdelivery.Coverage    `json:"coverage"`
}

type agentFactFinding struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Severity           string   `json:"severity"`
	CVSS               float64  `json:"cvss"`
	CWE                string   `json:"cwe,omitempty"`
	OWASP              string   `json:"owasp,omitempty"`
	CVE                string   `json:"cve,omitempty"`
	Target             string   `json:"target,omitempty"`
	Endpoint           string   `json:"endpoint,omitempty"`
	Method             string   `json:"method,omitempty"`
	VerificationMethod string   `json:"verification_method,omitempty"`
	Verified           bool     `json:"verified"`
	Tags               []string `json:"tags,omitempty"`
	Description        string   `json:"description,omitempty"`
	Impact             string   `json:"impact,omitempty"`
	TechnicalAnalysis  string   `json:"technical_analysis,omitempty"`
	PoCDescription     string   `json:"poc_description,omitempty"`
	ExploitationProof  string   `json:"exploitation_proof,omitempty"`
	Remediation        string   `json:"remediation,omitempty"`
	Fix                string   `json:"fix,omitempty"`
	TrafficAvailable   bool     `json:"traffic_available"`
	TrafficSummary     string   `json:"traffic_summary,omitempty"`
}

const reportAgentSystemPrompt = `你是授权渗透测试项目的报告撰写助手。你只能依据用户提供的 facts JSON 撰写叙述，禁止新增漏洞、修改严重等级/CVSS/CWE/OWASP/验证标签、编造请求响应或影响。

输出必须是一个 JSON 对象（不要 markdown 代码块），字段如下：
{
  "executive_summary": {"background": "...", "conclusion": "...", "key_findings": [{"finding_id": "...", "text": "..."}]},
  "risk_overview": {"narrative": "..."},
  "finding_narratives": [{"finding_id": "...", "description": "...", "impact": "...", "reproduction": "...", "remediation": "..."}],
  "attack_chains": [{"id": "chain-1", "title": "...", "confidence": "confirmed|likely|speculative", "steps": [{"finding_id": "...", "action": "..."}], "impact": "..."}],
  "remediation_plan": [{"priority": "immediate|high|medium|low", "finding_ids": ["..."], "immediate": "...", "root_fix": "...", "verification": "...", "sla": "..."}],
  "risk_adjustments": [{"finding_id": "...", "band": "immediate|high|medium|low", "rationale": "..."}]
}

规则：
1. 所有 finding_id 必须来自 facts；不得虚构漏洞或证据。
2. attack_chains 的主链（confirmed/likely）只能串联 verified 或 exploit-proven 的漏洞；推测链必须标 speculative。
3. risk_adjustments 只能把某个漏洞的 band 上下调整一级，且 rationale 必填；不得修改原始 severity/CVSS。
4. 叙述使用简体中文；不得出现 facts 中没有的数字、结论或影响。
5. 没有可串联的漏洞就不要编造攻击链。`

// generateAgentNarrative asks the configured model to fill the narrative half
// of the total report. Sub reports keep the deterministic template: one LLM
// call is enough for the management narrative, and per-subdomain prose can be
// derived from the same facts without extra token spend.
func (s *Server) generateAgentNarrative(ctx context.Context, bundle *reportBundle, model string) error {
	if bundle == nil {
		return fmt.Errorf("nil report bundle")
	}
	if s.reportNarrativeFn == nil {
		return fmt.Errorf("报告撰写 Agent 未配置可用模型")
	}
	factsJSON, err := buildAgentFactPack(&bundle.Total)
	if err != nil {
		return err
	}
	if len(bundle.Total.Facts.Findings) == 0 {
		return fmt.Errorf("报告没有可撰写的漏洞事实")
	}
	raw, err := s.reportNarrativeFn(ctx, reportAgentSystemPrompt, string(factsJSON), model)
	if err != nil {
		return fmt.Errorf("report agent call: %w", err)
	}
	out, err := parseAgentNarrative(raw)
	if err != nil {
		return err
	}
	if err := validateAgentNarrative(bundle.Total.Facts, &out); err != nil {
		return err
	}
	bundle.Total.Narrative = reportdelivery.Narrative{
		ExecutiveSummary:  out.ExecutiveSummary,
		RiskOverview:      out.RiskOverview,
		FindingNarratives: out.FindingNarratives,
		AttackChains:      out.AttackChains,
		RemediationPlan:   out.RemediationPlan,
	}
	applyAgentRiskAdjustments(bundle.Total.Facts.RiskScores, out.RiskAdjustments)
	for i := range bundle.Subs {
		bundle.Subs[i].Narrative = reportdelivery.BuildTemplateNarrative(bundle.Subs[i].Facts, bundle.Subs[i].Meta)
	}
	return nil
}

func buildAgentFactPack(spec *reportdelivery.Spec) ([]byte, error) {
	pack := agentFactPack{
		Meta:       spec.Meta,
		RiskScore:  spec.Facts.RiskScore,
		RiskScores: spec.Facts.RiskScores,
		Assets:     spec.Facts.Assets,
		Coverage:   spec.Facts.Coverage,
	}
	findings := spec.Facts.Findings
	if len(findings) > reportAgentMaxFindings {
		findings = findings[:reportAgentMaxFindings]
	}
	for _, f := range findings {
		summary := ""
		for _, traffic := range f.Traffic {
			if traffic.Available && strings.TrimSpace(traffic.RequestRaw) != "" {
				summary = truncateString(traffic.RequestRaw, 1200)
				break
			}
		}
		pack.Findings = append(pack.Findings, agentFactFinding{
			ID:                 f.ID,
			Title:              f.Title,
			Severity:           f.Severity,
			CVSS:               f.CVSS,
			CWE:                f.CWE,
			OWASP:              f.OWASP,
			CVE:                f.CVE,
			Target:             f.Target,
			Endpoint:           f.Endpoint,
			Method:             f.Method,
			VerificationMethod: f.VerificationMethod,
			Verified:           f.Verified,
			Tags:               f.Tags,
			Description:        truncateString(f.Description, 1500),
			Impact:             truncateString(f.Impact, 1500),
			TechnicalAnalysis:  truncateString(f.TechnicalAnalysis, 1500),
			PoCDescription:     truncateString(f.PoCDescription, 1500),
			ExploitationProof:  truncateString(f.ExploitationProof, 1500),
			Remediation:        truncateString(f.Remediation, 1200),
			Fix:                truncateString(f.Fix, 1200),
			TrafficAvailable:   f.TrafficAvailable(),
			TrafficSummary:     summary,
		})
	}
	data, err := json.MarshalIndent(pack, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal report facts: %w", err)
	}
	return data, nil
}

func parseAgentNarrative(raw string) (agentNarrativeOutput, error) {
	var out agentNarrativeOutput
	text := strings.TrimSpace(raw)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	start := strings.IndexByte(text, '{')
	end := strings.LastIndexByte(text, '}')
	if start < 0 || end <= start {
		return out, fmt.Errorf("report agent returned no JSON object")
	}
	text = text[start : end+1]
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return out, fmt.Errorf("report agent returned invalid JSON: %w", err)
	}
	return out, nil
}

func validateAgentNarrative(facts reportdelivery.Facts, out *agentNarrativeOutput) error {
	ids := facts.FindingIDs()
	baseByID := map[string]reportdelivery.RiskScore{}
	for _, s := range facts.RiskScores {
		baseByID[s.FindingID] = s
	}
	verified := map[string]bool{}
	for _, f := range facts.Findings {
		verified[f.ID] = f.Verified || hasTag(f.Tags, "verified") || hasTag(f.Tags, "exploit-proven")
	}
	total := 0
	for _, kf := range out.ExecutiveSummary.KeyFindings {
		if _, ok := ids[kf.FindingID]; !ok {
			return fmt.Errorf("agent referenced unknown finding %q", kf.FindingID)
		}
		if strings.TrimSpace(kf.Text) == "" {
			return fmt.Errorf("agent key finding %q has no text", kf.FindingID)
		}
		total += len(kf.Text)
	}
	for _, fn := range out.FindingNarratives {
		if _, ok := ids[fn.FindingID]; !ok {
			return fmt.Errorf("agent narrative references unknown finding %q", fn.FindingID)
		}
		for _, text := range []string{fn.Description, fn.Impact, fn.Reproduction, fn.Remediation} {
			if len(text) > reportAgentMaxText {
				return fmt.Errorf("agent narrative for %q exceeds %d characters", fn.FindingID, reportAgentMaxText)
			}
			total += len(text)
		}
	}
	for _, chain := range out.AttackChains {
		if strings.TrimSpace(chain.Title) == "" {
			return fmt.Errorf("agent attack chain has no title")
		}
		switch chain.Confidence {
		case reportdelivery.ConfidenceConfirmed, reportdelivery.ConfidenceLikely, reportdelivery.ConfidenceSpeculative:
		default:
			return fmt.Errorf("agent attack chain %q has invalid confidence %q", chain.Title, chain.Confidence)
		}
		if len(chain.Steps) == 0 {
			return fmt.Errorf("agent attack chain %q has no steps", chain.Title)
		}
		for _, step := range chain.Steps {
			if _, ok := ids[step.FindingID]; !ok {
				return fmt.Errorf("agent attack chain references unknown finding %q", step.FindingID)
			}
			if chain.Confidence != reportdelivery.ConfidenceSpeculative && !verified[step.FindingID] {
				return fmt.Errorf("agent attack chain %q uses unverified finding %q as a main-chain step", chain.Title, step.FindingID)
			}
			total += len(step.Action)
		}
		total += len(chain.Impact)
	}
	for _, item := range out.RemediationPlan {
		switch item.Priority {
		case reportdelivery.BandImmediate, reportdelivery.BandHigh, reportdelivery.BandMedium, reportdelivery.BandLow:
		default:
			return fmt.Errorf("agent remediation item has invalid priority %q", item.Priority)
		}
		if len(item.FindingIDs) == 0 {
			return fmt.Errorf("agent remediation item has no finding ids")
		}
		for _, id := range item.FindingIDs {
			if _, ok := ids[id]; !ok {
				return fmt.Errorf("agent remediation references unknown finding %q", id)
			}
		}
		total += len(item.Immediate) + len(item.RootFix) + len(item.Verification) + len(item.SLA)
	}
	for _, adj := range out.RiskAdjustments {
		base, ok := baseByID[adj.FindingID]
		if !ok {
			return fmt.Errorf("agent risk adjustment references unknown finding %q", adj.FindingID)
		}
		if strings.TrimSpace(adj.Rationale) == "" {
			return fmt.Errorf("agent risk adjustment for %q has no rationale", adj.FindingID)
		}
		switch adj.Band {
		case reportdelivery.BandImmediate, reportdelivery.BandHigh, reportdelivery.BandMedium, reportdelivery.BandLow:
		default:
			return fmt.Errorf("agent risk adjustment for %q has invalid band %q", adj.FindingID, adj.Band)
		}
		diff := reportdelivery.BandRank(adj.Band) - reportdelivery.BandRank(base.Band)
		if diff < -1 || diff > 1 {
			return fmt.Errorf("agent risk adjustment for %q moves more than one band", adj.FindingID)
		}
		total += len(adj.Rationale)
	}
	if total > reportAgentMaxTotal {
		return fmt.Errorf("agent narrative exceeds the total length budget")
	}
	// The agent never gets to restate facts as free text without the fact id;
	// sanitize every text field before it reaches a renderer.
	for i := range out.ExecutiveSummary.KeyFindings {
		out.ExecutiveSummary.KeyFindings[i].Text = reportdelivery.SanitizeText(out.ExecutiveSummary.KeyFindings[i].Text)
	}
	out.ExecutiveSummary.Background = reportdelivery.SanitizeText(out.ExecutiveSummary.Background)
	out.ExecutiveSummary.Conclusion = reportdelivery.SanitizeText(out.ExecutiveSummary.Conclusion)
	out.RiskOverview.Narrative = reportdelivery.SanitizeText(out.RiskOverview.Narrative)
	for i := range out.FindingNarratives {
		out.FindingNarratives[i].Description = reportdelivery.SanitizeText(out.FindingNarratives[i].Description)
		out.FindingNarratives[i].Impact = reportdelivery.SanitizeText(out.FindingNarratives[i].Impact)
		out.FindingNarratives[i].Reproduction = reportdelivery.SanitizeText(out.FindingNarratives[i].Reproduction)
		out.FindingNarratives[i].Remediation = reportdelivery.SanitizeText(out.FindingNarratives[i].Remediation)
	}
	for i := range out.AttackChains {
		out.AttackChains[i].Impact = reportdelivery.SanitizeText(out.AttackChains[i].Impact)
		for j := range out.AttackChains[i].Steps {
			out.AttackChains[i].Steps[j].Action = reportdelivery.SanitizeText(out.AttackChains[i].Steps[j].Action)
		}
	}
	return nil
}

func applyAgentRiskAdjustments(scores []reportdelivery.RiskScore, adjustments []agentRiskAdjustment) {
	if len(scores) == 0 || len(adjustments) == 0 {
		return
	}
	byID := make(map[string]int, len(scores))
	for i := range scores {
		byID[scores[i].FindingID] = i
	}
	for _, adj := range adjustments {
		idx, ok := byID[adj.FindingID]
		if !ok {
			continue
		}
		scores[idx].Band = adj.Band
		if strings.TrimSpace(adj.Rationale) != "" {
			scores[idx].Rationale = strings.TrimSpace(scores[idx].Rationale) + "；Agent 调整：" + reportdelivery.SanitizeText(adj.Rationale)
		}
	}
}

func hasTag(tags []string, want string) bool {
	for _, tag := range tags {
		if strings.EqualFold(strings.TrimSpace(tag), want) {
			return true
		}
	}
	return false
}

func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
