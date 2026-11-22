package agent

// Eino-path Verifier timing evidence (REDESIGN.md R4 acceptance). The legacy
// counterparts live in verifier_deadline_test.go; these run the SAME real
// chain — main loop blocked inside report_vulnerability while the shared
// client serves the verifier's own LLM turns — through the eino ADK runtime:
//
//   - the whole chain stays serial: the scripted server never sees two
//     in-flight requests, and the request kinds arrive in the exact
//     main→verifier→verifier→main order Gate 4.5 implies;
//   - a confirmed verdict lands as a persisted Verified finding with the
//     verifier's evidence folded into the proof;
//   - an expired verifier deadline cancels the in-flight request, preserves
//     the finding (inconclusive is kept, never dropped), and the client
//     context is restored so the main loop's NEXT request still succeeds.
//
// Verifier timing red lines hold: verifier.go's verdict logic, tool surface
// and budgets are untouched; reporting.go's gate order is untouched. Note on
// temperature: verifyFinding deliberately pins TempValidator for its turns and
// does not restore it afterwards — on legacy the per-role switch after
// report_vulnerability overwrites it, and on eino the resulting 0.0 equals the
// deterministic scanner default, so neither engine observes pollution. Only
// the CONTEXT restore is load-bearing, and it is asserted here.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/engine"
	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/reporting"
)

// verifierChainServer is one scripted provider for BOTH the main loop and the
// verifier turns on the shared client. Chat-completions requests are
// classified by whether they carry a tools array: main-loop turns do,
// verifier turns (ChatWithUsage) do not. It records the request-kind order and
// the peak number of in-flight requests, so a test can assert that the
// verifier's turns never overlap a main-loop turn and never run concurrently.
type verifierChainServer struct {
	srv *httptest.Server
	URL string

	kinds       []string
	inFlight    int32
	maxInFlight int32
	mu          sync.Mutex

	// hangVerifierTurns replaces the FIRST verifier turn with a handler that
	// hangs until the client's deadline cancels it (deadline test mode).
	hangVerifierTurns bool
	hangArmed         int32

	// nextMainResponse / nextVerifierTurn script the two turn kinds.
	nextMainResponse func(mainTurn int) map[string]any
	nextVerifierTurn func(verifierTurn int) string

	mainTurns     int
	verifierTurns int
}

func newVerifierChainServer(t *testing.T) *verifierChainServer {
	t.Helper()
	vc := &verifierChainServer{}
	vc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			// Plain target-shaped endpoint (the verifier's http_request re-test).
			vc.record("retest")
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("retest-ok: control response"))
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var probe struct {
			Tools []any `json:"tools"`
		}
		_ = json.Unmarshal(body, &probe)
		isMain := len(probe.Tools) > 0

		if !isMain && vc.hangVerifierTurns && atomic.CompareAndSwapInt32(&vc.hangArmed, 0, 1) {
			// Verifier turn: hang until the client deadline cancels the
			// transport request. net/http starts watching for client
			// cancellation only after the request body has been drained to
			// EOF, which the io.ReadAll above already did. Later no-tools
			// requests (the verifyFinding retry and the post-run context
			// probe) are answered normally.
			<-r.Context().Done()
			vc.record("verifier-hang")
			return
		}
		if !isMain && vc.hangVerifierTurns {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}},
			})
			return
		}

		// Overlap detector: any concurrent request means the main loop or the
		// tool node ran something in parallel with an in-flight turn.
		n := atomic.AddInt32(&vc.inFlight, 1)
		for {
			old := atomic.LoadInt32(&vc.maxInFlight)
			if n <= old || atomic.CompareAndSwapInt32(&vc.maxInFlight, old, n) {
				break
			}
		}
		defer atomic.AddInt32(&vc.inFlight, -1)
		time.Sleep(15 * time.Millisecond) // widen the overlap window

		w.Header().Set("Content-Type", "application/json")
		if isMain {
			vc.record("main")
			vc.mainTurns++
			_ = json.NewEncoder(w).Encode(vc.nextMainResponse(vc.mainTurns))
			return
		}
		vc.record("verifier")
		vc.verifierTurns++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": vc.nextVerifierTurn(vc.verifierTurns)},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	t.Cleanup(vc.srv.Close)
	vc.URL = vc.srv.URL
	return vc
}

func (vc *verifierChainServer) record(kind string) {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	vc.kinds = append(vc.kinds, kind)
}

func (vc *verifierChainServer) order() []string {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	return append([]string(nil), vc.kinds...)
}

// goldenScriptedToolCall builds an OpenAI-shape response carrying one native
// tool call (shared with the golden scheduling parity test's server shape).
func goldenScriptedToolCall(seq int, name string, args map[string]string) map[string]any {
	argsJSON, _ := json.Marshal(args)
	return map[string]any{
		"choices": []map[string]any{{
			"message": map[string]any{
				"role": "assistant",
				"tool_calls": []map[string]any{{
					"id":       "call_" + strconv.Itoa(seq),
					"type":     "function",
					"function": map[string]any{"name": name, "arguments": string(argsJSON)},
				}},
			},
			"finish_reason": "tool_calls",
		}},
	}
}

// newEinoVerifierAgent wires an eino-mode agent with its own scan context,
// checkpoint store and the shared scripted client. The registry's verifier is
// the REAL Agent.verifyFinding (wired by NewAgent via
// reporting.RegisterWithVerifier), so these tests exercise the production
// Gate 4.5 chain, not a stub.
func newEinoVerifierAgent(t *testing.T, srvURL string) (*Agent, *scanctx.ScanContext, chan Event) {
	t.Helper()
	sc := scanctx.New("eino-verifier-"+t.Name(), t.TempDir())
	scanctx.Activate(sc)
	t.Cleanup(func() {
		scanctx.Deactivate(sc.ID)
		reporting.CleanupContext(sc.ID)
		sc.Close()
	})

	cfg := &config.Config{
		LLM:                 "openai/gpt-test",
		APIBase:             srvURL,
		APIKey:              "sk-test",
		MaxIterations:       6,
		MinIterations:       1,
		DisableAutoDelegate: true,
	}
	events := make(chan Event, 256)
	store, err := engine.NewFileCheckpointStore(t.TempDir())
	if err != nil {
		t.Fatalf("checkpoint store: %v", err)
	}
	guard := scopeguard.Config{BindAddr: "127.0.0.1", Port: 0, AllowLocalTargets: true}
	ag := NewAgent(cfg, "eino-verifier", events, guard, sc,
		WithLLMClient(llm.NewClient(cfg)), WithRuntime(store, nil))
	t.Cleanup(ag.Stop)
	ag.state = goldenSeedState(t, sc)
	return ag, sc, events
}

const verifierProbeProof = "reproduced on demand: requesting /api/orders?id=2 returned order records for a different account (id 42, total 99.50) while the uncrafted request returned only the caller's own records"

func verifierProbeReport(mainTurn int) map[string]any {
	return goldenScriptedToolCall(mainTurn, "report_vulnerability", map[string]string{
		"title":               "Reproduced order-record exposure on /api/orders",
		"severity":            "low",
		"verification_method": "exploited",
		"exploitation_proof":  verifierProbeProof,
		"description":         "Order lookup discloses other accounts' records when the id parameter is crafted.",
	})
}

func verifierProbeFinish(mainTurn int) map[string]any {
	return goldenScriptedToolCall(mainTurn, "finish", map[string]string{"summary": "verifier chain probe complete"})
}

func collectAgentEvents(events chan Event) []Event {
	var out []Event
	for ev := range events {
		out = append(out, ev)
	}
	return out
}

// TestEinoGate45VerifierChainStaysSerialAndVerdictLands drives the REAL
// pipeline — eino runtime → report_vulnerability → Agent.verifyFinding →
// http_request re-test → submit_verdict — on one shared scripted server, and
// asserts the timing invariant plus the persisted verdict.
func TestEinoGate45VerifierChainStaysSerialAndVerdictLands(t *testing.T) {
	vc := newVerifierChainServer(t)
	evidence := "independent control comparison matched the reported behavior"
	vc.nextMainResponse = func(mainTurn int) map[string]any {
		switch mainTurn {
		case 1:
			// Turn 1 is a harmless bookkeeping call so the finish lands on
			// turn 3: the finish gate's iteration floor (iter < 3) rejects an
			// earlier finish, and a rejected finish legitimately costs one
			// recovery turn.
			return goldenScriptedToolCall(mainTurn, "read_ledger", map[string]string{"filter": "schedulable"})
		case 2:
			return verifierProbeReport(mainTurn)
		default:
			return verifierProbeFinish(mainTurn)
		}
	}
	vc.nextVerifierTurn = func(verifierTurn int) string {
		if verifierTurn == 1 {
			// First verifier turn: a real, successful independent re-test.
			return "<function=http_request><parameter=url>" + vc.URL + "/retest</parameter></function>"
		}
		return "<function=submit_verdict><parameter=verdict>confirmed</parameter>" +
			"<parameter=reason>independently reproduced with a negative control</parameter>" +
			"<parameter=evidence>" + evidence + "</parameter></function>"
	}

	ag, sc, events := newEinoVerifierAgent(t, vc.URL)
	ag.Run([]string{"https://target.example"}, "ctf drill: capture the flag on the isolated lab host")
	close(events)
	<-collectAgentEventsAsync(events)

	// Timing invariant: the whole chain is serial — the main loop blocked in
	// report_vulnerability while the verifier ran its turns on the shared
	// client; nothing ever overlapped.
	if got := atomic.LoadInt32(&vc.maxInFlight); got != 1 {
		t.Fatalf("peak in-flight LLM requests = %d, want exactly 1 (main loop must block inside report_vulnerability during Gate 4.5)", got)
	}
	wantOrder := []string{"main", "main", "verifier", "retest", "verifier", "main"}
	gotOrder := vc.order()
	if len(gotOrder) != len(wantOrder) {
		t.Fatalf("request order = %v, want %v", gotOrder, wantOrder)
	}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("request order = %v, want %v (position %d: %s != %s)", gotOrder, wantOrder, i, gotOrder[i], wantOrder[i])
		}
	}

	// The verdict landed: persisted as Verified with the verifier's evidence.
	vulns := reporting.GetVulnerabilitiesForContext(sc.ID)
	if len(vulns) != 1 {
		t.Fatalf("persisted findings = %d, want 1: %+v", len(vulns), vulns)
	}
	v := vulns[0]
	if !v.Verified {
		t.Fatalf("finding Verified = false, want true: %+v", v)
	}
	if len(v.Tags) == 0 || v.Tags[0] != reporting.TagVerified {
		t.Fatalf("finding tags = %v, want [%s]", v.Tags, reporting.TagVerified)
	}
	if !strings.Contains(v.ExploitationProof, evidence) {
		t.Fatalf("verifier evidence missing from the persisted proof: %q", v.ExploitationProof)
	}
}

func collectAgentEventsAsync(events chan Event) chan []Event {
	out := make(chan []Event, 1)
	go func() { out <- collectAgentEvents(events) }()
	return out
}

// TestEinoPathVerifierRestoresClientContext mirrors
// TestVerifierRestoresClientContext through the eino loop: a hung provider
// request must be canceled at the verifier deadline, the candidate finding
// must survive as kept-for-review, and the shared client's context must be
// restored so the main loop's very next request (the finish turn) succeeds.
func TestEinoPathVerifierRestoresClientContext(t *testing.T) {
	oldDeadline := verifierDeadline
	verifierDeadline = 250 * time.Millisecond
	t.Cleanup(func() { verifierDeadline = oldDeadline })

	vc := newVerifierChainServer(t)
	vc.hangVerifierTurns = true
	vc.nextMainResponse = func(mainTurn int) map[string]any {
		if mainTurn == 1 {
			return verifierProbeReport(mainTurn)
		}
		return verifierProbeFinish(mainTurn)
	}
	vc.nextVerifierTurn = func(int) string { return "" } // never reached in hang mode

	ag, sc, events := newEinoVerifierAgent(t, vc.URL)
	drained := collectAgentEventsAsync(events)
	ag.Run([]string{"https://target.example"}, "ctf drill: capture the flag on the isolated lab host")
	close(events)
	collected := <-drained

	var sawHungVerifier bool
	for _, kind := range vc.order() {
		if kind == "verifier-hang" {
			sawHungVerifier = true
		}
	}
	if !sawHungVerifier {
		t.Fatalf("verifier turn never hit the hanging handler; order=%v", vc.order())
	}
	if got := atomic.LoadInt32(&vc.maxInFlight); got != 1 {
		t.Fatalf("peak in-flight LLM requests = %d, want exactly 1 (deadline cancel must not open a parallel lane)", got)
	}

	// The finding survived with a non-verified tag (inconclusive is kept,
	// never dropped).
	vulns := reporting.GetVulnerabilitiesForContext(sc.ID)
	if len(vulns) != 1 {
		t.Fatalf("persisted findings = %d, want 1 (inconclusive verdicts must not drop findings): %+v", len(vulns), vulns)
	}
	if vulns[0].Verified || len(vulns[0].Tags) == 0 || vulns[0].Tags[0] == reporting.TagVerified {
		t.Fatalf("budget-exhausted finding must not be marked verified: verified=%v tags=%v", vulns[0].Verified, vulns[0].Tags)
	}

	// The client context was restored: the finish turn already succeeded
	// inside the run (the run reached a clean finish), and a post-run chat
	// with the shared client still works — an expired verifier deadline left
	// bound to the client would fail this instantly.
	if _, err := ag.client.Chat([]llm.Message{{Role: "user", Content: "still alive"}}); err != nil {
		t.Fatalf("post-verification chat failed: %v (client context not restored?)", err)
	}
	sawFinished := false
	for _, ev := range collected {
		if ev.Type == "finished" {
			sawFinished = true
		}
	}
	if !sawFinished {
		t.Fatal("eino run did not reach a finished event after the verifier deadline")
	}
}
