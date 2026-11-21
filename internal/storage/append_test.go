package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendSyncCreatesPrivateFileAndAppends(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	if err := EnsureSecureDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "records.jsonl")

	if err := AppendSync(path, []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if err := AppendSync(path, []byte("two\n")); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "one\ntwo\n" {
		t.Fatalf("unexpected contents: %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o, want 600", info.Mode().Perm())
	}
}

func TestAppendSyncDoesNotCreateParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "records.jsonl")
	if err := AppendSync(path, []byte("x\n")); err == nil {
		t.Fatal("expected missing parent directory error")
	}
}

func TestAppendSyncRejectsEmptyPath(t *testing.T) {
	if err := AppendSync("", []byte("x")); err == nil {
		t.Fatal("expected empty path error")
	}
}
