package llm

import (
	"fmt"
	"testing"
)

func resetFailoverEvents() {
	keyFailoverLog.mu.Lock()
	keyFailoverLog.events = keyFailoverLog.events[:0]
	keyFailoverLog.mu.Unlock()
}

func TestKeyFailoverRingKeepsNewestFirstAndFingerprintsOnly(t *testing.T) {
	resetFailoverEvents()
	t.Cleanup(resetFailoverEvents)

	for i := 0; i < keyFailoverRingSize+5; i++ {
		RecordKeyFailover("rate_limit", fmt.Sprintf("sk-from-%02d", i), fmt.Sprintf("sk-to-%02d", i))
	}
	events := KeyFailoverEvents()
	if len(events) != keyFailoverRingSize {
		t.Fatalf("ring length = %d, want %d", len(events), keyFailoverRingSize)
	}
	if events[0].From != KeyFingerprint(fmt.Sprintf("sk-from-%02d", keyFailoverRingSize+4)) {
		t.Fatalf("newest event first: got %q", events[0].From)
	}
	for _, ev := range events {
		if ev.From == "" || ev.To == "" {
			t.Fatalf("event missing fingerprints: %+v", ev)
		}
		if len(ev.From) == len("sk-from-00") {
			t.Fatalf("event leaked a raw key: %+v", ev)
		}
	}
}
