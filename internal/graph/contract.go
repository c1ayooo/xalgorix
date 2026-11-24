package graph

// contract.go — the intent contract carried in an intent node's payload.
//
// The contract is deliberately small and structural: it never carries
// evidence bodies, tool output, or model prose. Preconditions reference
// existing graph nodes by (kind, unit_key, state); yields declare what a
// concluded intent must produce. The contract is the write-side counterpart
// of the runtime conformance checks in conformance.go.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	// ContractVersion is the only contract version this binary understands.
	ContractVersion = 1
	// MaxContractPreconditions bounds one intent's precondition list.
	MaxContractPreconditions = 16
	// MaxContractYields bounds one intent's declared outputs.
	MaxContractYields = 8
	// MaxContractRefLen bounds a referenced unit key.
	MaxContractRefLen = 128
	// MaxContractTimeboxSeconds bounds the declared execution budget (24h).
	MaxContractTimeboxSeconds = 24 * 60 * 60
)

// Precondition is one graph fact that must already exist in a given state
// before the intent is claimable and before it may be concluded.
type Precondition struct {
	RefKind   Kind   `json:"ref_kind"`
	Ref       string `json:"ref"`
	NeedState State  `json:"need_state"`
}

// Yield declares one node kind a concluded intent is expected to produce.
type Yield struct {
	Kind Kind `json:"kind"`
}

// Contract is the optional "contract" object inside an intent payload.
// A zero contract means "no preconditions, no confirmation, produces a fact".
type Contract struct {
	Version              int            `json:"version,omitempty"`
	Preconditions        []Precondition `json:"preconditions,omitempty"`
	Yields               []Yield        `json:"yields,omitempty"`
	TimeboxSeconds       int            `json:"timebox_s,omitempty"`
	RequiresConfirmation bool           `json:"requires_confirmation,omitempty"`
	ConfirmedAt          string         `json:"confirmed_at,omitempty"`
	ConfirmedBy          string         `json:"confirmed_by,omitempty"`
}

// intentPayload is the minimal payload shape needed to read the contract.
// Other payload fields (task_id, target, phase, ...) are intentionally not
// decoded here: the contract reader must never depend on plan fields.
type intentPayload struct {
	Contract json.RawMessage `json:"contract,omitempty"`
}

// ParseContractStrict extracts the contract and rejects a malformed shape. An
// absent contract is not an error (legacy intent); a present but unparseable
// one is, because silently treating it as "no contract" would disable the
// confirmation/precondition gates.
func ParseContractStrict(payload string) (Contract, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return Contract{}, nil
	}
	if !strings.HasPrefix(payload, "{") {
		return Contract{}, fmt.Errorf("contract: payload is not a JSON object")
	}
	var p intentPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return Contract{}, fmt.Errorf("contract: payload is not valid JSON: %w", err)
	}
	raw := strings.TrimSpace(string(p.Contract))
	if raw == "" || raw == "null" {
		return Contract{}, nil
	}
	var c Contract
	if err := json.Unmarshal(p.Contract, &c); err != nil {
		return Contract{}, fmt.Errorf("contract: malformed object: %w", err)
	}
	if err := ValidateContract(c); err != nil {
		return Contract{}, err
	}
	return c, nil
}

// ParseContract is the lenient display/checker helper: malformed contracts
// yield a zero value. Write paths must use ParseContractStrict instead.
func ParseContract(payload string) Contract {
	c, err := ParseContractStrict(payload)
	if err != nil {
		return Contract{}
	}
	return c
}

// ValidateContract rejects structurally invalid contracts. It is called by
// the write paths (claim/conclude) so a malformed contract can never become
// silently unenforceable.
func ValidateContract(c Contract) error {
	if c.Version != 0 && c.Version != ContractVersion {
		return fmt.Errorf("contract: unsupported version %d", c.Version)
	}
	if len(c.Preconditions) > MaxContractPreconditions {
		return fmt.Errorf("contract: too many preconditions (%d > %d)", len(c.Preconditions), MaxContractPreconditions)
	}
	for i, pre := range c.Preconditions {
		if !preconditionKind(pre.RefKind) {
			return fmt.Errorf("contract: precondition[%d]: invalid ref_kind %q", i, pre.RefKind)
		}
		if strings.TrimSpace(pre.Ref) == "" || len(pre.Ref) > MaxContractRefLen {
			return fmt.Errorf("contract: precondition[%d]: invalid ref", i)
		}
		if !ValidState(pre.RefKind, pre.NeedState) {
			return fmt.Errorf("contract: precondition[%d]: state %q is not valid for kind %q", i, pre.NeedState, pre.RefKind)
		}
	}
	if len(c.Yields) > MaxContractYields {
		return fmt.Errorf("contract: too many yields (%d > %d)", len(c.Yields), MaxContractYields)
	}
	for i, y := range c.Yields {
		if y.Kind != KindFact && y.Kind != KindFinding {
			return fmt.Errorf("contract: yield[%d]: kind %q must be fact or finding", i, y.Kind)
		}
	}
	if c.TimeboxSeconds < 0 || c.TimeboxSeconds > MaxContractTimeboxSeconds {
		return fmt.Errorf("contract: timebox_s out of range")
	}
	if c.ConfirmedAt != "" {
		if _, err := time.Parse(time.RFC3339, c.ConfirmedAt); err != nil {
			return fmt.Errorf("contract: confirmed_at is not RFC3339")
		}
	}
	if len(c.ConfirmedBy) > MaxContractRefLen {
		return fmt.Errorf("contract: confirmed_by is too long")
	}
	return nil
}

// Confirmed reports whether the operator confirmation gate is satisfied.
func (c Contract) Confirmed() bool {
	return strings.TrimSpace(c.ConfirmedAt) != ""
}

// NeedsConfirmation reports whether the intent is gated on operator approval.
func (c Contract) NeedsConfirmation() bool {
	return c.RequiresConfirmation && !c.Confirmed()
}

// YieldKinds returns the declared output kinds, defaulting to fact so a
// concluded intent always has a structural receipt.
func (c Contract) YieldKinds() []Kind {
	if len(c.Yields) == 0 {
		return []Kind{KindFact}
	}
	out := make([]Kind, 0, len(c.Yields))
	for _, y := range c.Yields {
		out = append(out, y.Kind)
	}
	return out
}

func preconditionKind(k Kind) bool {
	switch k {
	case KindFact, KindBelief, KindIntent:
		return true
	default:
		return false
	}
}

// ValidateProducedYields reports whether the produced nodes satisfy the kinds
// the contract declared. An empty declaration defaults to fact (the mandatory
// origin receipt), so a normal done intent satisfies it. This is the first
// production consumer of Contract.Yields: a declared finding cannot be
// satisfied by an origin receipt alone.
func ValidateProducedYields(c Contract, produced []NodeInput) error {
	have := make(map[Kind]bool, len(produced))
	for i := range produced {
		k := produced[i].Kind
		if k == "" {
			k = KindFact
		}
		have[k] = true
	}
	for _, want := range c.YieldKinds() {
		if !have[want] {
			return fmt.Errorf("%w: declared %s", ErrYieldUnmet, want)
		}
	}
	return nil
}
