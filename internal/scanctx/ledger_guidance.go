package scanctx

// ledger_guidance.go — operator guidance enters the information space as a
// human-sourced observed InfoItem when it is delivered to a run. The R7 hint
// node remains the plan-facing projection; this journal record is the
// information-layer projection used by region/association lighting.
//
// Delivery can be replayed after a process restart, so the item ID is derived
// from the guidance row ID and upserts converge on one row.

import (
	"fmt"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
)

// GuidanceObservation identifies one delivered operator guidance row.
type GuidanceObservation struct {
	ID         string
	TaskID     string
	RunID      string
	InstanceID string
	CreatedBy  string
	Message    string
	ObservedAt time.Time
}

// AppendGuidanceObservation records one delivered guidance row as an observed
// InfoItem. Audit/cold-path only: it never mutates the hypothesis graph, plan
// or finding state.
func (ls *LedgerStore) AppendGuidanceObservation(obs GuidanceObservation) error {
	if ls == nil {
		return fmt.Errorf("scanctx: nil ledger")
	}
	if strings.TrimSpace(obs.ID) == "" {
		return fmt.Errorf("scanctx: guidance observation requires an id")
	}
	ls.mu.RLock()
	scanID := ls.scanID
	ls.mu.RUnlock()
	if scanID == "" {
		return fmt.Errorf("scanctx: guidance observation requires a scan id")
	}
	item := NewGuidanceInfoItem(scanID, obs)
	actor := domainjournal.Actor{Kind: domainjournal.ActorHuman, ID: strings.TrimSpace(obs.CreatedBy), Source: "operator_guidance"}
	return ls.appendObservation(domainjournal.OpRecordGuidance, actor, &item, "operator guidance delivered to run")
}

// NewGuidanceInfoItem builds the observed InfoItem for one delivered guidance
// row. Human guidance is raw input, so its nature is observed (never inferred),
// and its source kind is "human".
func NewGuidanceInfoItem(scanID string, obs GuidanceObservation) infoitem.Item {
	now := obs.ObservedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	subject := strings.TrimSpace(obs.TaskID)
	if subject == "" {
		subject = "task"
	}
	text := strings.TrimSpace(obs.Message)
	if len([]rune(text)) > 8192 {
		text = string([]rune(text)[:8192]) + "…"
	}
	context := map[string]string{
		"guidance_id": strings.TrimSpace(obs.ID),
	}
	if obs.TaskID != "" {
		context["task_id"] = obs.TaskID
	}
	if obs.RunID != "" {
		context["run_id"] = obs.RunID
	}
	if obs.InstanceID != "" {
		context["instance_id"] = obs.InstanceID
	}
	if obs.CreatedBy != "" {
		context["created_by"] = obs.CreatedBy
	}
	return infoitem.Item{
		ID:     infoitem.StableGuidanceID(scanID, obs.ID),
		ScanID: scanID,
		Nature: infoitem.NatureObserved,
		Coordinates: infoitem.Coordinates{
			Content:    infoitem.Content{Text: text, MediaType: "text/plain"},
			Source:     infoitem.Source{Kind: "human", Name: "operator"},
			ObservedAt: now,
			Subject:    infoitem.Subject{Kind: "task", Identifier: subject},
			Purpose:    "operator_guidance",
			Context:    context,
		},
		CoordinateStatus: infoitem.CoordinatesComplete,
		CreatedAt:        now,
	}
}
