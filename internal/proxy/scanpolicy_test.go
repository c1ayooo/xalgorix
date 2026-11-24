package proxy

import "testing"

func TestScanUpstreamPinAndClear(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	SetScanUpstream("scan-a", ScanUpstream{Direct: true})
	up, ok := ScanUpstreamFor("scan-a")
	if !ok || !up.Direct || up.Entry != nil {
		t.Fatalf("direct pin = %+v ok=%v", up, ok)
	}
	entry := &Proxy{Raw: "http://127.0.0.1:18080", Type: ProxyTypeHTTP, Host: "127.0.0.1", Port: "18080"}
	SetScanUpstream("scan-b", ScanUpstream{Entry: entry})
	up, ok = ScanUpstreamFor("scan-b")
	if !ok || up.Entry != entry {
		t.Fatalf("entry pin = %+v ok=%v", up, ok)
	}
	ClearScanUpstream("scan-b")
	if _, ok := ScanUpstreamFor("scan-b"); ok {
		t.Fatal("clear did not remove the pin")
	}
}

func TestEntriesAndEntryAtUseActivePool(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	if entries := Entries(); len(entries) != 0 {
		t.Fatalf("no manager must yield no entries, got %v", entries)
	}
	if err := InitWithPolicy(true, false, "http://127.0.0.1:18080", "", "roundrobin", 0); err != nil {
		t.Fatalf("init: %v", err)
	}
	entries := Entries()
	if len(entries) != 1 || entries[0].Index != 1 || entries[0].Label == "" {
		t.Fatalf("entries = %+v", entries)
	}
	if EntryAt(1) == nil {
		t.Fatal("entry 1 missing")
	}
	if EntryAt(2) != nil {
		t.Fatal("out-of-range index must return nil")
	}
}

func TestEgressEnvFollowsPinAndFailClosed(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	if err := InitWithPolicy(true, false, "http://127.0.0.1:18080", "", "roundrobin", 0); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Unpinned run inherits the global pool.
	env, err := EgressEnv("unpinned", false)
	if err != nil || len(env) == 0 {
		t.Fatalf("global egress env = %v, %v", env, err)
	}
	// Pinned entry wins over the global pool.
	entry := &Proxy{Raw: "http://10.9.9.9:3128", Type: ProxyTypeHTTP, Host: "10.9.9.9", Port: "3128"}
	SetScanUpstream("pinned", ScanUpstream{Entry: entry})
	env, err = EgressEnv("pinned", false)
	if err != nil {
		t.Fatalf("pinned env: %v", err)
	}
	found := false
	for _, kv := range env {
		if kv == "HTTP_PROXY=http://10.9.9.9:3128" {
			found = true
		}
	}
	if !found {
		t.Fatalf("pinned egress missing from env: %v", env)
	}

	// Explicit direct (not required) yields no env.
	SetScanUpstream("direct", ScanUpstream{Direct: true})
	if env, err := EgressEnv("direct", false); err != nil || len(env) != 0 {
		t.Fatalf("direct env = %v, %v", env, err)
	}

	// Required mode + a direct pin fails closed even though the pool exists.
	if _, err := EgressEnv("direct", true); err == nil {
		t.Fatal("required mode must reject a direct pin")
	}
	// Required mode with no manager at all (cfg required, restart pending)
	// also fails closed via the explicit required flag.
	Reset()
	if _, err := EgressEnv("no-pool", true); err == nil {
		t.Fatal("required mode with no upstream must error")
	}
}
