// Package web — handlers_users.go implements the admin-only account
// management API (RBAC, auth_rbac.go):
//
//	GET    /api/users        → list accounts (never includes password hashes)
//	POST   /api/users        → create {username, password, role}
//	PATCH  /api/users/{id}   → update {role?, disabled?, password?}
//	DELETE /api/users/{id}   → delete
//
// Authorization runs twice: the middleware's authorizePath (namespace-level,
// auth_rbac.go) and a claims check here (defense in depth for handlers that
// are reachable in unit tests directly). The last-enabled-admin invariant is
// enforced inside the store (auth_users.go) so every path — including future
// callers — shares one guard.
package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// requireAdminClaims returns the verified session claims when they belong to
// an enabled admin. Machine-token and open-mode requests carry no claims and
// are rejected — account management is a human-admin surface.
func requireAdminClaims(r *http.Request) (*sessionClaims, bool) {
	claims, ok := sessionClaimsFrom(r)
	if !ok || claims.Role != RoleAdmin || claims.Username == "" {
		return nil, false
	}
	return claims, true
}

// writeUserStoreError maps store sentinels to HTTP statuses.
func writeUserStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUserValidation):
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, errUserConflict):
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, errUserNotFound):
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "user not found"})
	case errors.Is(err, errLastAdmin):
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		log.Printf("[auth] user store error: %v", err)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "user store error"})
	}
}

// handleUsers serves GET /api/users (list) and POST /api/users (create).
func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if _, ok := requireAdminClaims(r); !ok {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": "Admin role required"})
		return
	}
	if s.authUsers == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "Auth store unavailable"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSONStatus(w, http.StatusOK, s.authUsers.list())
	case http.MethodPost:
		limitJSONBody(w, r)
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Role     string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
			return
		}
		u, err := s.authUsers.create(strings.TrimSpace(req.Username), req.Password, req.Role)
		if err != nil {
			writeUserStoreError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusCreated, u)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleUserDetail serves PATCH /api/users/{id} and DELETE /api/users/{id}.
func (s *Server) handleUserDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if _, ok := requireAdminClaims(r); !ok {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": "Admin role required"})
		return
	}
	if s.authUsers == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "Auth store unavailable"})
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/users/")
	if id == "" || strings.Contains(id, "/") {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}

	switch r.Method {
	case http.MethodPatch:
		limitJSONBody(w, r)
		var req struct {
			Role     *string `json:"role"`
			Disabled *bool   `json:"disabled"`
			Password *string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
			return
		}
		u, err := s.authUsers.update(id, func(mu *User) error {
			if req.Role != nil {
				if !validRole(*req.Role) {
					return errInvalidRole()
				}
				mu.Role = *req.Role
			}
			if req.Disabled != nil {
				mu.Disabled = *req.Disabled
			}
			if req.Password != nil {
				if len(*req.Password) < minPasswordLength || len(*req.Password) > maxBcryptPassword {
					return errInvalidPassword()
				}
				hash, herr := hashPassword(*req.Password)
				if herr != nil {
					return herr
				}
				mu.PasswordHash = hash
			}
			return nil
		})
		if err != nil {
			writeUserStoreError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusOK, u)
	case http.MethodDelete:
		if err := s.authUsers.delete(id); err != nil {
			writeUserStoreError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// errInvalidRole / errInvalidPassword shape the store's validation sentinel
// with a field-specific message (errors.Is matches errUserValidation → 400).
func errInvalidRole() error {
	return fmt.Errorf("%w: role must be one of admin|operator|viewer", errUserValidation)
}

func errInvalidPassword() error {
	return fmt.Errorf("%w: password must be 8-72 characters", errUserValidation)
}
