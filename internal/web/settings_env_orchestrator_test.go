package web

import "testing"

func TestWorkerConcurrencySettingApplyAndClamp(t *testing.T) {
	s := newTestServer(t, nil)
	before := s.cfg.Orchestrator
	s.applyEnvironmentToRuntimeConfig(map[string]string{
		"XALGORIX_WORKER_CONCURRENCY": "99",
		// The topology is built in, not a UI setting: an env value here must
		// not mutate the runtime config.
		"XALGORIX_ORCHESTRATOR": "legacy",
	})
	if s.cfg.WorkerConcurrency != 10 {
		t.Fatalf("worker concurrency = %d, want 10", s.cfg.WorkerConcurrency)
	}
	if s.cfg.Orchestrator != before {
		t.Fatalf("orchestrator changed through the settings API: %q -> %q", before, s.cfg.Orchestrator)
	}
	s.applyEnvironmentToRuntimeConfig(map[string]string{
		"XALGORIX_WORKER_CONCURRENCY": "0",
	})
	if s.cfg.WorkerConcurrency != 1 {
		t.Fatalf("worker concurrency = %d, want 1", s.cfg.WorkerConcurrency)
	}
}

func TestWorkerConcurrencyNormalization(t *testing.T) {
	v, err := normalizeEnvSettingValue(envSettingDefinition{Key: "XALGORIX_WORKER_CONCURRENCY"}, "0")
	if err != nil || v != "1" {
		t.Fatalf("clamp low = %q err=%v", v, err)
	}
	v, err = normalizeEnvSettingValue(envSettingDefinition{Key: "XALGORIX_WORKER_CONCURRENCY"}, "99")
	if err != nil || v != "10" {
		t.Fatalf("clamp high = %q err=%v", v, err)
	}
}
