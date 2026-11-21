package egress

import (
	"context"
	"errors"
	"testing"
)

func TestEnforceModeFailsClosedWithoutBackend(t *testing.T) {
	t.Setenv("XALGORIX_EGRESS_MODE", "enforce")
	if _, err := PrepareScan(context.Background(), "scan-1"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("PrepareScan enforce error = %v, want ErrNotSupported", err)
	}
}

func TestProxyModeRecordsStatus(t *testing.T) {
	t.Setenv("XALGORIX_EGRESS_MODE", "proxy")
	st, err := PrepareScan(context.Background(), "scan-2")
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode != "proxy" {
		t.Fatalf("mode = %q", st.Mode)
	}
	if st.Enforced {
		t.Fatal("proxy mode must not claim a proven OS-level boundary")
	}
	if got := StatusFor("scan-2"); got.Mode != st.Mode || got.Enforced {
		t.Fatalf("status not recorded: %+v", got)
	}
	Forget("scan-2")
	if got := StatusFor("scan-2"); got.Mode != "" {
		t.Fatalf("status survived Forget: %+v", got)
	}
}
