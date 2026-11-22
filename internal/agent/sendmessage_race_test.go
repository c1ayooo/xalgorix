package agent

import (
	"strings"
	"sync"
	"testing"
)

// TestSendMessage_ConcurrentWithMessageSnapshot is a regression test
// for the data race fixed alongside the scope-guard work: the agent
// loop used to read a.messages directly when calling client.Chat,
// while SendMessage appends to the conversation from the web chat
// handler on another goroutine. A concurrent append that reallocates
// the backing array mid-serialization is a data race.
//
// The delivery seam has since moved: the Eino ADK owns the
// conversation state, so SendMessage now appends to the
// pending-injection queue (guarded by the same msgMu) which
// BeforeTurn drains into the next turn nudge. This test drives the
// same two access patterns concurrently — the locked snapshot the
// drain path performs, and the locked append SendMessage performs —
// and must run cleanly under `go test -race`. It also asserts no
// appended message is lost.
func TestSendMessage_ConcurrentWithMessageSnapshot(t *testing.T) {
	a := &Agent{
		// Buffered events channel so emit() never blocks the
		// SendMessage goroutine during the test.
		events: make(chan Event, 4096),
	}

	const senders = 8
	const perSender = 200

	var wg sync.WaitGroup

	// Reader goroutines mimic the drain path: copy the queue under
	// msgMu and iterate the copy.
	stop := make(chan struct{})
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				a.msgMu.Lock()
				snap := make([]string, len(a.pendingInjects))
				copy(snap, a.pendingInjects)
				a.msgMu.Unlock()
				// Touch every element to surface torn reads under -race.
				total := 0
				for i := range snap {
					total += len(snap[i])
				}
				_ = total
			}
		}()
	}

	// Writer goroutines append via SendMessage concurrently.
	for s := 0; s < senders; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perSender; i++ {
				if _, err := a.SendMessage("ping"); err != nil {
					t.Errorf("SendMessage returned error: %v", err)
					return
				}
			}
		}()
	}

	// Wait until all appends land, then stop the readers and join.
	want := senders * perSender
	for {
		a.msgMu.Lock()
		n := len(a.pendingInjects)
		a.msgMu.Unlock()
		if n >= want {
			break
		}
	}
	close(stop)
	wg.Wait()

	a.msgMu.Lock()
	got := len(a.pendingInjects)
	a.msgMu.Unlock()
	if got != want {
		t.Fatalf("queue length = %d, want %d (appends lost under concurrency)", got, want)
	}
	if joined := a.drainPendingInjects(); len(strings.Split(joined, "\n\n")) != want {
		t.Fatalf("drain lost messages: got %d joined parts, want %d", len(strings.Split(joined, "\n\n")), want)
	}
}
