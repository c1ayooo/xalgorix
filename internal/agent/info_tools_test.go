package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

type fakeInfoReader struct {
	regionQuery      infoitem.RegionQuery
	associationQuery infoitem.AssociationQuery
	contextQuery     infoitem.ContextQuery
	item             infoitem.Item
}

func (f *fakeInfoReader) Region(_ context.Context, q infoitem.RegionQuery) (infoitem.RegionResult, error) {
	f.regionQuery = q
	return infoitem.RegionResult{Items: []infoitem.Item{f.item}}, nil
}

func (f *fakeInfoReader) Associate(_ context.Context, q infoitem.AssociationQuery) (infoitem.AssociationResult, error) {
	f.associationQuery = q
	return infoitem.AssociationResult{Hits: []infoitem.AssociationHit{{Item: f.item, Path: []string{"ii-2", "ii-1"}, Depth: 1}}}, nil
}

func (f *fakeInfoReader) ContextSlice(_ context.Context, q infoitem.ContextQuery) (infoitem.ContextResult, error) {
	f.contextQuery = q
	return infoitem.ContextResult{Items: []infoitem.Item{f.item}}, nil
}

func TestSearchInfoToolUsesCurrentScanAndIsReadOnly(t *testing.T) {
	reader := &fakeInfoReader{item: infoitem.Item{
		ID:     "ii-1",
		ScanID: "scan-info",
		Nature: infoitem.NatureObserved,
		Coordinates: infoitem.Coordinates{
			Content: infoitem.Content{Text: "anonymous request returned 401"},
			Subject: infoitem.Subject{Kind: "endpoint", Identifier: "/api/orders"},
			Purpose: "authorization review",
		},
	}}
	cancel := infoitem.RegisterReader(reader)
	defer cancel()

	sc := scanctx.New("scan-info", "")
	defer sc.Close()
	a := &Agent{scanCtx: sc, ctx: context.Background()}

	res, err := a.searchInfoTool(map[string]string{
		"mode": "region", "subject": "/api/orders", "nature": "observed", "limit": "5",
	})
	if err != nil || res.Error != "" {
		t.Fatalf("region search: result=%+v err=%v", res, err)
	}
	if len(reader.regionQuery.ScanIDs) != 1 || reader.regionQuery.ScanIDs[0] != "scan-info" {
		t.Fatalf("query escaped current scan: %+v", reader.regionQuery)
	}
	if !strings.Contains(res.Output, "not finding proof") {
		t.Fatalf("missing verdict-boundary reminder: %s", res.Output)
	}

	res, err = a.searchInfoTool(map[string]string{
		"mode": "association", "ids": "ii-2", "max_depth": "2",
	})
	if err != nil || res.Error != "" {
		t.Fatalf("association search: result=%+v err=%v", res, err)
	}
	if reader.associationQuery.ScanID != "scan-info" || len(reader.associationQuery.StartIDs) != 1 {
		t.Fatalf("unexpected association query: %+v", reader.associationQuery)
	}
	if !strings.Contains(res.Output, "ii-2 -> ii-1") {
		t.Fatalf("missing path in output: %s", res.Output)
	}
}

func TestSearchInfoToolReportsDisabledColdPath(t *testing.T) {
	// Ensure no other registration is active in this test binary.
	cancel := infoitem.RegisterReader(nil)
	cancel()
	sc := scanctx.New("scan-disabled", "")
	defer sc.Close()
	a := &Agent{scanCtx: sc}
	res, err := a.searchInfoTool(map[string]string{"mode": "region"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" || !strings.Contains(res.Output, "disabled") {
		t.Fatalf("expected disabled explanation, got %+v", res)
	}
}
