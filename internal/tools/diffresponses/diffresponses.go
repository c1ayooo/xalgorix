// Package diffresponses provides a deterministic structural diff between two
// HTTP responses (or JSON bodies) captured to files. It exists because
// "compare the two responses and decide whether the difference is an
// authorization leak" is the core evidence step of the methodology skills, and
// eyeballing two large bodies is exactly where models hallucinate.
//
// The tool reports WHERE the responses differ, with noise (timestamps, trace
// ids, random tokens) filtered out. It never claims the difference is a
// vulnerability: the caller reasons about the meaning of a changed field.
package diffresponses

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/sandbox"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

const maxFileBytes = 4 << 20

// Register adds diff_responses to the registry.
func Register(r *tools.Registry) {
	r.Register(&tools.Tool{
		Name: "diff_responses",
		Description: "Structurally diff two captured HTTP responses (or two JSON bodies) stored as files in the " +
			"scan workspace. Reports status-line/header differences and the exact JSON paths added, removed or " +
			"changed, filtering timestamps, trace/request ids and random tokens. Use this for authorization and " +
			"information-leak comparisons (own data vs other user's data, authenticated vs anonymous). " +
			"A difference is not a vulnerability by itself — read what changed.",
		Parameters: []tools.Parameter{
			{Name: "file_a", Description: "baseline response file (workspace path)", Required: true},
			{Name: "file_b", Description: "comparison response file (workspace path)", Required: true},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			return diff(r.GetScanContextID(), args)
		},
	})
}

// noiseKeyRe matches object keys whose values legitimately vary between two
// requests and therefore carry no evidentiary weight.
var noiseKeyRe = regexp.MustCompile(`(?i)^(x-)?(timestamp|time|date|ts|trace|trace_?id|request_?id|req_?id|nonce|random|rand|span_?id|correlation_?id|server-timing|etag|last-modified|set-cookie|date)$`)

// noiseValueRe matches values that are plausibly generated per request.
var noiseValueRe = regexp.MustCompile(`(?i)^(\d{10,13}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9a-f]{16,}|[A-Za-z0-9+/=_-]{32,})$`)

type httpMessage struct {
	StatusLine string
	Headers    map[string]string
	Body       string
}

type change struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"` // added | removed | changed
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

func diff(contextID string, args map[string]string) (tools.Result, error) {
	sc := scanctx.Get(contextID)
	if sc == nil {
		return tools.Result{Error: "no active scan context"}, nil
	}
	aPath, err := readPath(sc, args["file_a"])
	if err != nil {
		return tools.Result{Error: "file_a: " + err.Error()}, nil
	}
	bPath, err := readPath(sc, args["file_b"])
	if err != nil {
		return tools.Result{Error: "file_b: " + err.Error()}, nil
	}
	a, err := os.ReadFile(aPath)
	if err != nil {
		return tools.Result{Error: "file_a: " + err.Error()}, nil
	}
	b, err := os.ReadFile(bPath)
	if err != nil {
		return tools.Result{Error: "file_b: " + err.Error()}, nil
	}
	if len(a) > maxFileBytes || len(b) > maxFileBytes {
		return tools.Result{Error: fmt.Sprintf("one input exceeds the %d-byte limit", maxFileBytes)}, nil
	}

	msgA := parseMessage(string(a))
	msgB := parseMessage(string(b))
	var out strings.Builder
	fmt.Fprintf(&out, "baseline: %s\ncomparison: %s\n", aPath, bPath)

	if msgA.StatusLine != "" || msgB.StatusLine != "" {
		fmt.Fprintf(&out, "status: %q -> %q%s\n", msgA.StatusLine, msgB.StatusLine, sameMarker(msgA.StatusLine == msgB.StatusLine))
	}
	headerChanges := diffHeaders(msgA.Headers, msgB.Headers)
	if len(headerChanges) > 0 {
		out.WriteString("header differences:\n")
		for _, c := range headerChanges {
			fmt.Fprintf(&out, "  %s %s: %q -> %q\n", c.Kind, c.Path, c.Before, c.After)
		}
	}

	changes, structured := diffBodies(msgA.Body, msgB.Body)
	if structured {
		if len(changes) == 0 {
			out.WriteString("body: structurally identical (noise filtered)\n")
		} else {
			fmt.Fprintf(&out, "body differences (%d):\n", len(changes))
			for _, c := range changes {
				switch c.Kind {
				case "added":
					fmt.Fprintf(&out, "  + %s = %s\n", c.Path, c.After)
				case "removed":
					fmt.Fprintf(&out, "  - %s (was %s)\n", c.Path, c.Before)
				default:
					fmt.Fprintf(&out, "  ~ %s: %s -> %s\n", c.Path, c.Before, c.After)
				}
			}
		}
	} else {
		fmt.Fprintf(&out, "body: not JSON on both sides (lengths %d -> %d)%s\n",
			len(msgA.Body), len(msgB.Body), sameMarker(msgA.Body == msgB.Body))
		out.WriteString("body diff hint: use python_action or terminal_execute with `diff -u` for a textual diff\n")
	}

	out.WriteString("\nreminder: a difference is not a vulnerability. Decide whether the changed field is data the comparison request should not have been allowed to read, and keep both raw responses as evidence.\n")

	if len(changes) > 60 {
		changes = changes[:60]
	}
	return tools.Result{
		Output: out.String(),
		Metadata: map[string]any{
			"body_changes": len(changes),
			"structured":   structured,
		},
	}, nil
}

func sameMarker(equal bool) string {
	if equal {
		return " (identical)"
	}
	return ""
}

func readPath(sc *scanctx.ScanContext, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("path is required")
	}
	path := raw
	if !filepath.IsAbs(path) {
		path = filepath.Join(sc.ScanDir, path)
	}
	return sandbox.Default().CheckRead(sc, "diff_responses", path)
}

// parseMessage splits an optionally raw HTTP message into status line, headers
// and body. Plain bodies (JSON, text) yield an empty status line.
func parseMessage(raw string) httpMessage {
	msg := httpMessage{Headers: map[string]string{}, Body: raw}
	head, body, ok := strings.Cut(raw, "\n\n")
	if !ok {
		head, body, ok = strings.Cut(raw, "\r\n\r\n")
		if !ok {
			return msg
		}
	}
	lines := strings.Split(strings.ReplaceAll(head, "\r\n", "\n"), "\n")
	first := strings.TrimSpace(lines[0])
	if !looksLikeStartLine(first) {
		return msg
	}
	msg.StatusLine = first
	msg.Body = body
	for _, line := range lines[1:] {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		msg.Headers[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
	}
	return msg
}

func looksLikeStartLine(line string) bool {
	if strings.HasPrefix(line, "HTTP/") {
		return true
	}
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return false
	}
	switch fields[0] {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "TRACE", "CONNECT":
		return strings.HasPrefix(fields[2], "HTTP/")
	}
	return false
}

func diffHeaders(a, b map[string]string) []change {
	var out []change
	for name, av := range a {
		if noiseKeyRe.MatchString(name) {
			continue
		}
		bv, ok := b[name]
		if !ok {
			out = append(out, change{Path: name, Kind: "removed", Before: av})
			continue
		}
		if av != bv && !noiseValueRe.MatchString(bv) {
			out = append(out, change{Path: name, Kind: "changed", Before: av, After: bv})
		}
	}
	for name, bv := range b {
		if noiseKeyRe.MatchString(name) {
			continue
		}
		if _, ok := a[name]; !ok {
			out = append(out, change{Path: name, Kind: "added", After: bv})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// diffBodies compares two JSON documents structurally. When either side is not
// JSON it reports structured=false and the caller falls back to a textual diff.
func diffBodies(a, b string) ([]change, bool) {
	var va, vb any
	if err := json.Unmarshal([]byte(a), &va); err != nil {
		return nil, false
	}
	if err := json.Unmarshal([]byte(b), &vb); err != nil {
		return nil, false
	}
	var out []change
	walk("$", va, vb, &out)
	return out, true
}

func walk(path string, a, b any, out *[]change) {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, change{Path: path, Kind: "changed", Before: summarize(a), After: summarize(b)})
			return
		}
		keys := make([]string, 0, len(av)+len(bv))
		seen := map[string]bool{}
		for k := range av {
			keys = append(keys, k)
			seen[k] = true
		}
		for k := range bv {
			if !seen[k] {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			if noiseKeyRe.MatchString(k) {
				continue
			}
			child := path + "." + k
			avv, aok := av[k]
			bvv, bok := bv[k]
			switch {
			case aok && !bok:
				*out = append(*out, change{Path: child, Kind: "removed", Before: summarize(avv)})
			case !aok && bok:
				*out = append(*out, change{Path: child, Kind: "added", After: summarize(bvv)})
			default:
				walk(child, avv, bvv, out)
			}
		}
	case []any:
		bv, ok := b.([]any)
		if !ok {
			*out = append(*out, change{Path: path, Kind: "changed", Before: summarize(a), After: summarize(b)})
			return
		}
		if len(av) != len(bv) {
			*out = append(*out, change{Path: path, Kind: "changed", Before: fmt.Sprintf("len %d", len(av)), After: fmt.Sprintf("len %d", len(bv))})
			return
		}
		for i := range av {
			walk(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i], out)
		}
	case string:
		bv, ok := b.(string)
		if !ok || av == bv || noiseValueRe.MatchString(av) || noiseValueRe.MatchString(bv) {
			return
		}
		*out = append(*out, change{Path: path, Kind: "changed", Before: quote(av), After: quote(bv)})
	default:
		if fmt.Sprintf("%v", a) != fmt.Sprintf("%v", b) {
			*out = append(*out, change{Path: path, Kind: "changed", Before: summarize(a), After: summarize(b)})
		}
	}
}

func summarize(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return quote(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case map[string]any:
		return fmt.Sprintf("{%d keys}", len(t))
	case []any:
		return fmt.Sprintf("[%d items]", len(t))
	default:
		return fmt.Sprintf("%v", t)
	}
}

func quote(s string) string {
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return strconv.Quote(s)
}
