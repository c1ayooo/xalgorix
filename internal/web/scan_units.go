package web

// scan_units.go — GET /api/scans/{id}/units: the unified WorkUnit projection
// (docs/WORK_UNIT_DESIGN.md B4, 2026-11-23).
//
// A "unit" is the dispatch identity of work already modelled by the charter's
// own objects (no new top-level primitive):
//   - intent units   = plan tasks (PlanTask) with subject/owner/lease
//   - belief units   = ledger hypotheses (Hypothesis) with owner/lease
//
// The projection exists so panels and operators can see, on one page, what is
// open, what is claimed (and until when), and what leases have lapsed. The
// authorities stay exactly where the charter puts them: the live plan on
// ScanState (or its persisted PlanView), and the ledger; this endpoint only
// reads. Finish-gate semantics are unchanged (the gate already refuses on
// open plan tasks / unreported proven hypotheses).

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// scanUnit is one dispatch unit in the projection.
type scanUnit struct {
	Kind       string     `json:"kind"` // task | hypothesis
	ID         string     `json:"id"`
	Subject    string     `json:"subject"`
	State      string     `json:"state"`
	Owner      string     `json:"owner,omitempty"`
	LeaseUntil *time.Time `json:"lease_until,omitempty"`
	VulnClass  string     `json:"vuln_class,omitempty"`
	Phase      int        `json:"phase,omitempty"`
	Expired    bool       `json:"expired,omitempty"`
}

type scanUnitsView struct {
	ScanID   string         `json:"scan_id"`
	Source   string         `json:"source"` // live | record | none
	Units    []scanUnit     `json:"units"`
	Counters map[string]int `json:"counters"`
}

// handleScanUnits serves GET /api/scans/{id}/units.
func (s *Server) handleScanUnits(w http.ResponseWriter, r *http.Request) {
	scanID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/scans/"), "/units")
	if scanID == "" || strings.Contains(scanID, "/") {
		http.Error(w, `{"error":"scan id is required"}`, http.StatusBadRequest)
		return
	}
	view := scanUnitsView{
		ScanID: filepath.Base(scanID),
		Source: "none",
		Units:  []scanUnit{},
		Counters: map[string]int{
			"tasks_open": 0, "tasks_claimed": 0, "tasks_expired": 0,
			"hypotheses_queued": 0, "hypotheses_testing": 0, "hypotheses_expired": 0,
		},
	}
	now := time.Now().UTC()

	// R7/M1: the graph is the authority for intent/belief units. A zero-row
	// graph falls through to the legacy live/record/ledger projection while
	// the graph is still being seeded.
	if repo := graph.Backend(); repo != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		units, err := repo.Units(ctx, s.graphRunID(scanID))
		cancel()
		if err != nil {
			writeJSONStatus(w, http.StatusServiceUnavailable, map[string]any{
				"error": "graph units read failed", "detail": err.Error(), "scan_id": scanID,
			})
			return
		}
		if len(units) > 0 {
			view.Source = "graph"
			for _, u := range units {
				kind, state := "task", u.State
				if u.Kind == "belief" {
					kind = "hypothesis"
				} else {
					state = planStatusFromGraphState(u.State)
				}
				unit := scanUnit{
					Kind:       kind,
					ID:         u.UnitID,
					Subject:    u.Subject,
					State:      state,
					Owner:      u.Owner,
					LeaseUntil: u.LeaseUntil,
					VulnClass:  u.VulnClass,
					Phase:      u.Phase,
					Expired:    u.Expired,
				}
				if unit.Expired {
					if kind == "task" {
						view.Counters["tasks_expired"]++
					} else {
						view.Counters["hypotheses_expired"]++
					}
				}
				switch kind {
				case "task":
					switch state {
					case "pending":
						view.Counters["tasks_open"]++
					case "active":
						view.Counters["tasks_claimed"]++
					}
				case "hypothesis":
					switch u.State {
					case string(scanctx.HypothesisQueued):
						view.Counters["hypotheses_queued"]++
					case string(scanctx.HypothesisTesting):
						view.Counters["hypotheses_testing"]++
					}
				}
				view.Units = append(view.Units, unit)
			}
			writeJSONStatus(w, http.StatusOK, view)
			return
		}
	}

	// Intent units: live plan first, then the persisted projection.
	if planView, ok := s.livePlanView(scanID); ok && planView.Present {
		view.Source = "live"
		appendPlanUnits(&view, planView, now)
	} else if dir, ok := s.resolveScanDirByID(scanID); ok {
		if rec, ok := loadScanRecordFromDir(dir); ok {
			pv := planViewFromRecord(rec)
			view.Source = "record"
			appendPlanUnits(&view, pv, now)
		}
	}

	// Belief units: the persisted ledger snapshot (live ledger is read-only
	// projectable too, but the file is the durable authority across restarts).
	if dir, ok := s.resolveScanDirByID(scanID); ok {
		if raw, err := os.ReadFile(filepath.Join(dir, ledgerFileName)); err == nil {
			var snap struct {
				Hypotheses []scanctx.Hypothesis `json:"hypotheses"`
			}
			if json.Unmarshal(raw, &snap) == nil {
				for i := range snap.Hypotheses {
					h := &snap.Hypotheses[i]
					u := scanUnit{
						Kind:      "hypothesis",
						ID:        h.ID,
						Subject:   hypothesisSubject(h),
						State:     string(h.Status),
						Owner:     h.AssignedTo,
						VulnClass: h.VulnClass,
					}
					if !h.LeaseUntil.IsZero() {
						lease := h.LeaseUntil
						u.LeaseUntil = &lease
						if string(h.Status) == string(scanctx.HypothesisTesting) && now.After(lease) {
							u.Expired = true
							view.Counters["hypotheses_expired"]++
						}
					}
					switch string(h.Status) {
					case string(scanctx.HypothesisQueued):
						view.Counters["hypotheses_queued"]++
					case string(scanctx.HypothesisTesting):
						view.Counters["hypotheses_testing"]++
					}
					view.Units = append(view.Units, u)
				}
			}
		}
	}
	writeJSONStatus(w, http.StatusOK, view)
}

// appendPlanUnits projects plan tasks onto the unit list.
func appendPlanUnits(view *scanUnitsView, pv agent.PlanView, now time.Time) {
	for _, t := range pv.Tasks {
		subject := ""
		if t.Endpoint != "" {
			subject = "endpoint:" + strings.ToLower(t.Endpoint)
		} else if t.VulnClass != "" {
			subject = strings.ToLower(t.VulnClass)
		} else {
			subject = "phase:" + strconv.Itoa(t.Phase)
		}
		u := scanUnit{
			Kind:      "task",
			ID:        t.ID,
			Subject:   subject,
			State:     t.Status,
			Owner:     t.Owner,
			VulnClass: t.VulnClass,
			Phase:     t.Phase,
		}
		if !t.LeaseUntil.IsZero() {
			lease := t.LeaseUntil
			u.LeaseUntil = &lease
			if t.Status == "active" && now.After(lease) {
				u.Expired = true
				view.Counters["tasks_expired"]++
			}
		}
		switch t.Status {
		case "pending":
			view.Counters["tasks_open"]++
		case "active":
			view.Counters["tasks_claimed"]++
		}
		view.Units = append(view.Units, u)
	}
}

// planStatusFromGraphState maps the authoritative intent state onto the
// legacy PlanTask vocabulary used by the units API.
func planStatusFromGraphState(state string) string {
	switch state {
	case "open":
		return "pending"
	case "running", "paused":
		return "active"
	case "done":
		return "completed"
	case "blocked", "exhausted", "stopped", "deleted":
		return "skipped"
	default:
		return state
	}
}

func hypothesisSubject(h *scanctx.Hypothesis) string {
	if h == nil {
		return ""
	}
	if ep := strings.TrimSpace(h.Endpoint); ep != "" {
		return "endpoint:" + strings.ToLower(ep)
	}
	return strings.ToLower(strings.TrimSpace(h.VulnClass))
}
