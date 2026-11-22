package llm

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
)

// Regression: a dead pooled key must be dropped after its first 401, even
// when other requests are in flight. The drop used to mark "the most recent
// pick" — but concurrent requests keep advancing the rotator between the
// pick and the failure report, so the dead key survived every drop and kept
// failing every other request for the rest of the scan (incident 2026-11-22,
// run d8d46a9f: DeepSeek 401 "api key ****1111 is invalid" recurring at
// attempt 1/25 while the healthy key carried every retry).
func TestInvalidPooledKeyDroppedUnderConcurrency(t *testing.T) {
	goodKey := "k-good"
	badKey := "k-dead-1111"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.Header.Get("Authorization"), badKey) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"Authentication Fails, Your api key: ****1111 is invalid (request_id: t)","type":"authentication_error","param":null,"code":"invalid_request_error"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		APIKey:        goodKey,
		APIKeys:       []string{badKey},
		LLM:           "deepseek-chat",
		LLMProvider:   "deepseek",
		LLMMaxRetries: 3,
	}
	// Scan-shaped client: a provider-profile endpoint whose key equals
	// cfg.APIKey, so applyKeyRotation spreads traffic across the pool.
	ep := Endpoint{
		URL:         srv.URL + "/chat/completions",
		Model:       "deepseek-chat",
		HeaderStyle: "openai",
		Auth:        AuthAPIKey,
		APIKey:      goodKey,
	}
	c := NewClient(cfg, WithResolver(NewFixedResolver(ep)))
	c.SetContext(t.Context())

	var mu sync.Mutex
	fail401, ok200 := 0, 0
	var wg sync.WaitGroup
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				out, err := c.Chat([]Message{{Role: "user", Content: "ping"}})
				mu.Lock()
				if err != nil {
					if strings.Contains(err.Error(), "401") {
						fail401++
					} else {
						t.Errorf("unexpected non-401 error: %v", err)
					}
				} else if out == "ok" {
					ok200++
				}
				mu.Unlock()
				time.Sleep(2 * time.Millisecond)
			}
		}()
	}
	wg.Wait()

	if fail401 > 1 {
		t.Errorf("dead pooled key survived its drop: %d 401 failures across 30 calls (want at most 1)", fail401)
	}
	if fail401+ok200 == 0 {
		t.Fatal("no calls completed — harness broken")
	}
}
