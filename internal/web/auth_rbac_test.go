package web

// auth_rbac_test.go: the declarative role × method × path policy table
// (auth_rbac.go).

import "testing"

// TestAuthorizePath_Matrix walks the policy table itself.
func TestAuthorizePath_Matrix(t *testing.T) {
	type cell struct {
		role, method, path string
		want               bool
	}
	cells := []cell{
		// Admin is unconditional.
		{RoleAdmin, "GET", "/api/scans", true},
		{RoleAdmin, "POST", "/api/scan", true},
		{RoleAdmin, "DELETE", "/api/users/u_abc", true},
		{RoleAdmin, "POST", "/api/settings/llm/keys", true},

		// Viewer: read-only methods anywhere.
		{RoleViewer, "GET", "/api/scans", true},
		{RoleViewer, "GET", "/api/settings/environment", true},
		{RoleViewer, "GET", "/api/stream", true},
		{RoleViewer, "POST", "/api/scan", false},
		{RoleViewer, "DELETE", "/api/scans/abc", false},
		{RoleViewer, "PATCH", "/api/tasks/t1", false},

		// Viewer self-service: logout + own password change.
		{RoleViewer, "POST", "/api/auth/logout", true},
		{RoleViewer, "POST", "/api/auth/password", true},
		// …but not other auth surfaces.
		{RoleViewer, "POST", "/api/auth/login", false},

		// Operator: business writes allowed, credential/settings namespaces not.
		{RoleOperator, "POST", "/api/scan", true},
		{RoleOperator, "POST", "/api/tasks", true},
		{RoleOperator, "DELETE", "/api/scans/abc", true},
		{RoleOperator, "POST", "/api/projects/p1/runs", true},
		{RoleOperator, "GET", "/api/settings/environment", true},
		{RoleOperator, "GET", "/api/users", false},
		{RoleOperator, "POST", "/api/users", false},
		{RoleOperator, "POST", "/api/settings/llm/keys", false},
		{RoleOperator, "POST", "/api/auth/profiles/api-key", false},
		{RoleOperator, "POST", "/api/mcp-servers", false},
		{RoleOperator, "POST", "/api/skills", false},
		{RoleOperator, "PATCH", "/api/agent-profiles/x", false},
		// …but reads of those namespaces stay visible for the UI.
		{RoleOperator, "GET", "/api/mcp-servers", true},
		{RoleOperator, "GET", "/api/skills", true},

		// Unknown role fails closed.
		{"", "GET", "/api/scans", false},
		{"superuser", "GET", "/api/scans", false},
	}
	for _, c := range cells {
		if got := authorizePath(c.role, c.method, c.path); got != c.want {
			t.Errorf("authorizePath(%q, %q, %q) = %v, want %v", c.role, c.method, c.path, got, c.want)
		}
	}
}

// writeMethod must classify every mutating verb.
func TestWriteMethod(t *testing.T) {
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		if !writeMethod(m) {
			t.Errorf("writeMethod(%q) = false", m)
		}
	}
	for _, m := range []string{"GET", "HEAD", "OPTIONS"} {
		if writeMethod(m) {
			t.Errorf("writeMethod(%q) = true", m)
		}
	}
}

// Prefix matching must not match partial segments ("/api/users2" is not the
// users namespace).
func TestHasPathPrefix_SegmentBoundary(t *testing.T) {
	if !hasPathPrefix("/api/users/u_1", []string{"/api/users"}) {
		t.Fatal("subpath must match its namespace prefix")
	}
	if hasPathPrefix("/api/users2", []string{"/api/users"}) {
		t.Fatal("a longer sibling segment must not match the prefix")
	}
	if !hasPathPrefix("/api/users", []string{"/api/users"}) {
		t.Fatal("exact match must pass")
	}
}
