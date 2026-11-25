package agent

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

const (
	// readToolOutputDefaultLimit is the default page size. It must stay below
	// maxToolResultBytes so a page is never re-truncated by the per-message cap.
	readToolOutputDefaultLimit = 14000
	// readToolOutputMaxLimit is the largest page a caller may request.
	readToolOutputMaxLimit = 14000
)

// registerToolArchiveTool registers read_tool_output — the retrieval side of
// bounded working context. Aged tool-result messages are replaced by stubs
// naming an archive id; this tool returns the archived original in bounded,
// byte-addressed pages so a large output (a 27 KB challenge listing, a JS
// bundle) can be read completely instead of being re-truncated on every read.
func (a *Agent) registerToolArchiveTool(reg *tools.Registry) {
	reg.Register(&tools.Tool{
		Name: "read_tool_output",
		Description: "Retrieve the original output of an earlier tool result that was archived to keep the working context small. " +
			"The stub message in the conversation names the archive id. Large outputs are returned in pages: call again with " +
			"offset=<next byte offset> to read the next page (the response prints the exact next call). The original file is also " +
			"readable directly at tool-outputs/<id> inside the scan workdir via terminal tools.",
		Parameters: []tools.Parameter{
			{Name: "id", Description: "Archive id of the tool output (e.g. to_000042), as shown in the archived stub message.", Required: true},
			{Name: "offset", Description: "Byte offset to start from (default 0). Use the value printed by the previous page's [next: ...] line.", Required: false},
			{Name: "limit", Description: fmt.Sprintf("Page size in bytes (default %d, max %d).", readToolOutputDefaultLimit, readToolOutputMaxLimit), Required: false},
		},
		Execute: a.readToolOutputTool,
	})
}

func (a *Agent) readToolOutputTool(args map[string]string) (tools.Result, error) {
	id := strings.TrimSpace(args["id"])
	if id == "" {
		return tools.Result{Error: "id is required — it appears in the archived stub message, e.g. read_tool_output(id=\"to_000042\")"}, nil
	}
	if a.scanCtx == nil || a.scanCtx.ToolOutputs == nil {
		return tools.Result{Error: "tool-output archive unavailable in this context"}, nil
	}
	content, ok := a.scanCtx.ToolOutputs.Get(id)
	if !ok {
		// State the archive's extent so the model can tell a typo apart from a
		// genuinely missing record instead of guessing ids (each miss feeds the
		// tool's circuit breaker).
		hint := "the archive holds no entries yet"
		if n := a.scanCtx.ToolOutputs.Count(); n > 0 {
			hint = fmt.Sprintf("the archive holds %d entries (highest id to_%06d)", n, n)
		}
		return tools.Result{Error: fmt.Sprintf(
			"no archived tool output with id %q — %s; copy the exact id from the stub (the to_ prefix and leading zeros are optional)",
			id, hint)}, nil
	}

	offset, err := pageInt(args["offset"], 0)
	if err != nil {
		return tools.Result{Error: "offset must be a non-negative integer byte offset"}, nil
	}
	limit, err := pageInt(args["limit"], readToolOutputDefaultLimit)
	if err != nil || limit <= 0 {
		return tools.Result{Error: fmt.Sprintf("limit must be a positive integer (max %d)", readToolOutputMaxLimit)}, nil
	}
	if limit > readToolOutputMaxLimit {
		limit = readToolOutputMaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	total := len(content)
	if offset == 0 && total <= limit {
		// Small outputs keep the historical byte-identical contract; paging
		// annotations only appear when the caller actually needs a page.
		return tools.Result{Output: content}, nil
	}
	if offset >= total {
		return tools.Result{Output: fmt.Sprintf(
			"[read_tool_output %s — offset %d is past the end (%d bytes); use offset 0..%d]",
			id, offset, total, max(0, total-1))}, nil
	}
	if offset > 0 {
		for offset < total && !utf8.RuneStart(content[offset]) {
			offset++
		}
	}
	end := offset + limit
	if end > total {
		end = total
	}
	chunk := content[offset:end]
	for len(chunk) > 0 && !utf8.ValidString(chunk) {
		chunk = chunk[:len(chunk)-1]
	}
	end = offset + len(chunk)

	var b strings.Builder
	fmt.Fprintf(&b, "[read_tool_output %s — bytes %d-%d of %d]\n", id, offset, end, total)
	b.WriteString(chunk)
	if end < total {
		fmt.Fprintf(&b, "\n[next: read_tool_output(id=%q, offset=%d, limit=%d)]", id, end, limit)
	} else {
		b.WriteString("\n[end of output]")
	}
	return tools.Result{Output: b.String()}, nil
}

// pageInt parses one optional paging argument.
func pageInt(raw string, def int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}
	return strconv.Atoi(raw)
}
