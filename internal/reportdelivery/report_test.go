package reportdelivery

import (
	"strings"
	"testing"
	"time"
)

func TestSanitizeHTTPRawMasksHeadersAndBody(t *testing.T) {
	raw := "POST /login?password=hunter2&user=bob HTTP/1.1\r\n" +
		"Host: example.com\r\n" +
		"Cookie: session=abc123; theme=dark\r\n" +
		"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig\r\n" +
		"Content-Type: application/json\r\n" +
		"\r\n" +
		`{"username":"bob","password":"s3cr3t","api_key":"AKIA123","note":"mail bob@example.com"}`

	got := SanitizeHTTPRaw(raw)
	for _, secret := range []string{"hunter2", "abc123", "eyJhbGciOiJIUzI1NiJ9", "s3cr3t", "AKIA123", "bob@example.com"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sanitized output still contains %q:\n%s", secret, got)
		}
	}
	for _, want := range []string{"POST /login?", "Host: example.com", "Cookie:", "Authorization:", "<redacted>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("sanitized output lost %q:\n%s", want, got)
		}
	}
	// The wire shape must survive: the blank line still separates head/body.
	if !strings.Contains(got, "\r\n\r\n") {
		t.Fatalf("sanitizer removed the head/body separator:\n%s", got)
	}
}

func TestSanitizeHTTPRawResponseSetCookie(t *testing.T) {
	raw := "HTTP/1.1 200 OK\r\nSet-Cookie: sid=deadbeef; HttpOnly\r\nContent-Length: 2\r\n\r\nok"
	got := SanitizeHTTPRaw(raw)
	if strings.Contains(got, "deadbeef") {
		t.Fatalf("Set-Cookie value leaked: %s", got)
	}
	if !strings.Contains(got, "Set-Cookie:") {
		t.Fatalf("header name was lost: %s", got)
	}
}

func TestSanitizeTextMasksBearerAndPII(t *testing.T) {
	in := "curl -H 'Authorization: Bearer abcdef123456' https://x/?token=zzz ; call 13800138000 or a@b.com"
	got := SanitizeText(in)
	for _, secret := range []string{"abcdef123456", "zzz", "13800138000", "a@b.com"} {
		if strings.Contains(got, secret) {
			t.Fatalf("SanitizeText leaked %q: %s", secret, got)
		}
	}
}

func sampleSpec() *Spec {
	f := Finding{
		ID:             "XALG-1",
		ScanID:         "scan-1",
		Title:          "SQL 注入",
		Severity:       "high",
		CVSS:           8.1,
		CVSSVector:     "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:N",
		CWE:            "CWE-89",
		OWASP:          "A03:2021",
		Target:         "example.com",
		Endpoint:       "/api/user",
		Method:         "POST",
		Description:    "id 参数拼接进 SQL。",
		Impact:         "可读取用户表。",
		PoCDescription: "发送带有 SLEEP 的 id。",
		PoCScript:      "curl -sk -X POST 'https://example.com/api/user' --data 'id=1'",
		Remediation:    "使用参数化查询。",
		Verified:       true,
		Tags:           []string{"verified"},
		Traffic: []TrafficEvidence{{
			Session:     "scan-1",
			Seq:         3,
			CapturedAt:  "2026-11-23 20:00:00",
			Channel:     "proxy",
			RequestRaw:  SanitizeHTTPRaw("POST /api/user HTTP/1.1\r\nHost: example.com\r\nCookie: sid=secret\r\n\r\nid=1"),
			ResponseRaw: "HTTP/1.1 200 OK\r\n\r\nok",
			Available:   true,
		}},
		EvidenceRefs: []string{"finding_ref:XALG-1"},
	}
	facts := Facts{
		SeverityCounts: map[string]int{"high": 1},
		Findings:       []Finding{f},
		Assets:         []Asset{{Target: "example.com", IP: "203.0.113.10", Ports: []string{"443/tcp"}, Tech: []string{"nginx"}}},
		Coverage:       Coverage{TestedEndpoints: 12},
	}
	facts.RiskScores = ComputeRiskScores(facts.Findings, nil)
	facts.RiskScore = OverallRiskScore(facts.RiskScores)
	meta := Meta{
		ReportID:      "rep-1",
		Kind:          KindTotal,
		TaskName:      "示例系统",
		Revision:      1,
		Targets:       []string{"example.com"},
		Title:         "示例系统 渗透测试报告",
		Language:      "zh-CN",
		Template:      "standard",
		Generator:     GeneratorTemplate,
		GeneratedAt:   time.Date(2026, 11, 23, 21, 0, 0, 0, time.Local),
		TestingWindow: TimeWindow{Start: "2026-11-23 20:00:00", End: "2026-11-23 20:30:00"},
	}
	spec := &Spec{SpecVersion: SpecVersion, Meta: meta, Facts: facts}
	spec.Narrative = BuildTemplateNarrative(facts, meta)
	return spec
}

func TestRenderMarkdownHasFiveChaptersAndRawHTTP(t *testing.T) {
	spec := sampleSpec()
	md := string(RenderMarkdown(spec))
	for _, want := range []string{"一、执行摘要", "二、风险总览", "三、漏洞详情", "四、攻击链", "五、修复建议", "证明数据（HTTP Raw）", "POST /api/user HTTP/1.1"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "sid=secret") {
		t.Fatalf("markdown leaked the raw cookie value")
	}
}

func TestRenderHTMLHasFiveChaptersAndEscapes(t *testing.T) {
	spec := sampleSpec()
	spec.Facts.Findings[0].Description = `<script>alert(1)</script>`
	html := string(RenderHTML(spec))
	for _, want := range []string{"一、执行摘要", "二、风险总览", "三、漏洞详情", "四、攻击链", "五、修复建议", "证明数据（HTTP Raw）"} {
		if !strings.Contains(html, want) {
			t.Fatalf("html missing %q", want)
		}
	}
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Fatalf("html did not escape finding prose")
	}
	if strings.Contains(html, "sid=secret") {
		t.Fatalf("html leaked the raw cookie value")
	}
}

func TestRenderPDFEnglishSucceeds(t *testing.T) {
	spec := sampleSpec()
	spec.Meta.Language = "en"
	spec.Facts.Findings[0].Title = "SQL injection"
	data, err := RenderPDF(spec)
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	if len(data) < 1000 || string(data[:4]) != "%PDF" {
		t.Fatalf("RenderPDF output is not a PDF (len=%d)", len(data))
	}
}

func TestRenderFindingMarkdownMissingRawPacket(t *testing.T) {
	f := Finding{ID: "XALG-9", Title: "命令执行", Severity: "critical", CVSS: 9.8, Target: "example.com", Endpoint: "/cmd", Method: "POST", PoCScript: "curl x", Traffic: []TrafficEvidence{{Available: false, MissingReason: "未记录流量引用"}}}
	md := string(RenderFindingMarkdown(Meta{Kind: KindFinding, TaskName: "示例系统"}, f))
	if !strings.Contains(md, "未捕获原始 HTTP 请求") {
		t.Fatalf("missing-packet notice absent:\n%s", md)
	}
	if !strings.Contains(md, "非抓包原文") {
		t.Fatalf("reconstructed PoC was not labeled:\n%s", md)
	}
}

func TestComputeRiskScoresOrdersAndBands(t *testing.T) {
	findings := []Finding{
		{ID: "XALG-1", Severity: "low", CVSS: 3.0, Tags: []string{"needs-manual-verification"}},
		{ID: "XALG-2", Severity: "high", CVSS: 8.0, Tags: []string{"exploit-proven"}},
		{ID: "XALG-3", Severity: "critical", CVSS: 9.8, Tags: []string{"verified"}},
	}
	scores := ComputeRiskScores(findings, nil)
	if len(scores) != 3 {
		t.Fatalf("scores = %d", len(scores))
	}
	if scores[0].FindingID != "XALG-3" {
		t.Fatalf("highest score is %s, want XALG-3", scores[0].FindingID)
	}
	if scores[len(scores)-1].FindingID != "XALG-1" {
		t.Fatalf("lowest score is %s, want XALG-1", scores[len(scores)-1].FindingID)
	}
	if scores[0].Base <= scores[1].Base {
		t.Fatalf("scores not descending: %+v", scores)
	}
}

func TestBuildTemplateNarrativeOnlyUsesFacts(t *testing.T) {
	spec := sampleSpec()
	// Remove an unknown key finding reference: the template never invents one.
	for _, kf := range spec.Narrative.ExecutiveSummary.KeyFindings {
		if _, ok := spec.Facts.Finding(kf.FindingID); !ok {
			t.Fatalf("template invented finding %s", kf.FindingID)
		}
	}
	if !strings.Contains(spec.Narrative.ExecutiveSummary.Conclusion, "1 个漏洞") {
		t.Fatalf("conclusion does not restate the fact count: %s", spec.Narrative.ExecutiveSummary.Conclusion)
	}
}
