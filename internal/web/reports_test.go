package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/reportdelivery"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func writeReportTestScan(t *testing.T, dataDir string, rec ScanRecord, exchange *scanctx.TrafficExchange) string {
	t.Helper()
	dir := filepath.Join(dataDir, sanitizeTestPath(rec.Target), "2026-11-23", rec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scan.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if exchange != nil {
		if err := os.MkdirAll(filepath.Join(dir, "traffic"), 0o755); err != nil {
			t.Fatal(err)
		}
		exchange.Seq = 1
		if exchange.At.IsZero() {
			exchange.At = time.Now()
		}
		raw, err := json.Marshal(exchange)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "traffic", "tr_000001.json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func sanitizeTestPath(target string) string {
	return strings.NewReplacer("://", "_", "/", "_", ":", "_").Replace(target)
}

func TestBuildReportBundleAggregatesTargetsAndSanitizesTraffic(t *testing.T) {
	dataDir := t.TempDir()
	s := newTestServer(t, &config.Config{DataDir: dataDir, RateLimitRequests: 60, RateLimitWindow: 60})
	s.dataDir = dataDir
	store, err := platform.NewFileStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPlatformStore(store)
	project, _ := store.EnsureDefaultProject(context.Background())
	task, err := store.CreateTask(context.Background(), project.ID, platform.TaskInput{Name: "示例系统"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(context.Background(), task.ID, platform.RunMount{InstanceID: "inst-1", Mode: "wildcard"})
	if err != nil {
		t.Fatal(err)
	}

	parent := ScanRecord{
		ID:          "scan-parent",
		InstanceID:  "inst-1",
		Target:      "example.com",
		ScanMode:    "wildcard",
		Status:      "finished",
		StartedAt:   "2026-11-23T20:00:00+08:00",
		FinishedAt:  "2026-11-23T20:30:00+08:00",
		Phases:      []int{1, 2, 22},
		PhaseStatus: map[string]string{"1": "completed"},
		Vulns:       []VulnSummary{},
	}
	writeReportTestScan(t, dataDir, parent, nil)

	child := ScanRecord{
		ID:           "scan-child",
		InstanceID:   "inst-1",
		Target:       "api.example.com",
		ParentTarget: "example.com",
		ScanMode:     "wildcard",
		Status:       "finished",
		StartedAt:    "2026-11-23T20:05:00+08:00",
		FinishedAt:   "2026-11-23T20:20:00+08:00",
		Vulns: []VulnSummary{{
			ID:             "XALG-1",
			Title:          "SQL 注入",
			Severity:       "high",
			CVSS:           8.1,
			CVSSVector:     "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:N",
			CWE:            "CWE-89",
			OWASP:          "A03:2021",
			Target:         "api.example.com",
			Endpoint:       "/api/user",
			Method:         "POST",
			Description:    "id 参数拼接进 SQL。",
			Impact:         "可读取用户表。",
			PoCDescription: "发送 time-based payload。",
			PoCScript:      "curl -sk -X POST 'https://api.example.com/api/user' --data 'id=1'",
			Remediation:    "使用参数化查询。",
			Verified:       true,
			Tags:           []string{"verified"},
			TrafficRefs:    []scanctx.TrafficRef{{Session: "scan-child", Seq: 1}},
		}},
	}
	writeReportTestScan(t, dataDir, child, &scanctx.TrafficExchange{
		Method:  "POST",
		URL:     "https://api.example.com/api/user",
		Status:  200,
		Channel: "proxy",
		RequestRaw: "POST /api/user HTTP/1.1\r\n" +
			"Host: api.example.com\r\n" +
			"Cookie: session=super-secret\r\n" +
			"\r\n" +
			`{"id":"1 AND SLEEP(5)"}`,
		ResponseRaw: "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"ok\":true}",
	})

	bundle, err := s.buildReportBundle(task, []platform.Run{*run}, reportCreateRequest{
		Formats: []string{"md"}, Template: "standard", Language: "zh-CN", Mode: "template",
	})
	if err != nil {
		t.Fatalf("buildReportBundle: %v", err)
	}
	if len(bundle.Total.Facts.Findings) != 1 {
		t.Fatalf("total findings = %d, want 1", len(bundle.Total.Facts.Findings))
	}
	if len(bundle.Subs) != 2 {
		t.Fatalf("sub reports = %d, want 2 (parent + child)", len(bundle.Subs))
	}
	f := bundle.Total.Facts.Findings[0]
	if len(f.Traffic) != 1 || !f.Traffic[0].Available {
		t.Fatalf("traffic evidence = %+v, want one available packet", f.Traffic)
	}
	if strings.Contains(f.Traffic[0].RequestRaw, "super-secret") {
		t.Fatalf("raw traffic was not sanitized: %s", f.Traffic[0].RequestRaw)
	}
	md := string(reportdelivery.RenderMarkdown(&bundle.Total))
	for _, want := range []string{"一、执行摘要", "三、漏洞详情", "SQL 注入", "POST /api/user HTTP/1.1"} {
		if !strings.Contains(md, want) {
			t.Fatalf("rendered report missing %q", want)
		}
	}
	if strings.Contains(md, "super-secret") {
		t.Fatalf("rendered report leaked the cookie")
	}

	report, err := store.CreateReport(context.Background(), platform.ReportMount{
		TaskID: task.ID, RunIDs: []string{run.ID}, Formats: []string{"md"}, Mode: "template",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.writeReportArtifacts(context.Background(), report, bundle, []string{"md"}); err != nil {
		t.Fatalf("writeReportArtifacts: %v", err)
	}
	artifacts, err := store.ListReportArtifacts(context.Background(), report.ID)
	if err != nil {
		t.Fatal(err)
	}
	// total (1) + two subs (2) in one requested format.
	if len(artifacts) != 3 {
		t.Fatalf("artifacts = %d, want 3: %+v", len(artifacts), artifacts)
	}
	for _, a := range artifacts {
		if _, err := os.Stat(a.Path); err != nil {
			t.Fatalf("artifact %s missing: %v", a.Path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(reportDir(dataDir, task.ID, report.ID), "report.json")); err != nil {
		t.Fatalf("report.json missing: %v", err)
	}
}

func TestHandleFindingExportSanitizesAndHandlesMissingTraffic(t *testing.T) {
	dataDir := t.TempDir()
	s := newTestServer(t, &config.Config{DataDir: dataDir, RateLimitRequests: 60, RateLimitWindow: 60})
	s.dataDir = dataDir

	rec := ScanRecord{
		ID:         "scan-export",
		InstanceID: "inst-export",
		Target:     "example.com",
		Status:     "finished",
		StartedAt:  "2026-11-23T20:00:00+08:00",
		Vulns: []VulnSummary{
			{
				ID:          "XALG-1",
				Title:       "SQL 注入",
				Severity:    "high",
				CVSS:        8.1,
				Target:      "example.com",
				Endpoint:    "/api/user",
				Method:      "POST",
				Description: "id 参数拼接进 SQL。",
				Verified:    true,
				Tags:        []string{"verified"},
				TrafficRefs: []scanctx.TrafficRef{{Session: "scan-export", Seq: 1}},
			},
			{
				ID:        "XALG-2",
				Title:     "命令执行",
				Severity:  "critical",
				CVSS:      9.8,
				Target:    "example.com",
				Endpoint:  "/cmd",
				Method:    "POST",
				PoCScript: "curl -sk -X POST 'https://example.com/cmd' --data 'cmd=id'",
			},
		},
	}
	writeReportTestScan(t, dataDir, rec, &scanctx.TrafficExchange{
		Method:      "POST",
		URL:         "https://example.com/api/user",
		Channel:     "proxy",
		RequestRaw:  "POST /api/user HTTP/1.1\r\nHost: example.com\r\nAuthorization: Bearer topsecret\r\n\r\nid=1",
		ResponseRaw: "HTTP/1.1 200 OK\r\n\r\nok",
	})

	for _, tc := range []struct {
		vulnID string
		want   []string
		deny   []string
	}{
		{vulnID: "XALG-1", want: []string{"POST /api/user HTTP/1.1", "证明数据（HTTP Raw）"}, deny: []string{"topsecret"}},
		{vulnID: "XALG-2", want: []string{"未捕获原始 HTTP 请求", "非抓包原文"}, deny: nil},
	} {
		req := httptest.NewRequest("GET", "/api/scans/scan-export/findings/"+tc.vulnID+"/export?format=md", nil)
		rr := httptest.NewRecorder()
		s.handleFindingExport(rr, req)
		if rr.Code != 200 {
			t.Fatalf("%s: status = %d, body=%s", tc.vulnID, rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		for _, want := range tc.want {
			if !strings.Contains(body, want) {
				t.Fatalf("%s: export missing %q:\n%s", tc.vulnID, want, body)
			}
		}
		for _, deny := range tc.deny {
			if strings.Contains(body, deny) {
				t.Fatalf("%s: export leaked %q", tc.vulnID, deny)
			}
		}
	}
}

func TestValidateAgentNarrativeRejectsUnverifiedMainChainAndBandJump(t *testing.T) {
	facts := reportdelivery.Facts{
		Findings: []reportdelivery.Finding{
			{ID: "XALG-1", Severity: "high", CVSS: 8.0, Verified: true, Tags: []string{"verified"}},
			{ID: "XALG-2", Severity: "low", CVSS: 3.0, Tags: []string{"needs-manual-verification"}},
		},
	}
	facts.RiskScores = reportdelivery.ComputeRiskScores(facts.Findings, nil)

	out := agentNarrativeOutput{
		AttackChains: []reportdelivery.AttackChain{{
			Title: "反序列化到 RCE", Confidence: reportdelivery.ConfidenceConfirmed,
			Steps: []reportdelivery.AttackStep{{FindingID: "XALG-2", Action: "利用未验证漏洞"}},
		}},
	}
	if err := validateAgentNarrative(facts, &out); err == nil {
		t.Fatal("unverified main-chain step was accepted")
	}

	out = agentNarrativeOutput{
		RiskAdjustments: []agentRiskAdjustment{{FindingID: "XALG-2", Band: reportdelivery.BandImmediate, Rationale: "业务关键"}},
	}
	if err := validateAgentNarrative(facts, &out); err == nil {
		t.Fatal("two-band jump was accepted")
	}

	// A valid one-band adjustment with a rationale, plus a speculative chain
	// over the unverified finding, is allowed and applied.
	out = agentNarrativeOutput{
		AttackChains: []reportdelivery.AttackChain{{
			Title: "推测链", Confidence: reportdelivery.ConfidenceSpeculative,
			Steps: []reportdelivery.AttackStep{{FindingID: "XALG-2", Action: "待验证"}},
		}},
		RiskAdjustments: []agentRiskAdjustment{{FindingID: "XALG-2", Band: reportdelivery.BandMedium, Rationale: "业务关键"}},
	}
	if err := validateAgentNarrative(facts, &out); err != nil {
		t.Fatalf("valid narrative rejected: %v", err)
	}
	applyAgentRiskAdjustments(facts.RiskScores, out.RiskAdjustments)
	for _, score := range facts.RiskScores {
		if score.FindingID == "XALG-2" && score.Band != reportdelivery.BandMedium {
			t.Fatalf("adjustment not applied: %+v", score)
		}
	}
}

func TestHandleTaskReportsManualGenerationEndToEnd(t *testing.T) {
	dataDir := t.TempDir()
	s := newTestServer(t, &config.Config{DataDir: dataDir, RateLimitRequests: 60, RateLimitWindow: 60})
	s.dataDir = dataDir
	store, err := platform.NewFileStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPlatformStore(store)
	project, _ := store.EnsureDefaultProject(context.Background())
	task, err := store.CreateTask(context.Background(), project.ID, platform.TaskInput{Name: "端到端任务"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(context.Background(), task.ID, platform.RunMount{InstanceID: "inst-e2e", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	rec := ScanRecord{
		ID:         "scan-e2e",
		InstanceID: "inst-e2e",
		Target:     "e2e.example.com",
		Status:     "finished",
		StartedAt:  "2026-11-23T20:00:00+08:00",
		FinishedAt: "2026-11-23T20:30:00+08:00",
		Vulns: []VulnSummary{{
			ID: "XALG-1", Title: "SQL 注入", Severity: "high", CVSS: 8.1,
			Target: "e2e.example.com", Endpoint: "/api", Method: "POST",
			Description: "id 参数可注入。", Verified: true, Tags: []string{"verified"},
		}},
	}
	writeReportTestScan(t, dataDir, rec, nil)

	body := `{"formats":["md"],"mode":"template","language":"zh-CN"}`
	req := httptest.NewRequest("POST", "/api/tasks/"+task.ID+"/reports", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleTaskReports(rr, req, store, task.ID)
	if rr.Code != 202 {
		t.Fatalf("POST reports status = %d body=%s", rr.Code, rr.Body.String())
	}
	var created platform.Report
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v (%s)", err, rr.Body.String())
	}
	if created.Status != platform.ReportQueued && created.Status != platform.ReportGenerating {
		t.Fatalf("initial status = %q", created.Status)
	}

	deadline := time.Now().Add(5 * time.Second)
	var final *platform.Report
	for time.Now().Before(deadline) {
		final, err = store.GetReport(context.Background(), created.ID)
		if err == nil && (final.Status == platform.ReportReady || final.Status == platform.ReportFailed) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if final == nil || final.Status != platform.ReportReady {
		t.Fatalf("report did not become ready: %+v", final)
	}
	artifacts, err := store.ListReportArtifacts(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("artifacts = %d, want 2 (total + one target sub report): %+v", len(artifacts), artifacts)
	}
	seenTotal := false
	for _, artifact := range artifacts {
		if artifact.Kind == "total" && artifact.Format == "md" {
			seenTotal = true
		}
	}
	if !seenTotal {
		t.Fatalf("total md artifact missing: %+v", artifacts)
	}

	// GET the version view and the global list through the routes.
	getReq := httptest.NewRequest("GET", "/api/reports/"+created.ID, nil)
	getRR := httptest.NewRecorder()
	s.handleReportRoutes(getRR, getReq)
	if getRR.Code != 200 {
		t.Fatalf("GET report status = %d body=%s", getRR.Code, getRR.Body.String())
	}
	var view reportView
	if err := json.Unmarshal(getRR.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.TaskName != "端到端任务" || len(view.Artifacts) != 2 {
		t.Fatalf("view = %+v", view)
	}

	listReq := httptest.NewRequest("GET", "/api/reports", nil)
	listRR := httptest.NewRecorder()
	s.handleReportRoutes(listRR, listReq)
	if listRR.Code != 200 {
		t.Fatalf("GET reports status = %d", listRR.Code)
	}
}

func TestGenerateAgentNarrativeAppliesValidatedOutput(t *testing.T) {
	s := newTestServer(t, &config.Config{RateLimitRequests: 60, RateLimitWindow: 60})
	facts := reportdelivery.Facts{
		SeverityCounts: map[string]int{"high": 1},
		Findings: []reportdelivery.Finding{{
			ID: "XALG-1", Title: "SQL 注入", Severity: "high", CVSS: 8.1,
			Target: "example.com", Endpoint: "/api", Method: "POST",
			Verified: true, Tags: []string{"verified"},
		}},
	}
	facts.RiskScores = reportdelivery.ComputeRiskScores(facts.Findings, nil)
	facts.RiskScore = reportdelivery.OverallRiskScore(facts.RiskScores)
	bundle := &reportBundle{
		Total: reportdelivery.Spec{
			SpecVersion: reportdelivery.SpecVersion,
			Meta:        reportdelivery.Meta{Kind: reportdelivery.KindTotal, Targets: []string{"example.com"}, Language: "zh-CN"},
			Facts:       facts,
		},
		Subs: []reportdelivery.Spec{{
			Meta:  reportdelivery.Meta{Kind: reportdelivery.KindSub, ScopeTarget: "example.com", Language: "zh-CN"},
			Facts: facts,
		}},
	}
	s.reportNarrativeFn = func(_ context.Context, _, _ string, _ string) (string, error) {
		return `{
		  "executive_summary": {"background": "授权测试。", "conclusion": "发现一个高危 SQL 注入。", "key_findings": [{"finding_id": "XALG-1", "text": "公网接口可注入。"}]},
		  "risk_overview": {"narrative": "该漏洞风险最高，应优先处置。"},
		  "finding_narratives": [{"finding_id": "XALG-1", "description": "id 参数可注入。", "impact": "可读取用户表。", "reproduction": "发送 time-based payload。", "remediation": "参数化查询。"}],
		  "attack_chains": [{"id": "chain-1", "title": "注入到数据读取", "confidence": "confirmed", "steps": [{"finding_id": "XALG-1", "action": "注入读取用户表"}], "impact": "数据泄露"}],
		  "remediation_plan": [{"priority": "high", "finding_ids": ["XALG-1"], "immediate": "临时限制接口", "root_fix": "参数化查询", "verification": "复测", "sla": "7 天"}],
		  "risk_adjustments": [{"finding_id": "XALG-1", "band": "immediate", "rationale": "公网未授权"}]
		}`, nil
	}
	if err := s.agentNarrative(context.Background(), bundle, ""); err != nil {
		t.Fatalf("agentNarrative: %v", err)
	}
	if !strings.Contains(bundle.Total.Narrative.ExecutiveSummary.Conclusion, "SQL 注入") {
		t.Fatalf("narrative not applied: %+v", bundle.Total.Narrative)
	}
	if len(bundle.Total.Narrative.AttackChains) != 1 {
		t.Fatalf("chains = %d, want 1", len(bundle.Total.Narrative.AttackChains))
	}
	if bundle.Total.Narrative.RemediationPlan[0].Priority != reportdelivery.BandHigh {
		t.Fatalf("remediation priority = %q", bundle.Total.Narrative.RemediationPlan[0].Priority)
	}
	if bundle.Subs[0].Narrative.ExecutiveSummary.Conclusion == "" {
		t.Fatal("sub report did not fall back to the template narrative")
	}
}
