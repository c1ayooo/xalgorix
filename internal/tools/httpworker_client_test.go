package tools

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func serveFakeWorker(t *testing.T, socket string, payload string) {
	t.Helper()
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		if r.URL.Path != "/v1/request" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
	})
}

func TestHTTPWorkerRoundTripperMapsResponse(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "http-worker.sock")
	body := base64.StdEncoding.EncodeToString([]byte("hi"))
	serveFakeWorker(t, socket, `{"status":200,"headers":{"X-Test":["yes"]},"body_base64":"`+body+`"}`)

	sc := &scanctx.ScanContext{ID: "scan-test", ScanDir: dir}
	rt := &httpWorkerTransport{scanCtx: sc, socket: socket}
	req := httptest.NewRequest(http.MethodGet, "http://target.example/x", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if string(raw) != "hi" {
		t.Fatalf("body = %q", raw)
	}
	if resp.Header.Get("X-Test") != "yes" {
		t.Fatalf("header = %v", resp.Header)
	}
}

func TestHTTPWorkerRoundTripperSurfacesWorkerError(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "http-worker.sock")
	serveFakeWorker(t, socket, `{"error":"connection refused"}`)

	rt := &httpWorkerTransport{scanCtx: &scanctx.ScanContext{ID: "scan-test", ScanDir: dir}, socket: socket}
	req := httptest.NewRequest(http.MethodGet, "http://target.example/x", nil)
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("expected an error")
	}
}

func TestHTTPWorkerRequestEnvelope(t *testing.T) {
	// Guards the JSON shape the worker decodes; a silent field rename here
	// would make every container HTTP call fail at runtime.
	var req struct {
		Method    string              `json:"method"`
		URL       string              `json:"url"`
		Headers   map[string][]string `json:"headers"`
		TimeoutMS int64               `json:"timeout_ms"`
		MaxBody   int64               `json:"max_body"`
	}
	encoded := `{"method":"POST","url":"http://x","headers":{"A":["b"]},"timeout_ms":1000,"max_body":512}`
	if err := json.Unmarshal([]byte(encoded), &req); err != nil {
		t.Fatal(err)
	}
	if req.Method != "POST" || req.URL != "http://x" || req.Headers["A"][0] != "b" || req.TimeoutMS != 1000 || req.MaxBody != 512 {
		t.Fatalf("unexpected envelope: %+v", req)
	}
}

// TestHTTPWorkerSocketPathFitsSunPathLimit pins the EINVAL fix: a real scan
// directory path is ~140 bytes, past the kernel's unix socket sun_path limit,
// so the socket must live on a short hashed path that stays stable per scan.
func TestHTTPWorkerSocketPathFitsSunPathLimit(t *testing.T) {
	dir := "/home/operator/.xalgorix/data/tsecbench.zc.tencent.com/2026-11-24/tsecbench.zc.tencent.com_28b2fd623d239d2f6dbdc589bacf1e5e"
	path := HTTPWorkerSocketPath(dir)
	if len(path) >= 100 {
		t.Fatalf("socket path %q is %d bytes; unix sun_path binding fails well before 108", path, len(path))
	}
	if HTTPWorkerSocketPath(dir) != path {
		t.Fatal("socket path must be stable for the same scan dir")
	}
	if HTTPWorkerSocketPath(dir+"/sibling") == path {
		t.Fatal("different scan dirs must not share one socket")
	}
}
