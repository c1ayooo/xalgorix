// Package web — streaming loaders for the scan-detail read path.
//
// A finished scan's scan.json embeds the entire event log (every LLM message
// and raw tool stdout), which for a high-token scan can be hundreds of MB. The
// old GET /api/scans/{id} path json.Unmarshalled that whole file into
// ScanRecord (decoding millions of WSEvent struct fields) and then re-marshaled
// it — so opening one big scan could hang the handler for minutes.
//
// These loaders walk scan.json ONCE with a streaming decoder, decode only the
// small metadata fields, count the events, and materialize just the slice of
// events the caller needs (a tail for the detail view, or an offset/limit
// window for lazy paging). Event elements outside the requested slice are never
// decoded into structs and never held in memory.
package web

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// detailEventTail is how many of the most recent events GET /api/scans/{id}
// returns inline. Enough to render a useful activity tail immediately; older
// events are paged on demand via GET /api/scans/{id}/events.
const detailEventTail = 300

// maxEventsWindow bounds a single lazy events page so a caller can't ask the
// server to materialize an unbounded slice.
const maxEventsWindow = 1000

// walkScanJSON streams the scan.json object at path. Non-"events" top-level
// fields are captured (undecoded) into meta; every element of the "events"
// array is passed to onEvent by index. It returns the total event count. The
// whole file is read but only metadata + whatever onEvent retains stays live.
func walkScanJSON(path string, onEvent func(idx int, raw json.RawMessage)) (map[string]json.RawMessage, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	dec := json.NewDecoder(bufio.NewReaderSize(f, 1<<20))
	meta := make(map[string]json.RawMessage, 32)
	total := 0

	// Opening '{'.
	if _, err := dec.Token(); err != nil {
		return nil, 0, err
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, 0, err
		}
		key, _ := keyTok.(string)
		if key == "events" {
			// The value is either an array or null.
			t, err := dec.Token()
			if err != nil {
				return nil, 0, err
			}
			if d, ok := t.(json.Delim); ok && d == '[' {
				if onEvent == nil {
					// Summary mode: skip the events array by token depth without
					// materializing any element. Critically this avoids copying a
					// multi-hundred-MB events blob into memory (the old
					// ReadFile + scanRecordLite path did exactly that on every
					// cache-cold walk, thrashing GC near GOMEMLIMIT). Element
					// strings are tokenized transiently and GC'd one at a time.
					depth := 1
					for depth > 0 {
						tok, err := dec.Token()
						if err != nil {
							return nil, 0, err
						}
						if delim, ok := tok.(json.Delim); ok {
							switch delim {
							case '[', '{':
								depth++
							case ']', '}':
								depth--
							}
						}
					}
				} else {
					for dec.More() {
						var raw json.RawMessage
						if err := dec.Decode(&raw); err != nil {
							return nil, 0, err
						}
						onEvent(total, raw)
						total++
					}
					// Closing ']'.
					if _, err := dec.Token(); err != nil {
						return nil, 0, err
					}
				}
			}
			// else: null (or unexpected) → no events.
			continue
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, 0, err
		}
		meta[key] = raw
	}
	return meta, total, nil
}

// recordFromMeta rebuilds a ScanRecord from the captured non-event fields.
func recordFromMeta(meta map[string]json.RawMessage) (*ScanRecord, error) {
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	var rec ScanRecord
	if err := json.Unmarshal(metaBytes, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func decodeEvents(raws []json.RawMessage) []WSEvent {
	events := make([]WSEvent, 0, len(raws))
	for _, r := range raws {
		var e WSEvent
		if json.Unmarshal(r, &e) == nil {
			events = append(events, e)
		}
	}
	return events
}

// scanMetaFile is a small sidecar next to scan.json holding exactly what the
// detail view needs: the scan metadata plus only the last detailEventTail
// events and the true event count. Reading it is O(tail) instead of streaming
// the whole (possibly hundreds-of-MB) scan.json, which is what makes opening a
// large scan instant — the same shape the SaaS gets for free from Postgres.
// scan.json remains the full source of truth (reports, full-event consumers).
const scanMetaFile = "scan.meta.json"

// buildDetailMeta returns a light copy of rec carrying only the tail of events
// plus the total/truncation hints — the exact payload the detail view needs.
func buildDetailMeta(rec *ScanRecord) *ScanRecord {
	light := *rec
	total := max(rec.EventsTotal, len(rec.Events))
	// Copy the tail into a non-nil slice: append on a nil source would leave
	// Events nil for a zero-event scan, marshaling as JSON null both here and
	// in the persisted scan.meta.json sidecar.
	light.Events = make([]WSEvent, 0, min(len(rec.Events), detailEventTail))
	if len(rec.Events) > detailEventTail {
		light.Events = append(light.Events, rec.Events[len(rec.Events)-detailEventTail:]...)
	} else {
		light.Events = append(light.Events, rec.Events...)
	}
	light.EventsTotal = total
	light.EventsTruncated = total > len(light.Events)
	return &light
}

// writeScanDetailMeta persists the detail sidecar for a full record. Best-effort:
// a failure just means the next detail read falls back to streaming scan.json.
func writeScanDetailMeta(dir string, rec *ScanRecord) {
	if dir == "" || rec == nil {
		return
	}
	writeScanDetailMetaLight(dir, buildDetailMeta(rec))
}

// writeScanDetailMetaLight writes an already-trimmed light record atomically.
func writeScanDetailMetaLight(dir string, light *ScanRecord) {
	if dir == "" || light == nil {
		return
	}
	data, err := json.Marshal(light)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".scanmeta-*.tmp")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return
	}
	// Atomic replace so a concurrent reader never sees a half-written sidecar.
	if err := os.Rename(tmpPath, filepath.Join(dir, scanMetaFile)); err != nil {
		_ = os.Remove(tmpPath)
	}
}

// readScanDetailMeta loads the detail sidecar if present and current. It is
// considered stale (and ignored) when scan.json is newer, so a scan.json
// written by a path that didn't refresh the sidecar can't serve stale data.
func readScanDetailMeta(dir string) (*ScanRecord, bool) {
	if dir == "" {
		return nil, false
	}
	metaPath := filepath.Join(dir, scanMetaFile)
	metaInfo, err := os.Stat(metaPath)
	if err != nil {
		return nil, false
	}
	if srcInfo, err := os.Stat(filepath.Join(dir, "scan.json")); err == nil {
		if srcInfo.ModTime().After(metaInfo.ModTime()) {
			return nil, false // sidecar predates the latest scan.json → rebuild
		}
	}
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, false
	}
	var rec ScanRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, false
	}
	return &rec, true
}

// readScanSummary parses a scan.json into a ScanRecord WITHOUT its event log,
// streaming past the events array so a huge log is never read into memory. This
// is the events-free parse behind the summary cache; it replaces the old
// os.ReadFile + Unmarshal(scanRecordLite) path that copied the entire events
// blob into a RawMessage only to discard it.
func readScanSummary(path string) (*ScanRecord, bool) {
	meta, _, err := walkScanJSON(path, nil)
	if err != nil {
		return nil, false
	}
	rec, err := recordFromMeta(meta)
	if err != nil {
		return nil, false
	}
	rec.Events = nil
	return rec, true
}

// loadScanRecordForDetail loads a scan record for the detail response with only
// the last `tail` events inline. It sets EventsTotal and, when older events
// were dropped, EventsTruncated. Falls back to a full load on any streaming
// error so callers still get a correct (if slower) response for odd records.
//
// R6.1 (REDESIGN D12): with the scan state backend registered the record
// comes from the durable row and its event tail from run_events (the sidecar
// and scan.json serve only pre-import directories). nil backend keeps the
// file path byte-identical.
func (s *Server) loadScanRecordForDetail(dir string, tail int) (*ScanRecord, bool) {
	if dir == "" {
		return nil, false
	}
	if tail < 0 {
		tail = 0
	}

	if b := currentScanStateBackend(); b != nil {
		if rec, ok := s.loadScanDetailFromState(b, dir, tail); ok {
			return rec, true
		}
		// Fall through: pre-import directories keep their legacy sidecar/file.
	}

	// Fast path: a current sidecar already holds metadata + the event tail, so
	// serve it without touching the (possibly huge) scan.json at all.
	if meta, ok := readScanDetailMeta(dir); ok {
		return meta, true
	}

	path := filepath.Join(dir, "scan.json")

	// Ring buffer keeping only the last `tail` raw events.
	ring := make([]json.RawMessage, 0, tail)
	meta, total, err := walkScanJSON(path, func(_ int, raw json.RawMessage) {
		if tail == 0 {
			return
		}
		if len(ring) < tail {
			ring = append(ring, raw)
			return
		}
		copy(ring, ring[1:])
		ring[tail-1] = raw
	})
	if err != nil {
		// Fallback: legacy/edge records that don't stream cleanly.
		return loadScanRecordFromDir(dir)
	}
	rec, err := recordFromMeta(meta)
	if err != nil {
		return loadScanRecordFromDir(dir)
	}
	rec.Events = decodeEvents(ring)
	if rec.EventsJournal {
		// Non-nil seed: a zero-event journal must marshal as [] not null.
		rec.Events = []WSEvent{}
		var journalErr error
		total, _, journalErr = walkEventJournal(dir, func(_ int, event WSEvent) {
			if tail > 0 {
				if len(rec.Events) >= tail {
					copy(rec.Events, rec.Events[1:])
					rec.Events = rec.Events[:tail-1]
				}
				rec.Events = append(rec.Events, event)
			}
		})
		if journalErr != nil {
			return nil, false
		}
	}
	rec.EventsTotal = total
	rec.EventsTruncated = total > len(rec.Events)

	// Lazily persist the sidecar so the next open of this (immutable, terminal)
	// scan is instant. Running scans get a fresh sidecar from saveScanRecordTo
	// on every save, so only backfill terminal ones here.
	if isTerminalDetailStatus(rec.Status) {
		writeScanDetailMetaLight(dir, rec)
	}
	return rec, true
}

// loadScanDetailFromState is the R6.1 durable branch of loadScanRecordForDetail:
// the record metadata from the run_scan_records row, the event tail from the
// durable journal (run_events, via the events backend — the same registered
// store in production). The shapes mirror the file path: journal-backed
// records reseed Events from the journal; legacy records keep the event log
// embedded in the verbatim detail.
func (s *Server) loadScanDetailFromState(b ScanStateBackend, dir string, tail int) (*ScanRecord, bool) {
	scanID := scanStateIDForDir(dir)
	ctx, cancel := scanStateCtx()
	defer cancel()
	row, ok, err := b.LoadScanRecord(ctx, scanID)
	if err != nil {
		log.Printf("[journal] durable record read for %s failed, trying legacy file: %v", filepath.Base(dir), err)
		return nil, false
	}
	if !ok {
		return nil, false
	}
	rec, ok := decodeScanRecordFull(row.Detail)
	if !ok {
		return nil, false // damaged row: the legacy file fallback decides
	}
	if rec.ID == "" {
		rec.ID = row.ScanID
	}
	if !rec.EventsJournal {
		// Legacy record: the whole event log rides in the verbatim detail —
		// apply the same tail window the file walker's ring produced.
		total := len(rec.Events)
		if total > tail {
			rec.Events = append([]WSEvent(nil), rec.Events[total-tail:]...)
		}
		rec.EventsTotal = total
		rec.EventsTruncated = total > len(rec.Events)
		return rec, true
	}
	// Journal-backed: the durable rows are the event source. A read failure
	// or an unregistered events backend falls back to the legacy file path.
	events := currentEventStateBackend()
	if events == nil {
		return nil, false
	}
	_, total, err := events.LoadEventsByScan(ctx, scanID, 0, 1)
	if err != nil {
		log.Printf("[journal] durable event count for %s failed, trying legacy file: %v", scanID, err)
		return nil, false
	}
	offset := total - int64(tail)
	if offset < 0 {
		offset = 0
	}
	seed, _, err := events.LoadEventsByScan(ctx, scanID, int(offset), tail)
	if err != nil {
		log.Printf("[journal] durable event tail for %s failed, trying legacy file: %v", scanID, err)
		return nil, false
	}
	rec.Events = decodeEventRecords(seed)
	if rec.Events == nil {
		rec.Events = []WSEvent{}
	}
	rec.EventsTotal = int(total)
	rec.EventsTruncated = int(total) > len(rec.Events)
	return rec, true
}

// making its detail sidecar safe to cache indefinitely.
func isTerminalDetailStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "finished", "completed", "stopped", "failed":
		return true
	default:
		return false
	}
}

// scanEventsWindowFromState is the R6.2 events-panel window when the durable
// event store is registered (REDESIGN D12 — the panels read PG): the run's
// instance union first (the D11 aggregate — a re-dispatch's earlier sessions
// are run history, not residue), the resolved session's own rows second, the
// legacy file of exactly the resolved directory last (a pre-import straggler).
// The window/total contract is the file-era endpoints' paging shape; a clean
// empty durable journal is a valid window, not a miss.
func scanEventsWindowFromState(b EventStateBackend, res scanResolution, offset, limit int) ([]WSEvent, int, bool) {
	if offset < 0 {
		offset = 0
	}
	if limit > maxEventsWindow {
		limit = maxEventsWindow // the lazy-page cap the file-era windows kept
	}
	ctx, cancel := eventStateCtx()
	defer cancel()
	if res.instanceForm && res.rec != nil && strings.TrimSpace(res.rec.InstanceID) != "" {
		recs, total, err := b.LoadEventsByInstance(ctx, res.rec.InstanceID, offset, limit)
		if err != nil {
			log.Printf("[journal] durable run event window for %s failed, trying the session's own rows: %v", res.rec.InstanceID, err)
		} else if total > 0 {
			return decodeEventRecords(recs), int(total), true
		}
	}
	scanID := scanStateIDForDir(res.dir)
	recs, total, err := b.LoadEventsByScan(ctx, scanID, offset, limit)
	if err != nil {
		log.Printf("[journal] durable event window for %s failed, trying legacy file: %v", scanID, err)
	} else if total > 0 {
		return decodeEventRecords(recs), int(total), true
	}
	if evs, ftotal, ok := scanEventsWindowFromFile(res.dir, offset, limit); ok && ftotal > 0 {
		return evs, ftotal, true
	}
	if err == nil {
		// The durable journal is simply empty — a valid window, not a miss.
		return []WSEvent{}, 0, true
	}
	return nil, 0, false
}

// loadScanEventsWindow returns the events in [offset, offset+limit) in original
// order, plus the total event count. Used by the lazy-paging endpoint. With a
// state backend registered (R6.1) the window reads the session's durable
// run_events rows; a session with no durable rows falls back to its legacy
// file (a pre-import directory), and a clean durable miss reads as an empty
// journal rather than a missing scan.
func loadScanEventsWindow(dir string, offset, limit int) ([]WSEvent, int, bool) {
	if dir == "" || limit <= 0 {
		return nil, 0, false
	}
	if offset < 0 {
		offset = 0
	}
	if limit > maxEventsWindow {
		limit = maxEventsWindow
	}
	if b := currentEventStateBackend(); b != nil {
		ctx, cancel := eventStateCtx()
		recs, total, err := b.LoadEventsByScan(ctx, filepath.Base(filepath.Clean(dir)), offset, limit)
		cancel()
		if err != nil {
			log.Printf("[journal] durable event window for %s failed, trying legacy file: %v", filepath.Base(dir), err)
		} else if total > 0 {
			return decodeEventRecords(recs), int(total), true
		}
		if evs, ftotal, ok := scanEventsWindowFromFile(dir, offset, limit); ok && ftotal > 0 {
			return evs, ftotal, true
		}
		if err == nil {
			// The durable journal is simply empty — a valid window, not a miss.
			return []WSEvent{}, 0, true
		}
		return nil, 0, false
	}
	return scanEventsWindowFromFile(dir, offset, limit)
}

// scanEventsWindowFromFile is the pre-R6.1 file-backed window walk, kept
// byte-identical for the nil-backend (test) mode and the legacy fallback.
func scanEventsWindowFromFile(dir string, offset, limit int) ([]WSEvent, int, bool) {
	path := filepath.Join(dir, "scan.json")
	end := offset + limit
	if rec, ok := readScanSummary(path); ok && rec.EventsJournal {
		window := make([]WSEvent, 0, limit)
		total, _, err := walkEventJournalFile(dir, func(idx int, event WSEvent) {
			if idx >= offset && idx < end {
				window = append(window, event)
			}
		})
		return window, total, err == nil
	}
	window := make([]json.RawMessage, 0, limit)
	_, total, err := walkScanJSON(path, func(idx int, raw json.RawMessage) {
		if idx >= offset && idx < end {
			window = append(window, raw)
		}
	})
	if err != nil {
		return nil, 0, false
	}
	return decodeEvents(window), total, true
}

// loadRunEventsWindow is loadScanEventsWindow across a run's sibling session
// directories (D11 re-dispatch / multi-target): the journals concatenate in
// the given chronological order and the offset/limit window indexes into the
// union. A damaged sibling journal must not blank the whole run's history, so
// its unreadable tail — including any events parsed before the damage — is
// skipped while the sessions that still parse are kept.
//
// With a state backend registered (R6.1) the union comes from the durable
// run_events rows keyed by the run's instance_id — the D11 aggregate that
// also covers sibling sessions whose directories no longer exist (retention
// deletes files, not durable rows). When the run identity cannot be derived
// or has no rows yet, the union falls back to the concrete sessions by
// scan_id, then to the legacy per-directory files (pre-import directories).
func loadRunEventsWindow(dirs []string, offset, limit int) ([]WSEvent, int, bool) {
	if len(dirs) == 0 || limit <= 0 {
		return nil, 0, false
	}
	if offset < 0 {
		offset = 0
	}
	if limit > maxEventsWindow {
		limit = maxEventsWindow
	}
	if b := currentEventStateBackend(); b != nil {
		if instanceID := instanceIDForDirs(dirs); instanceID != "" {
			ctx, cancel := eventStateCtx()
			recs, total, err := b.LoadEventsByInstance(ctx, instanceID, offset, limit)
			cancel()
			if err != nil {
				log.Printf("[journal] durable run event window for %s failed, trying per-session sources: %v", instanceID, err)
			} else if total > 0 {
				return decodeEventRecords(recs), int(total), true
			}
		}
		if evs, total, ok := runEventsWindowFromStateByScans(b, dirs, offset, limit); ok && total > 0 {
			return evs, total, true
		}
	}
	return loadRunEventsWindowFromDirs(dirs, offset, limit)
}

// runEventsWindowFromStateByScans aggregates the durable journals of exactly
// the given session directories (scan_id = directory basename) when the run
// identity itself is not derivable — the same union, bounded to the sessions
// that still have a directory.
func runEventsWindowFromStateByScans(b EventStateBackend, dirs []string, offset, limit int) ([]WSEvent, int, bool) {
	end := offset + limit
	type windowed struct {
		globalIdx int
		event     WSEvent
	}
	var win []windowed
	total := int64(0)
	anySession := false
	for _, dir := range dirs {
		base := total
		ctx, cancel := eventStateCtx()
		recs, count, err := b.LoadEventsByScan(ctx, filepath.Base(filepath.Clean(dir)), 0, 0)
		cancel()
		if err != nil {
			// Skip the unreadable session; the union keeps the rest.
			continue
		}
		anySession = true
		total += count
		for i, r := range recs {
			event, ok := decodeEventRecord(r)
			if !ok {
				continue // damaged row: skip it, keep the rest of the union
			}
			if g := base + int64(i); g >= int64(offset) && g < int64(end) {
				win = append(win, windowed{globalIdx: int(g), event: event})
			}
		}
	}
	if !anySession || total == 0 {
		return nil, 0, false
	}
	window := make([]WSEvent, 0, len(win))
	for _, w := range win {
		window = append(window, w.event)
	}
	return window, int(total), true
}

// loadRunEventsWindowFromDirs is the pre-R6.1 file-backed sibling union, kept
// byte-identical for the nil-backend (test) mode and the legacy fallback.
func loadRunEventsWindowFromDirs(dirs []string, offset, limit int) ([]WSEvent, int, bool) {
	end := offset + limit
	type windowed struct {
		globalIdx int
		event     WSEvent
	}
	var win []windowed
	total := 0
	anySession := false
	for _, dir := range dirs {
		base := total
		count, _, err := walkEventJournalFile(dir, func(idx int, event WSEvent) {
			if g := base + idx; g >= offset && g < end {
				win = append(win, windowed{globalIdx: g, event: event})
			}
		})
		if err != nil {
			// Skip the whole damaged session; the union keeps the rest.
			continue
		}
		anySession = true
		total += count
	}
	if !anySession {
		return nil, 0, false
	}
	window := make([]WSEvent, 0, len(win))
	for _, w := range win {
		window = append(window, w.event)
	}
	return window, total, true
}
