package agent

// worker_control_tools.go — main-agent controls for the R7 worker pool.
//
// steer_work injects a correction into the active worker for one intent;
// kill_work cancels it. Both address workers by the authoritative unit id, so
// the main agent never needs the internal graph agent id.

import (
	"fmt"

	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

func (a *Agent) registerWorkerControlTools(reg *tools.Registry) {
	reg.Register(&tools.Tool{
		Name: "steer_work",
		Description: "Send a course correction to the worker currently executing a plan intent. " +
			"The worker receives it before its next model turn; it does not stop the run. " +
			"Use the plan task id (unit id) shown in the graph/plan.",
		Parameters: []tools.Parameter{
			{Name: "unit_id", Description: "Plan task id (intent unit) whose worker should receive the correction.", Required: true},
			{Name: "message", Description: "Concrete correction: what to stop, what to focus on, and why.", Required: true},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			unitID := args["unit_id"]
			msg := args["message"]
			if err := a.SteerWorker(unitID, msg); err != nil {
				return tools.Result{Output: "steer_work failed: " + err.Error()}, nil
			}
			return tools.Result{Output: fmt.Sprintf("Steered worker for %s before its next turn.", unitID)}, nil
		},
	})
	reg.Register(&tools.Tool{
		Name: "kill_work",
		Description: "Cancel the worker currently executing a plan intent. The scheduler settles that intent as blocked; " +
			"use this only when the worker is on the wrong direction or stuck. The engine refuses a stagnant kill while " +
			"the worker produced information-layer progress in the last two minutes.",
		Parameters: []tools.Parameter{
			{Name: "unit_id", Description: "Plan task id (intent unit) whose worker should be cancelled.", Required: true},
			{Name: "reason_code", Description: "Typed cause: human (default), stagnant, repeated_failure, lane_violation, scope_violation, duplicate, superseded.", Required: false},
			{Name: "reason", Description: "Why the worker is being cancelled (audited).", Required: false},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			unitID := args["unit_id"]
			reasonCode := args["reason_code"]
			reason := args["reason"]
			if err := a.KillWorkerWithReason(unitID, reasonCode, reason); err != nil {
				return tools.Result{Output: "kill_work failed: " + err.Error()}, nil
			}
			code, _ := normalizeKillReasonCode(reasonCode)
			return tools.Result{Output: fmt.Sprintf("Cancelled the worker for %s (reason_code=%s).", unitID, code)}, nil
		},
	})
}
