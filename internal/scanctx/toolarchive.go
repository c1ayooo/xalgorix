package scanctx

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// toolArchiveDir is the per-scan directory holding archived raw tool outputs.
const toolArchiveDir = "tool-outputs"

// archiveIDPattern validates retrieval ids (to_<seq>) and extracts the
// sequence number as submatch 1 (used by NewToolArchive to resume the counter
// past existing records). The capture group is load-bearing: indexing a
// groupless pattern panics on the first nonempty reopen.
var archiveIDPattern = regexp.MustCompile(`^to_([0-9]{1,12})$`)

// bareArchiveIDPattern matches an id written without the to_ prefix, e.g.
// "0012" or "12". Models routinely transcribe a stub id that way; rejecting it
// costs a turn and counts against the tool's circuit breaker.
var bareArchiveIDPattern = regexp.MustCompile(`^[0-9]{1,12}$`)

// archiveIDCandidates lists the on-disk spellings to try for a retrieval id.
// The canonical name is to_<seq> zero-padded to six digits (Archive's format),
// but a model-written "0012", "to_12" or "12" resolves to the same record. A
// non-canonical to_ spelling is tried verbatim first, so a record an older
// layout wrote under an unpadded name stays retrievable.
//
// Every candidate is built from validated decimal digits, so no path
// separator or traversal sequence can enter filepath.Join.
func archiveIDCandidates(id string) []string {
	id = strings.TrimSpace(id)
	var digits string
	switch {
	case archiveIDPattern.MatchString(id):
		digits = id[len("to_"):]
	case bareArchiveIDPattern.MatchString(id):
		digits = id
	default:
		return nil
	}
	seq, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || seq == 0 {
		return nil // seq 0 is never issued: the counter starts at 1
	}
	canonical := fmt.Sprintf("to_%06d", seq)
	if archiveIDPattern.MatchString(id) && id != canonical {
		return []string{id, canonical}
	}
	return []string{canonical}
}

// ToolArchive persists the complete, untruncated raw output of tool results so
// the conversation can replace aged tool-result messages with tiny retrieval
// stubs without destroying any information: every archived byte stays
// retrievable by the agent via the read_tool_output tool (byte-identical).
//
// This is the backing store for bounded working context: the agent keeps a
// verbatim recent window and archives everything older. Files live under
// <ScanDir>/tool-outputs/ so they survive restarts.
type ToolArchive struct {
	mu   sync.Mutex
	dir  string
	next int
}

// NewToolArchive creates an archive rooted at <scanDir>/tool-outputs.
// A nil-safe empty archive is returned when scanDir is empty.
//
// Existing records are immutable across reopen: the sequence counter starts
// past the highest archived id. A reopened archive previously restarted the
// counter at zero and Archive's temp+rename silently replaced an existing
// record with different content under the same retrieval id — deterministic
// evidence loss whenever the archive outlives one process (resume/restart).
func NewToolArchive(scanDir string) *ToolArchive {
	if scanDir == "" {
		return &ToolArchive{}
	}
	a := &ToolArchive{dir: filepath.Join(scanDir, toolArchiveDir)}
	if entries, err := os.ReadDir(a.dir); err == nil {
		for _, e := range entries {
			if m := archiveIDPattern.FindStringSubmatch(e.Name()); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil && n > a.next {
					a.next = n
				}
			}
		}
	}
	return a
}

// enabled reports whether the archive has a backing directory.
func (a *ToolArchive) enabled() bool {
	return a != nil && a.dir != ""
}

// Archive stores content and returns its retrieval id (to_<seq>).
// Content below minBytes is not stored and returns "" (the caller keeps the
// message verbatim instead — small outputs are cheap to carry in-context).
func (a *ToolArchive) Archive(toolName, content string, minBytes int) string {
	if !a.enabled() || len(content) < minBytes {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.next++
	id := fmt.Sprintf("to_%06d", a.next)
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return ""
	}
	// Belt-and-braces immutability: never overwrite an existing record, even if
	// the reopen scan raced a concurrent writer. Advance until the id is free.
	for target := filepath.Join(a.dir, id); ; {
		if _, err := os.Stat(target); os.IsNotExist(err) {
			break
		}
		a.next++
		id = fmt.Sprintf("to_%06d", a.next)
		target = filepath.Join(a.dir, id)
	}
	// The file body keeps the tool name as a header so a bare retrieval is
	// self-describing; the id alone never reveals target data.
	body := "tool: " + toolName + "\n\n" + content
	target := filepath.Join(a.dir, id)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return ""
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return ""
	}
	return id
}

// Get returns the archived content (tool header stripped) for an id.
// Prefix-less and unpadded spellings resolve to the canonical record; see
// archiveIDCandidates.
func (a *ToolArchive) Get(id string) (string, bool) {
	if !a.enabled() {
		return "", false
	}
	for _, cand := range archiveIDCandidates(id) {
		data, err := os.ReadFile(filepath.Join(a.dir, cand))
		if err != nil {
			continue
		}
		return stripArchiveHeader(string(data)), true
	}
	return "", false
}

// stripArchiveHeader removes the self-describing tool header written by
// Archive so a bare retrieval returns exactly the tool's output.
func stripArchiveHeader(s string) string {
	if idx := strings.Index(s, "\n\n"); idx >= 0 && strings.HasPrefix(s, "tool: ") {
		return s[idx+2:]
	}
	return s
}

// Count reports how many outputs have been archived.
func (a *ToolArchive) Count() int {
	if !a.enabled() {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.next
}
