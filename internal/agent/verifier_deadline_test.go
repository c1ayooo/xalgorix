package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/config"
	"github.com/xalgord/xalgorix/v4/internal/llm"
	"github.com/xalgord/xalgorix/v4/internal/scanctx"
	"github.com/xalgord/xalgorix/v4/internal/scopeguard"
	"github.com/xalgord/xalgorix/v4/internal/tools/reporting"
)

// The verifier's time budget must constrain an IN-FLIGHT LLM request, not
// just the space between turns: a hung provider call otherwise holds
// report_vulnerability open past the verifier deadline and races its 15-minute
// tool watchdog. Binding the deadline to the shared client's request context
// cancels the transport request; the verdict stays inconclusive so the
// candidate finding is preserved for manual verification (never dropped).
func TestVerifierDeadlineCancelsInFlightRequest(t *testing.T) {
	oldDeadline := verifierDeadline
	verifierDeadline = 300 * time.Millisecond
	t.Cleanup(func() { verifierDeadline = oldDeadline })

	var reached atomic.Bool
	requestCanceled := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		// net/http only starts the connection-closure watch (and thus cancels
		// r.Context()) after the request body has been read to EOF — drain it
		// first, then block until the client cancels (or the test exits).
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
		requestCanceled <- struct{}{}
	}))
	t.Cleanup(srv.Close)

	sctx := scanctx.New(t.Name(), t.TempDir())
	t.Cleanup(sctx.Close)
	a := &Agent{ID: "verifier", cfg: &config.Config{}, scanCtx: sctx,
		ctx: context.Background(), localGuard: scopeguard.Config{AllowLocalTargets: true}}
	a.client = llm.NewClient(&config.Config{LLM: "openai/gpt-test", APIBase: srv.URL, APIKey: "test"})
	a.client.SetContext(a.ctx)

	start := time.Now()
	verdict := a.verifyFinding(reporting.VerificationRequest{Title: "hung provider"})
	elapsed := time.Since(start)

	if !reached.Load() {
		t.Fatal("verifier never reached the LLM call")
	}
	// The deadline context must cancel the transport request, not just make
	// the turn loop give up afterwards.
	select {
	case <-requestCanceled:
	case <-time.After(3 * time.Second):
		t.Error("in-flight LLM request was not canceled at the transport level")
	}
	if elapsed > 10*time.Second {
		t.Errorf("verifyFinding took %s — the deadline must bound the in-flight request", elapsed)
	}
	if !verdict.Inconclusive {
		t.Errorf("verdict = %+v, want inconclusive (budget exhaustion must never confirm or reject)", verdict)
	}
	if verdict.Confirmed {
		t.Error("budget exhaustion must never produce a confirmed verdict")
	}
	if verdict.Reason != "verification exceeded time budget" {
		t.Errorf("reason = %q, want \"verification exceeded time budget\"", verdict.Reason)
	}
}

// The verifier must restore the shared client's context on exit so the main
// loop's cancellation wiring (Run deadline / semantic watchdog) survives.
func TestVerifierRestoresClientContext(t *testing.T) {
	oldDeadline := verifierDeadline
	verifierDeadline = 200 * time.Millisecond
	t.Cleanup(func() { verifierDeadline = oldDeadline })

	// A server that answers immediately with no tool calls: the verifier
	// walks its turns and exits through the normal (verdict-less) path.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"thinking..."}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)

	sctx := scanctx.New(t.Name(), t.TempDir())
	t.Cleanup(sctx.Close)
	a := &Agent{ID: "verifier", cfg: &config.Config{}, scanCtx: sctx,
		ctx: context.Background(), localGuard: scopeguard.Config{AllowLocalTargets: true}}
	a.client = llm.NewClient(&config.Config{LLM: "openai/gpt-test", APIBase: srv.URL, APIKey: "test"})
	a.client.SetContext(a.ctx)

	_ = a.verifyFinding(reporting.VerificationRequest{Title: "restore check"})

	// After verification the client ctx must be back to a.ctx — a follow-up
	// call must succeed (it would fail instantly if the expired verifier
	// deadline context was left behind).
	if _, err := a.client.Chat([]llm.Message{{Role: "user", Content: "still alive"}}); err != nil {
		t.Fatalf("post-verification chat failed: %v (client context not restored?)", err)
	}
}
