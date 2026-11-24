package web

import (
	"context"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
)

func TestLatestDigestBriefingReadsActiveDigest(t *testing.T) {
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })

	node, err := store.UpsertNode(context.Background(), graph.NodeInput{
		ScanID: "run-1", Kind: graph.KindDigest, UnitKey: "dg-1", State: graph.StateActive,
		Payload: `{"region":"lane:agent:a","snapshot":"## Dead ends\n- H-2 rejected xss"}`,
	})
	if err != nil {
		t.Fatalf("digest node: %v", err)
	}

	rec := &ScanRecord{ID: "run-1", InstanceID: "run-1"}
	out := latestDigestBriefing(rec, "scan-1")
	if !strings.Contains(out, "## Dead ends") || !strings.Contains(out, "H-2 rejected xss") {
		t.Fatalf("digest briefing missing snapshot: %q", out)
	}

	if _, err := store.Transition(context.Background(), "run-1", node.ID, graph.KindDigest, graph.StateActive, graph.StateSuperseded); err != nil {
		t.Fatalf("supersede: %v", err)
	}
	if got := latestDigestBriefing(rec, "scan-1"); got != "" {
		t.Fatalf("superseded digest leaked into resume briefing: %q", got)
	}
}

func TestLatestDigestBriefingIsBounded(t *testing.T) {
	store := graph.NewMemoryStore()
	graph.SetBackend(store)
	t.Cleanup(func() { graph.SetBackend(nil) })

	big := strings.Repeat("x", 40*1024)
	if _, err := store.UpsertNode(context.Background(), graph.NodeInput{
		ScanID: "run-2", Kind: graph.KindDigest, UnitKey: "dg-big", State: graph.StateActive,
		Payload: `{"region":"lane:agent:a","snapshot":"` + big + `"}`,
	}); err != nil {
		t.Fatalf("digest node: %v", err)
	}
	out := latestDigestBriefing(&ScanRecord{ID: "run-2"}, "scan-2")
	if len(out) > 13*1024 {
		t.Fatalf("resume digest %d bytes exceeds the 12 KiB budget", len(out))
	}
}
