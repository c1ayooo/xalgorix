package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xalgord/xalgorix/v4/internal/config"
)

// newIMCaptureServer returns an httptest server that records every request
// body on a channel and answers with a body satisfying all three response
// parsers (errcode=0 / code=0).
func newIMCaptureServer(t *testing.T) (*httptest.Server, chan []byte) {
	t.Helper()
	received := make(chan []byte, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- b
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok","code":0,"msg":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, received
}

func waitIMPayload(t *testing.T, ch chan []byte) []byte {
	t.Helper()
	select {
	case b := <-ch:
		return b
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for IM delivery")
		return nil
	}
}

func assertNoIMPayload(t *testing.T, ch chan []byte) {
	t.Helper()
	select {
	case b := <-ch:
		t.Fatalf("unexpected IM delivery: %s", b)
	case <-time.After(300 * time.Millisecond):
	}
}

// newIMTestServer builds a Server with local-target delivery allowed (the
// capture listeners are loopback httptest servers) and lets each test seed
// the IM channel settings.
func newIMTestServer(t *testing.T, mutate func(*config.Config)) *Server {
	t.Helper()
	cfg := &config.Config{AllowLocalTargets: true}
	if mutate != nil {
		mutate(cfg)
	}
	return newWebhookTestServer(t, cfg)
}

func TestDomesticIMFanOutDeliversAllChannelShapes(t *testing.T) {
	dingSrv, dingCh := newIMCaptureServer(t)
	feishuSrv, feishuCh := newIMCaptureServer(t)
	wecomSrv, wecomCh := newIMCaptureServer(t)
	s := newIMTestServer(t, func(cfg *config.Config) {
		cfg.DingTalkWebhook = dingSrv.URL
		cfg.DingTalkSecret = "SECtest"
		cfg.FeishuWebhook = feishuSrv.URL
		cfg.WeComWebhook = wecomSrv.URL
	})

	const desc = "**Targets:** example.com\n**Mode:** full"
	s.sendDomesticIM("", 0xef4444, "🐛 CRITICAL Vulnerability Found", desc)

	// DingTalk: markdown payload, signed URL params appended.
	var dingPayload struct {
		MsgType  string `json:"msgtype"`
		Markdown struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"markdown"`
	}
	if err := json.Unmarshal(waitIMPayload(t, dingCh), &dingPayload); err != nil {
		t.Fatalf("dingtalk payload: %v", err)
	}
	if dingPayload.MsgType != "markdown" || dingPayload.Markdown.Title != "🐛 CRITICAL Vulnerability Found" ||
		!strings.Contains(dingPayload.Markdown.Text, "**Targets:** example.com") {
		t.Fatalf("unexpected dingtalk payload: %+v", dingPayload)
	}

	// Feishu: interactive card with the severity-derived header template.
	var feishuPayload struct {
		MsgType string `json:"msg_type"`
		Card    struct {
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
		} `json:"card"`
	}
	if err := json.Unmarshal(waitIMPayload(t, feishuCh), &feishuPayload); err != nil {
		t.Fatalf("feishu payload: %v", err)
	}
	if feishuPayload.MsgType != "interactive" || feishuPayload.Card.Header.Template != "red" ||
		feishuPayload.Card.Header.Title.Content != "🐛 CRITICAL Vulnerability Found" ||
		len(feishuPayload.Card.Elements) != 1 ||
		!strings.Contains(feishuPayload.Card.Elements[0].Text.Content, "**Mode:** full") {
		t.Fatalf("unexpected feishu payload: %+v", feishuPayload)
	}

	// WeCom: markdown content opens with the bolded title.
	var wecomPayload struct {
		Markdown struct {
			Content string `json:"content"`
		} `json:"markdown"`
	}
	if err := json.Unmarshal(waitIMPayload(t, wecomCh), &wecomPayload); err != nil {
		t.Fatalf("wecom payload: %v", err)
	}
	if !strings.HasPrefix(wecomPayload.Markdown.Content, "**🐛 CRITICAL Vulnerability Found**\n") {
		t.Fatalf("unexpected wecom content: %q", wecomPayload.Markdown.Content)
	}
}

func TestDomesticIMSeverityGatingPerChannel(t *testing.T) {
	dingSrv, dingCh := newIMCaptureServer(t)
	feishuSrv, feishuCh := newIMCaptureServer(t)
	wecomSrv, wecomCh := newIMCaptureServer(t)
	s := newIMTestServer(t, func(cfg *config.Config) {
		cfg.DingTalkWebhook = dingSrv.URL
		cfg.DingTalkMinSeverity = "high"
		cfg.FeishuWebhook = feishuSrv.URL
		cfg.FeishuMinSeverity = "critical"
		cfg.WeComWebhook = wecomSrv.URL
		cfg.WeComMinSeverity = "high"
	})

	// Below every threshold: nothing is delivered anywhere.
	s.sendDomesticIM("low", 0x3b82f6, "🐛 LOW Vulnerability Found", "x")
	assertNoIMPayload(t, dingCh)
	assertNoIMPayload(t, feishuCh)
	assertNoIMPayload(t, wecomCh)

	// Meets high but not critical: DingTalk and WeCom fire, Feishu stays quiet.
	s.sendDomesticIM("high", 0xef4444, "🐛 HIGH Vulnerability Found", "x")
	waitIMPayload(t, dingCh)
	assertNoIMPayload(t, feishuCh)
	waitIMPayload(t, wecomCh)

	// Meets everything.
	s.sendDomesticIM("critical", 0xef4444, "🐛 CRITICAL Vulnerability Found", "x")
	waitIMPayload(t, dingCh)
	waitIMPayload(t, feishuCh)
	waitIMPayload(t, wecomCh)
}

func TestDomesticIMOnlyConfiguredChannelsFire(t *testing.T) {
	wecomSrv, wecomCh := newIMCaptureServer(t)
	s := newIMTestServer(t, func(cfg *config.Config) {
		cfg.WeComWebhook = wecomSrv.URL
	})

	s.sendDomesticIM("", 0x3b82f6, "✅ Scan Finished", "x")
	waitIMPayload(t, wecomCh)
}

func TestWeComMarkdownByteCapPreservesUTF8(t *testing.T) {
	wecomSrv, wecomCh := newIMCaptureServer(t)
	s := newIMTestServer(t, func(cfg *config.Config) {
		cfg.WeComWebhook = wecomSrv.URL
	})

	// Chinese text costs 3 bytes/char: a description past the cap must be
	// truncated on a rune boundary, never mid-character.
	s.sendWeCom(0xef4444, "漏洞", strings.Repeat("严重漏洞标题", 1000))
	var payload struct {
		Markdown struct {
			Content string `json:"content"`
		} `json:"markdown"`
	}
	if err := json.Unmarshal(waitIMPayload(t, wecomCh), &payload); err != nil {
		t.Fatalf("wecom payload: %v", err)
	}
	content := payload.Markdown.Content
	if len(content) > weComMarkdownLimit {
		t.Fatalf("content exceeds the %d-byte cap: %d", weComMarkdownLimit, len(content))
	}
	if !utf8.ValidString(content) {
		t.Fatalf("content is not valid UTF-8 (mid-rune cut)")
	}
}

func TestDingTalkSignedURL(t *testing.T) {
	hook := "https://oapi.dingtalk.com/robot/send?access_token=SECRET"
	// No secret: the hook passes through unchanged.
	got, err := dingTalkSignedURL(hook, "", time.UnixMilli(1700000000000))
	if err != nil || got != hook {
		t.Fatalf("unsigned hook: got %q err=%v", got, err)
	}

	now := time.UnixMilli(1700000000000)
	got, err = dingTalkSignedURL(hook, "SECtest", now)
	if err != nil {
		t.Fatalf("signed hook: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse signed: %v", err)
	}
	if q := u.Query(); q.Get("timestamp") != "1700000000000" {
		t.Fatalf("timestamp param: %q", q.Get("timestamp"))
	}
	mac := hmac.New(sha256.New, []byte("SECtest"))
	mac.Write([]byte("1700000000000" + "\n" + "SECtest"))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if u.Query().Get("sign") != want {
		t.Fatalf("sign param: got %q want %q", u.Query().Get("sign"), want)
	}
}

// TestFeishuSignDiffersFromDingTalk pins the inversion trap documented in the
// ported implementations: Feishu's HMAC key is timestamp+"\n"+secret with an
// empty message; DingTalk's key is the secret and the signed string is the
// message. Implementations that share one formula fail platform validation.
func TestFeishuSignDiffersFromDingTalk(t *testing.T) {
	const ts, secret = "1700000000", "SECtest"
	feishu := feishuSign(ts, secret)

	mac := hmac.New(sha256.New, []byte(secret)) // DingTalk's shape
	mac.Write([]byte(ts + "\n" + secret))
	dingTalk := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if feishu == dingTalk {
		t.Fatalf("feishuSign must use key=timestamp+\"\\n\"+secret, not DingTalk's shape")
	}
	if _, err := base64.StdEncoding.DecodeString(feishu); err != nil {
		t.Fatalf("feishuSign is not valid base64: %v", err)
	}
}

func TestTruncateIMTextUTF8Boundary(t *testing.T) {
	s := strings.Repeat("漏洞", 100) // 200 runes = 600 bytes
	got := truncateIMText(s, 100)
	if len(got) > 100 {
		t.Fatalf("len = %d, want <= 100", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("mid-rune cut produced invalid UTF-8")
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated text must end with an ellipsis: %q", got)
	}
	// Exactly at the cap: unchanged.
	if got := truncateIMText(s, len(s)); got != s {
		t.Fatalf("within-cap text was modified")
	}
	// A cap too small for the ellipsis degrades to a bare cut.
	if got := truncateIMText("abc", 2); got != "ab" {
		t.Fatalf("tiny cap: got %q", got)
	}
}

func TestIMResponseErrors(t *testing.T) {
	if err := dingTalkResponseError([]byte(`{"errcode":310000,"errmsg":"keyword not in content"}`)); err == nil ||
		!strings.Contains(err.Error(), "310000") {
		t.Fatalf("dingtalk errcode: %v", err)
	}
	if err := dingTalkResponseError([]byte(`{"errcode":0,"errmsg":"ok"}`)); err != nil {
		t.Fatalf("dingtalk ok: %v", err)
	}
	err := weComResponseError([]byte(`{"errcode":45009,"errmsg":"limit exceeded"}`))
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("wecom 45009 must be reported as a rate limit: %v", err)
	}
	if err := weComResponseError([]byte(`{"errcode":93000,"errmsg":"invalid webhook key"}`)); err == nil {
		t.Fatalf("wecom 93000: %v", err)
	}
	if err := feishuResponseError([]byte(`{"code":19021,"msg":"sign match fail"}`)); err == nil {
		t.Fatalf("feishu code: %v", err)
	}
	if err := feishuResponseError([]byte(`{"StatusCode":19021,"StatusMessage":"sign match fail"}`)); err == nil {
		t.Fatalf("feishu StatusCode form: %v", err)
	}
	if err := feishuResponseError([]byte(`{"code":0}`)); err != nil {
		t.Fatalf("feishu ok: %v", err)
	}
}

// TestIMTransportErrorRedaction ensures the credential-bearing query string
// never reaches logs through transport errors (DingTalk/WeCom embed their
// robot secret in the URL).
func TestIMTransportErrorRedaction(t *testing.T) {
	uerr := &url.Error{
		Op:  "Post",
		URL: "https://oapi.dingtalk.com/robot/send?access_token=SUPERSECRET&timestamp=1&sign=x",
		Err: errors.New("connection refused"),
	}
	got := imTransportError(uerr)
	if strings.Contains(got, "SUPERSECRET") {
		t.Fatalf("transport error leaks the access_token: %q", got)
	}
	if !strings.Contains(got, "oapi.dingtalk.com") || !strings.Contains(got, "connection refused") {
		t.Fatalf("transport error lost the useful parts: %q", got)
	}
	if got := redactWebhookTarget("https://open.feishu.cn/open-apis/bot/v2/hook/HOOKID"); strings.Contains(got, "HOOKID") {
		t.Fatalf("redactWebhookTarget leaks the hook id: %q", got)
	}
	if got := redactWebhookTarget("not a url"); got != "(webhook URL unparseable)" {
		t.Fatalf("unparseable target: %q", got)
	}
}

// TestDomesticIMDialGuardBlocksListener keeps the guard contract visible: an
// IM webhook pointing at the dashboard's own listener must not be deliverable
// even with AllowLocalTargets (the listener is always protected).
func TestDomesticIMDialGuardBlocksListener(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	t.Cleanup(srv.Close)

	s := newIMTestServer(t, func(cfg *config.Config) {
		// Point at the dashboard listener port the test server uses (9137),
		// swapped onto the capture host so the dial guard sees the protected
		// port without this test depending on network topology.
		u, _ := url.Parse(srv.URL)
		cfg.WeComWebhook = "http://127.0.0.1:9137" + u.Path
	})

	s.sendWeCom(0xef4444, "T", "x")
	time.Sleep(300 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatalf("dashboard listener must stay unreachable for IM webhooks")
	}
}
