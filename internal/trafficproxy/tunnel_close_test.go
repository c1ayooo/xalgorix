package trafficproxy

import (
	"net"
	"testing"
)

func TestCloseScanTunnelsClosesOnlyOwnRun(t *testing.T) {
	m := &Manager{tunnels: make(map[*tunnel]struct{})}
	ownA, ownB := net.Pipe()
	otherA, otherB := net.Pipe()
	t.Cleanup(func() {
		_ = ownA.Close()
		_ = ownB.Close()
		_ = otherA.Close()
		_ = otherB.Close()
	})
	own := &tunnel{conn: ownA, done: make(chan struct{}), scanID: "scan-own"}
	other := &tunnel{conn: otherA, done: make(chan struct{}), scanID: "scan-other"}
	m.registerTunnel(own)
	m.registerTunnel(other)

	if got := m.CloseScanTunnels("scan-own"); got != 1 {
		t.Fatalf("closed %d tunnels, want 1", got)
	}
	// The closed side rejects further writes immediately (net.Pipe semantics).
	if _, err := ownA.Write([]byte("x")); err == nil {
		t.Fatal("own tunnel is still writable after force-close")
	}
	// The other run's tunnel remains registered (its goroutine would unregister
	// it on connection close, which is not simulated here).
	m.tunnelsMu.Lock()
	_, otherTracked := m.tunnels[other]
	m.tunnelsMu.Unlock()
	if !otherTracked {
		t.Fatal("another run's tunnel was removed by the force-close")
	}
	// Closing without a matching run is a no-op.
	if got := m.CloseScanTunnels("scan-missing"); got != 0 {
		t.Fatalf("unexpected close count for a missing run: %d", got)
	}
}
