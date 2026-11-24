package reportdelivery

import (
	"fmt"
	"strings"
	"time"
)

// RenderMarkdown renders the five-chapter report as Markdown.
func RenderMarkdown(spec *Spec) []byte {
	if spec == nil {
		return nil
	}
	var b strings.Builder
	writeMDCover(&b, spec)
	writeMDExecutive(&b, spec)
	writeMDRiskOverview(&b, spec)
	writeMDFindings(&b, spec)
	writeMDAttackChains(&b, spec)
	writeMDRemediation(&b, spec)
	return []byte(b.String())
}

// RenderFindingMarkdown renders the single-vulnerability SRC submission.
func RenderFindingMarkdown(meta Meta, f Finding) []byte {
	f = SanitizeFinding(f)
	var b strings.Builder
	title := FindingTitle(meta, f)
	fmt.Fprintf(&b, "# %s\n\n", title)
	b.WriteString("## 基本信息\n\n")
	b.WriteString(mdKV("漏洞编号", f.ID))
	b.WriteString(mdKV("漏洞类型", classifyFinding(f)))
	b.WriteString(mdKV("危害等级", severityLabel(f.Severity)))
	b.WriteString(mdKV("CVSS", fmt.Sprintf("%.1f%s", f.CVSS, optionalParen(f.CVSSVector))))
	b.WriteString(mdKV("CWE / OWASP", strings.TrimSpace(f.CWE+" / "+f.OWASP)))
	b.WriteString(mdKV("CVE", valueOr(f.CVE, "无")))
	b.WriteString(mdKV("资产", valueOr(f.Target, "未记录")))
	b.WriteString(mdKV("接口", valueOr(f.Endpoint, "未记录")))
	b.WriteString(mdKV("请求方法", valueOr(f.Method, "未记录")))
	b.WriteString(mdKV("验证状态", verificationLabel(f)))
	b.WriteString(mdKV("发现时间", valueOr(f.DiscoveredAt, "未记录")))
	b.WriteString("\n## 漏洞描述\n\n")
	b.WriteString(proseOr(f.Description, "未提供描述。"))
	b.WriteString("\n")
	if strings.TrimSpace(f.TechnicalAnalysis) != "" {
		b.WriteString("\n### 技术分析\n\n")
		b.WriteString(proseOr(f.TechnicalAnalysis, ""))
		b.WriteString("\n")
	}
	b.WriteString("\n## 复现步骤\n\n")
	if strings.TrimSpace(f.PoCDescription) != "" {
		b.WriteString(f.PoCDescription)
		b.WriteString("\n\n")
	} else {
		b.WriteString("未提供分步复现说明，请以“证明数据”中的原始请求为准。\n\n")
	}
	if strings.TrimSpace(f.PoCScript) != "" {
		b.WriteString("```bash\n")
		b.WriteString(sanitizeFenced(f.PoCScript, "bash"))
		b.WriteString("\n```\n\n")
	}
	b.WriteString("## 证明数据（HTTP Raw）\n\n")
	b.WriteString(renderTrafficMarkdown(f, "http"))
	b.WriteString("\n## 影响\n\n")
	b.WriteString(proseOr(f.Impact, "未提供影响说明。"))
	b.WriteString("\n")
	if strings.TrimSpace(f.ExploitationProof) != "" {
		b.WriteString("\n### 利用证据\n\n")
		b.WriteString(proseOr(SanitizeText(f.ExploitationProof), ""))
		b.WriteString("\n")
	}
	b.WriteString("\n## 修复建议\n\n")
	if strings.TrimSpace(f.Remediation) != "" {
		b.WriteString(f.Remediation)
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(f.Fix) != "" {
		b.WriteString("**具体修复方案：**\n\n")
		b.WriteString(f.Fix)
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(f.Remediation) == "" && strings.TrimSpace(f.Fix) == "" {
		b.WriteString("未提供修复建议。\n\n")
	}
	b.WriteString("**验证方法：** 修复后按上述复现步骤重新验证，确认漏洞不再复现。\n\n")
	b.WriteString("## 测试声明\n\n")
	b.WriteString("本报告仅用于授权范围内的安全测试。测试过程中未破坏业务数据、未留存敏感数据；如使用了测试账号，已在测试结束后停用。" +
		"报告中涉及的凭据、Cookie、token 等敏感值均已脱敏。\n")
	if note := strings.TrimSpace(meta.AuthorizationNote); note != "" {
		b.WriteString("\n")
		b.WriteString(note)
		b.WriteString("\n")
	}
	return []byte(b.String())
}

func writeMDCover(b *strings.Builder, spec *Spec) {
	meta := spec.Meta
	fmt.Fprintf(b, "# %s\n\n", valueOr(meta.Title, "安全测试报告"))
	b.WriteString("> 本报告由 Xalgorix 基于已持久化的扫描结果生成；漏洞等级与验证标签来自报告门管线，报告不改写原始判定。\n\n")
	b.WriteString("| 项目 | 内容 |\n| --- | --- |\n")
	rows := [][2]string{
		{"报告编号", meta.ReportID},
		{"报告类型", kindLabel(meta.Kind)},
		{"任务", valueOr(meta.TaskName, meta.TaskID)},
		{"测试目标", strings.Join(nonEmpty(meta.Targets), "、")},
		{"测试窗口", strings.TrimSpace(meta.TestingWindow.Start + " 至 " + meta.TestingWindow.End)},
		{"密级", valueOr(meta.Classification, "内部")},
		{"版本", fmt.Sprintf("r%d", meta.Revision)},
		{"生成方式", generatorLabel(meta.Generator, meta.Model)},
		{"生成时间", formatTime(meta.GeneratedAt)},
	}
	if meta.Kind == KindSub {
		rows = append(rows, [2]string{"子报告范围", meta.ScopeTarget})
	}
	for _, row := range rows {
		if strings.TrimSpace(row[1]) == "" {
			continue
		}
		fmt.Fprintf(b, "| %s | %s |\n", mdCell(row[0]), mdCell(row[1]))
	}
	b.WriteString("\n")
	if note := strings.TrimSpace(meta.AuthorizationNote); note != "" {
		b.WriteString(note)
		b.WriteString("\n\n")
	}
}

func writeMDExecutive(b *strings.Builder, spec *Spec) {
	b.WriteString("## 一、执行摘要\n\n")
	es := spec.Narrative.ExecutiveSummary
	if strings.TrimSpace(es.Background) != "" {
		b.WriteString("**测试背景与目标**\n\n")
		b.WriteString(es.Background)
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(es.Conclusion) != "" {
		b.WriteString("**总体结论**\n\n")
		b.WriteString(es.Conclusion)
		b.WriteString("\n\n")
	}
	if len(es.KeyFindings) > 0 {
		b.WriteString("**关键漏洞**\n\n")
		for _, kf := range es.KeyFindings {
			if _, ok := spec.Facts.Finding(kf.FindingID); !ok {
				continue
			}
			fmt.Fprintf(b, "- `%s` %s\n", kf.FindingID, kf.Text)
		}
		b.WriteString("\n")
	}
}

func writeMDRiskOverview(b *strings.Builder, spec *Spec) {
	b.WriteString("## 二、风险总览\n\n")
	facts := spec.Facts
	counts := facts.SeverityCounts
	if counts == nil {
		counts = map[string]int{}
	}
	b.WriteString("| 严重 | 高危 | 中危 | 低危 | 整体风险分 |\n| --- | --- | --- | --- | --- |\n")
	fmt.Fprintf(b, "| %d | %d | %d | %d | %d/100 |\n\n",
		counts["critical"], counts["high"], counts["medium"], counts["low"], facts.RiskScore)
	if strings.TrimSpace(spec.Narrative.RiskOverview.Narrative) != "" {
		b.WriteString(spec.Narrative.RiskOverview.Narrative)
		b.WriteString("\n\n")
	}
	if len(facts.Assets) > 0 {
		b.WriteString("### 资产与暴露面\n\n")
		b.WriteString("| 目标 | IP | 端口 | 指纹 | 已观察 URL 数 |\n| --- | --- | --- | --- | --- |\n")
		for _, a := range facts.Assets {
			fmt.Fprintf(b, "| %s | %s | %s | %s | %d |\n",
				mdCell(a.Target), mdCell(a.IP), mdCell(strings.Join(a.Ports, ", ")), mdCell(strings.Join(a.Tech, ", ")), len(a.URLs))
		}
		b.WriteString("\n")
	}
	if len(facts.SubReports) > 0 {
		b.WriteString("### 子报告\n\n")
		fmt.Fprintf(b, "本次总报告附带 %d 份子报告：%s。\n\n", len(facts.SubReports), strings.Join(facts.SubReports, "、"))
	}
	if len(facts.RiskScores) > 0 {
		b.WriteString("### 风险排序\n\n")
		b.WriteString("| 排名 | 漏洞 | 风险分 | 报告优先级 | 依据 |\n| --- | --- | --- | --- | --- |\n")
		for i, s := range facts.RiskScores {
			f, ok := facts.Finding(s.FindingID)
			if !ok {
				continue
			}
			fmt.Fprintf(b, "| %d | `%s` %s | %.2f | %s | %s |\n",
				i+1, s.FindingID, mdCell(f.Title), s.Base, BandLabel(s.Band), mdCell(s.Rationale))
		}
		b.WriteString("\n")
	}
}

func writeMDFindings(b *strings.Builder, spec *Spec) {
	b.WriteString("## 三、漏洞详情\n\n")
	findings := orderedFindings(spec.Facts)
	if len(findings) == 0 {
		b.WriteString("本次测试未发现已确认漏洞。未覆盖项与限制见报告元数据与覆盖说明。\n\n")
		return
	}
	for i, f := range findings {
		f = SanitizeFinding(f)
		score, _ := RiskScoreFor(spec.Facts.RiskScores, f.ID)
		fmt.Fprintf(b, "### 3.%d `%s` %s\n\n", i+1, f.ID, f.Title)
		b.WriteString(mdKV("危害等级", fmt.Sprintf("%s（报告优先级：%s）", severityLabel(f.Severity), BandLabel(score.Band))))
		b.WriteString(mdKV("CVSS", fmt.Sprintf("%.1f%s", f.CVSS, optionalParen(f.CVSSVector))))
		b.WriteString(mdKV("CWE / OWASP", strings.TrimSpace(f.CWE+" / "+f.OWASP)))
		b.WriteString(mdKV("CVE", valueOr(f.CVE, "无")))
		b.WriteString(mdKV("资产 / 接口", strings.TrimSpace(valueOr(f.Target, "")+" "+valueOr(f.Endpoint, ""))))
		b.WriteString(mdKV("请求方法", valueOr(f.Method, "未记录")))
		b.WriteString(mdKV("验证状态", verificationLabel(f)))
		b.WriteString(mdKV("发现时间", valueOr(f.DiscoveredAt, "未记录")))
		if f.ScanID != "" {
			b.WriteString(mdKV("来源扫描", f.ScanID))
		}
		b.WriteString("\n")
		nn := narrativeFor(spec.Narrative, f.ID)
		b.WriteString("**漏洞描述**\n\n")
		b.WriteString(proseOr(firstNonEmpty(nn.Description, f.Description), "未提供描述。"))
		b.WriteString("\n\n")
		if txt := firstNonEmpty(f.TechnicalAnalysis); txt != "" {
			b.WriteString("**技术分析**\n\n")
			b.WriteString(proseOr(txt, ""))
			b.WriteString("\n\n")
		}
		b.WriteString("**复现步骤**\n\n")
		b.WriteString(proseOr(firstNonEmpty(nn.Reproduction, f.PoCDescription), "未提供分步复现说明，请以证明数据中的原始请求为准。"))
		b.WriteString("\n\n")
		if p := strings.TrimSpace(f.PoCScript); p != "" {
			b.WriteString("```bash\n")
			b.WriteString(sanitizeFenced(p, "bash"))
			b.WriteString("\n```\n\n")
		}
		b.WriteString("**证明数据（HTTP Raw）**\n\n")
		b.WriteString(renderTrafficMarkdown(f, "http"))
		b.WriteString("\n**影响**\n\n")
		b.WriteString(proseOr(firstNonEmpty(nn.Impact, f.Impact), "未提供影响说明。"))
		b.WriteString("\n\n")
		b.WriteString("**修复建议**\n\n")
		b.WriteString(proseOr(firstNonEmpty(nn.Remediation, f.Remediation, f.Fix), "未提供修复建议。"))
		b.WriteString("\n\n")
		if strings.TrimSpace(f.Remediation) != "" && strings.TrimSpace(f.Fix) != "" {
			b.WriteString("**具体修复方案**\n\n")
			b.WriteString(f.Fix)
			b.WriteString("\n\n")
		}
		if len(f.EvidenceRefs) > 0 || len(f.InfoItemIDs) > 0 {
			b.WriteString("**证据引用**\n\n")
			for _, ref := range f.EvidenceRefs {
				fmt.Fprintf(b, "- `%s`\n", ref)
			}
			for _, id := range f.InfoItemIDs {
				fmt.Fprintf(b, "- InfoItem `%s`\n", id)
			}
			b.WriteString("\n")
		}
	}
}

func writeMDAttackChains(b *strings.Builder, spec *Spec) {
	b.WriteString("## 四、攻击链\n\n")
	chains := spec.Narrative.AttackChains
	if len(chains) == 0 {
		b.WriteString("本次评估未形成可确认的完整攻击链。\n\n")
		return
	}
	var main, speculative []AttackChain
	for _, c := range chains {
		if c.Confidence == ConfidenceSpeculative {
			speculative = append(speculative, c)
		} else {
			main = append(main, c)
		}
	}
	if len(main) == 0 {
		b.WriteString("本次评估未形成已证实或高置信的完整攻击链。\n\n")
	}
	for i, c := range main {
		fmt.Fprintf(b, "### 4.%d %s\n\n", i+1, c.Title)
		b.WriteString(mdKV("置信度", confidenceLabel(c.Confidence)))
		b.WriteString("\n")
		for j, step := range c.Steps {
			f, ok := spec.Facts.Finding(step.FindingID)
			title := step.FindingID
			if ok {
				title = f.Title
			}
			fmt.Fprintf(b, "%d. `%s` %s", j+1, step.FindingID, title)
			if strings.TrimSpace(step.Action) != "" {
				fmt.Fprintf(b, " —— %s", step.Action)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
		if strings.TrimSpace(c.Impact) != "" {
			b.WriteString("**影响**\n\n")
			b.WriteString(c.Impact)
			b.WriteString("\n\n")
		}
	}
	if len(speculative) > 0 {
		b.WriteString("### 待验证风险（推测链）\n\n")
		for _, c := range speculative {
			fmt.Fprintf(b, "- **%s**（%s）：", c.Title, confidenceLabel(c.Confidence))
			if len(c.Steps) > 0 {
				var ids []string
				for _, s := range c.Steps {
					ids = append(ids, "`"+s.FindingID+"`")
				}
				fmt.Fprintf(b, "涉及 %s。", strings.Join(ids, "、"))
			}
			if strings.TrimSpace(c.Impact) != "" {
				b.WriteString(c.Impact)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
}

func writeMDRemediation(b *strings.Builder, spec *Spec) {
	b.WriteString("## 五、修复建议\n\n")
	plan := spec.Narrative.RemediationPlan
	if len(plan) == 0 {
		b.WriteString("暂无需要处置的漏洞。\n\n")
		return
	}
	for _, item := range plan {
		fmt.Fprintf(b, "### %s优先级\n\n", BandLabel(item.Priority))
		if len(item.FindingIDs) > 0 {
			b.WriteString("**关联漏洞：** ")
			for i, id := range item.FindingIDs {
				if i > 0 {
					b.WriteString("、")
				}
				fmt.Fprintf(b, "`%s`", id)
			}
			b.WriteString("\n\n")
		}
		if item.Immediate != "" {
			b.WriteString("**临时缓解：** " + item.Immediate + "\n\n")
		}
		if item.RootFix != "" {
			b.WriteString("**根治方案：** " + item.RootFix + "\n\n")
		}
		if item.Verification != "" {
			b.WriteString("**验证方法：** " + item.Verification + "\n\n")
		}
		if item.SLA != "" {
			b.WriteString("**建议时限：** " + item.SLA + "\n\n")
		}
	}
}

// renderTrafficMarkdown renders the raw-HTTP evidence section. Traffic bytes
// are sanitized defensively even though the assembler already did so.
func renderTrafficMarkdown(f Finding, fence string) string {
	var b strings.Builder
	available := 0
	for _, t := range f.Traffic {
		if !t.Available && strings.TrimSpace(t.RequestRaw) == "" && strings.TrimSpace(t.ResponseRaw) == "" {
			continue
		}
		available++
		meta := fmt.Sprintf("采集：%s:%d", valueOr(t.Session, "unknown"), t.Seq)
		if t.CapturedAt != "" {
			meta += " · " + t.CapturedAt
		}
		if t.Channel != "" {
			meta += " · " + t.Channel
		}
		if t.RequestTruncated || t.ResponseTruncated {
			meta += " · 原始包已截断"
		}
		fmt.Fprintf(&b, "> %s\n\n", meta)
		if req := SanitizeHTTPRaw(t.RequestRaw); strings.TrimSpace(req) != "" {
			b.WriteString("请求：\n\n```" + fence + "\n")
			b.WriteString(sanitizeFenced(req, fence))
			b.WriteString("\n```\n\n")
		}
		if resp := SanitizeHTTPRaw(t.ResponseRaw); strings.TrimSpace(resp) != "" {
			b.WriteString("响应：\n\n```" + fence + "\n")
			b.WriteString(sanitizeFenced(resp, fence))
			b.WriteString("\n```\n\n")
		}
	}
	if available > 0 {
		return b.String()
	}
	reason := ""
	for _, t := range f.Traffic {
		if strings.TrimSpace(t.MissingReason) != "" {
			reason = t.MissingReason
			break
		}
	}
	if reason == "" {
		reason = "代理未开启、流量未归因或目标非 HTTP"
	}
	fmt.Fprintf(&b, "> 未捕获原始 HTTP 请求（%s）。\n", reason)
	b.WriteString("> 以下为依据 finding 元数据重建的 PoC，**非抓包原文**：\n\n")
	if p := strings.TrimSpace(f.PoCScript); p != "" {
		b.WriteString("```bash\n")
		b.WriteString(sanitizeFenced(p, "bash"))
		b.WriteString("\n```\n")
	} else {
		b.WriteString("（无可用的重建 PoC）\n")
	}
	return b.String()
}

func orderedFindings(facts Facts) []Finding {
	byID := make(map[string]Finding, len(facts.Findings))
	for _, f := range facts.Findings {
		byID[f.ID] = f
	}
	out := make([]Finding, 0, len(facts.Findings))
	seen := map[string]struct{}{}
	for _, s := range facts.RiskScores {
		if f, ok := byID[s.FindingID]; ok {
			out = append(out, f)
			seen[f.ID] = struct{}{}
		}
	}
	for _, f := range facts.Findings {
		if _, ok := seen[f.ID]; !ok {
			out = append(out, f)
		}
	}
	return out
}

func narrativeFor(n Narrative, id string) FindingNarrative {
	for _, fn := range n.FindingNarratives {
		if fn.FindingID == id {
			return fn
		}
	}
	return FindingNarrative{}
}

func mdKV(key, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return fmt.Sprintf("- **%s：** %s\n", key, value)
}

func mdCell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}

func proseOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func optionalParen(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return "（" + s + "）"
}

// sanitizeFenced prevents a payload from prematurely closing the fence.
func sanitizeFenced(s, fence string) string {
	return strings.ReplaceAll(s, "```", "` ` `")
}

func severityLabel(sev string) string {
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "critical", "严重":
		return "严重"
	case "high", "高危", "高":
		return "高危"
	case "medium", "中危", "中":
		return "中危"
	case "low", "低危", "低":
		return "低危"
	case "info", "信息":
		return "信息"
	default:
		if strings.TrimSpace(sev) == "" {
			return "未评级"
		}
		return sev
	}
}

func verificationLabel(f Finding) string {
	for _, tag := range f.Tags {
		switch strings.ToLower(strings.TrimSpace(tag)) {
		case "exploit-proven":
			return "利用已证实（exploit-proven）"
		case "verified":
			return "已独立验证（verified）"
		case "needs-manual-verification":
			return "待人工确认（needs-manual-verification）"
		}
	}
	if f.Verified {
		return "已验证"
	}
	return "未验证"
}

func confidenceLabel(c string) string {
	switch c {
	case ConfidenceConfirmed:
		return "已证实"
	case ConfidenceLikely:
		return "高置信"
	case ConfidenceSpeculative:
		return "推测"
	default:
		return valueOr(c, "未标注")
	}
}

func classifyFinding(f Finding) string {
	if strings.TrimSpace(f.CWE) != "" {
		return f.CWE
	}
	if strings.TrimSpace(f.OWASP) != "" {
		return f.OWASP
	}
	return "未分类"
}

func kindLabel(kind string) string {
	switch kind {
	case KindTotal:
		return "任务总报告"
	case KindSub:
		return "子报告"
	case KindFinding:
		return "单漏洞报告"
	default:
		return "报告"
	}
}

func generatorLabel(generator, model string) string {
	switch generator {
	case GeneratorAgent:
		return "报告撰写 Agent" + optionalParen(model)
	default:
		return "确定性模板"
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}
