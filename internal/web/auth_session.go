package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	cryptorand "crypto/rand"
	"encoding/hex"
)

const sessionCookieName = "xalgorix_session"

// generateSessionToken creates a cryptographically random session token.
// 32 bytes of crypto/rand is already overwhelmingly sufficient entropy —
// hashing it wouldn't add security and only obscured the source.
// Returns an error if the system entropy source is unavailable instead of
// terminating the whole process — callers should surface a 500 to the user.
// Used for JWT jti values and user ids as well.
func generateSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := cryptorand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand unavailable: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// loginAttempts tracks failed-login backoff per source IP. We replaced the
// unconditional time.Sleep(1s) on every failure because it held an HTTP
// connection open and let an attacker tie up worker goroutines with one IP.
// Instead, we reject further attempts from an IP that has racked up too many
// failures within a short window; legitimate users on a clean IP see no
// latency hit.
var (
	loginAttempts   = make(map[string]*loginAttempt)
	loginAttemptsMu sync.Mutex
)

type loginAttempt struct {
	failures  int
	firstFail time.Time
	lockUntil time.Time
}

// Login-limiter defaults. Every threshold is operator-tunable via env (see
// loginRateLimitDisabled / loginEnvMinutes / loginEnvInt), and
// XALGORIX_LOGIN_RATE_LIMIT=off disables the limiter entirely.
const (
	defaultLoginWindow   = 15 * time.Minute
	defaultLoginMaxFails = 10
	defaultLoginLockFor  = 5 * time.Minute
)

// loginRateLimitDisabled reports whether the per-IP login limiter is fully
// disabled via XALGORIX_LOGIN_RATE_LIMIT=off (accepted spellings: off,
// disabled, false, 0, no). Intended for deployments where the login endpoint
// is not reachable from the public internet (the scanner sits behind a
// network-level allowlist or a reverse-proxy gate), or where a pooled
// reverse-proxy IP can otherwise accumulate unrelated clients' failures and
// lock out legitimate automation. Settings are read per attempt, so
// configuration changes apply on the next login without a restart.
func loginRateLimitDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("XALGORIX_LOGIN_RATE_LIMIT"))) {
	case "off", "disabled", "false", "0", "no":
		return true
	}
	return false
}

// loginEnvMinutes reads a positive integer minutes value from env with a
// default; missing, unparseable, or non-positive values fall back to the
// default rather than disabling the limiter by accident.
func loginEnvMinutes(name string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return time.Duration(n) * time.Minute
}

// loginEnvInt reads a positive integer from env with a default; missing,
// unparseable, or non-positive values fall back to the default.
func loginEnvInt(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// loginIsLocked returns (locked, retryAfterSeconds). It also garbage-collects
// stale entries opportunistically so the map cannot grow unbounded.
func loginIsLocked(ip string) (bool, int) {
	if loginRateLimitDisabled() {
		return false, 0
	}
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()
	now := time.Now()
	window := loginEnvMinutes("XALGORIX_LOGIN_WINDOW_MINUTES", defaultLoginWindow)
	// Opportunistic GC — bounded work, runs only when this IP is queried.
	for k, v := range loginAttempts {
		if now.Sub(v.firstFail) > window && now.After(v.lockUntil) {
			delete(loginAttempts, k)
		}
	}
	a := loginAttempts[ip]
	if a == nil {
		return false, 0
	}
	if now.Before(a.lockUntil) {
		return true, int(a.lockUntil.Sub(now).Seconds()) + 1
	}
	return false, 0
}

// loginRecordFailure increments the failure counter for an IP. After
// XALGORIX_LOGIN_MAX_FAILURES (default 10) within
// XALGORIX_LOGIN_WINDOW_MINUTES (default 15), subsequent attempts are locked
// out for XALGORIX_LOGIN_LOCKOUT_MINUTES (default 5). With
// XALGORIX_LOGIN_RATE_LIMIT=off nothing is recorded at all.
func loginRecordFailure(ip string) {
	if loginRateLimitDisabled() {
		return
	}
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()
	now := time.Now()
	window := loginEnvMinutes("XALGORIX_LOGIN_WINDOW_MINUTES", defaultLoginWindow)
	a := loginAttempts[ip]
	if a == nil || now.Sub(a.firstFail) > window {
		loginAttempts[ip] = &loginAttempt{failures: 1, firstFail: now}
		return
	}
	a.failures++
	if a.failures >= loginEnvInt("XALGORIX_LOGIN_MAX_FAILURES", defaultLoginMaxFails) {
		a.lockUntil = now.Add(loginEnvMinutes("XALGORIX_LOGIN_LOCKOUT_MINUTES", defaultLoginLockFor))
	}
}

// loginRecordSuccess clears any failure history on a successful login.
func loginRecordSuccess(ip string) {
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()
	delete(loginAttempts, ip)
}

// clientIP extracts a comparable client identifier from the request. We
// intentionally do not trust X-Forwarded-For; if you put xalgorix behind a
// reverse proxy you should bind it to loopback and let the proxy enforce
// auth, or extend this helper to honor a configured trusted-proxy list.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isCSRFSafe returns true when a state-changing request is verifiably
// originated from this site. We use Origin (and Referer as a fallback)
// because every modern browser sends one of them on POST/PUT/PATCH/DELETE.
// SameSite=Strict on the session cookie already blocks the most common CSRF
// vectors; this is defense in depth for the Sec-Fetch-Site and
// document-form-submit edge cases.
//
// Policy:
//   - Safe methods (GET/HEAD/OPTIONS) are always allowed.
//   - Sec-Fetch-Site: same-origin/none → allow; same-site/cross-site → deny.
//   - Else fall back to Origin/Referer host == r.Host.
//   - If none of the above are present AND the request carries our session
//     cookie, the request looks like a browser navigation without the
//     metadata we expected — refuse. Cookie-less non-browser clients
//     (curl, scripts) are still allowed; an attacker has no way to forge
//     a cookie on the victim, so allowing cookie-less requests is safe.
func isCSRFSafe(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}

	// Browser hint: only "same-origin"/"none" are safe.
	switch strings.ToLower(r.Header.Get("Sec-Fetch-Site")) {
	case "":
		// fall through to Origin/Referer checks
	case "same-origin", "none":
		return true
	default:
		// "same-site" or "cross-site" — refuse.
		return false
	}

	// Compare Origin/Referer host with request Host.
	check := func(raw string) (bool, bool) {
		if raw == "" {
			return false, false
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return false, true
		}
		return u.Host == r.Host, true
	}

	if ok, present := check(r.Header.Get("Origin")); present {
		return ok
	}
	if ok, present := check(r.Header.Get("Referer")); present {
		return ok
	}

	// Neither Origin nor Referer nor Sec-Fetch-Site present.
	// If the client carries our session cookie this is suspicious (a real
	// browser strips none of these on cookie-bearing POSTs in 2026) —
	// refuse. Cookie-less requests are non-browser tooling, allow.
	if _, err := r.Cookie(sessionCookieName); err == nil {
		return false
	}
	return true
}

// ── Session identity plumbing ──────────────────────────────────────────────

// sessionClaimsKey is the request-context key carrying the verified session
// claims (auth_jwt.go) for downstream handlers (RBAC decisions in
// handlers_users.go, self-service password change).
type sessionCtxKey int

const sessionClaimsKey sessionCtxKey = 0

// withSessionClaims attaches verified claims to a request context.
func withSessionClaims(ctx context.Context, c *sessionClaims) context.Context {
	return context.WithValue(ctx, sessionClaimsKey, c)
}

// sessionClaimsFrom returns the verified claims of an authenticated human
// session, or ok=false for machine-token/open routes.
func sessionClaimsFrom(r *http.Request) (*sessionClaims, bool) {
	c, ok := r.Context().Value(sessionClaimsKey).(*sessionClaims)
	return c, c != nil && ok
}

// authConfigured returns true when the server can authenticate anyone:
// at least one stored dashboard account (users.json; the env-configured
// operator is seeded into it at startup), OR at least one machine API token
// (a valid headless deployment — API access via Bearer, no human login).
// When nothing is configured the authMiddleware short-circuits and serves
// all routes — used by the bind-time safety check to refuse external
// interfaces without auth.
func (s *Server) authConfigured() bool {
	if s.authUsers != nil && s.authUsers.hasUsers() {
		return true
	}
	cfg := s.cfg
	if cfg == nil {
		return false
	}
	return len(apiTokenHashes(cfg.APITokens)) > 0
}

// needsSetup reports whether the operator still has to create the first
// admin account: no users exist AND no env credentials are configured to
// seed one. Only then does POST /api/auth/setup accept a bootstrap admin.
func (s *Server) needsSetup() bool {
	if s.authUsers == nil {
		return false
	}
	if !s.authUsers.empty() {
		return false
	}
	cfg := s.cfg
	if cfg == nil {
		return true
	}
	// Env credentials configured → the startup seed owns the first admin.
	return strings.TrimSpace(cfg.Username) == "" ||
		(cfg.Password == "" && cfg.PasswordHash == "")
}

// initAuthStack loads the JWT secret + user store at construction. Failures
// are non-fatal to the scanner (charter §6.6 posture): the affected auth
// handler surfaces "auth unavailable" (503) and machine-token auth keeps
// working; the operator repairs the file without losing the dashboard.
func (s *Server) initAuthStack() {
	secret, err := loadOrCreateJWTSecret(s.dataDir)
	if err != nil {
		log.Printf("%v (dashboard login disabled)", err)
		return
	}
	users, err := openUserStore(s.dataDir)
	if err != nil {
		log.Printf("%v (dashboard login disabled)", err)
		return
	}
	s.authJWT = newJWTSessions(secret)
	s.authUsers = users
	seedEnvAdmin(users, s.cfg)
}

// authenticateSession validates the session cookie and returns the claims.
func (s *Server) authenticateSession(r *http.Request) (*sessionClaims, bool) {
	if s.authJWT == nil {
		return nil, false
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, false
	}
	claims, err := s.authJWT.verify(cookie.Value)
	if err != nil {
		return nil, false
	}
	return claims, true
}

// setSessionCookie issues a fresh JWT and writes it as the session cookie.
// Cookie attributes are identical to the previous opaque-token cookie —
// HttpOnly, SameSite=Strict, Secure under TLS, 24h — so the SPA, SSE stream
// (EventSource) and direct file links keep working unchanged.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, username, role string) error {
	token, _, err := s.authJWT.issue(username, role, sessionDuration)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionDuration.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isSecureRequest(r),
	})
	return nil
}

// clearSessionCookie revokes the presented token's jti and expires the
// cookie with the exact attributes used at issue time (mismatched attributes
// make some browsers treat the deletion cookie as a different cookie).
func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	if s.authJWT != nil {
		if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
			if claims, err := s.authJWT.verify(cookie.Value); err == nil {
				s.authJWT.revoke(claims.JTI, claims.ExpiresAt)
			}
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isSecureRequest(r),
	})
}

// authMiddleware protects routes when auth is configured. Two independent
// authentication flows pass through here:
//
//   - human: POST /api/auth/login + username/password -> signed JWT in the
//     xalgorix_session cookie, with browser CSRF checks and the per-IP login
//     rate limiter. After authentication the request is role-checked
//     (auth_rbac.go) and carries its claims in the request context.
//   - machine: Authorization: Bearer <API token> -> direct API access with no
//     session, no CSRF requirement (browsers never auto-attach bearer
//     headers, so a forged cross-site request cannot carry one), no
//     dashboard-page authorization, no RBAC, and no interaction with the
//     login limiter.
//
// The flows do not fall back into each other: a presented-but-invalid bearer
// token is machine traffic (browsers never send Authorization), so it gets a
// clean 401 instead of silently continuing through the cookie path.
func authMiddleware(s *Server) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		// Machine tokens are hashed once at middleware creation, never per
		// request (same as the previous middleware generation).
		var tokenHashes [][sha256.Size]byte
		if s.cfg != nil {
			tokenHashes = apiTokenHashes(s.cfg.APITokens)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path

			// ── Machine-to-machine authentication ──
			switch classifyMachineAuth(r, tokenHashes) {
			case machineAuthValid:
				if isMachineAPIPath(path) && machineTokenPathAllowed(path) {
					// Valid service token on an authorized API route: serve
					// directly. This skips the browser CSRF check on purpose
					// — CSRF protects cookie-based browser sessions; a bearer
					// credential is not auto-attached by browsers and a
					// valid token never needs the login limiter or a session.
					next.ServeHTTP(w, r)
					return
				}
				if isMachineAPIPath(path) {
					// Valid token on an operator-only API route (settings,
					// auth profiles, chat): the credential is fine, the ROUTE is
					// forbidden for machine clients. Answer with 403 and an
					// explicit reason. This previously fell through to the
					// session flow and ended as a generic 401, indistinguishable
					// from a bad token - API clients (and their operators)
					// diagnosed a working credential as a rejected token.
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					_ = json.NewEncoder(w).Encode(map[string]string{
						"error": "Machine API tokens are not authorized for this route",
					})
					return
				}
				// Valid token on a dashboard page route: fall through to the
				// standard session flow. A service token can never silently
				// authorize the dashboard.
			case machineAuthInvalid:
				// A presented credential that matches no configured token:
				// fail loudly with a generic 401. Never fall back to cookie
				// auth (that would let a stale token hide behind a browser
				// session), never log the token, never touch the human login
				// failure counters.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "Invalid API token",
				})
				return
			}

			// CSRF: validate state-changing requests on /api/* regardless of
			// whether auth is configured. This blocks an attacker page from
			// triggering a scan via the cookie even when no password is set
			// for local-loopback deployments.
			if strings.HasPrefix(path, "/api/") && path != "/api/auth/login" {
				if !isCSRFSafe(r) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					_ = json.NewEncoder(w).Encode(map[string]string{
						"error": "CSRF check failed: request origin does not match server host",
					})
					return
				}
			}

			// Skip auth if no credentials configured (zero-config loopback
			// mode; unchanged behavior until the first admin account exists).
			if !s.authConfigured() {
				next.ServeHTTP(w, r)
				return
			}

			// Public routes that don't need auth. The React SPA owns the
			// operator login screen, so its static assets must be reachable
			// before a session exists; /api/auth/setup is the first-run
			// bootstrap (guarded server-side by the empty-store check).
			if path == "/api/auth/login" || path == "/api/auth/status" || path == "/api/auth/setup" ||
				isStaticWebAssetPath(path) || strings.HasPrefix(path, "/uploads/") {
				next.ServeHTTP(w, r)
				return
			}

			// Human session: verify the signed cookie.
			claims, ok := s.authenticateSession(r)
			if !ok {
				// For API requests, return 401 JSON
				if strings.HasPrefix(path, "/api/") {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_ = json.NewEncoder(w).Encode(map[string]string{
						"error": "Authentication required",
					})
					return
				}
				// For page requests, serve the SPA shell. The client-side
				// router will show the React login page after
				// /api/auth/status reports that there is no active session.
				next.ServeHTTP(w, r)
				return
			}

			// RBAC: role × method × path policy for human sessions
			// (auth_rbac.go). Machine tokens never reach this point.
			if strings.HasPrefix(path, "/api/") && !authorizePath(claims.Role, r.Method, path) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "Forbidden: your role (" + claims.Role + ") does not permit this action",
				})
				return
			}

			next.ServeHTTP(w, r.WithContext(withSessionClaims(r.Context(), claims)))
		})
	}
}

// isSecureRequest returns true if the request is over TLS. Used to decide
// whether to set the Secure flag on cookies — required for HTTPS deploys,
// must be off for localhost HTTP development.
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	// Honor an X-Forwarded-Proto header only when running behind a trusted
	// proxy; we keep it simple here and trust nothing by default. Operators
	// behind a TLS-terminating proxy should set the cookie's Secure flag
	// elsewhere if they need it.
	return false
}

// publicUserShape is the identity projection returned by auth endpoints.
type publicUserShape struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

// handleLogin handles POST /api/auth/login. Credentials are checked against
// the user store (auth_users.go); success mints a signed JWT session cookie.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	if s.authUsers == nil || s.authJWT == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Auth store unavailable"})
		return
	}

	ip := clientIP(r)
	// Per-IP lockout — replaces the old unconditional 1s sleep so we don't
	// occupy goroutines on attacker traffic.
	if locked, retryAfter := loginIsLocked(ip); locked {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("Too many failed attempts. Try again in %ds.", retryAfter),
		})
		return
	}

	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request"})
		return
	}

	user, ok := s.authUsers.authenticate(creds.Username, creds.Password)
	if !ok {
		loginRecordFailure(ip)
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid credentials"})
		return
	}
	loginRecordSuccess(ip)

	if err := s.setSessionCookie(w, r, user.Username, user.Role); err != nil {
		log.Printf("[auth] session issue failed: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Internal error generating session"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"user":   publicUserShape{Username: user.Username, Role: user.Role},
	})
}

// handleAuthSetup handles POST /api/auth/setup — first-run bootstrap. It
// accepts a bootstrap admin ONLY while no account exists and no env
// credentials are configured to seed one; afterwards the endpoint is dead
// (403) for the lifetime of the store. Rate limiting reuses the login
// limiter: setup authenticates a credential pair just like a login would.
func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	if s.authUsers == nil || s.authJWT == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Auth store unavailable"})
		return
	}
	if !s.needsSetup() {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Setup already completed"})
		return
	}

	ip := clientIP(r)
	if locked, retryAfter := loginIsLocked(ip); locked {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("Too many failed attempts. Try again in %ds.", retryAfter),
		})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request"})
		return
	}

	user, err := s.authUsers.create(strings.TrimSpace(req.Username), req.Password, RoleAdmin)
	if err != nil {
		switch {
		case errors.Is(err, errUserValidation):
			w.WriteHeader(http.StatusBadRequest)
		case errors.Is(err, errUserConflict):
			w.WriteHeader(http.StatusConflict)
		default:
			log.Printf("[auth] setup create failed: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	loginRecordSuccess(ip)
	if err := s.setSessionCookie(w, r, user.Username, user.Role); err != nil {
		log.Printf("[auth] session issue failed: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Internal error generating session"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"user":   publicUserShape{Username: user.Username, Role: user.Role},
	})
}

// handleAuthPassword handles POST /api/auth/password — self-service password
// change for any authenticated role. The current password must be presented.
func (s *Server) handleAuthPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	claims, ok := sessionClaimsFrom(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Authentication required"})
		return
	}
	if s.authUsers == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Auth store unavailable"})
		return
	}

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request"})
		return
	}
	if err := s.authUsers.changePassword(claims.Username, req.CurrentPassword, req.NewPassword); err != nil {
		if errors.Is(err, errUserValidation) {
			w.WriteHeader(http.StatusBadRequest)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleLogout handles POST /api/auth/logout — revokes the presented token's
// jti and clears the cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSessionCookie(w, r)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "logged_out"})
}

// handleAuthStatus handles GET /api/auth/status. Extends the legacy
// {auth_enabled, authenticated} shape with needs_setup (first-run bootstrap
// state) and the authenticated identity, so the SPA can gate UI by role.
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	authEnabled := s.authConfigured()

	authenticated := false
	var user *publicUserShape
	if authEnabled {
		if claims, ok := s.authenticateSession(r); ok {
			authenticated = true
			user = &publicUserShape{Username: claims.Username, Role: claims.Role}
		}
	} else {
		authenticated = true // No auth configured = always authenticated
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"auth_enabled":  authEnabled,
		"authenticated": authenticated,
		"needs_setup":   s.needsSetup(),
		"user":          user,
	})
}
