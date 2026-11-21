package browser

import (
	"context"
	"testing"

	"github.com/go-rod/rod"

	"github.com/xalgord/xalgorix/v4/internal/tools"
)

func TestContextPageBindsContextWithoutRecursing(t *testing.T) {
	const ctxID = "browser-context-page-test"
	s := getBrowserStoreByID(ctxID)
	t.Cleanup(func() { CleanupContext(ctxID) })
	callCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	page := new(rod.Page)
	s.callCtx = callCtx
	s.page = page

	oldBind := bindPageContext
	binds := 0
	bindPageContext = func(gotPage *rod.Page, gotCtx context.Context) *rod.Page {
		binds++
		if gotPage != page || gotCtx != callCtx {
			t.Fatalf("bind args = (%p, %v), want original page and call ctx", gotPage, gotCtx)
		}
		return gotPage
	}
	t.Cleanup(func() { bindPageContext = oldBind })

	if got := s.contextPage(); got != page {
		t.Fatalf("contextPage = %p, want original page %p", got, page)
	}
	if binds != 1 {
		t.Fatalf("bind called %d times, want exactly 1 (recursion guard)", binds)
	}
}

func TestBrowserActionContextBindingIsScopedToCall(t *testing.T) {
	const ctxID = "browser-context-scope-test"
	s := getBrowserStoreByID(ctxID)
	t.Cleanup(func() { CleanupContext(ctxID) })

	callCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := withBrowserTransportRecoveryCtx(ctxID, callCtx, func() (tools.Result, error) {
		if s.callCtx != callCtx {
			t.Fatalf("callCtx not bound inside action")
		}
		return tools.Result{Output: "ok"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.callCtx != nil {
		t.Fatalf("callCtx leaked after action: %v", s.callCtx)
	}
	// The browser/page itself is untouched, so a subsequent action can run.
	_, err = withBrowserTransportRecoveryCtx(ctxID, nil, func() (tools.Result, error) {
		if s.callCtx != nil {
			t.Fatalf("nil call context must not bind")
		}
		return tools.Result{Output: "next"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
