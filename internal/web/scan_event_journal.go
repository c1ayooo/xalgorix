package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/runstate"
)

const scanEventJournalFile = "events.jsonl"

// ── R6.1 state-authority seam (REDESIGN D12, charter v3.0 §5/§6) ─────────
//
// The backend is process-global infrastructure registered once at startup
// (web.SetEventStateBackend in cmd/xalgorix, the same pattern as
// scanctx.SetLedgerBackend); nil keeps the legacy events.jsonl file path
// byte-for-byte — production always registers, so the file path effectively
// serves test harnesses and the pre-R6 import fallback reads.
//
// Read resolution (charter §6): this scan's own rows first (scan_id), the
// run-wide union across sibling sessions for instance-form requests (D11),
// and the legacy file as the last-resort fallback for directories whose rows
// the R6.0 importer never landed. With a backend registered the journal file
// is never written again: the durable rows are the state authority.
//
// Failure discipline (charter §5.2/§6.4): an append failure is returned to
// the mutation caller (persist-failure is not success) and recorded on the
// run_state_watermarks events stream by the implementation; the in-memory
// record tail stays authoritative for the live session, and the next
// successful append continues the journal (idempotent on (scan_id, seq)).

// EventStateBackend is the durable home of the conversation/telemetry event
// journal — run_events in the PG primary store, one session keyed by
// (scan_id, seq) with the run (instance_id) stamped per row.
type EventStateBackend interface {
	// AppendEvent persists one journal event (idempotent on (scan_id, seq)).
	AppendEvent(scanID, instanceID string, seq uint64, typ string, occurredAt time.Time, payload []byte) error
	// PutEvents lands a batch in one transaction — the legacy-journal import
	// path (the R6.0 importer's row semantics, seq = durable line order).
	PutEvents(ctx context.Context, scanID, instanceID string, rows []runstate.EventRow) error
	// LoadEventsByScan reads one session's journal window plus its total.
	LoadEventsByScan(ctx context.Context, scanID string, offset, limit int) ([]runstate.EventRecord, int64, error)
	// LoadEventsByInstance reads the run-wide sibling-union window plus total.
	LoadEventsByInstance(ctx context.Context, instanceID string, offset, limit int) ([]runstate.EventRecord, int64, error)
	// DeleteEventsByScan drops one session's rows (reset-state re-run).
	DeleteEventsByScan(ctx context.Context, scanID string) error
}

var (
	eventStateBackendMu sync.RWMutex
	eventStateBackend   EventStateBackend
)

// SetEventStateBackend registers the process-wide event journal backend.
// Pass nil to restore the legacy events.jsonl file behavior (tests only).
func SetEventStateBackend(b EventStateBackend) {
	eventStateBackendMu.Lock()
	defer eventStateBackendMu.Unlock()
	eventStateBackend = b
}

func currentEventStateBackend() EventStateBackend {
	eventStateBackendMu.RLock()
	defer eventStateBackendMu.RUnlock()
	return eventStateBackend
}

// eventStateCtx bounds one backend round trip off the request paths.
func eventStateCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// eventStream is one session's write identity: the row key (scan id = the
// directory basename), the D11 stamp (instance id) and the next journal
// position. The in-process allocator is the seq authority for the live
// session — a journal is single-writer per scan (D12 forbids multi-process
// writes to one run), and prepare re-seeds the position from the durable
// count after a restart. The mutex serializes the session append path and
// the (rare) out-of-band conversation appends sharing the same scan.
type eventStream struct {
	scanID     string
	instanceID string
	next       int64 // 1-based; 0 = resolve lazily from the store on first append
	mu         sync.Mutex
}

var (
	eventStreamsMu sync.Mutex
	eventStreams   = map[string]*eventStream{} // cleaned scan dir → write identity
)

func eventStreamFor(dir string) *eventStream {
	key := filepath.Clean(dir)
	eventStreamsMu.Lock()
	defer eventStreamsMu.Unlock()
	st, ok := eventStreams[key]
	if !ok {
		st = &eventStream{scanID: filepath.Base(key)}
		eventStreams[key] = st
	}
	return st
}

// primeEventStream records the session's write identity at journal prepare
// time — the only moment both the instance stamp and the durable position are
// known without extra round trips.
func primeEventStream(dir, instanceID string, durableCount int64) {
	st := eventStreamFor(dir)
	st.mu.Lock()
	st.instanceID = instanceID
	st.next = durableCount + 1
	st.mu.Unlock()
}

// resolveInstance fills an un-primed stream's instance stamp: the event's own
// stamp (out-of-band conversation appends carry it), else the scan record
// read once (durable row first — R6.1 —, legacy file fallback) — the same
// field the importer and every read path derive the D11 grouping from.
func resolveInstance(st *eventStream, dir string, event WSEvent) string {
	if st.instanceID != "" {
		return st.instanceID
	}
	if id := strings.TrimSpace(event.InstanceID); id != "" {
		st.instanceID = id
		return id
	}
	if rec, ok := scanRecordMetaForDir(filepath.Clean(dir)); ok && rec.InstanceID != "" {
		st.instanceID = rec.InstanceID
	}
	return st.instanceID
}

// A partial final line is an interrupted append, never a completed event.
// The returned boundary permits recovery without discarding valid history.
func walkEventJournal(dir string, onEvent func(int, WSEvent)) (int, int64, error) {
	if b := currentEventStateBackend(); b != nil {
		return walkEventJournalState(b, dir, onEvent)
	}
	return walkEventJournalFile(dir, onEvent)
}

// walkEventJournalFile is the pre-R6.1 file walk, kept byte-identical: it
// serves the nil-backend (test) mode and the legacy-directory fallback reads.
func walkEventJournalFile(dir string, onEvent func(int, WSEvent)) (int, int64, error) {
	f, err := os.Open(filepath.Join(dir, scanEventJournalFile))
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	total := 0
	var boundary int64
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			return total, boundary, nil
		}
		if err != nil {
			return total, boundary, err
		}
		var event WSEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return total, boundary, fmt.Errorf("invalid event journal entry %d: %w", total, err)
		}
		if onEvent != nil {
			onEvent(total, event)
		}
		total++
		boundary += int64(len(line))
	}
}

// walkEventJournalState reads the session's durable journal. One row whose
// payload no longer decodes is skipped (the same damaged-row discipline as
// the journal/audit reads) — a corrupted row must not blank the session's
// history. A session with no durable rows yet falls back to its legacy file
// (a pre-import directory), and a clean miss reads as an empty journal so
// callers keep producing correct empty projections.
func walkEventJournalState(b EventStateBackend, dir string, onEvent func(int, WSEvent)) (int, int64, error) {
	ctx, cancel := eventStateCtx()
	defer cancel()
	recs, total, err := b.LoadEventsByScan(ctx, filepath.Base(filepath.Clean(dir)), 0, 0)
	if err != nil {
		log.Printf("[journal] durable event read for %s failed, trying legacy file: %v", filepath.Base(dir), err)
		return walkEventJournalFile(dir, onEvent)
	}
	if total == 0 {
		if n, boundary, ferr := walkEventJournalFile(dir, onEvent); ferr == nil || n > 0 {
			return n, boundary, ferr
		}
		return 0, 0, nil
	}
	decoded := 0
	for _, r := range recs {
		var event WSEvent
		if json.Unmarshal(r.Payload, &event) != nil {
			continue // damaged row: skip it, keep the rest of the history
		}
		if onEvent != nil {
			onEvent(decoded, event)
		}
		decoded++
	}
	return decoded, 0, nil
}

func appendEventTail(events []WSEvent, event WSEvent) []WSEvent {
	if len(events) < detailEventTail {
		return append(events, event)
	}
	copy(events, events[len(events)-detailEventTail+1:])
	events = events[:detailEventTail]
	events[detailEventTail-1] = event
	return events
}

// Migrate legacy embedded history once using bounded memory. scan.json keeps
// its old data until the journal is durable and the new metadata is saved.
func prepareScanEventJournal(dir string, rec *ScanRecord, reset bool) error {
	if b := currentEventStateBackend(); b != nil {
		return prepareScanEventJournalState(b, dir, rec, reset)
	}
	tail := make([]WSEvent, 0, detailEventTail)
	if rec.EventsJournal && !reset {
		total, boundary, err := walkEventJournalFile(dir, func(_ int, event WSEvent) { tail = appendEventTail(tail, event) })
		if err != nil {
			return err
		}
		if err := os.Truncate(filepath.Join(dir, scanEventJournalFile), boundary); err != nil {
			return err
		}
		rec.Events, rec.EventsTotal = tail, total
		rec.EventsTruncated = total > len(tail)
		return nil
	}
	f, err := os.CreateTemp(dir, ".events-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	defer f.Close()
	encoder := json.NewEncoder(f)
	total := 0
	var writeErr error
	writeEvent := func(event WSEvent) {
		if writeErr != nil {
			return
		}
		total++
		if event.EventID == "" {
			event.EventID = fmt.Sprintf("%s:%d", rec.ID, total)
		}
		writeErr = encoder.Encode(event)
		tail = appendEventTail(tail, event)
	}
	legacy := filepath.Join(dir, "scan.json")
	if _, err := os.Stat(legacy); err == nil && !reset {
		_, _, err = walkScanJSON(legacy, func(_ int, raw json.RawMessage) {
			if writeErr != nil {
				return
			}
			var event WSEvent
			if writeErr = json.Unmarshal(raw, &event); writeErr == nil {
				writeEvent(event)
			}
		})
		if err != nil {
			return err
		}
	} else {
		for _, event := range rec.Events {
			writeEvent(event)
		}
	}
	if writeErr != nil {
		return writeErr
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, scanEventJournalFile)); err != nil {
		return err
	}
	rec.EventsJournal = true
	rec.Events, rec.EventsTotal = tail, total
	rec.EventsTruncated = total > len(tail)
	return nil
}

// prepareScanEventJournalState is the durable-mode prepare: seed the record's
// event tail and total from run_events, delete this session's rows on a
// reset-state re-run, and import a pre-import directory's legacy journal once
// (the R6.0 importer's row semantics: seq = the 1-based durable line order,
// instance stamped from the record). The journal file is never written.
func prepareScanEventJournalState(b EventStateBackend, dir string, rec *ScanRecord, reset bool) error {
	scanID := filepath.Base(filepath.Clean(dir))
	instance := rec.InstanceID
	if reset {
		ctx, cancel := eventStateCtx()
		defer cancel()
		if err := b.DeleteEventsByScan(ctx, scanID); err != nil {
			return fmt.Errorf("reset event journal %s: %w", scanID, err)
		}
		primeEventStream(dir, instance, 0)
		rec.EventsJournal = true
		rec.Events = []WSEvent{}
		rec.EventsTotal = 0
		rec.EventsTruncated = false
		return nil
	}

	ctx, cancel := eventStateCtx()
	defer cancel()
	_, total, err := b.LoadEventsByScan(ctx, scanID, 0, 1)
	if err != nil {
		return fmt.Errorf("read event journal %s: %w", scanID, err)
	}
	if total == 0 {
		// A pre-R6 session resuming under PG: land its legacy journal first so
		// the session's writes continue the same durable stream instead of
		// colliding with a later import retry.
		imported, tail, ierr := importLegacyEventJournal(b, dir, rec)
		if ierr != nil {
			return ierr
		}
		primeEventStream(dir, instance, int64(imported))
		rec.EventsJournal = true
		rec.Events, rec.EventsTotal = tail, imported
		rec.EventsTruncated = imported > len(tail)
		return nil
	}
	seed, stotal, err := b.LoadEventsByScan(ctx, scanID, int(max(total-int64(detailEventTail), 0)), detailEventTail)
	if err != nil {
		return fmt.Errorf("read event journal tail %s: %w", scanID, err)
	}
	primeEventStream(dir, instance, stotal)
	rec.EventsJournal = true
	rec.Events = decodeEventRecords(seed)
	if rec.Events == nil {
		rec.Events = []WSEvent{}
	}
	rec.EventsTotal = int(stotal)
	rec.EventsTruncated = int(stotal) > len(rec.Events)
	return nil
}

// importLegacyEventJournal persists a session directory's pre-PG history:
// the events.jsonl journal when one exists (raw lines, importer seq
// semantics, damaged lines skipped), else the record's embedded event log.
// Returns the durable row count and the bounded tail for the live record.
func importLegacyEventJournal(b EventStateBackend, dir string, rec *ScanRecord) (int, []WSEvent, error) {
	instance := rec.InstanceID
	rows := []runstate.EventRow(nil)
	tail := make([]WSEvent, 0, detailEventTail)
	appendRow := func(seq int, payload []byte, typ string, at time.Time) {
		//nolint:gosec // seq is the 1-based import line order, bounded by the file's line count
		rows = append(rows, runstate.EventRow{Seq: uint64(seq), Type: typ, OccurredAt: at, Payload: payload})
		var event WSEvent
		if json.Unmarshal(payload, &event) == nil {
			tail = appendEventTail(tail, event)
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, scanEventJournalFile)); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var probe struct {
				Type      string `json:"type"`
				Timestamp string `json:"timestamp"`
			}
			if json.Unmarshal([]byte(line), &probe) != nil {
				continue // damaged line (incl. a torn final append): skip and compact
			}
			appendRow(len(rows)+1, []byte(line), probe.Type, journalTime(probe.Timestamp))
		}
	} else if len(rec.Events) > 0 {
		for _, event := range rec.Events {
			payload, merr := json.Marshal(event)
			if merr != nil {
				continue
			}
			appendRow(len(rows)+1, payload, event.Type, journalTime(event.Timestamp))
		}
	} else {
		return 0, []WSEvent{}, nil
	}
	ctx, cancel := eventStateCtx()
	defer cancel()
	if err := b.PutEvents(ctx, filepath.Base(filepath.Clean(dir)), instance, rows); err != nil {
		return 0, nil, fmt.Errorf("import legacy event journal %s: %w", filepath.Base(dir), err)
	}
	return len(rows), tail, nil
}

func appendScanEventJournal(dir string, event WSEvent) error {
	if b := currentEventStateBackend(); b != nil {
		return appendEventJournalState(b, dir, event)
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, scanEventJournalFile), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// appendEventJournalState persists one event into the durable journal. The
// stream's allocator supplies the seq — the durable journal position, seeded
// from the store at prepare and advanced under the per-scan mutex; the
// event's EventID stays a caller label and never keys storage (out-of-band
// conversation appends interleave with the session's numbered stream exactly
// as they always interleave the file). The error returns to the caller: the
// session's append path treats a failed persist as a checkpoint failure, the
// conversation path logs it.
func appendEventJournalState(b EventStateBackend, dir string, event WSEvent) error {
	st := eventStreamFor(dir)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.next <= 0 {
		ctx, cancel := eventStateCtx()
		_, total, err := b.LoadEventsByScan(ctx, st.scanID, 0, 1)
		cancel()
		if err != nil {
			return fmt.Errorf("resolve event journal position %s: %w", st.scanID, err)
		}
		st.instanceID = resolveInstance(st, dir, event)
		st.next = total + 1
	} else {
		st.instanceID = resolveInstance(st, dir, event)
	}
	seq := st.next
	st.next++
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	//nolint:gosec // seq is the journal's 1-based line position (the durable count + allocator), bounded far below 2^63
	return b.AppendEvent(st.scanID, st.instanceID, uint64(seq), event.Type, journalTime(event.Timestamp), payload)
}

// journalTime parses a journal timestamp; unparseable stamps persist as now
// (the importer's convention — one event's time is never worth failing a
// persist over).
func journalTime(timestamp string) time.Time {
	if t, err := time.Parse(time.RFC3339, timestamp); err == nil {
		return t
	}
	return time.Now().UTC()
}

// decodeEventRecords turns durable rows back into events, skipping rows
// whose payload no longer decodes (damaged-row discipline).
func decodeEventRecords(recs []runstate.EventRecord) []WSEvent {
	events := make([]WSEvent, 0, len(recs))
	for _, r := range recs {
		if event, ok := decodeEventRecord(r); ok {
			events = append(events, event)
		}
	}
	return events
}

// decodeEventRecord decodes one durable row; ok=false marks a row whose
// payload no longer decodes (external corruption).
func decodeEventRecord(r runstate.EventRecord) (WSEvent, bool) {
	var event WSEvent
	if json.Unmarshal(r.Payload, &event) != nil {
		return WSEvent{}, false
	}
	return event, true
}

// instanceIDForDirs returns the run identity shared by sibling session
// directories, derived from each directory's scan record (durable row first —
// R6.1 —, legacy file fallback; the same field the importer stamped on the
// durable rows). Memoized: an instance binding is immutable for a directory's
// lifetime.
func instanceIDForDirs(dirs []string) string {
	instanceDirsMu.Lock()
	defer instanceDirsMu.Unlock()
	for _, dir := range dirs {
		key := filepath.Clean(dir)
		if id, ok := instanceDirs[key]; ok {
			if id != "" {
				return id
			}
			continue
		}
		id := ""
		if rec, ok := scanRecordMetaForDir(key); ok {
			id = strings.TrimSpace(rec.InstanceID)
		}
		instanceDirs[key] = id
		if id != "" {
			return id
		}
	}
	return ""
}

var (
	instanceDirsMu sync.Mutex
	instanceDirs   = map[string]string{} // cleaned scan dir → run (instance) id
)
