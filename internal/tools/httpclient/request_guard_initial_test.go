package httpclient

import (
	"fmt"
	"strings"
	"testing"
)

// The request guard must run on the initial destination, not only redirect
// hops: otherwise an orchestrator/API lane can call a container URL directly,
// and follow_redirects=false bypasses the guard entirely.
func TestRequestGuardBlocksInitialDestination(t *testing.T) {
	guard := func(raw string) error { return fmt.Errorf("host %q is not allowed", raw) }
	res, err := executeWithContext("", guard, nil, map[string]string{
		"url":              "http://127.0.0.1:1/secret",
		"follow_redirects": "false",
	})
	if err != nil {
		t.Fatalf("guard rejection must be a tool result, not an error: %v", err)
	}
	if !strings.Contains(res.Output, "REQUEST NOT SENT") || !strings.Contains(res.Output, "127.0.0.1") {
		t.Fatalf("guard did not block the initial destination: %s", res.Output)
	}
}
