package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/tools"
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
