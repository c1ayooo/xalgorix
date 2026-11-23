package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeCDPProvider struct {
	endpoint string
	err      error
}

func (f fakeCDPProvider) CDPEndpoint(context.Context, string, string) (string, error) {
	return f.endpoint, f.err
}

// startEchoListener accepts connections and echoes bytes back.
func startEchoListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return ln
}

func dialRelayEndpoint(t *testing.T, endpoint string) net.Conn {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse endpoint %q: %v", endpoint, err)
	}
	conn, err := net.DialTimeout("tcp", parsed.Host, 2*time.Second)
	if err != nil {
		t.Fatalf("dial relay %s: %v", parsed.Host, err)
	}
	return conn
}

func TestCDPRelayForwardsBytes(t *testing.T) {
	upstream := startEchoListener(t)
	relay := NewCDPRelay("127.0.0.1:0", "127.0.0.1")
	defer func() { _ = relay.Close() }()

	endpoint, err := relay.Endpoint(context.Background(), fakeCDPProvider{
		endpoint: "ws://" + upstream.Addr().String() + "/devtools/browser/test",
	}, "project-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(endpoint, "ws://127.0.0.1:") || !strings.HasSuffix(endpoint, "/devtools/browser/test") {
		t.Fatalf("relay endpoint = %q", endpoint)
	}

	conn := dialRelayEndpoint(t, endpoint)
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("relay echoed %q, want ping", buf)
	}
}

func TestCDPRelayReusesListenerAndUpdatesTarget(t *testing.T) {
	first := startEchoListener(t)
	second := startEchoListener(t)
	relay := NewCDPRelay("127.0.0.1:0", "127.0.0.1")
	defer func() { _ = relay.Close() }()

	firstEndpoint, err := relay.Endpoint(context.Background(), fakeCDPProvider{
		endpoint: "ws://" + first.Addr().String() + "/devtools/browser/a",
	}, "project-b", "")
	if err != nil {
		t.Fatal(err)
	}
	secondEndpoint, err := relay.Endpoint(context.Background(), fakeCDPProvider{
		endpoint: "ws://" + second.Addr().String() + "/devtools/browser/b",
	}, "project-b", "")
	if err != nil {
		t.Fatal(err)
	}
	firstURL, err := url.Parse(firstEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	secondURL, err := url.Parse(secondEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if firstURL.Host != secondURL.Host {
		t.Fatalf("same project must reuse its relay listener: %q != %q", firstEndpoint, secondEndpoint)
	}

	conn := dialRelayEndpoint(t, secondEndpoint)
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "pong" {
		t.Fatalf("relay echoed %q, want pong", buf)
	}
}

func TestRunnerdHandlerCDPRoute(t *testing.T) {
	upstream := startEchoListener(t)
	relay := NewCDPRelay("127.0.0.1:0", "127.0.0.1")
	defer func() { _ = relay.Close() }()
	driver := &fakeDriver{cdpEndpoint: "ws://" + upstream.Addr().String() + "/devtools/browser/handler"}
	handler := &RunnerdHandler{Driver: driver, CDP: relay}

	body := strings.NewReader(`{"proxy_url":""}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/projects/project-c/cdp", body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Endpoint == "" {
		t.Fatal("empty endpoint")
	}
	conn := dialRelayEndpoint(t, out.Endpoint)
	_ = conn.Close()

	del := httptest.NewRequest(http.MethodDelete, "/v1/projects/project-c", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, del)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", rec.Code, rec.Body.String())
	}
	relay.mu.Lock()
	remaining := len(relay.relays)
	relay.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("relay map still has %d project(s) after removal", remaining)
	}
	closed := false
	for i := 0; i < 20; i++ {
		if _, err := net.DialTimeout("tcp", mustHost(out.Endpoint), 100*time.Millisecond); err != nil {
			closed = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !closed {
		t.Fatal("relay listener must be closed after project removal")
	}
}

func mustHost(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return parsed.Host
}
