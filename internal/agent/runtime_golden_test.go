// Single-runtime golden scheduling gate. One ledger snapshot drives the Eino
// ADK runtime and must produce the documented M5 claim order, tool sequence,
// finish semantics and turn count. Scheduling itself is pinned separately by
// the ledger/VOI tests; this test covers the runtime wiring end to end.

package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/engine"
	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/reporting"
)

// goldenScript is the model's fixed plan, served by request order to both
// engines: claim the next hypothesis (rotating the vuln_class filter), close
// each claim as rejected, then finish. The expected claim order is derived
// from the fixture below, not from either engine's behavior:
//   - turn 1: claim {}          -> H-1 (rank 0.7*0.90 + 0.3*0.80 = 0.87, the
//     VOI-weighted top; beats the same-confidence lower-VOI pair)
//   - turn 3: claim {ssrf}      -> H-4 (class filter; H-4 is the only queued ssrf)
//   - turn 5: claim {}          -> H-2 (rank 0.78 tie with H-3; strict-improvement
//     comparison keeps the earlier insertion order = charter M5 tie-break)
//   - turn 7: claim {}          -> H-3
//   - turn 9: claim {rce}       -> none claimable (filtered empty branch)
//   - turn 10: finish           -> accepted (fixture state satisfies the finish
//     gates; see goldenSeedState)
var goldenScript = []struct {
	name string
	args map[string]string
}{
	{"claim_next_hypothesis", map[string]string{}},
	{"update_hypothesis", map[string]string{"hypothesis_id": "H-1", "status": "rejected"}},
	{"claim_next_hypothesis", map[string]string{"vuln_class": "ssrf"}},
	{"update_hypothesis", map[string]string{"hypothesis_id": "H-4", "status": "rejected"}},
	{"claim_next_hypothesis", map[string]string{}},
	{"update_hypothesis", map[string]string{"hypothesis_id": "H-2", "status": "rejected"}},
	{"claim_next_hypothesis", map[string]string{}},
	{"update_hypothesis", map[string]string{"hypothesis_id": "H-3", "status": "rejected"}},
	{"claim_next_hypothesis", map[string]string{"vuln_class": "rce"}},
	{"finish", map[string]string{"summary": "Golden parity run complete: all queued hypotheses settled."}},
}

var goldenExpectedClaims = []string{"H-1", "H-4", "H-2", "H-3"}

var goldenClaimedRe = regexp.MustCompile(`Claimed (H-\d+)`)

// buildGoldenLedgerFixture writes the golden ledger.json once and returns its
// bytes. Seven hypotheses cover: VOI weighting (H-1), a same-score tie broken
// by insertion order (H-2/H-3), a class-filter target (H-4), evidence carrying
// (observed on H-4, inferred+supports on H-1), and three non-claimable
// statuses (blocked H-5, exhausted H-6, rejected H-7).
func buildGoldenLedgerFixture(t *testing.T) []byte {
	t.Helper()
	fixtureDir := t.TempDir()
	ls := scanctx.NewLedgerStore()
	ls.SetPersistPath(fixtureDir)

	h1 := ls.Upsert(scanctx.Hypothesis{
		VulnClass: "sqli", Endpoint: "/api/users", Parameter: "id",
		Title: "SQLi on user listing", Confidence: 0.9,
		VOI: 0.8, VOIEstimated: true,
		Preconditions: []string{"unknown"},
		NextAction:    "capture a baseline response, then probe the id parameter",
	})
	obs := ls.AddEvidence(h1.ID, scanctx.Evidence{
		Kind: "baseline", Nature: scanctx.EvidenceObserved,
		Summary: "Baseline 200 response captured",
		Request: "GET /api/users?id=1 HTTP/1.1", Response: "HTTP/1.1 200 OK",
	})
	if !obs {
		t.Fatal("fixture: observed evidence was not stored")
	}
	got, ok := ls.Get(h1.ID)
	if !ok || len(got.Evidence) != 1 {
		t.Fatalf("fixture: baseline evidence missing: %+v", got)
	}
	if !ls.AddEvidence(h1.ID, scanctx.Evidence{
		Kind: "probe", Nature: scanctx.EvidenceInferred,
		Summary:  "Error-based divergence inferred from the baseline",
		Supports: []string{got.Evidence[0].ID},
	}) {
		t.Fatal("fixture: inferred evidence was not stored")
	}

	h2 := ls.Upsert(scanctx.Hypothesis{
		VulnClass: "xss", Endpoint: "/search", Parameter: "q",
		Title: "Reflected XSS in search", Confidence: 0.9,
		VOI: 0.5, VOIEstimated: true,
		Preconditions: []string{"unknown"},
	})
	h3 := ls.Upsert(scanctx.Hypothesis{
		VulnClass: "idor", Endpoint: "/api/orders", Parameter: "id",
		Title: "IDOR on order lookup", Confidence: 0.9,
		VOI: 0.5, VOIEstimated: true,
		Preconditions: []string{"unknown"},
	})
	// R2: rejecting a hypothesis requires a refuting observation. H2/H3 are
	// rejected by the golden script, so the fixture carries the control that
	// refutes them (a different user receives 403/empty on the same request).
	for _, id := range []string{h2.ID, h3.ID} {
		if !ls.AddEvidence(id, scanctx.Evidence{
			Kind: "baseline", Nature: scanctx.EvidenceObserved,
			Summary: "control request as another user returned no protected data",
			Request: "GET same route as user B HTTP/1.1", Response: "HTTP/1.1 403 Forbidden",
		}) {
			t.Fatalf("fixture: refuting baseline for %s was not stored", id)
		}
	}
	h4 := ls.Upsert(scanctx.Hypothesis{
		VulnClass: "ssrf", Endpoint: "/fetch", Parameter: "url",
		Title: "SSRF via fetch proxy", Confidence: 0.7,
		Preconditions: []string{"unknown"},
	})
	if !ls.AddEvidence(h4.ID, scanctx.Evidence{
		Kind: "probe", Nature: scanctx.EvidenceObserved,
		Summary: "Fetch endpoint accepts arbitrary destinations",
		Request: "GET /fetch?url=http://example.org/ HTTP/1.1", Response: "HTTP/1.1 200 example.org body",
	}) {
		t.Fatal("fixture: ssrf evidence was not stored")
	}
	h5 := ls.Upsert(scanctx.Hypothesis{
		VulnClass: "auth", Endpoint: "/admin", Title: "Auth bypass",
		Confidence: 0.95, Preconditions: []string{"valid admin credentials"},
	})
	h6 := ls.Upsert(scanctx.Hypothesis{
		VulnClass: "rce", Endpoint: "/upload", Title: "Upload RCE",
		Confidence: 0.99, Preconditions: []string{"unknown"},
	})
	h7 := ls.Upsert(scanctx.Hypothesis{
		VulnClass: "lfi", Endpoint: "/download", Title: "LFI in download",
		Confidence: 0.85, Preconditions: []string{"unknown"},
	})
	ls.SetStatus(h5.ID, scanctx.HypothesisBlocked, "needs an operator-supplied admin session")
	ls.SetStatus(h6.ID, scanctx.HypothesisExhausted, "attempts spent without proof")
	ls.SetStatus(h7.ID, scanctx.HypothesisRejected, "baseline ruled the traversal out")

	for _, id := range []string{h1.ID, h2.ID, h3.ID, h4.ID, h5.ID, h6.ID, h7.ID} {
		want := "H-"
		if len(id) < 2 || id[:2] != want {
			t.Fatalf("fixture: unexpected hypothesis id %q (want sequential H-N)", id)
		}
	}
	if ls.Len() != 7 {
		t.Fatalf("fixture: expected 7 hypotheses, got %d", ls.Len())
	}
	snapshot, err := os.ReadFile(filepath.Join(fixtureDir, "ledger.json"))
	if err != nil {
		t.Fatalf("fixture: read snapshot: %v", err)
	}
	return snapshot
}

// goldenSeedState pre-seeds the ScanState counters the finish gates read so a
// short scripted run can finish honestly: recon milestones met, coverage
// counters satisfied, and 8 of the 10 mandatory vuln classes marked tested.
// ssrf and xxe stay untested on purpose: that keeps missingClasses at 2 (< 3,
// so the first-finish coverage gap does not block) and keeps
// oastRequiredForTestedBlindClasses false (no OAST ceremony).
func goldenSeedState(t *testing.T, sc *scanctx.ScanContext) *ScanState {
	t.Helper()
	state := NewScanState()
	state.ScanContextID = sc.ID
	state.ReconDone = true
	state.EndpointInventorySaved = true
	state.TerminalCalls = 5
	state.EndpointsTested = map[string]bool{"/api/users": true, "/api/login": true, "/api/admin": true}
	state.InjectionEndpoints = map[string]bool{"/api/users": true}
	state.AccessControlEndpoints = map[string]bool{"/api/login": true, "/api/admin": true}
	state.DirBustingHosts = map[string]bool{"target.example": true}
	state.VulnClassesTested = map[string]bool{
		"sqli": true, "xss": true, "ssti": true, "cmdi": true,
		"path_traversal": true, "csrf": true, "crlf": true, "parameter_mining": true,
	}
	return state
}

func goldenToolCallResponse(seq int, name string, args map[string]string) map[string]any {
	argsJSON, _ := json.Marshal(args)
	return map[string]any{
		"choices": []map[string]any{{
			"message": map[string]any{
				"role": "assistant",
				"tool_calls": []map[string]any{{
					"id":   "call_" + strconv.Itoa(seq),
					"type": "function",
					"function": map[string]any{
						"name":      name,
						"arguments": string(argsJSON),
					},
				}},
			},
			"finish_reason": "tool_calls",
		}},
	}
}

// goldenRunOutcome is the per-engine observation used by the parity assertions.
type goldenRunOutcome struct {
	requests      int
	toolCalls     []goldenToolCall
	claimIDs      []string
	finishedCount int
	finishAborted bool
	finishContent string
	eventTypes    []string
}

type goldenToolCall struct {
	name string
	args map[string]string
}

func runGoldenEngine(t *testing.T, engineName string, srvURL string, counter *atomic.Int32, snapshot []byte) goldenRunOutcome {
	t.Helper()

	// Fresh scan dir with the byte-identical ledger snapshot.
	engineDir := filepath.Join(t.TempDir(), "scan")
	if err := os.MkdirAll(engineDir, 0o700); err != nil {
		t.Fatalf("%s: scan dir: %v", engineName, err)
	}
	if err := os.WriteFile(filepath.Join(engineDir, "ledger.json"), snapshot, 0o600); err != nil {
		t.Fatalf("%s: write ledger snapshot: %v", engineName, err)
	}

	sc := scanctx.New("golden-"+engineName, engineDir)
	scanctx.Activate(sc)
	t.Cleanup(func() {
		scanctx.Deactivate(sc.ID)
		reporting.CleanupContext(sc.ID)
		sc.Close()
	})
	// scanctx.New enables the domain journal but leaves ledger persistence
	// unset (production wires it in scan_session); mirror that wiring and load
	// the byte-identical snapshot.
	sc.Ledger.SetPersistPath(engineDir)
	if n := sc.Ledger.LoadFromDisk(); n != 7 {
		t.Fatalf("%s: ledger fixture load = %d hypotheses, want 7", engineName, n)
	}
	// Production wiring (web/scan_session.go) threads the configured ranking
	// weight into the ledger; the golden sample uses the documented ceiling.
	sc.Ledger.SetVOIWeight(scanctx.MaxVOIWeight)

	cfg := &config.Config{
		LLM:                 "openai/gpt-test",
		APIBase:             srvURL,
		APIKey:              "sk-test",
		MaxIterations:       len(goldenScript) + 6, // bounded, comfortably above the script
		MinIterations:       1,                     // the finish gate's iteration floor
		DisableAutoDelegate: true,
	}

	events := make(chan Event, 256)
	client := llm.NewClient(cfg)
	guard := scopeguard.Config{BindAddr: "127.0.0.1", Port: 0}

	// Bind the agent to ITS OWN scan context: without it NewAgent falls back
	// to scanctx.Default(), which may be another test's context (and its
	// on-disk execution checkpoint).
	store, err := engine.NewFileCheckpointStore(t.TempDir())
	if err != nil {
		t.Fatalf("%s: checkpoint store: %v", engineName, err)
	}
	opts := []any{WithLLMClient(client), sc, WithRuntime(store, nil)}
	ag := NewAgent(cfg, "golden-"+engineName, events, guard, opts...)
	t.Cleanup(ag.Stop)
	ag.state = goldenSeedState(t, sc)

	done := make(chan goldenRunOutcome, 1)
	go func() {
		outcome := goldenRunOutcome{}
		for ev := range events {
			outcome.eventTypes = append(outcome.eventTypes, ev.Type)
			switch ev.Type {
			case "tool_call":
				args := make(map[string]string, len(ev.ToolArgs))
				for k, v := range ev.ToolArgs {
					if k == "tool_name" {
						continue // injected by the eino observer only
					}
					args[k] = v
				}
				outcome.toolCalls = append(outcome.toolCalls, goldenToolCall{name: ev.ToolName, args: args})
			case "tool_result":
				if ev.ToolName == "claim_next_hypothesis" {
					if m := goldenClaimedRe.FindStringSubmatch(ev.ToolResult.Output); m != nil {
						outcome.claimIDs = append(outcome.claimIDs, m[1])
					}
				}
			case "finished":
				outcome.finishedCount++
				outcome.finishAborted = ev.Aborted
				outcome.finishContent = ev.Content
			}
		}
		done <- outcome
	}()

	ag.Run([]string{"https://target.example"}, "ctf drill: capture the flag on the isolated lab host")
	close(events)
	outcome := <-done
	outcome.requests = int(counter.Load())
	return outcome
}

// TestGoldenSchedulingParity runs the golden scenario through the only
// runtime. It pins that every chat request carries the tools array on the wire
// and that the M5 golden schedule still holds end to end.
func TestGoldenSchedulingParity(t *testing.T) {
	snapshot := buildGoldenLedgerFixture(t)

	var counter atomic.Int32
	var requestsWithoutTools atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var probe struct {
			Tools []any `json:"tools"`
		}
		_ = json.Unmarshal(body, &probe)
		if strings.HasSuffix(r.URL.Path, "/chat/completions") && len(probe.Tools) == 0 {
			requestsWithoutTools.Add(1)
		}
		n := int(counter.Add(1))
		w.Header().Set("Content-Type", "application/json")
		if n <= len(goldenScript) {
			_ = json.NewEncoder(w).Encode(goldenToolCallResponse(n, goldenScript[n-1].name, goldenScript[n-1].args))
			return
		}
		_ = json.NewEncoder(w).Encode(goldenToolCallResponse(n, "finish", map[string]string{"summary": "script overflow"}))
	}))
	defer srv.Close()

	counter.Store(0)
	out := runGoldenEngine(t, "eino", srv.URL, &counter, snapshot)

	if got := requestsWithoutTools.Load(); got != 0 {
		t.Fatalf("%d chat requests reached the provider without a tools array — the eino path dropped the native tool surface", got)
	}
	if out.requests != len(goldenScript) {
		t.Fatalf("eino (factory-default form) model requests = %d, want %d (finished=%d aborted=%v claims=%v)",
			out.requests, len(goldenScript), out.finishedCount, out.finishAborted, out.claimIDs)
	}
	if len(out.claimIDs) != len(goldenExpectedClaims) {
		t.Fatalf("claim sequence = %v, want %v", out.claimIDs, goldenExpectedClaims)
	}
	for i, id := range goldenExpectedClaims {
		if out.claimIDs[i] != id {
			t.Fatalf("claim %d = %s, want %s (full sequence %v)", i, out.claimIDs[i], id, out.claimIDs)
		}
	}
	if len(out.toolCalls) != len(goldenScript) {
		t.Fatalf("expected %d tool_call events, eino emitted %d", len(goldenScript), len(out.toolCalls))
	}
	if out.finishedCount != 1 || out.finishAborted {
		t.Fatalf("finished events = %d aborted=%v, want exactly 1 clean completion", out.finishedCount, out.finishAborted)
	}
}
