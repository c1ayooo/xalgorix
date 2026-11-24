package browser

import (
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// TestBrowserActionSchemaMetadata pins the advisory type metadata added for
// native tool calling. It only describes the parameters browser_action already
// accepts; dispatch behavior is untouched.
func TestBrowserActionSchemaMetadata(t *testing.T) {
	reg := tools.NewRegistry()
	Register(reg)
	tool, ok := reg.Get("browser_action")
	if !ok {
		t.Fatal("browser_action not registered")
	}
	params := make(map[string]tools.Parameter, len(tool.Parameters))
	for _, p := range tool.Parameters {
		params[p.Name] = p
	}

	if got := params["command"].Type; got != "string" {
		t.Errorf("command type = %q", got)
	}
	if len(params["command"].Enum) == 0 {
		t.Fatal("command enum is empty")
	}
	actions := make(map[string]bool, len(params["command"].Enum))
	for _, a := range params["command"].Enum {
		actions[a] = true
	}
	for _, want := range []string{"launch", "goto", "snapshot", "click", "type", "verify_xss", "verify_path_template_xss", "discover_client_routes"} {
		if !actions[want] {
			t.Errorf("command enum missing %q", want)
		}
	}
	if got := params["timeout"].Type; got != "integer" {
		t.Errorf("timeout type = %q", got)
	}
	dir := params["direction"].Enum
	if len(dir) != 2 || dir[0] != "up" || dir[1] != "down" {
		t.Errorf("direction enum = %v", dir)
	}
	method := params["method"].Enum
	if len(method) != 2 || method[0] != "GET" || method[1] != "POST" {
		t.Errorf("method enum = %v", method)
	}
}
