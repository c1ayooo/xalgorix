package httpclient

import (
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func TestHTTPRequestHonorsTargetBackoff(t *testing.T) {
	sc := scanctx.New("httpclient-backoff", "")
	scanctx.Activate(sc)
	t.Cleanup(func() { scanctx.Deactivate(sc.ID) })
	sc.SetTargetBackoff(time.Now().Add(time.Minute), "HTTP 429 (Retry-After 60s)")

	res, err := executeWithContext(sc.ID, nil, nil, map[string]string{"url": "http://127.0.0.1:1/never"})
	if err != nil {
		t.Fatalf("backoff must be a soft result, got error %v", err)
	}
	if !strings.Contains(res.Output, "TARGET BACKOFF") {
		t.Fatalf("output = %q", res.Output)
	}
	if res.Error != "" {
		t.Fatalf("backoff must not set Result.Error (circuit breaker): %q", res.Error)
	}
}

func TestHTTPRequestHonorsEgressStop(t *testing.T) {
	sc := scanctx.New("httpclient-egress", "")
	scanctx.Activate(sc)
	t.Cleanup(func() { scanctx.Deactivate(sc.ID) })
	sc.SetEgressPolicy(scanctx.EgressPolicy{MaxConcurrency: 1})
	sc.Egress().Stop("operator stop")

	res, err := executeWithContext(sc.ID, nil, nil, map[string]string{"url": "http://127.0.0.1:1/never"})
	if err != nil {
		t.Fatalf("egress stop must be a soft result, got error %v", err)
	}
	if !strings.Contains(res.Output, "EGRESS POLICY") || !strings.Contains(res.Output, "stopped") {
		t.Fatalf("output = %q", res.Output)
	}
	if res.Error != "" {
		t.Fatalf("egress stop must not trip the breaker: %q", res.Error)
	}
}
