package web

// auth_jwt_test.go: the session-JWT layer (auth_jwt.go) — random secret
// persistence, HS256 sign/verify, expiry, alg confusion, jti revocation.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/config"
)

// First start generates a random secret, persists it 0600, and a second load
// returns the same secret so sessions survive restarts.
func TestJWTSecret_LoadOrCreatePersisted(t *testing.T) {
	dir := t.TempDir()

	secret1, err := loadOrCreateJWTSecret(dir)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	secret2, err := loadOrCreateJWTSecret(dir)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if string(secret1) != string(secret2) {
		t.Fatal("the persisted secret must be reloaded identically")
	}
	if len(secret1) < 32 {
		t.Fatalf("secret must have >=256 bits of entropy, got %d bytes", len(secret1))
	}
	path := filepath.Join(dir, "auth", jwtSecretFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("jwt key file missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("jwt key must be 0600, got %o", perm)
	}
	// Two independent data dirs must generate different secrets.
	other, err := loadOrCreateJWTSecret(t.TempDir())
	if err != nil {
		t.Fatalf("other load: %v", err)
	}
	if string(other) == string(secret1) {
		t.Fatal("each deployment must generate its own random secret")
	}
}

// Issue → verify roundtrip carries the identity claims.
func TestJWT_IssueVerifyRoundtrip(t *testing.T) {
	j := newJWTSessions([]byte("test-secret-at-least-32-bytes-long!!"))
	tok, _, err := j.issue("alice", RoleOperator, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := j.verify(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Username != "alice" || claims.Role != RoleOperator || claims.JTI == "" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if claims.ExpiresAt.IsZero() || time.Until(claims.ExpiresAt) <= 50*time.Minute {
		t.Fatalf("expiry must reflect the requested TTL, got %v", claims.ExpiresAt)
	}
}

// A token signed with a different secret is rejected.
func TestJWT_WrongSecretRejected(t *testing.T) {
	a := newJWTSessions([]byte("secret-a-32-bytes-aaaaaaaaaaaaaaaa"))
	b := newJWTSessions([]byte("secret-b-32-bytes-bbbbbbbbbbbbbbbb"))
	tok, _, err := a.issue("alice", RoleViewer, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := b.verify(tok); err == nil {
		t.Fatal("a token from another secret must not verify")
	}
}

// Expired tokens are rejected even with a valid signature.
func TestJWT_ExpiredRejected(t *testing.T) {
	j := newJWTSessions([]byte("test-secret-at-least-32-bytes-long!!"))
	tok, _, err := j.issue("alice", RoleViewer, -time.Minute)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := j.verify(tok); err == nil {
		t.Fatal("an expired token must not verify")
	}
}

// Garbage and tampered tokens never verify.
func TestJWT_TamperedAndGarbageRejected(t *testing.T) {
	j := newJWTSessions([]byte("test-secret-at-least-32-bytes-long!!"))
	tok, _, err := j.issue("alice", RoleViewer, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	for name, bad := range map[string]string{
		"garbage":      "not-a-token",
		"empty":        "",
		"tampered-sub": tok[:strings.LastIndex(tok, ".")] + ".forged-signature",
		"swapped-payload": func() string {
			parts := strings.Split(tok, ".")
			// Sign a token for bob and splice its payload into alice's token.
			other, _, _ := j.issue("bob", RoleViewer, time.Hour)
			otherParts := strings.Split(other, ".")
			return parts[0] + "." + otherParts[1] + "." + parts[2]
		}(),
	} {
		if _, err := j.verify(bad); err == nil {
			t.Fatalf("%s must not verify", name)
		}
	}
}

// Logout revocation: the jti denylist kills the token immediately; another
// token from the same issuer stays valid.
func TestJWT_RevokeJTI(t *testing.T) {
	j := newJWTSessions([]byte("test-secret-at-least-32-bytes-long!!"))
	tok1, c1, _ := j.issue("alice", RoleViewer, time.Hour)
	tok2, _, _ := j.issue("alice", RoleViewer, time.Hour)

	j.revoke(c1.JTI, c1.ExpiresAt)
	if _, err := j.verify(tok1); err == nil {
		t.Fatal("a revoked token must not verify")
	}
	if _, err := j.verify(tok2); err != nil {
		t.Fatalf("an unrelated token must stay valid: %v", err)
	}
}

// The middleware 401s on an expired/revoked cookie and 403s on RBAC denials,
// with the claims reaching the handler context on success.
func TestAuthMiddleware_JWTSessionAndRBAC(t *testing.T) {
	resetAuthSessionsForTest()
	s := newTestServer(t, &config.Config{Username: "admin", Password: "secret"})
	h := authMiddleware(s)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if claims, ok := sessionClaimsFrom(r); ok {
			_, _ = w.Write([]byte("role=" + claims.Role))
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))

	// admin token: GET passes and carries claims.
	tok, _, err := s.authJWT.issue("admin", RoleAdmin, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	req.Header.Set("Origin", "http://x.test")
	req.Host = "x.test"
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tok})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "role=admin") {
		t.Fatalf("admin session must reach the handler with claims, got %d %q", w.Code, w.Body.String())
	}

	// viewer token: a state-changing method is RBAC-blocked with 403.
	vtok, _, err := s.authJWT.issue("vie", RoleViewer, time.Hour)
	if err != nil {
		t.Fatalf("issue viewer: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/scan", nil)
	req.Header.Set("Origin", "http://x.test")
	req.Host = "x.test"
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: vtok})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "viewer") {
		t.Fatalf("viewer POST must be RBAC-403, got %d %q", w.Code, w.Body.String())
	}

	// Revoking the viewer's jti turns the same request into 401 (no session).
	tokRevoked, rclaims, _ := s.authJWT.issue("vie", RoleViewer, time.Hour)
	s.authJWT.revoke(rclaims.JTI, rclaims.ExpiresAt)
	req = httptest.NewRequest(http.MethodPost, "/api/scan", nil)
	req.Header.Set("Origin", "http://x.test")
	req.Host = "x.test"
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tokRevoked})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session must 401, got %d", w.Code)
	}
}
