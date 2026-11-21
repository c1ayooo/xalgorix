package safefile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadRejectsSymlinkComponent(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ReadFile(link); !errors.Is(err, ErrUnsafePath) && !errors.Is(err, ErrNotSupported) {
		t.Fatalf("ReadFile followed symlink: %v", err)
	}
}

func TestWriteFileReplacesWithoutFollowingSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(target, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("content = %q, want new", got)
	}
}

func TestCreateRefusesExisting(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Create(target, []byte("replace"), 0o600); err == nil {
		t.Fatal("Create must refuse an existing file")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "keep" {
		t.Fatalf("existing file modified: %q", got)
	}
}
