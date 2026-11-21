package infoitem

import "sync"

var (
	readerMu        sync.RWMutex
	readerSeq       uint64
	defaultReader   Reader
	defaultReaderID uint64
)

// RegisterReader installs the process-wide cold-path reader. The returned
// function removes it only if it is still the active registration.
func RegisterReader(reader Reader) func() {
	if reader == nil {
		return func() {}
	}
	readerMu.Lock()
	readerSeq++
	id := readerSeq
	defaultReader = reader
	defaultReaderID = id
	readerMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			readerMu.Lock()
			if defaultReaderID == id {
				defaultReader = nil
				defaultReaderID = 0
			}
			readerMu.Unlock()
		})
	}
}

// DefaultReader returns the optional cross-scan reader. Nil means the cold path
// is disabled; callers must continue with local ledger access.
func DefaultReader() Reader {
	readerMu.RLock()
	defer readerMu.RUnlock()
	return defaultReader
}
