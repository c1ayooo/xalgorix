package approval

import "testing"

// TestEvaluateScopeRules pins the scope-observer baseline: bulk destruction,
// uploads and OSS object writes are deny; the target's own web control plane
// keeps a human obligation (require); read methods and unrelated endpoints stay
// unregulated (none).
func TestEvaluateScopeRules(t *testing.T) {
	rules := DefaultScopeRules()
	cases := []struct {
		name   string
		method string
		url    string
		body   string
		want   ScopeDecision
		rule   string
	}{
		{
			name:   "bulk delete endpoint",
			method: "DELETE",
			url:    "https://app.example.com/api/companies/1/bulk_delete",
			want:   ScopeDeny,
			rule:   "no-bulk-data-destruction",
		},
		{
			name:   "delete-all payload",
			method: "POST",
			url:    "https://app.example.com/api/tasks",
			body:   `{"delete_all":true}`,
			want:   ScopeDeny,
			rule:   "no-bulk-data-destruction",
		},
		{
			name:   "file upload",
			method: "POST",
			url:    "https://app.example.com/api/chatupload",
			want:   ScopeDeny,
			rule:   "no-file-upload",
		},
		{
			name:   "oss object write",
			method: "PUT",
			url:    "https://bucket.oss-cn-hangzhou.aliyuncs.com/report.pdf",
			want:   ScopeDeny,
			rule:   "no-object-storage-writes",
		},
		{
			name:   "web settings write keeps human obligation",
			method: "PUT",
			url:    "https://app.example.com/api/settings",
			want:   ScopeRequire,
			rule:   "web-control-plane-write",
		},
		{
			name:   "bulk export keeps human obligation",
			method: "POST",
			url:    "https://app.example.com/api/reports/export",
			want:   ScopeRequire,
			rule:   "bulk-data-export",
		},
		{
			name:   "read method always passes",
			method: "GET",
			url:    "https://app.example.com/api/settings",
			body:   "delete_all",
			want:   ScopeNone,
		},
		{
			name:   "unrelated write stays unregulated",
			method: "POST",
			url:    "https://app.example.com/api/orders",
			body:   `{"qty":1}`,
			want:   ScopeNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateScopeRules(rules, tc.method, tc.url, tc.body)
			if got.Decision != tc.want {
				t.Fatalf("decision = %q, want %q (rule %q)", got.Decision, tc.want, got.Rule)
			}
			if tc.rule != "" && got.Rule != tc.rule {
				t.Fatalf("rule = %q, want %q", got.Rule, tc.rule)
			}
		})
	}
}

// TestEvaluateScopeRulesDenyOutranksRequire ensures ordering never lets a
// later require rule mask an earlier deny (the observer must fail closed).
func TestEvaluateScopeRulesDenyOutranksRequire(t *testing.T) {
	rules := []ScopeRule{
		{Name: "human", Decision: ScopeRequire, PathFeatures: []string{"/api"}},
		{Name: "deny", Decision: ScopeDeny, PathFeatures: []string{"/wipe"}},
	}
	got := EvaluateScopeRules(rules, "POST", "https://x.example.com/api/wipe", "")
	if got.Decision != ScopeDeny || got.Rule != "deny" {
		t.Fatalf("got %+v, want deny/deny", got)
	}
}
