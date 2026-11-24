package web

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/reportdelivery"
)

// reportPromptVersion is recorded on every generated report so an audit can
// tell which narrative contract produced it.
const reportPromptVersion = "report-v1"

// agentNarrative is the optional report-writing agent. It receives a fully
// assembled fact pack and may only fill the Narrative half of each Spec; the
// validator in report_agent_validate.go rejects invented findings, rewritten
// severities, uncited attack-chain steps and unjustified band changes. The
// current implementation is wired in report_agent_llm.go; this method exists so
// the generation pipeline has one stable seam.
func (s *Server) agentNarrative(ctx context.Context, bundle *reportBundle, model string) error {
	if bundle == nil {
		return fmt.Errorf("nil report bundle")
	}
	return s.generateAgentNarrative(ctx, bundle, model)
}

// handleFindingExport serves
// GET /api/scans/{scanID}/findings/{vulnID}/export?format=md|html|json.
func (s *Server) handleFindingExport(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/scans/")
	parts := strings.Split(rest, "/")
	if len(parts) != 4 || parts[1] != "findings" || parts[3] != "export" {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	scanID := parts[0]
	vulnID := parts[2]
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	switch format {
	case "md", "html", "json":
	default:
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "format must be md, html or json"})
		return
	}
	entries := s.findAllScanSummaries()
	res, ok := s.resolveScanRecordByIDFrom(scanID, func() []scanEntry { return entries })
	if !ok || res.rec == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "scan not found"})
		return
	}
	var vuln *VulnSummary
	for i := range res.rec.Vulns {
		if res.rec.Vulns[i].ID == vulnID {
			vuln = &res.rec.Vulns[i]
			break
		}
	}
	if vuln == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "finding not found"})
		return
	}
	dirBySession := map[string]string{}
	for _, entry := range entries {
		if entry.dir != "" {
			dirBySession[filepath.Base(entry.dir)] = entry.dir
		}
	}
	meta := findingMeta{}
	if loaded := loadFindingMeta(res.dir); loaded != nil {
		meta = loaded[vuln.ID]
	}
	target := strings.TrimSpace(vuln.Target)
	if target == "" {
		target = res.rec.Target
	}
	ref := reportFindingRef{v: *vuln, rec: res.rec, dir: res.dir, target: target, started: res.rec.StartedAt}
	finding := s.reportFinding(ref, meta, dirBySession)

	reportMeta := reportdelivery.Meta{
		Kind:        reportdelivery.KindFinding,
		TaskID:      res.rec.TaskID,
		TaskName:    res.rec.Name,
		ScanIDs:     []string{res.rec.ID},
		Targets:     []string{target},
		Company:     res.rec.Name,
		Language:    "zh-CN",
		Template:    "standard",
		Generator:   reportdelivery.GeneratorTemplate,
		GeneratedAt: time.Now(),
	}
	reportMeta.Title = reportdelivery.FindingTitle(reportMeta, finding)

	var data []byte
	var err error
	var contentType, ext string
	switch format {
	case "md":
		data = reportdelivery.RenderFindingMarkdown(reportMeta, finding)
		contentType, ext = "text/markdown; charset=utf-8", "md"
	case "html":
		data = reportdelivery.RenderFindingHTML(reportMeta, finding)
		contentType, ext = "text/html; charset=utf-8", "html"
	case "json":
		data, err = reportdelivery.RenderFindingJSON(reportMeta, finding)
		contentType, ext = "application/json; charset=utf-8", "json"
	}
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if r.URL.Query().Get("download") != "1" {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
		return
	}
	name := safeFileComponent(reportMeta.Title)
	if name == "" {
		name = safeFileComponent(vuln.ID)
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+"."+ext))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	_, _ = w.Write(data)
}
