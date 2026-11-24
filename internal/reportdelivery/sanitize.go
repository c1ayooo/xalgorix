package reportdelivery

import (
	"regexp"
	"strings"
)

// Redacted replaces any sensitive value in an exported artifact. The traffic
// archive itself is intentionally unredacted (operator decision, see
// internal/scanctx/traffic.go); every report/export path must pass through
// this sanitizer before the bytes leave the scan boundary.
const Redacted = "<redacted>"

// sensitiveHeaderNames is the header denylist. Values are replaced while the
// header name and layout stay intact so the reader can still see which
// credentials the request carried.
var sensitiveHeaderNames = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"cookie":              {},
	"set-cookie":          {},
	"x-api-key":           {},
	"x-auth-token":        {},
	"x-csrf-token":        {},
	"x-xsrf-token":        {},
	"x-access-token":      {},
	"x-session-token":     {},
	"api-key":             {},
	"apikey":              {},
}

var sensitiveKeyRe = `password|passwd|pwd|pass|token|secret|api[_-]?key|apikey|access[_-]?token|refresh[_-]?token|session[_-]?id|sessionid|session|otp|auth[_-]?key|private[_-]?key|client[_-]?secret`

var (
	// "password": "value" / "token": 123 / "api_key": true
	jsonSensitiveRe = regexp.MustCompile(`(?i)("(?:` + sensitiveKeyRe + `)"\s*:\s*)("(?:[^"\\]|\\.)*"|[^,}\r\n]+)`)
	// password=value&token=value (form body, query string, cookie-like text)
	formSensitiveRe = regexp.MustCompile(`(?i)\b(` + sensitiveKeyRe + `)=([^&\s;,"'}\]]+)`)
	// Authorization: Bearer xyz / Basic xyz
	bearerRe = regexp.MustCompile(`(?i)\b(Bearer|Basic|Digest)\s+[A-Za-z0-9\-._~+/=]{4,}`)
	// Chinese mainland mobile numbers.
	phoneRe = regexp.MustCompile(`\b1[3-9]\d{9}\b`)
	// Email addresses.
	emailRe = regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`)
	// 18-digit Chinese ID card.
	idCardRe = regexp.MustCompile(`\b\d{17}[\dXx]\b`)
)

// SanitizeHTTPRaw masks credentials in one raw HTTP request or response while
// preserving the wire shape (start line, headers, blank line, body, line
// endings). It never rewrites structure, so the result is still a valid raw
// HTTP capture for a report reader.
func SanitizeHTTPRaw(raw string) string {
	if raw == "" {
		return raw
	}
	head, body, sep := splitHTTPRaw(raw)
	lines := splitKeepEnds(head)
	for i, line := range lines {
		content := strings.TrimRight(line, "\r\n")
		ending := line[len(content):]
		if i == 0 {
			lines[i] = sanitizeStartLine(content) + ending
			continue
		}
		name, value, ok := splitHeader(content)
		if !ok {
			lines[i] = SanitizeText(content) + ending
			continue
		}
		if _, sensitive := sensitiveHeaderNames[strings.ToLower(strings.TrimSpace(name))]; sensitive {
			lines[i] = name + ":" + valuePrefix(value) + Redacted + ending
			continue
		}
		lines[i] = name + ":" + SanitizeText(value) + ending
	}
	return strings.Join(lines, "") + sep + SanitizeText(body)
}

// SanitizeText masks credentials and PII in free text (bodies, PoC scripts,
// evidence prose).
func SanitizeText(s string) string {
	if s == "" {
		return s
	}
	s = bearerRe.ReplaceAllStringFunc(s, func(m string) string {
		parts := strings.SplitN(m, " ", 2)
		return parts[0] + " " + Redacted
	})
	s = jsonSensitiveRe.ReplaceAllString(s, `${1}"`+Redacted+`"`)
	s = formSensitiveRe.ReplaceAllString(s, `${1}=`+Redacted)
	s = emailRe.ReplaceAllString(s, Redacted)
	s = phoneRe.ReplaceAllString(s, Redacted)
	s = idCardRe.ReplaceAllString(s, Redacted)
	return s
}

// splitHTTPRaw splits a raw message into head, separator and body. It accepts
// CRLF and bare-LF captures.
func splitHTTPRaw(raw string) (head, body, sep string) {
	if i := strings.Index(raw, "\r\n\r\n"); i >= 0 {
		return raw[:i], raw[i+4:], "\r\n\r\n"
	}
	if i := strings.Index(raw, "\n\n"); i >= 0 {
		return raw[:i], raw[i+2:], "\n\n"
	}
	return raw, "", ""
}

// splitKeepEnds splits text into lines while keeping each line's terminator.
func splitKeepEnds(s string) []string {
	var out []string
	for len(s) > 0 {
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:idx+1])
		s = s[idx+1:]
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// splitHeader splits "Name: value" preserving the raw name and the bytes after
// the colon (usually a single space).
func splitHeader(line string) (name, value string, ok bool) {
	idx := strings.IndexByte(line, ':')
	if idx <= 0 {
		return "", "", false
	}
	return line[:idx], line[idx+1:], true
}

// valuePrefix preserves the whitespace between the colon and the value.
func valuePrefix(value string) string {
	trimmed := strings.TrimLeft(value, " \t")
	return value[:len(value)-len(trimmed)]
}

// sanitizeStartLine masks sensitive query parameters in a request line.
func sanitizeStartLine(line string) string {
	return SanitizeText(line)
}

// SanitizeFinding returns a copy with every free-text field passed through
// SanitizeText. The web layer already sanitizes at assembly time; renderers
// re-run it so a direct caller (CLI, test, future exporter) cannot bypass the
// redaction boundary.
func SanitizeFinding(f Finding) Finding {
	f.Description = SanitizeText(f.Description)
	f.Impact = SanitizeText(f.Impact)
	f.TechnicalAnalysis = SanitizeText(f.TechnicalAnalysis)
	f.PoCDescription = SanitizeText(f.PoCDescription)
	f.PoCScript = SanitizeText(f.PoCScript)
	f.ExploitationProof = SanitizeText(f.ExploitationProof)
	f.Remediation = SanitizeText(f.Remediation)
	f.Fix = SanitizeText(f.Fix)
	return f
}
