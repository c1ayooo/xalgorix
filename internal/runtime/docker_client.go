package runtime

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Minimal Docker Engine API client over the UNIX socket.
//
// Deliberately hand-written: the official SDK (github.com/docker/docker) pulls a
// very large dependency tree into a project whose selling point is a single
// self-contained binary, and this platform uses eight endpoints. The surface is
// intentionally closed — callers cannot pass arbitrary HostConfig.
type dockerClient struct {
	http *http.Client
}

func newDockerClient(socketPath string) *dockerClient {
	if socketPath == "" {
		socketPath = "/var/run/docker.sock"
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
		DisableCompression: true,
		MaxIdleConns:       16,
		IdleConnTimeout:    60 * time.Second,
	}
	return &dockerClient{http: &http.Client{Transport: transport}}
}

// dockerError is the {"message": "..."} envelope the daemon returns.
type dockerError struct {
	Message string `json:"message"`
	status  int
}

func (e *dockerError) Error() string {
	if e.status != 0 {
		return fmt.Sprintf("docker api %d: %s", e.status, e.Message)
	}
	return "docker api: " + e.Message
}

func (c *dockerClient) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("docker: encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, reader)
	if err != nil {
		return fmt.Errorf("docker: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		apiErr := &dockerError{status: resp.StatusCode}
		if jsonErr := json.Unmarshal(payload, apiErr); jsonErr != nil || apiErr.Message == "" {
			apiErr.Message = strings.TrimSpace(string(payload))
		}
		return apiErr
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("docker: decode response: %w", err)
	}
	return nil
}

// IsNotFound reports whether err is a 404 from the daemon.
func dockerIsNotFound(err error) bool {
	var apiErr *dockerError
	return errors.As(err, &apiErr) && apiErr.status == http.StatusNotFound
}

type dockerPing struct {
	APIVersion string `json:"ApiVersion"`
	OSType     string `json:"OSType"`
}

func (c *dockerClient) ping(ctx context.Context) error {
	var out dockerPing
	if err := c.do(ctx, http.MethodGet, "/_ping", nil, nil); err != nil {
		return err
	}
	// A second call fetches the version document; /_ping returns plain text on
	// older daemons, so decode failures here are not fatal.
	_ = out
	return nil
}

type dockerPortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type dockerContainerConfig struct {
	Image        string              `json:"Image"`
	Cmd          []string            `json:"Cmd,omitempty"`
	Env          []string            `json:"Env,omitempty"`
	WorkingDir   string              `json:"WorkingDir,omitempty"`
	Labels       map[string]string   `json:"Labels,omitempty"`
	Hostname     string              `json:"Hostname,omitempty"`
	Entrypoint   []string            `json:"Entrypoint,omitempty"`
	User         string              `json:"User,omitempty"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
}

type dockerHostConfig struct {
	Binds          []string                       `json:"Binds,omitempty"`
	NetworkMode    string                         `json:"NetworkMode,omitempty"`
	ReadonlyRootfs bool                           `json:"ReadonlyRootfs,omitempty"`
	Privileged     bool                           `json:"Privileged"`
	CapDrop        []string                       `json:"CapDrop,omitempty"`
	CapAdd         []string                       `json:"CapAdd,omitempty"`
	SecurityOpt    []string                       `json:"SecurityOpt,omitempty"`
	Tmpfs          map[string]string              `json:"Tmpfs,omitempty"`
	StorageOpt     map[string]string              `json:"StorageOpt,omitempty"`
	Resources      dockerResources                `json:"Resources"`
	PortBindings   map[string][]dockerPortBinding `json:"PortBindings,omitempty"`
	AutoRemove     bool                           `json:"AutoRemove"`
	Init           bool                           `json:"Init,omitempty"`
}

type dockerResources struct {
	Memory     int64 `json:"Memory,omitempty"`
	MemorySwap int64 `json:"MemorySwap,omitempty"`
	NanoCPUs   int64 `json:"NanoCpus,omitempty"`
	CPUQuota   int64 `json:"CpuQuota,omitempty"`
	CPUPeriod  int64 `json:"CpuPeriod,omitempty"`
	PidsLimit  int64 `json:"PidsLimit,omitempty"`
}

type dockerCreateRequest struct {
	dockerContainerConfig
	HostConfig dockerHostConfig `json:"HostConfig"`
}

type dockerCreateResponse struct {
	ID       string   `json:"Id"`
	Warnings []string `json:"Warnings"`
}

func (c *dockerClient) createContainer(ctx context.Context, name string, req dockerCreateRequest) (string, error) {
	var out dockerCreateResponse
	path := "/containers/create"
	if name != "" {
		path += "?name=" + url.QueryEscape(name)
	}
	if err := c.do(ctx, http.MethodPost, path, req, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("docker: create container returned no id")
	}
	return out.ID, nil
}

func (c *dockerClient) startContainer(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil)
}

func (c *dockerClient) stopContainer(ctx context.Context, id string, grace time.Duration) error {
	secs := int(grace.Seconds())
	if secs < 1 {
		secs = 1
	}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/containers/%s/stop?t=%d", id, secs), nil, nil)
	if dockerIsNotFound(err) {
		return NewExecError(CodeNotFound, PhaseStop, "container %s not found", id)
	}
	if err != nil && strings.Contains(err.Error(), "not running") {
		return nil
	}
	return err
}

func (c *dockerClient) removeContainer(ctx context.Context, id string, force bool) error {
	path := "/containers/" + id
	if force {
		path += "?force=1&v=1"
	}
	err := c.do(ctx, http.MethodDelete, path, nil, nil)
	if dockerIsNotFound(err) {
		return NewExecError(CodeNotFound, PhaseRemove, "container %s not found", id)
	}
	return err
}

type dockerContainerInspect struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Status   string `json:"Status"`
		Running  bool   `json:"Running"`
		ExitCode int    `json:"ExitCode"`
		Error    string `json:"Error"`
	} `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	NetworkSettings struct {
		Ports map[string][]dockerPortBinding `json:"Ports"`
	} `json:"NetworkSettings"`
	Created string `json:"Created"`
}

func (c *dockerClient) inspectContainer(ctx context.Context, id string) (dockerContainerInspect, error) {
	var out dockerContainerInspect
	err := c.do(ctx, http.MethodGet, "/containers/"+id+"/json", nil, &out)
	if dockerIsNotFound(err) {
		return out, NewExecError(CodeNotFound, PhaseInspect, "container %s not found", id)
	}
	return out, err
}

type dockerContainerSummary struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`
	Labels  map[string]string `json:"Labels"`
	Created int64             `json:"Created"`
}

func (c *dockerClient) listContainers(ctx context.Context, filters map[string][]string) ([]dockerContainerSummary, error) {
	query := url.Values{"all": []string{"1"}}
	if len(filters) > 0 {
		encoded, err := json.Marshal(filters)
		if err != nil {
			return nil, fmt.Errorf("docker: encode filters: %w", err)
		}
		query.Set("filters", string(encoded))
	}
	var out []dockerContainerSummary
	if err := c.do(ctx, http.MethodGet, "/containers/json?"+query.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type dockerExecConfig struct {
	AttachStdin  bool     `json:"AttachStdin"`
	AttachStdout bool     `json:"AttachStdout"`
	AttachStderr bool     `json:"AttachStderr"`
	Tty          bool     `json:"Tty"`
	Env          []string `json:"Env,omitempty"`
	WorkingDir   string   `json:"WorkingDir,omitempty"`
	User         string   `json:"User,omitempty"`
	Cmd          []string `json:"Cmd"`
}

func (c *dockerClient) createExec(ctx context.Context, containerID string, cfg dockerExecConfig) (string, error) {
	var out struct {
		ID string `json:"Id"`
	}
	if err := c.do(ctx, http.MethodPost, "/containers/"+containerID+"/exec", cfg, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("docker: exec create returned no id")
	}
	return out.ID, nil
}

// startExec attaches to the exec and demultiplexes the stream. Docker returns an
// upgraded raw connection; the body is a sequence of frames with an 8-byte
// header (stream type, 3 padding bytes, big-endian length).
func (c *dockerClient) startExec(ctx context.Context, execID string, onStdout, onStderr func([]byte)) error {
	payload, err := json.Marshal(map[string]any{"Detach": false, "Tty": false})
	if err != nil {
		return fmt.Errorf("docker: encode exec start: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker/exec/"+execID+"/start", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("docker: build exec start: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker: exec start: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
		return fmt.Errorf("docker: exec start %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return demuxStream(resp.Body, onStdout, onStderr)
}

// demuxStream splits Docker's multiplexed stdout/stderr frames.
func demuxStream(r io.Reader, onStdout, onStderr func([]byte)) error {
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		size := binary.BigEndian.Uint32(header[4:8])
		if size == 0 {
			continue
		}
		buf := make([]byte, size)
		if _, err := io.ReadFull(r, buf); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		switch header[0] {
		case 2: // stderr
			if onStderr != nil {
				onStderr(buf)
			}
		default: // stdout (and stdin echoes, which we never attach)
			if onStdout != nil {
				onStdout(buf)
			}
		}
	}
}

// startExecDetached starts an exec without attaching; used for long-lived
// services inside the container (Chromium's DevTools endpoint).
func (c *dockerClient) startExecDetached(ctx context.Context, execID string) error {
	return c.do(ctx, http.MethodPost, "/exec/"+execID+"/start",
		map[string]any{"Detach": true, "Tty": false}, nil)
}

type dockerExecInspect struct {
	ExitCode int  `json:"ExitCode"`
	Running  bool `json:"Running"`
	Pid      int  `json:"Pid"`
}

func (c *dockerClient) inspectExec(ctx context.Context, execID string) (dockerExecInspect, error) {
	var out dockerExecInspect
	err := c.do(ctx, http.MethodGet, "/exec/"+execID+"/json", nil, &out)
	return out, err
}

func (c *dockerClient) pullImage(ctx context.Context, ref string) error {
	fromImage, tag := splitImageRef(ref)
	path := fmt.Sprintf("/images/create?fromImage=%s&tag=%s", url.QueryEscape(fromImage), url.QueryEscape(tag))
	// The daemon streams progress; drain it so the pull completes and errors
	// surface as a decoded {"error":...} payload.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker"+path, nil)
	if err != nil {
		return fmt.Errorf("docker: build pull request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker: pull %s: %w", ref, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
		return fmt.Errorf("docker: pull %s: %s", ref, strings.TrimSpace(string(body)))
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			break
		}
		if msg.Error != "" {
			return fmt.Errorf("docker: pull %s: %s", ref, msg.Error)
		}
	}
	return nil
}

func splitImageRef(ref string) (image, tag string) {
	if at := strings.Index(ref, "@"); at >= 0 {
		return ref[:at], ref[at+1:]
	}
	// A colon after the last slash is a tag; before it is a registry port.
	lastSlash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > lastSlash {
		return ref[:colon], ref[colon+1:]
	}
	return ref, "latest"
}

type dockerNetworkCreate struct {
	Name           string            `json:"Name"`
	Driver         string            `json:"Driver"`
	Internal       bool              `json:"Internal"`
	CheckDuplicate bool              `json:"CheckDuplicate"`
	Labels         map[string]string `json:"Labels,omitempty"`
	Options        map[string]string `json:"Options,omitempty"`
}

func (c *dockerClient) createNetwork(ctx context.Context, req dockerNetworkCreate) error {
	err := c.do(ctx, http.MethodPost, "/networks/create", req, nil)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

func (c *dockerClient) removeNetwork(ctx context.Context, name string) error {
	err := c.do(ctx, http.MethodDelete, "/networks/"+url.PathEscape(name), nil, nil)
	if dockerIsNotFound(err) || (err != nil && strings.Contains(err.Error(), "not found")) {
		return nil
	}
	return err
}

type dockerStats struct {
	Memory struct {
		Usage int64 `json:"usage"`
		Limit int64 `json:"limit"`
	} `json:"memory_stats"`
	CPU struct {
		Usage struct {
			Total uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		System uint64 `json:"system_cpu_usage"`
		Online int    `json:"online_cpus"`
	} `json:"cpu_stats"`
}

func (c *dockerClient) stats(ctx context.Context, id string) (dockerStats, error) {
	var out dockerStats
	err := c.do(ctx, http.MethodGet, "/containers/"+id+"/stats?stream=false&one-shot=true", nil, &out)
	return out, err
}

type dockerImageSummary struct {
	ID       string   `json:"Id"`
	RepoTags []string `json:"RepoTags"`
	Created  int64    `json:"Created"`
	Size     int64    `json:"Size"`
}

func (c *dockerClient) listImages(ctx context.Context, reference string) ([]dockerImageSummary, error) {
	query := url.Values{"all": []string{"0"}}
	if reference != "" {
		encoded, err := json.Marshal(map[string][]string{"reference": {reference}})
		if err != nil {
			return nil, fmt.Errorf("docker: encode image filters: %w", err)
		}
		query.Set("filters", string(encoded))
	}
	var out []dockerImageSummary
	if err := c.do(ctx, http.MethodGet, "/images/json?"+query.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *dockerClient) removeImage(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, "/images/"+url.PathEscape(id)+"?force=0&noprune=1", nil, nil)
	if dockerIsNotFound(err) || (err != nil && strings.Contains(err.Error(), "No such image")) {
		return nil
	}
	return err
}

// socketExists is a cheap preflight so failures read as configuration errors
// rather than opaque dial errors.
func socketExists(path string) bool {
	if path == "" {
		path = "/var/run/docker.sock"
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}
