package web

// Findings cold-path projection for task-level views (REDESIGN §3.1).
//
// scan.json remains the authority. With a DSN, already-accepted findings are
// copied into run_findings and task views query the replica; without one, the
// same projection is computed on read over every run page. This file never
// accepts, verifies, relabels or reorders findings.

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/platform"
)

// projectedRunFindings converts one saved ScanRecord's findings into the
// platform projection shape. Callers build this before handing work to a
// goroutine so cleanup never races with further record mutation.
func projectedRunFindings(rec *ScanRecord) []platform.RunFinding {
	if rec == nil {
		return nil
	}
	started := parseScanStartedAt(rec.StartedAt)
	byKey := make(map[string]platform.RunFinding, len(rec.Vulns))
	order := make([]string, 0, len(rec.Vulns))
	for _, v := range rec.Vulns {
		key := dedupFindingKey(rec.Target, v)
		if key == "" {
			continue
		}
		payload, err := json.Marshal(flatFinding{
			VulnSummary:   v,
			ScanID:        rec.ID,
			ScanTarget:    rec.Target,
			ScanStartedAt: rec.StartedAt,
		})
		if err != nil {
			continue
		}
		if _, exists := byKey[key]; !exists {
			order = append(order, key)
		}
		byKey[key] = platform.RunFinding{
			DedupKey:      key,
			ScanStartedAt: started,
			Finding:       payload,
		}
	}
	out := make([]platform.RunFinding, 0, len(order))
	for _, key := range order {
		out = append(out, byKey[key])
	}
	return out
}

func parseScanStartedAt(raw string) time.Time {
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// deletePlatformRunsForScan removes the mount(s) tied to a purged scan. The
// run_findings rows cascade. Best-effort: a failed cold-path cleanup logs and
// is retried naturally when the operator deletes the project.
func (s *Server) deletePlatformRunsForScan(instanceID, scanID string) {
	store := s.currentPlatformStore()
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	removed, err := store.DeleteRunsByScanIdentity(ctx, instanceID, scanID)
	if err != nil {
		log.Printf("[platform-findings] delete runs for scan %s: %v", scanID, err)
		return
	}
	if removed > 0 {
		log.Printf("[platform] removed %d run mount(s) for purged scan %s", removed, scanID)
	}
}

// queueRunFindingProjection writes the projection for one completed root
// session. The write is detached from cleanup and bounded: cold-path failure
// only logs and is repaired by the startup backfill.
func (s *Server) queueRunFindingProjection(instanceID string, rec *ScanRecord) {
	store := s.currentPlatformStore()
	fs, ok := store.(platform.FindingsStore)
	if !ok || strings.TrimSpace(instanceID) == "" {
		return
	}
	findings := projectedRunFindings(rec)
	started := parseScanStartedAt(rec.StartedAt)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		runs, err := store.RunsByInstanceID(ctx, instanceID)
		if err != nil {
			log.Printf("[platform-findings] resolve run for instance %s: %v", instanceID, err)
			return
		}
		for _, run := range runs {
			if err := fs.ReplaceRunFindings(ctx, run.ID, started, findings); err != nil {
				log.Printf("[platform-findings] project run %s: %v", run.ID, err)
			}
		}
	}()
}

// backfillRunFindingProjections repairs the "scan.json written, projection
// write missed" window and seeds projections for scans imported at startup.
// It is idempotent (replace-by-run) and never blocks startup.
func (s *Server) backfillRunFindingProjections(ctx context.Context) {
	store := s.currentPlatformStore()
	fs, ok := store.(platform.FindingsStore)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	runs, err := store.AllRuns(ctx)
	if err != nil {
		log.Printf("[platform-findings] backfill enumerate runs: %v", err)
		return
	}
	idx := s.scanSummaryIndexFor(s.findAllScanSummaries())
	projected := 0
	for _, run := range runs {
		rec := idx.lookup(run.InstanceID, run.LegacyScanID)
		if rec == nil {
			continue
		}
		started := parseScanStartedAt(rec.StartedAt)
		if started.IsZero() {
			started = run.CreatedAt
		}
		if err := fs.ReplaceRunFindings(ctx, run.ID, started, projectedRunFindings(rec)); err != nil {
			log.Printf("[platform-findings] backfill run %s: %v", run.ID, err)
			continue
		}
		projected++
	}
	if projected > 0 {
		log.Printf("[platform-findings] projected findings for %d run(s)", projected)
	}
}

// taskFindingsFromProjection reads the PG replica (one newest row per key).
func (s *Server) taskFindingsFromProjection(ctx context.Context, fs platform.FindingsStore, taskID string) ([]flatFinding, error) {
	rows, err := fs.ListTaskFindings(ctx, taskID)
	if err != nil {
		return nil, err
	}
	out := make([]flatFinding, 0, len(rows))
	for _, row := range rows {
		var finding flatFinding
		if err := json.Unmarshal(row.Finding, &finding); err != nil {
			log.Printf("[platform-findings] decode projected finding %q: %v", row.DedupKey, err)
			continue
		}
		out = append(out, finding)
	}
	sortTaskFindings(out)
	return out, nil
}

// taskFindingsFromScans is the no-DSN degradation: page through every run and
// apply the same deterministic newest-wins dedup, without a fixed run cap.
func (s *Server) taskFindingsFromScans(ctx context.Context, store platform.Repository, taskID string) ([]flatFinding, error) {
	const pageSize = 500
	deduped := make(map[string]flatFinding)
	idx := s.scanSummaryIndexFor(s.findAllScanSummaries())
	for page := 1; ; page++ {
		runs, total, err := store.ListRuns(ctx, taskID, page, pageSize)
		if err != nil {
			return nil, err
		}
		if len(runs) == 0 {
			break
		}
		for _, run := range runs {
			rec := idx.lookup(run.InstanceID, run.LegacyScanID)
			if rec == nil {
				continue
			}
			for _, v := range rec.Vulns {
				key := dedupFindingKey(rec.Target, v)
				candidate := flatFinding{
					VulnSummary:   v,
					ScanID:        rec.ID,
					ScanTarget:    rec.Target,
					ScanStartedAt: rec.StartedAt,
				}
				if existing, ok := deduped[key]; ok && existing.ScanStartedAt >= candidate.ScanStartedAt {
					continue
				}
				deduped[key] = candidate
			}
		}
		if page*pageSize >= total {
			break
		}
	}
	out := make([]flatFinding, 0, len(deduped))
	for _, finding := range deduped {
		out = append(out, finding)
	}
	sortTaskFindings(out)
	return out, nil
}

func sortTaskFindings(findings []flatFinding) {
	sort.Slice(findings, func(i, j int) bool {
		ri, rj := severityRankValue(findings[i].Severity), severityRankValue(findings[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return findings[i].ScanStartedAt > findings[j].ScanStartedAt
	})
}
