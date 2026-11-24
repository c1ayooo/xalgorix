package infoitem

import "testing"

func TestRank_DeterministicAndBounded(t *testing.T) {
	docs := []Doc{
		{ID: "E-2", Title: "SQL injection on /login", Summary: "error-based sqli confirmed"},
		{ID: "E-1", Title: "Login page probe", Summary: "SQL injection baseline"},
		{ID: "E-3", Title: "XSS on /search", Summary: "reflected"},
	}
	hits := Rank("SQL injection login", docs, 2)
	if len(hits) != 2 {
		t.Fatalf("hits = %d", len(hits))
	}
	if hits[0].ID != "E-2" && hits[0].ID != "E-1" {
		t.Fatalf("unexpected top hit: %+v", hits[0])
	}
	again := Rank("SQL injection login", docs, 2)
	if again[0].ID != hits[0].ID || again[1].ID != hits[1].ID {
		t.Fatalf("non-deterministic: %+v vs %+v", hits, again)
	}
	if Rank("completely unrelated", docs, 5) != nil {
		t.Fatal("unrelated query should return nil")
	}
}

func TestScore_TitleOutweighsSummary(t *testing.T) {
	title := Score("idor orders", "IDOR on /orders", "")
	summary := Score("idor orders", "", "IDOR on /orders")
	if title <= summary {
		t.Fatalf("title=%f summary=%f", title, summary)
	}
}

func TestTokenSet_CJK(t *testing.T) {
	set := tokenSet("登录接口 存在SQL注入")
	for _, want := range []string{"登", "录", "接", "口", "sql", "注", "入"} {
		if !set[want] {
			t.Fatalf("missing token %q in %v", want, set)
		}
	}
}
