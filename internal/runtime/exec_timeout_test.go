package runtime

import (
	"reflect"
	"testing"
	"time"
)

func TestWithInContainerTimeoutHasNoDoubleDash(t *testing.T) {
	got := withInContainerTimeout([]string{"sh", "-c", "echo ok"}, 1500*time.Millisecond)
	want := []string{"timeout", "-k", "5", "2", "sh", "-c", "echo ok"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wrapped argv = %v, want %v", got, want)
	}
	for _, arg := range got {
		if arg == "--" {
			t.Fatal("GNU timeout treats a post-DURATION -- as the command; it must not be present")
		}
	}
	if got := withInContainerTimeout([]string{"echo", "ok"}, 0); !reflect.DeepEqual(got, []string{"echo", "ok"}) {
		t.Fatalf("zero timeout must not wrap: %v", got)
	}
}
