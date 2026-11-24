package agent

import (
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
)

func TestCheckHTTPHop_OrchestratorLaneOnlyControlPlane(t *testing.T) {
	a := &Agent{
		toolSurface:       SurfaceOrchestrator,
		controlPlaneHosts: []string{"tsecbench.zc.tencent.com", "10.0.100.58"},
		localGuard:        scopeguard.Config{},
	}
	if err := a.checkHTTPHop("https://tsecbench.zc.tencent.com/openapi/v1/challenges"); err != nil {
		t.Fatalf("control-plane host rejected: %v", err)
	}
	if err := a.checkHTTPHop("http://10.0.100.58"); err != nil {
		t.Fatalf("health/control host rejected: %v", err)
	}
	if err := a.checkHTTPHop("http://10.0.177.176:23/"); err == nil {
		t.Fatal("container target must be rejected in the orchestrator lane")
	}
	a.toolSurface = SurfaceWorker
	if err := a.checkHTTPHop("http://10.0.177.176:23/"); err != nil {
		t.Fatalf("worker lane should allow container target: %v", err)
	}
}
