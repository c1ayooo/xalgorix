package proxy

// Per-run outbound proxy pinning (task-level proxy selector).
//
// The process-wide pool stays the default; a task may pin one of its entries
// (or explicit direct egress) for the lifetime of a run. The registry is keyed
// by the scan context id — the same identity the traffic proxy uses for
// attribution — so every consumer that already knows its run (MITM handler,
// httpclient) can resolve the same decision without threading a new parameter
// through the security chain.
//
// Security posture: pinning never weakens the six-ring guards. Direct is
// rejected at run start when the process runs in proxy-required mode; an entry
// is a pool member that already passed pool validation.

import (
	"errors"
	"sync"
)

// ScanUpstream is one run's pinned outbound decision.
type ScanUpstream struct {
	// Direct pins explicit direct egress (no upstream proxy).
	Direct bool
	// Entry is the pool member to use when Direct is false.
	Entry *Proxy
}

// ScanEntry is the model-facing view of one pool member (masked label, 1-based
// index used by the task selector).
type ScanEntry struct {
	Index int    `json:"index"`
	Label string `json:"label"`
}

var scanUpstreams sync.Map // scanCtxID -> ScanUpstream

// SetScanUpstream pins the outbound decision for one run.
func SetScanUpstream(scanCtxID string, up ScanUpstream) {
	if scanCtxID == "" {
		return
	}
	scanUpstreams.Store(scanCtxID, up)
}

// ClearScanUpstream forgets a run's pin (session teardown).
func ClearScanUpstream(scanCtxID string) {
	if scanCtxID == "" {
		return
	}
	scanUpstreams.Delete(scanCtxID)
}

// ScanUpstreamFor reports the pinned decision for a run, if any.
func ScanUpstreamFor(scanCtxID string) (ScanUpstream, bool) {
	v, ok := scanUpstreams.Load(scanCtxID)
	if !ok {
		return ScanUpstream{}, false
	}
	up, ok := v.(ScanUpstream)
	return up, ok
}

// UpstreamFor resolves the EGRESS proxy for one run: a per-task pin wins over
// the global pool. It is the one place every exit path (MITM forwarding, Go
// HTTP clients, subprocess environments) should consult, so "which proxy does
// this task leave through" has a single answer.
//
//	entry, direct, ok:
//	  ok=true,  direct=false → use entry
//	  ok=true,  direct=true  → explicit direct egress (a per-task pin)
//	  ok=false, direct=true  → no pool configured and no pin; fail closed when
//	                           Required(), otherwise dial direct
func UpstreamFor(scanCtxID string) (p *Proxy, direct bool, ok bool) {
	if up, has := ScanUpstreamFor(scanCtxID); has {
		if up.Direct {
			return nil, true, true
		}
		if up.Entry != nil {
			return up.Entry, false, true
		}
	}
	p = GetProxy()
	if p == nil {
		return nil, true, false
	}
	return p, false, true
}

// ErrRequiredNoUpstream is returned when proxy-required mode has no usable
// egress proxy for a run (no pool configured, or an invalid direct pin).
var ErrRequiredNoUpstream = errors.New("proxy-required mode has no upstream")

// EgressEnv builds the HTTP(S)_PROXY environment for a subprocess whose exit is
// the pool/pin. Used when the MITM is off (with interception on, the child is
// pointed at the loopback recorder instead, which forwards through this same
// egress). An empty slice means "no proxy env"; a non-nil error is the
// fail-closed required-mode case.
func EgressEnv(scanCtxID string, required bool) ([]string, error) {
	required = required || Required()
	p, direct, ok := UpstreamFor(scanCtxID)
	if direct && ok && required {
		return nil, ErrRequiredNoUpstream
	}
	if !direct && ok && p != nil {
		u, err := p.URL()
		if err != nil {
			return nil, err
		}
		raw := u.String()
		return []string{
			"HTTP_PROXY=" + raw,
			"HTTPS_PROXY=" + raw,
			"ALL_PROXY=" + raw,
			"http_proxy=" + raw,
			"https_proxy=" + raw,
			"all_proxy=" + raw,
			"NO_PROXY=127.0.0.1,localhost,::1",
		}, nil
	}
	if required {
		return nil, ErrRequiredNoUpstream
	}
	return nil, nil
}

// Entries returns the active pool as masked entries for the task selector.
// Labels never contain credentials (Proxy.String masks the password).
func Entries() []ScanEntry {
	m := defaultManager.Load()
	if m == nil || m.pool == nil {
		return nil
	}
	all := m.pool.All()
	out := make([]ScanEntry, 0, len(all))
	for i, p := range all {
		if p == nil {
			continue
		}
		out = append(out, ScanEntry{Index: i + 1, Label: p.String()})
	}
	return out
}

// EntryAt resolves a 1-based index into the current pool.
func EntryAt(index int) *Proxy {
	m := defaultManager.Load()
	if m == nil || m.pool == nil || index <= 0 {
		return nil
	}
	all := m.pool.All()
	if index > len(all) {
		return nil
	}
	return all[index-1]
}
