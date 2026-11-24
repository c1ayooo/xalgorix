package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

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

// TestEnqueueContainerCloseWaitsForLiveSibling pins that a close is never
// queued while another lane still attacks the same container.
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
