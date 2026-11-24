package reportdelivery

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// DefaultAssetWeight is used when the operator has not supplied a business
// weight for a target.
const DefaultAssetWeight = 0.5

// ComputeRiskScores builds the deterministic ranking base. The agent may move
// a finding one band with a written rationale, but the numbers here are what
// every renderer shows as the base score.
//
//	base = 0.40*norm(CVSS) + 0.30*verification + 0.20*exposure + 0.10*asset
func ComputeRiskScores(findings []Finding, assetWeights map[string]float64) []RiskScore {
	scores := make([]RiskScore, 0, len(findings))
	for i := range findings {
		f := &findings[i]
		cvss := f.CVSS
		if cvss <= 0 {
			cvss = severityCVSS(f.Severity)
		}
		if cvss < 0 {
			cvss = 0
		}
		if cvss > 10 {
			cvss = 10
		}
		cvssNorm := cvss / 10
		verify := verificationStrength(*f)
		exposure := exposureWeight(*f)
		weight := DefaultAssetWeight
		if assetWeights != nil {
			if w, ok := assetWeights[f.Target]; ok && w >= 0 && w <= 1 {
				weight = w
			}
		}
		base := 0.40*cvssNorm + 0.30*verify + 0.20*exposure + 0.10*weight
		if base > 1 {
			base = 1
		}
		scores = append(scores, RiskScore{
			FindingID: f.ID,
			Base:      math.Round(base*1000) / 1000,
			Band:      BandFor(base),
			Rationale: riskRationale(cvss, verify, exposure, weight),
		})
	}
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].Base != scores[j].Base {
			return scores[i].Base > scores[j].Base
		}
		ci, cj := findingCVSS(findings, scores[i].FindingID), findingCVSS(findings, scores[j].FindingID)
		if ci != cj {
			return ci > cj
		}
		return scores[i].FindingID < scores[j].FindingID
	})
	return scores
}

// BandFor maps a 0..1 base score to a remediation band.
func BandFor(base float64) string {
	switch {
	case base >= 0.85:
		return BandImmediate
	case base >= 0.70:
		return BandHigh
	case base >= 0.50:
		return BandMedium
	default:
		return BandLow
	}
}

// BandLabel renders the Chinese operator-facing band label.
func BandLabel(band string) string {
	switch band {
	case BandImmediate:
		return "立即处置"
	case BandHigh:
		return "高"
	case BandMedium:
		return "中"
	default:
		return "低"
	}
}

// BandRank orders bands for sorting/grouping (higher is more urgent).
func BandRank(band string) int {
	switch band {
	case BandImmediate:
		return 4
	case BandHigh:
		return 3
	case BandMedium:
		return 2
	default:
		return 1
	}
}

// OverallRiskScore converts the ranked findings into a 0..100 executive score.
// The highest finding dominates; the count of additional high-band findings
// adds a bounded uplift.
func OverallRiskScore(scores []RiskScore) int {
	if len(scores) == 0 {
		return 0
	}
	maxBase := scores[0].Base
	high := 0
	for _, s := range scores {
		if BandRank(s.Band) >= BandRank(BandHigh) {
			high++
		}
	}
	score := maxBase*100 + math.Min(float64(high-1)*2, 8)
	if score > 100 {
		score = 100
	}
	if score < 0 {
		score = 0
	}
	return int(math.Round(score))
}

// RiskScoreFor returns the score for one finding, if present.
func RiskScoreFor(scores []RiskScore, findingID string) (RiskScore, bool) {
	for _, s := range scores {
		if s.FindingID == findingID {
			return s, true
		}
	}
	return RiskScore{}, false
}

func verificationStrength(f Finding) float64 {
	for _, tag := range f.Tags {
		switch strings.ToLower(strings.TrimSpace(tag)) {
		case "exploit-proven":
			return 1.0
		case "verified":
			return 0.8
		case "needs-manual-verification":
			return 0.4
		}
	}
	if f.Verified {
		return 0.8
	}
	return 0.5
}

func exposureWeight(f Finding) float64 {
	host := strings.ToLower(f.Target)
	if host == "" {
		host = strings.ToLower(f.Endpoint)
	}
	for _, internal := range []string{"127.0.0.1", "localhost", "::1", ".local", ".internal", "10.", "192.168.", "172.16."} {
		if strings.Contains(host, internal) {
			return 0.4
		}
	}
	// The engine only scans operator-authorized, remotely reachable targets;
	// authentication requirements are not part of the finding record, so this
	// uses the conservative "internet-facing, auth unknown" factor.
	return 0.7
}

func severityCVSS(severity string) float64 {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical", "严重":
		return 9.5
	case "high", "高危", "高":
		return 8.0
	case "medium", "中危", "中":
		return 5.5
	case "low", "低危", "低":
		return 2.5
	default:
		return 0
	}
}

func riskRationale(cvss, verify, exposure, weight float64) string {
	verifyLabel := "验证强度未知"
	switch {
	case verify >= 1.0:
		verifyLabel = "已有利用证据"
	case verify >= 0.8:
		verifyLabel = "已独立验证"
	case verify <= 0.4:
		verifyLabel = "待人工确认"
	}
	exposureLabel := "内网目标"
	if exposure >= 0.7 {
		exposureLabel = "公网目标"
	}
	return fmt.Sprintf("CVSS %.1f；%s；%s；资产权重 %.1f", cvss, verifyLabel, exposureLabel, weight)
}

func findingCVSS(findings []Finding, id string) float64 {
	for i := range findings {
		if findings[i].ID == id {
			return findings[i].CVSS
		}
	}
	return 0
}

// BuildTemplateNarrative produces the zero-token narrative. It only restates
// facts already present in the report; it never invents attack chains or
// findings.
func BuildTemplateNarrative(facts Facts, meta Meta) Narrative {
	n := Narrative{
		ExecutiveSummary: ExecutiveSummary{
			Background: fmt.Sprintf("本次测试目标为 %s，测试窗口为 %s 至 %s。",
				strings.Join(nonEmpty(meta.Targets), "、"), valueOr(meta.TestingWindow.Start, "未记录"), valueOr(meta.TestingWindow.End, "未记录")),
		},
	}
	counts := facts.SeverityCounts
	if counts == nil {
		counts = map[string]int{}
	}
	total := len(facts.Findings)
	if total == 0 {
		n.ExecutiveSummary.Conclusion = fmt.Sprintf("本次测试未发现已确认漏洞。整体风险评分 %d/100。", facts.RiskScore)
		n.RiskOverview.Narrative = "所有已测目标未产生通过报告门的漏洞；覆盖范围与限制见测试范围章节。"
	} else {
		n.ExecutiveSummary.Conclusion = fmt.Sprintf(
			"本次测试共确认 %d 个漏洞：严重 %d 个、高危 %d 个、中危 %d 个、低危 %d 个。整体风险评分 %d/100，建议优先处置风险分最高的漏洞。",
			total, counts["critical"], counts["high"], counts["medium"], counts["low"], facts.RiskScore)
		n.RiskOverview.Narrative = buildRiskOverviewNarrative(facts)
	}
	for i := 0; i < len(facts.RiskScores) && i < 5; i++ {
		score := facts.RiskScores[i]
		f, ok := facts.Finding(score.FindingID)
		if !ok {
			continue
		}
		n.ExecutiveSummary.KeyFindings = append(n.ExecutiveSummary.KeyFindings, KeyFinding{
			FindingID: f.ID,
			Text:      fmt.Sprintf("%s（%s，%s 等级）", f.Title, BandLabel(score.Band)+"优先级", strings.ToUpper(f.Severity)),
		})
	}
	for i := range facts.Findings {
		f := &facts.Findings[i]
		n.FindingNarratives = append(n.FindingNarratives, FindingNarrative{
			FindingID:    f.ID,
			Description:  f.Description,
			Impact:       f.Impact,
			Reproduction: f.PoCDescription,
			Remediation:  firstNonEmpty(f.Remediation, f.Fix),
		})
	}
	n.RemediationPlan = buildTemplateRemediation(facts)
	return n
}

func buildRiskOverviewNarrative(facts Facts) string {
	if len(facts.RiskScores) == 0 {
		return "暂无风险排序数据。"
	}
	var b strings.Builder
	b.WriteString("按确定性风险底座排序，优先处置项如下：")
	for i := 0; i < len(facts.RiskScores) && i < 5; i++ {
		s := facts.RiskScores[i]
		f, ok := facts.Finding(s.FindingID)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "%d. %s：风险分 %.2f（%s）。", i+1, f.Title, s.Base, BandLabel(s.Band))
	}
	return b.String()
}

func buildTemplateRemediation(facts Facts) []RemediationItem {
	byBand := map[string][]string{}
	byBandFix := map[string][]string{}
	for _, s := range facts.RiskScores {
		byBand[s.Band] = append(byBand[s.Band], s.FindingID)
		if f, ok := facts.Finding(s.FindingID); ok {
			if fix := firstNonEmpty(f.Fix, f.Remediation); fix != "" {
				byBandFix[s.Band] = append(byBandFix[s.Band], fix)
			}
		}
	}
	bands := []string{BandImmediate, BandHigh, BandMedium, BandLow}
	out := make([]RemediationItem, 0, len(bands))
	for _, band := range bands {
		ids := byBand[band]
		if len(ids) == 0 {
			continue
		}
		out = append(out, RemediationItem{
			Priority:     band,
			FindingIDs:   ids,
			Immediate:    fmt.Sprintf("%s等级漏洞共 %d 个，建议按报告顺序逐项处置。", BandLabel(band), len(ids)),
			RootFix:      strings.Join(dedupeStrings(byBandFix[band]), "；"),
			Verification: "修复后使用报告中的复现步骤或 PoC 重新验证，确认漏洞不再复现。",
			SLA:          bandSLA(band),
		})
	}
	return out
}

func bandSLA(band string) string {
	switch band {
	case BandImmediate:
		return "建议 24 小时内完成临时缓解，7 天内根治"
	case BandHigh:
		return "建议 7 天内修复"
	case BandMedium:
		return "建议 30 天内修复"
	default:
		return "建议纳入常规迭代修复"
	}
}

func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func dedupeStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
