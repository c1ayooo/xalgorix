package llm

import (
	"context"
	"testing"
)

func TestChainResolverAdvancesInOrderThenStops(t *testing.T) {
	r := NewChainResolver([]Endpoint{{URL: "https://a", Model: "ma"}, {URL: "https://b", Model: "mb"}})
	if r.Len() != 2 || r.Current() != 0 {
		t.Fatalf("len/current = %d/%d", r.Len(), r.Current())
	}
	ep, err := r.Resolve(context.Background())
	if err != nil || ep.URL != "https://a" {
		t.Fatalf("first resolve = %+v, %v", ep, err)
	}
	if !r.Advance("quota_exhausted") {
		t.Fatal("advance to the second entry failed")
	}
	ep, _ = r.Resolve(context.Background())
	if ep.URL != "https://b" {
		t.Fatalf("second resolve = %+v", ep)
	}
	if r.Advance("auth") {
		t.Fatal("advance past the chain end must report false")
	}
	ep, _ = r.Resolve(context.Background())
	if ep.URL != "https://b" {
		t.Fatalf("exhausted chain must keep the last entry, got %+v", ep)
	}
}

func TestChainResolverEmptyErrors(t *testing.T) {
	r := NewChainResolver(nil)
	if _, err := r.Resolve(context.Background()); err == nil {
		t.Fatal("empty chain must error instead of returning a zero endpoint")
	}
}

func TestChainResolverSetEndpointsHotSwaps(t *testing.T) {
	r := NewChainResolver([]Endpoint{{URL: "https://a"}})
	ep, _ := r.Resolve(context.Background())
	if ep.URL != "https://a" {
		t.Fatalf("initial endpoint = %+v", ep)
	}
	r.SetEndpoints([]Endpoint{{URL: "https://b"}, {URL: "https://c"}})
	ep, _ = r.Resolve(context.Background())
	if ep.URL != "https://b" {
		t.Fatalf("hot swap did not apply: %+v", ep)
	}
	// Shrinking the chain clamps the index instead of panicking.
	r.Advance("x")
	r.SetEndpoints([]Endpoint{{URL: "https://only"}})
	ep, _ = r.Resolve(context.Background())
	if ep.URL != "https://only" {
		t.Fatalf("shrink clamp failed: %+v", ep)
	}
}

func TestClientSetChainEndpointsOnlyForChainResolver(t *testing.T) {
	c := NewClient(nil, WithResolver(NewFixedResolver(Endpoint{URL: "https://fixed"})))
	if c.SetChainEndpoints([]Endpoint{{URL: "https://x"}}) {
		t.Fatal("fixed resolver must report false")
	}
	chain := NewChainResolver([]Endpoint{{URL: "https://a"}})
	c2 := NewClient(nil, WithResolver(chain))
	if !c2.SetChainEndpoints([]Endpoint{{URL: "https://b"}}) {
		t.Fatal("chain resolver must accept the swap")
	}
	ep, _ := chain.Resolve(context.Background())
	if ep.URL != "https://b" {
		t.Fatalf("client swap did not reach the resolver: %+v", ep)
	}
}
