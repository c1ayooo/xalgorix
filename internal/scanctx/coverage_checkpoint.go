package scanctx

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/c1ayooo/xalgorix/v4/internal/storage"
)

type coveragePair struct {
	Endpoint string `json:"endpoint"`
	Class    string `json:"class"`
}

type coverageCheckpoint struct {
	Version  int            `json:"version"`
	Executed []coveragePair `json:"executed"`
	Verified []coveragePair `json:"verified"`
}

// SetScanID stamps the scan identity on the run_state_files row (R6.1).
// Mirrors LedgerStore.SetScanID: callers never infer a scan id from a
// filesystem path. New() stamps it from the ScanContext id.
func (s *CoverageStore) SetScanID(scanID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scanID = scanID
}

// SetInstanceID stamps the run identity (D11) on the store. With the
// StateFilesBackend registered, checkpoints carry it and a re-dispatched
// session's first LoadCheckpoint inherits the previous sibling session's
// coverage via the instance continuity read instead of starting empty.
func (s *CoverageStore) SetInstanceID(instanceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instanceID = instanceID
}

func coveragePairs(matrix map[string]map[string]struct{}) []coveragePair {
	var pairs []coveragePair
	for endpoint, classes := range matrix {
		for class := range classes {
			pairs = append(pairs, coveragePair{endpoint, class})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Endpoint != pairs[j].Endpoint {
			return pairs[i].Endpoint < pairs[j].Endpoint
		}
		return pairs[i].Class < pairs[j].Class
	})
	return pairs
}

// SaveCheckpoint serializes shared evidence under its lock. Concurrent lane
// writers cannot replace a newer matrix with an older snapshot.
//
// Durable home (charter v3.0 §6.3, R6.1): with a StateFilesBackend
// registered the checkpoint is a whole-snapshot upsert into the
// run_state_files row (scan_id, kind=coverage) under the state lock —
// coverage.json is never written. A failure is returned to the caller
// (persist-failure is not success, §5.2); the in-memory matrix stays
// authoritative and the next successful checkpoint re-persists everything.
// Without a backend the file behavior is unchanged.
func (s *CoverageStore) SaveCheckpoint(dir string) error {
	if s == nil {
		return nil
	}
	b := CurrentStateFilesBackend()
	if b == nil && dir == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(coverageCheckpoint{1, coveragePairs(s.pairs), coveragePairs(s.verified)})
	if err != nil {
		return err
	}
	if b != nil {
		s.writeSeq++
		if err := b.SaveStateFileSnapshot(s.scanID, StateFileKindCoverage, s.instanceID, s.writeSeq, data); err != nil {
			log.Printf("[coverage] Warning: state checkpoint save failed for %s (in-memory state stays authoritative): %v", s.scanID, err)
			return err
		}
		return nil
	}
	return storage.WriteAtomic(filepath.Join(dir, "coverage.json"), data)
}

// LoadCheckpoint restores only executed evidence, retaining the distinction
// between raw probes and deterministic verifier attribution.
//
// Snapshot source order with the backend registered (charter v3.0 §6.3):
//
//  1. this scan's own checkpoint;
//  2. the newest sibling session's checkpoint for the stamped instance — the
//     D11 continuity read (a re-dispatched session inherits the previous
//     session's coverage instead of starting empty);
//  3. the legacy coverage.json file — straggler resilience only; never a
//     write target while a backend is registered.
//
// A backend transport failure warns and falls through to the next source
// (the in-memory matrix stays authoritative); a checkpoint that is present
// but undecodable is an error — corruption must never read as "no coverage".
func (s *CoverageStore) LoadCheckpoint(dir string) error {
	if s == nil {
		return nil
	}
	if b := CurrentStateFilesBackend(); b != nil {
		if data, _, ok, err := b.LoadStateFileSnapshot(s.scanID, StateFileKindCoverage); err != nil {
			log.Printf("[coverage] Warning: state checkpoint load failed for %s: %v", s.scanID, err)
		} else if ok {
			return s.applyCheckpoint(data, fmt.Sprintf("state checkpoint %s/%s", s.scanID, StateFileKindCoverage))
		}
		if s.instanceID != "" {
			if data, _, source, ok, err := b.LoadLatestStateFileSnapshotForInstance(s.instanceID, s.scanID, StateFileKindCoverage); err != nil {
				log.Printf("[coverage] Warning: continuity checkpoint load failed for %s/%s: %v", s.instanceID, s.scanID, err)
			} else if ok {
				return s.applyCheckpoint(data, fmt.Sprintf("state continuity %s (from sibling %s)", s.instanceID, source))
			}
		}
	}
	if dir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "coverage.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.applyCheckpoint(data, filepath.Join(dir, "coverage.json"))
}

// applyCheckpoint decodes one checkpoint and applies its evidence to the
// matrix. Source names the origin for the version-mismatch error.
func (s *CoverageStore) applyCheckpoint(data []byte, source string) error {
	var saved coverageCheckpoint
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("coverage checkpoint %s does not parse: %w", source, err)
	}
	if saved.Version != 1 {
		return fmt.Errorf("unsupported coverage checkpoint version %d (from %s)", saved.Version, source)
	}
	for _, pair := range saved.Executed {
		s.Mark(pair.Endpoint, pair.Class)
	}
	for _, pair := range saved.Verified {
		s.MarkVerified(pair.Endpoint, pair.Class)
	}
	return nil
}
