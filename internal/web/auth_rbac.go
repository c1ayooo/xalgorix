// Package web — auth_rbac.go enforces role-based access control for human
// dashboard sessions. The policy is deliberately a small declarative table
// (namespace prefixes × method shapes), following the precedent of
// machineTokenRouteDenylist (api_token.go) — no permission matrix, no
// per-route wiring that can drift.
//
// Policy:
//
//	admin    — everything.
//	operator — all operational actions (scans, tasks, projects, approvals,
//	           schedules, reports, chat…). Writes inside credential/settings
//	           namespaces are rejected; reads stay visible so the UI can
//	           render them.
//	viewer   — read-only methods only, anywhere (including settings reads —
//	           the API never returns secrets, only *_configured booleans).
//
// Machine Bearer tokens bypass this layer entirely: they are automation
// identities, not people, and keep their own denylist semantics
// (machineTokenPathAllowed).
package web

import "net/http"

// adminOnlyPrefixes are namespaces reserved for admins on ALL methods.
var adminOnlyPrefixes = []string{
	// Account management itself — non-admins never enumerate accounts.
	"/api/users",
}

// adminOnlyWritePrefixes are namespaces whose WRITE operations are reserved
// for admins. They carry credential material or executable definitions; reads
// stay visible so the UI can render them (the API never returns secrets):
//   - /api/settings         — provider API keys and environment secrets
//   - /api/auth/profiles    — LLM provider OAuth/API-key credentials
//   - /api/mcp-servers      — MCP definitions carry credential env/headers
//   - /api/skills           — executable definitions (arbitrary command lines)
//   - /api/agent-profiles   — executable specialist configurations
var adminOnlyWritePrefixes = []string{
	"/api/settings",
	"/api/auth/profiles",
	"/api/mcp-servers",
	"/api/skills",
	"/api/agent-profiles",
}

// selfServicePrefixes are state-changing endpoints every authenticated role
// may call on their own account. They are checked before the viewer
// read-only rule.
var selfServicePrefixes = []string{
	"/api/auth/logout",
	"/api/auth/password",
}

// writeMethod reports whether the HTTP method mutates state.
func writeMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// hasPathPrefix reports whether path is inside any of the namespace
// prefixes. Matching respects segment boundaries: /api/users matches
// /api/users and /api/users/u_1 but not /api/users2.
func hasPathPrefix(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if len(path) < len(p) || path[:len(p)] != p {
			continue
		}
		if len(path) == len(p) || path[len(p)] == '/' {
			return true
		}
	}
	return false
}

// authorizePath answers "may this role perform this method on this path?".
// It is evaluated only for authenticated human sessions on /api/* routes;
// public auth routes are handled earlier in the middleware.
func authorizePath(role, method, path string) bool {
	switch role {
	case RoleAdmin:
		return true
	case RoleViewer:
		// Self-service account actions (logout, own password) are allowed.
		if writeMethod(method) && hasPathPrefix(path, selfServicePrefixes) {
			return true
		}
		return !writeMethod(method)
	case RoleOperator:
		// Account management is fully admin-only.
		if hasPathPrefix(path, adminOnlyPrefixes) {
			return false
		}
		// Credential/settings namespaces: writes admin-only, reads visible.
		if writeMethod(method) && hasPathPrefix(path, adminOnlyWritePrefixes) {
			return false
		}
		return true
	default:
		// Unknown role claim: fail closed.
		return false
	}
}
