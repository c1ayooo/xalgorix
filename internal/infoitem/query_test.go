package infoitem

import (
	"testing"
	"time"
)

func TestParseRegionCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 22, 10, 0, 0, 123456789, time.UTC)
	original := RegionCursor{ObservedAt: at, ID: "ii-1"}

	parsed, err := ParseRegionCursor(original.String())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed == nil || !parsed.ObservedAt.Equal(at) || parsed.ID != "ii-1" {
		t.Fatalf("parsed cursor = %+v, want %+v", parsed, original)
	}
	if parsed.String() != original.String() {
		t.Fatalf("round trip = %q, want %q", parsed.String(), original.String())
	}
}

func TestParseRegionCursorEmptyIsFirstPage(t *testing.T) {
	// An absent cursor is the first page, not an error: the web layer passes
	// the raw query parameter straight through.
	for _, raw := range []string{"", "   "} {
		cursor, err := ParseRegionCursor(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if cursor != nil {
			t.Fatalf("parse %q = %+v, want nil", raw, cursor)
		}
	}
}

func TestParseRegionCursorRejectsMalformed(t *testing.T) {
	for _, raw := range []string{
		"no-separator",
		"2026-11-22T10:00:00Z|",
		"2026-11-22T10:00:00Z|   ",
		"not-a-time|ii-1",
	} {
		if _, err := ParseRegionCursor(raw); err == nil {
			t.Fatalf("parse %q: expected error", raw)
		}
	}
}

func TestRegionQueryValidateRejectsIncompleteCursor(t *testing.T) {
	q := RegionQuery{
		ScanIDs: []string{"scan-a"},
		Cursor:  &RegionCursor{ID: "ii-1"},
	}
	if err := q.Validate(); err == nil {
		t.Fatal("expected an incomplete cursor to be rejected")
	}
}
