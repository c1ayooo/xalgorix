package web

import "testing"

// The visibility maps are keyed by env name; a typo there would silently
// leave an entry visible (or hide the wrong one), which is hard to spot in the
// rendered dashboard.
func TestEnvironmentMetadataMapsMatchRegistry(t *testing.T) {
	defs := allEnvSettingDefinitions()
	known := make(map[string]envSettingDefinition, len(defs))
	for _, def := range defs {
		known[def.Key] = def
		if def.CategoryKey == "" {
			t.Errorf("%s has no categoryKey", def.Key)
		}
		if def.Hidden && def.Advanced {
			t.Errorf("%s is both hidden and advanced", def.Key)
		}
	}
	for key := range environmentHiddenKeys {
		if _, ok := known[key]; !ok {
			t.Errorf("hidden key %s is not in the registry", key)
		}
	}
	for key := range environmentAdvancedKeys {
		if _, ok := known[key]; !ok {
			t.Errorf("advanced key %s is not in the registry", key)
		}
	}
	for _, key := range []string{
		"XALGORIX_TOOLBOX_PROXY_URL",
		"XALGORIX_TOOLBOX_BROWSER_PROXY",
		"XALGORIX_EGRESS_MODE",
		"XALGORIX_EGRESS_ROUTER",
	} {
		if _, ok := known[key]; !ok {
			t.Errorf("%s must be registered in the Egress proxy category", key)
		}
	}
	// Traffic recording is its own group: the recorder toggle and its listener
	// knobs live there together so "recording" and "egress" are not conflated.
	for _, key := range []string{
		"XALGORIX_TRAFFICPROXY",
		"XALGORIX_TRAFFICPROXY_BIND",
	} {
		def, ok := known[key]
		if !ok || def.Category != "Traffic recording" {
			t.Errorf("%s must be registered in the Traffic recording category", key)
		}
	}
}

func TestEnvironmentMetadataVisibility(t *testing.T) {
	byKey := envDefinitionByKey()

	// Keys owned by a dedicated control must never render a second editor.
	for _, key := range []string{
		"XALGORIX_RATE_LIMIT_REQUESTS",
		"XALGORIX_RATE_LIMIT_WINDOW",
	} {
		if !byKey[key].Hidden {
			t.Errorf("%s should be hidden from the Environment tab", key)
		}
	}

	// Whole categories owned by a dedicated tab.
	for key, def := range byKey {
		if def.Category == "Notifications" && !def.Hidden {
			t.Errorf("%s (Notifications) should be hidden from the Environment tab", key)
		}
	}

	// LLM settings the LLM tab already edits must not get a second editor.
	for _, key := range []string{
		"XALGORIX_LLM",
		"XALGORIX_API_KEY",
		"XALGORIX_API_KEYS",
		"XALGORIX_LLM_PROFILE",
		"GEMINI_API_KEY",
	} {
		if !byKey[key].Hidden {
			t.Errorf("%s should be hidden from the Environment tab", key)
		}
	}

	// LLM knobs with no other surface stay reachable, collapsed under Advanced.
	for _, key := range []string{
		"XALGORIX_LANGUAGE",
		"XALGORIX_PDF_CJK_FONT",
		"XALGORIX_LLM_CONTEXT_WINDOW",
		"XALGORIX_MAX_OUTPUT_TOKENS",
	} {
		def := byKey[key]
		if def.Hidden || !def.Advanced {
			t.Errorf("%s should be visible in the advanced section", key)
		}
	}

	// The everyday surface stays visible and unfolded.
	for _, key := range []string{
		"XALGORIX_USE_PROXY",
		"XALGORIX_PROXY_URL",
		"XALGORIX_PROXY_REQUIRED",
		"XALGORIX_MAX_DURATION",
		"XALGORIX_MAX_ITERATIONS",
		"XALGORIX_WORKER_CONCURRENCY",
		"XALGORIX_APPROVAL_MODE",
	} {
		def := byKey[key]
		if def.Hidden || def.Advanced {
			t.Errorf("%s should be visible in the common section", key)
		}
	}

	// Internal tuning stays off the surface entirely.
	for _, key := range []string{
		"XALGORIX_CONVERGE_FINISH_IDLE",
		"XALGORIX_SCAN_MEMORY_BUDGET_MB",
		"XALGORIX_PASSWORD_HASH",
	} {
		if !byKey[key].Hidden {
			t.Errorf("%s should be hidden from the Environment tab", key)
		}
	}
}
