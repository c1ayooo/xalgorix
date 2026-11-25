package postgres

import (
	"math"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
)

// TestClampEdgeWeight pins the mapping from infoitem.Rank scores onto the
// info_item_edges.weight column. Rank is deliberately unnormalised (title hit
// weighs 2, exact substring adds 1), so values above 1 are the normal case for
// a strong match, not an error — before this clamp they violated
// info_item_edges_weight_check and aborted the whole relation pass.
func TestClampEdgeWeight(t *testing.T) {
	cases := []struct {
		in     float64
		want   float64
		wantOK bool
		why    string
	}{
		{0.7, 0.7, true, "an in-range lexical score is preserved"},
		{1, 1, true, "the upper bound is legal"},
		{1.8, 1, true, "a strong title match clamps to the maximum strength"},
		{4, 1, true, "the maximum possible Rank score clamps to 1"},
		{-0.2, 0, true, "a negative score clamps to the minimum strength"},
		{0, 0, true, "zero is legal"},
		{math.NaN(), 0, false, "NaN is not a weight and the edge is dropped"},
		{math.Inf(1), 0, false, "+Inf is not a weight and the edge is dropped"},
		{math.Inf(-1), 0, false, "-Inf is not a weight and the edge is dropped"},
	}
	for _, tc := range cases {
		got, ok := clampEdgeWeight(tc.in)
		if ok != tc.wantOK {
			t.Fatalf("clampEdgeWeight(%v) ok = %v, want %v (%s)", tc.in, ok, tc.wantOK, tc.why)
		}
		if ok && got != tc.want {
			t.Fatalf("clampEdgeWeight(%v) = %v, want %v (%s)", tc.in, got, tc.want, tc.why)
		}
	}
}

// TestRankScoresExceedOne documents why the clamp exists: run the real ranker
// on a query that hits the title twice and confirm it leaves the [0,1] range.
func TestRankScoresExceedOne(t *testing.T) {
	score := infoitem.Score(
		"sql injection login",
		"SQL injection login form",
		"direct sql injection on the login form",
	)
	if score <= 1 {
		t.Fatalf("expected the ranker to score above 1 for a strong match, got %v — "+
			"if this changes, revisit clampEdgeWeight", score)
	}
}
