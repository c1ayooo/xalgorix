// trace.go — R3 Information-to-Decision Trace.
//
// Two audit-only projections live here:
//
//   - InfoRead: an engine-side record of a model/planner information read
//     (what was read, why, whether the result was truncated/incomplete).
//   - ActionAttempt: one target-facing action fingerprint, so a repeated action
//     can be traced to the earlier attempt and its evidence instead of being
//     repeated blindly.
//
// Both are journal-backed audit records with a bounded in-memory projection.
// They are never consulted by state transitions, claim ordering, verification
// or reporting: reading information cannot change a verdict by itself.

package scanctx

import (
	"fmt"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
)

// InfoRead is one engine-recorded information read.
type InfoRead struct {
	Tool       string
	Mode       string
	Reason     string
	RefIDs     []string
	Actor      string
	Found      bool
	Truncated  bool
	Incomplete bool
	At         time.Time
}

// ActionAttempt is one target-facing action fingerprint.
type ActionAttempt struct {
	ID           string
	Fingerprint  string
	Tool         string
	Summary      string
	EvidenceRefs []string
	Count        int
	At           time.Time
}

// InfoTrace is the read-side join for one InfoItem/Evidence ID: who read it,
// which evidence was derived from it, which hypotheses hold that evidence, and
// which intents those observations name.
type InfoTrace struct {
	ID         string
	Reads      []InfoRead
	Evidence   []Evidence
	Hypotheses []string
	IntentIDs  []string
}

const (
	maxInfoReadProjection      = 500
	maxActionAttemptProjection = 500
)

// AppendInfoRead records one information read. It is called by the tool
// execution layer after a successful (or not-found) read, never by the model.
func (ls *LedgerStore) AppendInfoRead(read InfoRead, actor domainjournal.Actor) error {
	read.Tool = truncate(strings.TrimSpace(read.Tool), maxHypothesisFieldLen)
	read.Mode = truncate(strings.TrimSpace(read.Mode), maxHypothesisFieldLen)
	read.Reason = truncate(strings.TrimSpace(read.Reason), maxSummaryLen)
	read.Actor = truncate(strings.TrimSpace(read.Actor), maxHypothesisFieldLen)
	read.At = time.Now().UTC()

	ls.mu.RLock()
	j := ls.journal
	scanID := ls.scanID
	ls.mu.RUnlock()
	if j == nil {
		return fmt.Errorf("scanctx: no journal for info read")
	}
	op := domainjournal.OpReadInfoItem
	if read.Tool == "search_info" {
		op = domainjournal.OpSearchInfo
	}
	summary := fmt.Sprintf("%s mode=%s found=%t truncated=%t incomplete=%t refs=%s reason=%s",
		read.Tool, read.Mode, read.Found, read.Truncated, read.Incomplete,
		strings.Join(read.RefIDs, ","), read.Reason)
	if _, err := j.Append(domainjournal.Record{
		ScanID:    scanID,
		Actor:     actor,
		Operation: op,
		Summary:   truncate(summary, 2048),
	}); err != nil {
		return fmt.Errorf("scanctx: append info read: %w", err)
	}
	ls.mu.Lock()
	ls.infoReads = append(ls.infoReads, read)
	if len(ls.infoReads) > maxInfoReadProjection {
		ls.infoReads = append([]InfoRead(nil), ls.infoReads[len(ls.infoReads)-maxInfoReadProjection:]...)
	}
	ls.mu.Unlock()
	return nil
}

// InfoReads returns a copy of the bounded read projection.
func (ls *LedgerStore) InfoReads() []InfoRead {
	if ls == nil {
		return nil
	}
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return append([]InfoRead(nil), ls.infoReads...)
}

// TraceInfoItem joins reads, evidence, hypotheses and intents by one ID.
// Evidence matches when its ID or InfoItemID is the requested ID; hypotheses
// are those holding that evidence; intents are the Evidence.IntentID values.
func (ls *LedgerStore) TraceInfoItem(id string) InfoTrace {
	trace := InfoTrace{ID: strings.TrimSpace(id)}
	if ls == nil || trace.ID == "" {
		return trace
	}
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	for _, r := range ls.infoReads {
		for _, ref := range r.RefIDs {
			if strings.EqualFold(strings.TrimSpace(ref), trace.ID) {
				trace.Reads = append(trace.Reads, r)
				break
			}
		}
	}
	seenHyp := map[string]bool{}
	seenIntent := map[string]bool{}
	for _, h := range ls.hyps {
		for _, ev := range h.Evidence {
			match := strings.EqualFold(ev.ID, trace.ID) || strings.EqualFold(ev.InfoItemID, trace.ID)
			if !match {
				continue
			}
			trace.Evidence = append(trace.Evidence, ev)
			if !seenHyp[h.ID] {
				seenHyp[h.ID] = true
				trace.Hypotheses = append(trace.Hypotheses, h.ID)
			}
			if ev.IntentID != "" && !seenIntent[ev.IntentID] {
				seenIntent[ev.IntentID] = true
				trace.IntentIDs = append(trace.IntentIDs, ev.IntentID)
			}
		}
	}
	return trace
}

// RecordActionAttempt records one action fingerprint and returns the prior
// attempt when the same fingerprint was seen before. The prior attempt is the
// trace anchor a repeat-suppression nudge cites.
func (ls *LedgerStore) RecordActionAttempt(fingerprint, tool, summary string, evidenceRefs []string, actor domainjournal.Actor) (ActionAttempt, *ActionAttempt, error) {
	fingerprint = truncate(strings.TrimSpace(fingerprint), maxSummaryLen)
	if fingerprint == "" {
		return ActionAttempt{}, nil, fmt.Errorf("scanctx: empty action fingerprint")
	}
	ls.mu.Lock()
	var prior *ActionAttempt
	count := 1
	for i := range ls.actionAttempts {
		if ls.actionAttempts[i].Fingerprint != fingerprint {
			continue
		}
		cp := ls.actionAttempts[i]
		prior = &cp // latest matching attempt is the trace anchor
		if cp.Count >= count {
			count = cp.Count + 1
		}
	}
	ls.actionSeq++
	att := ActionAttempt{
		ID:           fmt.Sprintf("A-%d", ls.actionSeq),
		Fingerprint:  fingerprint,
		Tool:         truncate(strings.TrimSpace(tool), maxHypothesisFieldLen),
		Summary:      truncate(strings.TrimSpace(summary), maxSummaryLen),
		EvidenceRefs: append([]string(nil), evidenceRefs...),
		Count:        count,
		At:           time.Now().UTC(),
	}
	ls.actionAttempts = append(ls.actionAttempts, att)
	if len(ls.actionAttempts) > maxActionAttemptProjection {
		ls.actionAttempts = append([]ActionAttempt(nil), ls.actionAttempts[len(ls.actionAttempts)-maxActionAttemptProjection:]...)
	}
	ls.mu.Unlock()

	if j := ls.journalForAppend(); j != nil {
		priorID := ""
		if prior != nil {
			priorID = prior.ID
		}
		_, err := j.Append(domainjournal.Record{
			ScanID:    ls.scanIDForAppend(),
			Actor:     actor,
			Operation: domainjournal.OpActionAttempt,
			Summary:   truncate(fmt.Sprintf("%s %s prior=%s %s", att.ID, fingerprint, priorID, att.Summary), 2048),
		})
		if err != nil {
			return ActionAttempt{}, prior, fmt.Errorf("scanctx: append action attempt: %w", err)
		}
	}
	return att, prior, nil
}

// ActionAttempts returns a copy of the bounded attempt projection.
func (ls *LedgerStore) ActionAttempts() []ActionAttempt {
	if ls == nil {
		return nil
	}
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return append([]ActionAttempt(nil), ls.actionAttempts...)
}

// AppendEngagementEvent writes one egress-policy audit record. It is called by
// the budget, never by the model, and never mutates scan state.
func (ls *LedgerStore) AppendEngagementEvent(operation, summary string) error {
	if ls == nil {
		return nil
	}
	op := domainjournal.OpEgressPause
	if strings.Contains(operation, "stop") {
		op = domainjournal.OpEgressStop
	}
	ls.mu.RLock()
	j := ls.journal
	scanID := ls.scanID
	ls.mu.RUnlock()
	if j == nil {
		return nil
	}
	_, err := j.Append(domainjournal.Record{
		ScanID: scanID, Actor: engineActor("egress"), Operation: op,
		Summary: truncate(strings.TrimSpace(summary), 1024),
	})
	return err
}

func (ls *LedgerStore) journalForAppend() *domainjournal.Journal {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return ls.journal
}

func (ls *LedgerStore) scanIDForAppend() string {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return ls.scanID
}
