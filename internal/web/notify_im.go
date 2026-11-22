package web

// Domestic IM robot notifications (DingTalk / Feishu / WeCom group custom
// robots). The channel implementations are ported from the 4zreco project;
// the delivery model follows this file's Discord/Telegram senders: operator
// configured via environment settings, fire-and-forget in a goroutine with a
// bounded timeout, and a per-channel minimum-severity threshold.
//
// Platform traps preserved from the port source:
//   - DingTalk returns HTTP 200 for business failures; only the errcode field
//     in the body tells them apart. Signing (加签) is HMAC-SHA256 over
//     timestamp+"\n"+secret with the secret as the HMAC key.
//   - Feishu's sign algorithm is the exact inverse of DingTalk's: the HMAC key
//     is timestamp+"\n"+secret and the message is empty. Copying one
//     implementation to the other fails signature validation (code 19021).
//   - WeCom's markdown content is capped at 4096 bytes (not runes), so the
//     text must be truncated client-side on UTF-8 rune boundaries — the
//     platform rejects the whole message otherwise.
//   - Feishu also has a self-built-app (应用机器人) mode: an App ID/Secret is
//     exchanged for a cached tenant_access_token and the same card is posted
//     to the IM Open API with an explicit receive_id. The group robot webhook
//     stays the default when the app fields are absent.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/c1ayooo/xalgorix/v4/internal/safe"
)

const (
	// weComMarkdownLimit is WeCom's hard byte cap on markdown content. The
	// tightest of the three channels: Chinese text costs 3 bytes/char, so
	// roughly 1300 characters are available.
	weComMarkdownLimit = 4096
	// dingTalkMarkdownLimit is a conservative cap on the markdown body (no
	// documented byte limit, but the description must not balloon).
	dingTalkMarkdownLimit = 20000
	// feishuMaxCardBytes keeps the interactive card well below Feishu's card
	// size limit (oversized cards are rejected as a whole).
	feishuMaxCardBytes = 24000
)

func (s *Server) dingTalkConfigured() bool { return s.dingTalkWebhook != "" }
func (s *Server) feishuConfigured() bool   { return s.feishuWebhook != "" || s.feishuAppConfigured() }
func (s *Server) weComConfigured() bool    { return s.weComWebhook != "" }

// feishuAppConfigured reports whether the self-built-app shape is complete.
// All three parts are required: the credentials identify the app, the receive
// ID identifies the conversation, and one is useless without the other. A
// partially filled app config falls back to the webhook, so a half-saved
// change can never silently stop notifications.
func (s *Server) feishuAppConfigured() bool {
	return s.feishuAppID != "" && s.feishuAppSecret != "" && s.feishuReceiveID != ""
}

// sendDomesticIM fans one notification out to every configured domestic IM
// channel. severity follows severityMeetsThreshold semantics ("": always
// send) and is applied per channel, mirroring the independent Discord and
// Telegram thresholds. IM robot webhooks cannot carry file attachments, so
// report-bearing notifications send their text form (the title already says
// the report is ready; the PDF stays on the dashboard).
func (s *Server) sendDomesticIM(severity string, color int, title, description string) {
	if s.dingTalkConfigured() && severityMeetsThreshold(severity, s.dingTalkMinSeverity) {
		s.sendDingTalk(color, title, description)
	}
	if s.feishuConfigured() && severityMeetsThreshold(severity, s.feishuMinSeverity) {
		s.sendFeishu(color, title, description)
	}
	if s.weComConfigured() && severityMeetsThreshold(severity, s.weComMinSeverity) {
		s.sendWeCom(color, title, description)
	}
}

// sendDingTalk posts a markdown message to the configured DingTalk robot,
// appending the timestamp+sign query parameters when a sign secret is set.
func (s *Server) sendDingTalk(color int, title, description string) {
	if !s.dingTalkConfigured() {
		return
	}
	endpoint, err := dingTalkSignedURL(s.dingTalkWebhook, s.dingTalkSecret, time.Now())
	if err != nil {
		log.Printf("DingTalk webhook error: %v", err)
		return
	}
	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"title": title, "text": truncateIMText(description, dingTalkMarkdownLimit)},
	}
	s.postIMMessage("DingTalk", endpoint, payload, dingTalkResponseError)
}

// sendFeishu posts an interactive card to the configured Feishu/Lark target.
// App mode wins when its three fields are complete; otherwise the group robot
// webhook is used. The header color template is derived from the notification
// color the Discord/Telegram paths already carry, so severity keeps its
// visual weight in both modes.
func (s *Server) sendFeishu(color int, title, description string) {
	if !s.feishuConfigured() {
		return
	}
	if s.feishuAppConfigured() {
		s.sendFeishuApp(color, title, description)
		return
	}
	card := feishuCard(color, title, description)
	payload := map[string]any{"msg_type": "interactive", "card": card}
	// The sign fields sit next to the message and only appear when signing
	// is configured (the plain webhook stays valid without them).
	if s.feishuSecret != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		payload["timestamp"] = ts
		payload["sign"] = feishuSign(ts, s.feishuSecret)
	}
	s.postIMMessage("Feishu", s.feishuWebhook, payload, feishuResponseError)
}

// feishuCard builds the interactive card shared by both delivery modes.
func feishuCard(color int, title, description string) map[string]any {
	return map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"title":    map[string]any{"tag": "plain_text", "content": title},
			"template": feishuTemplateForColor(color),
		},
		"elements": []any{feishuMarkdownDiv(truncateIMText(description, feishuMaxCardBytes))},
	}
}

// Feishu Open API endpoints. Declared as vars (not consts) so tests can point
// them at an httptest stub; production code never reassigns them. Pinned to
// the official host for the same reason as telegramAPIBase: credentials must
// never be steerable to an attacker-influenced base URL.
var (
	feishuTokenURL    = "https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal"
	feishuMessagesURL = "https://open.feishu.cn/open-apis/im/v1/messages"
)

const (
	// feishuIMBodyLimit bounds every Open API response copy we retain. The
	// card itself is already size-capped by feishuMaxCardBytes.
	feishuIMBodyLimit = 8 << 10
	// feishuDefaultReceiveIDType mirrors the IM API's own default.
	feishuDefaultReceiveIDType = "open_id"
)

// feishuReceiveIDTypes is the set the IM API accepts; an unknown value is
// normalized to the default rather than sent through (it would be rejected).
var feishuReceiveIDTypes = map[string]bool{
	"open_id": true, "user_id": true, "union_id": true, "email": true, "chat_id": true,
}

// feishuTokenStaleCodes are the Open API codes meaning the cached
// tenant_access_token is no longer valid. The cache expires 5 minutes early,
// so these only fire on clock skew; one forced refresh and resend recovers
// without a user-visible loss.
var feishuTokenStaleCodes = map[int]bool{99991663: true, 99991661: true, 99991679: true}

// sendFeishuApp delivers the card through the self-built-app IM API. The
// tenant_access_token is fetched (and cached) at send time, and the exchange
// runs in the same fire-and-forget goroutine as the webhook path, so a slow
// credential check never stalls a scan.
func (s *Server) sendFeishuApp(color int, title, description string) {
	card := feishuCard(color, title, description)
	content, err := json.Marshal(card)
	if err != nil {
		log.Printf("Feishu app webhook error: failed to marshal card: %v", err)
		return
	}
	endpoint := feishuMessagesURL + "?receive_id_type=" + url.QueryEscape(normalizeFeishuReceiveIDType(s.feishuReceiveIDType))
	// The IM API's content is the card JSON as a STRING, not a nested object.
	payload := map[string]any{
		"receive_id": s.feishuReceiveID,
		"msg_type":   "interactive",
		"content":    string(content),
	}
	appID, appSecret := s.feishuAppID, s.feishuAppSecret

	go func() {
		defer safe.Recover("im.Feishu.app", "")
		code, msg, err := s.feishuAppSendOnce(endpoint, appID, appSecret, false, payload)
		if err != nil {
			log.Printf("Feishu app webhook error: %v", err)
			return
		}
		if feishuTokenStaleCodes[code] {
			// The token expired between cache and use: force a refresh and
			// resend exactly once (cards are side-effect free).
			if code, msg, err = s.feishuAppSendOnce(endpoint, appID, appSecret, true, payload); err != nil {
				log.Printf("Feishu app webhook error: %v", err)
				return
			}
		}
		if code != 0 {
			log.Printf("Feishu app webhook error: code %d: %s", code, msg)
		}
	}()
}

// feishuAppSendOnce posts one message with a (possibly cached) tenant token and
// returns the platform business code. Transport and parse failures come back
// as errors; a non-zero business code without an error means the call reached
// Feishu but was rejected.
func (s *Server) feishuAppSendOnce(endpoint, appID, appSecret string, force bool, payload map[string]any) (int, string, error) {
	token, err := feishuTenantToken(s, appID, appSecret, force)
	if err != nil {
		return 0, "", err
	}
	raw, err := s.feishuPostJSON(endpoint, map[string]string{"Authorization": "Bearer " + token}, payload)
	if err != nil {
		return 0, "", err
	}
	var res struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, "", fmt.Errorf("unparseable response: %w (%s)", err, imSnippet(raw))
	}
	return res.Code, res.Msg, nil
}

// feishuPostJSON is the shared Open API POST: it uses the guarded notification
// client (so the fixed Feishu host still gets the dial-time metadata/link-local
// guard and redirects stay refused) and bounds the response copy.
func (s *Server) feishuPostJSON(endpoint string, headers map[string]string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		// Never echo url.Parse's error: it carries the full target.
		return nil, fmt.Errorf("invalid endpoint (%s)", redactWebhookTarget(endpoint))
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := s.notificationClient(endpoint)
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New(imTransportError(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, feishuIMBodyLimit))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, imSnippet(raw))
	}
	return raw, nil
}

// feishuTokenEntry caches one tenant_access_token per app_id. The token is a
// derived credential shared process-wide: re-fetching per message would hammer
// the credential endpoint (which is rate limited) for no benefit.
type feishuTokenEntry struct {
	token    string
	expireAt time.Time
}

var (
	feishuTokenMu    sync.Mutex
	feishuTokenCache = map[string]feishuTokenEntry{}
)

// feishuTenantToken returns a cached tenant_access_token for appID, refreshing
// it when missing, forced, or within 5 minutes of expiry.
func feishuTenantToken(s *Server, appID, appSecret string, force bool) (string, error) {
	feishuTokenMu.Lock()
	defer feishuTokenMu.Unlock()
	if e, ok := feishuTokenCache[appID]; ok && !force && time.Now().Before(e.expireAt) {
		return e.token, nil
	}
	raw, err := s.feishuPostJSON(feishuTokenURL, nil, map[string]string{"app_id": appID, "app_secret": appSecret})
	if err != nil {
		return "", fmt.Errorf("tenant token request: %w", err)
	}
	var res struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int64  `json:"expire"` // seconds
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("unparseable token response: %w (%s)", err, imSnippet(raw))
	}
	if res.Code != 0 {
		return "", fmt.Errorf("app credentials rejected (%d): %s", res.Code, res.Msg)
	}
	if res.TenantAccessToken == "" {
		return "", errors.New("token response missing tenant_access_token")
	}
	ttl := time.Duration(res.Expire) * time.Second
	if ttl > 5*time.Minute {
		ttl -= 5 * time.Minute
	} else {
		ttl /= 2
	}
	feishuTokenCache[appID] = feishuTokenEntry{token: res.TenantAccessToken, expireAt: time.Now().Add(ttl)}
	return res.TenantAccessToken, nil
}

// normalizeFeishuReceiveIDType maps an unknown/empty type onto the API default
// so a typo cannot make every send fail validation.
func normalizeFeishuReceiveIDType(raw string) string {
	t := strings.ToLower(strings.TrimSpace(raw))
	if !feishuReceiveIDTypes[t] {
		return feishuDefaultReceiveIDType
	}
	return t
}

// sendWeCom posts a markdown message to the configured WeCom group robot.
// There is no separate title field, so the title opens the content as a bold
// line before the 4096-byte cap is applied.
func (s *Server) sendWeCom(color int, title, description string) {
	if !s.weComConfigured() {
		return
	}
	content := truncateIMText("**"+title+"**\n"+description, weComMarkdownLimit)
	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": content},
	}
	s.postIMMessage("WeCom", s.weComWebhook, payload, weComResponseError)
}

// postIMMessage delivers one domestic-IM payload. Fire-and-forget in a
// goroutine, mirroring sendSimpleEmbedTo. The client is always the guarded
// one: an IM webhook never equals the operator's Discord endpoint, so
// notificationClient applies the dial-time guard (this machine, the dashboard
// listener, link-local/metadata addresses) and refuses redirects — the robot
// URLs embed their credential (access_token / key / hook id), and following a
// redirect would hand that credential to the redirect target.
func (s *Server) postIMMessage(channel, endpoint string, payload any, responseError func([]byte) error) {
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("%s webhook error: failed to marshal payload: %v", channel, err)
		return
	}
	client := s.notificationClient(endpoint)
	go func() {
		defer safe.Recover("im."+channel, "")
		resp, err := client.Post(endpoint, "application/json; charset=utf-8", bytes.NewReader(body))
		if err != nil {
			log.Printf("%s webhook error: %v", channel, imTransportError(err))
			return
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			log.Printf("%s webhook HTTP error: %d %s", channel, resp.StatusCode, imSnippet(raw))
			return
		}
		if responseError != nil {
			if rerr := responseError(raw); rerr != nil {
				log.Printf("%s webhook error: %v", channel, rerr)
			}
		}
	}()
}

// dingTalkResponseError surfaces the business failure DingTalk hides inside
// HTTP 200. 301000 is a signature failure and 310000 a keyword mismatch —
// configuration errors that no retry heals.
func dingTalkResponseError(raw []byte) error {
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("unparseable response: %w (%s)", err, imSnippet(raw))
	}
	if res.ErrCode != 0 {
		return fmt.Errorf("errcode %d: %s", res.ErrCode, res.ErrMsg)
	}
	return nil
}

// feishuResponseError handles both field namings seen across Feishu robot
// versions (code/msg and StatusCode/StatusMessage).
func feishuResponseError(raw []byte) error {
	var res struct {
		Code          int    `json:"code"`
		Msg           string `json:"msg"`
		StatusCode    int    `json:"StatusCode"`
		StatusMessage string `json:"StatusMessage"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("unparseable response: %w (%s)", err, imSnippet(raw))
	}
	if res.Code != 0 {
		return fmt.Errorf("code %d: %s", res.Code, res.Msg)
	}
	if res.StatusCode != 0 {
		return fmt.Errorf("code %d: %s", res.StatusCode, res.StatusMessage)
	}
	return nil
}

// weComResponseError distinguishes the retryable rate-limit code (45009 — the
// platform window rolls, a later send can succeed) from permanent failures
// (93000 is an invalid webhook key; only the configured value fixes it).
func weComResponseError(raw []byte) error {
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("unparseable response: %w (%s)", err, imSnippet(raw))
	}
	if res.ErrCode != 0 {
		if res.ErrCode == 45009 {
			return fmt.Errorf("rate limited (errcode %d): %s", res.ErrCode, res.ErrMsg)
		}
		return fmt.Errorf("errcode %d: %s", res.ErrCode, res.ErrMsg)
	}
	return nil
}

// dingTalkSignedURL appends the timestamp and sign parameters per DingTalk's
// official rule: HMAC-SHA256 of timestamp+"\n"+secret with the secret as the
// key, base64-encoded. An empty secret returns the hook unchanged (robots
// with keyword/IP security don't sign).
func dingTalkSignedURL(hook, secret string, now time.Time) (string, error) {
	if secret == "" {
		return hook, nil
	}
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	u, err := url.Parse(hook)
	if err != nil {
		// Never pass url.Parse's error through: its text carries the full
		// URL, and the query string embeds the robot's access_token.
		return "", fmt.Errorf("invalid webhook URL (%s)", redactWebhookTarget(hook))
	}
	q := u.Query()
	q.Set("timestamp", ts)
	q.Set("sign", sign)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// feishuSign computes Feishu's webhook signature. The HMAC key is
// timestamp+"\n"+secret and the message is empty — the inverse of DingTalk's
// algorithm, where the secret is the key and the signed string is the
// message. See the package comment before "simplifying" either one.
func feishuSign(timestamp, secret string) string {
	mac := hmac.New(sha256.New, []byte(timestamp+"\n"+secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// feishuMarkdownDiv builds a lark_md content block for the card body. The
// notification descriptions are authored in the same Markdown subset the
// Discord path uses (**bold**, links), which lark_md renders natively.
func feishuMarkdownDiv(content string) map[string]any {
	return map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": content}}
}

// feishuTemplateForColor maps the notification colors the existing senders
// pass (Discord embed colors) onto Feishu card header templates, keeping the
// severity/meaning mapping consistent with the other channels. Unknown colors
// use grey rather than blue so they can't be mistaken for low-severity.
func feishuTemplateForColor(color int) string {
	switch color {
	case 0xff6b6b, 0xef4444:
		return "red" // critical/high vulns, restart/error notices
	case 0xd97706:
		return "orange" // medium vulns
	case 0x2dd4bf, 0x00ff88:
		return "green" // scan started, clean reports
	case 0x3b82f6, 0x4dabf7:
		return "blue" // informational (scan finished, low/info vulns)
	default:
		return "grey"
	}
}

// truncateIMText caps s at max bytes without splitting a UTF-8 rune, escaping
// nothing: platform byte caps (WeCom 4096) reject or mangle mid-rune cuts.
// max<=0 means unlimited. Appends an ellipsis unless max can't hold one.
func truncateIMText(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	const ellipsis = "…"
	budget := max - len(ellipsis)
	suffix := ellipsis
	if budget < 0 {
		budget = max
		suffix = ""
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

// imSnippet compresses a response body into one bounded line for logs.
func imSnippet(raw []byte) string {
	s := strings.Join(strings.Fields(string(raw)), " ")
	runes := []rune(s)
	if len(runes) > 200 {
		return string(runes[:199]) + "…"
	}
	return s
}

// redactWebhookTarget reduces a robot webhook URL to scheme://host/… for
// errors. This is deliberately blunt: the credential sits in the query
// (DingTalk access_token, WeCom key) or the path tail (Feishu hook id), and
// no generic rule can tell which part is safe to echo, so nothing past the
// host ever is.
func redactWebhookTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(webhook URL unparseable)"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// imTransportError strips the credential-bearing target from transport
// errors: *url.Error.Error() prints the full URL, and the robot URLs embed
// their credential in the query or path. Keep the operation and host only.
func imTransportError(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		target := redactWebhookTarget(uerr.URL)
		if uerr.Err != nil {
			return fmt.Sprintf("%s %s: %v", uerr.Op, target, uerr.Err)
		}
		return fmt.Sprintf("%s %s", uerr.Op, target)
	}
	return err.Error()
}
