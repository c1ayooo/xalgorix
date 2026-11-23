package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/trafficproxy"
)

// HTTPWorkerSocketPath is the per-scan unix socket served by the in-container
// httpworker. The socket's parent directory is bind-mounted at the same
// absolute path on both sides, so the platform can dial it directly.
//
// The path must stay under the kernel's ~104-byte sun_path limit: a scan
// directory (data dir + target + date + scan id) is routinely ~140 bytes, and
// binding a unix socket there fails with EINVAL, leaving http_request dead
// while every other tool works. A hashed short name under the temp dir keeps
// the address bounded; the runtime mounts the parent into the container.
func HTTPWorkerSocketPath(scanDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(scanDir)))
	return filepath.Join(os.TempDir(), "xalg-http", fmt.Sprintf("%x.sock", sum[:8]))
}

// ContainerHTTPTransport returns a RoundTripper that executes each single HTTP
// request inside the scan's project container. It is used only when the docker
// execution plane is active; the native path is unchanged.
func ContainerHTTPTransport(contextID string) (http.RoundTripper, bool) {
	if !ContainerActive() {
		return nil, false
	}
	sc := scanctx.Get(contextID)
	if sc == nil || strings.TrimSpace(sc.ScanDir) == "" {
		return nil, false
	}
	return &httpWorkerTransport{
		scanCtx: sc,
		socket:  HTTPWorkerSocketPath(sc.ScanDir),
	}, true
}

type httpWorkerTransport struct {
	scanCtx *scanctx.ScanContext
	socket  string

	clientOnce sync.Once
	client     *http.Client
}

const (
	httpWorkerRequestLimit = 8 << 20
	httpWorkerBodyDefault  = 512 << 10
	httpWorkerBodyHard     = 32 << 20
)

type workerBodyLimitKey struct{}

// WithHTTPWorkerBodyLimit sets how many response bytes the container-side
// worker should return. Native callers cap the body after reading; the
// container path uses this to avoid transferring a huge response over the
// socket only to truncate it immediately.
func WithHTTPWorkerBodyLimit(ctx context.Context, n int64) context.Context {
	if n <= 0 {
		return ctx
	}
	return context.WithValue(ctx, workerBodyLimitKey{}, n)
}

func httpWorkerBodyLimit(ctx context.Context) int64 {
	if v, ok := ctx.Value(workerBodyLimitKey{}).(int64); ok && v > 0 {
		return v
	}
	return httpWorkerBodyDefault
}

func (t *httpWorkerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t == nil || t.scanCtx == nil {
		return nil, errors.New("http worker: no scan context")
	}
	if err := t.ensureWorker(req.Context()); err != nil {
		return nil, err
	}

	var body []byte
	if req.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(req.Body, httpWorkerRequestLimit+1))
		if err != nil {
			return nil, fmt.Errorf("http worker: read request body: %w", err)
		}
		_ = req.Body.Close()
		if len(raw) > httpWorkerRequestLimit {
			return nil, fmt.Errorf("http worker: request body exceeds %d bytes", httpWorkerRequestLimit)
		}
		body = raw
	}

	headers := make(map[string][]string, len(req.Header))
	for name, values := range req.Header {
		headers[name] = append([]string(nil), values...)
	}
	maxBody := httpWorkerBodyLimit(req.Context())
	if maxBody > httpWorkerBodyHard {
		maxBody = httpWorkerBodyHard
	}
	timeout := 30 * time.Second
	if deadline, ok := req.Context().Deadline(); ok {
		timeout = time.Until(deadline)
		if timeout <= 0 {
			timeout = time.Millisecond
		}
	}
	proxyAuth := ""
	if token := trafficproxy.RunTokenFor(t.scanCtx.ID); token != "" {
		proxyAuth = "Bearer " + token
	}
	payload, err := json.Marshal(struct {
		Method             string              `json:"method"`
		URL                string              `json:"url"`
		Headers            map[string][]string `json:"headers,omitempty"`
		BodyBase64         string              `json:"body_base64,omitempty"`
		TimeoutMS          int64               `json:"timeout_ms"`
		MaxBody            int64               `json:"max_body"`
		TLSSkipVerify      bool                `json:"tls_skip_verify,omitempty"`
		ProxyAuthorization string              `json:"proxy_authorization,omitempty"`
	}{
		Method:             req.Method,
		URL:                req.URL.String(),
		Headers:            headers,
		BodyBase64:         base64.StdEncoding.EncodeToString(body),
		TimeoutMS:          timeout.Milliseconds(),
		MaxBody:            maxBody,
		TLSSkipVerify:      config.Get().TLSSkipVerify,
		ProxyAuthorization: proxyAuth,
	})
	if err != nil {
		return nil, fmt.Errorf("http worker: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, "http://httpworker/v1/request", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := t.unixClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http worker: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Status     int                 `json:"status"`
		Headers    map[string][]string `json:"headers"`
		BodyBase64 string              `json:"body_base64"`
		Truncated  bool                `json:"truncated"`
		DurationMS int64               `json:"duration_ms"`
		Error      string              `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("http worker: decode response: %w", err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("http worker: %s", out.Error)
	}
	raw, err := base64.StdEncoding.DecodeString(out.BodyBase64)
	if err != nil {
		return nil, fmt.Errorf("http worker: decode body: %w", err)
	}
	header := make(http.Header, len(out.Headers))
	for name, values := range out.Headers {
		header[name] = append([]string(nil), values...)
	}
	return &http.Response{
		StatusCode:    out.Status,
		Status:        fmt.Sprintf("%d %s", out.Status, http.StatusText(out.Status)),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(raw)),
		ContentLength: int64(len(raw)),
		Request:       req,
	}, nil
}

func (t *httpWorkerTransport) ensureWorker(ctx context.Context) error {
	if _, err := EnsureScanProject(ctx, t.scanCtx); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if t.healthy(ctx) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("http worker: health check on %s did not succeed (project image may be missing /opt/xalgorix/bin/httpworker)", t.socket)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (t *httpWorkerTransport) healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://httpworker/v1/health", nil)
	if err != nil {
		return false
	}
	resp, err := t.unixClient().Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

func (t *httpWorkerTransport) unixClient() *http.Client {
	t.clientOnce.Do(func() {
		tr := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", t.socket)
			},
			MaxIdleConns:    4,
			IdleConnTimeout: 30 * time.Second,
		}
		t.client = &http.Client{Transport: tr}
	})
	return t.client
}
