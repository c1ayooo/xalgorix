package httpclient

import (
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// TestHTTPRequestSchemaMetadata pins the advisory type metadata added for
// native tool calling. Only parameters that http_request actually reads are
// annotated; execution semantics are untouched.
func TestHTTPRequestSchemaMetadata(t *testing.T) {
	reg := tools.NewRegistry()
	Register(reg)
	tool, ok := reg.Get("http_request")
	if !ok {
		t.Fatal("http_request not registered")
	}
	params := make(map[string]tools.Parameter, len(tool.Parameters))
	for _, p := range tool.Parameters {
		params[p.Name] = p
	}

	wantTypes := map[string]string{
		"url":              "string",
		"method":           "string",
		"headers":          "string",
		"body":             "string",
		"follow_redirects": "boolean",
		"timeout":          "integer",
		"max_bytes":        "integer",
	}
	for name, want := range wantTypes {
		p, ok := params[name]
		if !ok {
			t.Fatalf("parameter %q missing", name)
		}
		if p.Type != want {
			t.Errorf("parameter %q type = %q, want %q", name, p.Type, want)
		}
	}
	if !params["url"].Required {
		t.Error("url must stay required")
	}
	gotMethod := params["method"].Enum
	wantMethod := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"}
	if len(gotMethod) != len(wantMethod) {
		t.Fatalf("method enum = %v", gotMethod)
	}
	for i := range wantMethod {
		if gotMethod[i] != wantMethod[i] {
			t.Fatalf("method enum = %v, want %v", gotMethod, wantMethod)
		}
	}
}
