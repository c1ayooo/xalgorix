package web

// Feishu self-built-app (应用机器人) delivery tests. The app mode is the
// 4zreco-ported path: App ID + Secret are exchanged for a cached
// tenant_access_token, then the same interactive card is posted to the IM Open
// API with an explicit receive_id.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/config"
)

// feishuStub records what the Open API stub received. Token and message calls
// are separable so a test can assert the token was cached (one call for many
// sends) and that a stale-token code triggers exactly one refresh.
type feishuStub struct {
	mu          sync.Mutex
	tokenCalls  atomic.Int32
	msgCalls    atomic.Int32
	tokenBodies []map[string]string
	authHeaders []string
	msgQueries  []string
	msgBodies   []map[string]any
	msgCodes    []int // business codes returned, in order; exhausted → success
}

func newFeishuStub(t *testing.T) (*feishuStub, *httptest.Server) {
	t.Helper()
	stub := &feishuStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/tenant_access_token/"):
			stub.tokenCalls.Add(1)
			var body map[string]string
			_ = json.Unmarshal(raw, &body)
			stub.mu.Lock()
			stub.tokenBodies = append(stub.tokenBodies, body)
			stub.mu.Unlock()
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`))
		case strings.Contains(r.URL.Path, "/im/v1/messages"):
			stub.msgCalls.Add(1)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			stub.mu.Lock()
			stub.authHeaders = append(stub.authHeaders, r.Header.Get("Authorization"))
			stub.msgQueries = append(stub.msgQueries, r.URL.RawQuery)
			stub.msgBodies = append(stub.msgBodies, body)
			code := 0
			if len(stub.msgCodes) > 0 {
				code = stub.msgCodes[0]
				stub.msgCodes = stub.msgCodes[1:]
			}
			stub.mu.Unlock()
			if code != 0 {
				_, _ = w.Write([]byte(`{"code":` + strconv.Itoa(code) + `,"msg":"stale token"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	prevToken, prevMessages := feishuTokenURL, feishuMessagesURL
	feishuTokenURL = srv.URL + "/open-apis/auth/v3/tenant_access_token/internal"
	feishuMessagesURL = srv.URL + "/open-apis/im/v1/messages"
	t.Cleanup(func() {
		feishuTokenURL, feishuMessagesURL = prevToken, prevMessages
	})
	return stub, srv
}

func waitFeishuMessages(t *testing.T, stub *feishuStub, want int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for stub.msgCalls.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d Feishu app messages (got %d)", want, stub.msgCalls.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// resetFeishuTokenCache isolates every test from the process-wide cache.
func resetFeishuTokenCache(t *testing.T) {
	t.Helper()
	clear := func() {
		feishuTokenMu.Lock()
		feishuTokenCache = map[string]feishuTokenEntry{}
		feishuTokenMu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

func newFeishuAppServer(t *testing.T, mutate func(*config.Config)) *Server {
	t.Helper()
	resetFeishuTokenCache(t)
	return newIMTestServer(t, func(cfg *config.Config) {
		cfg.FeishuAppID = "cli_test_app"
		cfg.FeishuAppSecret = "secret_test"
		cfg.FeishuReceiveID = "oc_test_chat"
		cfg.FeishuReceiveIDType = "chat_id"
		if mutate != nil {
			mutate(cfg)
		}
	})
}

func TestFeishuAppModeDeliversInteractiveCard(t *testing.T) {
	stub, _ := newFeishuStub(t)
	s := newFeishuAppServer(t, nil)

	s.sendDomesticIM("critical", 0xef4444, "🐛 CRITICAL Vulnerability Found", "**Targets:** example.com")
	waitFeishuMessages(t, stub, 1)

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.tokenBodies) != 1 {
		t.Fatalf("token calls = %d, want 1", len(stub.tokenBodies))
	}
	if stub.tokenBodies[0]["app_id"] != "cli_test_app" || stub.tokenBodies[0]["app_secret"] != "secret_test" {
		t.Fatalf("token request carried wrong credentials: %+v", stub.tokenBodies[0])
	}
	if stub.authHeaders[0] != "Bearer t-test" {
		t.Fatalf("Authorization = %q, want Bearer t-test", stub.authHeaders[0])
	}
	if !strings.Contains(stub.msgQueries[0], "receive_id_type=chat_id") {
		t.Fatalf("query = %q, want receive_id_type=chat_id", stub.msgQueries[0])
	}
	body := stub.msgBodies[0]
	if body["receive_id"] != "oc_test_chat" || body["msg_type"] != "interactive" {
		t.Fatalf("unexpected message body: %+v", body)
	}
	content, ok := body["content"].(string)
	if !ok {
		t.Fatalf("content must be a JSON STRING (IM API shape), got %T", body["content"])
	}
	var card struct {
		Header struct {
			Template string `json:"template"`
			Title    struct {
				Content string `json:"content"`
			} `json:"title"`
		} `json:"header"`
		Elements []struct {
			Text struct {
				Content string `json:"content"`
			} `json:"text"`
		} `json:"elements"`
	}
	if err := json.Unmarshal([]byte(content), &card); err != nil {
		t.Fatalf("content is not a card JSON document: %v", err)
	}
	if card.Header.Template != "red" || card.Header.Title.Content != "🐛 CRITICAL Vulnerability Found" {
		t.Fatalf("unexpected card header: %+v", card.Header)
	}
	if len(card.Elements) != 1 || !strings.Contains(card.Elements[0].Text.Content, "**Targets:** example.com") {
		t.Fatalf("unexpected card body: %+v", card.Elements)
	}
}

func TestFeishuAppModeCachesTenantToken(t *testing.T) {
	stub, _ := newFeishuStub(t)
	s := newFeishuAppServer(t, nil)

	s.sendFeishu(0x3b82f6, "first", "x")
	waitFeishuMessages(t, stub, 1)
	s.sendFeishu(0x3b82f6, "second", "y")
	waitFeishuMessages(t, stub, 2)

	if got := stub.tokenCalls.Load(); got != 1 {
		t.Fatalf("token endpoint calls = %d, want 1 (cache must be reused)", got)
	}
}

func TestFeishuAppModeRefreshesStaleTokenOnce(t *testing.T) {
	stub, _ := newFeishuStub(t)
	stub.msgCodes = []int{99991663} // first message reports an expired token
	s := newFeishuAppServer(t, nil)

	s.sendFeishu(0xef4444, "stale", "x")
	waitFeishuMessages(t, stub, 2)

	if got := stub.tokenCalls.Load(); got != 2 {
		t.Fatalf("token endpoint calls = %d, want 2 (one initial + one forced refresh)", got)
	}
}

func TestFeishuAppModeRequiresAllFields(t *testing.T) {
	stub, _ := newFeishuStub(t)
	// App ID only: incomplete app config must fall back to the webhook, and
	// with no webhook either the channel stays silent.
	s := newFeishuAppServer(t, func(cfg *config.Config) {
		cfg.FeishuAppSecret = ""
		cfg.FeishuReceiveID = ""
	})

	s.sendFeishu(0xef4444, "incomplete", "x")
	time.Sleep(300 * time.Millisecond)
	if stub.msgCalls.Load() != 0 || stub.tokenCalls.Load() != 0 {
		t.Fatalf("incomplete app config must not call the Open API (msgs=%d tokens=%d)",
			stub.msgCalls.Load(), stub.tokenCalls.Load())
	}
}

func TestNormalizeFeishuReceiveIDType(t *testing.T) {
	cases := map[string]string{
		"":         "open_id",
		"  ":       "open_id",
		"chat_id":  "chat_id",
		"CHAT_ID":  "chat_id",
		"email":    "email",
		"bogus":    "open_id",
		"open_id":  "open_id",
		"user_id":  "user_id",
		"union_id": "union_id",
	}
	for in, want := range cases {
		if got := normalizeFeishuReceiveIDType(in); got != want {
			t.Errorf("normalizeFeishuReceiveIDType(%q) = %q, want %q", in, got, want)
		}
	}
}
