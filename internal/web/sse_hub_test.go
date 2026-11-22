package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// sseFrameData extracts the data: JSON payload of the first event in an SSE
// body (used by handler-level tests that read raw response text).
func sseFrameData(t *testing.T, body string) WSEvent {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if payload, ok := strings.CutPrefix(line, "data: "); ok {
			var evt WSEvent
			if err := json.Unmarshal([]byte(payload), &evt); err != nil {
				t.Fatalf("invalid data payload: %v", err)
			}
			return evt
		}
	}
	t.Fatalf("no data line in SSE body: %q", body)
	return WSEvent{}
}

// TestSSEFrameShape pins the wire format: id/event/data lines plus blank
// terminator, with the stream seq stamped on both the id line and the JSON.
func TestSSEFrameShape(t *testing.T) {
	s := newTestServer(t, nil)
	client := &sseClient{send: make(chan []byte, 1)}
	s.mu.Lock()
	s.clients[client] = true
	s.mu.Unlock()

	s.broadcast(WSEvent{Type: "message", Content: "hi"})

	select {
	case raw := <-client.send:
		frame := string(raw)
		if !strings.HasPrefix(frame, "id: 1\nevent: message\n") {
			t.Fatalf("frame head = %q, want id/event lines with seq 1", frame[:min(40, len(frame))])
		}
		if !strings.HasSuffix(frame, "\n\n") {
			t.Fatal("frame must end with a blank line")
		}
		evt := sseFrameEvent(t, raw)
		if evt.Seq != 1 {
			t.Fatalf("payload seq = %d, want 1", evt.Seq)
		}
		if evt.Timestamp == "" {
			t.Fatal("payload missing server timestamp")
		}
	default:
		t.Fatal("client did not receive frame")
	}
}

// TestSSEBroadcastDashboardOnlyReachesUnfiltered: dashboard-domain events
// skip clients holding an instance filter — same routing as the WS era.
func TestSSEBroadcastDashboardOnlyReachesUnfiltered(t *testing.T) {
	s := newTestServer(t, nil)
	unfiltered := &sseClient{send: make(chan []byte, 1)}
	filtered := &sseClient{send: make(chan []byte, 1), instanceID: "inst-9"}
	s.mu.Lock()
	s.clients[unfiltered] = true
	s.clients[filtered] = true
	s.mu.Unlock()

	s.broadcastDashboard(WSEvent{Type: "instance_updated", Content: "inst-9"})

	select {
	case <-unfiltered.send:
	default:
		t.Fatal("dashboard client did not receive dashboard event")
	}
	select {
	case <-filtered.send:
		t.Fatal("instance-filtered client received dashboard event")
	default:
	}
}

// TestSSERememberRingAndLastEventIDReplay: events land in the replay ring and
// Last-Event-ID (or ?since=) resumption re-delivers everything after the
// client's last seq, honoring the ?instance= filter.
func TestSSERememberRingAndLastEventIDReplay(t *testing.T) {
	s := newTestServer(t, nil)

	s.broadcast(WSEvent{Type: "message", Content: "before", InstanceID: "inst-1"})
	s.broadcastToInstance("inst-2", WSEvent{Type: "tool_result", Content: "mid"})
	last := s.evtSeq.Load()
	if last != 2 {
		t.Fatalf("seq = %d, want 2", last)
	}
	s.broadcastToInstance("inst-2", WSEvent{Type: "finished", Content: "after"})

	// Resume inst-2's stream from seq 2: only the finished frame replays.
	rec := newSyncedRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stream?instance=inst-2", nil)
	req.Header.Set("Last-Event-ID", strconv.FormatUint(last, 10))
	runStreamHandler(t, s, rec, req, func(body string) {
		evt := sseFrameData(t, body)
		if evt.Type != "finished" || evt.Content != "after" {
			t.Fatalf("replayed event = %s/%q, want finished/after", evt.Type, evt.Content)
		}
		if strings.Contains(body, "\"before\"") || strings.Contains(body, "\"mid\"") {
			t.Fatal("replay delivered events at or before Last-Event-ID")
		}
	})

	// ?since= fallback without instance filter replays both inst-2 frames.
	rec = newSyncedRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/stream?since=2", nil)
	runStreamHandler(t, s, rec, req, func(body string) {
		if strings.Count(body, "data: ") != 1 || !strings.Contains(body, "\"after\"") {
			t.Fatalf("since=2 replay body = %q, want exactly the finished frame", body)
		}
	})
}

// TestSSEHandlerInstanceFilterRejectsForeignLiveEvents proves the ?instance=
// filter also applies to the live side: a filtered connection never sees
// frames for other instances.
func TestSSEHandlerInstanceFilterRejectsForeignLiveEvents(t *testing.T) {
	s := newTestServer(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/stream?instance=inst-a", nil).WithContext(ctx)
	rec := newSyncedRecorder()

	started := make(chan struct{})
	go func() {
		close(started)
		s.handleEventStream(rec, req)
	}()
	<-started
	waitFor(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return len(s.clients) == 1
	}, "stream client registration")

	s.broadcastToInstance("inst-b", WSEvent{Type: "message", Content: "foreign"})
	s.broadcastToInstance("inst-a", WSEvent{Type: "message", Content: "mine"})

	waitFor(t, func() bool {
		return strings.Contains(rec.body(), "\"mine\"")
	}, "filtered instance frame delivery")
	if strings.Contains(rec.body(), "foreign") {
		t.Fatal("instance-filtered connection received a foreign frame")
	}
}

// TestSSESlowClientDroppedWhenBufferFull: a client that stops draining its
// send channel is removed once the buffer overflows — the SSE equivalent of
// the WS slow-client disconnect, so one stuck tab cannot stall fan-out.
func TestSSESlowClientDroppedWhenBufferFull(t *testing.T) {
	s := newTestServer(t, nil)
	stuck := &sseClient{send: make(chan []byte, 1)}
	s.mu.Lock()
	s.clients[stuck] = true
	s.mu.Unlock()

	s.broadcast(WSEvent{Type: "message", Content: "fills the buffer"})
	s.broadcast(WSEvent{Type: "message", Content: "overflows it"})

	waitFor(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return len(s.clients) == 0
	}, "slow client removal")
	// removeClient closes the channel in the same critical section that
	// removes it from the map, so draining is safe now: range consumes the
	// one buffered frame and returns on the close.
	for range stuck.send {
	}
}

// syncedRecorder is a thread-safe stand-in for httptest.ResponseRecorder: the
// SSE pump writes frames from its handler goroutine while the test goroutine
// polls the body, which races unless both sides share a lock.
type syncedRecorder struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	header http.Header
}

func newSyncedRecorder() *syncedRecorder {
	return &syncedRecorder{header: make(http.Header)}
}

func (r *syncedRecorder) Header() http.Header { return r.header }

func (r *syncedRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *syncedRecorder) WriteHeader(int) {}

func (r *syncedRecorder) Flush() {}

func (r *syncedRecorder) body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

// runStreamHandler drives the streaming handler and cancels once the
// assertion callback has seen the expected body.
func runStreamHandler(t *testing.T, s *Server, rec *syncedRecorder, req *http.Request, assert func(body string)) {
	t.Helper()
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	done := make(chan struct{})
	go func() {
		s.handleEventStream(rec, req)
		close(done)
	}()
	// Replay is synchronous inside the handler before it blocks on the pump
	// loop: once a data line shows up, the response body is complete.
	waitFor(t, func() bool {
		return strings.Contains(rec.body(), "data: ")
	}, "stream body")
	assert(rec.body())
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleEventStream did not return after cancel")
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
