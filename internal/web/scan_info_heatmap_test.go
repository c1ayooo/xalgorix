package web

// Heatmap contract tests: the subject × info-type density map is an
// aggregation form of Region. Authorization, the project/task/run whitelist and
// the cold-path gap disclosure must be identical; a reader without the
// aggregation extension is an explicit "unavailable", never a fabricated
// client-side approximation.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/infospace"
)

type fakeHeatmapReader struct {
	fakeInfoSpaceReader
	heatQ     infospace.HeatmapQuery
	heatRes   infospace.HeatmapResult
	heatErr   error
	heatCalls int
}

func (f *fakeHeatmapReader) Heatmap(_ context.Context, q infospace.HeatmapQuery) (infospace.HeatmapResult, error) {
	f.heatCalls++
	f.heatQ = q
	return f.heatRes, f.heatErr
}

func TestInfoHeatmapGraphPassthroughAndShape(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	at := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	reader := &fakeHeatmapReader{heatRes: infospace.HeatmapResult{
		Types: infospace.DefaultInfoTypes(),
		Rows: []infospace.HeatmapRow{
			{
				Subject: "api.example", SubjectKind: "host", Total: 5,
				Counts: map[infospace.InfoType]int{
					infospace.InfoTypeObservation: 3,
					infospace.InfoTypeInference:   2,
				},
				LastObserved: &at,
			},
		},
		Truncated:    true,
		SubjectTotal: 7,
		Unlabeled:    2,
		Incomplete:   true,
		GapReason:    "cold path gap",
	}}
	withInfoGraphReader(t, reader)
	s.SetInfoReader(&fakeInfoReader{})

	rr := infoRequest(t, s, "/api/info/heatmap?project_id="+projectA.ID+
		"&subject=api.example&subject_kind=host&purpose=recon&limit=5&q=api"+
		"&after=2026-11-24T09:00:00Z")
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	q := reader.heatQ
	if q.RowLimit != 5 {
		t.Fatalf("row limit passed = %d", q.RowLimit)
	}
	if q.Subject != "api.example" || q.SubjectKind != "host" || q.Purpose != "recon" || q.Query != "api" {
		t.Fatalf("filters passed = %+v", q)
	}
	if q.ObservedAfter == nil || !q.ObservedAfter.Equal(time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("after passed = %v", q.ObservedAfter)
	}
	if len(q.Types) != len(infospace.DefaultInfoTypes()) {
		t.Fatalf("types passed = %v", q.Types)
	}
	if len(q.RunIDs) != 1 || q.RunIDs[0] != "inst-a" {
		t.Fatalf("run ids passed = %v", q.RunIDs)
	}

	var body struct {
		Available    bool             `json:"available"`
		Source       string           `json:"source"`
		Types        []string         `json:"types"`
		Rows         []infoHeatmapRow `json:"rows"`
		Truncated    bool             `json:"truncated"`
		SubjectTotal int              `json:"subject_total"`
		Unlabeled    int              `json:"unlabeled"`
		Incomplete   bool             `json:"incomplete"`
		GapReason    string           `json:"gap_reason"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Available || body.Source != "graph" {
		t.Fatalf("available/source = %v/%q", body.Available, body.Source)
	}
	if len(body.Types) != 6 || body.Types[0] != "observation" ||
		body.Types[3] != "intent" || body.Types[5] != "finding" {
		t.Fatalf("types = %v", body.Types)
	}
	if len(body.Rows) != 1 {
		t.Fatalf("rows = %+v", body.Rows)
	}
	row := body.Rows[0]
	if row.Subject != "api.example" || row.SubjectKind != "host" || row.Total != 5 {
		t.Fatalf("row = %+v", row)
	}
	if row.Counts["observation"] != 3 || row.Counts["inference"] != 2 {
		t.Fatalf("counts = %v", row.Counts)
	}
	if _, ok := row.Counts["belief"]; !ok {
		t.Fatalf("zero-count types must be present: %v", row.Counts)
	}
	if _, ok := row.Counts["intent"]; !ok {
		t.Fatalf("intent stage must be present: %v", row.Counts)
	}
	if !body.Truncated || body.SubjectTotal != 7 || body.Unlabeled != 2 || !body.Incomplete || body.GapReason != "cold path gap" {
		t.Fatalf("disclosure = %+v", body)
	}
}

func TestInfoHeatmapRunScopeIsWhitelistBounded(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	reader := &fakeHeatmapReader{heatRes: infospace.HeatmapResult{
		Types: infospace.DefaultInfoTypes(), Rows: []infospace.HeatmapRow{},
	}}
	withInfoGraphReader(t, reader)

	rr := infoRequest(t, s, "/api/info/heatmap?project_id="+projectA.ID+"&scan_id=inst-b")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("foreign run: code=%d body=%s", rr.Code, rr.Body.String())
	}
	if reader.heatCalls != 0 {
		t.Fatalf("reader called for a foreign run: %+v", reader.heatQ)
	}

	rr = infoRequest(t, s, "/api/info/heatmap?project_id="+projectA.ID+"&scan_id=inst-a")
	if rr.Code != http.StatusOK {
		t.Fatalf("own run: code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(reader.heatQ.RunIDs) != 1 || reader.heatQ.RunIDs[0] != "inst-a" {
		t.Fatalf("run scope passed = %v", reader.heatQ.RunIDs)
	}
}

func TestInfoHeatmapUnavailableWithoutAggregation(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	// Region-only reader: the page must see an explicit unavailable, not a
	// silently empty map.
	withInfoGraphReader(t, &fakeInfoSpaceReader{})

	rr := infoRequest(t, s, "/api/info/heatmap?project_id="+projectA.ID)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Available bool   `json:"available"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Available || body.Reason != "heatmap_unavailable" {
		t.Fatalf("body = %+v", body)
	}
}

func TestInfoHeatmapRejectsUnknownType(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	reader := &fakeHeatmapReader{}
	withInfoGraphReader(t, reader)

	rr := infoRequest(t, s, "/api/info/heatmap?project_id="+projectA.ID+"&type=bogus")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if reader.heatCalls != 0 {
		t.Fatalf("reader called for an invalid type")
	}
}

func TestInfoRegionKindFilterPassthrough(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	reader := &fakeInfoSpaceReader{}
	withInfoGraphReader(t, reader)
	s.SetInfoReader(&fakeInfoReader{})

	rr := infoRequest(t, s, "/api/info/region?project_id="+projectA.ID+
		"&layer=aggregate&kind=belief,fact")
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(reader.regionQ.Kinds) != 2 ||
		reader.regionQ.Kinds[0] != "belief" || reader.regionQ.Kinds[1] != "fact" {
		t.Fatalf("kinds passed = %v", reader.regionQ.Kinds)
	}
}

func TestInfoSpacePayloadAdapterLiftsSummaryContent(t *testing.T) {
	atom := infoItemFromSpace(infospace.Item{
		InfoID: "atom-1",
		Payload: []byte(`{"content":{"text":"GET /api returned 200","media_type":"text/plain"},` +
			`"supports":["seed"],"confidence":0.8}`),
	})
	if atom.Coordinates.Content.Text != "GET /api returned 200" || atom.Confidence != 0.8 || len(atom.Supports) != 1 {
		t.Fatalf("atom adapter = %+v", atom)
	}

	// Aggregate payloads carry a title/summary, never a body: the drawer needs
	// the one-line summary, not a copied document.
	belief := infoItemFromSpace(infospace.Item{
		InfoID:  "belief-1",
		Payload: []byte(`{"title":"IDOR candidate","summary":"orders differ by user scope"}`),
	})
	if belief.Coordinates.Content.Text != "orders differ by user scope" {
		t.Fatalf("aggregate summary = %q", belief.Coordinates.Content.Text)
	}
	titleOnly := infoItemFromSpace(infospace.Item{
		InfoID:  "fact-1",
		Payload: []byte(`{"title":"sqli response"}`),
	})
	if titleOnly.Coordinates.Content.Text != "sqli response" {
		t.Fatalf("aggregate title fallback = %q", titleOnly.Coordinates.Content.Text)
	}
}
