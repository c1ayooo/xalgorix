package scanctx

// ledger_objective.go — the operator-stated task objective enters the
// information layer as a human-sourced observation (charter §2: human input
// is a `source=human` InfoItem; §3.4: observed vs inferred).
//
// The objective is context for the loop, never a verdict: the record carries
// no hypothesis mutation, and nothing downstream may treat it as proof.

import (
	"fmt"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
)

// AppendObjectiveObservation records the task objective as an observed
// InfoItem in this scan's domain journal. The journal is only the host: the
// objective never mutates the hypothesis graph and never participates in a
// verdict.
func (ls *LedgerStore) AppendObjectiveObservation(item *infoitem.Item, summary string) error {
	ls.mu.RLock()
	j := ls.journal
	ls.mu.RUnlock()
	if j == nil {
		return fmt.Errorf("scanctx: no journal for objective observation")
	}
	if item == nil {
		return fmt.Errorf("scanctx: objective observation requires an info item")
	}
	rec := domainjournal.Record{
		ScanID:    ls.scanID,
		Actor:     domainjournal.Actor{Kind: domainjournal.ActorHuman, Source: "task_objective"},
		Operation: domainjournal.OpRecordObjective,
		Summary:   summary,
		InfoItem:  item,
	}
	if _, err := j.Append(rec); err != nil {
		return fmt.Errorf("scanctx: append objective observation: %w", err)
	}
	return nil
}

// ObjectiveObservation describes the human-stated objective of a run.
type ObjectiveObservation struct {
	ID         string
	ProjectID  string
	TaskID     string
	Statement  string
	Criteria   []string
	ObservedAt time.Time
}

// NewObjectiveInfoItem builds the observed InfoItem for an objective. A human
// statement is a raw input, so the nature is observed (not inferred) and the
// source kind is "human".
func NewObjectiveInfoItem(obs ObjectiveObservation) infoitem.Item {
	now := obs.ObservedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	subject := strings.TrimSpace(obs.TaskID)
	if subject == "" {
		subject = "task"
	}
	text := obs.Statement
	if len(obs.Criteria) > 0 {
		text = obs.Statement + "\n" + formatObjectiveCriteria(obs.Criteria)
	}
	context := map[string]string{}
	if obs.ProjectID != "" {
		context["project_id"] = obs.ProjectID
	}
	if obs.TaskID != "" {
		context["task_id"] = obs.TaskID
	}
	return infoitem.Item{
		ID:        obs.ID,
		Nature:    infoitem.NatureObserved,
		CreatedAt: now,
		Coordinates: infoitem.Coordinates{
			Content:    infoitem.Content{Text: text, MediaType: "text/plain"},
			Source:     infoitem.Source{Kind: "human", Name: "task_objective"},
			Subject:    infoitem.Subject{Kind: "task", Identifier: subject},
			Purpose:    "task_objective",
			Context:    context,
			ObservedAt: now,
		},
	}
}

func formatObjectiveCriteria(criteria []string) string {
	parts := make([]string, 0, len(criteria))
	for _, c := range criteria {
		if strings.TrimSpace(c) == "" {
			continue
		}
		parts = append(parts, "- "+c)
	}
	return strings.Join(parts, "\n")
}
