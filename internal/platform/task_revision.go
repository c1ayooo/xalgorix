package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// TaskRunRevisionKey is the tasks.context key holding the runnable-content
// revision the latest accepted dispatch consumed. It is an internal marker,
// never part of the wire Task (Context is json:"-").
const TaskRunRevisionKey = "run_revision"

// taskContentSnapshot is the canonical set of task fields that decide WHAT a
// run does. Revisions hash exactly this snapshot: changing goal, instruction,
// structured objective, phase policy, context files, the model chain or the
// egress proxy pin means "there is new work to run"; renaming the task, retiming its schedule,
// archiving it or flipping the HITL audit switch does not.
//
// The field set is deliberately explicit (not reflected) so a new task field
// cannot silently join the revision without a decision here.
type taskContentSnapshot struct {
	Goal         string       `json:"goal"`
	Instruction  string       `json:"instruction"`
	Objective    *Objective   `json:"objective,omitempty"`
	PhasePolicy  *PhasePolicy `json:"phase_policy,omitempty"`
	ContextPaths []string     `json:"context_paths,omitempty"`
	LLMChain     []string     `json:"llm_chain,omitempty"`
	ActiveLLM    string       `json:"active_llm,omitempty"`
	// Proxy is the task-level egress pin ("" inherit, "direct", pool index).
	// It changes where the run leaves, so it is runnable content: switching it
	// makes the task "have something new to run" like a goal/policy edit.
	Proxy string `json:"proxy,omitempty"`
}

// TaskContentRevision returns a stable digest of the task's runnable content.
// Two tasks with identical runnable content hash identically, which is what
// makes "the run already consumed this content" a content fact rather than a
// timestamp race (a no-op PATCH that rewrites the same text keeps the same
// revision).
func TaskContentRevision(t Task) string {
	snap := taskContentSnapshot{
		Goal:         strings.TrimSpace(t.Goal),
		Instruction:  strings.TrimSpace(t.Instruction),
		Objective:    t.Objective,
		PhasePolicy:  t.PhasePolicy,
		ContextPaths: normalizeContextPaths(t.ContextPaths),
		LLMChain:     append([]string(nil), t.LLMChain...),
		ActiveLLM:    strings.TrimSpace(t.ActiveLLM),
		Proxy:        NormalizeTaskProxy(t.Proxy),
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		// The snapshot is plain strings/slices/structs; a marshal error is
		// unreachable. An empty revision makes the gate fail open (run
		// allowed), which is the safe direction: it never locks a task out.
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// DispatchedRunRevision returns the revision recorded when the task's latest
// dispatch was accepted, or "" when none was recorded (legacy/imported runs,
// pre-migration tasks, or a failed marker write).
func DispatchedRunRevision(t Task) string {
	if t.Context == nil {
		return ""
	}
	raw, _ := t.Context[TaskRunRevisionKey].(string)
	return strings.TrimSpace(raw)
}
