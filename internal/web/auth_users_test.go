package web

// auth_users_test.go: the file-backed account store (auth_users.go) — CRUD,
// username uniqueness, last-enabled-admin guard, timing-equalized auth, env
// seeding, and persistence across reopen.

import (
	"errors"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
)

func newTestUserStore(t *testing.T) *userStore {
	t.Helper()
	st, err := openUserStore(t.TempDir())
	if err != nil {
		t.Fatalf("openUserStore: %v", err)
	}
	return st
}

// Create + list roundtrip; the hash never leaves the store.
func TestUserStore_CreateAndList(t *testing.T) {
	st := newTestUserStore(t)
	u, err := st.create("alice", "password123", RoleOperator)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.ID == "" || u.Username != "alice" || u.Role != RoleOperator {
		t.Fatalf("unexpected user: %+v", u)
	}
	if u.PasswordHash != "" {
		t.Fatal("create must project a hash-free user")
	}
	list := st.list()
	if len(list) != 1 || list[0].PasswordHash != "" {
		t.Fatalf("list must be hash-free: %+v", list)
	}
}

// Username uniqueness is case-insensitive; weak passwords/roles fail.
func TestUserStore_ValidationAndConflicts(t *testing.T) {
	st := newTestUserStore(t)
	if _, err := st.create("alice", "password123", RoleAdmin); err != nil {
		t.Fatalf("seed create: %v", err)
	}
	if _, err := st.create("ALICE", "password123", RoleViewer); !errors.Is(err, errUserConflict) {
		t.Fatalf("case-duplicate username must conflict, got %v", err)
	}
	for _, tc := range []struct {
		name, username, password, role string
	}{} {
		_ = tc
	}
	for _, bad := range []struct{ name, username, password, role string }{
		{"space in username", "b ob", "password123", RoleViewer},
		{"short password", "bob", "short", RoleViewer},
		{"unknown role", "bob", "password123", "superuser"},
		{"empty username", "", "password123", RoleViewer},
	} {
		if _, err := st.create(bad.username, bad.password, bad.role); !errors.Is(err, errUserValidation) {
			t.Fatalf("%s must be rejected, got %v", bad.name, err)
		}
	}
	if _, err := st.create("bob", "short", RoleViewer); !errors.Is(err, errUserValidation) {
		t.Fatal("short passwords must be rejected")
	}
	if _, err := st.create("bob", "password123", "superuser"); !errors.Is(err, errUserValidation) {
		t.Fatal("unknown roles must be rejected")
	}
	if st.hasUsers() && len(st.list()) != 1 {
		t.Fatalf("failed creates must not persist rows: %+v", st.list())
	}
}

// Authenticate accepts the right password, rejects wrong password and wrong
// username, and never authenticates a disabled account.
func TestUserStore_Authenticate(t *testing.T) {
	st := newTestUserStore(t)
	if _, err := st.create("alice", "password123", RoleOperator); err != nil {
		t.Fatalf("create: %v", err)
	}
	if u, ok := st.authenticate("alice", "password123"); !ok || u.Username != "alice" {
		t.Fatal("valid credentials must authenticate")
	}
	// Case-insensitive username match.
	if _, ok := st.authenticate("Alice", "password123"); !ok {
		t.Fatal("username matching must be case-insensitive")
	}
	if _, ok := st.authenticate("alice", "wrong"); ok {
		t.Fatal("wrong password must not authenticate")
	}
	if _, ok := st.authenticate("nobody", "password123"); ok {
		t.Fatal("unknown username must not authenticate")
	}
	// Disabled accounts are not authenticated.
	if _, err := st.create("bob", "password123", RoleViewer); err != nil {
		t.Fatalf("create bob: %v", err)
	}
	list := st.list()
	var bobID string
	for _, u := range list {
		if u.Username == "bob" {
			bobID = u.ID
		}
	}
	if _, err := st.update(bobID, func(mu *User) error { mu.Disabled = true; return nil }); err != nil {
		t.Fatalf("disable bob: %v", err)
	}
	if _, ok := st.authenticate("bob", "password123"); ok {
		t.Fatal("a disabled account must not authenticate")
	}
}

// The last enabled admin cannot be disabled, demoted, or deleted.
func TestUserStore_LastAdminGuard(t *testing.T) {
	st := newTestUserStore(t)
	admin, err := st.create("root", "password123", RoleAdmin)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if _, err := st.create("op", "password123", RoleOperator); err != nil {
		t.Fatalf("create operator: %v", err)
	}

	if _, err := st.update(admin.ID, func(mu *User) error { mu.Disabled = true; return nil }); !errors.Is(err, errLastAdmin) {
		t.Fatalf("disabling the last admin must fail, got %v", err)
	}
	if _, err := st.update(admin.ID, func(mu *User) error { mu.Role = RoleViewer; return nil }); !errors.Is(err, errLastAdmin) {
		t.Fatalf("demoting the last admin must fail, got %v", err)
	}
	if err := st.delete(admin.ID); !errors.Is(err, errLastAdmin) {
		t.Fatalf("deleting the last admin must fail, got %v", err)
	}

	// A second admin unlocks every mutation.
	admin2, err := st.create("root2", "password123", RoleAdmin)
	if err != nil {
		t.Fatalf("create admin2: %v", err)
	}
	if _, err := st.update(admin.ID, func(mu *User) error { mu.Disabled = true; return nil }); err != nil {
		t.Fatalf("disabling an admin with another enabled admin must succeed: %v", err)
	}
	if _, err := st.update(admin2.ID, func(mu *User) error { mu.Disabled = true; return nil }); !errors.Is(err, errLastAdmin) {
		t.Fatal("the remaining enabled admin must still be guarded")
	}
}

// Password change verifies the current password and rotates the hash.
func TestUserStore_ChangePassword(t *testing.T) {
	st := newTestUserStore(t)
	if _, err := st.create("alice", "oldpassword1", RoleViewer); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.changePassword("alice", "wrongpassword", "newpassword1"); !errors.Is(err, errUserValidation) {
		t.Fatalf("wrong current password must be rejected, got %v", err)
	}
	if err := st.changePassword("alice", "oldpassword1", "short"); !errors.Is(err, errUserValidation) {
		t.Fatalf("weak new password must be rejected, got %v", err)
	}
	if err := st.changePassword("alice", "oldpassword1", "newpassword1"); err != nil {
		t.Fatalf("changePassword: %v", err)
	}
	if _, ok := st.authenticate("alice", "oldpassword1"); ok {
		t.Fatal("the old password must stop working")
	}
	if _, ok := st.authenticate("alice", "newpassword1"); !ok {
		t.Fatal("the new password must authenticate")
	}
}

// The store persists across reopen (WriteAtomic snapshot), including the
// seeded admin.
func TestUserStore_PersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	st, err := openUserStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := st.create("persist-me", "password123", RoleAdmin); err != nil {
		t.Fatalf("create: %v", err)
	}
	st2, err := openUserStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !st2.hasUsers() || len(st2.list()) != 1 || st2.list()[0].Username != "persist-me" {
		t.Fatalf("users must survive reopen: %+v", st2.list())
	}
}

// XALGORIX_USERNAME/PASSWORD seed the first admin on an empty store; the
// store wins once it has users.
func TestSeedEnvAdmin(t *testing.T) {
	dir := t.TempDir()
	st, err := openUserStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	cfg := &config.Config{Username: "envop", Password: "envpassword1"}
	seedEnvAdmin(st, cfg)
	users := st.list()
	if len(users) != 1 || users[0].Username != "envop" || users[0].Role != RoleAdmin {
		t.Fatalf("env credentials must seed exactly one admin, got %+v", users)
	}
	if _, ok := st.authenticate("envop", "envpassword1"); !ok {
		t.Fatal("the seeded admin must authenticate with the env password")
	}
	// A second seeding attempt (e.g. duplicated startup) must not duplicate.
	seedEnvAdmin(st, cfg)
	if len(st.list()) != 1 {
		t.Fatalf("seeding must be idempotent, got %+v", st.list())
	}
	// Existing stores are left alone.
	if _, err := st.create("human", "password123", RoleViewer); err != nil {
		t.Fatalf("create: %v", err)
	}
	seedEnvAdmin(st, &config.Config{Username: "another", Password: "password123"})
	if len(st.list()) != 2 {
		t.Fatalf("seed must not run once users exist, got %+v", st.list())
	}
}

// A pre-existing bcrypt hash (XALGORIX_PASSWORD_HASH) seeds verbatim.
func TestSeedEnvAdmin_UsesProvidedHash(t *testing.T) {
	dir := t.TempDir()
	st, err := openUserStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	hash, err := hashPassword("hashpassword1")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	seedEnvAdmin(st, &config.Config{Username: "hashed", PasswordHash: hash})
	if _, ok := st.authenticate("hashed", "hashpassword1"); !ok {
		t.Fatal("the provided bcrypt hash must keep working after seeding")
	}
}
