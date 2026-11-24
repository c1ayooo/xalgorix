package scanctx

import (
	"path/filepath"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/domainjournal"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
)

func TestAppendGuidanceObservationProjectsObservedInfoItem(t *testing.T) {
	ls := NewLedgerStore()
	ls.SetScanID("scan-guidance")
	dir := t.TempDir()
	if err := ls.EnableJournal(dir); err != nil {
		t.Fatalf("enable journal: %v", err)
	}

	obs := GuidanceObservation{
		ID:         "g-1",
		TaskID:     "task-7",
		RunID:      "run-1",
		InstanceID: "inst-1",
		CreatedBy:  "alice",
		Message:    "focus on /admin first",
	}
	if err := ls.AppendGuidanceObservation(obs); err != nil {
		t.Fatalf("append guidance: %v", err)
	}
	if err := ls.AppendGuidanceObservation(obs); err != nil {
		t.Fatalf("replayed append: %v", err)
	}

	records, corrupt, err := domainjournal.ReadRecords(filepath.Join(dir, domainjournal.FileName))
	if err != nil || corrupt {
		t.Fatalf("read journal: err=%v corrupt=%v", err, corrupt)
	}
	var delivered []*infoitem.Item
	for i := range records {
		if records[i].Operation != domainjournal.OpRecordGuidance {
			continue
		}
		if records[i].InfoItem == nil {
			t.Fatal("guidance record has no InfoItem")
		}
		delivered = append(delivered, records[i].InfoItem)
	}
	if len(delivered) != 2 {
		t.Fatalf("guidance records = %d, want 2", len(delivered))
	}
	for _, item := range delivered {
		if item.ID != infoitem.StableGuidanceID("scan-guidance", "g-1") {
			t.Fatalf("id = %q, want stable guidance id", item.ID)
		}
		if item.Nature != infoitem.NatureObserved {
			t.Fatalf("nature = %q, want observed", item.Nature)
		}
		if item.Coordinates.Source.Kind != "human" {
			t.Fatalf("source kind = %q, want human", item.Coordinates.Source.Kind)
		}
		if item.Coordinates.Purpose != "operator_guidance" {
			t.Fatalf("purpose = %q", item.Coordinates.Purpose)
		}
		if item.Coordinates.Subject.Identifier != "task-7" {
			t.Fatalf("subject = %q", item.Coordinates.Subject.Identifier)
		}
		if item.Coordinates.Context["created_by"] != "alice" {
			t.Fatalf("created_by context = %q", item.Coordinates.Context["created_by"])
		}
	}
}

func TestAppendGuidanceObservationRejectsMissingID(t *testing.T) {
	ls := NewLedgerStore()
	ls.SetScanID("scan-guidance")
	if err := ls.AppendGuidanceObservation(GuidanceObservation{Message: "no id"}); err == nil {
		t.Fatal("expected missing guidance id to fail")
	}
}
