package agent

import (
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

func TestToolSurfacePredicates(t *testing.T) {
	cases := []struct {
		surface ToolSurface
		name    string
		want    bool
	}{
		{SurfaceMain, "terminal_execute", false},
		{SurfaceMain, "http_request", false},
		{SurfaceMain, "report_vulnerability", false},
		{SurfaceMain, "read_ledger", true},
		{SurfaceMain, "list_findings", true},
		{SurfaceMain, "add_intent", true},
		{SurfaceMain, "finish", true},
		{SurfacePlanner, "terminal_execute", false},
		{SurfacePlanner, "report_vulnerability", false},
		{SurfacePlanner, "build_plan", true},
		{SurfacePlanner, "add_intent", true},
		{SurfacePlanner, "read_plan", true},
		{SurfaceWorker, "terminal_execute", true},
		{SurfaceWorker, "report_vulnerability", true},
		{SurfaceWorker, "update_plan", true},
		{SurfaceWorker, "spawn_agent", false},
		{SurfaceWorker, "build_plan", false},
		{SurfaceWorker, "finish", false},
		{SurfaceAnalysis, "terminal_execute", false},
		{SurfaceAnalysis, "python_action", false},
		{SurfaceAnalysis, "http_request", false},
		{SurfaceAnalysis, "browser_action", false},
		{SurfaceAnalysis, "port_scan", false},
		{SurfaceAnalysis, "read_notes", true},
		{SurfaceAnalysis, "add_note", true},
		{SurfaceAnalysis, "read_ledger", true},
		{SurfaceAnalysis, "cve_search", true},
		{SurfaceLegacyRoot, "terminal_execute", true},
	}
	for _, tc := range cases {
		if got := tc.surface.Allows(tc.name); got != tc.want {
			t.Fatalf("%s.Allows(%q) = %v, want %v", tc.surface, tc.name, got, tc.want)
		}
	}
}

func TestRegistryFilterAndRetain(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&tools.Tool{Name: "terminal_execute"})
	reg.Register(&tools.Tool{Name: "read_ledger"})
	reg.Register(&tools.Tool{Name: "build_plan"})
	reg.SetToolFilter(SurfaceMain.Allows)
	reg.Retain(SurfaceMain.Allows)
	if _, ok := reg.Get("terminal_execute"); ok {
		t.Fatal("terminal_execute survived the main surface")
	}
	if _, ok := reg.Get("build_plan"); ok {
		t.Fatal("build_plan survived the main surface")
	}
	if _, ok := reg.Get("read_ledger"); !ok {
		t.Fatal("read_ledger missing from the main surface")
	}
	// Registrations after the filter are also enforced.
	reg.Register(&tools.Tool{Name: "http_request"})
	if _, ok := reg.Get("http_request"); ok {
		t.Fatal("post-filter registration bypassed the surface")
	}
}

func TestNewAgentSurfaces(t *testing.T) {
	newAgent := func(surface ToolSurface) *Agent {
		return NewAgent(&config.Config{}, "surface-test", make(chan Event, 1),
			scopeguard.Config{BindAddr: "127.0.0.1"}, WithToolSurface(surface))
	}
	main := newAgent(SurfaceMain)
	if _, ok := main.registry.Get("terminal_execute"); ok {
		t.Fatal("main surface still has terminal_execute")
	}
	if _, ok := main.registry.Get("http_request"); ok {
		t.Fatal("main surface still has http_request")
	}
	if _, ok := main.registry.Get("report_vulnerability"); ok {
		t.Fatal("main surface still has report_vulnerability")
	}
	if _, ok := main.registry.Get("read_ledger"); !ok {
		t.Fatal("main surface lost read_ledger")
	}
	if _, ok := main.registry.Get("finish"); !ok {
		t.Fatal("main surface lost finish")
	}
	if _, ok := main.registry.Get("steer_work"); !ok {
		t.Fatal("main surface lost steer_work")
	}
	if _, ok := main.registry.Get("kill_work"); !ok {
		t.Fatal("main surface lost kill_work")
	}

	planner := newAgent(SurfacePlanner)
	if _, ok := planner.registry.Get("terminal_execute"); ok {
		t.Fatal("planner surface still has terminal_execute")
	}
	if _, ok := planner.registry.Get("report_vulnerability"); ok {
		t.Fatal("planner surface still has report_vulnerability")
	}
	if _, ok := planner.registry.Get("build_plan"); !ok {
		t.Fatal("planner surface lost build_plan")
	}

	worker := newAgent(SurfaceWorker)
	if _, ok := worker.registry.Get("terminal_execute"); !ok {
		t.Fatal("worker surface lost terminal_execute")
	}
	if _, ok := worker.registry.Get("report_vulnerability"); !ok {
		t.Fatal("worker surface lost report_vulnerability")
	}
	if _, ok := worker.registry.Get("spawn_agent"); ok {
		t.Fatal("worker surface still has spawn_agent")
	}
	if _, ok := worker.registry.Get("build_plan"); ok {
		t.Fatal("worker surface still has build_plan")
	}
	if _, ok := worker.registry.Get("finish"); ok {
		t.Fatal("worker surface still has finish")
	}
	if _, ok := worker.registry.Get("steer_work"); ok {
		t.Fatal("worker surface still has steer_work")
	}
	if _, ok := worker.registry.Get("kill_work"); ok {
		t.Fatal("worker surface still has kill_work")
	}
}
