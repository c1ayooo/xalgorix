package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// stagnantRoot models a worker that never produces progress: it blocks until
// the engine kills it, which is exactly the round-5 failure mode (a-03 ran 4.6h
// on a confirmed-but-unexploited SQLi).
type stagnantRoot struct {
	mu      sync.Mutex
	agents  map[string]string        // unitID -> agentID
	waiting map[string]chan struct{} // agentID -> closed on kill
	kills   []string
	typed   []string
	steers  []string
}

func newStagnantRoot() *stagnantRoot {
	return &stagnantRoot{agents: map[string]string{}, waiting: map[string]chan struct{}{}}
}

func (r *stagnantRoot) dispatch(unitID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	agentID := "w-" + unitID
	r.agents[unitID] = agentID
	if _, ok := r.waiting[agentID]; !ok {
		r.waiting[agentID] = make(chan struct{})
	}
	return agentID, nil
}

func (r *stagnantRoot) DispatchWorkerTask(name, task string, targets []string, unitID string) (string, error) {
	return r.dispatch(unitID)
}

func (r *stagnantRoot) DispatchWorkerTaskWithSurface(name, task string, targets []string, unitID, surface string) (string, error) {
	return r.dispatch(unitID)
}

func (r *stagnantRoot) WaitWorkerResult(ctx context.Context, agentID string) (string, string, string) {
	r.mu.Lock()
	ch, ok := r.waiting[agentID]
	r.mu.Unlock()
	if !ok {
		ch = make(chan struct{})
	}
	select {
	case <-ch:
		return "", "canceled", "context canceled"
	case <-ctx.Done():
		return "", "timeout", ctx.Err().Error()
	}
}

func (r *stagnantRoot) WorkerPoolRunning() int { return 0 }

func (r *stagnantRoot) kill(unitID string) {
	r.mu.Lock()
	agentID := r.agents[unitID]
	ch := r.waiting[agentID]
	r.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

func (r *stagnantRoot) KillWorker(unitID, reason string) error {
	r.mu.Lock()
	r.kills = append(r.kills, unitID)
	r.mu.Unlock()
	r.kill(unitID)
	return nil
}

func (r *stagnantRoot) KillWorkerWithReason(unitID, reasonCode, reason string) error {
	r.mu.Lock()
	r.typed = append(r.typed, reasonCode)
	r.mu.Unlock()
	r.kill(unitID)
	return nil
}

func (r *stagnantRoot) SteerWorker(unitID, message string) error {
	r.mu.Lock()
	r.steers = append(r.steers, unitID)
	r.mu.Unlock()
	return nil
}

func (r *stagnantRoot) count() (kills, typed, steers int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.kills), len(r.typed), len(r.steers)
}

// TestWorkerProgressDisabledIsNotDefault pins the off-switch: zero means
// default, and only the explicit flag disables the reclaim clock. The round-5
// parser mapped "off" to a negative value that the constructor turned back into
// the 20m default.
func TestWorkerProgressDisabledIsNotDefault(t *testing.T) {
	base := Config{ScanID: "progress-off", Workers: 1, Repo: graph.NewMemoryStore(), Root: newStagnantRoot()}
	off := base
	off.WorkerProgressDisabled = true
	pool, err := New(context.Background(), off)
	if err != nil {
		t.Fatal(err)
	}
	if pool.cfg.WorkerProgressTimeout != 0 {
		t.Fatalf("disabled progress timeout = %s, want 0", pool.cfg.WorkerProgressTimeout)
	}
	def := base
	pool, err = New(context.Background(), def)
	if err != nil {
		t.Fatal(err)
	}
	if pool.cfg.WorkerProgressTimeout != defaultWorkerProgressTimeout {
		t.Fatalf("zero timeout = %s, want the default %s", pool.cfg.WorkerProgressTimeout, defaultWorkerProgressTimeout)
	}
}

// delayedExitRoot models a worker that acknowledges a kill but keeps winding
// down: the engine must not write the terminal intent state until the worker's
// done channel actually closes.
type delayedExitRoot struct {
	agentID  string
	done     chan struct{}
	killed   chan struct{}
	doneOnce sync.Once
	killOnce sync.Once
}

func newDelayedExitRoot() *delayedExitRoot {
	return &delayedExitRoot{agentID: "w-delayed", done: make(chan struct{}), killed: make(chan struct{})}
}

func (r *delayedExitRoot) DispatchWorkerTask(name, task string, targets []string, unitID string) (string, error) {
	return r.agentID, nil
}

func (r *delayedExitRoot) DispatchWorkerTaskWithSurface(name, task string, targets []string, unitID, surface string) (string, error) {
	return r.agentID, nil
}

func (r *delayedExitRoot) WaitWorkerResult(ctx context.Context, agentID string) (string, string, string) {
	select {
	case <-r.done:
		return "", "canceled", "killed: stagnant"
	case <-ctx.Done():
		return "", "timeout", ctx.Err().Error()
	}
}

func (r *delayedExitRoot) WorkerPoolRunning() int { return 1 }

func (r *delayedExitRoot) SteerWorker(unitID, message string) error { return nil }

func (r *delayedExitRoot) KillWorkerWithReason(unitID, reasonCode, reason string) error {
	r.killOnce.Do(func() { close(r.killed) })
	return nil
}

func (r *delayedExitRoot) exit() { r.doneOnce.Do(func() { close(r.done) }) }

func (r *delayedExitRoot) WaitWorkerExit(ctx context.Context, unitID string) error {
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestReclaimSettlesOnlyAfterWorkerExit pins the review's acceptance sequence:
// the stall decision is visible, the kill is delivered, and the graph terminal
// state (with its reason) appears only after the worker actually exits.
func TestReclaimSettlesOnlyAfterWorkerExit(t *testing.T) {
	store := graph.NewMemoryStore()
	sctx := scanctx.New("exit-order", t.TempDir())
	root := newDelayedExitRoot()
	pool, err := New(context.Background(), Config{
		ScanID: "exit-order", Workers: 1, Repo: store, Root: root, ScanCtx: sctx,
		WorkerTimeout:         10 * time.Second,
		WindDown:              20 * time.Millisecond,
		WorkerProgressTimeout: 100 * time.Millisecond,
		ProgressCheckInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	id := addIntent(t, store, "exit-order", "u-exit")
	pool.Start()
	t.Cleanup(pool.Stop)

	select {
	case <-root.killed:
	case <-time.After(2 * time.Second):
		t.Fatal("reclaim never delivered a kill")
	}

	// Kill delivered, worker still winding down: no terminal row yet.
	time.Sleep(50 * time.Millisecond)
	node, err := store.GetNode(context.Background(), "exit-order", id)
	if err != nil {
		t.Fatal(err)
	}
	if node.State != graph.StateRunning {
		t.Fatalf("state = %s before the worker exited, want running", node.State)
	}

	root.exit()
	waitState(t, store, "exit-order", id, graph.StateExhausted)
	node, _ = store.GetNode(context.Background(), "exit-order", id)
	if !strings.Contains(node.BlockedReason, "no information-layer progress") || !strings.Contains(node.BlockedReason, "exit=confirmed") {
		t.Fatalf("terminal reason = %q", node.BlockedReason)
	}
}

// TestRenewLeaseLossStopsWorker pins XALG008: when the intent lease is
// reclaimed by another owner, the old worker is cancelled instead of being left
// to produce side effects it no longer owns. Settlement is separately
// owner-fenced, so the old worker cannot rewrite the new row.
func TestRenewLeaseLossStopsWorker(t *testing.T) {
	store := graph.NewMemoryStore()
	root := newStagnantRoot()
	pool, err := New(context.Background(), Config{
		ScanID: "lease-loss", Workers: 1, Repo: store, Root: root,
		ScanCtx: scanctx.New("lease-loss", t.TempDir()), LeaseTTL: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	addIntent(t, store, "lease-loss", "u-lease")
	first, err := store.ClaimNextIntent(context.Background(), "lease-loss", "work#1", 20*time.Millisecond)
	if err != nil || first == nil {
		t.Fatalf("first claim: %v %+v", err, first)
	}
	time.Sleep(60 * time.Millisecond)
	second, err := store.ClaimNextIntent(context.Background(), "lease-loss", "work#2", time.Minute)
	if err != nil || second == nil || second.Owner != "work#2" {
		t.Fatalf("reclaim claim: %v %+v", err, second)
	}

	stop := make(chan struct{})
	go pool.renewLease(first, "work#1", stop)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		root.mu.Lock()
		typed := append([]string(nil), root.typed...)
		root.mu.Unlock()
		for _, code := range typed {
			if code == agent.KillReasonLeaseLost {
				close(stop)
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	t.Fatal("old worker was not stopped after losing its lease")
}

// TestStallReportMarksActiveTool pins the honest stall projection: when a tool
// is actually executing, the report says so (and names it) instead of inferring
// "tool-running" from a recent touch.
func TestStallReportMarksActiveTool(t *testing.T) {
	sctx := scanctx.New("stall-active", t.TempDir())
	pool, err := New(context.Background(), Config{
		ScanID: "stall-active", Workers: 1, Repo: graph.NewMemoryStore(),
		Root: newStagnantRoot(), ScanCtx: sctx,
	})
	if err != nil {
		t.Fatal(err)
	}
	pool.trackActive("u-1", "agent-1", "worker", time.Now().Add(-20*time.Minute))
	sctx.Progress.BeginTool("agent-1", "terminal_execute")
	report := pool.StallReport()
	if len(report) != 1 {
		t.Fatalf("stall report = %+v, want one worker", report)
	}
	if !report[0].ActiveTool || report[0].ActiveToolName != "terminal_execute" || report[0].ActiveFor <= 0 {
		t.Fatalf("active tool projection = %+v", report[0])
	}
}

// TestStallGuardSkipsActiveTool pins the planner-side half: a legitimate long
// tool is not steered just because no result has arrived yet.
func TestStallGuardSkipsActiveTool(t *testing.T) {
	steers := 0
	p := NewPlanner(context.Background(), PlannerConfig{
		ScanID: "guard-active",
		Steer:  func(string, string) error { steers++; return nil },
	})
	worker := StalledWorker{UnitID: "u-1", Lane: "execution", IdleFor: 20 * time.Minute, ActiveTool: true}
	p.applyStallGuard([]StalledWorker{worker})
	p.applyStallGuard([]StalledWorker{worker})
	if steers != 0 {
		t.Fatalf("active tool was steered %d times", steers)
	}
	worker.ActiveTool = false
	p.applyStallGuard([]StalledWorker{worker})
	p.applyStallGuard([]StalledWorker{worker})
	if steers != 1 {
		t.Fatalf("inactive stalled worker steers = %d, want 1 after two rounds", steers)
	}
}

// TestProgressWatchPausesForActiveTool pins the long-tool protection: a worker
// with a legitimate tool in flight is not reclaimed until the tool returns.
func TestProgressWatchPausesForActiveTool(t *testing.T) {
	sctx := scanctx.New("active-tool", t.TempDir())
	root := newStagnantRoot()
	pool, err := New(context.Background(), Config{
		ScanID: "active-tool", Workers: 1, Repo: graph.NewMemoryStore(), Root: root, ScanCtx: sctx,
		WorkerTimeout:         10 * time.Second,
		WindDown:              20 * time.Millisecond,
		WorkerProgressTimeout: 60 * time.Millisecond,
		ProgressCheckInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	root.dispatch("u-1")
	sctx.Progress.BeginTool("w-u-1", "terminal_execute")
	w := pool.watchProgress("u-1", "w-u-1", "worker")
	time.Sleep(180 * time.Millisecond)
	if reason := w.reclaimed(); reason != "" {
		t.Fatalf("active tool was reclaimed: %s", reason)
	}
	if kills, typed, steers := root.count(); kills != 0 || typed != 0 || steers != 0 {
		t.Fatalf("active tool was steered/killed: kills=%d typed=%d steers=%d", kills, typed, steers)
	}

	sctx.Progress.EndTool("w-u-1")
	deadline := time.Now().Add(2 * time.Second)
	for w.reclaimed() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if w.reclaimed() == "" {
		t.Fatal("worker was not reclaimed after the tool returned and progress stayed absent")
	}
	w.stop()
}

// refusingRoot models the production root's stagnant-kill guard: the engine
// refuses because the worker produced progress inside the window. The orchestrator
// must not fall back to the human-labeled plain kill (round-5 bug).
type refusingRoot struct {
	stagnantRoot
	mu     sync.Mutex
	plain  int
	typed  int
	reason string
}

func (r *refusingRoot) KillWorkerWithReason(unitID, reasonCode, reason string) error {
	r.mu.Lock()
	r.typed++
	r.reason = reason
	r.mu.Unlock()
	return fmt.Errorf("%w: %s is still progressing", agent.ErrStagnantKillRefused, unitID)
}

func (r *refusingRoot) KillWorker(unitID, reason string) error {
	r.mu.Lock()
	r.plain++
	r.mu.Unlock()
	return nil
}

func TestKillStagnantRefusalDoesNotFallBackToPlainKill(t *testing.T) {
	root := &refusingRoot{}
	p := &Pool{cfg: Config{Root: root}}
	delivered, refused := p.killStagnant("u-1", "no progress")
	if delivered || !refused {
		t.Fatalf("killStagnant = delivered %v refused %v, want false/true", delivered, refused)
	}
	root.mu.Lock()
	plain, typed := root.plain, root.typed
	root.mu.Unlock()
	if typed != 1 || plain != 0 {
		t.Fatalf("typed=%d plain=%d, want 1/0 (refusal must not become a human kill)", typed, plain)
	}
}

// TestCancelLikeClassifiesEngineCauses pins the reason-composition predicate:
// a reclaim reason only replaces the settlement reason for cancel-like exits.
func TestCancelLikeClassifiesEngineCauses(t *testing.T) {
	if !isCancelLike("timeout", "context deadline exceeded") {
		t.Fatal("timeout must be cancel-like")
	}
	if !isCancelLike("failed", "killed: stagnant: no progress") {
		t.Fatal("killed worker must be cancel-like")
	}
	if isCancelLike("failed", "model api error: 500") {
		t.Fatal("an independent failure must not be treated as a cancel")
	}
}

// TestPoolReclaimsStagnantExecutionLane pins P0-1: an execution-lane worker
// with no information-layer increment is steered, killed with the typed
// stagnant cause and settled exhausted, with the reason recorded.
func TestPoolReclaimsStagnantExecutionLane(t *testing.T) {
	store := graph.NewMemoryStore()
	sctx := scanctx.New("reclaim", t.TempDir())
	root := newStagnantRoot()
	pool, err := New(context.Background(), Config{
		ScanID: "reclaim", Workers: 1, Repo: store, Root: root, ScanCtx: sctx,
		WorkerTimeout:         10 * time.Second,
		WindDown:              30 * time.Millisecond,
		WorkerProgressTimeout: 150 * time.Millisecond,
		ProgressCheckInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	id := addIntent(t, store, "reclaim", "stuck-a-03")
	pool.Start()
	t.Cleanup(pool.Stop)
	waitState(t, store, "reclaim", id, graph.StateExhausted)

	// The typed killer is preferred, so the plain kill path stays unused.
	kills, typed, steers := root.count()
	if typed != 1 || steers != 1 || kills != 0 {
		t.Fatalf("kills=%d typed=%d steers=%d, want 0/1/1", kills, typed, steers)
	}
	node, err := store.GetNode(context.Background(), "reclaim", id)
	if err != nil || node == nil {
		t.Fatalf("get node: %v", err)
	}
	if !strings.Contains(node.BlockedReason, "no information-layer progress") {
		t.Fatalf("exhausted reason not recorded: %q", node.BlockedReason)
	}
	if !strings.Contains(node.BlockedReason, "exit=confirmed") {
		t.Fatalf("reclaim reason must prove the worker exited: %q", node.BlockedReason)
	}
	if node.CompletedAt == nil || node.Owner != "" || node.LeaseUntil != nil {
		t.Fatalf("terminal row not atomically released: state=%s owner=%q lease=%v", node.State, node.Owner, node.LeaseUntil)
	}
}

// TestProgressWatchSkipsNonExecutionLanes pins that the reclaim clock never
// touches the API or analysis lanes: an HTTP call produces no information
// increments, and neither lane holds a container slot.
func TestProgressWatchSkipsNonExecutionLanes(t *testing.T) {
	p := &Pool{cfg: Config{
		ScanCtx:               scanctx.New("watch-lane", ""),
		WorkerProgressTimeout: 50 * time.Millisecond,
		ProgressCheckInterval: 50 * time.Millisecond,
	}}
	for _, surface := range []string{"orchestrator", "analysis"} {
		w := p.watchProgress("unit", "agent", surface)
		time.Sleep(120 * time.Millisecond)
		if reason := w.reclaimed(); reason != "" {
			t.Fatalf("%s lane was reclaimed: %s", surface, reason)
		}
		w.stop()
	}
}

func addContainerAttack(t *testing.T, store graph.Repository, scanID, code, addr string, state graph.State) *graph.Node {
	t.Helper()
	payload := `{"task_id":"container-attack:` + code + `","target":"` + addr + `","target_kind":"container","title":"Attack ` + code + `"}`
	n, err := store.UpsertNode(context.Background(), graph.NodeInput{
		ScanID: scanID, Kind: graph.KindIntent, UnitKey: "container-attack:" + code,
		State: state, Priority: containerAttackPriority, Origin: "engine", Payload: payload,
	})
	if err != nil {
		t.Fatalf("add container attack %s: %v", code, err)
	}
	return n
}

// TestEnqueueContainerCloseIsOneShot pins the other half of P0-1: a reclaimed
// container intent produces exactly one control-plane close intent, with the
// documented endpoint, so the platform slot is released instead of staying
// open (round 5's a-18 stayed open for 3.1h).
func TestEnqueueContainerCloseIsOneShot(t *testing.T) {
	store := graph.NewMemoryStore()
	p := &Pool{cfg: Config{
		ScanID: "close", Repo: store,
		ControlPlaneHosts: []string{"bench.example.com"},
		UserInstruction:   "Platform API: POST http://bench.example.com/openapi/v1/challenges/start",
	}}
	node := addContainerAttack(t, store, "close", "a-18", "10.0.175.188:8080", graph.StateBlocked)
	p.enqueueContainerClose(node, graph.StateBlocked)

	nodes, err := store.ListNodes(context.Background(), "close", graph.KindIntent)
	if err != nil {
		t.Fatal(err)
	}
	var closes []graph.Node
	for _, n := range nodes {
		if strings.HasPrefix(n.UnitKey, "container-close:") {
			closes = append(closes, n)
		}
	}
	if len(closes) != 1 {
		t.Fatalf("close intents = %d, want 1", len(closes))
	}
	payload := closes[0].PayloadJSON()
	if got := payload["target_kind"]; got != "control_plane_api" {
		t.Fatalf("target_kind = %v", got)
	}
	if got := payload["target"]; got != "bench.example.com" {
		t.Fatalf("target = %v", got)
	}
	endpoint, _ := payload["endpoint"].(string)
	if !strings.Contains(endpoint, "/openapi/v1/challenges/close") || !strings.Contains(endpoint, "unique_code=a-18") {
		t.Fatalf("endpoint = %q", endpoint)
	}
	if closes[0].Priority != containerClosePriority {
		t.Fatalf("priority = %d", closes[0].Priority)
	}

	// Second reclaim must not queue a duplicate close.
	p.enqueueContainerClose(node, graph.StateBlocked)
	nodes, _ = store.ListNodes(context.Background(), "close", graph.KindIntent)
	count := 0
	for _, n := range nodes {
		if strings.HasPrefix(n.UnitKey, "container-close:") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("close intents after second reclaim = %d, want 1", count)
	}
}

// TestEnqueueContainerCloseRetriesFailedAttempt pins the difference between a
// close request that ran and one that failed: a blocked/exhausted close must not
// count as success, while an in-flight or completed close is not duplicated.
func TestEnqueueContainerCloseRetriesFailedAttempt(t *testing.T) {
	store := graph.NewMemoryStore()
	p := &Pool{cfg: Config{
		ScanID: "close-retry", Repo: store,
		ControlPlaneHosts: []string{"bench.example.com"},
	}}
	node := addContainerAttack(t, store, "close-retry", "a-18", "10.0.175.188:8080", graph.StateBlocked)
	addClose := func(unit string, state graph.State) {
		t.Helper()
		if _, err := store.UpsertNode(context.Background(), graph.NodeInput{
			ScanID: "close-retry", Kind: graph.KindIntent, UnitKey: unit, State: state,
			Priority: containerClosePriority, Origin: "engine",
			Payload: `{"task_id":"` + unit + `","target":"bench.example.com","target_kind":"control_plane_api"}`,
		}); err != nil {
			t.Fatalf("add close %s: %v", unit, err)
		}
	}
	closeUnits := func() []string {
		nodes, _ := store.ListNodes(context.Background(), "close-retry", graph.KindIntent)
		var out []string
		for _, n := range nodes {
			if strings.HasPrefix(n.UnitKey, "container-close:") {
				out = append(out, n.UnitKey)
			}
		}
		return out
	}

	addClose("container-close:a-18", graph.StateBlocked)
	p.enqueueContainerClose(node, graph.StateBlocked)
	units := closeUnits()
	if len(units) != 2 || !strings.Contains(units[1], ":r2") {
		t.Fatalf("close units = %v, want a retry with an attempt suffix", units)
	}

	// An in-flight close is not duplicated.
	p.enqueueContainerClose(node, graph.StateBlocked)
	if got := len(closeUnits()); got != 2 {
		t.Fatalf("in-flight close was duplicated: %v", closeUnits())
	}

	// A completed close is not duplicated either.
	nodes, _ := store.ListNodes(context.Background(), "close-retry", graph.KindIntent)
	for _, n := range nodes {
		if n.UnitKey != "container-close:a-18:r2" {
			continue
		}
		if _, err := store.ConcludePlanned(context.Background(), "close-retry", n.ID, graph.StateDone, []graph.NodeInput{{
			ScanID: "close-retry", Kind: graph.KindFact, State: graph.StateOrigin,
			UnitKey: "close-result:a-18", Payload: `{"status":"requested"}`,
		}}); err != nil {
			t.Fatalf("conclude close: %v", err)
		}
	}
	p.enqueueContainerClose(node, graph.StateBlocked)
	if got := len(closeUnits()); got != 2 {
		t.Fatalf("completed close was duplicated: %v", closeUnits())
	}
}

func TestEnqueueContainerCloseWaitsForLiveSibling(t *testing.T) {
	store := graph.NewMemoryStore()
	p := &Pool{cfg: Config{
		ScanID:            "close-live",
		Repo:              store,
		ControlPlaneHosts: []string{"bench.example.com"},
	}}
	node := addContainerAttack(t, store, "close-live", "a-18", "10.0.175.188:8080", graph.StateBlocked)
	addContainerAttack(t, store, "close-live", "a-18b", "10.0.175.188:8080", graph.StateOpen)
	p.enqueueContainerClose(node, graph.StateBlocked)

	nodes, _ := store.ListNodes(context.Background(), "close-live", graph.KindIntent)
	for _, n := range nodes {
		if strings.HasPrefix(n.UnitKey, "container-close:") {
			t.Fatalf("close queued while a sibling intent is still open: %s", n.UnitKey)
		}
	}
}

// TestEnqueueContainerCloseNeedsPlatform pins that a plain scan (no platform
// API configured) never invents close intents.
func TestEnqueueContainerCloseNeedsPlatform(t *testing.T) {
	store := graph.NewMemoryStore()
	p := &Pool{cfg: Config{ScanID: "close-none", Repo: store}}
	node := addContainerAttack(t, store, "close-none", "x-01", "10.0.0.9:80", graph.StateExhausted)
	p.enqueueContainerClose(node, graph.StateExhausted)
	nodes, _ := store.ListNodes(context.Background(), "close-none", graph.KindIntent)
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want only the attack intent", len(nodes))
	}
}

// TestProgressWatchReclaimsExpiredTool pins XALG005's other half: a tool past
// its own deadline is no longer protected by the active-tool pause.
func TestProgressWatchReclaimsExpiredTool(t *testing.T) {
	sctx := scanctx.New("expired-tool", t.TempDir())
	root := newStagnantRoot()
	pool, err := New(context.Background(), Config{
		ScanID: "expired-tool", Workers: 1, Repo: graph.NewMemoryStore(), Root: root, ScanCtx: sctx,
		WorkerTimeout:         10 * time.Second,
		WindDown:              20 * time.Millisecond,
		WorkerProgressTimeout: 60 * time.Millisecond,
		ProgressCheckInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	root.dispatch("u-expired")
	sctx.Progress.BeginTool("w-u-expired", "terminal_execute")
	sctx.Progress.SetToolDeadline("w-u-expired", time.Now().Add(-time.Second))
	w := pool.watchProgress("u-expired", "w-u-expired", "worker")
	deadline := time.Now().Add(2 * time.Second)
	for w.reclaimed() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if w.reclaimed() == "" {
		t.Fatal("worker with an expired tool deadline was not reclaimed")
	}
	w.stop()
}
