// Package egress centralizes the outbound-traffic policy for tools that can
// bypass Go's DialGuard (browser, terminal, Python). The default mode relies on
// the loopback traffic proxy; enforce mode requires a platform backend that can
// prove OS-level isolation and fails closed otherwise.
package egress

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/xalgord/xalgorix/v4/internal/trafficproxy"
)

// ErrNotSupported is returned when enforce mode is requested but the platform
// has no verified OS-level isolation backend.
var ErrNotSupported = errors.New("egress: OS-level isolation is not available on this platform")

// Status describes the effective boundary for one scan.
type Status struct {
	Mode     string `json:"mode"`     // proxy | enforce
	Enforced bool   `json:"enforced"` // true only when the boundary is proven
	Reason   string `json:"reason,omitempty"`
}

var (
	mu     sync.RWMutex
	byScan = map[string]Status{}
)

// Mode returns the configured policy: proxy (default) or enforce.
func Mode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("XALGORIX_EGRESS_MODE"))) {
	case "enforce":
		return "enforce"
	default:
		return "proxy"
	}
}

// PrepareScan records the boundary used for a scan.
func PrepareScan(ctx context.Context, scanID string) (Status, error) {
	mode := Mode()
	status := Status{Mode: mode}
	if mode == "enforce" {
		var err error
		status, err = preparePlatform(ctx, scanID)
		if err != nil {
			return Status{}, err
		}
	} else if trafficproxy.Enabled() {
		// Proxy environment variables route cooperative clients, but they are
		// not an OS boundary: a child may ignore them or dial directly.
		status.Reason = "loopback traffic proxy configured; OS-level isolation is unmanaged"
	} else {
		status.Reason = "traffic proxy disabled; OS-level isolation is unmanaged"
	}
	mu.Lock()
	byScan[scanID] = status
	mu.Unlock()
	return status, nil
}

// StatusFor returns the last prepared status for a scan.
func StatusFor(scanID string) Status {
	mu.RLock()
	defer mu.RUnlock()
	return byScan[scanID]
}

// EnvForChild returns environment markers for child processes. Proxy variables
// themselves remain owned by trafficproxy.Environment so there is one source of
// truth for proxy URL/CA settings.
func EnvForChild(scanID string) []string {
	st := StatusFor(scanID)
	// Keep trafficproxy=off byte-identical for child environments: no boundary
	// was prepared and there is nothing truthful to advertise.
	if st.Mode == "" || (st.Mode == "proxy" && !trafficproxy.Enabled()) {
		return nil
	}
	return []string{"XALGORIX_EGRESS_MODE=" + st.Mode, "XALGORIX_EGRESS_ENFORCED=" + boolString(st.Enforced)}
}

// Forget releases the per-scan status and the platform rules when this was the
// last active enforced scan.
func Forget(scanID string) {
	mu.Lock()
	st, existed := byScan[scanID]
	delete(byScan, scanID)
	mu.Unlock()
	if existed && st.Mode == "enforce" {
		forgetPlatform(scanID)
	}
}

func boolString(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func enforceUnavailable(scanID string, err error) error {
	return fmt.Errorf("%w (scan %s): %v", ErrNotSupported, scanID, err)
}
