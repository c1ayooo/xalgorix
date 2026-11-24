package terminal

import (
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// TestTerminalExecuteSchemaMetadata pins the advisory type metadata added for
// native tool calling. Execution is unchanged: the registry still passes
// strings, so this only guards the documented schema.
func TestTerminalExecuteSchemaMetadata(t *testing.T) {
	reg := tools.NewRegistry()
	Register(reg)
	tool, ok := reg.Get("terminal_execute")
	if !ok {
		t.Fatal("terminal_execute not registered")
	}
	if len(tool.Parameters) != 1 {
		t.Fatalf("terminal_execute parameters = %d, want 1", len(tool.Parameters))
	}
	p := tool.Parameters[0]
	if p.Name != "command" || p.Type != "string" || !p.Required {
		t.Fatalf("command parameter = %+v", p)
	}
}
