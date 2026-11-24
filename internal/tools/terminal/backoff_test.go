package terminal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func TestTerminalHonorsTargetBackoff(t *testing.T) {
	sc := scanctx.New("terminal-backoff", "")
	scanctx.Activate(sc)
	t.Cleanup(func() { scanctx.Deactivate(sc.ID) })
	sc.SetTargetBackoff(time.Now().Add(time.Minute), "HTTP 429")

	out, code := runShellInternalContext(context.Background(), sc.ID, "echo SHOULD_NOT_RUN")
	if code != 0 {
		t.Fatalf("exit code = %d, want soft 0", code)
	}
	if !strings.Contains(out, "TARGET BACKOFF") {
		t.Fatalf("output = %q", out)
	}
	if strings.Contains(out, "SHOULD_NOT_RUN") {
		t.Fatalf("command executed during backoff: %q", out)
	}
}

func TestTerminalHonorsEgressStop(t *testing.T) {
	sc := scanctx.New("terminal-egress", "")
	scanctx.Activate(sc)
	t.Cleanup(func() { scanctx.Deactivate(sc.ID) })
	sc.SetEgressPolicy(scanctx.EgressPolicy{MaxConcurrency: 1})
	sc.Egress().Stop("operator stop")

	out, code := runShellInternalContext(context.Background(), sc.ID, "echo SHOULD_NOT_RUN")
	if code != 0 || !strings.Contains(out, "EGRESS POLICY") || strings.Contains(out, "SHOULD_NOT_RUN") {
		t.Fatalf("output=%q code=%d", out, code)
	}
}
