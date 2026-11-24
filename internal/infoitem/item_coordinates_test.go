package infoitem

import "testing"

func TestNormalizeDerivesPurposeFromEngineSource(t *testing.T) {
	item := Item{
		Coordinates: Coordinates{
			Content: Content{Text: "GET /admin 200"},
			Source:  Source{Kind: "trafficproxy"},
			Subject: Subject{Kind: "endpoint", Identifier: "GET /admin"},
		},
	}
	item.Normalize()
	if item.Coordinates.Purpose != "traffic_observation" {
		t.Fatalf("purpose = %q, want traffic_observation", item.Coordinates.Purpose)
	}
	if item.CoordinateStatus != CoordinatesComplete {
		t.Fatalf("status = %q, want complete", item.CoordinateStatus)
	}
}

func TestNormalizeUnknownSourceWithoutPurposeIsPartial(t *testing.T) {
	item := Item{
		Coordinates: Coordinates{
			Content: Content{Text: "something observed"},
			Source:  Source{Kind: "mystery"},
			Subject: Subject{Kind: "endpoint", Identifier: "/x"},
		},
	}
	item.Normalize()
	if item.Coordinates.Purpose != "" {
		t.Fatalf("purpose = %q, want empty", item.Coordinates.Purpose)
	}
	if item.CoordinateStatus != CoordinatesPartial {
		t.Fatalf("status = %q, want partial", item.CoordinateStatus)
	}
}

func TestNormalizePreservesExplicitPendingStatus(t *testing.T) {
	item := Item{
		CoordinateStatus: CoordinatesPending,
		Coordinates: Coordinates{
			Content: Content{Text: "awaiting extraction"},
			Source:  Source{Kind: "trafficproxy"},
			Subject: Subject{Kind: "endpoint", Identifier: "/x"},
		},
	}
	item.Normalize()
	if item.CoordinateStatus != CoordinatesPending {
		t.Fatalf("status = %q, want pending preserved", item.CoordinateStatus)
	}
}

func TestClassifyCoordinateStatusPending(t *testing.T) {
	if got := (Item{}).ClassifyCoordinateStatus(); got != CoordinatesPending {
		t.Fatalf("empty item status = %q, want pending", got)
	}
}
