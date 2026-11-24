package web

// IG1b: the project-scoped info read APIs accept an optional layer filter and
// prefer the unified information space (infospace.Reader) when the graph
// authority provides it. Tests inject a bounded fake through the
// package-level infoGraphReader seam, so no PG DSN and no process-wide graph
// backend are required.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/infospace"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

type fakeInfoSpaceReader struct {
	regionQ    infospace.RegionQuery
	contextQ   infospace.ContextQuery
	assocCalls []infospace.AssociationQuery
	regionRes  infospace.RegionResult
	contextRes infospace.ContextResult
	assocRes   infospace.AssociationResult
	assocByRun map[string]infospace.AssociationResult
	err        error
}

func (f *fakeInfoSpaceReader) Region(_ context.Context, q infospace.RegionQuery) (infospace.RegionResult, error) {
	f.regionQ = q
	return f.regionRes, f.err
}

func (f *fakeInfoSpaceReader) ContextSlice(_ context.Context, q infospace.ContextQuery) (infospace.ContextResult, error) {
	f.contextQ = q
	return f.contextRes, f.err
}

func (f *fakeInfoSpaceReader) Associate(_ context.Context, q infospace.AssociationQuery) (infospace.AssociationResult, error) {
	f.assocCalls = append(f.assocCalls, q)
	if res, ok := f.assocByRun[q.RunID]; ok {
		return res, f.err
	}
	return f.assocRes, f.err
}

// withInfoGraphReader swaps the graph-reader seam for one test and restores it.
func withInfoGraphReader(t *testing.T, reader infospace.Reader) {
	t.Helper()
	prev := infoGraphReader
	infoGraphReader = func() infospace.Reader { return reader }
	t.Cleanup(func() { infoGraphReader = prev })
}

func TestInfoLayerParamInvalidIsRejected(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	graphReader := &fakeInfoSpaceReader{}
	cold := &fakeInfoReader{}
	withInfoGraphReader(t, graphReader)
	s.SetInfoReader(cold)

	for _, path := range []string{
		"/api/info/region?project_id=" + projectA.ID + "&layer=bogus",
		"/api/info/context?project_id=" + projectA.ID + "&scan_id=inst-a&layer=bogus",
		"/api/info/association?project_id=" + projectA.ID + "&ids=i1&layer=bogus",
	} {
		rr := infoRequest(t, s, path)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d body=%s", path, rr.Code, rr.Body.String())
		}
	}
	if graphReader.regionQ.Limit != 0 || graphReader.contextQ.Limit != 0 || len(graphReader.assocCalls) != 0 {
		t.Fatalf("graph reader called for invalid layer: %+v %+v %+v",
			graphReader.regionQ, graphReader.contextQ, graphReader.assocCalls)
	}
	if cold.regionQ.Limit != 0 || cold.contextQ.Limit != 0 || len(cold.assocCalls) != 0 {
		t.Fatalf("cold reader called for invalid layer: %+v %+v %+v",
			cold.regionQ, cold.contextQ, cold.assocCalls)
	}
}

func TestInfoRegionGraphLayerPassthroughAndShape(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	observedAt := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	graphReader := &fakeInfoSpaceReader{regionRes: infospace.RegionResult{
		Items: []infospace.Item{
			{
				ScanID: "inst-a", RunID: "inst-a", InfoID: "ii-2", Layer: infospace.LayerAtom,
				Kind: "info_item", Nature: "observed", Subject: "api.example", SubjectKind: "host",
				Purpose: "recon", Origin: "tool", ObservedAt: observedAt,
				Context:   []byte(`{"phase":"recon","hits":3}`),
				Payload:   []byte(`{"supports":["ii-1"],"confidence":0.75}`),
				CreatedAt: observedAt,
			},
			{
				ScanID: "inst-a", RunID: "inst-a", InfoID: "ii-1", Layer: infospace.LayerAggregate,
				Kind: "intent", ObservedAt: observedAt.Add(-time.Minute), CreatedAt: observedAt,
			},
		},
		Truncated:  true,
		NextCursor: "2026-11-22T10:00:00Z|ii-1",
	}}
	withInfoGraphReader(t, graphReader)
	cold := &fakeInfoReader{}
	s.SetInfoReader(cold)

	rr := infoRequest(t, s, "/api/info/region?project_id="+projectA.ID+
		"&layer=atom&subject=api.example&subject_kind=host&purpose=recon&nature=observed"+
		"&after=2026-11-22T09:00:00Z&limit=10")
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	q := graphReader.regionQ
	if q.Layer != infospace.LayerAtom {
		t.Fatalf("layer passed to graph = %q", q.Layer)
	}
	if len(q.RunIDs) != 1 || q.RunIDs[0] != "inst-a" {
		t.Fatalf("run ids = %v", q.RunIDs)
	}
	if q.Subject != "api.example" || q.SubjectKind != "host" || q.Purpose != "recon" || q.Nature != infoitem.NatureObserved {
		t.Fatalf("filters = %+v", q)
	}
	if q.Limit != 10 || q.ObservedAfter == nil {
		t.Fatalf("bounds = %+v", q)
	}
	if cold.regionQ.Limit != 0 {
		t.Fatalf("cold path queried while graph served: %+v", cold.regionQ)
	}

	var body struct {
		Available  bool   `json:"available"`
		Layer      string `json:"layer"`
		Source     string `json:"source"`
		Truncated  bool   `json:"truncated"`
		HasMore    bool   `json:"has_more"`
		NextCursor string `json:"next_cursor"`
		Items      []struct {
			ID          string   `json:"id"`
			ScanID      string   `json:"scan_id"`
			Nature      string   `json:"nature"`
			Supports    []string `json:"supports"`
			Confidence  float64  `json:"confidence"`
			Coordinates struct {
				Subject struct {
					Kind       string `json:"kind"`
					Identifier string `json:"identifier"`
				} `json:"subject"`
				Purpose string            `json:"purpose"`
				Context map[string]string `json:"context"`
				Source  struct {
					Kind string `json:"kind"`
				} `json:"source"`
			} `json:"coordinates"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Available || body.Layer != "atom" || body.Source != "graph" {
		t.Fatalf("envelope = %+v", body)
	}
	if !body.Truncated || !body.HasMore || body.NextCursor != "2026-11-22T10:00:00Z|ii-1" {
		t.Fatalf("truncation metadata = %+v", body)
	}
	if len(body.Items) != 2 || body.Items[0].ID != "ii-2" || body.Items[1].ID != "ii-1" {
		t.Fatalf("reader order not preserved: %+v", body.Items)
	}
	first := body.Items[0]
	if first.ScanID != "inst-a" || first.Nature != "observed" ||
		first.Coordinates.Subject.Identifier != "api.example" || first.Coordinates.Subject.Kind != "host" ||
		first.Coordinates.Purpose != "recon" || first.Coordinates.Source.Kind != "tool" {
		t.Fatalf("item mapping = %+v", first)
	}
	if len(first.Supports) != 1 || first.Supports[0] != "ii-1" || first.Confidence != 0.75 {
		t.Fatalf("payload summary = %+v", first)
	}
	if first.Coordinates.Context["phase"] != "recon" || first.Coordinates.Context["hits"] != "3" {
		t.Fatalf("context mapping = %+v", first.Coordinates.Context)
	}

	// No layer parameter means the whole space: the zero value reaches the
	// graph reader and the envelope discloses "all".
	rr = infoRequest(t, s, "/api/info/region?project_id="+projectA.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if graphReader.regionQ.Layer != "" {
		t.Fatalf("default layer = %q, want empty (both layers)", graphReader.regionQ.Layer)
	}
	var all struct {
		Layer string `json:"layer"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if all.Layer != "all" {
		t.Fatalf("default layer echo = %q", all.Layer)
	}
}

func TestInfoRegionGraphUnavailableFallsBackToColdPath(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	cold := &fakeInfoReader{regionRes: infoitem.RegionResult{
		Items: []infoitem.Item{{ID: "atom-1", ScanID: "inst-a"}},
	}}
	s.SetInfoReader(cold)
	withInfoGraphReader(t, &fakeInfoSpaceReader{err: errors.New("graph: information space unavailable")})

	rr := infoRequest(t, s, "/api/info/region?project_id="+projectA.ID+"&layer=atom")
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(cold.regionQ.ScanIDs) != 1 || cold.regionQ.ScanIDs[0] != "inst-a" {
		t.Fatalf("cold path scope = %v", cold.regionQ.ScanIDs)
	}
	var body struct {
		Available bool   `json:"available"`
		Source    string `json:"source"`
		Layer     string `json:"layer"`
		Items     []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Available || body.Source != "cold_path" || body.Layer != "atom" ||
		len(body.Items) != 1 || body.Items[0].ID != "atom-1" {
		t.Fatalf("fallback body = %+v", body)
	}
}

// A graph failure that is not "backend absent" must be surfaced, not hidden by
// silently serving an atom-only cold-path page.
func TestInfoRegionGraphQueryFailureIsNotSilentlyDowngraded(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	cold := &fakeInfoReader{}
	s.SetInfoReader(cold)
	withInfoGraphReader(t, &fakeInfoSpaceReader{err: errors.New("graph: region query: boom")})

	rr := infoRequest(t, s, "/api/info/region?project_id="+projectA.ID)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["available"] != false || body["reason"] != "graph_query_failed" {
		t.Fatalf("body = %+v", body)
	}
	if cold.regionQ.Limit != 0 {
		t.Fatalf("cold path queried after graph failure: %+v", cold.regionQ)
	}
}

func TestInfoAggregateLayerWithoutGraphIsExplicit(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	cold := &fakeInfoReader{}
	s.SetInfoReader(cold)
	withInfoGraphReader(t, nil)

	for _, path := range []string{
		"/api/info/region?project_id=" + projectA.ID + "&layer=aggregate",
		"/api/info/context?project_id=" + projectA.ID + "&scan_id=inst-a&layer=aggregate",
		"/api/info/association?project_id=" + projectA.ID + "&ids=i1&layer=aggregate",
	} {
		rr := infoRequest(t, s, path)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: code=%d body=%s", path, rr.Code, rr.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["available"] != false || body["reason"] != "layer_unavailable" || body["layer"] != "aggregate" {
			t.Fatalf("%s: body = %+v", path, body)
		}
	}
	if cold.regionQ.Limit != 0 || cold.contextQ.Limit != 0 || len(cold.assocCalls) != 0 {
		t.Fatalf("cold path queried for aggregate layer: %+v %+v %+v",
			cold.regionQ, cold.contextQ, cold.assocCalls)
	}

	// atom/all keep the existing cold-path behavior.
	rr := infoRequest(t, s, "/api/info/region?project_id="+projectA.ID+"&layer=all")
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Source string `json:"source"`
		Layer  string `json:"layer"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Source != "cold_path" || body.Layer != "all" {
		t.Fatalf("cold envelope = %+v", body)
	}
}

func TestInfoContextGraphUsesRunIdentityAndLayer(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	observedAt := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	graphReader := &fakeInfoSpaceReader{contextRes: infospace.ContextResult{
		Items: []infospace.Item{{
			ScanID: "inst-a", RunID: "inst-a", InfoID: "ii-9", Layer: infospace.LayerAggregate,
			Kind: "belief", ObservedAt: observedAt, CreatedAt: observedAt,
		}},
	}}
	withInfoGraphReader(t, graphReader)
	s.SetInfoReader(&fakeInfoReader{})

	rr := infoRequest(t, s, "/api/info/context?project_id="+projectA.ID+
		"&scan_id=inst-a&layer=aggregate&subject=api.example&limit=5")
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	q := graphReader.contextQ
	if q.RunID != "inst-a" || q.Layer != infospace.LayerAggregate || q.Subject != "api.example" || q.Limit != 5 {
		t.Fatalf("context query = %+v", q)
	}
	var body struct {
		Layer  string `json:"layer"`
		Source string `json:"source"`
		Items  []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Layer != "aggregate" || body.Source != "graph" || len(body.Items) != 1 || body.Items[0].ID != "ii-9" {
		t.Fatalf("context body = %+v", body)
	}
}

func TestInfoAssociationGraphUsesRunIDsAndDedupes(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	store := s.currentPlatformStore()
	ctx := context.Background()
	task, err := store.CreateTask(ctx, projectA.ID, platform.TaskInput{Name: "Graph second run"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: "inst-a2", Mode: "single"}); err != nil {
		t.Fatal(err)
	}
	atomHit := func(scanID string) infospace.AssociationHit {
		return infospace.AssociationHit{
			Item: infospace.Item{
				ScanID: scanID, RunID: scanID, InfoID: "ii-2", Layer: infospace.LayerAtom,
				Kind: "info_item", Nature: "observed",
			},
			Path: []string{"ii-1", "ii-2"}, Rels: []string{"supports"}, Depth: 1,
		}
	}
	aggregateHit := infospace.AssociationHit{
		Item: infospace.Item{
			ScanID: "inst-a", RunID: "inst-a", InfoID: "ii-9", Layer: infospace.LayerAggregate,
			Kind: "intent",
		},
		Path: []string{"ii-1", "ii-9"}, Rels: []string{"yields"}, Depth: 1,
	}
	graphReader := &fakeInfoSpaceReader{assocByRun: map[string]infospace.AssociationResult{
		"inst-a":  {Hits: []infospace.AssociationHit{atomHit("inst-a"), aggregateHit}},
		"inst-a2": {Hits: []infospace.AssociationHit{atomHit("inst-a2")}},
	}}
	withInfoGraphReader(t, graphReader)
	s.SetInfoReader(&fakeInfoReader{})

	rr := infoRequest(t, s, "/api/info/association?project_id="+projectA.ID+"&ids=ii-1&layer=atom&max_depth=2&limit=10")
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(graphReader.assocCalls) != 2 {
		t.Fatalf("associate calls = %d (%+v)", len(graphReader.assocCalls), graphReader.assocCalls)
	}
	seenRuns := map[string]bool{}
	for _, q := range graphReader.assocCalls {
		seenRuns[q.RunID] = true
		if q.MaxDepth != 2 || q.Limit != 10 || len(q.StartIDs) != 1 || q.StartIDs[0] != "ii-1" {
			t.Fatalf("bounded traversal params not preserved: %+v", q)
		}
		if len(q.EdgeTypes) != 0 {
			t.Fatalf("edge types invented: %+v", q.EdgeTypes)
		}
	}
	if !seenRuns["inst-a"] || !seenRuns["inst-a2"] {
		t.Fatalf("graph queries missed whitelisted runs: %+v", graphReader.assocCalls)
	}
	var body struct {
		Layer          string `json:"layer"`
		Source         string `json:"source"`
		ScansConsulted int    `json:"scans_consulted"`
		Hits           []struct {
			Item struct {
				ID string `json:"id"`
			} `json:"item"`
			Path  []string `json:"path"`
			Rels  []string `json:"rels"`
			Depth int      `json:"depth"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Layer != "atom" || body.Source != "graph" || body.ScansConsulted != 2 {
		t.Fatalf("envelope = %+v", body)
	}
	// The aggregate hit is filtered out by layer=atom and the duplicate atom
	// hit from the second run is deduped on the item id.
	if len(body.Hits) != 1 || body.Hits[0].Item.ID != "ii-2" {
		t.Fatalf("hits = %+v", body.Hits)
	}
	if len(body.Hits[0].Rels) != 1 || body.Hits[0].Rels[0] != "supports" || body.Hits[0].Depth != 1 {
		t.Fatalf("hit path metadata = %+v", body.Hits[0])
	}

	// Without a layer filter both hits survive, in first-seen (deterministic)
	// order, and the duplicate across runs is still deduped.
	rr = infoRequest(t, s, "/api/info/association?project_id="+projectA.ID+"&ids=ii-1&limit=10")
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var allBody struct {
		Hits []struct {
			Item struct {
				ID string `json:"id"`
			} `json:"item"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &allBody); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(allBody.Hits))
	for _, hit := range allBody.Hits {
		ids = append(ids, hit.Item.ID)
	}
	if len(ids) != 2 || ids[0] != "ii-2" || ids[1] != "ii-9" {
		t.Fatalf("unfiltered, deduped order = %v", ids)
	}
}

func TestInfoAssociationGraphUnavailableFallsBackToColdPath(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	cold := &fakeInfoReader{assocRes: infoitem.AssociationResult{
		Hits: []infoitem.AssociationHit{{
			Item: infoitem.Item{ID: "ii-2"}, Path: []string{"ii-1", "ii-2"}, Depth: 1,
		}},
	}}
	s.SetInfoReader(cold)
	withInfoGraphReader(t, &fakeInfoSpaceReader{err: errors.New("graph: information space unavailable")})

	rr := infoRequest(t, s, "/api/info/association?project_id="+projectA.ID+"&ids=ii-1&layer=atom")
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(cold.assocCalls) != 1 || cold.assocCalls[0].ScanID != "inst-a" {
		t.Fatalf("cold traversal = %+v", cold.assocCalls)
	}
	var body struct {
		Source string `json:"source"`
		Hits   []struct {
			Item struct {
				ID string `json:"id"`
			} `json:"item"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Source != "cold_path" || len(body.Hits) != 1 || body.Hits[0].Item.ID != "ii-2" {
		t.Fatalf("fallback body = %+v", body)
	}
}

func TestInfoRunIDsPreservesWhitelistAndDedupes(t *testing.T) {
	s, _, _ := infoTestServer(t)
	got := s.infoRunIDs([]string{"inst-a", "inst-a", " ", "inst-b"})
	if len(got) != 2 || got[0] != "inst-a" || got[1] != "inst-b" {
		t.Fatalf("run ids = %v", got)
	}
}
