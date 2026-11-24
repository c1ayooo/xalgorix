// Package proxy provides Caido proxy integration tools.
package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/proxy"
	"github.com/c1ayooo/xalgorix/v4/internal/ratelimit"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
	"github.com/c1ayooo/xalgorix/v4/internal/trafficproxy"
)

// globalLimiter is initialized once from config when the package is loaded.
// It is replaced by initLimiter() so tests can override it cleanly.
var globalLimiter *ratelimit.Limiter

func init() {
	initLimiter()
}

func initLimiter() {
	cfg := config.Get()
	rps := cfg.RateLimitRPS
	burst := cfg.RateLimitBurst
	if rps <= 0 {
		rps = 10
	}
	if burst <= 0 {
		burst = 20
	}
	globalLimiter = ratelimit.New(rps, burst)
}

// Register adds proxy tools to the registry.
func Register(r *tools.Registry) {
	r.Register(&tools.Tool{
		Name:        "send_request",
		Description: "Send an HTTP request using the configured outbound proxy when enabled. Proxy-required mode never falls back to a direct request. Responses are capped at 32MB; large responses (>10KB) are saved to a unique temporary file; use terminal_execute to inspect it.",
		Parameters: []tools.Parameter{
			{Name: "method", Description: "HTTP method (GET, POST, PUT, DELETE, etc.)", Required: true},
			{Name: "url", Description: "Target URL", Required: true},
			{Name: "headers", Description: "Request headers as JSON object", Required: false},
			{Name: "body", Description: "Request body", Required: false},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			return sendRequestGuarded(r.GetScanContextID(), r.RequestGuard(), r.DialGuard(), args)
		},
		ExecuteContext: func(ctx context.Context, args map[string]string) (tools.Result, error) {
			return sendRequestGuardedCtx(ctx, r.GetScanContextID(), r.RequestGuard(), r.DialGuard(), args)
		},
	})

	r.Register(&tools.Tool{
		Name:        "list_requests",
		Description: "List the HTTP exchanges captured by this run's MITM traffic proxy (the same raw archive the dashboard shows). Returns the newest `count` rows matching an optional URL substring / method. Pass seq=<n> to read one exchange's full raw request and response — use it to quote exact wire evidence in exploitation_proof.",
		Parameters: []tools.Parameter{
			{Name: "count", Description: "Number of exchanges to list (default: 20, max: 100)", Required: false},
			{Name: "filter", Description: "Case-insensitive URL substring", Required: false},
			{Name: "method", Description: "Exact HTTP method filter (e.g. POST)", Required: false},
			{Name: "seq", Description: "Return the full raw request/response of one archived exchange (the #n id from the list)", Required: false},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			return listRequests(r.GetScanContextID(), args)
		},
	})
}

// httpClient returns the configured client. Proxy-required mode never falls
// back to a direct connection when initialization failed. dialGuard, when
// non-nil, checks every connection of the direct client (see
// tools.GuardDials); the proxy pool's shared client is left as it is, since
// the proxy resolves and connects to the destination.
func httpClient(scanCtxID string, dialGuard tools.DialGuard) (*http.Client, error) {
	if rt, ok := tools.ContainerHTTPTransport(scanCtxID); ok {
		return &http.Client{Transport: rt, Timeout: 60 * time.Second}, nil
	}
	cfg := config.Get()
	if cfg.ProxyRequired && !proxy.Required() {
		return nil, fmt.Errorf("proxy-required mode is not initialized")
	}
	// R2b default posture: with TLS interception active, send_request routes
	// through the shared loopback traffic proxy (also in proxy-required
	// mode) so the MITM owns upstream selection and observation. The
	// fail-closed required checks above stay byte-identical.
	if trafficproxy.Enabled() {
		return trafficProxyClient(cfg, scanCtxID, dialGuard)
	}
	// Egress pool (not the MITM): honor this task's pin when it has one.
	if p, direct, ok := proxy.UpstreamFor(scanCtxID); !direct && ok && p != nil {
		client := proxy.NewDirectClient(cfg.TLSSkipVerify)
		tr, ok := client.Transport.(*http.Transport)
		if !ok {
			return nil, fmt.Errorf("egress client transport cannot be configured")
		}
		pinned, err := p.URL()
		if err != nil {
			return nil, fmt.Errorf("invalid configured proxy: %w", err)
		}
		tr.Proxy = http.ProxyURL(pinned)
		// No run-token attribution here: a remote egress proxy must never see
		// the token that only the local MITM consumes.
		if err := tools.GuardDials(tr, dialGuard); err != nil {
			return nil, err
		}
		return client, nil
	}
	// A settings edit cannot disable the live required route before restart.
	if proxy.Required() {
		if viaProxyPool(cfg) {
			return proxy.GetClient()
		}
		return nil, fmt.Errorf("proxy-required mode has no upstream")
	}
	if viaProxyPool(cfg) {
		return proxy.GetClient()
	}
	// Direct. This includes UseProxy set while the proxy manager is off (no
	// proxy configured, or the setting was changed without a restart): the
	// manager would hand back its own unguarded direct client.
	client := proxy.NewDirectClient(cfg.TLSSkipVerify)
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		if dialGuard != nil {
			return nil, fmt.Errorf("direct client transport cannot be guarded")
		}
		return client, nil
	}
	// NewDirectClient's transport is private to this call.
	if err := tools.GuardDials(tr, dialGuard); err != nil {
		return nil, err
	}
	// Run-token attribution (a no-op unless TLS interception is active).
	client.Transport = trafficproxy.AttributionRoundTripper(tr, scanCtxID)
	return client, nil
}

// trafficProxyClient builds the loopback-proxy client: LocalURL routing plus
// trust for the traffic-proxy CA plus the run-token attribution header. The
// MITM applies the operator-machine dial guard to its own upstream dials;
// GuardDials on this transport only ever sees the loopback proxy address,
// which it exempts like any operator proxy.
func trafficProxyClient(cfg *config.Config, scanCtxID string, dialGuard tools.DialGuard) (*http.Client, error) {
	localURL, err := trafficproxy.LocalURL()
	if err != nil {
		return nil, fmt.Errorf("traffic proxy unavailable: %w", err)
	}
	localParsed, err := url.Parse(localURL)
	if err != nil {
		return nil, fmt.Errorf("invalid traffic proxy URL: %w", err)
	}
	client := proxy.NewDirectClient(cfg.TLSSkipVerify)
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("traffic proxy client transport cannot be configured")
	}
	tr.Proxy = http.ProxyURL(localParsed)
	if caPool := trafficproxy.RootCAs(); caPool != nil && !cfg.TLSSkipVerify {
		if tr.TLSClientConfig == nil {
			tr.TLSClientConfig = &tls.Config{} //nolint:gosec
		} else {
			tr.TLSClientConfig = tr.TLSClientConfig.Clone()
		}
		tr.TLSClientConfig.RootCAs = caPool
	}
	if err := tools.GuardDials(tr, dialGuard); err != nil {
		return nil, err
	}
	// Run-token attribution rides Proxy-Authorization, stripped by the proxy
	// before the upstream hop; never stamped toward a remote upstream.
	client.Transport = trafficproxy.AttributionRoundTripper(tr, scanCtxID)
	return client, nil
}

// viaProxyPool reports whether send_request routes through the proxy pool.
func viaProxyPool(cfg *config.Config) bool {
	return proxy.Required() || (cfg.UseProxy && proxy.Enabled())
}

func parseHeaders(headersJSON string) (map[string]string, error) {
	if strings.TrimSpace(headersJSON) == "" {
		return nil, nil
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(headersJSON), &headers); err != nil {
		return nil, fmt.Errorf("invalid headers JSON: %w", err)
	}
	return headers, nil
}

func clampRequestCount(raw string) int {
	count := 20
	if strings.TrimSpace(raw) != "" {
		if _, err := fmt.Sscanf(raw, "%d", &count); err != nil {
			count = 20
		}
	}
	if count < 1 {
		return 1
	}
	if count > 100 {
		return 100
	}
	return count
}

func sendRequest(args map[string]string) (tools.Result, error) {
	return sendRequestGuarded("", nil, nil, args)
}

// sendRequestGuarded runs one send_request call. guard, when non-nil, is
// applied to every redirect hop before it is followed; dialGuard, when
// non-nil, to every direct connection.
func sendRequestGuarded(scanCtxID string, guard tools.RequestGuard, dialGuard tools.DialGuard, args map[string]string) (tools.Result, error) {
	return sendRequestGuardedCtx(context.Background(), scanCtxID, guard, dialGuard, args)
}

func sendRequestGuardedCtx(parent context.Context, scanCtxID string, guard tools.RequestGuard, dialGuard tools.DialGuard, args map[string]string) (tools.Result, error) {
	targetURL := args["url"]
	method := strings.ToUpper(args["method"])

	// R1: honor the target's Retry-After before any send.
	if sc := scanctx.Get(scanCtxID); sc != nil {
		if msg := sc.TargetBackoffMessage(); msg != "" {
			return tools.Result{Output: msg}, nil
		}
	}
	// R4: shared Engagement budget.
	var ticket *scanctx.Ticket
	status, retryAfter := 0, time.Duration(0)
	if sc := scanctx.Get(scanCtxID); sc != nil {
		if budget := sc.Egress(); budget != nil {
			base := parent
			if base == nil {
				base = context.Background()
			}
			acqCtx, cancel := context.WithTimeout(base, 30*time.Second)
			t, aerr := budget.Acquire(acqCtx, targetURL)
			cancel()
			if aerr != nil {
				return tools.Result{Output: scanctx.EgressSoftMessage(aerr)}, nil
			}
			ticket = t
			defer func() { ticket.Done(status, retryAfter) }()
		}
	}

	// Rate-limit before sending — blocks until a token is available for
	// this domain. No-op when XALGORIX_RATE_RPS=0.
	globalLimiter.Wait(targetURL)

	var bodyReader io.Reader
	if body := args["body"]; body != "" {
		bodyReader = strings.NewReader(body)
	}

	reqCtx := parent
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	req, err := http.NewRequestWithContext(reqCtx, method, targetURL, bodyReader)
	if err != nil {
		return tools.Result{}, fmt.Errorf("invalid request: %w", err)
	}
	// send_request historically allowed 32MB; carry that bound into the
	// container-side worker.
	req = req.WithContext(tools.WithHTTPWorkerBodyLimit(req.Context(), 32<<20))

	if headersJSON := args["headers"]; headersJSON != "" {
		headers, err := parseHeaders(headersJSON)
		if err != nil {
			return tools.Result{}, err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
	}

	shared, err := httpClient(scanCtxID, dialGuard)
	if err != nil {
		return tools.Result{}, err
	}
	// The proxy pool's client is shared; guard a per-request copy. Only a
	// client on the default (follow) redirect policy needs the hop check.
	client := *shared
	refusedHop := func() string { return "" }
	if client.CheckRedirect == nil {
		refusedHop = tools.GuardRedirects(&client, guard)
	}
	resp, err := client.Do(req)
	if err != nil {
		return tools.Result{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	retryAfter = scanctx.ParseRetryAfter(resp.Header.Get("Retry-After"))

	// Bound the in-memory read so a hostile/huge response cannot exhaust the
	// worker. The preview file still gets the full (bounded) body.
	const maxResponseBytes = 32 << 20
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return tools.Result{}, fmt.Errorf("failed to read response body: %w", err)
	}
	responseTruncated := len(respBody) > maxResponseBytes
	if responseTruncated {
		respBody = respBody[:maxResponseBytes]
	}

	var b strings.Builder
	if note := refusedHop(); note != "" {
		b.WriteString(note + "\n\n")
	}
	viaProxy := viaProxyPool(config.Get())
	if viaProxy {
		b.WriteString("[via proxy pool]\n")
	} else {
		b.WriteString("[direct request]\n")
	}
	b.WriteString(fmt.Sprintf("HTTP/%s %s\n", resp.Proto, resp.Status))
	for k, vs := range resp.Header {
		for _, v := range vs {
			b.WriteString(fmt.Sprintf("%s: %s\n", k, v))
		}
	}
	b.WriteString("\n")

	// For large responses, save the full body to a temp file so the agent
	// can grep/search it with terminal_execute instead of losing data.
	const previewLimit = 10000
	bodyStr := string(respBody)
	var savedPath string
	if len(bodyStr) > previewLimit {
		// Unique file per response: concurrent requests to the same URL must
		// not overwrite each other, and a predictable path is symlink-prone.
		f, createErr := os.CreateTemp("", "send_request_*.txt")
		if createErr == nil {
			if _, writeErr := f.Write(respBody); writeErr == nil {
				savedPath = f.Name()
			}
			_ = f.Close()
			if savedPath == "" {
				_ = os.Remove(f.Name())
			}
		}
	}

	if savedPath != "" {
		b.WriteString(bodyStr[:previewLimit])
		b.WriteString(fmt.Sprintf("\n\n... [RESPONSE TOO LARGE: %d bytes shown; response truncated=%v]\n", len(bodyStr), responseTruncated))
		b.WriteString(fmt.Sprintf("💾 Full response saved to: %s\n", savedPath))
		b.WriteString("Use terminal_execute to search it: grep -oE 'pattern' " + savedPath + "\n")
	} else {
		b.WriteString(bodyStr)
	}

	meta := map[string]any{
		"status_code": resp.StatusCode,
		"url":         targetURL,
		"via_proxy":   viaProxy,
	}
	if savedPath != "" {
		meta["saved_to"] = savedPath
		meta["full_size"] = len(respBody)
	}

	return tools.Result{
		Output:   b.String(),
		Metadata: meta,
	}, nil
}

// listRequests reads the run's MITM traffic archive — the exact raw packets
// the dashboard renders — so the agent can consult what it actually sent and
// quote wire evidence instead of recalling it from context. Without seq it
// lists the newest exchanges; with seq it returns that exchange's full raw
// request/response, bounded so one tool result cannot blow the context.
func listRequests(scanCtxID string, args map[string]string) (tools.Result, error) {
	sc := scanctx.Get(scanCtxID)
	if sc == nil || strings.TrimSpace(sc.ScanDir) == "" {
		return tools.Result{Output: "No live scan context: the traffic archive is per run and only readable while that run is active."}, nil
	}

	if rawSeq := strings.TrimSpace(args["seq"]); rawSeq != "" {
		seq, err := strconv.ParseInt(rawSeq, 10, 64)
		if err != nil || seq <= 0 {
			return tools.Result{Error: fmt.Sprintf("invalid seq %q (expected the #n id from the listing)", rawSeq)}, nil
		}
		ex, err := scanctx.ReadTrafficExchangeAt(sc.ScanDir, seq)
		if err != nil {
			return tools.Result{Error: fmt.Sprintf("read archived exchange #%d: %v", seq, err)}, nil
		}
		if ex == nil {
			return tools.Result{Output: fmt.Sprintf("No archived exchange #%d in this session. Call list_requests without seq to see the available ids.", seq)}, nil
		}
		return tools.Result{
			Output:   renderTrafficExchange(*ex),
			Metadata: map[string]any{"traffic_seq": ex.Seq, "status": ex.Status, "method": ex.Method, "url": ex.URL},
		}, nil
	}

	all, err := scanctx.ReadTrafficExchanges(sc.ScanDir)
	if err != nil {
		return tools.Result{Error: fmt.Sprintf("read traffic archive: %v", err)}, nil
	}
	if len(all) == 0 {
		return tools.Result{Output: "This run has no captured HTTP exchanges yet. Target traffic must go through the traffic proxy (http_request/send_request, curl/wget, Python, or the browser)."}, nil
	}

	filter := strings.ToLower(strings.TrimSpace(args["filter"]))
	method := strings.ToUpper(strings.TrimSpace(args["method"]))
	matched := make([]scanctx.TrafficExchange, 0, len(all))
	for _, ex := range all {
		if method != "" && strings.ToUpper(ex.Method) != method {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(ex.URL), filter) {
			continue
		}
		matched = append(matched, ex)
	}
	count := clampRequestCount(args["count"])
	if len(matched) > count {
		matched = matched[len(matched)-count:] // newest window, ascending
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d captured HTTP exchanges in this run's traffic proxy archive", len(all))
	if len(matched) != len(all) {
		fmt.Fprintf(&b, " (%d match)", len(matched))
	}
	b.WriteString(". Use list_requests seq=<n> for the full raw request/response.\n")
	for _, ex := range matched {
		status := "failed"
		if ex.Status != 0 {
			status = strconv.Itoa(ex.Status)
		}
		b.WriteString("#")
		b.WriteString(strconv.FormatInt(ex.Seq, 10))
		b.WriteByte(' ')
		b.WriteString(ex.Method)
		b.WriteByte(' ')
		b.WriteString(ex.URL)
		b.WriteString(" -> ")
		b.WriteString(status)
		if ex.Channel != "" {
			b.WriteString(" [")
			b.WriteString(ex.Channel)
			b.WriteByte(']')
		}
		if ex.HighRisk != "" {
			b.WriteString(" high-risk: ")
			b.WriteString(ex.HighRisk)
		}
		if ex.Denied {
			b.WriteString(" DENIED")
		}
		b.WriteByte('\n')
	}
	return tools.Result{Output: b.String(), Metadata: map[string]any{
		"total":   len(all),
		"matched": len(matched),
	}}, nil
}

// listRequestsRawLimit bounds one raw exchange dump (request + response +
// framing) so a single tool result cannot swallow the context window.
const listRequestsRawLimit = 192 << 10

// renderTrafficExchange formats one archived exchange as wire text. Bodies are
// already capped by the proxy (128 KiB per side); the combined rendering is
// capped again here.
func renderTrafficExchange(ex scanctx.TrafficExchange) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s %s -> %d", ex.Seq, ex.Method, ex.URL, ex.Status)
	if ex.Channel != "" {
		fmt.Fprintf(&b, " [%s]", ex.Channel)
	}
	if ex.Upstream != "" {
		fmt.Fprintf(&b, " via %s", ex.Upstream)
	}
	if ex.HighRisk != "" {
		fmt.Fprintf(&b, " high-risk: %s", ex.HighRisk)
	}
	if ex.Denied {
		b.WriteString(" DENIED (upstream never contacted)")
	}
	b.WriteString("\n")
	if ex.RequestRaw != "" {
		b.WriteString("--- request ---\n")
		b.WriteString(ex.RequestRaw)
		if !strings.HasSuffix(ex.RequestRaw, "\n") {
			b.WriteByte('\n')
		}
		if ex.ReqTruncated {
			b.WriteString("… (request truncated at the capture limit)\n")
		}
	}
	if ex.ResponseRaw != "" {
		b.WriteString("--- response ---\n")
		b.WriteString(ex.ResponseRaw)
		if !strings.HasSuffix(ex.ResponseRaw, "\n") {
			b.WriteByte('\n')
		}
		if ex.RespTruncated {
			b.WriteString("… (response truncated at the capture limit)\n")
		}
	}
	out := b.String()
	if len(out) > listRequestsRawLimit {
		out = out[:listRequestsRawLimit] + "\n… (tool output truncated; the full packet is in the traffic archive)\n"
	}
	return out
}
