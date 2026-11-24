package web

import "testing"

func TestOrderedTaskLLMChainPutsActiveFirst(t *testing.T) {
	got := orderedTaskLLMChain([]string{"openai:a", "anthropic:b", "openai:c"}, "anthropic:b")
	want := []string{"anthropic:b", "openai:a", "openai:c"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestOrderedTaskLLMChainEmptyActiveKeepsOrder(t *testing.T) {
	got := orderedTaskLLMChain([]string{"openai:a", "openai:a", "anthropic:b"}, "")
	if len(got) != 2 || got[0] != "openai:a" || got[1] != "anthropic:b" {
		t.Fatalf("dedupe/order failed: %v", got)
	}
}
