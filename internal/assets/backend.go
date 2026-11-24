package assets

import "sync"

var (
	backendMu sync.RWMutex
	backend   Repository
)

// SetBackend installs the process-wide asset authority.
func SetBackend(repo Repository) {
	backendMu.Lock()
	defer backendMu.Unlock()
	backend = repo
}

// Backend returns the installed authority (nil in tests/offline).
func Backend() Repository {
	backendMu.RLock()
	defer backendMu.RUnlock()
	return backend
}
