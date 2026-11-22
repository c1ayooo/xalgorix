// Package web — auth_jwt.go implements the stateless session token used for
// human dashboard logins. It replaces the previous opaque in-memory session
// map: the token is a signed HS256 JWT whose signing secret is generated
// randomly on first start and persisted under <dataDir>/auth/jwt.key, so
// logins survive process restarts while the secret never leaves the machine
// (invariant: the API never returns secrets — there is no endpoint that
// reads this file).
//
// Statelessness is bounded on purpose: logout revokes the token's jti in an
// in-memory denylist, so a logged-out cookie is dead immediately even though
// the signature is still cryptographically valid. The denylist is process-
// local by design — it only needs to outlive the cookie in the browser,
// which logout clears anyway.
package web

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	cryptorand "crypto/rand"
	"encoding/hex"

	"github.com/golang-jwt/jwt/v5"
	"github.com/xalgord/xalgorix/v4/internal/storage"
)

const (
	jwtSecretFile = "jwt.key"
	jwtIssuer     = "xalgorix"
	// sessionDuration matches the previous cookie-session lifetime (24h).
	sessionDuration = 24 * time.Hour
	// jtiReaperInterval sweeps the revocation denylist. Logins are human
	// events, so the list is tiny; a slow cadence is plenty.
	jtiReaperInterval = 15 * time.Minute
)

// generateSessionSecret creates the random signing secret: 32 bytes of
// crypto/rand, hex-encoded. Same entropy argument as generateSessionToken —
// 256 bits is beyond brute-force reach, and hashing would only obscure.
func generateSessionSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := cryptorand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand unavailable: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// loadOrCreateJWTSecret returns the signing secret from
// <dataDir>/auth/jwt.key, creating the file on first start. The file is
// written via storage.WriteAtomic (0600, fsync, rename — the repo-wide
// durability contract) inside a 0700 directory. A load failure is fatal to
// the auth stack only: callers surface it as "auth unavailable" rather than
// refusing to start the scanner.
func loadOrCreateJWTSecret(dataDir string) ([]byte, error) {
	dir := filepath.Join(dataDir, "auth")
	path := filepath.Join(dir, jwtSecretFile)
	if raw, err := os.ReadFile(path); err == nil {
		secret := trimSecret(raw)
		if len(secret) >= 32 {
			return []byte(secret), nil
		}
		// Too short to be a real secret (hand-truncated file): regenerate
		// rather than sign with weak material.
		log.Printf("[auth] jwt key at %s is too short, regenerating", path)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("[auth] read jwt key: %w", err)
	}
	secret, err := generateSessionSecret()
	if err != nil {
		return nil, err
	}
	if err := storage.EnsureSecureDir(dir); err != nil {
		return nil, err
	}
	if err := storage.WriteAtomic(path, []byte(secret)); err != nil {
		return nil, fmt.Errorf("[auth] persist jwt key: %w", err)
	}
	return []byte(secret), nil
}

// trimSecret strips whitespace/newlines that editors tend to add to key files.
func trimSecret(raw []byte) string {
	out := make([]byte, 0, len(raw))
	for _, c := range raw {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

// sessionClaims is the authenticated identity carried by every dashboard
// request after login.
type sessionClaims struct {
	Username  string
	Role      string
	JTI       string
	ExpiresAt time.Time
}

// jwtSessions signs and verifies login JWTs and tracks revoked jti values.
// It hangs off Server (auth field) instead of a package global so scan
// isolation stays intact and tests can build isolated stacks.
type jwtSessions struct {
	mu         sync.Mutex
	secret     []byte
	revoked    map[string]time.Time // jti → token expiry
	reaperOnce sync.Once
}

func newJWTSessions(secret []byte) *jwtSessions {
	return &jwtSessions{secret: secret, revoked: make(map[string]time.Time)}
}

// issue mints a signed token for the authenticated user.
func (j *jwtSessions) issue(username, role string, d time.Duration) (token string, claims sessionClaims, err error) {
	now := time.Now()
	claims = sessionClaims{
		Username:  username,
		Role:      role,
		ExpiresAt: now.Add(d),
	}
	claims.JTI, err = generateSessionToken()
	if err != nil {
		return "", claims, err
	}
	mc := jwt.MapClaims{
		"iss":  jwtIssuer,
		"sub":  username,
		"role": role,
		"jti":  claims.JTI,
		"iat":  now.Unix(),
		"exp":  claims.ExpiresAt.Unix(),
	}
	token, err = jwt.NewWithClaims(jwt.SigningMethodHS256, mc).SignedString(j.secret)
	if err != nil {
		return "", claims, fmt.Errorf("[auth] sign jwt: %w", err)
	}
	return token, claims, nil
}

var errSessionRevoked = errors.New("session revoked")

// verify validates signature, algorithm, expiry, issuer and revocation.
// The signing method is pinned to HS256 — a token advertising another alg
// (the classic alg-confusion vector) is rejected before the keyfunc runs.
func (j *jwtSessions) verify(token string) (*sessionClaims, error) {
	parsed, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok || t.Header["alg"] != "HS256" {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return j.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(jwtIssuer), jwt.WithExpirationRequired())
	if err != nil || !parsed.Valid {
		return nil, fmt.Errorf("[auth] invalid token: %w", err)
	}
	mc, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, errors.New("[auth] unexpected claims shape")
	}
	sub, _ := mc["sub"].(string)
	role, _ := mc["role"].(string)
	jti, _ := mc["jti"].(string)
	if sub == "" || jti == "" {
		return nil, errors.New("[auth] token missing sub/jti")
	}
	expClaims := sessionClaims{Username: sub, Role: role, JTI: jti}
	if exp, err := mc.GetExpirationTime(); err == nil && exp != nil {
		expClaims.ExpiresAt = exp.Time
	}

	j.mu.Lock()
	_, dead := j.revoked[jti]
	j.mu.Unlock()
	if dead {
		return nil, errSessionRevoked
	}
	return &expClaims, nil
}

// revoke adds a jti to the denylist until its natural expiry.
func (j *jwtSessions) revoke(jti string, exp time.Time) {
	if jti == "" {
		return
	}
	if exp.IsZero() {
		exp = time.Now().Add(sessionDuration)
	}
	j.mu.Lock()
	j.revoked[jti] = exp
	j.mu.Unlock()
}

// startReaper sweeps expired denylist entries so the map cannot grow
// unbounded. Runs once per process.
func (j *jwtSessions) startReaper() {
	j.reaperOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(jtiReaperInterval)
			defer ticker.Stop()
			for range ticker.C {
				now := time.Now()
				j.mu.Lock()
				for jti, exp := range j.revoked {
					if now.After(exp) {
						delete(j.revoked, jti)
					}
				}
				j.mu.Unlock()
			}
		}()
	})
}
