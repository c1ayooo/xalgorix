package llm

// keyfailover_test.go — end-to-end key-pool failover.
//
// The pool's value is not rotation but failover: when the key that served a
// request is out of credit, the SAME request must be retried immediately on
// another key instead of failing the scan or waiting out the agent's backoff.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
)

func TestChatWithRetryFailsOverOnQuota(t *testing.T) {
	freshKeys(t)
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		seen = append(seen, key)
		mu.Unlock()
		if key == "k-dead" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = io.WriteString(w, `{"error":{"message":"Insufficient balance; Go covers only OpenCode Go provider","type":"insufficient_quota"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	cfg := &config.Config{
		APIBase:           srv.URL,
		APIKey:            "k-dead",
		APIKeys:           []string{"k-live"},
		LLM:               "test-model",
		LLMMaxRetries:     3,
		LLMTimeoutSeconds: 10,
	}
	c := NewClient(cfg)
	out, _, err := c.chatWithRetry([]Message{{Role: "user", Content: "hi"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("request must fail over to the live key, got error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("answer = %q, want ok", out)
	}
	mu.Lock()
	tried := append([]string(nil), seen...)
	mu.Unlock()
	if len(tried) < 2 || tried[0] != "k-dead" || tried[1] != "k-live" {
		t.Fatalf("expected dead→live failover, saw %v", tried)
	}
	if !keyHealthOpen(KeyFingerprint("k-dead")) {
		t.Fatal("the exhausted key must be cooling after the failover")
	}
	if keyHealthOpen(KeyFingerprint("k-live")) {
		t.Fatal("the live key must stay healthy")
	}
}

// TestChatWithRetryReportsExhaustedPool keeps the honest failure: when every
// key is dead the caller still gets a clear error instead of a silent loop.
func TestChatWithRetryReportsExhaustedPool(t *testing.T) {
	freshKeys(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(w, `{"error":{"message":"insufficient_quota: credit balance is too low"}}`)
	}))
	defer srv.Close()

	cfg := &config.Config{
		APIBase:       srv.URL,
		APIKey:        "k-a",
		APIKeys:       []string{"k-b"},
		LLM:           "test-model",
		LLMMaxRetries: 3,
	}
	c := NewClient(cfg)
	if _, _, err := c.chatWithRetry([]Message{{Role: "user", Content: "hi"}}, nil, nil, nil); err == nil {
		t.Fatal("an exhausted pool must surface an error")
	}
	for _, k := range []string{"k-a", "k-b"} {
		if !keyHealthOpen(KeyFingerprint(k)) {
			t.Fatalf("key %q must be cooling after the failed attempt", k)
		}
	}
}
