package web

// handlers_intent_proposals.go — the deterministic confirmation path for main
// chat intent drafts.
//
// The main agent never writes the plan directly: propose_intent stores an
// in-memory draft and emits intent_proposed. The operator confirms it either
// by clicking the transcript card (these endpoints) or by an explicit later
// message handled by confirm_intent. The click path deliberately does not go
// through the model: a button press is the operator's decision, not a prompt
// hint, and it works even while the model is busy.
//
// Proposals are process memory, not state authority: a run that ends or the
// server restarting discards them, and the endpoint answers 409 honestly
// instead of pretending to confirm something that no longer exists.

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

func (s *Server) handleIntentProposalDecision(
	w http.ResponseWriter,
	r *http.Request,
	store platform.Repository,
	instanceID, proposalID string,
	confirm bool,
) {
	instanceID = strings.TrimSpace(instanceID)
	proposalID = strings.TrimSpace(proposalID)
	if instanceID == "" || proposalID == "" {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	agnt := s.instanceAgent(instanceID)
	if agnt == nil {
		writeJSONStatus(w, http.StatusConflict, map[string]string{
			"error": "run is not live; intent proposals expire with the run",
		})
		return
	}

	createdBy := ""
	if claims, ok := sessionClaimsFrom(r); ok {
		createdBy = claims.Username
	}

	if confirm {
		taskID, title, err := agnt.ConfirmMainIntentProposal(proposalID)
		if err != nil {
			writeJSONStatus(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		s.recordIntentProposalDecision(r.Context(), store, instanceID, proposalID, title, createdBy, true)
		writeJSONStatus(w, http.StatusOK, map[string]any{
			"status":      "confirmed",
			"proposal_id": proposalID,
			"task_id":     taskID,
		})
		return
	}

	title, err := agnt.RejectMainIntentProposal(proposalID)
	if err != nil {
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	s.recordIntentProposalDecision(r.Context(), store, instanceID, proposalID, title, createdBy, false)
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"status":      "rejected",
		"proposal_id": proposalID,
	})
}

// recordIntentProposalDecision writes the operator's click as a human
// guidance turn (audit + transcript). Failure is logged, never fatal: the
// intent write already happened, and the event stream carries the outcome.
func (s *Server) recordIntentProposalDecision(
	ctx context.Context,
	store platform.Repository,
	instanceID, proposalID, title, createdBy string,
	confirmed bool,
) {
	if store == nil {
		return
	}
	run, err := s.runForInstance(ctx, store, instanceID)
	if err != nil || run == nil {
		return
	}
	status := "rejected"
	prefix := "放弃意图提案："
	if confirmed {
		status = "confirmed"
		prefix = "确认派发意图："
	}
	message := prefix + strings.TrimSpace(title)
	if strings.TrimSpace(title) == "" {
		message = prefix + proposalID
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	g, created, err := store.CreateGuidance(cctx, platform.GuidanceInput{
		TaskID:     run.TaskID,
		RunID:      run.ID,
		InstanceID: instanceID,
		Message:    message,
		RequestID:  "proposal-" + proposalID + "-" + status,
		CreatedBy:  createdBy,
	})
	if err != nil {
		log.Printf("[platform] proposal %s: record decision: %v", proposalID, err)
		return
	}
	if !created {
		return
	}
	if _, err := store.UpdateGuidanceStatus(cctx, g.ID, platform.GuidanceInjected); err != nil {
		log.Printf("[platform] proposal %s: mark decision turn injected: %v", proposalID, err)
		return
	}
	g.Status = platform.GuidanceInjected
	s.broadcastToInstance(instanceID, WSEvent{
		Type:     "guidance_injected",
		Guidance: g,
		Content:  "Operator decision recorded",
	})
}
