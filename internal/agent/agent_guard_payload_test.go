package agent

// agent_guard_payload_test.go — pins the SSRF payload-data refinement: a
// loopback token in VALUE position with an allowed destination present is
// data the target may fetch (not a local request); any destination-position
// loopback still blocks.

import (
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
)

func payloadGuardAgent(t *testing.T) *Agent {
	t.Helper()
	ctx, state := newTestCtxState(t)
	a := NewAgent(&config.Config{MaxIterations: 10}, "payload-guard", make(chan Event, 16), scopeguard.Config{}, ctx)
	t.Cleanup(a.Stop)
	a.state = state
	return a
}

func TestOutOfScopeAllowsLoopbackPayloadData(t *testing.T) {
	a := payloadGuardAgent(t)
	cases := []map[string]string{
		{"command": `curl -sk "http://10.0.179.225/api/import?url=http://127.0.0.1:8080/"`},
		{"command": `curl -s -X POST http://10.0.179.226/api -d '{"target":"127.0.0.1:22"}'`},
		{"command": `python3 -c "import urllib.request; urllib.request.urlopen('http://10.0.179.225/x?h=127.0.0.1')"`},
	}
	for _, args := range cases {
		if blocked, reason := a.shouldBlockForOutOfScope("terminal_execute", args); blocked {
			t.Fatalf("payload-data loopback must not block when an allowed destination exists: %v -> %s", args, reason)
		}
	}
}

func TestOutOfScopeStillBlocksLoopbackDestinations(t *testing.T) {
	a := payloadGuardAgent(t)
	cases := []map[string]string{
		{"command": "curl -s http://127.0.0.1:9137/api/projects"},                        // destination
		{"command": "nc 127.0.0.1 22"},                                                   // destination
		{"command": `curl "http://10.0.179.225/?u=http://127.0.0.1"; curl http://127.0.0.1/`}, // one payload, one destination
		{"command": "echo x=127.0.0.1"},                                                  // payload but NO allowed destination
		{"command": "python3 -c \"import socket; socket.create_connection(('127.0.0.1',22))\""}, // destination
	}
	for _, args := range cases {
		blocked, _ := a.shouldBlockForOutOfScope("terminal_execute", args)
		if !blocked {
			t.Fatalf("destination-position loopback must still block: %v", args)
		}
	}
	if blocked, _ := a.shouldBlockForOutOfScope("terminal_execute", map[string]string{"command": "grep -n 'x' notes.txt"}); blocked {
		t.Fatal("hostless command must pass")
	}
	_ = strings.Contains
}
