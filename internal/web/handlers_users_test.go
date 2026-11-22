package web

// handlers_users_test.go: contract coverage for the admin account API
// (handlers_users.go), the setup bootstrap (auth_session.go) and route
// registration lockstep — following the httptest-direct-handler conventions
// of handlers_approval_rules_test.go.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/config"
)

// claimsRequest attaches verified session claims and a JSON body to a bare
// request — the unit-test stand-in for what authMiddleware does in
// production. body may be nil.
func claimsRequest(t *testing.T, method, path string, claims *sessionClaims, body string) *http.Request {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if claims != nil {
		r = r.WithContext(withSessionClaims(r.Context(), claims))
	}
	return r
}

func future() time.Time { return time.Now().Add(time.Hour) }

func adminClaims() *sessionClaims {
	return &sessionClaims{Username: "root", Role: RoleAdmin, JTI: "jti-admin", ExpiresAt: future()}
}

func viewerClaims() *sessionClaims {
	return &sessionClaims{Username: "vie", Role: RoleViewer, JTI: "jti-viewer", ExpiresAt: future()}
}

func operatorClaims() *sessionClaims {
	return &sessionClaims{Username: "op", Role: RoleOperator, JTI: "jti-op", ExpiresAt: future()}
}

// newUsersTestServer builds a server with a live auth stack.
func newUsersTestServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t, &config.Config{})
	if s.authUsers == nil || s.authJWT == nil {
		t.Fatal("auth stack must initialize")
	}
	return s
}

// The users API requires admin claims: anonymous (machine-token/open-mode
// context) and non-admin roles are rejected with 403.
func TestUsersHandlers_RoleGate(t *testing.T) {
	s := newUsersTestServer(t)

	rr := httptest.NewRecorder()
	s.handleUsers(rr, httptest.NewRequest(http.MethodGet, "/api/users", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("claims-less GET /api/users must be 403, got %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	s.handleUsers(rr, claimsRequest(t, http.MethodGet, "/api/users", viewerClaims(), ""))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("viewer GET /api/users must be 403, got %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	s.handleUserDetail(rr, claimsRequest(t, http.MethodDelete, "/api/users/u_x", operatorClaims(), ""))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("operator DELETE /api/users/{id} must be 403, got %d", rr.Code)
	}
}

// Full admin CRUD contract: create → list → patch (role + password) → delete.
func TestUsersHandlers_AdminCRUDContract(t *testing.T) {
	s := newUsersTestServer(t)

	// Create.
	rr := httptest.NewRecorder()
	s.handleUsers(rr, claimsRequest(t, http.MethodPost, "/api/users", adminClaims(),
		`{"username":"op1","password":"password123","role":"operator"}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%q", rr.Code, rr.Body.String())
	}
	var created User
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.PasswordHash != "" {
		t.Fatal("API responses must never include the password hash")
	}

	// Validation failures.
	rr = httptest.NewRecorder()
	s.handleUsers(rr, claimsRequest(t, http.MethodPost, "/api/users", adminClaims(), `{"username":"op1"}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("empty-password create must 400, got %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	s.handleUsers(rr, claimsRequest(t, http.MethodPost, "/api/users", adminClaims(),
		`{"username":"op1","password":"password123","role":"viewer"}`))
	if rr.Code != http.StatusConflict {
		t.Fatalf("duplicate username must 409, got %d", rr.Code)
	}

	// List.
	rr = httptest.NewRecorder()
	s.handleUsers(rr, claimsRequest(t, http.MethodGet, "/api/users", adminClaims(), ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d", rr.Code)
	}
	var users []User
	if err := json.Unmarshal(rr.Body.Bytes(), &users); err != nil || len(users) != 1 {
		t.Fatalf("list must return the created user, got %s err=%v", rr.Body.String(), err)
	}

	// Patch: demote + reset password.
	rr = httptest.NewRecorder()
	s.handleUserDetail(rr, claimsRequest(t, http.MethodPatch, "/api/users/"+created.ID, adminClaims(),
		`{"role":"viewer","password":"newpassword9"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%q", rr.Code, rr.Body.String())
	}
	// Hash rotation actually took effect.
	if _, ok := s.authUsers.authenticate("op1", "newpassword9"); !ok {
		t.Fatal("the reset password must authenticate")
	}
	if u, _ := s.authUsers.get(created.ID); u.Role != RoleViewer {
		t.Fatalf("role patch must persist, got %q", u.Role)
	}

	// Delete.
	rr = httptest.NewRecorder()
	s.handleUserDetail(rr, claimsRequest(t, http.MethodDelete, "/api/users/"+created.ID, adminClaims(), ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rr.Code)
	}
	if len(s.authUsers.list()) != 0 {
		t.Fatal("delete must remove the account")
	}

	// 404 afterwards.
	rr = httptest.NewRecorder()
	s.handleUserDetail(rr, claimsRequest(t, http.MethodPatch, "/api/users/"+created.ID, adminClaims(), `{}`))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("patch on deleted user must 404, got %d", rr.Code)
	}
}

// A bare &Server{} (auth stack nil) surfaces 503 rather than panicking.
func TestUsersHandlers_NilStore503(t *testing.T) {
	s := &Server{}
	rr := httptest.NewRecorder()
	s.handleUsers(rr, claimsRequest(t, http.MethodGet, "/api/users", adminClaims(), ""))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil-store GET /api/users must 503, got %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	s.handleUserDetail(rr, claimsRequest(t, http.MethodPatch, "/api/users/u_1", adminClaims(), `{}`))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil-store PATCH must 503, got %d", rr.Code)
	}
}

// Setup bootstrap: allowed exactly once, then dead.
func TestAuthSetup_BootstrapOnce(t *testing.T) {
	s := newUsersTestServer(t)

	// Before setup, status advertises needs_setup.
	rr := httptest.NewRecorder()
	s.handleAuthStatus(rr, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if !strings.Contains(rr.Body.String(), `"needs_setup":true`) {
		t.Fatalf("empty store must advertise needs_setup: %s", rr.Body.String())
	}

	// Create the first admin.
	rr = httptest.NewRecorder()
	s.handleAuthSetup(rr, httptest.NewRequest(http.MethodPost, "/api/auth/setup",
		strings.NewReader(`{"username":"root","password":"password123"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("setup status = %d body=%q", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Header().Get("Set-Cookie"), sessionCookieName) {
		t.Fatal("setup must log the operator in (session cookie)")
	}

	// A second attempt is rejected even with valid-shaped input.
	rr = httptest.NewRecorder()
	s.handleAuthSetup(rr, httptest.NewRequest(http.MethodPost, "/api/auth/setup",
		strings.NewReader(`{"username":"other","password":"password123"}`)))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("second setup must be 403, got %d", rr.Code)
	}

	// Status no longer advertises setup.
	rr = httptest.NewRecorder()
	s.handleAuthStatus(rr, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if strings.Contains(rr.Body.String(), `"needs_setup":true`) {
		t.Fatalf("needs_setup must flip false after setup: %s", rr.Body.String())
	}
}

// Setup is refused when env credentials are configured (the seed owns the
// first admin).
func TestAuthSetup_RefusedWhenEnvCredsConfigured(t *testing.T) {
	s := newTestServer(t, &config.Config{Username: "envop", Password: "envpassword1"})
	rr := httptest.NewRecorder()
	s.handleAuthSetup(rr, httptest.NewRequest(http.MethodPost, "/api/auth/setup",
		strings.NewReader(`{"username":"root","password":"password123"}`)))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("setup with env creds configured must be 403, got %d", rr.Code)
	}
}

// Self-service password change requires the current password and works for
// any role.
func TestAuthPassword_SelfService(t *testing.T) {
	s := newUsersTestServer(t)
	if _, err := s.authUsers.create("vie", "oldpassword1", RoleViewer); err != nil {
		t.Fatalf("create viewer: %v", err)
	}

	rr := httptest.NewRecorder()
	s.handleAuthPassword(rr, claimsRequest(t, http.MethodPost, "/api/auth/password", viewerClaims(),
		`{"current_password":"oldpassword1","new_password":"newpassword1"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("self-service change status = %d body=%q", rr.Code, rr.Body.String())
	}
	if _, ok := s.authUsers.authenticate("vie", "newpassword1"); !ok {
		t.Fatal("the new password must authenticate")
	}

	// Wrong current password → 400.
	rr = httptest.NewRecorder()
	s.handleAuthPassword(rr, claimsRequest(t, http.MethodPost, "/api/auth/password", viewerClaims(),
		`{"current_password":"nope","new_password":"anotherpass1"}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("wrong current password must 400, got %d", rr.Code)
	}

	// Claims-less requests are rejected.
	rr = httptest.NewRecorder()
	s.handleAuthPassword(rr, httptest.NewRequest(http.MethodPost, "/api/auth/password",
		strings.NewReader(`{}`)))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("claims-less password change must 401, got %d", rr.Code)
	}
}

// Route registration lockstep: the users/auth routes are in dashboardRoutes.
func TestUsersRoutesRegistered(t *testing.T) {
	want := []string{"/api/users", "/api/users/", "/api/auth/setup", "/api/auth/password"}
	for _, pattern := range want {
		found := false
		for _, p := range dashboardRoutes {
			if p == pattern {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("dashboardRoutes must contain %q (lockstep with mux registration)", pattern)
		}
	}
}
