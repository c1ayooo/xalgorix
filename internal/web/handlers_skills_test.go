package web

// User-skill handler tests (API_CONTRACT §integration-skills): route
// lockstep, nil-store 503, and the CRUD wire contract including validation
// and duplicate-name 409.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

func newSkillsTestServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t, nil)
	store, err := platform.NewFileStore(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPlatformStore(store)
	return s
}

// TestSkillRoutesRegistered extends the lockstep invariant to /api/skills.
func TestSkillRoutesRegistered(t *testing.T) {
	want := []string{"/api/skills", "/api/skills/"}
	counts := make(map[string]int, len(dashboardRoutes))
	for _, route := range dashboardRoutes {
		counts[route]++
	}
	for _, pattern := range want {
		if counts[pattern] != 1 {
			t.Errorf("dashboardRoutes contains %q %d time(s), want exactly 1", pattern, counts[pattern])
		}
	}
}

// TestSkillHandlersNilStore503 preserves the platform 503 posture.
func TestSkillHandlersNilStore503(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		name string
		call func(w http.ResponseWriter, r *http.Request)
	}{
		{"GET skills", s.handleSkills},
		{"POST skills", s.handleSkills},
		{"GET skill detail", s.handleSkillDetail},
		{"PATCH skill detail", s.handleSkillDetail},
		{"DELETE skill detail", s.handleSkillDetail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/skills/x", strings.NewReader(`{}`))
			tc.call(rr, req)
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("code = %d, want 503; body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

// TestSkillHandlersContract walks the full CRUD wire contract.
func TestSkillHandlersContract(t *testing.T) {
	s := newSkillsTestServer(t)

	// Create.
	createBody := `{"name":"testing-price-tampering","description":"Prove a manipulated price is accepted","tags":["payment"],"intent":"offensive","body":"# Price tampering\n\n1. Replay…","enabled":true}`
	rr := httptest.NewRecorder()
	s.handleSkills(rr, httptest.NewRequest(http.MethodPost, "/api/skills", strings.NewReader(createBody)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var created platform.Skill
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Category != platform.SkillCategoryUser || created.CreatedBy == "" {
		t.Fatalf("created skill wrong: %+v", created)
	}

	// Duplicate name → 409.
	rr = httptest.NewRecorder()
	s.handleSkills(rr, httptest.NewRequest(http.MethodPost, "/api/skills", strings.NewReader(createBody)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("duplicate = %d, want 409", rr.Code)
	}

	// Validation failure → 400.
	rr = httptest.NewRecorder()
	s.handleSkills(rr, httptest.NewRequest(http.MethodPost, "/api/skills", strings.NewReader(`{"name":"Not Kebab!","description":"d","body":"b"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid name = %d, want 400", rr.Code)
	}

	// List.
	rr = httptest.NewRecorder()
	s.handleSkills(rr, httptest.NewRequest(http.MethodGet, "/api/skills", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d", rr.Code)
	}
	var list struct {
		Skills []platform.Skill `json:"skills"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Skills) != 1 || list.Skills[0].Name != "testing-price-tampering" {
		t.Fatalf("list wrong: %+v", list.Skills)
	}

	// PATCH: disable.
	rr = httptest.NewRecorder()
	s.handleSkillDetail(rr, httptest.NewRequest(http.MethodPatch, "/api/skills/"+created.ID, strings.NewReader(`{"enabled":false}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rr.Code, rr.Body.String())
	}
	var patched platform.Skill
	if err := json.Unmarshal(rr.Body.Bytes(), &patched); err != nil {
		t.Fatal(err)
	}
	if patched.Enabled {
		t.Fatalf("patch did not disable: %+v", patched)
	}

	// GET detail.
	rr = httptest.NewRecorder()
	s.handleSkillDetail(rr, httptest.NewRequest(http.MethodGet, "/api/skills/"+created.ID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("get detail = %d", rr.Code)
	}

	// Unknown id → 404.
	rr = httptest.NewRecorder()
	s.handleSkillDetail(rr, httptest.NewRequest(http.MethodGet, "/api/skills/nope", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown id = %d, want 404", rr.Code)
	}

	// DELETE then 404 on second touch.
	rr = httptest.NewRecorder()
	s.handleSkillDetail(rr, httptest.NewRequest(http.MethodDelete, "/api/skills/"+created.ID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete = %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	s.handleSkillDetail(rr, httptest.NewRequest(http.MethodDelete, "/api/skills/"+created.ID, nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("double delete = %d, want 404", rr.Code)
	}

	// Path traversal guard: ids containing "/" are rejected as not-found.
	rr = httptest.NewRecorder()
	s.handleSkillDetail(rr, httptest.NewRequest(http.MethodGet, "/api/skills/a/b", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("nested path = %d, want 404", rr.Code)
	}
}

// TestBuiltinSkillHandlers pins the read-only corpus projection: the list is
// served without a platform store, detail returns the markdown body with
// frontmatter stripped, and unknown names are 404.
func TestBuiltinSkillHandlers(t *testing.T) {
	s := newTestServer(t, nil) // no platform store: corpus is compiled in

	rr := httptest.NewRecorder()
	s.handleBuiltinSkills(rr, httptest.NewRequest(http.MethodGet, "/api/skills/builtin", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rr.Code, rr.Body.String())
	}
	var list struct {
		Skills []struct {
			Name     string   `json:"name"`
			Category string   `json:"category"`
			Tags     []string `json:"tags"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Skills) < 100 {
		t.Fatalf("builtin list = %d rows, want the embedded corpus", len(list.Skills))
	}
	first := list.Skills[0]

	rr = httptest.NewRecorder()
	s.handleBuiltinSkillDetail(rr, httptest.NewRequest(http.MethodGet,
		"/api/skills/builtin/"+first.Name+"?category="+first.Category, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("detail = %d: %s", rr.Code, rr.Body.String())
	}
	var detail struct {
		Name string `json:"name"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Name != first.Name || strings.TrimSpace(detail.Body) == "" {
		t.Fatalf("detail wrong: name=%q bodyLen=%d", detail.Name, len(detail.Body))
	}
	if strings.HasPrefix(strings.TrimSpace(detail.Body), "---") {
		t.Fatal("detail body still carries frontmatter")
	}

	// Unknown name → 404; nested path → 404; non-GET → 405.
	for _, tc := range []struct {
		name     string
		method   string
		path     string
		wantCode int
		call     func(w http.ResponseWriter, r *http.Request)
	}{
		{"unknown", http.MethodGet, "/api/skills/builtin/nope-not-a-skill", http.StatusNotFound, s.handleBuiltinSkillDetail},
		{"nested", http.MethodGet, "/api/skills/builtin/a/b", http.StatusNotFound, s.handleBuiltinSkillDetail},
		{"post", http.MethodPost, "/api/skills/builtin", http.StatusMethodNotAllowed, s.handleBuiltinSkills},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			tc.call(rr, httptest.NewRequest(tc.method, tc.path, nil))
			if rr.Code != tc.wantCode {
				t.Fatalf("%s code = %d, want %d", tc.name, rr.Code, tc.wantCode)
			}
		})
	}
}
