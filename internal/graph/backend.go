package graph

import (
	"sync"
)

var (
	backendMu sync.RWMutex
	backend   Repository
)

// SetBackend installs the process-wide graph authority. Production passes
// the PGStore; tests may pass a MemoryStore. Passing nil clears it.
func SetBackend(repo Repository) {
	backendMu.Lock()
	defer backendMu.Unlock()
	backend = repo
}

// Backend returns the installed authority, or nil when none is configured
// (legacy/offline mode and most unit tests).
func Backend() Repository {
	backendMu.RLock()
	defer backendMu.RUnlock()
	return backend
}

// HasBackend reports whether an authority is installed.
func HasBackend() bool { return Backend() != nil }
