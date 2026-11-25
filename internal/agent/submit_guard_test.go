package agent

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// platformGuardAgent builds a control-plane-lane agent with a configured
// platform host and a scan ledger.
func platformGuardAgent(t *testing.T, surface ToolSurface) *Agent {
	t.Helper()
	return &Agent{
		scanCtx:           scanctx.New("submit-guard-"+t.Name(), t.TempDir()),
		controlPlaneHosts: []string{"bench.example.com"},
		toolSurface:       surface,
	}
}

func submitCall(code, flag string) map[string]string {
	return map[string]string{
		"url":  "http://bench.example.com/openapi/v1/challenges/submit?unique_code=" + code,
		"body": `{"unique_code":"` + code + `","flag":"` + flag + `"}`,
	}
}

// TestSubmitGuardBlocksUnobservedValue pins the provenance rule: a flag that no
// tool in this run printed is refused (round 5 submitted 449 such values).
func TestSubmitGuardBlocksUnobservedValue(t *testing.T) {
	a := platformGuardAgent(t, SurfaceOrchestrator)
	blocked, reason := a.shouldBlockForPlatformDiscipline("http_request", submitCall("c-08", "flag{invented}"))
	if !blocked {
		t.Fatal("unobserved flag must be blocked")
	}
	if !strings.Contains(reason, "not observed") {
		t.Fatalf("reason = %q", reason)
	}
}

func TestSubmitGuardAllowsObservedValue(t *testing.T) {
	a := platformGuardAgent(t, SurfaceOrchestrator)
	a.observePlatformResult("terminal_execute", map[string]string{"command": "cat /flag"}, "flag{real-flag}")
	blocked, reason := a.shouldBlockForPlatformDiscipline("http_request", submitCall("c-08", "flag{real-flag}"))
	if blocked {
		t.Fatalf("observed flag was blocked: %s", reason)
	}
	if got := a.submitGuard().Attempts("c-08"); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestSubmitGuardRejectsProbeValues(t *testing.T) {
	a := platformGuardAgent(t, SurfaceOrchestrator)
	for _, code := range []string{"flag{probe}", "flag{r6c_probe}", "flag{contract_probe}", "dummy_flag"} {
		a.submitGuard().Observe(code) // even an observed probe is never a flag
		blocked, reason := a.shouldBlockForPlatformDiscipline("http_request", submitCall("c-08", code))
		if !blocked {
			t.Fatalf("%s must be blocked as a probe", code)
		}
		if !strings.Contains(reason, "probe") && !strings.Contains(reason, "placeholder") {
			t.Fatalf("reason for %s = %q", code, reason)
		}
	}
}

func TestSubmitGuardRejectsUnknownCatalogCode(t *testing.T) {
	a := platformGuardAgent(t, SurfaceOrchestrator)
	a.submitGuard().Observe(`{"challenges":[{"unique_code":"c-08","score":100}]}`)
	// Observed in output but absent from the catalog: an invented challenge id.
	a.submitGuard().Observe("trying unique_code=c-25 anyway")
	blocked, reason := a.shouldBlockForPlatformDiscipline("http_request", submitCall("c-25", "flag{x}"))
	if !blocked {
		t.Fatal("code outside the platform catalog must be blocked")
	}
	if !strings.Contains(reason, "catalog") {
		t.Fatalf("reason = %q", reason)
	}
	// A listed code with provenance still passes.
	a.submitGuard().Observe("flag{c-08}")
	blocked, reason = a.shouldBlockForPlatformDiscipline("http_request", submitCall("c-08", "flag{c-08}"))
	if blocked {
		t.Fatalf("catalogued code blocked: %s", reason)
	}
}

func TestSubmitGuardRejectsLoops(t *testing.T) {
	a := platformGuardAgent(t, SurfaceOrchestrator)
	a.observePlatformResult("terminal_execute", map[string]string{"command": "cat /flag"}, "flag{loop}")
	loop := map[string]string{
		"command": "for i in $(seq 1 40); do curl -s -X POST http://bench.example.com/openapi/v1/challenges/submit -d 'unique_code=c-08&flag=flag{loop}'; done",
	}
	blocked, reason := a.shouldBlockForPlatformDiscipline("terminal_execute", loop)
	if !blocked {
		t.Fatal("loop construct must be blocked")
	}
	if !strings.Contains(reason, "loop") {
		t.Fatalf("reason = %q", reason)
	}
	double := map[string]string{
		"command": "curl http://bench.example.com/openapi/v1/challenges/submit?unique_code=c-08&flag=flag{loop} && curl http://bench.example.com/openapi/v1/challenges/submit?unique_code=c-08&flag=flag{loop}",
	}
	if blocked, _ := a.shouldBlockForPlatformDiscipline("terminal_execute", double); !blocked {
		t.Fatal("two submits in one call must be blocked")
	}
}

// TestSubmitGuardKeepsLifecycleOnControlPlaneLane pins P0-3: an execution-lane
// worker cannot start/close/submit platform challenges.
func TestSubmitGuardKeepsLifecycleOnControlPlaneLane(t *testing.T) {
	worker := platformGuardAgent(t, SurfaceWorker)
	worker.observePlatformResult("terminal_execute", map[string]string{"command": "cat /flag"}, "flag{worker-flag}")
	blocked, reason := worker.shouldBlockForPlatformDiscipline("http_request", submitCall("c-08", "flag{worker-flag}"))
	if !blocked {
		t.Fatal("execution lane must not submit challenges")
	}
	if !strings.Contains(reason, "control-plane lane") {
		t.Fatalf("reason = %q", reason)
	}
	start := map[string]string{"url": "http://bench.example.com/openapi/v1/challenges/start?unique_code=c-08"}
	if blocked, _ := worker.shouldBlockForPlatformDiscipline("http_request", start); !blocked {
		t.Fatal("execution lane must not start challenges")
	}
	// Reads are the control-plane lane's job too: reconcile state is not
	// container-lane reconnaissance.
	read := map[string]string{"url": "http://bench.example.com/openapi/v1/challenges"}
	if blocked, _ := worker.shouldBlockForPlatformDiscipline("http_request", read); !blocked {
		t.Fatal("execution lane must not read the platform API")
	}
	if err := worker.checkHTTPHop("http://bench.example.com/openapi/v1/challenges"); err == nil {
		t.Fatal("worker lane must not reach the control-plane host over HTTP")
	}
	// The analysis lane has no network surface, but the guard still refuses if
	// a call reaches it.
	analysis := platformGuardAgent(t, SurfaceAnalysis)
	if blocked, _ := analysis.shouldBlockForPlatformDiscipline("http_request", start); !blocked {
		t.Fatal("analysis lane must not start challenges")
	}
	// Legacy CLI agents keep the historical full surface.
	legacy := platformGuardAgent(t, SurfaceLegacyRoot)
	legacy.observePlatformResult("terminal_execute", map[string]string{"command": "cat /flag"}, "flag{legacy-flag}")
	if blocked, reason := legacy.shouldBlockForPlatformDiscipline("http_request", submitCall("c-08", "flag{legacy-flag}")); blocked {
		t.Fatalf("legacy surface blocked: %s", reason)
	}
}

// TestSubmitGuardIgnoresTargetApplicationRoutes keeps the guard off ordinary
// target traffic: a scanned application may implement its own /challenges route.
func TestSubmitGuardIgnoresTargetApplicationRoutes(t *testing.T) {
	a := platformGuardAgent(t, SurfaceWorker)
	call := map[string]string{
		"url":  "http://10.0.175.121:8080/challenges/submit?unique_code=app-flag",
		"body": `{"code":"app-flag"}`,
	}
	if blocked, reason := a.shouldBlockForPlatformDiscipline("http_request", call); blocked {
		t.Fatalf("target-app route was treated as a platform call: %s", reason)
	}
	// No control-plane hosts configured (normal pentest run) => inert.
	plain := &Agent{scanCtx: scanctx.New("no-cp", ""), toolSurface: SurfaceOrchestrator}
	if blocked, _ := plain.shouldBlockForPlatformDiscipline("http_request", submitCall("c-08", "flag{x}")); blocked {
		t.Fatal("guard must be inert without a configured control-plane host")
	}
}

func TestSubmitGuardRejectsRepeatedSubmitAfterAcceptance(t *testing.T) {
	a := platformGuardAgent(t, SurfaceOrchestrator)
	a.observePlatformResult("terminal_execute", map[string]string{"command": "cat /flag"}, "flag{once}")
	call := submitCall("c-08", "flag{once}")
	if blocked, reason := a.shouldBlockForPlatformDiscipline("http_request", call); blocked {
		t.Fatalf("first submit blocked: %s", reason)
	}
	a.observePlatformResult("http_request", call, `{"correct": true, "score": 100}`)
	blocked, reason := a.shouldBlockForPlatformDiscipline("http_request", call)
	if !blocked {
		t.Fatal("repeat submit after acceptance must be blocked")
	}
	if !strings.Contains(reason, "already submitted") {
		t.Fatalf("reason = %q", reason)
	}
}

func TestSubmitGuardRequiresParseableCode(t *testing.T) {
	a := platformGuardAgent(t, SurfaceOrchestrator)
	blocked, reason := a.shouldBlockForPlatformDiscipline("http_request", map[string]string{
		"url": "http://bench.example.com/openapi/v1/challenges/submit",
	})
	if !blocked || !strings.Contains(reason, "unique_code") {
		t.Fatalf("blocked=%v reason=%q", blocked, reason)
	}
}

// TestSubmitGuardAcceptsRealCatalogCodeWithFlagLiteral is the regression test
// for the round-6 zero-score bug: the platform listing is a bare JSON array,
// the catalog must learn every code from it, and the submitted flag literal
// must never be checked against the code catalog.
func TestSubmitGuardAcceptsRealCatalogCodeWithFlagLiteral(t *testing.T) {
	a := platformGuardAgent(t, SurfaceOrchestrator)
	// Two start acks teach two codes; the full listing adds f1-04.
	a.submitGuard().Observe(`{"unique_code":"e1-01","container_addr":["10.0.189.8:80"]}`)
	a.submitGuard().Observe(`{"unique_code":"e1-02","container_addr":["10.0.189.9:80"]}`)
	a.submitGuard().Observe(`[{"unique_code":"e1-01","difficulty":"medium","total_score":250,"flag_count":1,"correct_flag_count":0,"is_completed":false,"container_status":"running","container_addr":["10.0.189.8:80"]},` +
		`{"unique_code":"f1-04","difficulty":"easy","total_score":100,"flag_count":1,"correct_flag_count":0,"is_completed":false,"container_status":"running","container_addr":["10.0.189.9:9013"]}]`)
	if got := a.submitGuard().CatalogSize(); got != 3 {
		t.Fatalf("catalog size = %d, want 3", got)
	}
	a.observePlatformResult("terminal_execute", map[string]string{"command": "cat /challenge/flag.txt"}, "flag{907600af-c8c1-4c5a-bded-84635df833dc}")
	call := submitCall("f1-04", "flag{907600af-c8c1-4c5a-bded-84635df833dc}")
	if blocked, reason := a.shouldBlockForPlatformDiscipline("http_request", call); blocked {
		t.Fatalf("catalogued code + observed flag was blocked: %s", reason)
	}
	if got := a.submitGuard().Attempts("f1-04"); got != 1 {
		t.Fatalf("attempts(f1-04) = %d, want 1", got)
	}
}
