// hook.go — wires the projection into graph conclusion.
//
// The hook reads the scan's intel items from the in-memory authority (same
// process, same scan context the tools wrote through) and derives on the
// conclusion transaction. That is the only derive trigger; a rebuild tool can
// replay the same derivation offline because it is a pure function of the
// snapshot.

package intelaccess

import (
	"context"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/graph"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
)

// intelContextKey carries the intel store's own context id through a
// conclusion. Access rows are keyed by the graph run id (the dispatch identity),
// but the in-memory intel store is keyed by the scan-session context id, which
// can differ in production (legacy scan id vs run id). Without this, the hook
// would derive nothing outside tests where the two ids happen to match.
type intelContextKey struct{}

// WithIntelContextID attaches the intel store context id to a conclusion
// context. Callers without one (tests, CLI) keep the graph id fallback.
func WithIntelContextID(ctx context.Context, id string) context.Context {
	if id = strings.TrimSpace(id); id == "" {
		return ctx
	}
	return context.WithValue(ctx, intelContextKey{}, id)
}

func intelContextID(ctx context.Context, fallback string) string {
	if ctx != nil {
		if id, ok := ctx.Value(intelContextKey{}).(string); ok && strings.TrimSpace(id) != "" {
			return id
		}
	}
	return fallback
}

// ConcludeHook returns the PG-backed conclusion hook.
func (s *PGStore) ConcludeHook() graph.ConcludeHook {
	if s == nil {
		return nil
	}
	return func(ctx context.Context, tx graph.Tx, scanID string, intentID int64) error {
		items := intelligence.ItemsForContext(intelContextID(ctx, scanID))
		if len(items) == 0 {
			return nil
		}
		_, err := s.DeriveTx(ctx, tx, scanID, intentID, items)
		return err
	}
}

// ConcludeHook returns the memory-backed conclusion hook (no SQL).
func (m *MemoryStore) ConcludeHook() graph.ConcludeHook {
	if m == nil {
		return nil
	}
	return func(ctx context.Context, _ graph.Tx, scanID string, intentID int64) error {
		items := intelligence.ItemsForContext(intelContextID(ctx, scanID))
		if len(items) == 0 {
			return nil
		}
		_, err := m.DeriveScanForIntent(ctx, scanID, intentID, items)
		return err
	}
}
