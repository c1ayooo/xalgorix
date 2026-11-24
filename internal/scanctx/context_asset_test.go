package scanctx

import "testing"

func TestMarkAssetSeenDeduplicatesPerSession(t *testing.T) {
	sc := New("scan-assets", t.TempDir())
	if !sc.MarkAssetSeen("endpoint", "/login") {
		t.Fatal("first sight must be new")
	}
	if sc.MarkAssetSeen("endpoint", "/login") {
		t.Fatal("repeat must not be new")
	}
	if !sc.MarkAssetSeen("endpoint", "/admin") {
		t.Fatal("a different key must be new")
	}
	if sc.MarkAssetSeen("", "") {
		t.Fatal("empty identity must be rejected")
	}
}

func TestMarkAssetSeenNilContext(t *testing.T) {
	var sc *ScanContext
	if sc.MarkAssetSeen("endpoint", "/x") {
		t.Fatal("nil context must report not-new")
	}
}
