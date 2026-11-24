package engine

// Dangling tool-call repair. Eino's tool node always writes a tool message for
// a dispatched call, but two real paths leave an assistant tool_call without a
// response in the history the next model request serializes:
//
//   - an approval interrupt suspends the run at the tool boundary, and a
//     resumed/compacted history can be built without the pending result;
//   - the scan's bounded-context compactor can drop a tool message while
//     keeping the assistant turn that requested it.
//
// A provider that receives a dangling tool_call either rejects the request or
// silently loses the turn, so the middleware repairs it with an explicit
// "not executed" result. When nothing is dangling the middleware returns the
// state messages untouched (same pointers, same bytes), so clean histories are
// never rewritten.

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/patchtoolcalls"
	"github.com/cloudwego/eino/schema"
)

// patchedToolCallMessage is intentionally bilingual: the model instructions are
// English but operators read the transcript, and the wording must make clear
// that the call did NOT run (it is not a tool failure to retry blindly).
const patchedToolCallMessage = "此工具调用 %q 未执行（审批中断或上下文压缩），请继续。\nThe tool call %q was NOT executed (approval interrupt or context compaction). Continue from the current plan; do not assume the tool ran and do not report findings from it."

// newPatchToolCallsMiddleware builds the repair handler installed in
// ChatModelAgentConfig.Handlers by NewRuntime.
func newPatchToolCallsMiddleware(ctx context.Context) (adk.ChatModelAgentMiddleware, error) {
	return patchtoolcalls.New(ctx, &patchtoolcalls.Config{
		PatchedToolResultGenerator: func(_ context.Context, toolName, _ string, _ *schema.ToolArgument) (*patchtoolcalls.PatchedToolResult, error) {
			return &patchtoolcalls.PatchedToolResult{
				Content: fmt.Sprintf(patchedToolCallMessage, toolName, toolName),
			}, nil
		},
	})
}
