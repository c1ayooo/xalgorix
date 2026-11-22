// Package runstate — the PostgreSQL primary store for run state and panel
// data (REDESIGN D12, charter v3.0, R6).
//
// From R6.1 the engine persists state here at every mutation boundary
// (in-memory authority + synchronous PG transaction); the per-session scan
// directories stop being a durable unit. The package is PG-only by design —
// there is no file backend to fall back to (charter §6.6 v3.0), and it must
// not import the web layer (the dependency arrow points the other way in
// R6.1), so scan.json / WSEvent shapes are mirrored here as narrow decode
// structs rather than reused.
//
// Identity discipline (charter §6.3 v3.0): idempotency keys stay
// (scan_id, seq) / (scan_id, kind); rows carry instance_id stamped at the
// write boundary. PG write failures never block a running scan — callers
// record the failure on run_state_watermarks and the reconciler catches up.
package runstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
)

// State file kinds stored in run_state_files. Engine-owned constants; the
// importer and the R6.1 persistence backend must agree on them.
const (
	KindIntelligence = "intelligence"
	KindNotes        = "notes"
	KindCoverage     = "coverage"
	KindQueue        = "queue"
	KindTokens       = "tokens"
)

// Watermark status values — the M3 four-state protocol (charter §6.4).
const (
	StatusPending = "pending"
	StatusApplied = "applied"
	StatusGap     = "gap"
	StatusFailed  = "failed"
)

// Watermark stream kinds (migration 0012: kind ∈ {events, journal, ledger,
// state:<kind>, findings, ...}). State-file streams nest under the
// "state:" prefix via StateWatermarkKind, so every state family owns exactly
// one (scan, kind) watermark row.
const (
	WatermarkKindLedger    = "ledger"
	WatermarkKindEvents    = "events"
	WatermarkKindJournal   = "journal"
	WatermarkKindDirImport = "dir_import"
)

// StateWatermarkKind names the watermark stream of one state-file kind
// ("notes" → "state:notes").
func StateWatermarkKind(kind string) string { return "state:" + kind }

// Store is the PG-only run-state store. It assumes the shared migrator has
// applied the schema (platform.Open / coldpath.New both migrate on start);
// Open only validates connectivity.
type Store struct {
	pool *pgxpool.Pool
}

// Open validates the DSN and returns a store on a clamped pool.
func Open(ctx context.Context, dsn string) (*Store, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("runstate: empty database URL")
	}
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("runstate: parse database URL: %w", err)
	}
	if poolConfig.MaxConns < 2 || poolConfig.MaxConns > 8 {
		poolConfig.MaxConns = 4
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("runstate: create PostgreSQL pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("runstate: PostgreSQL unavailable: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// EventRow is one conversation/telemetry journal event. Payload is the raw
// WSEvent JSON; Type is the projected discriminator the panels filter on.
type EventRow struct {
	Seq        uint64
	Type       string
	OccurredAt time.Time
	Payload    []byte
}

// PutEvents upserts a batch of events for one scan. Idempotent on
// (scan_id, seq).
func (s *Store) PutEvents(ctx context.Context, scanID, instanceID string, rows []EventRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("runstate: begin events tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only after commit path
	for _, row := range rows {
		if _, err := tx.Exec(ctx,
			`INSERT INTO run_events (scan_id, instance_id, seq, type, occurred_at, payload)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (scan_id, seq) DO UPDATE
			   SET instance_id = EXCLUDED.instance_id,
			       type = EXCLUDED.type,
			       payload = EXCLUDED.payload`,
			scanID, instanceID, row.Seq, row.Type, row.OccurredAt, string(row.Payload),
		); err != nil {
			return fmt.Errorf("runstate: put event %s/%d: %w", scanID, row.Seq, err)
		}
	}
	return tx.Commit(ctx)
}

// JournalRow is one domain journal record: raw domainjournal.Record JSON plus
// its occurrence time (decoded by the caller from the record's At field).
type JournalRow struct {
	Seq        uint64
	OccurredAt time.Time
	Payload    []byte
}

// PutJournalRecords upserts domain journal rows. Idempotent on (scan_id, seq).
func (s *Store) PutJournalRecords(ctx context.Context, scanID, instanceID string, rows []JournalRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("runstate: begin journal tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only after commit path
	for _, row := range rows {
		if _, err := tx.Exec(ctx,
			`INSERT INTO run_journal (scan_id, instance_id, seq, occurred_at, record)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (scan_id, seq) DO UPDATE
			   SET instance_id = EXCLUDED.instance_id,
			       record = EXCLUDED.record`,
			scanID, instanceID, row.Seq, row.OccurredAt, string(row.Payload),
		); err != nil {
			return fmt.Errorf("runstate: put journal %s/%d: %w", scanID, row.Seq, err)
		}
	}
	return tx.Commit(ctx)
}

// PutLedgerSnapshot rewrites the ledger state authority for one scan
// (whole-snapshot semantics; the ledger is bounded to dozens of hypotheses).
func (s *Store) PutLedgerSnapshot(ctx context.Context, scanID, instanceID string, seq int64, data []byte) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO run_ledger_snapshots (scan_id, instance_id, seq, data, updated_at)
		 VALUES ($1, $2, $3, $4, now())
		 ON CONFLICT (scan_id) DO UPDATE
		   SET instance_id = EXCLUDED.instance_id,
		       seq = EXCLUDED.seq,
		       data = EXCLUDED.data,
		       updated_at = now()`,
		scanID, instanceID, seq, string(data),
	); err != nil {
		return fmt.Errorf("runstate: put ledger snapshot %s: %w", scanID, err)
	}
	return nil
}

// PutStateFile upserts one bounded state document (intelligence/notes/
// coverage/queue/tokens) for a scan.
func (s *Store) PutStateFile(ctx context.Context, scanID, kind, instanceID string, seq int64, data []byte) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO run_state_files (scan_id, kind, instance_id, seq, data, updated_at)
		 VALUES ($1, $2, $3, $4, $5, now())
		 ON CONFLICT (scan_id, kind) DO UPDATE
		   SET instance_id = EXCLUDED.instance_id,
		       seq = EXCLUDED.seq,
		       data = EXCLUDED.data,
		       updated_at = now()`,
		scanID, kind, instanceID, seq, string(data),
	); err != nil {
		return fmt.Errorf("runstate: put state file %s/%s: %w", scanID, kind, err)
	}
	return nil
}

// PutDispatchSnapshot upserts one dispatch tombstone (D11 re-dispatch state).
func (s *Store) PutDispatchSnapshot(ctx context.Context, instanceID string, data []byte) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO dispatch_snapshots (instance_id, data, updated_at)
		 VALUES ($1, $2, now())
		 ON CONFLICT (instance_id) DO UPDATE
		   SET data = EXCLUDED.data, updated_at = now()`,
		instanceID, string(data),
	); err != nil {
		return fmt.Errorf("runstate: put dispatch snapshot %s: %w", instanceID, err)
	}
	return nil
}

// AttachScanState lives in scan_record_backend.go since R6.1: one transaction
// writes the per-record run_scan_records row AND the runs mount row's
// takeover columns (top-level records only), so the directory importer and
// the live record safe point share one durable home per scan record.

// Watermark upserts the four-state protocol row for one (scan, kind).
func (s *Store) Watermark(ctx context.Context, scanID, kind, instanceID string, appliedSeq int64, status, errText string) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO run_state_watermarks (scan_id, kind, instance_id, applied_seq, status, error, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, now())
		 ON CONFLICT (scan_id, kind) DO UPDATE
		   SET instance_id = EXCLUDED.instance_id,
		       applied_seq = EXCLUDED.applied_seq,
		       status = EXCLUDED.status,
		       error = EXCLUDED.error,
		       updated_at = now()`,
		scanID, kind, instanceID, appliedSeq, status, errText,
	); err != nil {
		return fmt.Errorf("runstate: watermark %s/%s: %w", scanID, kind, err)
	}
	return nil
}

// ── scanctx.LedgerBackend implementation (R6.1) ──────────────────────────
// Synchronous single-row persistence for the ledger state authority; the
// failure path records a failed watermark so the operator audit view sees
// every unpersisted mutation window (charter §6.4). Satisfies
// scanctx.LedgerBackend implicitly (no import: raw snapshot bytes only).

func (s *Store) backendCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// SaveLedgerSnapshot rewrites the durable snapshot for one scan (whole
// snapshot semantics; the ledger is bounded to dozens of hypotheses).
func (s *Store) SaveLedgerSnapshot(scanID, instanceID string, seq int64, data []byte) error {
	ctx, cancel := s.backendCtx()
	defer cancel()
	err := s.PutLedgerSnapshot(ctx, scanID, instanceID, seq, data)
	if err != nil {
		if wmErr := s.Watermark(context.Background(), scanID, WatermarkKindLedger, instanceID, seq, StatusFailed, err.Error()); wmErr != nil {
			return fmt.Errorf("%v (watermark also failed: %v)", err, wmErr)
		}
		return err
	}
	return nil
}

// LoadLedgerSnapshot returns the scan's own durable snapshot.
func (s *Store) LoadLedgerSnapshot(scanID string) ([]byte, int64, bool, error) {
	ctx, cancel := s.backendCtx()
	defer cancel()
	var data []byte
	var seq int64
	err := s.pool.QueryRow(ctx,
		`SELECT data, seq FROM run_ledger_snapshots WHERE scan_id = $1`,
		scanID,
	).Scan(&data, &seq)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, 0, false, nil
		}
		return nil, 0, false, fmt.Errorf("runstate: load ledger snapshot %s: %w", scanID, err)
	}
	return data, seq, true, nil
}

// LoadLatestLedgerSnapshotForInstance returns the newest sibling session's
// snapshot — the D11 continuity source for a re-dispatched session.
func (s *Store) LoadLatestLedgerSnapshotForInstance(instanceID, excludeScanID string) ([]byte, int64, string, bool, error) {
	if strings.TrimSpace(instanceID) == "" {
		return nil, 0, "", false, nil
	}
	ctx, cancel := s.backendCtx()
	defer cancel()
	var data []byte
	var seq int64
	var source string
	err := s.pool.QueryRow(ctx,
		`SELECT data, seq, scan_id FROM run_ledger_snapshots
		 WHERE instance_id = $1 AND scan_id <> $2
		 ORDER BY updated_at DESC LIMIT 1`,
		instanceID, excludeScanID,
	).Scan(&data, &seq, &source)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, 0, "", false, nil
		}
		return nil, 0, "", false, fmt.Errorf("runstate: load continuity snapshot %s: %w", instanceID, err)
	}
	return data, seq, source, true, nil
}

// ── scanctx.StateFilesBackend / intelligence.Backend implementation ──────
// Synchronous single-row persistence for the bounded state documents
// (notes/coverage/intelligence); failure records a failed watermark
// (charter §6.4). Satisfies both interfaces implicitly, raw bytes only.

func (s *Store) SaveStateFileSnapshot(scanID, kind, instanceID string, seq int64, data []byte) error {
	ctx, cancel := s.backendCtx()
	defer cancel()
	err := s.PutStateFile(ctx, scanID, kind, instanceID, seq, data)
	if err != nil {
		if wmErr := s.Watermark(context.Background(), scanID, "state:"+kind, instanceID, seq, StatusFailed, err.Error()); wmErr != nil {
			return fmt.Errorf("%v (watermark also failed: %v)", err, wmErr)
		}
		return err
	}
	return nil
}

func (s *Store) LoadStateFileSnapshot(scanID, kind string) ([]byte, int64, bool, error) {
	ctx, cancel := s.backendCtx()
	defer cancel()
	var data []byte
	var seq int64
	err := s.pool.QueryRow(ctx,
		`SELECT data, seq FROM run_state_files WHERE scan_id = $1 AND kind = $2`,
		scanID, kind,
	).Scan(&data, &seq)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, 0, false, nil
		}
		return nil, 0, false, fmt.Errorf("runstate: load state file %s/%s: %w", scanID, kind, err)
	}
	return data, seq, true, nil
}

func (s *Store) LoadLatestStateFileSnapshotForInstance(instanceID, excludeScanID, kind string) ([]byte, int64, string, bool, error) {
	if strings.TrimSpace(instanceID) == "" {
		return nil, 0, "", false, nil
	}
	ctx, cancel := s.backendCtx()
	defer cancel()
	var data []byte
	var seq int64
	var source string
	err := s.pool.QueryRow(ctx,
		`SELECT data, seq, scan_id FROM run_state_files
		 WHERE instance_id = $1 AND scan_id <> $2 AND kind = $3
		 ORDER BY updated_at DESC LIMIT 1`,
		instanceID, excludeScanID, kind,
	).Scan(&data, &seq, &source)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, 0, "", false, nil
		}
		return nil, 0, "", false, fmt.Errorf("runstate: load continuity state file %s/%s: %w", instanceID, kind, err)
	}
	return data, seq, source, true, nil
}

// (Intelligence-family adapters live in intel_backend.go.)

// Imported reports whether the one-shot directory import already applied for
// a scan (R6.0 guard: startup re-imports skip applied scans; deleting the
// watermark row re-arms the import).
func (s *Store) Imported(ctx context.Context, scanID string) (bool, error) {
	var status string
	err := s.pool.QueryRow(ctx,
		`SELECT status FROM run_state_watermarks WHERE scan_id = $1 AND kind = $2`,
		scanID, WatermarkKindDirImport,
	).Scan(&status)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return false, nil
		}
		return false, fmt.Errorf("runstate: check import watermark %s: %w", scanID, err)
	}
	return status == StatusApplied, nil
}

// ── R6.1/R6.2 shared read/append base ────────────────────────────────────
// The one store surface the state-authority switch (R6.1 writers) and the
// panel read switch (R6.2 handlers) both consume. Read resolution order per
// the charter: this scan's own rows first, then the same instance's sibling
// rows (D11 continuity), with the legacy file fallback decided by the caller
// — never by this package.

// EventRecord is one event as read back: the write-shaped EventRow plus the
// owning session's scan id, so sibling aggregations can attribute rows
// (exploration per-target filtering, honest load logs).
type EventRecord struct {
	ScanID string
	EventRow
}

// clampWindow normalizes offset/limit paging arguments with the semantics of
// the file-era windows (loadScanEventsWindow / loadRunEventsWindow): a
// negative offset clamps to 0; limit <= 0 means unbounded (all rows from
// offset on). Any upper bound on the page size is the caller's policy.
func clampWindow(offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 0
	}
	return offset, limit
}

// limitArg maps the clampWindow sentinel to SQL: 0 = unbounded becomes LIMIT
// NULL (PostgreSQL treats a NULL limit as no limit), keeping one query shape.
func limitArg(limit int) any {
	if limit > 0 {
		return limit
	}
	return nil
}

// LoadEventsByScan reads one session's event journal in durable order (the
// events.jsonl line order, seq ascending), windowed to [offset, offset+limit).
// total is the session's full event count, independent of the window — the
// paging contract of the events endpoints.
func (s *Store) LoadEventsByScan(ctx context.Context, scanID string, offset, limit int) ([]EventRecord, int64, error) {
	var total int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM run_events WHERE scan_id = $1`, scanID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("runstate: count events %s: %w", scanID, err)
	}
	off, lim := clampWindow(offset, limit)
	rows, err := s.pool.Query(ctx,
		`SELECT scan_id, seq, type, occurred_at, payload
		 FROM run_events
		 WHERE scan_id = $1
		 ORDER BY seq
		 OFFSET $2 LIMIT $3`,
		scanID, off, limitArg(lim),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("runstate: read events %s: %w", scanID, err)
	}
	out, err := collectEvents(rows)
	if err != nil {
		return nil, 0, fmt.Errorf("runstate: read events %s: %w", scanID, err)
	}
	return out, total, nil
}

// LoadEventsByInstance aggregates every sibling session's events of one run
// (instance_id) — the D11 union behind the transcript, the events window and
// the resume replay. Ordering reproduces the file-era concatenation: sessions
// in chronological order (each session's earliest event time), durable
// journal order (seq) within a session. offset/limit index into that union;
// total counts the whole union.
func (s *Store) LoadEventsByInstance(ctx context.Context, instanceID string, offset, limit int) ([]EventRecord, int64, error) {
	var total int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM run_events WHERE instance_id = $1`, instanceID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("runstate: count events %s: %w", instanceID, err)
	}
	off, lim := clampWindow(offset, limit)
	rows, err := s.pool.Query(ctx,
		`SELECT scan_id, seq, type, occurred_at, payload FROM (
		     SELECT scan_id, seq, type, occurred_at, payload,
		            min(occurred_at) OVER (PARTITION BY scan_id) AS session_start
		     FROM run_events
		     WHERE instance_id = $1
		 ) unioned
		 ORDER BY session_start, scan_id, seq
		 OFFSET $2 LIMIT $3`,
		instanceID, off, limitArg(lim),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("runstate: read events %s: %w", instanceID, err)
	}
	out, err := collectEvents(rows)
	if err != nil {
		return nil, 0, fmt.Errorf("runstate: read events %s: %w", instanceID, err)
	}
	return out, total, nil
}

func collectEvents(rows pgx.Rows) ([]EventRecord, error) {
	defer rows.Close()
	out := []EventRecord(nil)
	for rows.Next() {
		var rec EventRecord
		var payload []byte
		if err := rows.Scan(&rec.ScanID, &rec.Seq, &rec.Type, &rec.OccurredAt, &payload); err != nil {
			return nil, fmt.Errorf("runstate: scan event row: %w", err)
		}
		rec.Payload = payload
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("runstate: iterate event rows: %w", err)
	}
	return out, nil
}

// LoadJournalRecordsByScan reads one session's full domain journal decoded
// into the engine's records, in per-scan seq order. A row that no longer
// decodes (external corruption) is skipped and reported through the damaged
// flag — one damaged row must never blank the audit history (the same
// discipline as the file journal's damaged-tail contract, surfaced the same
// way the audit/exploration views already render it).
func (s *Store) LoadJournalRecordsByScan(ctx context.Context, scanID string) ([]domainjournal.Record, bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT record FROM run_journal WHERE scan_id = $1 ORDER BY seq`, scanID,
	)
	if err != nil {
		return nil, false, fmt.Errorf("runstate: read journal %s: %w", scanID, err)
	}
	return collectJournalRecords(rows)
}

// LoadJournalRecordsByInstance reads the whole run's journal across its
// sibling sessions (D11 union), ordered exactly like LoadEventsByInstance:
// sessions chronologically (earliest record time), per-scan seq within a
// session. The returned records carry their own ScanID, so per-target or
// per-session filtering stays possible without another query.
func (s *Store) LoadJournalRecordsByInstance(ctx context.Context, instanceID string) ([]domainjournal.Record, bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT record FROM (
		     SELECT record, scan_id, seq,
		            min(occurred_at) OVER (PARTITION BY scan_id) AS session_start
		     FROM run_journal
		     WHERE instance_id = $1
		 ) unioned
		 ORDER BY session_start, scan_id, seq`,
		instanceID,
	)
	if err != nil {
		return nil, false, fmt.Errorf("runstate: read journal %s: %w", instanceID, err)
	}
	return collectJournalRecords(rows)
}

func collectJournalRecords(rows pgx.Rows) ([]domainjournal.Record, bool, error) {
	defer rows.Close()
	damaged := false
	out := []domainjournal.Record(nil)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, damaged, fmt.Errorf("runstate: scan journal row: %w", err)
		}
		rec, err := decodeJournalRecord(payload)
		if err != nil {
			damaged = true // skip the damaged row, keep the rest of the history
			continue
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, damaged, fmt.Errorf("runstate: iterate journal rows: %w", err)
	}
	return out, damaged, nil
}

// decodeJournalRecord decodes one run_journal payload (the verbatim
// domainjournal.Record JSON the writer stored).
func decodeJournalRecord(payload []byte) (domainjournal.Record, error) {
	var rec domainjournal.Record
	if err := json.Unmarshal(payload, &rec); err != nil {
		return domainjournal.Record{}, err
	}
	return rec, nil
}

// AppendJournalRecord persists ONE domain journal record synchronously — the
// R6.1 persistence hook for domainjournal.Journal.Append. The in-memory
// journal stays the run-time seq authority; PG becomes the durable home.
// Idempotent on (scan_id, seq): a retried append after a failed write reuses
// the sequence (a failed Journal.Append leaves its sequence reusable). The
// failure path records a failed watermark (kind=journal, attempted seq) and
// returns the error to the mutation caller — persist-failure is not success
// (charter §5.2). Success does not touch the watermark (the ledger backend
// precedent: the row itself is the durable proof).
func (s *Store) AppendJournalRecord(scanID, instanceID string, rec domainjournal.Record) error {
	if strings.TrimSpace(scanID) == "" {
		return errors.New("runstate: journal append requires a scan id")
	}
	if rec.Seq == 0 {
		return fmt.Errorf("runstate: journal append %s: seq must be positive", scanID)
	}
	if rec.ScanID == "" {
		rec.ScanID = scanID
	} else if rec.ScanID != scanID {
		return fmt.Errorf("runstate: journal append: record scan_id %q does not match %q", rec.ScanID, scanID)
	}
	if rec.At.IsZero() {
		rec.At = time.Now().UTC()
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("runstate: encode journal record %s/%d: %w", scanID, rec.Seq, err)
	}
	ctx, cancel := s.backendCtx()
	defer cancel()
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO run_journal (scan_id, instance_id, seq, occurred_at, record)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (scan_id, seq) DO UPDATE
		   SET instance_id = EXCLUDED.instance_id,
		       occurred_at = EXCLUDED.occurred_at,
		       record = EXCLUDED.record`,
		scanID, instanceID, rec.Seq, rec.At, string(payload),
	); err != nil {
		err = fmt.Errorf("runstate: append journal %s/%d: %w", scanID, rec.Seq, err)
		if wmErr := s.RecordWatermarkFailure(context.Background(), scanID, WatermarkKindJournal, instanceID, int64(rec.Seq), err); wmErr != nil {
			return fmt.Errorf("%v (watermark also failed: %v)", err, wmErr)
		}
		return err
	}
	return nil
}

// LoadJournalSources enumerates every scan's durable journal position (max
// durable seq per scan) — the cold-path reconciler's PG-side source
// discovery (charter §6.4): from R6.1 production journals live in
// run_journal and have no file under the data root, so crash catch-up must
// enumerate the durable stream itself. Implements the domainjournal.Backend
// extension the reconciler consumes.
func (s *Store) LoadJournalSources(ctx context.Context) ([]domainjournal.JournalSource, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT scan_id, max(seq) FROM run_journal GROUP BY scan_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("runstate: journal sources: %w", err)
	}
	defer rows.Close()
	out := []domainjournal.JournalSource(nil)
	for rows.Next() {
		var src domainjournal.JournalSource
		var last int64
		if err := rows.Scan(&src.ScanID, &last); err != nil {
			return nil, fmt.Errorf("runstate: scan journal source: %w", err)
		}
		src.LastSeq = uint64(last)
		out = append(out, src)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("runstate: iterate journal sources: %w", err)
	}
	return out, nil
}

// LoadStateFile reads one bounded state document (intelligence/notes/
// coverage/queue/tokens) for a scan. Absent → (nil, false, nil); a present
// row returns the verbatim bytes (TEXT, byte fidelity — migration 0013).
func (s *Store) LoadStateFile(ctx context.Context, scanID, kind string) ([]byte, bool, error) {
	var data []byte
	err := s.pool.QueryRow(ctx,
		`SELECT data FROM run_state_files WHERE scan_id = $1 AND kind = $2`,
		scanID, kind,
	).Scan(&data)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("runstate: load state file %s/%s: %w", scanID, kind, err)
	}
	return data, true, nil
}

// LoadLatestStateFileForInstance returns the newest sibling session's state
// document of one kind — the D11 continuity source for a re-dispatched
// session, resolving in the charter's read order (own scan first via
// LoadStateFile, then the latest same-instance sibling, then the caller's
// legacy-file fallback). source is the owning scan id of the returned
// document (for honest restore logs).
func (s *Store) LoadLatestStateFileForInstance(ctx context.Context, instanceID, excludeScanID, kind string) ([]byte, string, bool, error) {
	if strings.TrimSpace(instanceID) == "" {
		return nil, "", false, nil
	}
	var data []byte
	var source string
	err := s.pool.QueryRow(ctx,
		`SELECT data, scan_id FROM run_state_files
		 WHERE instance_id = $1 AND scan_id <> $2 AND kind = $3
		 ORDER BY updated_at DESC LIMIT 1`,
		instanceID, excludeScanID, kind,
	).Scan(&data, &source)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", false, nil
		}
		return nil, "", false, fmt.Errorf("runstate: load continuity state file %s/%s: %w", instanceID, kind, err)
	}
	return data, source, true, nil
}

// AdvanceWatermark marks the (scan, kind) stream durably applied through
// appliedSeq (the M3 four-state row). Monotonic: a late or stale success can
// never pull the applied position back. The writer contract is that every
// seq below the advanced position is durably written — the advance clears a
// previously recorded failure for the same window.
func (s *Store) AdvanceWatermark(ctx context.Context, scanID, kind, instanceID string, appliedSeq int64) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO run_state_watermarks (scan_id, kind, instance_id, applied_seq, status, error, updated_at)
		 VALUES ($1, $2, $3, $4, $5, '', now())
		 ON CONFLICT (scan_id, kind) DO UPDATE
		   SET instance_id = EXCLUDED.instance_id,
		       applied_seq = GREATEST(run_state_watermarks.applied_seq, EXCLUDED.applied_seq),
		       status = $5,
		       error = '',
		       updated_at = now()`,
		scanID, kind, instanceID, appliedSeq, StatusApplied,
	); err != nil {
		return fmt.Errorf("runstate: advance watermark %s/%s: %w", scanID, kind, err)
	}
	return nil
}

// RecordWatermarkFailure records a failed persistence attempt for the
// (scan, kind) stream: status=failed so the operator audit view sees the
// unpersisted window (charter §6.4). attemptedSeq is recorded on the row for
// visibility — the ledger backend precedent — and is superseded by the next
// advance through it. A nil cause records an empty error text.
func (s *Store) RecordWatermarkFailure(ctx context.Context, scanID, kind, instanceID string, attemptedSeq int64, cause error) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	return s.Watermark(ctx, scanID, kind, instanceID, attemptedSeq, StatusFailed, msg)
}
