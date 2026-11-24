package web

// Project-graph contract tests: subject-level relation aggregates must use the
// same project/task/run whitelist as the heatmap, disclose unavailability
// instead of returning an empty graph, and carry the real relation category
// (chain vs information association).

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/infospace"
)

type fakeRelationsReader struct {
	fakeInfoSpaceReader
	relQ     infospace.RelationsQuery
	relRes   infospace.RelationsResult
	relErr   error
	relCalls int
}

func (f *fakeRelationsReader) Relations(_ context.Context, q infospace.RelationsQuery) (infospace.RelationsResult, error) {
	f.relCalls++
	f.relQ = q
	return f.relRes, f.relErr
}

func TestInfoRelationsGraphPassthroughAndShape(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	at := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	reader := &fakeRelationsReader{relRes: infospace.RelationsResult{
		Edges: []infospace.RelationEdge{
			{
				SrcSubject: "10.0.1.5:8080", SrcKind: "container",
				DstSubject: "10.0.2.10:22", DstKind: "container",
				Rel: "yields", Category: infospace.RelationChain, Count: 3,
				SrcTitle: "pivot via SOCKS tunnel", DstTitle: "tunnel live", LastAt: &at,
			},
			{
				SrcSubject: "api.example.com", SrcKind: "host",
				DstSubject: "admin.example.com", DstKind: "host",
				Rel: "references", Category: infospace.RelationInfo, Count: 1,
			},
		},
		Truncated:  true,
		Incomplete: true,
		GapReason:  "cold path gap",
	}}
	withInfoGraphReader(t, reader)

	rr := infoRequest(t, s, "/api/info/relations?project_id="+projectA.ID+
		"&scan_id=inst-a&after=2026-11-24T09:00:00Z&limit=50")
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if reader.relQ.Limit != 50 {
		t.Fatalf("limit passed = %d", reader.relQ.Limit)
	}
	if reader.relQ.ObservedAfter == nil {
		t.Fatal("after not passed")
	}
	if len(reader.relQ.RunIDs) != 1 || reader.relQ.RunIDs[0] != "inst-a" {
		t.Fatalf("run ids passed = %v", reader.relQ.RunIDs)
	}

	var body infoRelationsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Available || body.Source != "graph" || len(body.Edges) != 2 {
		t.Fatalf("body = %+v", body)
	}
	if body.Edges[0].Rel != "yields" || body.Edges[0].Category != "chain" || body.Edges[0].Count != 3 {
		t.Fatalf("chain edge = %+v", body.Edges[0])
	}
	if body.Edges[0].SrcTitle != "pivot via SOCKS tunnel" || body.Edges[0].DstTitle != "tunnel live" {
		t.Fatalf("titles = %+v", body.Edges[0])
	}
	if body.Edges[1].Category != "info" {
		t.Fatalf("info edge = %+v", body.Edges[1])
	}
	if !body.Truncated || !body.Incomplete || body.GapReason != "cold path gap" {
		t.Fatalf("disclosure = %+v", body)
	}
}

func TestInfoRelationsUnavailableWithoutReader(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	withInfoGraphReader(t, &fakeInfoSpaceReader{})

	rr := infoRequest(t, s, "/api/info/relations?project_id="+projectA.ID)
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
	if body.Available || body.Reason != "relations_unavailable" {
		t.Fatalf("body = %+v", body)
	}
}

func TestInfoRelationsRejectsOversizedLimit(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	reader := &fakeRelationsReader{}
	withInfoGraphReader(t, reader)

	rr := infoRequest(t, s, "/api/info/relations?project_id="+projectA.ID+"&limit=801")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if reader.relCalls != 0 {
		t.Fatalf("reader called for an invalid limit")
	}
}

func TestInfoRelationsRejectsForeignRun(t *testing.T) {
	s, projectA, _ := infoTestServer(t)
	reader := &fakeRelationsReader{}
	withInfoGraphReader(t, reader)

	rr := infoRequest(t, s, "/api/info/relations?project_id="+projectA.ID+"&scan_id=inst-b")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if reader.relCalls != 0 {
		t.Fatalf("reader called for a foreign run")
	}
}
