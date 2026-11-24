package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

func TestRegistryToolInfoAndInvokableRun(t *testing.T) {
	reg := tools.NewRegistry()
	var captured map[string]string
	calls := 0
	reg.Register(&tools.Tool{
		Name:        "echo",
		Description: "Echo arguments",
		Parameters:  []tools.Parameter{{Name: "q", Description: "query", Required: true}},
		Execute: func(args map[string]string) (tools.Result, error) {
			calls++
			captured = args
			return tools.Result{Success: true, Output: "ok"}, nil
		},
	})
	adapters, err := RegistryTools(reg)
	if err != nil || len(adapters) != 1 {
		t.Fatalf("RegistryTools = %d, %v", len(adapters), err)
	}
	info, err := adapters[0].Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "echo" || info.Desc != "Echo arguments" || info.ParamsOneOf == nil {
		t.Fatalf("tool info = %+v", info)
	}
	out, err := adapters[0].InvokableRun(context.Background(),
		`{"q":"needle","n":42,"b":true,"obj":{"a":1},"z":null}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" || calls != 1 {
		t.Fatalf("run = %q, calls=%d", out, calls)
	}
	if captured["q"] != "needle" || captured["n"] != "42" || captured["b"] != "true" {
		t.Fatalf("argument conversion = %+v", captured)
	}
	if _, exists := captured["z"]; exists {
		t.Fatalf("null argument should be omitted: %+v", captured)
	}
	var nested map[string]any
	if err := json.Unmarshal([]byte(captured["obj"]), &nested); err != nil || nested["a"] != float64(1) {
		t.Fatalf("structured argument = %q (%v)", captured["obj"], err)
	}
}

func TestRegistryToolHardTimeoutForceReturnsForUncooperativeTool(t *testing.T) {
	oldGrace := toolTimeoutGrace
	toolTimeoutGrace = 20 * time.Millisecond
	t.Cleanup(func() { toolTimeoutGrace = oldGrace })
	reg := tools.NewRegistry()
	release := make(chan struct{})
	reg.Register(&tools.Tool{
		Name: "blocked", Description: "ignores ctx",
		Execute: func(map[string]string) (tools.Result, error) {
			<-release
			return tools.Result{Output: "late"}, nil
		},
	})
	timedOut := 0
	adapters, err := registryTools(reg, func(string) time.Duration { return 10 * time.Millisecond }, func(string) { timedOut++ })
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = adapters[0].InvokableRun(context.Background(), `{}`)
	if err == nil || !strings.Contains(err.Error(), "hard timeout") {
		close(release)
		t.Fatalf("hard timeout = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		close(release)
		t.Fatalf("hard timeout took %s", elapsed)
	}
	if timedOut != 1 {
		close(release)
		t.Fatalf("timeout callback calls = %d", timedOut)
	}
	close(release)
}

func TestRegistryToolFailureSemantics(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&tools.Tool{
		Name: "soft-fail", Description: "fails",
		Execute: func(map[string]string) (tools.Result, error) {
			return tools.Result{Success: false, Error: "blocked by policy"}, nil
		},
	})
	reg.Register(&tools.Tool{
		Name: "hard-fail", Description: "errors",
		Execute: func(map[string]string) (tools.Result, error) {
			return tools.Result{}, context.DeadlineExceeded
		},
	})
	adapters, err := RegistryTools(reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapters[0].InvokableRun(context.Background(), `{}`); err == nil || !strings.Contains(err.Error(), "hard-fail") {
		t.Fatalf("hard failure = %v", err)
	}
	if _, err := adapters[1].InvokableRun(context.Background(), `{}`); err == nil || !strings.Contains(err.Error(), "blocked by policy") {
		t.Fatalf("soft failure = %v", err)
	}
}

// TestRegistryToolInfoParameterTypes pins the tools.Parameter.Type -> Eino
// JSON-schema mapping used for native tool calling. The zero value must stay
// "string" (so every pre-existing tool literal keeps its schema), enums are
// carried through, and array/object degrade to "string" because
// tools.Parameter has no element/sub-field metadata to describe their shape.
func TestRegistryToolInfoParameterTypes(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&tools.Tool{
		Name:        "typed",
		Description: "typed params",
		Parameters: []tools.Parameter{
			{Name: "plain", Required: true},
			{Name: "num", Type: "number"},
			{Name: "int", Type: "integer"},
			{Name: "flag", Type: "boolean"},
			{Name: "mode", Type: "string", Enum: []string{"a", "b"}, Default: "a"},
			{Name: "arr", Type: "array"},
			{Name: "obj", Type: "object"},
			{Name: "bogus", Type: "bogus"},
		},
	})
	adapters, err := RegistryTools(reg)
	if err != nil || len(adapters) != 1 {
		t.Fatalf("RegistryTools = %d, %v", len(adapters), err)
	}
	info, err := adapters[0].Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.ParamsOneOf == nil {
		t.Fatal("ParamsOneOf is nil")
	}
	js, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("ToJSONSchema: %v", err)
	}
	if js.Properties == nil {
		t.Fatal("no properties")
	}

	wantType := map[string]string{
		"plain": "string",
		"num":   "number",
		"int":   "integer",
		"flag":  "boolean",
		"mode":  "string",
		"arr":   "string", // degraded: no ElemInfo available
		"obj":   "string", // degraded: no SubParams available
		"bogus": "string", // unknown degrades to the safe default
	}
	for name, want := range wantType {
		prop, ok := js.Properties.Get(name)
		if !ok {
			t.Fatalf("property %q missing", name)
		}
		if prop.Type != want {
			t.Errorf("property %q type = %q, want %q", name, prop.Type, want)
		}
	}
	mode, _ := js.Properties.Get("mode")
	if len(mode.Enum) != 2 || mode.Enum[0] != "a" || mode.Enum[1] != "b" {
		t.Fatalf("mode enum = %v", mode.Enum)
	}
	plain, _ := js.Properties.Get("plain")
	if len(plain.Enum) != 0 {
		t.Fatalf("plain unexpectedly has enum %v", plain.Enum)
	}
	if len(js.Required) != 1 || js.Required[0] != "plain" {
		t.Fatalf("required = %v, want [plain]", js.Required)
	}
}
