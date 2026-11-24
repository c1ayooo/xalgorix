package portscan

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func TestPortScanHonorsTargetBackoff(t *testing.T) {
	sc := scanctx.New("portscan-backoff", "")
	scanctx.Activate(sc)
	t.Cleanup(func() { scanctx.Deactivate(sc.ID) })
	sc.SetTargetBackoff(time.Now().Add(time.Minute), "HTTP 429")

	res, err := Execute(context.Background(), sc.ID, map[string]string{"target": "10.0.0.5", "ports": "top2"})
	if err != nil {
		t.Fatalf("backoff must be soft, got %v", err)
	}
	if !strings.Contains(res.Output, "TARGET BACKOFF") {
		t.Fatalf("output = %q", res.Output)
	}
	if res.Error != "" {
		t.Fatalf("backoff must not trip the breaker: %q", res.Error)
	}
}
