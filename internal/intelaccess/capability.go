// capability.go — one-shot permissions for reaching a target OUTSIDE the
// project scope (INTEL_HANDOFF_SPEC M3 / D3).
//
// A capability is issued by an operator (decision inbox) or a rule engine, is
// bound to one intent and one exact canonical target, and expires. It is
// consumed once per dispatch decision (D9), never per HTTP hop. DenyTargets
// never receive one (D7).

package intelaccess

import (
	"context"
	"fmt"
	"time"
)

// capabilityBatchID groups the rows of one approval so the UI/audit can see
// "one decision, N doors".
func capabilityBatchID(scanID string, accessID int64, now time.Time) string {
	return fmt.Sprintf("cap-%s-%d-%d", scanID, accessID, now.UnixNano())
}

// Capability is one issued permission.
type Capability struct {
	ID        int64     `json:"id"`
	ScanID    string    `json:"scan_id"`
	IntentID  int64     `json:"intent_id"`
	AccessID  int64     `json:"access_id"`
	Target    string    `json:"target"`
	BatchID   string    `json:"batch_id,omitempty"`
	UsesLeft  int       `json:"uses_left"`
	ExpiresAt time.Time `json:"expires_at"`
	IssuedBy  string    `json:"issued_by"`
	CreatedAt time.Time `json:"created_at"`
}

// IssueInput is one operator/rule approval. Targets must already be canonical
// (NormalizeTarget); the issuers do that before calling the store so an
// approval can never widen a hand-typed target.
type IssueInput struct {
	ScanID   string
	IntentID int64
	AccessID int64
	Targets  []string
	UsesLeft int
	TTL      time.Duration
	IssuedBy string
}

// Issuer is the narrow write surface of approvals (the decision endpoint and
// the rule engine are its only callers).
type Issuer interface {
	IssueCapabilities(ctx context.Context, in IssueInput) ([]Capability, error)
}
