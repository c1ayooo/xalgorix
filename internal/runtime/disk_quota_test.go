package runtime

import "testing"

func TestStorageOptIsOptIn(t *testing.T) {
	limits := ResourceLimits{DiskMB: 4096}
	if got := storageOpt(limits, false); got != nil {
		t.Fatalf("storageOpt with enforcement off = %v, want nil", got)
	}
	got := storageOpt(limits, true)
	if got["size"] != "4096m" {
		t.Fatalf("storageOpt size = %q, want 4096m", got["size"])
	}
	if got := storageOpt(ResourceLimits{}, true); got != nil {
		t.Fatalf("zero disk budget must not set StorageOpt: %v", got)
	}
}
