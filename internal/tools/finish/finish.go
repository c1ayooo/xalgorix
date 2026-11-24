// Package finish provides the finish tool to end agent execution and the
// worker-level complete_work protocol.
package finish

import (
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// Register adds the root finish tool and the delegated-worker completion tool.
func Register(r *tools.Registry) {
	r.Register(&tools.Tool{
		Name:        "finish",
		Description: "Signal that the agent has completed its task. Provide a summary of findings.",
		Parameters: []tools.Parameter{
			{Name: "summary", Description: "Summary of what was done and key findings", Required: true},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			return tools.Result{
				Output:   args["summary"],
				Metadata: map[string]any{"finished": true},
			}, nil
		},
	})

	r.Register(&tools.Tool{
		Name:        "complete_work",
		Description: "Finish this delegated intent and return its result to the scheduler. Use when the assigned work is done or you cannot make further productive progress. Root scans must use finish instead. Declare an exit: none (with blocked_reason: the lane yielded no result), access:<fingerprint> (this lane produced that intel item) or finding:<id> (this lane reported that finding).",
		Parameters: []tools.Parameter{
			{Name: "summary", Description: "Short result summary: what was done, observed, and the final state", Required: true},
			{Name: "unfinished", Description: "What remains undone, if anything", Required: false},
			{Name: "partial_result", Description: "Partial result or checkpoint a follow-up intent can resume from", Required: false},
			{Name: "resumable", Description: "true if a follow-up can continue from this checkpoint", Required: false},
			{Name: "exit", Description: "none | access:<fingerprint> | finding:<id> (default none)", Required: false},
			{Name: "blocked_reason", Description: "Why the lane yielded nothing; required when exit=none", Required: false},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			exit, reason, errText := normalizeExit(args["exit"], args["blocked_reason"])
			if errText != "" {
				// A bad declaration is a model mistake, not an engine failure:
				// return it as Output (the registry treats Result.Error as a
				// failure and would open its circuit breaker) and withhold
				// work_completed so the lane keeps running and the model
				// corrects the call.
				return tools.Result{Output: "complete_work: " + errText}, nil
			}
			var b strings.Builder
			b.WriteString(strings.TrimSpace(args["summary"]))
			if v := strings.TrimSpace(args["unfinished"]); v != "" {
				b.WriteString("\nUnfinished: ")
				b.WriteString(v)
			}
			if v := strings.TrimSpace(args["partial_result"]); v != "" {
				b.WriteString("\nPartial result: ")
				b.WriteString(v)
			}
			if strings.EqualFold(strings.TrimSpace(args["resumable"]), "true") {
				b.WriteString("\nResumable: true")
			}
			b.WriteString("\nExit: ")
			b.WriteString(exit)
			if reason != "" {
				b.WriteString(" — ")
				b.WriteString(reason)
			}
			return tools.Result{
				Output: b.String(),
				Metadata: map[string]any{
					"work_completed": true,
					"exit":           exit,
					"blocked_reason": reason,
				},
			}, nil
		},
	})
}

// normalizeExit validates the worker's lane outcome (INTEL_HANDOFF_SPEC D6).
// The returned reason is only meaningful for exit=none; errText explains a
// malformed declaration.
func normalizeExit(rawExit, rawReason string) (exit, reason, errText string) {
	exit = strings.ToLower(strings.TrimSpace(rawExit))
	reason = strings.TrimSpace(rawReason)
	if exit == "" {
		exit = "none"
	}
	switch {
	case exit == "none":
		if reason == "" {
			return "", "", "exit=none requires blocked_reason (why did this lane yield nothing?)"
		}
		return exit, reason, ""
	case strings.HasPrefix(exit, "access:"):
		ref := strings.TrimSpace(strings.TrimPrefix(exit, "access:"))
		if ref == "" {
			return "", "", "exit=access:<fingerprint> requires a fingerprint"
		}
		return "access:" + ref, reason, ""
	case strings.HasPrefix(exit, "finding:"):
		ref := strings.TrimSpace(strings.TrimPrefix(exit, "finding:"))
		if ref == "" {
			return "", "", "exit=finding:<id> requires a finding id"
		}
		return "finding:" + ref, reason, ""
	default:
		return "", "", `exit must be "none", "access:<fingerprint>" or "finding:<id>"`
	}
}
