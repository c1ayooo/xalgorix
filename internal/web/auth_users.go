// Package web — auth_users.go holds the local dashboard account store that
// backs RBAC. Deliberately a file-backed store in the web layer, NOT part of
// internal/platform:
//
//   - login is a hard web capability, not an optional cold-path feature: the
//     platform store's failure posture is 503-with-degradation, which must
//     never apply to authentication (an unreachable PG would otherwise lock
//     every operator out);
//   - machine API tokens are likewise machine-local configuration (env), so
//     per-machine file accounts are the consistent posture for the
//     single-binary deployment; a PG-backed copy can be added later if
//     multi-instance shared accounts ever become a requirement.
//
// The snapshot lives at <dataDir>/auth/users.json ("auth" is a reserved
// top-level data-dir name) and is written exclusively via
// storage.WriteAtomic behind an RWMutex, mirroring platform/filestore.go.
package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

// Role constants for RBAC (auth_rbac.go). They are the wire values in JWT
// claims, users.json and the /api/users API.
const (
	RoleAdmin    = "admin"    // full control, incl. settings/keys/user management
	RoleOperator = "operator" // run scans/tasks/approvals; no secrets or accounts
	RoleViewer   = "viewer"   // read-only
)

// validRoles is the authoritative set; unknown roles fail validation.
var validRoles = map[string]struct{}{RoleAdmin: {}, RoleOperator: {}, RoleViewer: {}}

// validRole reports whether s is one of the three fixed roles.
func validRole(s string) bool {
	_, ok := validRoles[s]
	return ok
}

// Store errors, mapped to HTTP statuses by handlers_users.go.
var (
	errUserValidation = errors.New("validation failed")
	errUserConflict   = errors.New("conflict")
	errUserNotFound   = errors.New("not found")
	errLastAdmin      = errors.New("cannot remove or disable the last enabled admin")
)

// usernameRE constrains account names: 1-64 chars of letters, digits, dot,
// underscore, hyphen. No whitespace, no shell-adjacent characters.
var usernameRE = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)

// maxBcryptPassword caps input at bcrypt's 72-byte boundary so a long
// password fails validation with a clear message instead of being silently
// truncated by the hash.
const maxBcryptPassword = 72

// minPasswordLength balances "usable" against brute force. The login limiter
// is the second line of defense.
const minPasswordLength = 8

// User is a dashboard account. PasswordHash is persisted to users.json but
// is NEVER serialized into any API response — handlers project through
// safeUser (invariant: the API never returns secrets).
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	Role         string    `json:"role"`
	Disabled     bool      `json:"disabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// enabledAdmin reports whether u currently counts toward the last-admin
// guard.
func (u *User) enabledAdmin() bool {
	return u.Role == RoleAdmin && !u.Disabled
}

// usersSnapshot is the users.json wire shape (platform/filestore.go pattern:
// a versioned full snapshot).
type usersSnapshot struct {
	Version int    `json:"version"`
	Users   []User `json:"users"`
}

// userStore is the file-backed account store.
type userStore struct {
	mu    sync.RWMutex
	path  string
	users []User
}

// usersFilePath is where the snapshot lives under the data dir.
func usersFilePath(dataDir string) string {
	return filepath.Join(dataDir, "auth", "users.json")
}

// openUserStore loads users.json; a missing file is an empty store (first
// start). A corrupt snapshot is NOT silently ignored — it surfaces as an
// error so the operator can repair it (auth handlers then 503, the scanner
// keeps working; charter §6.6 posture).
func openUserStore(dataDir string) (*userStore, error) {
	path := usersFilePath(dataDir)
	st := &userStore{path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return nil, fmt.Errorf("[auth] read users file: %w", err)
	}
	var snap usersSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("[auth] parse %s: %w", path, err)
	}
	if snap.Users == nil {
		snap.Users = []User{}
	}
	st.users = snap.Users
	return st, nil
}

// empty reports whether no account exists at all (the needs_setup state).
func (st *userStore) empty() bool {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return len(st.users) == 0
}

// hasUsers reports whether any account exists (auth is enabled).
func (st *userStore) hasUsers() bool {
	return !st.empty()
}

// safeUser strips the password hash for API projection. HTTP responses are
// built from this shape only.
func safeUser(u User) User {
	u.PasswordHash = ""
	return u
}

// list returns hash-free copies of all accounts.
func (st *userStore) list() []User {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]User, 0, len(st.users))
	for _, u := range st.users {
		out = append(out, safeUser(u))
	}
	return out
}

// get returns a hash-free copy by id.
func (st *userStore) get(id string) (User, error) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	for _, u := range st.users {
		if u.ID == id {
			return safeUser(u), nil
		}
	}
	return User{}, errUserNotFound
}

// hashPassword wraps bcrypt.GenerateFromPassword with the repo's default
// cost; the error is wrapped with context for the operator-facing 500.
func hashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("[auth] bcrypt: %w", err)
	}
	return string(h), nil
}

// validateUsernamePassword enforces the username/password contract shared by
// setup, user creation and password changes.
func validateUsernamePassword(username, password string) error {
	if !usernameRE.MatchString(username) {
		return fmt.Errorf("%w: username must be 1-64 chars of [a-zA-Z0-9._-]", errUserValidation)
	}
	if len(password) < minPasswordLength {
		return fmt.Errorf("%w: password must be at least %d characters", errUserValidation, minPasswordLength)
	}
	if len(password) > maxBcryptPassword {
		return fmt.Errorf("%w: password must be at most %d characters", errUserValidation, maxBcryptPassword)
	}
	return nil
}

// newUserID mints an unguessable account id.
func newUserID() (string, error) {
	tok, err := generateSessionToken()
	if err != nil {
		return "", err
	}
	return "u_" + tok[:16], nil
}

// create adds an account. Username uniqueness is case-insensitive.
func (st *userStore) create(username, password, role string) (User, error) {
	username = strings.TrimSpace(username)
	if err := validateUsernamePassword(username, password); err != nil {
		return User{}, err
	}
	if !validRole(role) {
		return User{}, fmt.Errorf("%w: role must be one of admin|operator|viewer", errUserValidation)
	}
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, u := range st.users {
		if strings.EqualFold(u.Username, username) {
			return User{}, fmt.Errorf("%w: username already exists", errUserConflict)
		}
	}
	id, err := newUserID()
	if err != nil {
		return User{}, err
	}
	now := time.Now().UTC()
	u := User{
		ID:           id,
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	st.users = append(st.users, u)
	if err := st.saveLocked(); err != nil {
		st.users = st.users[:len(st.users)-1]
		return User{}, err
	}
	return safeUser(u), nil
}

// update mutates one account under the write lock. mutate may return a
// validation error to abort. The last-admin guard rejects any mutation that
// would leave zero enabled admins (demote/disable).
func (st *userStore) update(id string, mutate func(*User) error) (User, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	idx := -1
	for i := range st.users {
		if st.users[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return User{}, errUserNotFound
	}
	before := st.users[idx]
	after := before
	if err := mutate(&after); err != nil {
		return User{}, err
	}
	if before.enabledAdmin() && !after.enabledAdmin() {
		enabled := 0
		for i := range st.users {
			if i != idx && st.users[i].enabledAdmin() {
				enabled++
			}
		}
		if enabled == 0 {
			return User{}, errLastAdmin
		}
	}
	after.UpdatedAt = time.Now().UTC()
	st.users[idx] = after
	if err := st.saveLocked(); err != nil {
		st.users[idx] = before
		return User{}, err
	}
	return safeUser(after), nil
}

// delete removes an account, refusing to strand the dashboard without an
// enabled admin.
func (st *userStore) delete(id string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	idx := -1
	for i := range st.users {
		if st.users[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errUserNotFound
	}
	if st.users[idx].enabledAdmin() {
		enabled := 0
		for i := range st.users {
			if i != idx && st.users[i].enabledAdmin() {
				enabled++
			}
		}
		if enabled == 0 {
			return errLastAdmin
		}
	}
	removed := st.users[idx]
	st.users = append(st.users[:idx], st.users[idx+1:]...)
	if err := st.saveLocked(); err != nil {
		st.users = append(st.users[:idx], append([]User{removed}, st.users[idx:]...)...)
		return err
	}
	return nil
}

// dummyBcryptHash is a precomputed bcrypt digest used solely for timing
// equalization on username misses: a wrong-username attempt runs the same
// bcrypt comparison as a wrong-password attempt, so neither the username
// nor its existence leaks through response timing. It is never a valid
// credential for any real account.
var dummyBcryptHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// authenticate verifies credentials and returns the matching account. The
// store never authenticates a disabled account.
func (st *userStore) authenticate(username, password string) (User, bool) {
	st.mu.RLock()
	hash := ""
	id := ""
	for i := range st.users {
		if !st.users[i].Disabled && strings.EqualFold(st.users[i].Username, username) {
			hash = st.users[i].PasswordHash
			id = st.users[i].ID
			break
		}
	}
	st.mu.RUnlock()

	if hash == "" {
		// Username miss (or disabled): burn one bcrypt comparison so the
		// wall-clock shape matches a wrong-password attempt.
		_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(password))
		return User{}, false
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return User{}, false
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	// Re-read under lock so a concurrent disable between the two critical
	// sections cannot authenticate a just-disabled account.
	for i := range st.users {
		if st.users[i].ID == id {
			if st.users[i].Disabled {
				return User{}, false
			}
			return safeUser(st.users[i]), true
		}
	}
	return User{}, false
}

// changePassword verifies the current password and rotates the hash. Any
// role may change their own password.
func (st *userStore) changePassword(username, current, next string) error {
	u, ok := st.authenticate(username, current)
	if !ok {
		return fmt.Errorf("%w: current password does not match", errUserValidation)
	}
	if err := validateUsernamePassword(u.Username, next); err != nil {
		// Username already passed account validation; this call only enforces
		// the new password bounds.
		return err
	}
	hash, err := hashPassword(next)
	if err != nil {
		return err
	}
	_, err = st.update(u.ID, func(mu *User) error {
		mu.PasswordHash = hash
		return nil
	})
	return err
}

// saveLocked persists the snapshot. Caller must hold st.mu for writing.
func (st *userStore) saveLocked() error {
	if err := storage.EnsureSecureDir(filepath.Dir(st.path)); err != nil {
		return err
	}
	snap := usersSnapshot{Version: 1, Users: st.users}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return storage.WriteAtomic(st.path, raw)
}

// seedEnvAdmin creates the first admin from XALGORIX_USERNAME/PASSWORD(_HASH)
// on startup when the store is empty. This keeps existing deployments
// (env-configured single operator) working unchanged: their env credentials
// become a stored admin account and the store wins from then on. A plaintext
// XALGORIX_PASSWORD is hashed once into the store (the env value itself is
// left untouched — config owns that lifecycle and its deprecation warning).
func seedEnvAdmin(st *userStore, cfg *config.Config) {
	if st == nil || cfg == nil {
		return
	}
	username := strings.TrimSpace(cfg.Username)
	if username == "" || (cfg.Password == "" && cfg.PasswordHash == "") {
		return
	}
	if st.hasUsers() {
		return
	}
	hash := cfg.PasswordHash
	if hash == "" {
		var err error
		hash, err = hashPassword(cfg.Password)
		if err != nil {
			log.Printf("[auth] env admin seed failed: %v", err)
			return
		}
	}
	if !usernameRE.MatchString(username) {
		// Legacy names may predate the charset contract; the credential pair
		// is what matters for continuity, so seed anyway and say so.
		log.Printf("[auth] seeding admin %q despite non-conforming username", username)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, u := range st.users {
		if strings.EqualFold(u.Username, username) {
			return
		}
	}
	id, err := newUserID()
	if err != nil {
		log.Printf("[auth] env admin seed failed: %v", err)
		return
	}
	now := time.Now().UTC()
	u := User{ID: id, Username: username, PasswordHash: hash, Role: RoleAdmin, CreatedAt: now, UpdatedAt: now}
	st.users = append(st.users, u)
	if err := st.saveLocked(); err != nil {
		st.users = st.users[:len(st.users)-1]
		log.Printf("[auth] env admin seed persist failed: %v", err)
		return
	}
	log.Printf("[auth] seeded admin account %q from environment credentials", username)
}
