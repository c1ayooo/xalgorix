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
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xalgord/xalgorix/v4/internal/safe"
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
func (s *Server) feishuConfigured() bool   { return s.feishuWebhook != "" }
func (s *Server) weComConfigured() bool    { return s.weComWebhook != "" }

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

// sendFeishu posts an interactive card to the configured Feishu/Lark robot.
// The header color template is derived from the notification color the
// Discord/Telegram paths already carry, so severity keeps its visual weight.
func (s *Server) sendFeishu(color int, title, description string) {
	if !s.feishuConfigured() {
		return
	}
	card := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"title":    map[string]any{"tag": "plain_text", "content": title},
			"template": feishuTemplateForColor(color),
		},
		"elements": []any{feishuMarkdownDiv(truncateIMText(description, feishuMaxCardBytes))},
	}
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
