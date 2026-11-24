package orchestrator

// notes_view.go — one live view of the run's notes for the orchestrator.
//
// Two in-memory note stores exist in a production run:
//
//   - internal/tools/notes (what add_note/read_notes write): the durable PG
//     snapshot and the authoritative content a worker publishes;
//   - scanctx.NoteStore: engine-side memory, written only by agent hooks
//     (for example the platform_finished marker).
//
// Neither is a superset. Reading only the scanctx store — as the planner and
// the container materializer did — shows a stale snapshot in which a lane's
// freshly published container addresses never appear. The engine then has
// capacity for N workers but only the LLM-created intents to run, which is
// exactly the "several workers, only one streams" symptom. The helpers below
// union both stores so planning and lane materialization see what the workers
// actually published.

import (
	"fmt"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/notes"
)

// liveNotes returns the union of the durable tool-written notes and the
// engine-side hook markers. Tool-written keys win on conflict: that store is
// the durable, worker-visible snapshot.
func liveNotes(sc *scanctx.ScanContext) map[string]string {
	merged := map[string]string{}
	if sc == nil {
		return merged
	}
	if sc.ID != "" {
		for k, v := range notes.GetAllNotesForContext(sc.ID) {
			merged[k] = v
		}
	}
	if sc.Notes != nil {
		for k, v := range sc.Notes.GetAll() {
			if _, exists := merged[k]; !exists {
				merged[k] = v
			}
		}
	}
	return merged
}

// liveNote looks up one key in the live union.
func liveNote(sc *scanctx.ScanContext, key string) (string, bool) {
	v, ok := liveNotes(sc)[key]
	return v, ok
}

// noteSaysClosed reports whether text explicitly marks a container/challenge
// as solved or closed. Callers pass lower-cased text.
func noteSaysClosed(lower string) bool {
	for _, marker := range []string{"solved", "closed", "已解", "已关闭", "flag{"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// liveNotesBlob renders the live union for prompt context. The durable store
// keeps its own compact formatting; hook-only markers are appended so the
// planner can still see them.
func liveNotesBlob(sc *scanctx.ScanContext) string {
	if sc == nil {
		return ""
	}
	var b strings.Builder
	var durable map[string]string
	if sc.ID != "" {
		durable = notes.GetAllNotesForContext(sc.ID)
		if blob := notes.FormatForContextID(sc.ID); strings.TrimSpace(blob) != "" {
			b.WriteString(blob)
		}
	}
	if sc.Notes != nil {
		for k, v := range sc.Notes.GetAll() {
			if _, exists := durable[k]; exists {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			if len(v) > 500 {
				v = v[:500] + "... (truncated)"
			}
			fmt.Fprintf(&b, "• %s: %s\n", k, v)
		}
	}
	return b.String()
}
