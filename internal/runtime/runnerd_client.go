package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// RunnerdDriver is the same execution plane as DockerDriver, reached through the
// runnerd service instead of the Docker socket.
//
// Why it exists: mounting /var/run/docker.sock into the platform container hands
// the engine root-equivalent power over the host. runnerd runs on the host, owns
// the socket, and exposes exactly the operations this interface needs — no
// arbitrary HostConfig, no shell on the host, no image push/build.
//
// The wire contract is the RunnerdAPI structs below; cmd/runnerd implements the
// server side with the identical shapes.
type RunnerdDriver struct {
	http   *http.Client
	socket string
}

// RunnerdConfig configures the runnerd transport.
type RunnerdConfig struct {
	SocketPath string
	// Timeout bounds a single control-plane call. Exec calls use their own
	// per-command deadline, so this only guards ensure/stop/inspect.
	Timeout time.Duration
}

// NewRunnerdDriver builds a client for the host-side runnerd.
func NewRunnerdDriver(cfg RunnerdConfig) (*RunnerdDriver, error) {
	socket := strings.TrimSpace(cfg.SocketPath)
	if socket == "" {
		socket = "/run/xalgorix/runner.sock"
	}
	if _, err := net.DialTimeout("unix", socket, 2*time.Second); err != nil {
		return nil, fmt.Errorf("runtime: runnerd socket %q is not reachable: %w", socket, err)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		DisableCompression: true,
		MaxIdleConns:       8,
		IdleConnTimeout:    60 * time.Second,
	}
	return &RunnerdDriver{http: &http.Client{Transport: transport, Timeout: timeout}, socket: socket}, nil
}

// Backend implements Driver.
func (d *RunnerdDriver) Backend() Backend { return BackendDocker }

// Close implements Driver.
func (d *RunnerdDriver) Close() error {
	if tr, ok := d.http.Transport.(interface{ CloseIdleConnections() }); ok {
		tr.CloseIdleConnections()
	}
	return nil
}

// runnerdError is the wire error envelope.
type runnerdError struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Retryable bool      `json:"retryable"`
	Phase     string    `json:"phase"`
}

type runnerdEnvelope struct {
	Error *runnerdError `json:"error,omitempty"`
}

func (d *RunnerdDriver) call(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return NewExecError(CodeRuntimeUnavailable, PhaseExec, "encode request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://runnerd"+path, reader)
	if err != nil {
		return NewExecError(CodeRuntimeUnavailable, PhaseExec, "build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return NewExecError(CodeRuntimeUnavailable, PhaseExec, "runnerd %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return NewExecError(CodeRuntimeUnavailable, PhaseExec, "read runnerd response: %v", readErr)
	}
	var envelope runnerdEnvelope
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &envelope)
	}
	if envelope.Error != nil {
		return &ExecError{
			Code:      envelope.Error.Code,
			Message:   envelope.Error.Message,
			Retryable: envelope.Error.Retryable,
			Phase:     envelope.Error.Phase,
		}
	}
	if resp.StatusCode >= 400 {
		return NewExecError(CodeRuntimeUnavailable, PhaseExec, "runnerd http %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return NewExecError(CodeRuntimeUnavailable, PhaseExec, "decode runnerd response: %v", err)
	}
	return nil
}

// EnsureProject implements Driver.
func (d *RunnerdDriver) EnsureProject(ctx context.Context, spec ProjectSpec) (ProjectHandle, error) {
	var out struct {
		Project ProjectHandle `json:"project"`
	}
	if err := d.call(ctx, http.MethodPost, "/v1/projects", spec, &out); err != nil {
		return ProjectHandle{}, err
	}
	return out.Project, nil
}

// Exec implements Driver.
func (d *RunnerdDriver) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	return d.ExecStream(ctx, spec, nil)
}

// ExecStream implements Driver. It requests NDJSON so the runnerd can forward
// frames as they arrive; the terminal frame carries the final result.
func (d *RunnerdDriver) ExecStream(ctx context.Context, spec ExecSpec, onFrame func(StreamFrame)) (*ExecResult, error) {
	encoded, err := json.Marshal(spec)
	if err != nil {
		return nil, NewExecError(CodeRuntimeUnavailable, PhaseExec, "encode exec spec: %v", err)
	}
	path := "/v1/projects/" + spec.ProjectID + "/exec"
	if onFrame != nil {
		path += "?stream=1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://runnerd"+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, NewExecError(CodeRuntimeUnavailable, PhaseExec, "build exec request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if onFrame != nil {
		req.Header.Set("Accept", "application/x-ndjson")
	}
	// Exec calls are bounded by their own deadline; the client-wide timeout must
	// not cut them short.
	resp, err := (&http.Client{Transport: d.http.Transport}).Do(req)
	if err != nil {
		return nil, NewExecError(CodeRuntimeUnavailable, PhaseExec, "runnerd exec: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/x-ndjson") {
		return decodeNDJSON(resp.Body, onFrame)
	}
	payload, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, NewExecError(CodeRuntimeUnavailable, PhaseExec, "read exec response: %v", readErr)
	}
	var envelope struct {
		Result *ExecResult   `json:"result"`
		Error  *runnerdError `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, NewExecError(CodeRuntimeUnavailable, PhaseExec, "decode exec response: %v", err)
	}
	if envelope.Error != nil {
		return nil, &ExecError{Code: envelope.Error.Code, Message: envelope.Error.Message, Retryable: envelope.Error.Retryable, Phase: envelope.Error.Phase}
	}
	if envelope.Result == nil {
		return nil, NewExecError(CodeRuntimeUnavailable, PhaseExec, "runnerd returned an empty exec result")
	}
	return envelope.Result, nil
}

func decodeNDJSON(r io.Reader, onFrame func(StreamFrame)) (*ExecResult, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var frame runnerdFrame
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		frame = runnerdFrame{}
		if err := json.Unmarshal(line, &frame); err != nil {
			return nil, NewExecError(CodeRuntimeUnavailable, PhaseStream, "decode frame: %v", err)
		}
		switch frame.Type {
		case "stdout", "stderr":
			if onFrame != nil {
				onFrame(StreamFrame{Kind: StreamKind(frame.Type), Seq: frame.Seq, Data: frame.Data})
			}
		case "exit":
			if frame.Error != nil {
				return nil, &ExecError{Code: frame.Error.Code, Message: frame.Error.Message, Retryable: frame.Error.Retryable, Phase: frame.Error.Phase}
			}
			if frame.Result != nil {
				return frame.Result, nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, NewExecError(CodeRuntimeUnavailable, PhaseStream, "read frame stream: %v", err)
	}
	return nil, NewExecError(CodeRuntimeUnavailable, PhaseStream, "runnerd stream ended without an exit frame")
}

// runnerdFrame is one NDJSON line produced by runnerd.
type runnerdFrame struct {
	Type   string        `json:"type"`
	Seq    uint64        `json:"seq,omitempty"`
	Data   string        `json:"data,omitempty"`
	Result *ExecResult   `json:"result,omitempty"`
	Error  *runnerdError `json:"error,omitempty"`
}

// StopProject implements Driver.
func (d *RunnerdDriver) StopProject(ctx context.Context, projectID string, grace time.Duration) error {
	req := struct {
		Grace string `json:"grace"`
	}{Grace: grace.String()}
	return d.call(ctx, http.MethodPost, "/v1/projects/"+projectID+"/stop", req, nil)
}

// RemoveProject implements Driver.
func (d *RunnerdDriver) RemoveProject(ctx context.Context, projectID string) error {
	return d.call(ctx, http.MethodDelete, "/v1/projects/"+projectID, nil, nil)
}

// Inspect implements Driver.
func (d *RunnerdDriver) Inspect(ctx context.Context, projectID string) (ProjectState, error) {
	var out struct {
		Project ProjectState `json:"project"`
	}
	if err := d.call(ctx, http.MethodGet, "/v1/projects/"+projectID, nil, &out); err != nil {
		return ProjectState{}, err
	}
	return out.Project, nil
}

// ListProjects implements ProjectLister.
func (d *RunnerdDriver) ListProjects(ctx context.Context) ([]ProjectState, error) {
	var out struct {
		Projects []ProjectState `json:"projects"`
	}
	if err := d.call(ctx, http.MethodGet, "/v1/projects", nil, &out); err != nil {
		return nil, err
	}
	return out.Projects, nil
}

// Capacity implements Driver.
func (d *RunnerdDriver) Capacity(ctx context.Context) (CapacityReport, error) {
	var out struct {
		Capacity CapacityReport `json:"capacity"`
	}
	if err := d.call(ctx, http.MethodGet, "/v1/capacity", nil, &out); err != nil {
		return CapacityReport{}, err
	}
	out.Capacity.Transport = string(TransportRunnerd)
	return out.Capacity, nil
}

// CDPEndpoint asks runnerd for a relay URL to the project's Chromium DevTools
// endpoint. The relay is what makes the browser usable from a platform that
// runs in its own container: runnerd sees the host's published port, the
// platform cannot.
func (d *RunnerdDriver) CDPEndpoint(ctx context.Context, projectID, proxyURL string) (string, error) {
	var out struct {
		Endpoint string `json:"endpoint"`
	}
	body := map[string]string{"proxy_url": proxyURL}
	if err := d.call(ctx, http.MethodPost, "/v1/projects/"+projectID+"/cdp", body, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Endpoint) == "" {
		return "", NewExecError(CodeRuntimeUnavailable, PhaseExec, "runnerd returned an empty CDP endpoint")
	}
	return out.Endpoint, nil
}

// RunnerdHandler is the server half of the runnerd contract. cmd/runnerd wires
// it to a DockerDriver; tests wire it to a fake.
type RunnerdHandler struct {
	Driver Driver
	// CDP relays the project's loopback-bound Chromium DevTools port to a
	// host name the platform can reach. nil keeps the direct endpoint (host
	// platform) and makes the /cdp route unsupported.
	CDP *CDPRelay
}

// ServeHTTP implements the runnerd API.
func (h *RunnerdHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Driver == nil {
		writeRunnerdError(w, NewExecError(CodeRuntimeUnavailable, PhaseExec, "runnerd has no driver"))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	switch {
	case r.Method == http.MethodPost && path == "/projects":
		var spec ProjectSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			writeRunnerdError(w, NewExecError(CodeContainerStart, PhaseEnsure, "decode project spec: %v", err))
			return
		}
		handle, err := h.Driver.EnsureProject(r.Context(), spec)
		if err != nil {
			writeRunnerdError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": handle})

	case r.Method == http.MethodGet && path == "/projects":
		lister, ok := h.Driver.(ProjectLister)
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"projects": []ProjectState{}})
			return
		}
		projects, err := lister.ListProjects(r.Context())
		if err != nil {
			writeRunnerdError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"projects": projects})

	case r.Method == http.MethodGet && path == "/capacity":
		capacity, err := h.Driver.Capacity(r.Context())
		if err != nil {
			writeRunnerdError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"capacity": capacity})

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/exec"):
		projectID := strings.TrimSuffix(strings.TrimPrefix(path, "/projects/"), "/exec")
		var spec ExecSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			writeRunnerdError(w, NewExecError(CodeExecSpawn, PhaseExec, "decode exec spec: %v", err))
			return
		}
		spec.ProjectID = projectID
		if r.URL.Query().Get("stream") == "1" {
			h.streamExec(w, r, spec)
			return
		}
		result, err := h.Driver.Exec(r.Context(), spec)
		if err != nil {
			writeRunnerdError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": result})

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/cdp"):
		projectID := strings.TrimSuffix(strings.TrimPrefix(path, "/projects/"), "/cdp")
		var body struct {
			ProxyURL string `json:"proxy_url"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		provider, ok := h.Driver.(CDPProvider)
		if !ok || h.CDP == nil {
			writeRunnerdError(w, NewExecError(CodeUnsupported, PhaseExec,
				"this runnerd cannot relay a CDP endpoint (driver has no DevTools support or the relay is disabled)"))
			return
		}
		endpoint, err := h.CDP.Endpoint(r.Context(), provider, projectID, body.ProxyURL)
		if err != nil {
			writeRunnerdError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"endpoint": endpoint})

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/stop"):
		projectID := strings.TrimSuffix(strings.TrimPrefix(path, "/projects/"), "/stop")
		var body struct {
			Grace string `json:"grace"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		grace := 10 * time.Second
		if body.Grace != "" {
			if parsed, err := time.ParseDuration(body.Grace); err == nil && parsed > 0 {
				grace = parsed
			}
		}
		if err := h.Driver.StopProject(r.Context(), projectID, grace); err != nil {
			writeRunnerdError(w, err)
			return
		}
		if h.CDP != nil {
			h.CDP.Remove(projectID)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/projects/"):
		projectID := strings.TrimPrefix(path, "/projects/")
		if err := h.Driver.RemoveProject(r.Context(), projectID); err != nil {
			writeRunnerdError(w, err)
			return
		}
		if h.CDP != nil {
			h.CDP.Remove(projectID)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case r.Method == http.MethodGet && strings.HasPrefix(path, "/projects/"):
		projectID := strings.TrimPrefix(path, "/projects/")
		state, err := h.Driver.Inspect(r.Context(), projectID)
		if err != nil {
			writeRunnerdError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": state})

	default:
		writeRunnerdError(w, NewExecError(CodeUnsupported, PhaseExec, "unsupported runnerd route %s %s", r.Method, r.URL.Path))
	}
}

func (h *RunnerdHandler) streamExec(w http.ResponseWriter, r *http.Request, spec ExecSpec) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeRunnerdError(w, NewExecError(CodeUnsupported, PhaseStream, "streaming unsupported by this connection"))
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	result, err := h.Driver.ExecStream(r.Context(), spec, func(frame StreamFrame) {
		_ = enc.Encode(runnerdFrame{Type: string(frame.Kind), Seq: frame.Seq, Data: frame.Data})
		flusher.Flush()
	})
	if err != nil {
		code := CodeOf(err)
		if code == "" {
			code = CodeRuntimeUnavailable
		}
		_ = enc.Encode(runnerdFrame{Type: "exit", Error: &runnerdError{
			Code:      code,
			Message:   err.Error(),
			Retryable: RetryableCode(code),
			Phase:     phaseOf(err),
		}})
		flusher.Flush()
		return
	}
	_ = enc.Encode(runnerdFrame{Type: "exit", Result: result})
	flusher.Flush()
}

func phaseOf(err error) string {
	var execErr *ExecError
	if errors.As(err, &execErr) && execErr.Phase != "" {
		return execErr.Phase
	}
	return PhaseExec
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeRunnerdError(w http.ResponseWriter, err error) {
	code := CodeOf(err)
	if code == "" {
		code = CodeRuntimeUnavailable
	}
	status := http.StatusInternalServerError
	switch code {
	case CodeNotFound:
		status = http.StatusNotFound
	case CodeProjectLimit, CodeLeaseTimeout:
		status = http.StatusTooManyRequests
	case CodeCanceled:
		status = http.StatusRequestTimeout
	case CodeUnsupported:
		status = http.StatusNotImplemented
	case CodeContainerStart, CodeImageMissing:
		status = http.StatusBadGateway
	}
	writeJSON(w, status, map[string]any{"error": runnerdError{
		Code:      code,
		Message:   err.Error(),
		Retryable: RetryableCode(code),
		Phase:     PhaseExec,
	}})
}
