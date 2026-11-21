package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/xalgord/xalgorix/v4/internal/llm"
	"github.com/xalgord/xalgorix/v4/internal/tools"
	"github.com/xalgord/xalgorix/v4/internal/tools/reporting"
)

// timingCtxSeq makes per-test reporting context IDs unique across -count=N
// reruns of the timing tests.
var timingCtxSeq atomic.Int64

type parallelToolClient struct{ turns int }

func (c *parallelToolClient) ChatWithUsageToolCalls([]llm.Message, []llm.ToolDefinition) (string, []llm.ToolCall, *llm.TokenUsage, error) {
	c.turns++
	if c.turns == 1 {
		return "", []llm.ToolCall{
			{ID: "a", Name: "report_vulnerability", Args: map[string]string{"id": "a"}},
			{ID: "b", Name: "verify_finding", Args: map[string]string{"id": "b"}},
		}, nil, nil
	}
	return "done", nil, nil, nil
}

// TestRuntimeToolsNodeConfigPinsVerifierSerialExecution is the explicit
// binding regression: the runtime must construct its ToolsNodeConfig with
// ExecuteSequentially=true. A refactor that quietly parallelizes the tool node
// turns this red BEFORE the behavioral timing tests can flake on scheduling.
func TestRuntimeToolsNodeConfigPinsVerifierSerialExecution(t *testing.T) {
	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client: &parallelToolClient{}, Registry: tools.NewRegistry(),
		CheckpointStore: NewMemoryCheckpointStore(), Instruction: "test", MaxIterations: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rt.ToolsNodeConfig().ExecuteSequentially {
		t.Fatal("ToolsNodeConfig.ExecuteSequentially = false: parallel tool execution would race Gate 4.5's verifier turns on the shared LLM client (verifier.go timing invariant)")
	}
}

func TestRuntimeExecutesToolsSequentiallyForVerifierSafety(t *testing.T) {
	reg := tools.NewRegistry()
	var active, maxActive int32
	run := func(args map[string]string) (tools.Result, error) {
		n := atomic.AddInt32(&active, 1)
		for {
			old := atomic.LoadInt32(&maxActive)
			if n <= old || atomic.CompareAndSwapInt32(&maxActive, old, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return tools.Result{Success: true, Output: args["id"]}, nil
	}
	reg.Register(&tools.Tool{Name: "report_vulnerability", Description: "report", Execute: run})
	reg.Register(&tools.Tool{Name: "verify_finding", Description: "verify", Execute: run})

	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client: &parallelToolClient{}, Registry: reg, CheckpointStore: NewMemoryCheckpointStore(),
		Instruction: "test", MaxIterations: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "timing-cp"))
	if got := atomic.LoadInt32(&maxActive); got != 1 {
		t.Fatalf("concurrent tools peaked at %d, want exactly 1 (Verifier safety)", got)
	}
}

// TestGate45VerifierChainStaysSerialInsideToolNode extends the timing gate to
// the REAL report_vulnerability pipeline: reporting.RegisterWithVerifier wires
// Gate 4.5 into the registry, so the tool node's report_vulnerability call
// blocks while the independent verifier runs its own LLM turn (scripted
// server), and only then does the next tool execute. The maxActive counter
// wraps only the top-level tool executions (the verifier window runs INSIDE
// report_vulnerability and is not counted twice); a parallel tool node would
// overlap the two top-level windows and push it past 1. The report-gate
// window's seriality is additionally pinned by
// TestRuntimeToolsNodeConfigPinsVerifierSerialExecution and, end to end on
// the shared client, by the agent-package server-side maxInFlight==1 tests.
// The verdict must also land: the persisted finding is Verified with the
// verifier's evidence appended to its proof (REDESIGN.md R4 acceptance
// "Verifier 时序测试").
func TestGate45VerifierChainStaysSerialInsideToolNode(t *testing.T) {
	// Unique per run: -count=2 reruns this test against the same process, and
	// the reporting store dedups by context — reuse would collide with the
	// first run's persisted finding. Cleanup matches the agent-package
	// discipline (reporting.CleanupContext).
	ctxID := fmt.Sprintf("%s-%d", t.Name(), timingCtxSeq.Add(1))
	t.Cleanup(func() { reporting.CleanupContext(ctxID) })
	var active, maxActive int32
	enter := func() {
		n := atomic.AddInt32(&active, 1)
		for {
			old := atomic.LoadInt32(&maxActive)
			if n <= old || atomic.CompareAndSwapInt32(&maxActive, old, n) {
				break
			}
		}
	}
	leave := func() { atomic.AddInt32(&active, -1) }

	verifierRan := atomic.Bool{}
	verifierTurns := atomic.Int32{}
	verifierSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		verifierTurns.Add(1)
		// Widen the window: if anything else executed concurrently with the
		// verifier's in-flight turn, the peak-active counter would expose it.
		time.Sleep(40 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"verdict": "confirmed"})
	}))
	defer verifierSrv.Close()

	verifier := func(reporting.VerificationRequest) reporting.VerificationVerdict {
		verifierRan.Store(true)
		// The verifier's LLM turn is a blocking provider request issued from
		// INSIDE the report_vulnerability tool execution, exactly like
		// Agent.verifyFinding's ChatWithUsage calls.
		resp, err := http.Post(verifierSrv.URL+"/v1/verifier-turn", "application/json",
			strings.NewReader(`{"title":"gate-4.5 timing probe"}`))
		if err != nil {
			return reporting.VerificationVerdict{Inconclusive: true, Reason: "verifier transport error: " + err.Error()}
		}
		defer resp.Body.Close()
		var body struct {
			Verdict string `json:"verdict"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Verdict != "confirmed" {
			return reporting.VerificationVerdict{Inconclusive: true, Reason: "verifier scripted turn failed"}
		}
		return reporting.VerificationVerdict{
			Confirmed: true,
			Reason:    "independently reproduced with a negative control",
			Evidence:  "independent control comparison matched the reported behavior",
		}
	}

	reg := tools.NewRegistry()
	reg.SetScanContextID(ctxID)
	reporting.RegisterWithVerifier(reg, verifier)
	reg.Register(&tools.Tool{
		Name: "verify_finding", Description: "probe tool",
		Execute: func(args map[string]string) (tools.Result, error) {
			enter()
			defer leave()
			time.Sleep(30 * time.Millisecond)
			return tools.Result{Success: true, Output: args["id"]}, nil
		},
	})

	client := &gate45Client{turns: 0}
	rt, err := NewRuntime(context.Background(), RuntimeConfig{
		Client: client, Registry: reg, CheckpointStore: NewMemoryCheckpointStore(),
		Instruction: "test", MaxIterations: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	drainEvents(rt.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}, "gate45-cp"))

	if got := atomic.LoadInt32(&maxActive); got != 1 {
		t.Fatalf("tool executions (incl. the Gate 4.5 verifier window) peaked at %d, want exactly 1", got)
	}
	if !verifierRan.Load() {
		t.Fatal("Gate 4.5 never invoked the verifier")
	}
	if got := verifierTurns.Load(); got != 1 {
		t.Fatalf("verifier scripted turn count = %d, want exactly 1", got)
	}
	if client.turns != 2 {
		t.Fatalf("model turns = %d, want 2 (one tool turn + one completion turn)", client.turns)
	}

	// The verdict landed: the finding is persisted as Verified with the
	// verifier's evidence folded into the proof.
	vulns := reporting.GetVulnerabilitiesForContext(ctxID)
	if len(vulns) != 1 {
		t.Fatalf("persisted findings = %d, want 1 (verdict did not land): %+v", len(vulns), vulns)
	}
	v := vulns[0]
	if !v.Verified {
		t.Fatalf("finding Verified = false, want true (confirmed verdict must persist as verified): %+v", v)
	}
	if len(v.Tags) == 0 || v.Tags[0] != reporting.TagVerified {
		t.Fatalf("finding tags = %v, want [%s]", v.Tags, reporting.TagVerified)
	}
	if !strings.Contains(v.ExploitationProof, "independent control comparison matched") {
		t.Fatalf("verifier evidence was not appended to the proof: %q", v.ExploitationProof)
	}
}

// gate45Client returns a report_vulnerability call plus a probe tool call in
// one turn (the ADK must serialize them), then a plain completion.
type gate45Client struct{ turns int }

func (c *gate45Client) ChatWithUsageToolCalls([]llm.Message, []llm.ToolDefinition) (string, []llm.ToolCall, *llm.TokenUsage, error) {
	c.turns++
	if c.turns == 1 {
		return "", []llm.ToolCall{
			{
				ID: "r1", Name: "report_vulnerability",
				Args: map[string]string{
					"title":               "Reproduced order-record exposure on /api/orders",
					"severity":            "low",
					"verification_method": "exploited",
					"exploitation_proof":  "reproduced on demand: requesting /api/orders?id=2 returned order records for a different account (id 42, total 99.50) while the uncrafted request returned only the caller's own records",
					"description":         "Order lookup discloses other accounts' records when the id parameter is crafted.",
				},
			},
			{ID: "p1", Name: "verify_finding", Args: map[string]string{"id": "probe"}},
		}, nil, nil
	}
	return "done", nil, nil, nil
}
