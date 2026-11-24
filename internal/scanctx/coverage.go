package scanctx

import (
	"sort"
	"strings"
	"sync"
)

// CoverageStore is the scan-shared endpoint × vulnerability-class matrix.
// Each Agent keeps its own behavioral ScanState, but coordinators and delegated
// specialists must see the same executed coverage or their plans diverge. The
// agent package normalizes endpoint aliases and class names before writing.
type CoverageStore struct {
	mu    sync.RWMutex
	pairs map[string]map[string]struct{}
	// verified is the VERIFIER-ATTRIBUTED tier: (endpoint, class) pairs
	// where a deterministic verifier (verify_sqli, authz_matrix, ...) actually
	// executed, regardless of which agent ran it. Unlike raw request probes
	// (pairs, above), verifier execution is trustworthy scan-level evidence a
	// coordinator may consume without re-running the probe itself — one
	// shallow child request still closes nothing.
	verified map[string]map[string]struct{}

	// R6.1 durable-home identity (see coverage_checkpoint.go): the
	// run_state_files row stamp (scan_id, instance_id) and a monotonic write
	// counter for watermark comparisons.
	scanID     string
	instanceID string
	writeSeq   int64
}

// NewCoverageStore creates an empty shared coverage matrix.
func NewCoverageStore() *CoverageStore {
	return &CoverageStore{
		pairs:    make(map[string]map[string]struct{}),
		verified: make(map[string]map[string]struct{}),
	}
}

// Mark records one executed endpoint/class pair. Empty values are ignored.
func (s *CoverageStore) Mark(endpoint, class string) {
	if s == nil {
		return
	}
	endpoint = strings.TrimSpace(endpoint)
	class = strings.TrimSpace(class)
	if endpoint == "" || class == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pairs == nil {
		s.pairs = make(map[string]map[string]struct{})
	}
	if s.pairs[endpoint] == nil {
		s.pairs[endpoint] = make(map[string]struct{})
	}
	s.pairs[endpoint][class] = struct{}{}
}

// MarkVerified records a verifier-attributed (endpoint, class) pair: a
// deterministic verifier executed against the endpoint for the class.
func (s *CoverageStore) MarkVerified(endpoint, class string) {
	if s == nil {
		return
	}
	endpoint = strings.TrimSpace(endpoint)
	class = strings.TrimSpace(class)
	if endpoint == "" || class == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.verified == nil {
		s.verified = make(map[string]map[string]struct{})
	}
	if s.verified[endpoint] == nil {
		s.verified[endpoint] = make(map[string]struct{})
	}
	s.verified[endpoint][class] = struct{}{}
}

// HasVerified reports whether a deterministic verifier executed the exact
// (endpoint, class) pair — scan-level evidence any agent may rely on.
func (s *CoverageStore) HasVerified(endpoint, class string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.verified[endpoint][class]
	return ok
}

// Has reports whether an exact endpoint/class pair has executed coverage.
func (s *CoverageStore) Has(endpoint, class string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.pairs[endpoint][class]
	return ok
}

// HasEndpoint reports whether any class has executed coverage for endpoint.
// It lets compatibility fallbacks distinguish a legacy endpoint from a modern
// matrix entry whose requested class is genuinely still missing.
func (s *CoverageStore) HasEndpoint(endpoint string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.pairs[endpoint]) > 0
}

// Snapshot returns read-only copies of the probed and verifier-attributed
// matrices (endpoint -> sorted classes). Callers must not mutate the result.
// Used by the deterministic coverage-gap pass; it never writes.
func (s *CoverageStore) Snapshot() (probed, verified map[string][]string) {
	if s == nil {
		return map[string][]string{}, map[string][]string{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyMatrix(s.pairs), copyMatrix(s.verified)
}

func copyMatrix(src map[string]map[string]struct{}) map[string][]string {
	out := make(map[string][]string, len(src))
	for endpoint, classes := range src {
		if len(classes) == 0 {
			continue
		}
		list := make([]string, 0, len(classes))
		for class := range classes {
			list = append(list, class)
		}
		sort.Strings(list)
		out[endpoint] = list
	}
	return out
}
