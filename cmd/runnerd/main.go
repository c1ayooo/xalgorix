// Command runnerd is the host-side container runtime service for xalgorix.
//
// It exists so the platform container never needs /var/run/docker.sock: runnerd
// owns the socket and exposes exactly the operations the executor needs
// (ensure/exec/stop/remove/inspect/list/capacity). It has no database access and
// no knowledge of scans — it is a pure, stateless execution endpoint.
//
// Run it on the host:
//
//	XALGORIX_TOOLBOX_IMAGE=ghcr.io/c1ayooo/xalgorix-toolbox:1.0.0-core@sha256:... \
//	  runnerd --socket /run/xalgorix/runner.sock
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	toolrt "github.com/c1ayooo/xalgorix/v4/internal/runtime"
)

var version = "dev"

func main() {
	var (
		socket = flag.String("socket", envOr("XALGORIX_RUNNERD_SOCKET", "/run/xalgorix/runner.sock"), "unix socket to listen on")
		image  = flag.String("image", os.Getenv("XALGORIX_TOOLBOX_IMAGE"), "toolbox image reference (digest-pinned in production)")
		prof   = flag.String("profile", envOr("XALGORIX_TOOLBOX_PROFILE", toolrt.ProfileCore), "toolbox profile: core|full|web")
		showV  = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showV {
		fmt.Println("runnerd", version)
		return
	}
	if strings.TrimSpace(*image) == "" {
		log.Fatal("runnerd: XALGORIX_TOOLBOX_IMAGE (or -image) is required")
	}

	instanceID := envOr("XALGORIX_RUNNERD_INSTANCE", toolrt.InstanceID())
	limit, _ := strconv.Atoi(os.Getenv("XALGORIX_MAX_PROJECTS"))
	concurrency, _ := strconv.Atoi(os.Getenv("XALGORIX_TOOLBOX_CONCURRENCY"))

	driver, err := toolrt.NewDockerDriver(toolrt.DockerConfig{
		SocketPath:   envOr("XALGORIX_DOCKER_SOCKET", "/var/run/docker.sock"),
		InstanceID:   instanceID,
		Image:        *image,
		Profile:      *prof,
		ProjectLimit: limit,
		ExecLimit:    concurrency,
	})
	if err != nil {
		log.Fatalf("runnerd: %v", err)
	}
	defer func() { _ = driver.Close() }()

	if err := os.MkdirAll(dirOf(*socket), 0o750); err != nil {
		log.Fatalf("runnerd: create socket dir: %v", err)
	}
	// A leftover socket from a previous crash would make Listen fail with
	// "address already in use"; removing it is safe because only one runnerd may
	// own this path.
	if info, statErr := os.Stat(*socket); statErr == nil && info.Mode()&os.ModeSocket != 0 {
		if conn, dialErr := net.DialTimeout("unix", *socket, 200*time.Millisecond); dialErr != nil {
			_ = os.Remove(*socket)
		} else {
			_ = conn.Close()
			log.Fatalf("runnerd: socket %s is already served by another process", *socket)
		}
	}
	listener, err := net.Listen("unix", *socket)
	if err != nil {
		log.Fatalf("runnerd: listen %s: %v", *socket, err)
	}
	if err := os.Chmod(*socket, 0o660); err != nil {
		log.Printf("runnerd: chmod socket: %v", err)
	}

	srv := &http.Server{
		Handler:           &toolrt.RunnerdHandler{Driver: driver},
		ReadHeaderTimeout: 10 * time.Second,
		// Exec calls stream for as long as the command runs; the per-command
		// deadline is enforced inside the container, so no server write timeout.
	}
	log.Printf("[runnerd] %s listening on %s (image=%s profile=%s instance=%s max_projects=%d concurrency=%d)",
		version, *socket, *image, *prof, instanceID, limit, concurrency)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		if serveErr := srv.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("runnerd: serve: %v", serveErr)
			stop()
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Printf("[runnerd] stopped; project containers are left for the platform to reconcile")
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return "."
}
