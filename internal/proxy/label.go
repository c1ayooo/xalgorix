package proxy

// UpstreamLabelFor returns a non-consuming label for the egress a run will use:
// an explicit pin's masked proxy label, "direct" for an explicit direct pin, or
// "global-pool" when the run follows the rotation pool. It never advances the
// rotation, so audit/metrics callers cannot change routing by observing it.
func UpstreamLabelFor(scanCtxID string) string {
	if up, ok := ScanUpstreamFor(scanCtxID); ok {
		if up.Direct {
			return "direct"
		}
		if up.Entry != nil {
			return up.Entry.String()
		}
	}
	return "global-pool"
}
