package config

import "testing"

func TestNormalizeEngineMode(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"", "legacy"},
		{"legacy", "legacy"},
		{" EINO ", "eino"},
		{"eino", "eino"},
		{"agentic", "legacy"},
	} {
		if got := normalizeEngineMode(tc.in); got != tc.want {
			t.Fatalf("normalizeEngineMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
