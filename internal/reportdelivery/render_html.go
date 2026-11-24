package reportdelivery

import (
	"fmt"
	"html"
	"strings"
)

// RenderHTML renders the five-chapter report as a self-contained HTML page.
func RenderHTML(spec *Spec) []byte {
	if spec == nil {
		return nil
	}
	var b strings.Builder
	writeHTMLHead(&b, valueOr(spec.Meta.Title, "安全测试报告"))
	writeHTMLCover(&b, spec)
	writeHTMLExecutive(&b, spec)
	writeHTMLRiskOverview(&b, spec)
	writeHTMLFindings(&b, spec)
	writeHTMLAttackChains(&b, spec)
	writeHTMLRemediation(&b, spec)
	b.WriteString("</main></body></html>\n")
	return []byte(b.String())
}

// RenderFindingHTML renders one vulnerability as a self-contained HTML page.
func RenderFindingHTML(meta Meta, f Finding) []byte {
	f = SanitizeFinding(f)
	var b strings.Builder
	writeHTMLHead(&b, FindingTitle(meta, f))
	b.WriteString("<main>\n")
	fmt.Fprintf(&b, "<h1>%s</h1>\n", esc(FindingTitle(meta, f)))
	b.WriteString("<h2>基本信息</h2>\n<table class=\"kv\">\n")
	writeKVRow(&b, "漏洞编号", f.ID)
	writeKVRow(&b, "漏洞类型", classifyFinding(f))
	writeKVRow(&b, "危害等级", severityLabel(f.Severity))
	writeKVRow(&b, "CVSS", fmt.Sprintf("%.1f%s", f.CVSS, optionalParen(f.CVSSVector)))
	writeKVRow(&b, "CWE / OWASP", strings.TrimSpace(f.CWE+" / "+f.OWASP))
	writeKVRow(&b, "CVE", valueOr(f.CVE, "无"))
	writeKVRow(&b, "资产", valueOr(f.Target, "未记录"))
	writeKVRow(&b, "接口", valueOr(f.Endpoint, "未记录"))
	writeKVRow(&b, "请求方法", valueOr(f.Method, "未记录"))
	writeKVRow(&b, "验证状态", verificationLabel(f))
	writeKVRow(&b, "发现时间", valueOr(f.DiscoveredAt, "未记录"))
	b.WriteString("</table>\n")
	b.WriteString("<h2>漏洞描述</h2>\n")
	b.WriteString(htmlProse(proseOr(f.Description, "未提供描述。")))
	if strings.TrimSpace(f.TechnicalAnalysis) != "" {
		b.WriteString("<h3>技术分析</h3>\n" + htmlProse(f.TechnicalAnalysis))
	}
	b.WriteString("<h2>复现步骤</h2>\n")
	b.WriteString(htmlProse(proseOr(f.PoCDescription, "未提供分步复现说明，请以“证明数据”中的原始请求为准。")))
	if strings.TrimSpace(f.PoCScript) != "" {
		b.WriteString("<pre class=\"code\">" + esc(sanitizeFenced(f.PoCScript, "")) + "</pre>\n")
	}
	b.WriteString("<h2>证明数据（HTTP Raw）</h2>\n")
	b.WriteString(renderTrafficHTML(f))
	b.WriteString("<h2>影响</h2>\n")
	b.WriteString(htmlProse(proseOr(f.Impact, "未提供影响说明。")))
	if strings.TrimSpace(f.ExploitationProof) != "" {
		b.WriteString("<h3>利用证据</h3>\n" + htmlProse(SanitizeText(f.ExploitationProof)))
	}
	b.WriteString("<h2>修复建议</h2>\n")
	if strings.TrimSpace(f.Remediation) != "" {
		b.WriteString(htmlProse(f.Remediation))
	}
	if strings.TrimSpace(f.Fix) != "" {
		b.WriteString("<h3>具体修复方案</h3>\n" + htmlProse(f.Fix))
	}
	if strings.TrimSpace(f.Remediation) == "" && strings.TrimSpace(f.Fix) == "" {
		b.WriteString(htmlProse("未提供修复建议。"))
	}
	b.WriteString(htmlProse("验证方法：修复后按上述复现步骤重新验证，确认漏洞不再复现。"))
	b.WriteString("<h2>测试声明</h2>\n")
	b.WriteString(htmlProse("本报告仅用于授权范围内的安全测试。测试过程中未破坏业务数据、未留存敏感数据；如使用了测试账号，已在测试结束后停用。报告中涉及的凭据、Cookie、token 等敏感值均已脱敏。"))
	if note := strings.TrimSpace(meta.AuthorizationNote); note != "" {
		b.WriteString(htmlProse(note))
	}
	b.WriteString("</main></body></html>\n")
	return []byte(b.String())
}

func writeHTMLHead(b *strings.Builder, title string) {
	b.WriteString(`<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + esc(title) + `</title>
<style>
:root { color-scheme: light; }
* { box-sizing: border-box; }
body { margin: 0; background: #f5f6f8; color: #1f2430;
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Noto Sans CJK SC", sans-serif;
  line-height: 1.7; }
main { max-width: 900px; margin: 0 auto; padding: 48px 32px 80px; background: #fff; min-height: 100vh; }
h1 { font-size: 28px; margin: 0 0 8px; letter-spacing: .01em; }
h2 { font-size: 21px; margin: 40px 0 12px; padding-bottom: 6px; border-bottom: 1px solid #e3e6ea; }
h3 { font-size: 16px; margin: 26px 0 8px; }
h4 { font-size: 14px; margin: 20px 0 6px; }
p { margin: 8px 0; }
blockquote { margin: 14px 0; padding: 10px 14px; background: #f7f8fa; border-left: 3px solid #c7ccd4; color: #4a5160; }
table { width: 100%; border-collapse: collapse; margin: 12px 0 20px; font-size: 13px; }
th, td { border: 1px solid #e3e6ea; padding: 7px 9px; text-align: left; vertical-align: top; }
th { background: #f7f8fa; font-weight: 600; }
table.kv td:first-child { width: 140px; color: #5b6270; background: #fafbfc; }
.prose { white-space: pre-wrap; word-break: break-word; }
pre.code, pre.http { white-space: pre-wrap; word-break: break-all; background: #0f1720; color: #e6edf3;
  padding: 14px 16px; border-radius: 6px; font-size: 12px; line-height: 1.55; overflow-x: auto;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace; }
.meta { color: #6a7280; font-size: 13px; }
.tag { display: inline-block; padding: 1px 8px; border-radius: 999px; font-size: 12px; background: #eef1f5; color: #3d4552; }
.finding { margin: 30px 0 10px; }
.cover-note { margin: 18px 0 28px; }
footer { margin-top: 56px; color: #8a909c; font-size: 12px; border-top: 1px solid #e3e6ea; padding-top: 12px; }
@media print { body { background: #fff; } main { max-width: none; padding: 0; } pre.code, pre.http { white-space: pre-wrap; } }
</style>
</head>
<body>
<main>
`)
}

func writeHTMLCover(b *strings.Builder, spec *Spec) {
	meta := spec.Meta
	fmt.Fprintf(b, "<h1>%s</h1>\n", esc(valueOr(meta.Title, "安全测试报告")))
	b.WriteString(`<blockquote class="cover-note">本报告由 Xalgorix 基于已持久化的扫描结果生成；漏洞等级与验证标签来自报告门管线，报告不改写原始判定。</blockquote>` + "\n")
	b.WriteString("<table class=\"kv\">\n")
	writeKVRow(b, "报告编号", meta.ReportID)
	writeKVRow(b, "报告类型", kindLabel(meta.Kind))
	writeKVRow(b, "任务", valueOr(meta.TaskName, meta.TaskID))
	writeKVRow(b, "测试目标", strings.Join(nonEmpty(meta.Targets), "、"))
	writeKVRow(b, "测试窗口", strings.TrimSpace(meta.TestingWindow.Start+" 至 "+meta.TestingWindow.End))
	writeKVRow(b, "密级", valueOr(meta.Classification, "内部"))
	writeKVRow(b, "版本", fmt.Sprintf("r%d", meta.Revision))
	writeKVRow(b, "生成方式", generatorLabel(meta.Generator, meta.Model))
	writeKVRow(b, "生成时间", formatTime(meta.GeneratedAt))
	if meta.Kind == KindSub {
		writeKVRow(b, "子报告范围", meta.ScopeTarget)
	}
	b.WriteString("</table>\n")
	if note := strings.TrimSpace(meta.AuthorizationNote); note != "" {
		b.WriteString(htmlProse(note))
	}
}

func writeHTMLExecutive(b *strings.Builder, spec *Spec) {
	b.WriteString("<h2>一、执行摘要</h2>\n")
	es := spec.Narrative.ExecutiveSummary
	if strings.TrimSpace(es.Background) != "" {
		b.WriteString("<h3>测试背景与目标</h3>\n" + htmlProse(es.Background))
	}
	if strings.TrimSpace(es.Conclusion) != "" {
		b.WriteString("<h3>总体结论</h3>\n" + htmlProse(es.Conclusion))
	}
	if len(es.KeyFindings) > 0 {
		b.WriteString("<h3>关键漏洞</h3>\n<ul>\n")
		for _, kf := range es.KeyFindings {
			if _, ok := spec.Facts.Finding(kf.FindingID); !ok {
				continue
			}
			fmt.Fprintf(b, "<li><code>%s</code> %s</li>\n", esc(kf.FindingID), esc(kf.Text))
		}
		b.WriteString("</ul>\n")
	}
}

func writeHTMLRiskOverview(b *strings.Builder, spec *Spec) {
	b.WriteString("<h2>二、风险总览</h2>\n")
	counts := spec.Facts.SeverityCounts
	if counts == nil {
		counts = map[string]int{}
	}
	b.WriteString("<table>\n<tr><th>严重</th><th>高危</th><th>中危</th><th>低危</th><th>整体风险分</th></tr>\n")
	fmt.Fprintf(b, "<tr><td>%d</td><td>%d</td><td>%d</td><td>%d</td><td>%d/100</td></tr>\n</table>\n",
		counts["critical"], counts["high"], counts["medium"], counts["low"], spec.Facts.RiskScore)
	if strings.TrimSpace(spec.Narrative.RiskOverview.Narrative) != "" {
		b.WriteString(htmlProse(spec.Narrative.RiskOverview.Narrative))
	}
	if len(spec.Facts.Assets) > 0 {
		b.WriteString("<h3>资产与暴露面</h3>\n<table>\n<tr><th>目标</th><th>IP</th><th>端口</th><th>指纹</th><th>URL 数</th></tr>\n")
		for _, a := range spec.Facts.Assets {
			fmt.Fprintf(b, "<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%d</td></tr>\n",
				esc(a.Target), esc(a.IP), esc(strings.Join(a.Ports, ", ")), esc(strings.Join(a.Tech, ", ")), len(a.URLs))
		}
		b.WriteString("</table>\n")
	}
	if len(spec.Facts.SubReports) > 0 {
		fmt.Fprintf(b, "<h3>子报告</h3>\n<p>本次总报告附带 %d 份子报告：%s。</p>\n", len(spec.Facts.SubReports), esc(strings.Join(spec.Facts.SubReports, "、")))
	}
	if len(spec.Facts.RiskScores) > 0 {
		b.WriteString("<h3>风险排序</h3>\n<table>\n<tr><th>#</th><th>漏洞</th><th>风险分</th><th>报告优先级</th><th>依据</th></tr>\n")
		for i, s := range spec.Facts.RiskScores {
			f, ok := spec.Facts.Finding(s.FindingID)
			if !ok {
				continue
			}
			fmt.Fprintf(b, "<tr><td>%d</td><td><code>%s</code> %s</td><td>%.2f</td><td>%s</td><td>%s</td></tr>\n",
				i+1, esc(s.FindingID), esc(f.Title), s.Base, esc(BandLabel(s.Band)), esc(s.Rationale))
		}
		b.WriteString("</table>\n")
	}
}

func writeHTMLFindings(b *strings.Builder, spec *Spec) {
	b.WriteString("<h2>三、漏洞详情</h2>\n")
	findings := orderedFindings(spec.Facts)
	if len(findings) == 0 {
		b.WriteString(htmlProse("本次测试未发现已确认漏洞。未覆盖项与限制见报告元数据与覆盖说明。"))
		return
	}
	for i, f := range findings {
		f = SanitizeFinding(f)
		score, _ := RiskScoreFor(spec.Facts.RiskScores, f.ID)
		fmt.Fprintf(b, "<section class=\"finding\"><h3>3.%d <code>%s</code> %s</h3>\n", i+1, esc(f.ID), esc(f.Title))
		b.WriteString("<table class=\"kv\">\n")
		writeKVRow(b, "危害等级", fmt.Sprintf("%s（报告优先级：%s）", severityLabel(f.Severity), BandLabel(score.Band)))
		writeKVRow(b, "CVSS", fmt.Sprintf("%.1f%s", f.CVSS, optionalParen(f.CVSSVector)))
		writeKVRow(b, "CWE / OWASP", strings.TrimSpace(f.CWE+" / "+f.OWASP))
		writeKVRow(b, "CVE", valueOr(f.CVE, "无"))
		writeKVRow(b, "资产 / 接口", strings.TrimSpace(valueOr(f.Target, "")+" "+valueOr(f.Endpoint, "")))
		writeKVRow(b, "请求方法", valueOr(f.Method, "未记录"))
		writeKVRow(b, "验证状态", verificationLabel(f))
		writeKVRow(b, "发现时间", valueOr(f.DiscoveredAt, "未记录"))
		if f.ScanID != "" {
			writeKVRow(b, "来源扫描", f.ScanID)
		}
		b.WriteString("</table>\n")
		nn := narrativeFor(spec.Narrative, f.ID)
		b.WriteString("<h4>漏洞描述</h4>\n" + htmlProse(proseOr(firstNonEmpty(nn.Description, f.Description), "未提供描述。")))
		if strings.TrimSpace(f.TechnicalAnalysis) != "" {
			b.WriteString("<h4>技术分析</h4>\n" + htmlProse(f.TechnicalAnalysis))
		}
		b.WriteString("<h4>复现步骤</h4>\n" + htmlProse(proseOr(firstNonEmpty(nn.Reproduction, f.PoCDescription), "未提供分步复现说明，请以证明数据中的原始请求为准。")))
		if strings.TrimSpace(f.PoCScript) != "" {
			b.WriteString("<pre class=\"code\">" + esc(sanitizeFenced(f.PoCScript, "")) + "</pre>\n")
		}
		b.WriteString("<h4>证明数据（HTTP Raw）</h4>\n")
		b.WriteString(renderTrafficHTML(f))
		b.WriteString("<h4>影响</h4>\n" + htmlProse(proseOr(firstNonEmpty(nn.Impact, f.Impact), "未提供影响说明。")))
		b.WriteString("<h4>修复建议</h4>\n" + htmlProse(proseOr(firstNonEmpty(nn.Remediation, f.Remediation, f.Fix), "未提供修复建议。")))
		if strings.TrimSpace(f.Remediation) != "" && strings.TrimSpace(f.Fix) != "" {
			b.WriteString("<h4>具体修复方案</h4>\n" + htmlProse(f.Fix))
		}
		if len(f.EvidenceRefs) > 0 || len(f.InfoItemIDs) > 0 {
			b.WriteString("<h4>证据引用</h4>\n<ul>\n")
			for _, ref := range f.EvidenceRefs {
				fmt.Fprintf(b, "<li><code>%s</code></li>\n", esc(ref))
			}
			for _, id := range f.InfoItemIDs {
				fmt.Fprintf(b, "<li>InfoItem <code>%s</code></li>\n", esc(id))
			}
			b.WriteString("</ul>\n")
		}
		b.WriteString("</section>\n")
	}
}

func writeHTMLAttackChains(b *strings.Builder, spec *Spec) {
	b.WriteString("<h2>四、攻击链</h2>\n")
	chains := spec.Narrative.AttackChains
	if len(chains) == 0 {
		b.WriteString(htmlProse("本次评估未形成可确认的完整攻击链。"))
		return
	}
	main := 0
	for _, c := range chains {
		if c.Confidence == ConfidenceSpeculative {
			continue
		}
		main++
		fmt.Fprintf(b, "<h3>4.%d %s</h3>\n", main, esc(c.Title))
		fmt.Fprintf(b, "<p class=\"meta\">置信度：%s</p>\n", esc(confidenceLabel(c.Confidence)))
		b.WriteString("<ol>\n")
		for _, step := range c.Steps {
			f, ok := spec.Facts.Finding(step.FindingID)
			title := step.FindingID
			if ok {
				title = f.Title
			}
			fmt.Fprintf(b, "<li><code>%s</code> %s", esc(step.FindingID), esc(title))
			if strings.TrimSpace(step.Action) != "" {
				fmt.Fprintf(b, " —— %s", esc(step.Action))
			}
			b.WriteString("</li>\n")
		}
		b.WriteString("</ol>\n")
		if strings.TrimSpace(c.Impact) != "" {
			b.WriteString("<h4>影响</h4>\n" + htmlProse(c.Impact))
		}
	}
	if main == 0 {
		b.WriteString(htmlProse("本次评估未形成已证实或高置信的完整攻击链。"))
	}
	var speculative []AttackChain
	for _, c := range chains {
		if c.Confidence == ConfidenceSpeculative {
			speculative = append(speculative, c)
		}
	}
	if len(speculative) > 0 {
		b.WriteString("<h3>待验证风险（推测链）</h3>\n<ul>\n")
		for _, c := range speculative {
			fmt.Fprintf(b, "<li><strong>%s</strong>（%s）", esc(c.Title), esc(confidenceLabel(c.Confidence)))
			if len(c.Steps) > 0 {
				var ids []string
				for _, s := range c.Steps {
					ids = append(ids, "<code>"+esc(s.FindingID)+"</code>")
				}
				fmt.Fprintf(b, " 涉及 %s。", strings.Join(ids, "、"))
			}
			if strings.TrimSpace(c.Impact) != "" {
				b.WriteString(esc(c.Impact))
			}
			b.WriteString("</li>\n")
		}
		b.WriteString("</ul>\n")
	}
}

func writeHTMLRemediation(b *strings.Builder, spec *Spec) {
	b.WriteString("<h2>五、修复建议</h2>\n")
	if len(spec.Narrative.RemediationPlan) == 0 {
		b.WriteString(htmlProse("暂无需要处置的漏洞。"))
		return
	}
	for _, item := range spec.Narrative.RemediationPlan {
		fmt.Fprintf(b, "<h3>%s优先级</h3>\n", esc(BandLabel(item.Priority)))
		if len(item.FindingIDs) > 0 {
			b.WriteString("<p>关联漏洞：")
			for i, id := range item.FindingIDs {
				if i > 0 {
					b.WriteString("、")
				}
				fmt.Fprintf(b, "<code>%s</code>", esc(id))
			}
			b.WriteString("</p>\n")
		}
		if item.Immediate != "" {
			b.WriteString("<p><strong>临时缓解：</strong>" + esc(item.Immediate) + "</p>\n")
		}
		if item.RootFix != "" {
			b.WriteString("<p><strong>根治方案：</strong>" + esc(item.RootFix) + "</p>\n")
		}
		if item.Verification != "" {
			b.WriteString("<p><strong>验证方法：</strong>" + esc(item.Verification) + "</p>\n")
		}
		if item.SLA != "" {
			b.WriteString("<p><strong>建议时限：</strong>" + esc(item.SLA) + "</p>\n")
		}
	}
}

func renderTrafficHTML(f Finding) string {
	var b strings.Builder
	available := 0
	for _, t := range f.Traffic {
		if !t.Available && strings.TrimSpace(t.RequestRaw) == "" && strings.TrimSpace(t.ResponseRaw) == "" {
			continue
		}
		available++
		b.WriteString("<p class=\"meta\">采集：")
		b.WriteString(esc(fmt.Sprintf("%s:%d", valueOr(t.Session, "unknown"), t.Seq)))
		if t.CapturedAt != "" {
			b.WriteString(" · " + esc(t.CapturedAt))
		}
		if t.Channel != "" {
			b.WriteString(" · " + esc(t.Channel))
		}
		if t.RequestTruncated || t.ResponseTruncated {
			b.WriteString(" · 原始包已截断")
		}
		b.WriteString("</p>\n")
		if req := SanitizeHTTPRaw(t.RequestRaw); strings.TrimSpace(req) != "" {
			b.WriteString("<h4>请求</h4>\n<pre class=\"http\">" + esc(req) + "</pre>\n")
		}
		if resp := SanitizeHTTPRaw(t.ResponseRaw); strings.TrimSpace(resp) != "" {
			b.WriteString("<h4>响应</h4>\n<pre class=\"http\">" + esc(resp) + "</pre>\n")
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
	fmt.Fprintf(&b, "<blockquote>未捕获原始 HTTP 请求（%s）。<br>以下为依据 finding 元数据重建的 PoC，<strong>非抓包原文</strong>：</blockquote>\n", esc(reason))
	if p := strings.TrimSpace(f.PoCScript); p != "" {
		b.WriteString("<pre class=\"code\">" + esc(sanitizeFenced(p, "")) + "</pre>\n")
	} else {
		b.WriteString("<p>（无可用的重建 PoC）</p>\n")
	}
	return b.String()
}

func writeKVRow(b *strings.Builder, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	fmt.Fprintf(b, "<tr><td>%s</td><td>%s</td></tr>\n", esc(key), esc(value))
}

func htmlProse(s string) string {
	return "<div class=\"prose\">" + esc(s) + "</div>\n"
}

func esc(s string) string { return html.EscapeString(s) }
