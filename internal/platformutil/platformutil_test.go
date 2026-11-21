package platformutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiskAvailableRoot(t *testing.T) {
	if got := DiskAvailable(string(filepath.Separator)); got == 0 {
		t.Fatal("DiskAvailable returned 0 for root")
	}
}

func TestTryLockFileIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	ok, err := TryLockFile(first)
	if err != nil || !ok {
		t.Fatalf("first lock = %v, %v", ok, err)
	}
	second, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ok, err = TryLockFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("second lock unexpectedly succeeded")
	}
	if err := UnlockFile(first); err != nil {
		t.Fatal(err)
	}
	ok, err = TryLockFile(second)
	if err != nil || !ok {
		t.Fatalf("lock after unlock = %v, %v", ok, err)
	}
	_ = UnlockFile(second)
}
