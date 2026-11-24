package runtime

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDockerProjectStartsHTTPWorker is opt-in: it needs a real Docker daemon
// and a toolbox image that contains /opt/xalgorix/bin/httpworker.
//
//	XALGORIX_HTTP_WORKER_INTEGRATION=1 \
//	XALGORIX_TEST_TOOLBOX_IMAGE=xalgorix-toolbox:dev-core \
//	go test ./internal/runtime -run TestDockerProjectStartsHTTPWorker -v
func TestDockerProjectStartsHTTPWorker(t *testing.T) {
	if os.Getenv("XALGORIX_HTTP_WORKER_INTEGRATION") != "1" {
		t.Skip("set XALGORIX_HTTP_WORKER_INTEGRATION=1 to run against Docker")
	}
	image := os.Getenv("XALGORIX_TEST_TOOLBOX_IMAGE")
	if image == "" {
		t.Fatal("XALGORIX_TEST_TOOLBOX_IMAGE is required")
	}
	t.Setenv("XALGORIX_ALLOW_UNPINNED_TOOLBOX", "1")
	dir := t.TempDir()
	socket := HTTPWorkerSocketPathForTest(dir)
	spec, err := BuildProject(ProjectOptions{
		InstanceID:       "http-worker-test",
		ScanID:           "http-worker-test",
		ScanDir:          dir,
		Image:            image,
		Profile:          ProfileCore,
		NetProfile:       NetProfileDirect,
		HTTPWorkerSocket: socket,
	})
	if err != nil {
		t.Fatal(err)
	}
	driver, err := NewDockerDriver(DockerConfig{Image: image, Profile: ProfileCore, InstanceID: "http-worker-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = driver.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := driver.EnsureProject(ctx, spec); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = driver.RemoveProject(cleanupCtx, spec.ProjectID)
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		if httpWorkerHealthyForTest(ctx, socket) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("http worker health never succeeded on %s", socket)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func HTTPWorkerSocketPathForTest(scanDir string) string {
	return filepath.Join(scanDir, ".xalgorix", "http-worker.sock")
}

func httpWorkerHealthyForTest(ctx context.Context, socket string) bool {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}
	client := &http.Client{Transport: tr, Timeout: time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://httpworker/v1/health", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}
