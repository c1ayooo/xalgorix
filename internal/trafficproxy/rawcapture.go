package trafficproxy

// Raw wire-packet capture (2026-11-22 operator decision amending R2b decision
// 8): attributed exchanges persist their raw request/response packets —
// header VALUES and bodies, unredacted — into the per-scan traffic archive
// (scanctx.TrafficArchive) so findings can display the exact wire packets.
//
// Fidelity and bounds:
//   - the request is rendered in origin form from the forwarded request
//     (after the hop-by-hop strip): request line, Host, headers, body —
//     this is the packet the target actually received;
//   - the response is rendered from the decoded representation Go's HTTP
//     client hands back (status line, headers, body). Chunked-transfer
//     framing is not reproduced — the body is the reassembled body;
//   - bodies are bounded by rawCaptureLimit per side; the remainder streams
//     through untouched and the exchange is flagged truncated;
//   - upgrade passthrough (raw byte relay) and blind CONNECT have no
//     plaintext split to capture — documented boundary, unchanged.
//
// Capture is attribution-gated: system-level traffic (no run token) has no
// scan directory to archive into and is never captured.

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// channelName renders the attribution channel for the archive record.
func channelName(c Channel) string {
	switch c {
	case ChannelBearer:
		return "bearer"
	case ChannelSubprocess:
		return "subprocess"
	default:
		return ""
	}
}

// renderRequestRaw renders the forwarded request in origin-form wire format.
// Headers render in sorted order — http.Header is a map and the rendering is
// a capture artifact, not a byte-for-byte transcript of header order.
func renderRequestRaw(req *http.Request, body []byte) string {
	var b strings.Builder
	uri := "/"
	if req.URL != nil {
		uri = req.URL.RequestURI()
	}
	fmt.Fprintf(&b, "%s %s HTTP/1.1\r\n", req.Method, uri)
	if req.Host != "" {
		fmt.Fprintf(&b, "Host: %s\r\n", req.Host)
	}
	writeHeadersRaw(&b, req.Header)
	b.WriteString("\r\n")
	b.Write(body)
	return b.String()
}

// renderResponseRaw renders the upstream response (decoded representation).
func renderResponseRaw(resp *http.Response, body []byte) string {
	var b strings.Builder
	status := strings.TrimSpace(resp.Status)
	if status == "" {
		status = fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	fmt.Fprintf(&b, "HTTP/1.1 %s\r\n", status)
	writeHeadersRaw(&b, resp.Header)
	b.WriteString("\r\n")
	b.Write(body)
	return b.String()
}

// writeHeadersRaw renders every header with its values, names sorted.
func writeHeadersRaw(b *strings.Builder, h http.Header) {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, value := range h[name] {
			fmt.Fprintf(b, "%s: %s\r\n", name, value)
		}
	}
}

// boundedCapture accumulates the first rawCaptureLimit bytes of a streaming
// body while letting every byte flow through to the client untouched. Its
// Write always reports success: capture is best-effort and must never stall
// or error the relay.
type boundedCapture struct {
	buf  []byte
	full bool
}

func newBoundedCapture(capacity int) *boundedCapture {
	return &boundedCapture{buf: make([]byte, 0, capacity)}
}

func (c *boundedCapture) Write(p []byte) (int, error) {
	if !c.full {
		room := cap(c.buf) - len(c.buf)
		if len(p) <= room {
			c.buf = append(c.buf, p...)
		} else {
			c.buf = append(c.buf, p[:room]...)
			c.full = true
		}
	}
	return len(p), nil
}

func (c *boundedCapture) bytes() []byte   { return c.buf }
func (c *boundedCapture) truncated() bool { return c.full }

var _ io.Writer = (*boundedCapture)(nil)
