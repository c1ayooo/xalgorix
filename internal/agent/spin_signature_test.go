package agent

import "testing"

func TestSpinSignatureCollapsesRetries(t *testing.T) {
	a := map[string]string{"tool_name": "terminal_execute", "error": "exit status 7", "output": "curl: connection refused"}
	b := map[string]string{"tool_name": "terminal_execute", "error": "exit status 7", "output": "dial tcp 10.0.0.9:8443: connect: connection refused"}
	if got, want := spinSignature("terminal_execute", a), spinSignature("terminal_execute", b); got != want {
		t.Fatalf("retry signatures differ: %q vs %q", got, want)
	}
}

func TestSpinSignatureExtractsTargetAndStatus(t *testing.T) {
	got := spinSignature("http_request", map[string]string{
		"url": "https://10.0.177.18:8443/login?next=/", "output": "HTTP/1.1 404 Not Found",
	})
	if want := "http_request|10.0.177.18:8443|404"; got != want {
		t.Fatalf("signature = %q, want %q", got, want)
	}
	cmd := spinSignature("terminal_execute", map[string]string{"command": "curl -s http://10.0.177.18:80/", "output": "welcome"})
	if want := "terminal_execute|10.0.177.18:80|ok"; cmd != want {
		t.Fatalf("command signature = %q, want %q", cmd, want)
	}
}
