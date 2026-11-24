package assets

import "testing"

func TestInputsFromURL(t *testing.T) {
	got := InputsFromURL("p1", "https://API.Example.com/a/b?x=1")
	if len(got) != 2 {
		t.Fatalf("inputs = %d, want 2: %+v", len(got), got)
	}
	if got[0].Kind != KindSubdomain || got[0].Key != "api.example.com" {
		t.Fatalf("host asset = %+v", got[0])
	}
	if got[1].Kind != KindEndpoint || got[1].Key != "/a/b" {
		t.Fatalf("endpoint asset = %+v", got[1])
	}
}

func TestInputsFromURLRejectsNonNetworkPaths(t *testing.T) {
	if got := InputsFromURL("p1", "/etc/passwd"); len(got) != 1 || got[0].Kind != KindEndpoint {
		t.Fatalf("a bare path is an endpoint identity, got %+v", got)
	}
	if got := InputsFromURL("p1", "not a url"); len(got) != 0 {
		t.Fatalf("free text must not become an asset: %+v", got)
	}
}

func TestInputsFromURLRootDomain(t *testing.T) {
	got := InputsFromURL("p1", "http://example.com/")
	if len(got) != 1 || got[0].Kind != KindRootDomain {
		t.Fatalf("got %+v, want one root_domain", got)
	}
}
