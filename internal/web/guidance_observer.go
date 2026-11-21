package web

// guidance_observer.go — guidance consumed flip closed loop (REDESIGN §5.2
// R2a deferred item, API_CONTRACT §3.1: "consumed = 引擎已消费（对应
// hypothesis/evidence 产出后由后端翻转）").
//
// Mechanism:
//
//  1. Path A (SendMessage injection, handlers_approvals.go) embeds a stable
//     in-prompt marker `[OPERATOR GUIDANCE gi:<guidanceID>]: ` so the exact
//     guidance row is attributable from engine output.
//  2. On injection success the web layer arms a guidanceWatcher on the
//     instance's per-scan domain journal (scanctx-created by
//     LedgerStore.EnableJournal, <scanDir>/domain_journal.jsonl). The
//     domainjournal package exposes a process-global observer seam
//     (RegisterObserver; coldpath/service.go is the precedent) that fires on
//     the append goroutine with (path, scanID, seq) only — never the record
//     body — so the watcher drains notifications on its own goroutine and
//     reads the durable records there (the append path only does a
//     non-blocking channel send, per the observer contract).
//  3. When a hypothesis/evidence record's free text (summary, InfoItem
//     content) cites `gi:<id>` of a pending injected row, the watcher flips
//     the row via UpdateGuidanceStatus(consumed) and broadcasts
//     `guidance_consumed` (web-layer WS event, event flow stays one-way).
//
// Path B (queued → briefing merge in scan_session.go) has no per-row marker
// carrier, so those rows intentionally stay injected (documented decision,
// API_CONTRACT §3.1): the merge text cannot be attributed back to specific
// rows, and "consumed" would be a guess, not an observation.
//
// Lifecycle: the watcher retires itself when its pending set drains, when the
// scan context is deactivated (session cleanup), or on store errors it stays
// armed and only logs — it never panics and never blocks the journal append.

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/domainjournal"
	"github.com/xalgord/xalgorix/v4/internal/platform"
	"github.com/xalgord/xalgorix/v4/internal/scanctx"
)

// guidanceMarkerPrefixFmt is the stable marker carrier for path A. gi:<id> is
// our own guidance row id (uuid), never a secret; it must enter the prompt
// anyway, so carrying the id is free.
const guidanceMarkerPrefixFmt = "[OPERATOR GUIDANCE gi:%s]: "

// guidanceConsumedTimeout bounds each store flip from the watcher goroutine so
// a slow store cannot pin a watcher indefinitely.
const guidanceConsumedTimeout = 3 * time.Second

// guidanceWatchCapacity is the per-watcher notification buffer. Overflow falls
// back to a detached goroutine, so notifications are never dropped — there is
// no reconciler backstop for this seam (unlike the cold path).
const guidanceWatchCapacity = 64

// guidanceMarkerRe extracts gi:<id> tokens. The \b keeps "logi:8080" style
// substrings from matching; ids we do not recognize are filtered by the
// pending-set intersection, so a stray token can never flip a foreign row.
var guidanceMarkerRe = regexp.MustCompile(`\bgi:([0-9A-Za-z][0-9A-Za-z._:-]*)`)

// operatorGuidanceMessage renders the injected operator turn (marker + body).
func operatorGuidanceMessage(guidanceID, message string) string {
	return fmt.Sprintf(guidanceMarkerPrefixFmt, guidanceID) + message
}

// extractGuidanceMarkerIDs returns the gi:<id> tokens found in texts, deduped
// in first-seen order. Callers must intersect with their own pending set.
func extractGuidanceMarkerIDs(texts ...string) []string {
	seen := make(map[string]struct{})
	var ids []string
	for _, text := range texts {
		for _, m := range guidanceMarkerRe.FindAllStringSubmatch(text, -1) {
			id := m[1]
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids
}

// guidanceRecordTexts lists the free-text fields of a journal record that the
// engine fills from model output (hypothesis/evidence summaries, InfoItem
// content). Only these may carry the marker — State fields and relation edges
// are structured and never quoted prose.
func guidanceRecordTexts(rec domainjournal.Record) []string {
	texts := make([]string, 0, 3)
	if rec.Summary != "" {
		texts = append(texts, rec.Summary)
	}
	if rec.InfoItem != nil && rec.InfoItem.Coordinates.Content.Text != "" {
		texts = append(texts, rec.InfoItem.Coordinates.Content.Text)
	}
	for i := range rec.InfoItems {
		if t := rec.InfoItems[i].Coordinates.Content.Text; t != "" {
			texts = append(texts, t)
		}
	}
	return texts
}

// ── watcher registry ─────────────────────────────────────────────────────────
//
// One dispatch hook is installed process-wide (the domainjournal observer
// registry is global) and fans out to live watchers by journal path. Lock
// order is guidanceWatchersMu → watcher.mu everywhere; retire releases
// watcher.mu before touching the registry, so the reverse nesting never
// happens.

var (
	guidanceWatchersMu sync.Mutex
	guidanceWatchers   = map[string]*guidanceWatcher{} // journal path → live watcher
	guidanceObserver   sync.Once
)

// dispatchGuidanceJournalAppend is the domainjournal.Observer callback. It
// runs on the journal append goroutine: map lookup + non-blocking send only.
func dispatchGuidanceJournalAppend(path, scanID string, seq uint64) {
	guidanceWatchersMu.Lock()
	w := guidanceWatchers[path]
	guidanceWatchersMu.Unlock()
	if w != nil {
		w.notify(seq)
	}
}

type guidanceWatcher struct {
	journalPath string
	scanCtxID   string // scanctx registry key (= session id = journal scan_id)
	instanceID  string
	store       platform.Repository
	broadcast   func(WSEvent) // web-layer WS emit, bound at construction

	mu      sync.Mutex
	pending map[string]struct{}
	lastSeq uint64 // highest journal seq already scanned
	stopped bool

	notifyCh chan uint64
	stopCh   chan struct{}
	stopOnce sync.Once
}

// watchGuidanceConsumption arms the journal observer for one injected guidance
// row. Best effort by design: no scan context, no scan dir, or no platform
// store leaves the row injected (the pre-closed-loop behavior) — guidance
// delivery itself never depends on the platform, mirroring the fail-open
// posture of the rest of the HITL surface.
func (s *Server) watchGuidanceConsumption(instanceID, guidanceID string) {
	sctx := s.instanceScanContext(instanceID)
	if sctx == nil || strings.TrimSpace(sctx.ScanDir) == "" {
		log.Printf("[platform] guidance %s: no scan context for instance %s; consumed observation unavailable", guidanceID, instanceID)
		return
	}
	store := s.currentPlatformStore()
	if store == nil {
		log.Printf("[platform] guidance %s: no platform store; consumed observation unavailable", guidanceID)
		return
	}
	path := filepath.Join(sctx.ScanDir, domainjournal.FileName)

	guidanceWatchersMu.Lock()
	w := guidanceWatchers[path]
	if w == nil || w.stoppedLocked() {
		w = &guidanceWatcher{
			journalPath: path,
			scanCtxID:   sctx.ID,
			instanceID:  instanceID,
			store:       store,
			broadcast:   func(evt WSEvent) { s.broadcastToInstance(instanceID, evt) },
			pending:     make(map[string]struct{}),
			notifyCh:    make(chan uint64, guidanceWatchCapacity),
			stopCh:      make(chan struct{}),
		}
		w.pending[guidanceID] = struct{}{}
		guidanceWatchers[path] = w
		guidanceWatchersMu.Unlock()
		guidanceObserver.Do(func() {
			domainjournal.RegisterObserver(dispatchGuidanceJournalAppend)
		})
		go w.drain()
		return
	}
	w.mu.Lock()
	w.pending[guidanceID] = struct{}{}
	w.mu.Unlock()
	guidanceWatchersMu.Unlock()
}

// instanceScanContext reads the instance's per-session scan context. The
// session goroutine assigns sctx once under inst.mu; readers take the same
// lock (same discipline as instanceCanReceiveGuidance).
func (s *Server) instanceScanContext(instanceID string) *scanctx.ScanContext {
	s.instancesMu.RLock()
	inst := s.instances[instanceID]
	s.instancesMu.RUnlock()
	if inst == nil {
		return nil
	}
	inst.mu.RLock()
	defer inst.mu.RUnlock()
	return inst.sctx
}

func (w *guidanceWatcher) stoppedLocked() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stopped
}

// notify hands one durable-append notification to the drain goroutine. Never
// blocks (the caller is the journal append goroutine) and never drops.
func (w *guidanceWatcher) notify(seq uint64) {
	select {
	case <-w.stopCh:
		return
	default:
	}
	select {
	case w.notifyCh <- seq:
	default:
		go w.consume(seq) // buffer full: detach; consume serializes on w.mu
	}
}

func (w *guidanceWatcher) drain() {
	for {
		select {
		case <-w.stopCh:
			return
		case seq := <-w.notifyCh:
			w.consume(seq)
		}
	}
}

// consume processes one append notification on the watcher goroutine (or a
// detached fallback): records above the watermark are scanned for pending
// gi:<id> markers; a hit flips the row to consumed and broadcasts
// guidance_consumed. Read failure leaves the watermark untouched so the next
// notification retries the same range.
func (w *guidanceWatcher) consume(seq uint64) {
	w.mu.Lock()
	reason := w.processLocked(seq)
	w.mu.Unlock()
	if reason != "" {
		w.retire(reason)
	}
}

// processLocked scans newly durable records. A non-empty return reason means
// the watcher must retire (the caller does it after releasing mu).
func (w *guidanceWatcher) processLocked(seq uint64) string {
	if w.stopped || seq <= w.lastSeq {
		return ""
	}
	if len(w.pending) == 0 {
		w.markStoppedLocked()
		return "no pending guidance rows"
	}
	// Session context deactivated (run finished/cleaned up): the engine can no
	// longer produce records for this journal, so stop watching.
	if scanctx.Get(w.scanCtxID) == nil {
		w.markStoppedLocked()
		return "scan context no longer active"
	}
	records, _, err := domainjournal.ReadRecords(w.journalPath)
	if err != nil {
		log.Printf("[platform] guidance watcher %s: read journal: %v", w.journalPath, err)
		return ""
	}
	high := seq
	texts := make([]string, 0, 4)
	for _, rec := range records {
		if rec.Seq <= w.lastSeq || rec.Seq > high {
			continue
		}
		texts = append(texts, guidanceRecordTexts(rec)...)
	}
	w.lastSeq = high
	for _, id := range extractGuidanceMarkerIDs(texts...) {
		if _, pending := w.pending[id]; !pending {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), guidanceConsumedTimeout)
		row, err := w.store.UpdateGuidanceStatus(ctx, id, platform.GuidanceConsumed)
		cancel()
		if err != nil {
			// Row stays pending; a later engine record may still cite the
			// marker. Never fatal for the scan.
			log.Printf("[platform] guidance %s flip to consumed failed: %v", id, err)
			continue
		}
		delete(w.pending, id)
		if w.broadcast != nil {
			w.broadcast(WSEvent{
				Type:     "guidance_consumed",
				Content:  "Operator guidance consumed by the engine (cited in hypothesis/evidence output)",
				Guidance: row,
			})
		}
	}
	if len(w.pending) == 0 {
		w.markStoppedLocked()
		return "guidance consumed"
	}
	return ""
}

func (w *guidanceWatcher) markStoppedLocked() {
	w.stopped = true
	w.stopOnce.Do(func() { close(w.stopCh) })
}

// retire idempotently stops the watcher and removes it from the registry.
func (w *guidanceWatcher) retire(reason string) {
	w.mu.Lock()
	w.markStoppedLocked()
	w.mu.Unlock()
	guidanceWatchersMu.Lock()
	if guidanceWatchers[w.journalPath] == w {
		delete(guidanceWatchers, w.journalPath)
	}
	guidanceWatchersMu.Unlock()
	if reason != "" {
		log.Printf("[platform] guidance watcher for %s retired: %s", w.journalPath, reason)
	}
}
