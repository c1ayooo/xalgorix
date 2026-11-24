package infoitem

import (
	"strings"
	"testing"
	"time"
)

func sampleItem() Item {
	return Item{
		ID:     "i-1",
		ScanID: "s-1",
		Nature: NatureObserved,
		Coordinates: Coordinates{
			Content:    Content{Text: "# Login\nusername=admin\nline two\nline three"},
			Source:     Source{Kind: "tool", Name: "http_request", AgentID: "worker-1", EvidenceID: "E-1"},
			ObservedAt: time.Unix(1700000000, 0).UTC(),
			Subject:    Subject{Kind: "endpoint", Identifier: "/login"},
			Purpose:    "probe",
			Context:    map[string]string{"method": "POST"},
		},
		Supports:   []string{"E-0"},
		Confidence: 0.8,
	}
}

func TestIndex_BoundsAndFlags(t *testing.T) {
	item := sampleItem()
	entry := Index(item, 10, 20)
	if entry.ID != "i-1" || entry.Nature != NatureObserved || entry.Subject != "/login" {
		t.Fatalf("entry = %+v", entry)
	}
	if !entry.HasDetail || !entry.HasArtifact {
		t.Fatalf("flags = detail:%v artifact:%v", entry.HasDetail, entry.HasArtifact)
	}
	if len([]rune(entry.Summary)) > 21 { // 20 + ellipsis
		t.Fatalf("summary not bounded: %q", entry.Summary)
	}
	if !entry.Truncated {
		t.Fatal("truncated flag not set")
	}
	if strings.Contains(entry.Summary, "\n") {
		t.Fatalf("summary kept newlines: %q", entry.Summary)
	}
}

func TestDetailOf_Bounded(t *testing.T) {
	item := sampleItem()
	item.Coordinates.Content.Text = strings.Repeat("x", MaxDetailRunes+10)
	d := DetailOf(item, 100)
	if len([]rune(d.Text)) > 101 {
		t.Fatalf("detail not bounded: %d", len([]rune(d.Text)))
	}
	if !d.Truncated || !strings.HasSuffix(d.Text, "…") {
		t.Fatalf("detail truncated=%v text=%q", d.Truncated, d.Text)
	}
	if len(d.Supports) != 1 || d.Source.AgentID != "worker-1" {
		t.Fatalf("detail metadata = %+v", d)
	}
}

func TestArtifactOf_ReferenceOnly(t *testing.T) {
	ref := ArtifactOf(sampleItem())
	if ref.EvidenceID != "E-1" || ref.InfoItemID != "i-1" || ref.Kind != "tool" {
		t.Fatalf("artifact = %+v", ref)
	}
	if strings.Contains(ref.Name, "username") {
		t.Fatalf("artifact leaked content: %+v", ref)
	}
}

func TestNormalizeNature(t *testing.T) {
	if NormalizeNature("") != NatureInferred || NormalizeNature("weird") != NatureInferred {
		t.Fatal("unknown nature must normalize to inferred")
	}
	if NormalizeNature(NatureObserved) != NatureObserved {
		t.Fatal("observed nature lost")
	}
}
