package browser

import (
	"fmt"
	"log"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"

	"github.com/c1ayooo/xalgorix/v4/internal/resources"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// launchBrowserInProject attaches to Chromium running inside the scan's project
// container instead of launching it in the platform process.
//
// The container keeps Chromium alive across browser_action calls (that is what
// the per-project container buys us), so this function only has to ensure the
// endpoint exists and wire rod to it. The embedded extension and the host-side
// launcher are deliberately not used: the image serves a plain headless
// Chromium and the extension bridge has no host process to talk to.
//
// Callers hold s.mu (ensureBrowser) — the same locking discipline as the native
// launch path, which is what keeps the browserStore fields coherent.
func (s *browserStore) launchBrowserInProject(ctxID string, requiredProxy bool) error {
	sc := scanctx.Get(ctxID)
	if sc == nil {
		return fmt.Errorf("browser requires an active scan context for container execution")
	}
	browserCtx := browserWaitContext(ctxID)

	bs := sc.Browser
	leaseAcquired := false
	var lease *resources.ToolLease
	if bs == nil || bs.Lease() == nil {
		l, err := resources.AcquireToolLeaseContext(browserCtx, false, "browser_action")
		if err != nil {
			return fmt.Errorf("browser launch canceled while waiting for resource capacity: %w", err)
		}
		lease = l
		leaseAcquired = true
	}
	leaseAttached := false
	defer func() {
		if leaseAcquired && !leaseAttached {
			lease.Release()
		}
	}()

	endpoint, err := tools.ProjectCDPURL(browserCtx, sc)
	if err != nil {
		return fmt.Errorf("launch container browser: %w", err)
	}

	client, err := cdp.StartWithURL(browserCtx, endpoint, nil)
	if err != nil {
		return fmt.Errorf("connect container browser: %w", err)
	}
	transport := &browserTransport{CDPClient: client}
	br := rod.New().Context(browserCtx).Client(transport)
	if err := br.Connect(); err != nil {
		return fmt.Errorf("connect container browser: %w", err)
	}

	s.transport = transport
	s.browser = br
	// No host launcher exists for a containerised Chromium: cleanup must not try
	// to kill a process it does not own. The container's removal stops it.
	s.browserLauncher = nil
	s.requiredProxy = requiredProxy
	if leaseAcquired {
		s.lease = lease
		if bs != nil {
			bs.SetLease(lease)
		}
		leaseAttached = true
	}
	log.Printf("[browser] Attached to container Chromium (required_proxy=%v)", requiredProxy)
	return nil
}
