package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/reportdelivery"
	pdfreport "github.com/c1ayooo/xalgorix/v4/internal/reporting"
	"github.com/c1ayooo/xalgorix/v4/internal/safe"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/storage"
)

// Manual report delivery (REPORT_DELIVERY_SPEC). Generation is always
// operator-triggered: no scan lifecycle hook creates a report artifact.

const (
	reportDefaultLanguage = "zh-CN"
	reportDefaultTemplate = "standard"
	reportMaxTrafficRefs  = 5
	reportMaxRawBytes     = 128 << 10
	reportJobTimeout      = 15 * time.Minute
)

var reportSupportedFormats = map[string]bool{
	"pdf":  true,
	"md":   true,
	"html": true,
}

type reportJob struct {
	reportID string
	taskID   string
	mu       sync.Mutex
	progress string
	cancel   context.CancelFunc
}

func (j *reportJob) setProgress(text string) {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.progress = text
	j.mu.Unlock()
}

func (j *reportJob) currentProgress() string {
	if j == nil {
		return ""
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.progress
}

type reportCreateRequest struct {
	RunIDs   []string `json:"run_ids"`
	Formats  []string `json:"formats"`
	Template string   `json:"template"`
	Language string   `json:"language"`
	Mode     string   `json:"mode"`
	Model    string   `json:"model"`
}

type reportArtifactView struct {
	Kind   string `json:"kind"`
	ScanID string `json:"scan_id,omitempty"`
	Target string `json:"target,omitempty"`
	Format string `json:"format"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256,omitempty"`
	URL    string `json:"url"`
}

type reportView struct {
	platform.Report
	Artifacts []reportArtifactView `json:"artifacts"`
	Progress  string               `json:"progress,omitempty"`
	// Response-only enrichment for the task-level list.
	TaskName string   `json:"task_name,omitempty"`
	Targets  []string `json:"targets,omitempty"`
}

func (s *Server) reportStore() (platform.ReportStore, bool) {
	store := s.currentPlatformStore()
	if store == nil {
		return nil, false
	}
	rs, ok := store.(platform.ReportStore)
	return rs, ok
}

func (s *Server) putReportJob(job *reportJob) {
	if job == nil {
		return
	}
	s.reportJobsMu.Lock()
	if s.reportJobs == nil {
		s.reportJobs = map[string]*reportJob{}
	}
	s.reportJobs[job.reportID] = job
	s.reportJobsMu.Unlock()
}

func (s *Server) getReportJob(reportID string) *reportJob {
	s.reportJobsMu.Lock()
	defer s.reportJobsMu.Unlock()
	return s.reportJobs[reportID]
}

func (s *Server) dropReportJob(reportID string) {
	s.reportJobsMu.Lock()
	delete(s.reportJobs, reportID)
	s.reportJobsMu.Unlock()
}

// handleTaskReports serves GET/POST /api/tasks/{id}/reports.
func (s *Server) handleTaskReports(w http.ResponseWriter, r *http.Request, store platform.Repository, taskID string) {
	reportStore, ok := s.reportStore()
	if !ok {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "report store not available"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		reports, err := reportStore.ListReportsByTask(r.Context(), taskID)
		if err != nil {
			writePlatformError(w, err)
			return
		}
		views := make([]reportView, 0, len(reports))
		cache := s.newReportViewCache()
		for i := range reports {
			views = append(views, s.buildReportViewWith(r.Context(), reportStore, &reports[i], cache))
		}
		writeJSONStatus(w, http.StatusOK, map[string]any{"items": views})
	case http.MethodPost:
		limitJSONBody(w, r)
		var req reportCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		req, err := normalizeReportRequest(req)
		if err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		task, err := store.GetTask(r.Context(), taskID)
		if err != nil {
			writePlatformError(w, err)
			return
		}
		runs, err := s.taskRunsForReport(r.Context(), store, taskID, req.RunIDs)
		if err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if len(runs) == 0 {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "该任务还没有可用的运行记录，无法生成报告"})
			return
		}
		// A freshly dispatched run may not have a scan record yet. Default to
		// the newest run that does; an explicitly requested run without one is
		// a 400, not a silently empty report.
		hasRecords := s.runsWithReportRecords(runs)
		if len(req.RunIDs) == 0 {
			reportable := make([]platform.Run, 0, len(runs))
			for _, run := range runs {
				if hasRecords[run.ID] {
					reportable = append(reportable, run)
				}
			}
			if len(reportable) == 0 {
				writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "最近的运行还没有扫描记录，请等待扫描产生结果后再生成报告"})
				return
			}
			runs = reportable[:1]
		} else {
			for _, run := range runs {
				if !hasRecords[run.ID] {
					writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "所选运行还没有扫描记录：" + run.ID})
					return
				}
			}
		}
		runIDs := make([]string, 0, len(runs))
		for _, run := range runs {
			runIDs = append(runIDs, run.ID)
		}
		report, err := reportStore.CreateReport(r.Context(), platform.ReportMount{
			TaskID:        taskID,
			RunIDs:        runIDs,
			Formats:       req.Formats,
			Template:      req.Template,
			Language:      req.Language,
			Mode:          req.Mode,
			Model:         strings.TrimSpace(req.Model),
			PromptVersion: reportPromptVersion,
		})
		if err != nil {
			if errors.Is(err, platform.ErrConflict) {
				writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "该任务已有报告正在生成，请稍后再试"})
				return
			}
			writePlatformError(w, err)
			return
		}
		s.launchReportGeneration(report, task, runs, req)
		writeJSONStatus(w, http.StatusAccepted, s.buildReportView(r.Context(), reportStore, report))
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleReportRoutes serves /api/reports/{id}[/download/{format}|/cancel].
func (s *Server) handleReportRoutes(w http.ResponseWriter, r *http.Request) {
	reportStore, ok := s.reportStore()
	if !ok {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "report store not available"})
		return
	}
	if r.URL.Path == "/api/reports" || r.URL.Path == "/api/reports/" {
		if r.Method != http.MethodGet {
			writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		lister, ok := s.currentPlatformStore().(platform.ReportLister)
		if !ok {
			writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "report list not available"})
			return
		}
		limit := 200
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				limit = n
			}
		}
		reports, err := lister.ListReports(r.Context(), limit)
		if err != nil {
			writePlatformError(w, err)
			return
		}
		views := make([]reportView, 0, len(reports))
		cache := s.newReportViewCache()
		for i := range reports {
			views = append(views, s.buildReportViewWith(r.Context(), reportStore, &reports[i], cache))
		}
		writeJSONStatus(w, http.StatusOK, map[string]any{"items": views})
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/reports/"), "/")
	if rest == "" {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	parts := strings.Split(rest, "/")
	reportID := parts[0]
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		report, err := reportStore.GetReport(r.Context(), reportID)
		if err != nil {
			writePlatformError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusOK, s.buildReportView(r.Context(), reportStore, report))
	case len(parts) == 1 && r.Method == http.MethodDelete:
		s.deleteReport(w, r, reportStore, reportID)
	case len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost:
		s.cancelReport(w, r, reportStore, reportID)
	case len(parts) == 3 && parts[1] == "download" && r.Method == http.MethodGet:
		s.downloadReportArtifact(w, r, reportStore, reportID, parts[2])
	default:
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (s *Server) buildReportView(ctx context.Context, store platform.ReportStore, report *platform.Report) reportView {
	return s.buildReportViewWith(ctx, store, report, nil)
}

// reportViewCache memoizes the two lookups a report list repeats: scan id →
// target (one shared summary walk) and task id → name (one query per distinct
// task, not per report row).
type reportViewCache struct {
	targetsByScanID map[string]string
	taskNames       map[string]string
}

func (s *Server) newReportViewCache() *reportViewCache {
	cache := &reportViewCache{
		targetsByScanID: map[string]string{},
		taskNames:       map[string]string{},
	}
	for _, entry := range s.findAllScanSummaries() {
		if entry.rec.ID != "" && strings.TrimSpace(entry.rec.Target) != "" {
			cache.targetsByScanID[entry.rec.ID] = strings.TrimSpace(entry.rec.Target)
		}
	}
	return cache
}

func (s *Server) buildReportViewWith(ctx context.Context, store platform.ReportStore, report *platform.Report, cache *reportViewCache) reportView {
	if cache == nil {
		cache = s.newReportViewCache()
	}
	view := reportView{Report: *report}
	if job := s.getReportJob(report.ID); job != nil {
		view.Progress = job.currentProgress()
	}
	if name, ok := cache.taskNames[report.TaskID]; ok {
		view.TaskName = name
	} else if pstore := s.currentPlatformStore(); pstore != nil {
		if task, err := pstore.GetTask(ctx, report.TaskID); err == nil && task != nil {
			view.TaskName = task.Name
			cache.taskNames[report.TaskID] = task.Name
		}
	}
	view.Targets = reportTargetsFromCache(report.ScanIDs, cache.targetsByScanID)
	artifacts, err := store.ListReportArtifacts(ctx, report.ID)
	if err == nil {
		for _, a := range artifacts {
			q := url.Values{}
			q.Set("kind", a.Kind)
			if a.ScanID != "" {
				q.Set("scan_id", a.ScanID)
			}
			if a.Kind == reportdelivery.KindSub && a.Target != "" {
				q.Set("target", a.Target)
			}
			view.Artifacts = append(view.Artifacts, reportArtifactView{
				Kind:   a.Kind,
				ScanID: a.ScanID,
				Target: a.Target,
				Format: a.Format,
				Bytes:  a.Bytes,
				SHA256: a.SHA256,
				URL:    fmt.Sprintf("/api/reports/%s/download/%s?%s", report.ID, a.Format, q.Encode()),
			})
		}
	}
	if view.Artifacts == nil {
		view.Artifacts = []reportArtifactView{}
	}
	return view
}

// taskHasActiveReport reports whether a task still has a queued/generating
// report. Task deletion must be rejected in that case: removing the row and
// directory under a live job would leave an orphan writer.
func (s *Server) taskHasActiveReport(ctx context.Context, taskID string) bool {
	store, ok := s.reportStore()
	if !ok {
		return false
	}
	reports, err := store.ListReportsByTask(ctx, taskID)
	if err != nil {
		return false
	}
	for _, report := range reports {
		if report.Status == platform.ReportQueued || report.Status == platform.ReportGenerating {
			return true
		}
	}
	return false
}

func (s *Server) deleteTaskReportDirs(ctx context.Context, taskID string) {
	store, ok := s.reportStore()
	if !ok {
		return
	}
	reports, err := store.ListReportsByTask(ctx, taskID)
	if err != nil {
		log.Printf("[report] list task reports for deletion: %v", err)
		return
	}
	for _, report := range reports {
		dir := reportDir(s.dataDir, report.TaskID, report.ID)
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("[report] remove %s: %v", dir, err)
		}
	}
}

func (s *Server) deleteReport(w http.ResponseWriter, r *http.Request, store platform.ReportStore, reportID string) {
	report, err := store.GetReport(r.Context(), reportID)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	if report.Status == platform.ReportQueued || report.Status == platform.ReportGenerating {
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "报告正在生成，请先取消再删除"})
		return
	}
	if job := s.getReportJob(reportID); job != nil && job.cancel != nil {
		job.cancel()
	}
	if err := store.DeleteReport(r.Context(), reportID); err != nil {
		writePlatformError(w, err)
		return
	}
	s.dropReportJob(reportID)
	dir := reportDir(s.dataDir, report.TaskID, report.ID)
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("[report] remove %s: %v", dir, err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) cancelReport(w http.ResponseWriter, r *http.Request, store platform.ReportStore, reportID string) {
	report, err := store.GetReport(r.Context(), reportID)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	if report.Status != platform.ReportQueued && report.Status != platform.ReportGenerating {
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": "报告已结束，无需取消"})
		return
	}
	if job := s.getReportJob(reportID); job != nil && job.cancel != nil {
		job.cancel()
	}
	if err := store.UpdateReportStatus(r.Context(), reportID, platform.ReportCancelled, "cancelled by operator"); err != nil {
		writePlatformError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]string{"status": platform.ReportCancelled})
}

func (s *Server) downloadReportArtifact(w http.ResponseWriter, r *http.Request, store platform.ReportStore, reportID, format string) {
	format = strings.ToLower(strings.TrimSpace(format))
	if !reportSupportedFormats[format] {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "unsupported format"})
		return
	}
	report, err := store.GetReport(r.Context(), reportID)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = reportdelivery.KindTotal
	}
	scanID := strings.TrimSpace(r.URL.Query().Get("scan_id"))
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	artifacts, err := store.ListReportArtifacts(r.Context(), reportID)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	var chosen *platform.ReportArtifact
	for i := range artifacts {
		a := &artifacts[i]
		if a.Format != format || a.Kind != kind {
			continue
		}
		if scanID != "" && a.ScanID != scanID {
			continue
		}
		if target != "" && a.Target != target {
			continue
		}
		chosen = a
		break
	}
	if chosen == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "artifact not found"})
		return
	}
	dir := reportDir(s.dataDir, report.TaskID, report.ID)
	absDir, err := filepath.Abs(dir)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "invalid report directory"})
		return
	}
	abs, err := filepath.Abs(chosen.Path)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "invalid artifact path"})
		return
	}
	rel, err := filepath.Rel(absDir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "artifact path escapes the report directory"})
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "artifact file missing"})
		return
	}
	filename := fmt.Sprintf("%s-r%d-%s.%s", safeFileComponent(report.TaskID), report.Revision, kind, format)
	contentType := "application/octet-stream"
	switch format {
	case "pdf":
		contentType = "application/pdf"
	case "md":
		contentType = "text/markdown; charset=utf-8"
	case "html":
		contentType = "text/html; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	_, _ = w.Write(data)
}

func normalizeReportRequest(req reportCreateRequest) (reportCreateRequest, error) {
	req.Template = strings.TrimSpace(req.Template)
	if req.Template == "" {
		req.Template = reportDefaultTemplate
	}
	if req.Template != reportDefaultTemplate {
		return req, fmt.Errorf("unsupported template %q", req.Template)
	}
	req.Language = strings.TrimSpace(req.Language)
	if req.Language == "" {
		req.Language = reportDefaultLanguage
	}
	if req.Language != "zh-CN" && req.Language != "en" {
		return req, fmt.Errorf("unsupported language %q", req.Language)
	}
	req.Mode = strings.TrimSpace(req.Mode)
	if req.Mode == "" {
		req.Mode = reportdelivery.GeneratorTemplate
	}
	if req.Mode != reportdelivery.GeneratorTemplate && req.Mode != reportdelivery.GeneratorAgent {
		return req, fmt.Errorf("unsupported mode %q", req.Mode)
	}
	if len(req.Formats) == 0 {
		req.Formats = []string{"pdf", "md", "html"}
	}
	seen := map[string]struct{}{}
	formats := make([]string, 0, len(req.Formats))
	for _, f := range req.Formats {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		if !reportSupportedFormats[f] {
			return req, fmt.Errorf("unsupported format %q", f)
		}
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		formats = append(formats, f)
	}
	if len(formats) == 0 {
		return req, fmt.Errorf("at least one format is required")
	}
	req.Formats = formats
	req.RunIDs = dedupeNonEmpty(req.RunIDs)
	return req, nil
}

// taskRunsForReport returns the runs a report covers. Empty requested run_ids
// means the newest run (manual generation always starts from a concrete run).
func (s *Server) taskRunsForReport(ctx context.Context, store platform.Repository, taskID string, requested []string) ([]platform.Run, error) {
	var all []platform.Run
	for page := 1; page <= 20; page++ {
		items, total, err := store.ListRuns(ctx, taskID, page, 500)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) == 0 || len(all) >= total {
			break
		}
	}
	if len(requested) == 0 {
		if len(all) == 0 {
			return nil, nil
		}
		return all[:1], nil
	}
	want := map[string]struct{}{}
	for _, id := range requested {
		want[id] = struct{}{}
	}
	out := make([]platform.Run, 0, len(requested))
	for _, run := range all {
		if _, ok := want[run.ID]; ok {
			out = append(out, run)
			delete(want, run.ID)
		}
	}
	if len(want) > 0 {
		return nil, fmt.Errorf("run not found in this task: %s", strings.Join(sortedKeys(want), ", "))
	}
	return out, nil
}

// runsWithReportRecords reports which runs have at least one scan record
// (including wildcard children and D11 sibling sessions).
func (s *Server) runsWithReportRecords(runs []platform.Run) map[string]bool {
	idx := s.scanSummaryIndexFor(s.findAllScanSummaries())
	out := make(map[string]bool, len(runs))
	for _, run := range runs {
		out[run.ID] = len(idx.lookupAll(run.InstanceID, run.LegacyScanID)) > 0
	}
	return out
}

// launchReportGeneration runs one report job. The durable status row is the
// authority; this goroutine only fills progress and artifacts.
func (s *Server) launchReportGeneration(report *platform.Report, task *platform.Task, runs []platform.Run, req reportCreateRequest) {
	store, ok := s.reportStore()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), reportJobTimeout)
	job := &reportJob{reportID: report.ID, taskID: report.TaskID, progress: "已排队", cancel: cancel}
	s.putReportJob(job)
	safe.Go("report.generate", report.TaskID, func() {
		defer cancel()
		defer s.dropReportJob(report.ID)
		fail := func(stage string, err error) {
			log.Printf("[report] %s %s failed at %s: %v", report.TaskID, report.ID, stage, err)
			job.setProgress("失败：" + err.Error())
			// An operator cancel is final: never overwrite it with a failure
			// from the job unwinding.
			if current, getErr := store.GetReport(context.Background(), report.ID); getErr == nil && current.Status == platform.ReportCancelled {
				return
			}
			_ = store.UpdateReportStatus(context.Background(), report.ID, platform.ReportFailed, stage+": "+err.Error())
		}
		defer func() {
			if r := recover(); r != nil {
				fail("panic", fmt.Errorf("%v", r))
			}
		}()
		if err := store.UpdateReportStatus(ctx, report.ID, platform.ReportGenerating, ""); err != nil {
			fail("mark generating", err)
			return
		}
		job.setProgress("装配事实")
		bundle, err := s.buildReportBundle(task, runs, req)
		if err != nil {
			fail("assemble facts", err)
			return
		}
		job.setProgress("生成叙述")
		s.applyReportNarrative(ctx, bundle, req, job)
		if ctx.Err() != nil {
			fail("cancelled", ctx.Err())
			return
		}
		job.setProgress("渲染文件")
		if err := s.writeReportArtifacts(ctx, report, bundle, req.Formats); err != nil {
			if ctx.Err() != nil {
				fail("cancelled", ctx.Err())
				return
			}
			fail("render artifacts", err)
			return
		}
		if current, getErr := store.GetReport(context.Background(), report.ID); getErr == nil && current.Status == platform.ReportCancelled {
			return
		}
		if err := store.UpdateReportStatus(context.Background(), report.ID, platform.ReportReady, ""); err != nil {
			fail("mark ready", err)
			return
		}
		job.setProgress("完成")
	})
}

// applyReportNarrative fills the narrative package. Template mode is
// deterministic; agent mode is layered on top later (see agent.go).
func (s *Server) applyReportNarrative(ctx context.Context, bundle *reportBundle, req reportCreateRequest, job *reportJob) {
	if bundle == nil {
		return
	}
	if req.Mode == reportdelivery.GeneratorAgent {
		job.setProgress("报告撰写 Agent")
		if err := s.agentNarrative(ctx, bundle, req.Model); err != nil {
			log.Printf("[report] agent narrative failed, falling back to template: %v", err)
			job.setProgress("Agent 失败，回退模板")
		} else {
			return
		}
	}
	bundle.Total.Narrative = reportdelivery.BuildTemplateNarrative(bundle.Total.Facts, bundle.Total.Meta)
	for i := range bundle.Subs {
		bundle.Subs[i].Narrative = reportdelivery.BuildTemplateNarrative(bundle.Subs[i].Facts, bundle.Subs[i].Meta)
	}
}

type reportBundle struct {
	Total reportdelivery.Spec   `json:"total"`
	Subs  []reportdelivery.Spec `json:"subs"`
}

func (s *Server) writeReportArtifacts(ctx context.Context, report *platform.Report, bundle *reportBundle, formats []string) error {
	dir := reportDir(s.dataDir, report.TaskID, report.ID)
	if err := storage.EnsureSecureDir(dir); err != nil {
		return fmt.Errorf("create report dir: %w", err)
	}
	if err := writeReportJSON(dir, bundle); err != nil {
		return err
	}
	store, ok := s.reportStore()
	if !ok {
		return fmt.Errorf("report store not available")
	}
	artifacts := make([]platform.ReportArtifact, 0, len(formats)*(1+len(bundle.Subs)))
	for _, format := range formats {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := renderReportFormat(&bundle.Total, format)
		if err != nil {
			return fmt.Errorf("render total %s: %w", format, err)
		}
		path := filepath.Join(dir, "report."+format)
		if err := storage.WriteAtomic(path, data); err != nil {
			return fmt.Errorf("write total %s: %w", format, err)
		}
		artifacts = append(artifacts, newReportArtifact(report.ID, reportdelivery.KindTotal, "", "", format, path, data))
	}
	if len(bundle.Subs) > 0 {
		subDir := filepath.Join(dir, "sub")
		if err := storage.EnsureSecureDir(subDir); err != nil {
			return fmt.Errorf("create sub report dir: %w", err)
		}
		for i := range bundle.Subs {
			if err := ctx.Err(); err != nil {
				return err
			}
			sub := &bundle.Subs[i]
			base := safeFileComponent(sub.Meta.ScopeTarget)
			if base == "" {
				base = fmt.Sprintf("sub-%d", i+1)
			}
			scanID := firstString(sub.Meta.ScanIDs)
			if scanID == "" {
				// The artifact PK is (report,kind,scan_id,format); a target-only
				// sub report needs a stable non-empty key or a later target would
				// overwrite it.
				scanID = firstNonEmptyString(sub.Meta.ScopeTarget, fmt.Sprintf("sub-%d", i+1))
			}
			for _, format := range formats {
				data, err := renderReportFormat(sub, format)
				if err != nil {
					return fmt.Errorf("render sub %s %s: %w", sub.Meta.ScopeTarget, format, err)
				}
				path := filepath.Join(subDir, fmt.Sprintf("%s-%s.%s", base, scanIDOrFallback(scanID, i), format))
				if err := storage.WriteAtomic(path, data); err != nil {
					return fmt.Errorf("write sub %s %s: %w", sub.Meta.ScopeTarget, format, err)
				}
				artifacts = append(artifacts, newReportArtifact(report.ID, reportdelivery.KindSub, scanID, sub.Meta.ScopeTarget, format, path, data))
			}
		}
	}
	return store.PutReportArtifacts(ctx, report.ID, artifacts)
}

func writeReportJSON(dir string, bundle *reportBundle) error {
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report spec: %w", err)
	}
	return storage.WriteAtomic(filepath.Join(dir, "report.json"), data)
}

func renderReportFormat(spec *reportdelivery.Spec, format string) ([]byte, error) {
	switch format {
	case "md":
		return reportdelivery.RenderMarkdown(spec), nil
	case "html":
		return reportdelivery.RenderHTML(spec), nil
	case "pdf":
		return reportdelivery.RenderPDF(spec)
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
}

func newReportArtifact(reportID, kind, scanID, target, format, path string, data []byte) platform.ReportArtifact {
	sum := sha256.Sum256(data)
	return platform.ReportArtifact{
		ReportID: reportID,
		Kind:     kind,
		ScanID:   scanID,
		Target:   target,
		Format:   format,
		Path:     path,
		Bytes:    int64(len(data)),
		SHA256:   hex.EncodeToString(sum[:]),
	}
}

// ── fact assembly ───────────────────────────────────────────────────────────

type reportRecordRef struct {
	rec *ScanRecord
	dir string
}

type reportFindingRef struct {
	v       VulnSummary
	rec     *ScanRecord
	dir     string
	target  string
	started string
	// scanID is the scan record that owns the finding's target. For wildcard
	// runs the finding may be stored in the parent record while its target is
	// a subdomain; the sub report and "source scan" field should name the
	// child record.
	scanID string
}

func (s *Server) buildReportBundle(task *platform.Task, runs []platform.Run, req reportCreateRequest) (*reportBundle, error) {
	if task == nil {
		return nil, fmt.Errorf("task is required")
	}
	entries := s.findAllScanSummaries()
	idx := s.scanSummaryIndexFor(entries)
	records := s.reportRecordsForRuns(runs, idx)
	if len(records) == 0 {
		return nil, fmt.Errorf("所选运行还没有扫描记录")
	}
	refs := dedupeReportFindings(records)
	targets := reportTargets(records, refs)
	scans := reportScanIDs(records)
	window := reportTestingWindow(records)
	meta := reportdelivery.Meta{
		TaskID:         task.ID,
		TaskName:       task.Name,
		ProjectID:      task.ProjectID,
		RunIDs:         runIDsOf(runs),
		ScanIDs:        scans,
		Targets:        targets,
		Company:        task.Name,
		Classification: "内部",
		Language:       req.Language,
		Template:       req.Template,
		Generator:      req.Mode,
		Model:          req.Model,
		PromptVersion:  reportPromptVersion,
		GeneratedAt:    time.Now(),
		TestingWindow:  window,
	}
	title := strings.TrimSpace(task.Name)
	if title == "" {
		title = strings.Join(targets, "、")
	}
	if title == "" {
		title = "安全测试"
	}
	meta.Kind = reportdelivery.KindTotal
	meta.Title = title + " 渗透测试报告"

	bundle := &reportBundle{Total: reportdelivery.Spec{SpecVersion: reportdelivery.SpecVersion, Meta: meta}}
	bundle.Total.Facts = s.buildReportFacts(refs, records)
	bundle.Total.Facts.SubReports = append([]string(nil), targets...)

	// One sub report per target/subdomain, including targets with no findings.
	byTarget := map[string][]reportFindingRef{}
	for _, ref := range refs {
		byTarget[ref.target] = append(byTarget[ref.target], ref)
	}
	targetRecords := map[string][]reportRecordRef{}
	for _, rec := range records {
		target := strings.TrimSpace(rec.rec.Target)
		if target == "" {
			continue
		}
		targetRecords[target] = append(targetRecords[target], rec)
	}
	sort.Strings(targets)
	for _, target := range targets {
		subMeta := meta
		subMeta.Kind = reportdelivery.KindSub
		subMeta.ScopeTarget = target
		subMeta.Title = fmt.Sprintf("%s - %s 渗透测试报告", title, target)
		if recs := targetRecords[target]; len(recs) > 0 {
			subMeta.ScanIDs = reportScanIDs(recs)
		} else {
			subMeta.ScanIDs = nil
		}
		sub := reportdelivery.Spec{SpecVersion: reportdelivery.SpecVersion, Meta: subMeta}
		sub.Facts = s.buildReportFacts(byTarget[target], targetRecords[target])
		bundle.Subs = append(bundle.Subs, sub)
	}
	return bundle, nil
}

func (s *Server) reportRecordsForRuns(runs []platform.Run, idx *scanSummaryIndex) []reportRecordRef {
	seen := map[string]struct{}{}
	var out []reportRecordRef
	for _, run := range runs {
		for _, rec := range idx.lookupAll(run.InstanceID, run.LegacyScanID) {
			if rec == nil || rec.ID == "" {
				continue
			}
			if _, ok := seen[rec.ID]; ok {
				continue
			}
			seen[rec.ID] = struct{}{}
			out = append(out, reportRecordRef{rec: rec, dir: idx.dirByScanID[rec.ID]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rec.StartedAt < out[j].rec.StartedAt })
	return out
}

// dedupeReportFindings collapses the same finding across a run's sibling
// sessions and wildcard parent/child records. The newest record wins, which
// matches the findings list projection.
func dedupeReportFindings(records []reportRecordRef) []reportFindingRef {
	byKey := map[string]reportFindingRef{}
	order := []string{}
	// records is oldest-first, so the last record for a target is the newest.
	targetScan := map[string]string{}
	for _, rr := range records {
		if target := strings.TrimSpace(rr.rec.Target); target != "" {
			targetScan[target] = rr.rec.ID
		}
	}
	for _, rr := range records {
		for _, v := range rr.rec.Vulns {
			target := strings.TrimSpace(v.Target)
			if target == "" {
				target = strings.TrimSpace(rr.rec.Target)
			}
			key := dedupFindingKey(target, v)
			existing, ok := byKey[key]
			if ok && existing.started > rr.rec.StartedAt {
				continue
			}
			if !ok {
				order = append(order, key)
			}
			scanID := rr.rec.ID
			if mapped := targetScan[target]; mapped != "" {
				scanID = mapped
			}
			byKey[key] = reportFindingRef{
				v:       v,
				rec:     rr.rec,
				dir:     rr.dir,
				target:  target,
				started: rr.rec.StartedAt,
				scanID:  scanID,
			}
		}
	}
	out := make([]reportFindingRef, 0, len(order))
	for _, key := range order {
		out = append(out, byKey[key])
	}
	return out
}

func reportTargets(records []reportRecordRef, refs []reportFindingRef) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(target string) {
		target = strings.TrimSpace(target)
		if target == "" {
			return
		}
		if _, ok := seen[target]; ok {
			return
		}
		seen[target] = struct{}{}
		out = append(out, target)
	}
	for _, rr := range records {
		add(rr.rec.Target)
	}
	for _, ref := range refs {
		add(ref.target)
	}
	sort.Strings(out)
	return out
}

func reportScanIDs(records []reportRecordRef) []string {
	out := make([]string, 0, len(records))
	seen := map[string]struct{}{}
	for _, rr := range records {
		if rr.rec.ID == "" {
			continue
		}
		if _, ok := seen[rr.rec.ID]; ok {
			continue
		}
		seen[rr.rec.ID] = struct{}{}
		out = append(out, rr.rec.ID)
	}
	sort.Strings(out)
	return out
}

func reportTestingWindow(records []reportRecordRef) reportdelivery.TimeWindow {
	var start, end string
	for _, rr := range records {
		if rr.rec.StartedAt != "" && (start == "" || rr.rec.StartedAt < start) {
			start = rr.rec.StartedAt
		}
		if rr.rec.FinishedAt != "" && rr.rec.FinishedAt > end {
			end = rr.rec.FinishedAt
		}
	}
	return reportdelivery.TimeWindow{Start: reportTimeDisplay(start), End: reportTimeDisplay(end)}
}

func (s *Server) buildReportFacts(refs []reportFindingRef, records []reportRecordRef) reportdelivery.Facts {
	facts := reportdelivery.Facts{SeverityCounts: map[string]int{}}
	dirBySession := map[string]string{}
	for _, rr := range records {
		if rr.dir != "" {
			dirBySession[filepath.Base(rr.dir)] = rr.dir
		}
	}
	metaByDir := map[string]map[string]findingMeta{}
	for _, ref := range refs {
		meta := findingMeta{}
		if ref.dir != "" {
			if cached, ok := metaByDir[ref.dir]; ok {
				meta = cached[ref.v.ID]
			} else {
				loaded := loadFindingMeta(ref.dir)
				metaByDir[ref.dir] = loaded
				meta = loaded[ref.v.ID]
			}
		}
		f := s.reportFinding(ref, meta, dirBySession)
		facts.Findings = append(facts.Findings, f)
		bucket := normalizeSeverityBucket(f.Severity)
		facts.SeverityCounts[bucket]++
	}
	facts.RiskScores = reportdelivery.ComputeRiskScores(facts.Findings, nil)
	facts.RiskScore = reportdelivery.OverallRiskScore(facts.RiskScores)
	facts.Findings = orderFindingsByRisk(facts.Findings, facts.RiskScores)
	facts.Assets = s.reportAssets(records)
	facts.Coverage = reportCoverage(records, facts.Findings)
	return facts
}

func (s *Server) reportFinding(ref reportFindingRef, meta findingMeta, dirBySession map[string]string) reportdelivery.Finding {
	v := ref.v
	target := strings.TrimSpace(v.Target)
	if target == "" {
		target = ref.target
	}
	f := reportdelivery.Finding{
		ID:                 v.ID,
		ScanID:             firstNonEmptyString(ref.scanID, ref.rec.ID),
		Title:              v.Title,
		Severity:           v.Severity,
		OriginalSeverity:   meta.OriginalSeverity,
		CVSS:               v.CVSS,
		CVSSVector:         v.CVSSVector,
		CWE:                v.CWE,
		OWASP:              v.OWASP,
		CVE:                v.CVE,
		Target:             target,
		Endpoint:           v.Endpoint,
		Method:             v.Method,
		Description:        reportdelivery.SanitizeText(v.Description),
		Impact:             reportdelivery.SanitizeText(v.Impact),
		TechnicalAnalysis:  reportdelivery.SanitizeText(v.TechnicalAnalysis),
		PoCDescription:     reportdelivery.SanitizeText(v.PoCDescription),
		PoCScript:          reportdelivery.SanitizeText(v.PoCScript),
		ExploitationProof:  reportdelivery.SanitizeText(v.ExploitationProof),
		Remediation:        reportdelivery.SanitizeText(v.Remediation),
		Fix:                reportdelivery.SanitizeText(v.Fix),
		VerificationMethod: v.VerificationMethod,
		Verified:           v.Verified,
		Tags:               append([]string(nil), v.Tags...),
		DiscoveredAt:       firstNonEmptyString(meta.Timestamp, ""),
		EvidenceRefs:       []string{"finding_ref:" + v.ID},
	}
	f.Traffic = s.resolveReportTraffic(v.TrafficRefs, ref.dir, dirBySession)
	return f
}

func (s *Server) resolveReportTraffic(refs []scanctx.TrafficRef, fallbackDir string, dirBySession map[string]string) []reportdelivery.TrafficEvidence {
	if len(refs) == 0 {
		return []reportdelivery.TrafficEvidence{{
			Available:     false,
			MissingReason: "未记录流量引用（代理未开启或流量未归因）",
		}}
	}
	out := make([]reportdelivery.TrafficEvidence, 0, len(refs))
	for i, ref := range refs {
		if i >= reportMaxTrafficRefs {
			break
		}
		evidence := reportdelivery.TrafficEvidence{Session: ref.Session, Seq: ref.Seq, Available: false}
		dir := ""
		if ref.Session != "" {
			dir = dirBySession[ref.Session]
			if dir == "" {
				if resolved, ok := s.resolveScanDirByID(ref.Session); ok {
					dir = resolved
				}
			}
		}
		if dir == "" {
			// A TrafficRef's session is part of its identity. Falling back to
			// another directory could bind a same-numbered sequence from the
			// wrong session; only legacy refs with no session use the owning
			// record's directory.
			if ref.Session == "" {
				dir = fallbackDir
			} else {
				evidence.MissingReason = "找不到该流量会话目录"
				out = append(out, evidence)
				continue
			}
		}
		ex, err := scanctx.ReadTrafficExchangeAt(dir, ref.Seq)
		if err != nil {
			evidence.MissingReason = "读取流量归档失败：" + err.Error()
			out = append(out, evidence)
			continue
		}
		if ex == nil {
			evidence.MissingReason = "归档中未找到该请求"
			out = append(out, evidence)
			continue
		}
		if !ex.At.IsZero() {
			evidence.CapturedAt = reportTimeDisplay(ex.At.Format(time.RFC3339))
		}
		evidence.Channel = ex.Channel
		evidence.RequestTruncated = ex.ReqTruncated
		evidence.ResponseTruncated = ex.RespTruncated
		req, reqCut := truncateRaw(reportdelivery.SanitizeHTTPRaw(ex.RequestRaw))
		resp, respCut := truncateRaw(reportdelivery.SanitizeHTTPRaw(ex.ResponseRaw))
		evidence.RequestRaw = req
		evidence.ResponseRaw = resp
		evidence.RequestTruncated = evidence.RequestTruncated || reqCut
		evidence.ResponseTruncated = evidence.ResponseTruncated || respCut
		evidence.Available = strings.TrimSpace(req) != ""
		if !evidence.Available {
			evidence.MissingReason = "归档中无请求原文"
		}
		out = append(out, evidence)
	}
	return out
}

func (s *Server) reportAssets(records []reportRecordRef) []reportdelivery.Asset {
	byTarget := map[string]*reportdelivery.Asset{}
	for _, rr := range records {
		target := strings.TrimSpace(rr.rec.Target)
		if target == "" {
			continue
		}
		asset := byTarget[target]
		if asset == nil {
			asset = &reportdelivery.Asset{Target: target}
			byTarget[target] = asset
		}
		if rr.dir == "" {
			continue
		}
		full, ok := loadScanRecordFromDir(rr.dir)
		if !ok || full == nil || len(full.Events) == 0 {
			continue
		}
		events := make([]pdfreport.Event, 0, len(full.Events))
		for _, e := range full.Events {
			events = append(events, pdfreport.Event{
				Type: e.Type, Content: e.Content, ToolName: e.ToolName,
				ToolArgs: e.ToolArgs, Output: e.Output, Error: e.Error,
			})
		}
		recon := pdfreport.CollectReconSummary(events)
		if asset.IP == "" && len(recon.IPAddresses) > 0 {
			asset.IP = recon.IPAddresses[0]
		}
		asset.Ports = dedupeNonEmpty(append(asset.Ports, recon.Ports...))
		asset.Tech = dedupeNonEmpty(append(asset.Tech, recon.Technologies...))
		asset.URLs = dedupeNonEmpty(append(asset.URLs, recon.URLs...))
	}
	targets := make([]string, 0, len(byTarget))
	for target := range byTarget {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	out := make([]reportdelivery.Asset, 0, len(targets))
	for _, target := range targets {
		a := *byTarget[target]
		if len(a.Ports) > 20 {
			a.Ports = a.Ports[:20]
		}
		if len(a.Tech) > 20 {
			a.Tech = a.Tech[:20]
		}
		if len(a.URLs) > 50 {
			a.URLs = a.URLs[:50]
		}
		out = append(out, a)
	}
	return out
}

func reportCoverage(records []reportRecordRef, findings []reportdelivery.Finding) reportdelivery.Coverage {
	cov := reportdelivery.Coverage{}
	if len(records) > 0 {
		newest := records[len(records)-1]
		for id, status := range newest.rec.PhaseStatus {
			phase, err := parsePhaseID(id)
			if err != nil {
				continue
			}
			cov.Phases = append(cov.Phases, reportdelivery.PhaseStatus{
				ID:     phase,
				Status: status,
				Reason: newest.rec.PhaseReasons[id],
			})
		}
		sort.Slice(cov.Phases, func(i, j int) bool { return cov.Phases[i].ID < cov.Phases[j].ID })
		for _, rr := range records {
			if strings.EqualFold(rr.rec.Status, "finished") {
				continue
			}
			reason := strings.TrimSpace(rr.rec.StopReason)
			if reason != "" {
				cov.Limitations = append(cov.Limitations, fmt.Sprintf("%s：%s（停止原因：%s）", rr.rec.Target, rr.rec.Status, reason))
			} else {
				cov.Limitations = append(cov.Limitations, fmt.Sprintf("%s：%s", rr.rec.Target, rr.rec.Status))
			}
		}
		cov.Limitations = dedupeNonEmpty(cov.Limitations)
	}
	endpoints := map[string]struct{}{}
	for _, f := range findings {
		if f.Endpoint != "" {
			endpoints[f.Method+" "+f.Endpoint] = struct{}{}
		}
	}
	cov.TestedEndpoints = len(endpoints)
	return cov
}

func orderFindingsByRisk(findings []reportdelivery.Finding, scores []reportdelivery.RiskScore) []reportdelivery.Finding {
	byID := make(map[string]reportdelivery.Finding, len(findings))
	for _, f := range findings {
		byID[f.ID] = f
	}
	out := make([]reportdelivery.Finding, 0, len(findings))
	seen := map[string]struct{}{}
	for _, s := range scores {
		if f, ok := byID[s.FindingID]; ok {
			out = append(out, f)
			seen[f.ID] = struct{}{}
		}
	}
	for _, f := range findings {
		if _, ok := seen[f.ID]; !ok {
			out = append(out, f)
		}
	}
	return out
}

type findingMeta struct {
	Timestamp        string `json:"timestamp"`
	AgentName        string `json:"agent_name"`
	OriginalSeverity string `json:"original_severity"`
}

func loadFindingMeta(dir string) map[string]findingMeta {
	out := map[string]findingMeta{}
	if dir == "" {
		return out
	}
	data, err := os.ReadFile(filepath.Join(dir, "findings.json"))
	if err != nil {
		return out
	}
	var checkpoint struct {
		Findings []struct {
			ID               string `json:"id"`
			Timestamp        string `json:"timestamp"`
			AgentName        string `json:"agent_name"`
			OriginalSeverity string `json:"original_severity"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return out
	}
	for _, f := range checkpoint.Findings {
		out[f.ID] = findingMeta{Timestamp: f.Timestamp, AgentName: f.AgentName, OriginalSeverity: f.OriginalSeverity}
	}
	return out
}

func reportTargetsFromCache(scanIDs []string, targetsByScanID map[string]string) []string {
	if len(scanIDs) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, id := range scanIDs {
		target := strings.TrimSpace(targetsByScanID[id])
		if target == "" {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		out = append(out, target)
	}
	sort.Strings(out)
	return out
}

func reportDir(dataDir, taskID, reportID string) string {
	return filepath.Join(dataDir, "reports", safeFileComponent(taskID), safeFileComponent(reportID))
}

func safeFileComponent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}

func truncateRaw(s string) (string, bool) {
	if len(s) <= reportMaxRawBytes {
		return s, false
	}
	cut := reportMaxRawBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

func reportTimeDisplay(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Local().Format("2006-01-02 15:04:05")
	}
	return raw
}

func parsePhaseID(id string) (int, error) {
	var phase int
	_, err := fmt.Sscanf(strings.TrimSpace(id), "%d", &phase)
	return phase, err
}

func runIDsOf(runs []platform.Run) []string {
	out := make([]string, 0, len(runs))
	for _, run := range runs {
		if run.ID != "" {
			out = append(out, run.ID)
		}
	}
	return out
}

func scanIDOrFallback(scanID string, index int) string {
	if strings.TrimSpace(scanID) != "" {
		return safeFileComponent(scanID)
	}
	return fmt.Sprintf("%d", index+1)
}

func firstString(values []string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func dedupeNonEmpty(values []string) []string {
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

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
