// Package proxy provides Caido proxy integration tools.
package proxy

import (
	"bytes"
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

	"github.com/xalgord/xalgorix/v4/internal/config"
	"github.com/xalgord/xalgorix/v4/internal/proxy"
	"github.com/xalgord/xalgorix/v4/internal/ratelimit"
	"github.com/xalgord/xalgorix/v4/internal/tools"
	"github.com/xalgord/xalgorix/v4/internal/trafficproxy"
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
		Description: "List HTTP requests captured by Caido proxy.",
		Parameters: []tools.Parameter{
			{Name: "count", Description: "Number of requests to list (default: 20)", Required: false},
			{Name: "filter", Description: "Filter by URL substring", Required: false},
		},
		Execute: listRequests,
	})
}

// httpClient returns the configured client. Proxy-required mode never falls
// back to a direct connection when initialization failed. dialGuard, when
// non-nil, checks every connection of the direct client (see
// tools.GuardDials); the proxy pool's shared client is left as it is, since
// the proxy resolves and connects to the destination.
func httpClient(scanCtxID string, dialGuard tools.DialGuard) (*http.Client, error) {
	cfg := config.Get()
	if cfg.ProxyRequired && !proxy.Required() {
		return nil, fmt.Errorf("proxy-required mode is not initialized")
	}
	// R2b default posture: with TLS interception active, send_request routes
	// through the shared loopback traffic proxy (also in proxy-required
	// mode) so the MITM owns upstream selection and observation. The
	// fail-closed required checks above stay byte-identical.
	if trafficproxy.Enabled() && trafficproxy.MITMEnabled() {
		return trafficProxyClient(cfg, scanCtxID, dialGuard)
	}
	// A settings edit cannot disable the live required route before restart.
	if proxy.Required() {
		return proxy.GetClient()
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

func listRequests(args map[string]string) (tools.Result, error) {
	cfg := config.Get()
	if cfg.CaidoAPIToken == "" {
		return tools.Result{Output: "Caido API token not configured. Set CAIDO_API_TOKEN in ~/.xalgorix.env"}, nil
	}

	count := clampRequestCount(args["count"])

	query := `query { requests(first: ` + strconv.Itoa(count) + `) { edges { node { id method url response { statusCode } } } } }`

	gqlReq := map[string]any{"query": query}
	body, err := json.Marshal(gqlReq)
	if err != nil {
		return tools.Result{Error: fmt.Sprintf("Failed to marshal GraphQL query: %v", err)}, nil
	}

	gqlURL := fmt.Sprintf("http://127.0.0.1:%d/graphql", caidoPort())
	req, err := http.NewRequest(http.MethodPost, gqlURL, bytes.NewReader(body))
	if err != nil {
		return tools.Result{Error: fmt.Sprintf("Failed to create GraphQL request: %v", err)}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.CaidoAPIToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return tools.Result{Output: fmt.Sprintf("Failed to query Caido GraphQL API at %s: %v\nMake sure Caido is running and accessible.", gqlURL, err)}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return tools.Result{Error: fmt.Sprintf("Failed to read Caido response: %v", err)}, nil
	}
	return tools.Result{Output: string(respBody)}, nil
}

// caidoPort returns the configured or auto-detected Caido port.
func caidoPort() int {
	if p := config.Get().CaidoPort; p > 0 {
		return p
	}
	return 8080
}
