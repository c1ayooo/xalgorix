package orchestrator

// reclaim.go — slot reclamation and rotation
// (BENCHMARK_ROUND5_ANALYSIS §4.1 / §7 P0-1).
//
// Round 5 held its 3 container slots for 596 slot-minutes on four challenges
// that scored 0: one sat on a confirmed-but-unexploited SQLi for 4.6h (six
// restarts), another kept a container open for 3.1h with no live worker. The
// pool only rotated on a clean finish or a dead port.
//
// Two engine-owned mechanisms fix that, and neither changes the frozen
// contract (worker timebox floor 30m, Verifier, report gate):
//
//  1. a progress clock — an execution-lane worker that produced no
//     information-layer increment for WorkerProgressTimeout is steered to save
//     its work, then reclaimed as exhausted with a typed reason, freeing the
//     slot for another challenge;
//  2. a close intent — when a container intent is reclaimed without a flag,
//     the engine enqueues ONE control-plane intent that calls the platform
//     close endpoint, so the platform slot is actually released instead of
//     staying open until the run ends.
//
// Both are deterministic: no LLM decides to rotate, and the same progress
// ledger + settings produce the same decision.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// noProgressMessage tells a stalled worker to checkpoint before the kill. It is
// deliberately different from windDownMessage (budget exhaustion): here the
// worker still has budget but produced nothing information-bearing.
const noProgressMessage = "No new notes, evidence, hypotheses or flags were recorded for a while. Save what you have now: a short note with the exact state (what answered, what failed, the next concrete step) and return. Do not start new work."

// workerStagnantKiller is implemented by the root agent (KillWorkerWithReason).
// Optional so test/legacy roots keep working.
type workerStagnantKiller interface {
	KillWorkerWithReason(unitID, reasonCode, reason string) error
}

// progressWatch is the reclaim clock for one dispatched worker. It is an
// observation device: it never mutates intent state, it only steers/kills the
// worker; the run path settles the intent.
type progressWatch struct {
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	reason string
}

func (w *progressWatch) stop() {
	if w == nil || w.done == nil {
		return
	}
	w.once.Do(func() { close(w.done) })
}

func (w *progressWatch) reclaimed() string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.reason
}

func (w *progressWatch) setReason(reason string) {
	w.mu.Lock()
	w.reason = reason
	w.mu.Unlock()
}

// watchProgress starts the reclaim clock for an execution-lane worker. Other
// lanes (orchestrator/analysis) return a no-op watch: an API call has no
// information-layer increments to leak, and the analysis lane never holds a
// container slot.
func (p *Pool) watchProgress(unitID, agentID, surface string) *progressWatch {
	w := &progressWatch{done: make(chan struct{})}
	if p == nil || surface != "worker" {
		return w
	}
	limit := p.cfg.WorkerProgressTimeout
	if limit <= 0 {
		return w
	}
	interval := p.cfg.ProgressCheckInterval
	if interval <= 0 {
		interval = defaultProgressCheckInterval
	}
	if interval < minProgressCheckInterval {
		interval = minProgressCheckInterval
	}
	if interval > limit {
		interval = limit
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		started := time.Now()
		activeLogged := false
		lastSpinLog := time.Time{}
		defer p.logSpinObservation(unitID, agentID, started, true)
		for {
			select {
			case <-w.done:
				return
			case <-p.ctx.Done():
				return
			case <-ticker.C:
			}
			idle := time.Since(p.lastProgressAt(agentID, started))
			if snap, ok := p.spinSnapshot(agentID, started); ok {
				// Phase-1 observation only: log the blind spot the time clock
				// cannot see (actions since the last signal, repeated failure
				// signatures). No decision reads this.
				if snap.ActionsSinceProgress >= 20 || snap.MaxRepeat >= 3 || time.Since(lastSpinLog) >= 5*time.Minute {
					log.Printf("[spin] unit=%s agent=%s actions_since_progress=%d signals=%d max_repeat=%d last_sig=%q idle_no_signal=%s",
						unitID, agentID, snap.ActionsSinceProgress, snap.Signals, snap.MaxRepeat, snap.LastSignature, snap.IdleSinceSignal.Round(time.Second))
					lastSpinLog = time.Now()
				}
			}
			if idle < limit {
				continue
			}
			// A legitimate long tool has no result to record yet: pause the
			// reclaim clock while it is actually executing. The worker budget
			// remains the hard backstop for a tool that never returns.
			if p.cfg.ScanCtx != nil && p.cfg.ScanCtx.Progress != nil {
				if name, since, ok := p.cfg.ScanCtx.Progress.ActiveTool(agentID); ok {
					deadline, _ := p.cfg.ScanCtx.Progress.ActiveToolDeadline(agentID)
					if deadline.IsZero() || time.Now().Before(deadline) {
						if !activeLogged {
							log.Printf("[orchestrator] reclaim %s paused: tool %s running for %s", unitID, name, time.Since(since).Round(time.Second))
							activeLogged = true
						}
						continue
					}
					log.Printf("[orchestrator] reclaim %s: tool %s exceeded its %s deadline; treating as stalled", unitID, name, time.Since(since).Round(time.Second))
				}
			}
			activeLogged = false
			reason := fmt.Sprintf("no information-layer progress for %s (limit %s); intent reclaimed so the slot can rotate", idle.Round(time.Second), limit)
			log.Printf("[orchestrator] reclaim candidate %s: %s", unitID, reason)
			p.steer(unitID, noProgressMessage)
			grace := p.cfg.WindDown
			if grace <= 0 {
				grace = 30 * time.Second
			}
			select {
			case <-w.done:
				return
			case <-p.ctx.Done():
				return
			case <-time.After(grace):
			}
			delivered, refused := p.killStagnant(unitID, reason)
			if delivered {
				w.setReason(reason)
				log.Printf("[orchestrator] reclaimed %s: %s", unitID, reason)
				return
			}
			if !refused {
				// The worker already exited or the kill request failed; the run
				// path settles the real status. Do not keep trying behind it.
				return
			}
			// The engine refused because the worker produced progress inside the
			// guard window. Resume watching; do not fall back to a human-labeled
			// plain cancel (round-5 bug).
			log.Printf("[orchestrator] reclaim %s deferred: the worker made progress inside the kill guard window", unitID)
			select {
			case <-w.done:
				return
			case <-p.ctx.Done():
				return
			case <-time.After(agent.KillProgressWindow):
			}
		}
	}()
	return w
}

// spinSnapshot reads the observe-only spin projection when available.
func (p *Pool) spinSnapshot(agentID string, started time.Time) (scanctx.SpinSnapshot, bool) {
	if p == nil || p.cfg.ScanCtx == nil || p.cfg.ScanCtx.Progress == nil || strings.TrimSpace(agentID) == "" {
		return scanctx.SpinSnapshot{}, false
	}
	return p.cfg.ScanCtx.Progress.SpinSnapshotFor(agentID, started, time.Now()), true
}

// logSpinObservation writes the final Phase-1 line when a worker's watch
// stops, so every lane has one comparable record even if it never crossed a
// periodic threshold.
func (p *Pool) logSpinObservation(unitID, agentID string, started time.Time, final bool) {
	if !final {
		return
	}
	snap, ok := p.spinSnapshot(agentID, started)
	if !ok {
		return
	}
	log.Printf("[spin] final unit=%s agent=%s actions_since_progress=%d signals=%d max_repeat=%d last_sig=%q elapsed=%s",
		unitID, agentID, snap.ActionsSinceProgress, snap.Signals, snap.MaxRepeat, snap.LastSignature,
		time.Since(started).Round(time.Second))
}

// lastProgressAt resolves the worker's last typed progress increment. A worker
// with no ledger entry is measured from its start time. Artifact, belief,
// evidence and coverage progress all count; an analysis worker is not reclaimed
// merely because no new InfoItem was written.
func (p *Pool) lastProgressAt(agentID string, started time.Time) time.Time {
	if p == nil || p.cfg.ScanCtx == nil || p.cfg.ScanCtx.Progress == nil || strings.TrimSpace(agentID) == "" {
		return started
	}
	if last := p.cfg.ScanCtx.Progress.LastSignalAt(agentID); !last.IsZero() {
		return last
	}
	return started
}

func (p *Pool) steer(unitID, msg string) {
	if p == nil || p.cfg.Root == nil {
		return
	}
	if steerer, ok := p.cfg.Root.(workerSteerer); ok {
		if err := steerer.SteerWorker(unitID, msg); err != nil {
			log.Printf("[orchestrator] steer %s failed: %v", unitID, err)
		}
	}
}

// killStagnant kills a reclaimed worker with the typed stagnant cause so the
// engine's own validation runs and the audit trail says why. delivered reports a
// confirmed kill request; refused reports an engine refusal due to recent
// progress (the caller must resume watching, never fall back to a plain kill).
func (p *Pool) killStagnant(unitID, reason string) (delivered, refused bool) {
	if p == nil || p.cfg.Root == nil {
		return false, false
	}
	if killer, ok := p.cfg.Root.(workerStagnantKiller); ok {
		err := killer.KillWorkerWithReason(unitID, "stagnant", reason)
		if err == nil {
			log.Printf("[orchestrator] killed stagnant worker for %s", unitID)
			return true, false
		}
		if errors.Is(err, agent.ErrStagnantKillRefused) {
			log.Printf("[orchestrator] typed stagnant kill for %s refused: %v", unitID, err)
			return false, true
		}
		// A typed root's error is authoritative. Falling back to KillWorker would
		// relabel the cause as "human" and could cancel a worker the engine just
		// decided to keep.
		log.Printf("[orchestrator] typed stagnant kill for %s failed: %v", unitID, err)
		return false, false
	}
	// Legacy/test roots only expose the plain kill path; this is the one case a
	// fallback is legitimate.
	if err := p.killWorker(unitID, reason); err != nil {
		log.Printf("[orchestrator] plain kill fallback for %s failed: %v", unitID, err)
		return false, false
	}
	return true, false
}

// platformChallengesPrefixRE finds the platform API base path in the task
// instruction/notes, so the close intent uses the endpoint the operator's
// instruction actually documented.
var platformChallengesPrefixRE = regexp.MustCompile(`(?i)/(?:openapi|api)/v\d+/challenges`)

// closeEndpoint builds the one-shot close URL for a challenge code.
func (p *Pool) closeEndpoint(code string) string {
	prefix := "/openapi/v1/challenges"
	brief := p.briefSnapshot()
	for _, text := range []string{brief.UserInstruction, brief.Instruction} {
		if m := platformChallengesPrefixRE.FindString(text); m != "" {
			prefix = m
			break
		}
	}
	return prefix + "/close?unique_code=" + url.QueryEscape(code)
}

// containerCodeFor recovers the challenge code of an engine-materialized
// containerCodeFor recovers the challenge code of an engine-materialized
// container intent ("container-attack:<code>" or "...:g<N>").
func containerCodeFor(node *graph.Node) string {
	if node == nil {
		return ""
	}
	unit := strings.ToLower(strings.TrimSpace(node.UnitKey))
	if strings.HasPrefix(unit, "container-attack:") {
		return containerGenerationSuffixRE.ReplaceAllString(strings.TrimPrefix(unit, "container-attack:"), "")
	}
	payload := node.PayloadJSON()
	if code := strings.ToLower(strings.TrimSpace(str(payload["unique_code"]))); code != "" {
		return code
	}
	return ""
}

// containerGenerationSuffixRE matches the ":g<N>" reuse suffix appended when a
// terminal container intent already used the same code/address.
var containerGenerationSuffixRE = regexp.MustCompile(`:g\d+$`)

// containerUnitGeneration parses a container unit key's generation: 0 for the
// base key, N for "<base>:g<N>". ok is false for an unrelated unit key.
func containerUnitGeneration(unit, base string) (int, bool) {
	unit = strings.TrimSpace(unit)
	if unit == base {
		return 0, true
	}
	prefix := base + ":g"
	if !strings.HasPrefix(unit, prefix) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(unit, prefix))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// enqueueContainerClose reacts to a reclaimed container intent by asking the
// control-plane lane to close the platform slot. It is idempotent per code and
// never closes a container another live intent is still attacking.
func (p *Pool) enqueueContainerClose(node *graph.Node, target graph.State) {
	if p == nil || p.cfg.Repo == nil || node == nil {
		return
	}
	if target == graph.StateDone || intentTargetKind(node) != "container" {
		return
	}
	if len(p.cfg.ControlPlaneHosts) == 0 {
		// No platform API configured: there is nothing to close.
		return
	}
	code := containerCodeFor(node)
	if code == "" {
		return
	}
	addr := strings.TrimSpace(str(node.PayloadJSON()["target"]))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nodes, err := p.cfg.Repo.ListNodes(ctx, p.cfg.ScanID, graph.KindIntent)
	if err != nil {
		return
	}
	closePrefix := "container-close:" + code
	failedCloses := 0
	for i := range nodes {
		n := &nodes[i]
		unit := strings.ToLower(strings.TrimSpace(n.UnitKey))
		if unit == closePrefix || strings.HasPrefix(unit, closePrefix+":r") {
			switch n.State {
			case graph.StateDone:
				// A close request already ran to completion; do not burn a slot
				// on a duplicate. done is a request receipt, not proof that the
				// platform released the slot (see the close intent notes).
				return
			case graph.StateOpen, graph.StateRunning, graph.StatePaused:
				// The request is already in flight.
				return
			default:
				// blocked/exhausted/stopped: the close did NOT complete, so a
				// later reclaim may retry with an attempt-suffixed unit key.
				failedCloses++
			}
			continue
		}
		if n.ID == node.ID {
			continue
		}
		payload := n.PayloadJSON()
		if intentTargetKind(n) != "container" {
			continue
		}
		if addr != "" && strings.TrimSpace(str(payload["target"])) != addr {
			continue
		}
		if n.State == graph.StateOpen || n.State == graph.StateRunning || n.State == graph.StatePaused {
			// Another lane is still working this container: closing it would
			// destroy live work. Wait for that intent to settle.
			return
		}
	}
	const maxContainerCloseAttempts = 3
	if failedCloses >= maxContainerCloseAttempts {
		log.Printf("[orchestrator] close for %s already failed %d times; not retrying", code, failedCloses)
		return
	}
	closeUnit := closePrefix
	if failedCloses > 0 {
		// Keep every attempt in the audit trail instead of overwriting a failed
		// close row or reusing a unit key the graph may treat as done.
		closeUnit = fmt.Sprintf("%s:r%d", closePrefix, failedCloses+1)
	}
	cpHost := strings.TrimSpace(p.cfg.ControlPlaneHosts[0])
	if cpHost == "" {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"task_id":     closeUnit,
		"title":       "Close challenge " + code + " and free the slot",
		"phase":       0,
		"vuln_class":  "manual",
		"endpoint":    p.closeEndpoint(code),
		"target":      cpHost,
		"target_kind": "control_plane_api",
		"origin":      "engine",
		"unique_code": code,
		"notes": "slot rotation: the attack lane for " + code + " was reclaimed without a flag. " +
			"Call the platform close endpoint exactly once, record the response in notes, then finish. Do not start another challenge from this intent.",
		"objective_ref": "GOAL",
		"contract": map[string]any{
			"version":               1,
			"timebox_s":             containerCloseTimebox,
			"requires_confirmation": false,
		},
	})
	if err != nil {
		return
	}
	if _, err := p.cfg.Repo.AddNode(ctx, graph.NodeInput{
		ScanID: p.cfg.ScanID, Kind: graph.KindIntent, UnitKey: closeUnit,
		State: graph.StateOpen, Priority: containerClosePriority,
		Origin: "engine", Payload: string(payload),
	}); err != nil {
		log.Printf("[orchestrator] enqueue close for %s failed: %v", code, err)
		return
	}
	log.Printf("[orchestrator] enqueued platform close request for %s (%s) as %s", code, addr, closeUnit)
}
