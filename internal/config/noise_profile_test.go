package config

import "testing"

func TestNormalizeNoiseProfile(t *testing.T) {
	cases := map[string]string{
		"":           NoiseBalanced,
		"quiet":      NoiseQuiet,
		"low":        NoiseQuiet,
		"stealth":    NoiseQuiet,
		"passive":    NoiseQuiet,
		"balanced":   NoiseBalanced,
		"loud":       NoiseLoud,
		"high":       NoiseLoud,
		"aggressive": NoiseLoud,
		"nonsense":   NoiseBalanced,
		"  QUIET  ":  NoiseQuiet,
	}
	for in, want := range cases {
		if got := NormalizeNoiseProfile(in); got != want {
			t.Errorf("NormalizeNoiseProfile(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnvOrChecklistDefaultsToLean(t *testing.T) {
	cases := map[string]string{
		"":             "lean",
		"lean":         "lean",
		"professional": "professional",
		"full":         "full",
		"FULL":         "full",
		"bogus":        "lean",
	}
	for in, want := range cases {
		if got := envOrChecklist(in); got != want {
			t.Errorf("envOrChecklist(%q) = %q, want %q", in, got, want)
		}
	}
}
