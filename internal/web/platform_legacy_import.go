package web

// R1 legacy importer (REDESIGN §8): mount existing scan records and schedules
// into the implicit default project so pre-platform history is visible through
// the project/task/run API. Strictly non-destructive — scan directories and
// schedule files are read in place, never copied or moved — and idempotent:
//   - scan.json records mount as runs keyed by runs.legacy_scan_id;
//   - schedules convert to tasks keyed by tasks.context.legacy_schedule_id.
//
// The scheduler keeps firing the original schedules; the imported task's
// schedule attribute is stored for the R1 model only (no new trigger engine
// in this milestone). Runs triggered by the legacy scheduler are NOT mounted
// automatically — that linkage arrives with the R2+ schedule integration.
//
// Import failure is always best-effort and logged, never fatal: scanning does
// not depend on the platform store (charter §6.6).

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

// importLegacyPlatformRecords is the startup entry point (called from
// Server.Start when a platform store is wired).
func (s *Server) importLegacyPlatformRecords() {
	store := s.currentPlatformStore()
	if store == nil {
		return
	}
	ctx := context.Background()

	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		log.Printf("[platform-import] ensure default project: %v (import skipped)", err)
		return
	}
	task, err := store.EnsureImportTask(ctx, project.ID)
	if err != nil {
		log.Printf("[platform-import] ensure import task: %v (import skipped)", err)
		return
	}

	scansImported, scansSkipped := s.importLegacyScansToPlatform(ctx, store, task.ID)
	schedulesImported := s.importLegacySchedulesToPlatform(ctx, store, project.ID)
	if scansImported > 0 || scansSkipped > 0 || schedulesImported > 0 {
		log.Printf("[platform-import] mounted %d legacy scan(s) (skipped %d) and %d schedule(s) into project %q",
			scansImported, scansSkipped, schedulesImported, project.ID)
	}
	// Cold-path findings are repaired after the mount inventory is complete.
	// Without a DSN this is a no-op (file mode projects on read).
	s.backfillRunFindingProjections(ctx)
}

// importLegacyScansToPlatform mounts every existing scan.json under the
// umbrella task. findAllScanSummaries provides the same record set as
// findAllScans without decoding event logs — only ID/InstanceID/ScanMode are
// read here. Records without an ID are skipped.
func (s *Server) importLegacyScansToPlatform(ctx context.Context, store platform.Repository, taskID string) (imported, skipped int) {
	existing, err := store.LegacyScanIDs(ctx)
	if err != nil {
		log.Printf("[platform-import] load imported scan ids: %v (scan import skipped)", err)
		return 0, 0
	}
	for _, entry := range s.findAllScanSummaries() {
		rec := entry.rec
		if strings.TrimSpace(rec.ID) == "" {
			continue
		}
		if _, done := existing[rec.ID]; done {
			continue
		}
		instanceID := strings.TrimSpace(rec.InstanceID)
		if instanceID == "" {
			// Legacy records predate dispatch instances; the scan record ID
			// is the only stable association key (fallback rule).
			instanceID = rec.ID
		}
		if _, created, err := store.ImportLegacyRun(ctx, taskID, platform.RunMount{
			LegacyScanID: rec.ID,
			InstanceID:   instanceID,
			Mode:         rec.ScanMode,
		}); err != nil {
			log.Printf("[platform-import] mount scan %s: %v", rec.ID, err)
			skipped++
			continue
		} else if created {
			imported++
		}
	}
	return imported, skipped
}

// importLegacySchedulesToPlatform converts each in-memory schedule (loaded
// from dataDir/_schedules/*.json) into a default-project task, idempotent by
// tasks.context.legacy_schedule_id. Legacy fields without a contract slot
// (run day, report branding, webhook, model) are parked in tasks.context as
// legacy hints per REDESIGN §3.2.
func (s *Server) importLegacySchedulesToPlatform(ctx context.Context, store platform.Repository, projectID string) int {
	s.schedulesMu.RLock()
	snapshot := make([]*ScanSchedule, 0, len(s.schedules))
	for _, sch := range s.schedules {
		snapshot = append(snapshot, sch)
	}
	s.schedulesMu.RUnlock()

	imported := 0
	for _, sch := range snapshot {
		if sch == nil {
			continue
		}
		in := legacyScheduleToTaskInput(sch)
		if _, created, err := store.ImportScheduleTask(ctx, projectID, in, sch.ID); err != nil {
			log.Printf("[platform-import] convert schedule %s (%s): %v", sch.ID, sch.Name, err)
			continue
		} else if created {
			imported++
		}
	}
	return imported
}

// legacyScheduleToTaskInput maps a ScanSchedule onto the task write model.
// The schedule's instruction fills the task instruction (the run dispatch
// falls back goal → instruction → nothing), and an empty interval defaults
// to "daily" so the contract enum stays valid.
func legacyScheduleToTaskInput(sch *ScanSchedule) platform.TaskInput {
	interval := strings.TrimSpace(sch.Interval)
	if interval == "" {
		interval = "daily"
	}
	name := strings.TrimSpace(sch.Name)
	if name == "" {
		name = "Imported schedule " + sch.ID
	}
	contextMap := map[string]any{
		"legacy_enabled":      sch.Enabled,
		"legacy_targets":      append([]string(nil), sch.Targets...),
		"legacy_run_day":      sch.RunDay,
		"legacy_company_name": sch.CompanyName,
		"legacy_logo_path":    sch.LogoPath,
		"legacy_webhook":      sch.DiscordWebhook != "",
		"legacy_model":        sch.Model,
	}
	if !sch.NextRun.IsZero() {
		contextMap["legacy_next_run"] = sch.NextRun.UTC().Format(time.RFC3339)
	}
	if !sch.LastRun.IsZero() {
		contextMap["legacy_last_run"] = sch.LastRun.UTC().Format(time.RFC3339)
	}
	goal := strings.TrimSpace(sch.TaskGoal)
	if goal == "" {
		goal = strings.TrimSpace(sch.Instruction)
	}
	return platform.TaskInput{
		Name:        name,
		Goal:        goal,
		Instruction: sch.Instruction,
		PhasePolicy: &platform.PhasePolicy{
			Phases:         append([]int(nil), sch.Phases...),
			SeverityFilter: append([]string(nil), sch.SeverityFilter...),
			ScanMode:       sch.ScanMode,
			Intensity:      sch.ScanIntensity,
		},
		Schedule: &platform.TaskSchedule{
			Interval: interval,
			Time:     sch.RunAt,
			Timezone: sch.Timezone,
		},
		Context: contextMap,
	}
}
