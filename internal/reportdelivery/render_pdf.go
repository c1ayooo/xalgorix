package reportdelivery

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/go-pdf/fpdf"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
)

const reportCJKFontFamily = "XalgorixCJK"

var candidateCJKFontPaths = []string{
	"/usr/share/fonts/truetype/noto/NotoSansCJKsc-Regular.ttf",
	"/usr/share/fonts/truetype/noto/NotoSansSC-Regular.ttf",
	"/usr/share/fonts/opentype/noto/NotoSansCJKsc-Regular.ttf",
	"/usr/share/fonts/truetype/arphic/uming.ttf",
	"/usr/local/share/fonts/NotoSansSC-Regular.ttf",
}

var (
	cjkFontOnce  sync.Once
	cjkFontBytes []byte
)

func loadCJKFont() []byte {
	cjkFontOnce.Do(func() {
		cjkFontBytes = resolveCJKFontBytes()
	})
	return cjkFontBytes
}

func resolveCJKFontBytes() []byte {
	var paths []string
	if custom := strings.TrimSpace(os.Getenv("XALGORIX_PDF_CJK_FONT")); custom != "" {
		paths = append(paths, custom)
	}
	paths = append(paths, candidateCJKFontPaths...)
	for _, path := range paths {
		if !strings.EqualFold(fileExt(path), ".ttf") {
			if strings.TrimSpace(path) != "" {
				log.Printf("[report] skipping CJK font %q: only .ttf is supported", path)
			}
			continue
		}
		data, err := os.ReadFile(path) //nolint:gosec // operator-configured font path
		if err != nil || len(data) == 0 {
			continue
		}
		log.Printf("[report] loaded CJK PDF font from %s (%d bytes)", path, len(data))
		return data
	}
	return nil
}

func fileExt(path string) string {
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		return path[i:]
	}
	return ""
}

// RenderPDF renders the five-chapter report as a PDF document.
func RenderPDF(spec *Spec) ([]byte, error) {
	var buf bytes.Buffer
	if err := RenderPDFTo(&buf, spec); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderPDFTo renders the report into w (typically an atomic file writer).
func RenderPDFTo(w io.Writer, spec *Spec) error {
	if spec == nil {
		return fmt.Errorf("reportdelivery: nil spec")
	}
	language := config.NormalizeLanguage(valueOr(spec.Meta.Language, "zh-CN"))
	needsCJK := language == "zh-CN"
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(16, 18, 16)
	pdf.SetAutoPageBreak(true, 18)
	cjk := ""
	if needsCJK {
		font := loadCJKFont()
		if len(font) == 0 {
			return fmt.Errorf("reportdelivery: 中文 PDF 需要 CJK TrueType 字体；请设置 XALGORIX_PDF_CJK_FONT 指向 .ttf 文件（MD/HTML 不受影响）")
		}
		pdf.AddUTF8FontFromBytes(reportCJKFontFamily, "", font)
		if pdf.Err() {
			err := pdf.Error()
			pdf.ClearError()
			return fmt.Errorf("reportdelivery: 注册 CJK 字体失败: %w", err)
		}
		cjk = reportCJKFontFamily
	}
	doc := &pdfDoc{Fpdf: pdf, cjk: cjk, title: valueOr(spec.Meta.Title, "安全测试报告")}
	doc.render(spec)
	if pdf.Err() {
		return fmt.Errorf("reportdelivery: 生成 PDF 失败: %w", pdf.Error())
	}
	return pdf.Output(w)
}

type pdfDoc struct {
	*fpdf.Fpdf
	cjk   string
	title string
}

func (d *pdfDoc) setFont(size float64, bold bool) {
	if d.cjk != "" {
		d.SetFont(d.cjk, "", size)
		return
	}
	style := ""
	if bold {
		style = "B"
	}
	d.SetFont("Helvetica", style, size)
}

func (d *pdfDoc) mono(size float64) {
	if d.cjk != "" {
		d.SetFont(d.cjk, "", size)
		return
	}
	d.SetFont("Courier", "", size)
}

func (d *pdfDoc) render(spec *Spec) {
	d.SetHeaderFunc(func() {
		if d.PageNo() <= 1 {
			return
		}
		d.setFont(7.5, false)
		d.SetTextColor(130, 137, 148)
		d.SetY(8)
		d.CellFormat(0, 5, pdfSafe(d.title), "", 0, "L", false, 0, "")
		d.Ln(5)
	})
	d.SetFooterFunc(func() {
		d.setFont(7.5, false)
		d.SetTextColor(130, 137, 148)
		d.SetY(-12)
		d.CellFormat(90, 5, "Xalgorix 安全测试报告", "", 0, "L", false, 0, "")
		d.CellFormat(0, 5, fmt.Sprintf("%d", d.PageNo()), "", 0, "R", false, 0, "")
	})
	d.AddPage()
	d.renderCover(spec)
	d.renderExecutive(spec)
	d.renderRiskOverview(spec)
	d.renderFindings(spec)
	d.renderAttackChains(spec)
	d.renderRemediation(spec)
}

func (d *pdfDoc) renderCover(spec *Spec) {
	meta := spec.Meta
	d.SetTextColor(24, 32, 46)
	d.setFont(24, true)
	d.MultiCell(0, 11, pdfSafe(valueOr(meta.Title, "安全测试报告")), "", "L", false)
	d.Ln(2)
	d.setFont(9.5, false)
	d.SetTextColor(95, 104, 118)
	d.MultiCell(0, 5.6, pdfSafe("本报告由 Xalgorix 基于已持久化的扫描结果生成；漏洞等级与验证标签来自报告门管线，报告不改写原始判定。"), "", "L", false)
	d.Ln(6)
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
	d.kvTable(rows)
	if note := strings.TrimSpace(meta.AuthorizationNote); note != "" {
		d.Ln(3)
		d.setFont(9, false)
		d.SetTextColor(70, 78, 92)
		d.MultiCell(0, 5, pdfSafe(note), "", "L", false)
	}
}

func (d *pdfDoc) renderExecutive(spec *Spec) {
	d.chapter("一、执行摘要")
	es := spec.Narrative.ExecutiveSummary
	if strings.TrimSpace(es.Background) != "" {
		d.subheading("测试背景与目标")
		d.paragraph(es.Background)
	}
	if strings.TrimSpace(es.Conclusion) != "" {
		d.subheading("总体结论")
		d.paragraph(es.Conclusion)
	}
	if len(es.KeyFindings) > 0 {
		d.subheading("关键漏洞")
		for _, kf := range es.KeyFindings {
			if _, ok := spec.Facts.Finding(kf.FindingID); !ok {
				continue
			}
			d.bullet(fmt.Sprintf("%s  %s", kf.FindingID, kf.Text))
		}
	}
}

func (d *pdfDoc) renderRiskOverview(spec *Spec) {
	d.chapter("二、风险总览")
	counts := spec.Facts.SeverityCounts
	if counts == nil {
		counts = map[string]int{}
	}
	d.setFont(9, false)
	d.SetTextColor(40, 48, 62)
	d.CellFormat(32, 7, "严重", "1", 0, "C", false, 0, "")
	d.CellFormat(32, 7, "高危", "1", 0, "C", false, 0, "")
	d.CellFormat(32, 7, "中危", "1", 0, "C", false, 0, "")
	d.CellFormat(32, 7, "低危", "1", 0, "C", false, 0, "")
	d.CellFormat(46, 7, "整体风险分", "1", 1, "C", false, 0, "")
	d.CellFormat(32, 7, fmt.Sprintf("%d", counts["critical"]), "1", 0, "C", false, 0, "")
	d.CellFormat(32, 7, fmt.Sprintf("%d", counts["high"]), "1", 0, "C", false, 0, "")
	d.CellFormat(32, 7, fmt.Sprintf("%d", counts["medium"]), "1", 0, "C", false, 0, "")
	d.CellFormat(32, 7, fmt.Sprintf("%d", counts["low"]), "1", 0, "C", false, 0, "")
	d.CellFormat(46, 7, fmt.Sprintf("%d/100", spec.Facts.RiskScore), "1", 1, "C", false, 0, "")
	d.Ln(2)
	if strings.TrimSpace(spec.Narrative.RiskOverview.Narrative) != "" {
		d.paragraph(spec.Narrative.RiskOverview.Narrative)
	}
	if len(spec.Facts.Assets) > 0 {
		d.subheading("资产与暴露面")
		for _, a := range spec.Facts.Assets {
			line := valueOr(a.Target, "未命名目标")
			var extra []string
			if a.IP != "" {
				extra = append(extra, "IP "+a.IP)
			}
			if len(a.Ports) > 0 {
				extra = append(extra, "端口 "+strings.Join(a.Ports, ", "))
			}
			if len(a.Tech) > 0 {
				extra = append(extra, "指纹 "+strings.Join(a.Tech, ", "))
			}
			if len(a.URLs) > 0 {
				extra = append(extra, fmt.Sprintf("URL %d 个", len(a.URLs)))
			}
			if len(extra) > 0 {
				line += " —— " + strings.Join(extra, "；")
			}
			d.bullet(line)
		}
	}
	if len(spec.Facts.SubReports) > 0 {
		d.subheading("子报告")
		d.paragraph(fmt.Sprintf("本次总报告附带 %d 份子报告：%s。", len(spec.Facts.SubReports), strings.Join(spec.Facts.SubReports, "、")))
	}
	if len(spec.Facts.RiskScores) > 0 {
		d.subheading("风险排序")
		for i, s := range spec.Facts.RiskScores {
			f, ok := spec.Facts.Finding(s.FindingID)
			if !ok {
				continue
			}
			d.bullet(fmt.Sprintf("%d. %s（%s）：%.2f，%s。%s", i+1, f.Title, f.ID, s.Base, BandLabel(s.Band), s.Rationale))
		}
	}
}

func (d *pdfDoc) renderFindings(spec *Spec) {
	d.chapter("三、漏洞详情")
	findings := orderedFindings(spec.Facts)
	if len(findings) == 0 {
		d.paragraph("本次测试未发现已确认漏洞。未覆盖项与限制见报告元数据与覆盖说明。")
		return
	}
	for i, f := range findings {
		f = SanitizeFinding(f)
		score, _ := RiskScoreFor(spec.Facts.RiskScores, f.ID)
		d.subheading(fmt.Sprintf("3.%d %s（%s）", i+1, f.Title, f.ID))
		rows := [][2]string{
			{"危害等级", fmt.Sprintf("%s（报告优先级：%s）", severityLabel(f.Severity), BandLabel(score.Band))},
			{"CVSS", fmt.Sprintf("%.1f%s", f.CVSS, optionalParen(f.CVSSVector))},
			{"CWE / OWASP", strings.TrimSpace(f.CWE + " / " + f.OWASP)},
			{"CVE", valueOr(f.CVE, "无")},
			{"资产 / 接口", strings.TrimSpace(valueOr(f.Target, "") + " " + valueOr(f.Endpoint, ""))},
			{"请求方法", valueOr(f.Method, "未记录")},
			{"验证状态", verificationLabel(f)},
			{"发现时间", valueOr(f.DiscoveredAt, "未记录")},
			{"来源扫描", f.ScanID},
		}
		d.kvTable(rows)
		nn := narrativeFor(spec.Narrative, f.ID)
		d.labelParagraph("漏洞描述", proseOr(firstNonEmpty(nn.Description, f.Description), "未提供描述。"))
		if strings.TrimSpace(f.TechnicalAnalysis) != "" {
			d.labelParagraph("技术分析", f.TechnicalAnalysis)
		}
		d.labelParagraph("复现步骤", proseOr(firstNonEmpty(nn.Reproduction, f.PoCDescription), "未提供分步复现说明，请以证明数据中的原始请求为准。"))
		if strings.TrimSpace(f.PoCScript) != "" {
			d.codeBlock(f.PoCScript)
		}
		d.label("证明数据（HTTP Raw）")
		d.traffic(f)
		d.labelParagraph("影响", proseOr(firstNonEmpty(nn.Impact, f.Impact), "未提供影响说明。"))
		d.labelParagraph("修复建议", proseOr(firstNonEmpty(nn.Remediation, f.Remediation, f.Fix), "未提供修复建议。"))
		if strings.TrimSpace(f.Remediation) != "" && strings.TrimSpace(f.Fix) != "" {
			d.labelParagraph("具体修复方案", f.Fix)
		}
		if len(f.EvidenceRefs) > 0 || len(f.InfoItemIDs) > 0 {
			d.label("证据引用")
			for _, ref := range f.EvidenceRefs {
				d.bullet(ref)
			}
			for _, id := range f.InfoItemIDs {
				d.bullet("InfoItem " + id)
			}
		}
		d.Ln(2)
	}
}

func (d *pdfDoc) renderAttackChains(spec *Spec) {
	d.chapter("四、攻击链")
	chains := spec.Narrative.AttackChains
	if len(chains) == 0 {
		d.paragraph("本次评估未形成可确认的完整攻击链。")
		return
	}
	main := 0
	for _, c := range chains {
		if c.Confidence == ConfidenceSpeculative {
			continue
		}
		main++
		d.subheading(fmt.Sprintf("4.%d %s", main, c.Title))
		d.metaLine("置信度：" + confidenceLabel(c.Confidence))
		for j, step := range c.Steps {
			f, ok := spec.Facts.Finding(step.FindingID)
			title := step.FindingID
			if ok {
				title = f.Title
			}
			text := fmt.Sprintf("%d. %s %s", j+1, step.FindingID, title)
			if strings.TrimSpace(step.Action) != "" {
				text += " —— " + step.Action
			}
			d.bullet(text)
		}
		if strings.TrimSpace(c.Impact) != "" {
			d.labelParagraph("影响", c.Impact)
		}
	}
	if main == 0 {
		d.paragraph("本次评估未形成已证实或高置信的完整攻击链。")
	}
	var speculative []AttackChain
	for _, c := range chains {
		if c.Confidence == ConfidenceSpeculative {
			speculative = append(speculative, c)
		}
	}
	if len(speculative) > 0 {
		d.subheading("待验证风险（推测链）")
		for _, c := range speculative {
			text := fmt.Sprintf("%s（%s）", c.Title, confidenceLabel(c.Confidence))
			if len(c.Steps) > 0 {
				var ids []string
				for _, s := range c.Steps {
					ids = append(ids, s.FindingID)
				}
				text += " 涉及 " + strings.Join(ids, "、") + "。"
			}
			if c.Impact != "" {
				text += c.Impact
			}
			d.bullet(text)
		}
	}
}

func (d *pdfDoc) renderRemediation(spec *Spec) {
	d.chapter("五、修复建议")
	if len(spec.Narrative.RemediationPlan) == 0 {
		d.paragraph("暂无需要处置的漏洞。")
		return
	}
	for _, item := range spec.Narrative.RemediationPlan {
		d.subheading(BandLabel(item.Priority) + "优先级")
		if len(item.FindingIDs) > 0 {
			d.labelParagraph("关联漏洞", strings.Join(item.FindingIDs, "、"))
		}
		if item.Immediate != "" {
			d.labelParagraph("临时缓解", item.Immediate)
		}
		if item.RootFix != "" {
			d.labelParagraph("根治方案", item.RootFix)
		}
		if item.Verification != "" {
			d.labelParagraph("验证方法", item.Verification)
		}
		if item.SLA != "" {
			d.labelParagraph("建议时限", item.SLA)
		}
	}
}

func (d *pdfDoc) chapter(title string) {
	d.AddPage()
	d.SetTextColor(24, 32, 46)
	d.setFont(17, true)
	d.MultiCell(0, 9, pdfSafe(title), "", "L", false)
	d.Ln(1)
}

func (d *pdfDoc) subheading(title string) {
	d.Ln(1)
	d.SetTextColor(24, 32, 46)
	d.setFont(11.5, true)
	d.MultiCell(0, 6.5, pdfSafe(title), "", "L", false)
	d.Ln(0.5)
}

func (d *pdfDoc) paragraph(text string) {
	d.setFont(9.5, false)
	d.SetTextColor(48, 56, 70)
	d.MultiCell(0, 5.6, pdfSafe(text), "", "L", false)
	d.Ln(1)
}

func (d *pdfDoc) label(label string) {
	d.Ln(1)
	d.setFont(9.5, true)
	d.SetTextColor(32, 40, 54)
	d.MultiCell(0, 5.6, pdfSafe(label), "", "L", false)
	d.Ln(0.4)
}

func (d *pdfDoc) labelParagraph(label, text string) {
	d.label(label)
	d.paragraph(text)
}

func (d *pdfDoc) metaLine(text string) {
	d.setFont(8.5, false)
	d.SetTextColor(105, 112, 124)
	d.MultiCell(0, 5, pdfSafe(text), "", "L", false)
}

func (d *pdfDoc) bullet(text string) {
	d.setFont(9.5, false)
	d.SetTextColor(48, 56, 70)
	d.SetX(18)
	d.MultiCell(176, 5.4, pdfSafe("• "+text), "", "L", false)
}

func (d *pdfDoc) kvTable(rows [][2]string) {
	d.setFont(9, false)
	for _, row := range rows {
		if strings.TrimSpace(row[1]) == "" {
			continue
		}
		d.SetTextColor(110, 117, 128)
		d.SetFillColor(248, 249, 251)
		d.CellFormat(38, 6.4, pdfSafe(row[0]), "1", 0, "L", true, 0, "")
		d.SetTextColor(40, 48, 62)
		d.MultiCell(138, 6.4, pdfSafe(row[1]), "1", "L", false)
	}
	d.Ln(1)
}

func (d *pdfDoc) codeBlock(text string) {
	d.mono(7.3)
	d.SetFillColor(244, 246, 249)
	d.SetTextColor(30, 36, 46)
	d.MultiCell(0, 4.2, pdfSafe(text), "1", "L", true)
	d.Ln(2)
}

func (d *pdfDoc) traffic(f Finding) {
	available := 0
	for _, t := range f.Traffic {
		if !t.Available && strings.TrimSpace(t.RequestRaw) == "" && strings.TrimSpace(t.ResponseRaw) == "" {
			continue
		}
		available++
		metaLine := fmt.Sprintf("采集：%s:%d", valueOr(t.Session, "unknown"), t.Seq)
		if t.CapturedAt != "" {
			metaLine += " · " + t.CapturedAt
		}
		if t.Channel != "" {
			metaLine += " · " + t.Channel
		}
		if t.RequestTruncated || t.ResponseTruncated {
			metaLine += " · 原始包已截断"
		}
		d.metaLine(metaLine)
		if req := SanitizeHTTPRaw(t.RequestRaw); strings.TrimSpace(req) != "" {
			d.label("请求")
			d.codeBlock(req)
		}
		if resp := SanitizeHTTPRaw(t.ResponseRaw); strings.TrimSpace(resp) != "" {
			d.label("响应")
			d.codeBlock(resp)
		}
	}
	if available > 0 {
		return
	}
	d.setFont(9, false)
	d.SetTextColor(110, 80, 40)
	d.SetFillColor(252, 248, 238)
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
	d.MultiCell(0, 5.2, pdfSafe(fmt.Sprintf("未捕获原始 HTTP 请求（%s）。以下为依据 finding 元数据重建的 PoC，非抓包原文：", reason)), "1", "L", true)
	if p := strings.TrimSpace(f.PoCScript); p != "" {
		d.codeBlock(p)
	} else {
		d.paragraph("（无可用的重建 PoC）")
	}
}

// pdfSafe strips control characters fpdf cannot render and guarantees valid
// UTF-8. Newlines and tabs are preserved.
func pdfSafe(s string) string {
	if s == "" {
		return s
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
