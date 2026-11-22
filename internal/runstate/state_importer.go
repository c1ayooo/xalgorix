// state_importer.go — R6.0 one-shot importer: existing scan directories'
// state files (scan.json / events.jsonl / domain_journal.jsonl /
// ledger.json / intelligence|notes|coverage.json / .dispatches) are parsed
// into the PG primary store so pre-R6 runs stay visible on the panels after
// the read paths switch (R6.2).
//
// Idempotent and guarded: a scan is imported once (run_state_watermarks row
// kind='dir_import', status='applied'); re-running the import skips it.
// Delete the watermark row to re-arm. The directories themselves are never
// modified — they remain the byte-artifact home (tool outputs, traffic
// packets, sandbox workspace) and the comparison baseline for the importer's
// round-trip test.
//
// Idempotency keys mirror the live R6.1 writer's conventions:
//   - run_events.seq = the 1-based line index in events.jsonl (the journal
//     line order IS the durable order; WSEvent.Seq is a live-frame-only
//     field and is unset in persisted records), and
//   - run_journal.seq = the domain journal record's own per-scan seq.

package runstate

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
)

// ScanStateDetail (the runs-table takeover payload for one scan.json: typed
// hot fields + the verbatim record as TEXT) lives in scan_record_backend.go
// since R6.1, alongside the AttachScanState writer it feeds. The importer
// fills ScanID (the scan directory basename — the run_scan_records primary
// key, the same key the events/journal rows use) and ScanDir (the
// dataDir-relative workspace anchor) so the durable rows carry the identity
// the file layer still needs for byte artifacts.

// ImportStats summarizes one data-dir import pass.
type ImportStats struct {
	Scans       int
	Events      int64
	JournalRows int64
	Ledgers     int
	StateFiles  int
	Dispatches  int
	Skipped     int
	Failed      int
}

// ImportDataDir walks dataDir and imports every not-yet-imported scan's
// state into PG. Per-scan failures are recorded on the watermark (failed)
// and do not abort the pass; the returned error is only for fatal
// conditions (store unreachable).
func (s *Store) ImportDataDir(ctx context.Context, dataDir string) (ImportStats, error) {
	var stats ImportStats
	scanDirs, err := scanDirs(dataDir)
	if err != nil {
		return stats, fmt.Errorf("runstate: walk data dir: %w", err)
	}
	for _, dir := range scanDirs {
		scanID := filepath.Base(dir)
		imported, err := s.Imported(ctx, scanID)
		if err != nil {
			return stats, err
		}
		if imported {
			stats.Skipped++
			continue
		}
		if err := s.importScan(ctx, dir, dataDir, &stats); err != nil {
			stats.Failed++
			werr := s.Watermark(ctx, scanID, WatermarkKindDirImport, "", 0, StatusFailed, err.Error())
			if werr != nil {
				return stats, werr
			}
			continue
		}
		stats.Scans++
		if err := s.Watermark(ctx, scanID, WatermarkKindDirImport, "", 0, StatusApplied, ""); err != nil {
			return stats, err
		}
	}
	stats.Dispatches, err = s.importDispatches(ctx, dataDir)
	if err != nil {
		return stats, err
	}
	return stats, nil
}

// scanDirs collects the top-level scan directories: every directory directly
// or transitively holding a scan.json, skipping dot/underscore entries
// (.dispatches, _platform) and non-scan trees.
func scanDirs(dataDir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are not import errors
		}
		if d.IsDir() {
			name := d.Name()
			if path != dataDir && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != "scan.json" {
			return nil
		}
		out = append(out, filepath.Dir(path))
		return filepath.SkipAll // one scan per directory tree: stop descending
	})
	return out, err
}

// importScan ingests one scan directory's state files.
func (s *Store) importScan(ctx context.Context, dir, dataDir string, stats *ImportStats) error {
	scanID := filepath.Base(dir)

	detail, err := parseScanState(dir)
	if err != nil {
		return err
	}
	// The workspace anchor (R6.1): stored dataDir-relative so the durable row
	// survives data-dir relocation; the byte artifacts stay on disk. A
	// relative-path failure falls back to the basename convention.
	if rel, rerr := filepath.Rel(dataDir, dir); rerr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		detail.ScanDir = rel
	}
	if err := s.AttachScanState(ctx, detail); err != nil {
		return err
	}

	events, err := parseEvents(dir)
	if err != nil {
		return err
	}
	if err := s.PutEvents(ctx, scanID, detail.InstanceID, events); err != nil {
		return err
	}
	stats.Events += int64(len(events))

	rows, err := parseJournal(dir)
	if err != nil {
		return err
	}
	if err := s.PutJournalRecords(ctx, scanID, detail.InstanceID, rows); err != nil {
		return err
	}
	stats.JournalRows += int64(len(rows))

	if ledger, seq, ok, err := parseLedger(dir); err != nil {
		return err
	} else if ok {
		if err := s.PutLedgerSnapshot(ctx, scanID, detail.InstanceID, seq, ledger); err != nil {
			return err
		}
		stats.Ledgers++
	}

	for _, kind := range []string{KindIntelligence, KindNotes, KindCoverage} {
		data, ok, err := readStateFile(dir, kind+".json")
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := s.PutStateFile(ctx, scanID, kind, detail.InstanceID, 0, data); err != nil {
			return err
		}
		stats.StateFiles++
	}
	return nil
}

// parseScanState reads scan.json into the takeover payload.
func parseScanState(dir string) (ScanStateDetail, error) {
	var detail ScanStateDetail
	raw, err := os.ReadFile(filepath.Join(dir, "scan.json"))
	if err != nil {
		return detail, fmt.Errorf("runstate: read scan.json: %w", err)
	}
	var rec struct {
		ID                 string `json:"id"`
		InstanceID         string `json:"instance_id"`
		Status             string `json:"status"`
		Target             string `json:"target"`
		ParentTarget       string `json:"parent_target"`
		StartedAt          string `json:"started_at"`
		FinishedAt         string `json:"finished_at"`
		CurrentPhase       int    `json:"current_phase"`
		PlanTasksTotal     int    `json:"plan_tasks_total"`
		PlanTasksCompleted int    `json:"plan_tasks_completed"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return detail, fmt.Errorf("runstate: decode scan.json: %w", err)
	}
	detail = ScanStateDetail{
		ScanID:        filepath.Base(filepath.Clean(dir)),
		InstanceID:    rec.InstanceID,
		LegacyScanID:  rec.ID,
		Status:        rec.Status,
		Target:        rec.Target,
		ParentTarget:  rec.ParentTarget,
		CurrentPhase:  rec.CurrentPhase,
		PlanTotal:     rec.PlanTasksTotal,
		PlanCompleted: rec.PlanTasksCompleted,
		Raw:           raw,
	}
	if t, err := parseTime(rec.StartedAt); err == nil {
		detail.StartedAt = t
	}
	if t, err := parseTime(rec.FinishedAt); err == nil {
		detail.FinishedAt = t
	}
	return detail, nil
}

// eventLine is the narrow decode of one events.jsonl line.
type eventLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
}

// parseEvents reads events.jsonl; seq = 1-based line index (durable order).
func parseEvents(dir string) ([]EventRow, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("runstate: read events.jsonl: %w", err)
	}
	var out []EventRow
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev eventLine
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			// One damaged line must not blank the run's history (the same
			// discipline as the merged events window): skip and continue.
			continue
		}
		row := EventRow{Seq: uint64(len(out) + 1), Type: ev.Type, Payload: []byte(line)}
		if t, err := parseTime(ev.Timestamp); err == nil {
			row.OccurredAt = *t
		} else {
			row.OccurredAt = time.Now().UTC()
		}
		out = append(out, row)
	}
	return out, nil
}

// parseJournal reads domain_journal.jsonl via the engine's own reader
// (trailing-line repair included); seq = the record's per-scan seq.
func parseJournal(dir string) ([]JournalRow, error) {
	records, _, err := domainjournal.ReadRecords(filepath.Join(dir, domainjournal.FileName))
	if err != nil || len(records) == 0 {
		return nil, err // absent file → ReadRecords returns (nil, false, nil)
	}
	out := make([]JournalRow, 0, len(records))
	for i := range records {
		payload, err := json.Marshal(records[i])
		if err != nil {
			return nil, fmt.Errorf("runstate: encode journal record %s/%d: %w", filepath.Base(dir), records[i].Seq, err)
		}
		out = append(out, JournalRow{Seq: records[i].Seq, OccurredAt: records[i].At, Payload: payload})
	}
	return out, nil
}

// parseLedger reads ledger.json raw (the snapshot is stored verbatim; the
// R6.1 read path decodes it into the engine's ledger store). Returns the
// snapshot's own mutation seq for watermark comparisons.
func parseLedger(dir string) ([]byte, int64, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "ledger.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, false, nil
		}
		return nil, 0, false, fmt.Errorf("runstate: read ledger.json: %w", err)
	}
	var snap struct {
		Seq int64 `json:"seq"`
	}
	_ = json.Unmarshal(raw, &snap) // seq is advisory; absence = 0
	return raw, snap.Seq, true, nil
}

// readStateFile reads one bounded state document if present.
func readStateFile(dir, name string) ([]byte, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("runstate: read %s: %w", name, err)
	}
	return raw, true, nil
}

// importDispatches imports dataDir/.dispatches/*.json tombstones. A single
// unreadable/unwritable dispatch file is skipped (the pass continues); only
// store-level failures abort.
func (s *Store) importDispatches(ctx context.Context, dataDir string) (int, error) {
	entries, err := os.ReadDir(filepath.Join(dataDir, ".dispatches"))
	if err != nil {
		return 0, nil // no dispatch dir = nothing to import
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		instanceID := strings.TrimSuffix(entry.Name(), ".json")
		raw, err := os.ReadFile(filepath.Join(dataDir, ".dispatches", entry.Name()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "[runstate] dispatch %s skipped: %v\n", entry.Name(), err)
			continue
		}
		if err := s.PutDispatchSnapshot(ctx, instanceID, raw); err != nil {
			fmt.Fprintf(os.Stderr, "[runstate] dispatch %s skipped: %v\n", entry.Name(), err)
			continue
		}
		count++
	}
	return count, nil
}

// parseTime parses RFC3339 timestamps; nil on empty/unparsable input.
func parseTime(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, os.ErrInvalid
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
