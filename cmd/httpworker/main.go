// Command httpworker executes one HTTP request at a time inside a project
// container. The platform keeps redirect handling, scope guards, timeouts and
// response formatting; this worker only performs the network I/O so untrusted
// target responses never touch the platform process directly.
//
// Transport: HTTP/1.1 over a unix socket inside the scan directory (mounted at
// the same absolute path on both sides).
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type request struct {
	Method             string              `json:"method"`
	URL                string              `json:"url"`
	Headers            map[string][]string `json:"headers,omitempty"`
	BodyBase64         string              `json:"body_base64,omitempty"`
	TimeoutMS          int                 `json:"timeout_ms,omitempty"`
	MaxBody            int64               `json:"max_body,omitempty"`
	TLSSkipVerify      bool                `json:"tls_skip_verify,omitempty"`
	ProxyAuthorization string              `json:"proxy_authorization,omitempty"`
}

type response struct {
	Status     int                 `json:"status"`
	Headers    map[string][]string `json:"headers,omitempty"`
	BodyBase64 string              `json:"body_base64,omitempty"`
	Truncated  bool                `json:"truncated,omitempty"`
	DurationMS int64               `json:"duration_ms"`
	Error      string              `json:"error,omitempty"`
}

const (
	defaultMaxBody = 512 << 10
	hardMaxBody    = 32 << 20
	maxRequestSize = 8 << 20
)

var transports sync.Map // key: token|skip -> *http.Transport

func main() {
	var socket string
	flag.StringVar(&socket, "socket", "", "unix socket to listen on")
	flag.Parse()
	if strings.TrimSpace(socket) == "" {
		log.Fatal("httpworker: --socket is required")
	}
	// The platform process runs as a different uid than the container root, so
	// the socket directory and socket must be traversable/connectable by it.
	// The scan directory itself (mode 0700) remains the outer boundary.
	if err := os.MkdirAll(dirOf(socket), 0o755); err != nil {
		log.Fatalf("httpworker: create socket dir: %v", err)
	}
	_ = os.Chmod(dirOf(socket), 0o755)
	// A stale socket from a crashed worker would make Listen fail.
	if info, err := os.Stat(socket); err == nil && info.Mode()&os.ModeSocket != 0 {
		if conn, dialErr := net.DialTimeout("unix", socket, 200*time.Millisecond); dialErr != nil {
			_ = os.Remove(socket)
		} else {
			_ = conn.Close()
			log.Fatalf("httpworker: socket %s is already served", socket)
		}
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		log.Fatalf("httpworker: listen %s: %v", socket, err)
	}
	_ = os.Chmod(socket, 0o666)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/v1/request", handleRequest)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			log.Printf("httpworker: serve: %v", serveErr)
			stop()
		}
	}()
	log.Printf("httpworker: listening on %s", socket)
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestSize+1))
	if err != nil {
		writeJSON(w, response{Error: "read request: " + err.Error()})
		return
	}
	if len(body) > maxRequestSize {
		writeJSON(w, response{Error: "request too large"})
		return
	}
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, response{Error: "decode request: " + err.Error()})
		return
	}
	writeJSON(w, execute(r.Context(), req))
}

func execute(parent context.Context, in request) (result response) {
	started := time.Now()
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()

	if strings.TrimSpace(in.URL) == "" {
		result.Error = "url is required"
		return result
	}
	method := strings.TrimSpace(in.Method)
	if method == "" {
		method = http.MethodGet
	}
	maxBody := in.MaxBody
	if maxBody <= 0 {
		maxBody = defaultMaxBody
	}
	if maxBody > hardMaxBody {
		maxBody = hardMaxBody
	}
	timeout := time.Duration(in.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	var bodyReader io.Reader
	if in.BodyBase64 != "" {
		raw, err := base64.StdEncoding.DecodeString(in.BodyBase64)
		if err != nil {
			result.Error = "decode body: " + err.Error()
			return result
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, in.URL, bodyReader)
	if err != nil {
		result.Error = "build request: " + err.Error()
		return result
	}
	for name, values := range in.Headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	tr := transportFor(in.ProxyAuthorization, in.TLSSkipVerify)
	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if in.ProxyAuthorization != "" {
		if proxyURL, _ := http.ProxyFromEnvironment(req); proxyURL != nil {
			req.Header.Set("Proxy-Authorization", in.ProxyAuthorization)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer func() { _ = resp.Body.Close() }()

	result.Status = resp.StatusCode
	result.Headers = resp.Header
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if readErr != nil {
		result.Error = "read response: " + readErr.Error()
		return result
	}
	if int64(len(raw)) > maxBody {
		raw = raw[:maxBody]
		result.Truncated = true
	}
	result.BodyBase64 = base64.StdEncoding.EncodeToString(raw)
	return result
}

func transportFor(token string, skipVerify bool) *http.Transport {
	key := token + "|" + strconv.FormatBool(skipVerify)
	if v, ok := transports.Load(key); ok {
		return v.(*http.Transport)
	}
	tr := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: skipVerify}, //nolint:gosec // operator-configured lab mode
	}
	if token != "" {
		tr.ProxyConnectHeader = http.Header{"Proxy-Authorization": {token}}
	}
	actual, _ := transports.LoadOrStore(key, tr)
	return actual.(*http.Transport)
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return "."
}
