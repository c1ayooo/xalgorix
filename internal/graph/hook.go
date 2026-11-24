// hook.go — the conclusion seam for derived projections.
//
// Intent conclusion is the only place the engine settles a unit of work, which
// makes it the right single trigger for projections that summarize what the
// work produced (M2 of INTEL_HANDOFF_SPEC: the intel reachability projection).
// The hook runs on the conclusion transaction inside a savepoint: when it
// fails the savepoint is rolled back and the conclusion still commits. That is
// deliberate — a projection is idempotent and replayable (its derivation is a
// pure function of the scan's stores), so a stale projection must never be able
// to block settling an intent, and a future rebuild catches up.
//
// The hook is process infrastructure (registered once at startup, like the
// other backends), not per-scan state.

package graph

import (
	"context"
	"errors"
	"log"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Tx is the transactional surface exposed to a conclusion hook. *pgx.Tx
// satisfies it; the memory store passes a no-op (memory hooks derive from
// in-process stores and never issue SQL).
type Tx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ConcludeHook runs once per concluded intent, after the state transition is
// applied and before the transaction commits.
type ConcludeHook func(ctx context.Context, tx Tx, scanID string, intentID int64) error

var (
	concludeHookMu sync.RWMutex
	concludeHook   ConcludeHook
)

// SetConcludeHook registers the process-wide conclusion hook (nil clears it).
func SetConcludeHook(h ConcludeHook) {
	concludeHookMu.Lock()
	defer concludeHookMu.Unlock()
	concludeHook = h
}

func currentConcludeHook() ConcludeHook {
	concludeHookMu.RLock()
	defer concludeHookMu.RUnlock()
	return concludeHook
}

// runConcludeHookPG applies the hook under a savepoint on the conclusion
// transaction. Hook errors roll the savepoint back and are logged; the
// conclusion itself is unaffected.
func runConcludeHookPG(ctx context.Context, tx pgx.Tx, scanID string, intentID int64) {
	hook := currentConcludeHook()
	if hook == nil {
		return
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		log.Printf("[graph] conclude hook (intent %d): savepoint failed: %v — projection is stale until the next conclusion", intentID, err)
		return
	}
	if hookErr := hook(ctx, sp, scanID, intentID); hookErr != nil {
		_ = sp.Rollback(ctx)
		log.Printf("[graph] conclude hook (intent %d) failed: %v — derivation is idempotent; the next conclusion or rebuild catches up", intentID, hookErr)
		return
	}
	if err := sp.Commit(ctx); err != nil {
		log.Printf("[graph] conclude hook (intent %d): savepoint commit failed: %v", intentID, err)
	}
}

// runConcludeHookMemory applies the hook for the memory backend. Callers hold
// the store lock; hooks here derive from in-process stores and never query the
// graph, so no lock inversion is possible.
func runConcludeHookMemory(ctx context.Context, scanID string, intentID int64) {
	hook := currentConcludeHook()
	if hook == nil {
		return
	}
	if err := hook(ctx, noopTx{}, scanID, intentID); err != nil {
		log.Printf("[graph] conclude hook (intent %d) failed: %v — derivation is idempotent; the next conclusion or rebuild catches up", intentID, err)
	}
}

// noopTx is the memory backend's transactional surface: SQL issued against it
// fails loudly rather than silently succeeding.
type noopTx struct{}

var errNoSQLInMemory = errors.New("graph: conclusion hook issued SQL against the memory store")

func (noopTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errNoSQLInMemory
}

func (noopTx) QueryRow(context.Context, string, ...any) pgx.Row { return errRow{} }

func (noopTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errNoSQLInMemory
}

type errRow struct{}

func (errRow) Scan(...any) error { return errNoSQLInMemory }
