package agent

import (
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func TestRecordExitDeclarationUsesDelegatedIdentity(t *testing.T) {
	ctx := scanctx.New("exit-key-test", t.TempDir())
	scanctx.Activate(ctx)
	t.Cleanup(func() { scanctx.Deactivate(ctx.ID); ctx.Close() })

	a := &Agent{ID: "agent_local", delegatedAgentID: "sub_42", scanCtx: ctx}
	a.recordExitDeclaration("none", "control-plane lane yielded no intel")

	got, ok := ctx.Progress.ExitFor("sub_42")
	if !ok || got.Exit != "none" || got.Reason == "" {
		t.Fatalf("delegated identity must hold the declaration: %+v ok=%v", got, ok)
	}
	if _, ok := ctx.Progress.ExitFor("agent_local"); ok {
		t.Fatal("declaration must not be stored under the local agent id")
	}
}
