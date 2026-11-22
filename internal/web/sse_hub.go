package web

// sse_hub.go — the dashboard event stream over Server-Sent Events.
//
// Replaces the former /ws WebSocket hub (2026-11-22): the frontend only ever
// consumed the connection one-way (receive events, filter by instance), so
// SSE matches the semantics and keeps the stream inside plain HTTP — the gzip
// middleware, auth middleware and any future proxy layer no longer need a
// connection-hijack special case. Machine clients lost the WS scan-launch
// channel by design (user decision): they start scans via POST /api/scan and
// consume events via REST polling (GET /api/instances/{id}/events).
//
// Ordering / resumption: every fanned-out event is stamped with a monotonic
// per-process seq and buffered into a bounded ring. SSE frames carry
// `id: <seq>`, so EventSource reconnects resume via Last-Event-ID from the
// ring window. Process restarts drop the ring — the dashboard's REST history
// merge (mergeFeedEvents) is the documented fallback (API_CONTRACT §2).
//
// Event flow stays one-way (server → client). Nothing here reads from the
// request body; instance filtering is fixed per connection via ?instance=.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// SSE keepalive / backpressure settings (WS-era semantics preserved).
	ssePingInterval = 30 * time.Second
	sseWriteWait    = 10 * time.Second
	sseMaxClients   = 50
	sseSendBufSize  = 512 // buffered channel size per client
	// sseRingSize bounds the Last-Event-ID replay window. Beyond it, clients
	// fall back to REST history endpoints — same recovery path as a restart.
	sseRingSize = 1000
)

// sseRingEntry is one fanned-out event kept for Last-Event-ID replay.
type sseRingEntry struct {
	seq        uint64
	instanceID string // target instance, empty for dashboard-domain events
	frame      []byte // complete SSE frame (id/event/data lines)
}

// sseClient wraps one streaming response with a buffered send channel.
//
// Concurrency: the struct mirrors the old wsClient discipline — clients live
// in server.clients and instanceID filters fan-out. instanceID is set once at
// connect (?instance=) and never mutated, so only map membership needs
// server.mu. The lock also guards iteration over server.clients during
// fan-out and the channel close in removeClient (send on closed channel is
// the failure mode it prevents).
type sseClient struct {
	send       chan []byte
	server     *Server
	instanceID string // immutable per connection; empty = dashboard scope
	cancel     context.CancelFunc
	w          http.ResponseWriter
	flusher    http.Flusher
}

// sseFrame renders one event as a complete SSE frame. The JSON payload is
// single-line by construction (encoding/json escapes control characters), so
// the data: line cannot split the framing.
func sseFrame(seq uint64, evt WSEvent) []byte {
	data, err := json.Marshal(evt)
	if err != nil {
		return nil
	}
	var b bytes.Buffer
	b.WriteString("id: ")
	b.WriteString(strconv.FormatUint(seq, 10))
	b.WriteString("\nevent: ")
	b.WriteString(evt.Type)
	b.WriteString("\ndata: ")
	b.Write(data)
	b.WriteString("\n\n")
	return b.Bytes()
}

// stampStreamEvent assigns the timestamp and the global stream seq. Seq is
// stamped only here — never on events that are persisted into snapshots
// (exactStopEvent et al.) — so the field means "live-stream order".
func (s *Server) stampStreamEvent(evt WSEvent) (uint64, WSEvent, []byte) {
	evt = withEventTimestamp(evt)
	seq := s.evtSeq.Add(1)
	evt.Seq = seq
	return seq, evt, sseFrame(seq, evt)
}

// rememberRing keeps the frame for Last-Event-ID replay.
func (s *Server) rememberRing(seq uint64, instanceID string, frame []byte) {
	if frame == nil {
		return
	}
	s.streamMu.Lock()
	s.streamRing = append(s.streamRing, sseRingEntry{seq: seq, instanceID: instanceID, frame: frame})
	if len(s.streamRing) > sseRingSize {
		s.streamRing = s.streamRing[len(s.streamRing)-sseRingSize:]
	}
	s.streamMu.Unlock()
}

// fanout queues the frame to matching clients. A client whose send buffer is
// full is dropped — the SSE equivalent of the old slow-client disconnect, so
// one stuck dashboard tab can never stall the event pipeline.
func (s *Server) fanout(frame []byte, match func(*sseClient) bool) {
	if frame == nil {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for client := range s.clients {
		if !match(client) {
			continue
		}
		select {
		case client.send <- frame:
			// queued successfully
		default:
			go func(c *sseClient) {
				s.removeClient(c)
			}(client)
		}
	}
}

// removeClient safely removes a client from the server's client set. Closing
// send under the lock guarantees fan-out never queues onto a closed channel.
func (s *Server) removeClient(c *sseClient) {
	s.mu.Lock()
	if _, ok := s.clients[c]; ok {
		delete(s.clients, c)
		close(c.send)
	}
	s.mu.Unlock()
	if c.cancel != nil {
		// Streaming clients always carry a cancel func (handleEventStream);
		// bare test clients may omit it.
		c.cancel()
	}
}

func (s *Server) broadcast(evt WSEvent) {
	seq, evt, frame := s.stampStreamEvent(evt)
	s.rememberRing(seq, evt.InstanceID, frame)
	s.fanout(frame, func(*sseClient) bool { return true })
}

// broadcastToInstance sends an event to clients subscribed to a specific
// instance and to dashboard clients without an instance subscription.
// Buffers events into the instance for replay.
func (s *Server) broadcastToInstance(instanceID string, evt WSEvent) {
	if evt.InstanceID == "" {
		evt.InstanceID = instanceID
	}
	seq, evt, frame := s.stampStreamEvent(evt)

	// Buffer event into instance for replay (cap at 500)
	s.instancesMu.RLock()
	if inst, ok := s.instances[instanceID]; ok {
		inst.mu.Lock()
		if len(inst.events) < 500 {
			inst.events = append(inst.events, evt)
		} else {
			// Keep last 400, drop oldest
			inst.events = append(inst.events[100:], evt)
		}
		// Also buffer vulns
		if len(evt.Vulns) > 0 {
			for _, vuln := range evt.Vulns {
				applySummaryReplacement(&inst.Vulns, &vuln, metadataBool(evt.ResultMeta, "upgraded"))
				appendVulnSummaryUnique(&inst.Vulns, vuln)
			}
		}
		if evt.CurrentPhase > 0 {
			inst.CurrentPhase = evt.CurrentPhase
		}
		inst.mu.Unlock()
	}
	s.instancesMu.RUnlock()

	s.rememberRing(seq, evt.InstanceID, frame)
	s.fanout(frame, func(c *sseClient) bool {
		return c.instanceID == "" || c.instanceID == instanceID
	})
}

// notifyInstanceListeners delivers an event that is already present in the
// coherent instance snapshot. Unlike broadcastToInstance it never buffers the
// event again, preventing exact-stop retries from duplicating replay history.
func (s *Server) notifyInstanceListeners(instanceID string, evt WSEvent) {
	if evt.InstanceID == "" {
		evt.InstanceID = instanceID
	}
	seq, _, frame := s.stampStreamEvent(evt)
	s.rememberRing(seq, evt.InstanceID, frame)
	s.fanout(frame, func(c *sseClient) bool {
		return c.instanceID == "" || c.instanceID == instanceID
	})
}

// broadcastDashboard sends an event only to dashboard clients (no instance subscription).
func (s *Server) broadcastDashboard(evt WSEvent) {
	seq, _, frame := s.stampStreamEvent(evt)
	s.rememberRing(seq, evt.InstanceID, frame)
	s.fanout(frame, func(c *sseClient) bool { return c.instanceID == "" })
}

// handleEventStream serves GET /api/stream (text/event-stream). The handler
// goroutine is the write pump: it drains the send channel, flushes after every
// frame and emits keepalive comments. Auth is enforced by authMiddleware
// (cookie session); EventSource cannot send Bearer headers, which is why
// machine-token clients are pointed at REST polling instead.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	s.mu.RLock()
	numClients := len(s.clients)
	s.mu.RUnlock()
	if numClients >= sseMaxClients {
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	// Best-effort proxy hint: nginx-style buffers must not hold SSE frames.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx, cancel := context.WithCancel(r.Context())
	client := &sseClient{
		send:       make(chan []byte, sseSendBufSize),
		server:     s,
		instanceID: strings.TrimSpace(r.URL.Query().Get("instance")),
		cancel:     cancel,
		w:          w,
		flusher:    flusher,
	}

	// Replay the ring window after the client's last received seq. This runs
	// before registration so replayed and live frames can never interleave.
	if last := lastEventSeq(r); last > 0 {
		s.streamMu.Lock()
		for _, entry := range s.streamRing {
			if entry.seq <= last {
				continue
			}
			if client.instanceID != "" && entry.instanceID != client.instanceID {
				continue
			}
			rcSetWriteDeadline(w)
			if _, err := w.Write(entry.frame); err != nil {
				s.streamMu.Unlock()
				cancel()
				return
			}
		}
		s.streamMu.Unlock()
		flusher.Flush()
	}

	s.mu.Lock()
	s.clients[client] = true
	s.mu.Unlock()

	ticker := time.NewTicker(ssePingInterval)
	defer ticker.Stop()
	for {
		select {
		case frame, ok := <-client.send:
			if !ok {
				// removeClient closed the channel (slow-client drop).
				return
			}
			rcSetWriteDeadline(w)
			if _, err := w.Write(frame); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			rcSetWriteDeadline(w)
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}

// rcSetWriteDeadline applies the per-frame write deadline via
// http.NewResponseController. Unsupported underlying writers (tests) are
// fine — the deadline is best-effort backpressure protection.
func rcSetWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(sseWriteWait))
}

// lastEventSeq resolves the client's resume point: the Last-Event-ID header
// EventSource sends automatically on reconnect, with ?since= as an explicit
// (curl-friendly) fallback. 0 = no resume, start live.
func lastEventSeq(r *http.Request) uint64 {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("since"))
	}
	if raw == "" {
		return 0
	}
	seq, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return seq
}

func withEventTimestamp(evt WSEvent) WSEvent {
	if strings.TrimSpace(evt.Timestamp) == "" {
		evt.Timestamp = time.Now().Format(time.RFC3339)
	}
	return evt
}
