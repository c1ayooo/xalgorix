package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

// TestPlatformRoutesRegistered locks in the lockstep invariant between the
// mux registrations in Start and the dashboardRoutes slice: every platform
// pattern must be present exactly once so the routes inherit the same
// auth + CSRF + rate-limit middleware as the rest of /api/*.
func TestPlatformRoutesRegistered(t *testing.T) {
	want := []string{
		"/api/projects",
		"/api/projects/",
		"/api/tasks/",
	}
	counts := make(map[string]int, len(dashboardRoutes))
	for _, route := range dashboardRoutes {
		counts[route]++
	}
	for _, pattern := range want {
		if counts[pattern] != 1 {
			t.Errorf("dashboardRoutes contains %q %d time(s), want exactly 1", pattern, counts[pattern])
		}
	}
	// No pre-existing route was displaced by the platform wiring.
	for _, pattern := range []string{"/api/scan", "/api/scans", "/api/instances", "/api/findings", "/api/schedules"} {
		if counts[pattern] < 1 {
			t.Errorf("dashboardRoutes missing pre-existing entry %q", pattern)
		}
	}
}

// TestPlatformHandlersNilDependency preserves the 503 contract: without a
// platform store (construction/assembly failed), every platform endpoint
// surfaces 503 instead of panicking — the rest of the dashboard keeps serving.
func TestPlatformHandlersNilDependency(t *testing.T) {
	s := &Server{} // platformStore == nil

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"GET projects", http.MethodGet, "/api/projects"},
		{"POST projects", http.MethodPost, "/api/projects"},
		{"GET project", http.MethodGet, "/api/projects/some-id"},
		{"PATCH project", http.MethodPatch, "/api/projects/some-id"},
		{"DELETE project", http.MethodDelete, "/api/projects/some-id"},
		{"GET project tasks", http.MethodGet, "/api/projects/some-id/tasks"},
		{"POST project tasks", http.MethodPost, "/api/projects/some-id/tasks"},
		{"GET task", http.MethodGet, "/api/tasks/some-id"},
		{"PATCH task", http.MethodPatch, "/api/tasks/some-id"},
		{"DELETE task", http.MethodDelete, "/api/tasks/some-id"},
		{"POST task runs", http.MethodPost, "/api/tasks/some-id/runs"},
		{"GET task runs", http.MethodGet, "/api/tasks/some-id/runs"},
		{"GET task findings", http.MethodGet, "/api/tasks/some-id/findings"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
			if tc.method == http.MethodGet || tc.method == http.MethodDelete {
				req = httptest.NewRequest(tc.method, tc.path, nil)
			}
			switch {
			case strings.HasPrefix(tc.path, "/api/projects") && strings.HasSuffix(tc.path, "/tasks"):
				// handled below via handleProjectDetail
				s.handleProjectDetail(rr, req)
			case strings.HasPrefix(tc.path, "/api/projects"):
				if tc.path == "/api/projects" {
					s.handleProjects(rr, req)
				} else {
					s.handleProjectDetail(rr, req)
				}
			case strings.HasPrefix(tc.path, "/api/tasks/"):
				s.handleTaskDetail(rr, req)
			default:
				t.Fatalf("unrouted path %s", tc.path)
			}
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("code = %d, want 503; body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

// TestPlatformWiringConstruction verifies NewServer + file store wiring: the
// handlers serve real data once SetPlatformStore is called with a store rooted
// at the server's data dir.
func TestPlatformWiringConstruction(t *testing.T) {
	s := newTestServer(t, nil)
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPlatformStore(store)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	s.handleProjects(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Projects []map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}
	// The implicit default project materializes on first ensure — via the
	// API path it appears once created by the importer or a first POST. The
	// wiring test only asserts a decodable list envelope.
	if body.Projects == nil {
		t.Error("GET /api/projects must return a projects array (possibly empty)")
	}
}

// TestPlatformMethodGuards checks the 405 surface of the shared detail
// handlers when the store is present.
func TestPlatformMethodGuards(t *testing.T) {
	s := newTestServer(t, nil)
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPlatformStore(store)

	t.Run("PUT /api/projects → 405", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/projects", nil)
		s.handleProjects(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code = %d, want 405", rr.Code)
		}
	})
	t.Run("POST /api/tasks/{id}/findings → 405", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/some-id/findings", nil)
		s.handleTaskDetail(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code = %d, want 405", rr.Code)
		}
	})
	t.Run("PUT /api/tasks/{id} → 405", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/tasks/some-id", nil)
		s.handleTaskDetail(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code = %d, want 405", rr.Code)
		}
	})
}
