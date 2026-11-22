package approval

// Scope observer rules (v4.6): the project-scoped "do not break the target"
// baseline that the scope observer agent applies automatically while a run is
// live. Unlike AssessHTTP (which recognizes pentest payloads), these rules name
// the target's own control plane — the shapes an autonomous agent must not
// exercise even when they technically sit inside the authorized scope:
//
//	destruction  bulk delete / truncate / wipe endpoints and payloads;
//	upload       file/material uploads (object storage, chat/workspace files);
//	web/oss      web console, settings, agent/skill management and OSS/S3
//	             object-store control-plane writes.
//
// The rule set is a pure classifier (no platform dependency) so the traffic
// proxy, the Eino broker and the REST layer all evaluate the same table. The
// operator's own approval_rules rows are evaluated separately (and take
// precedence) by the web layer.
//
// Verdict mapping: "deny" → the scope observer auto-rejects the call (a terminal
// approval row with decided_by="scope-observer"); "require" → the call still
// pends for a human decision (the observer never widens a human obligation).

import "strings"

// ScopeDecision is the scope observer's rule-level outcome.
type ScopeDecision string

const (
	// ScopeNone means no built-in rule matched.
	ScopeNone ScopeDecision = ""
	// ScopeDeny means the observer must reject the call without asking.
	ScopeDeny ScopeDecision = "deny"
	// ScopeRequire means the call keeps its human approval obligation.
	ScopeRequire ScopeDecision = "require"
)

// ScopeRule is one built-in baseline rule. Matching is deliberately simple and
// structural (method + substring features), mirroring ApprovalRuleMatch:
//   - Methods empty = every write method (POST/PUT/PATCH/DELETE) applies;
//   - PathFeatures match "host path" (case-insensitive substring);
//   - BodyFeatures match the request body (case-insensitive substring);
//   - HostFeatures match the URL host (case-insensitive substring).
//
// A rule matches when ANY populated location group (path or body) hits;
// HostFeatures, when populated, is an additional AND constraint.
type ScopeRule struct {
	Name         string
	Decision     ScopeDecision
	Methods      []string
	PathFeatures []string
	BodyFeatures []string
	HostFeatures []string
}

// ScopeMatch is the evaluation result for one call.
type ScopeMatch struct {
	Rule     string
	Decision ScopeDecision
	Reason   string
}

// writeMethods are the methods the baseline rules assess. Read methods stay on
// the tool-call audit trail exactly like AssessHTTP's allow path.
var writeMethods = map[string]struct{}{
	"POST": {}, "PUT": {}, "PATCH": {}, "DELETE": {},
}

// dataDestroyFeatures name bulk-destruction endpoints/payloads. These are
// hard denies: a single call wipes more than the engagement can justify.
var dataDestroyFeatures = []string{
	"bulk_delete", "bulk-delete", "bulkdelete", "delete_all", "delete-all",
	"deleteall", "delete_batch", "batch_delete", "batch-delete", "mass_delete",
	"purge_all", "purge-all", "wipe", "truncate", "drop_database",
	"flushall", "flushdb", "empty_bucket", "remove_all", "rm_all",
}

// uploadFeatures name material-upload surfaces (object storage + web workspace).
var uploadFeatures = []string{
	"upload", "uploads", "file_upload", "multipart/form-data", "attachment",
	"attachments", "chatupload", "skill_upload", "workspace/upload",
	"/files", "filename=", "content-disposition",
}

// ossFeatures name object-storage control-plane coordinates (OSS/S3/MinIO/COS).
var ossFeatures = []string{
	"oss", "s3", "minio", "bucket", "object-storage", "objectstorage",
	"storage.googleapis", "blob.core.windows", "cos.ap-", "aliyuncs",
}

// webControlFeatures name the target platform's own management plane: settings,
// project/task/agent/skill lifecycle and intercept/update controls. These keep a
// human obligation (require) — they are testable, but a wrong call reconfigures
// the product the engagement is meant to leave intact.
var webControlFeatures = []string{
	"/api/settings", "/api/config", "/api/companies", "/api/tasks",
	"/api/agents", "/api/skills", "/api/hostenv", "/api/intercept",
	"/api/update", "/api/mcp", "/api/auth/change-password", "/api/auth/init",
	"/api/users", "/api/roles",
}

// dataExportFeatures name bulk-export endpoints (whole-dataset reads).
var dataExportFeatures = []string{
	"/export", "/dump", "/backup", "/download-all", "/archive",
}

// DefaultScopeRules is the built-in baseline, ordered by severity (deny first).
// It is applied by the scope observer for every run of a bound project.
func DefaultScopeRules() []ScopeRule {
	return []ScopeRule{
		{
			Name:         "no-bulk-data-destruction",
			Decision:     ScopeDeny,
			PathFeatures: dataDestroyFeatures,
			BodyFeatures: dataDestroyFeatures,
		},
		{
			Name:         "no-object-storage-writes",
			Decision:     ScopeDeny,
			PathFeatures: ossFeatures,
		},
		{
			Name:         "no-file-upload",
			Decision:     ScopeDeny,
			PathFeatures: uploadFeatures,
			BodyFeatures: uploadFeatures,
		},
		{
			Name:         "web-control-plane-write",
			Decision:     ScopeRequire,
			PathFeatures: webControlFeatures,
		},
		{
			Name:         "bulk-data-export",
			Decision:     ScopeRequire,
			PathFeatures: dataExportFeatures,
		},
	}
}

// EvaluateScopeRules classifies one HTTP call against the given rules. A deny
// always outranks a require; the first matching rule within a severity wins so
// the reason names the most specific entry the operator sees.
func EvaluateScopeRules(rules []ScopeRule, method, rawURL, body string) ScopeMatch {
	m := normalizeMethod(method)
	if _, ok := writeMethods[m]; !ok {
		return ScopeMatch{}
	}
	path, query := splitURLPathQuery(rawURL)
	host := strings.ToLower(splitURLHost(rawURL))
	// splitURLPathQuery trims the leading slash; features are written with it
	// ("/api/settings"), so restore it before matching.
	pathLower := "/" + strings.ToLower(path)
	queryLower := strings.ToLower(query)
	bodyLower := strings.ToLower(body)
	// Path features are evaluated against "host path": object-storage
	// vocabulary (oss / s3 / bucket / …) typically lives in the hostname, and
	// folding it in keeps one feature list meaningful for both coordinates.
	hostPathLower := host + " " + pathLower

	var require ScopeMatch
	for _, rule := range rules {
		if len(rule.Methods) > 0 && !methodIn(rule.Methods, m) {
			continue
		}
		// Location groups are OR-ed (a rule matching either the endpoint or the
		// payload is enough); HostFeatures stays an additional constraint when
		// populated.
		groups, hit := 0, false
		if len(rule.PathFeatures) > 0 {
			groups++
			hit = hit || matchesFeatures(rule.PathFeatures, hostPathLower)
		}
		if len(rule.BodyFeatures) > 0 {
			groups++
			hit = hit || matchesFeatures(rule.BodyFeatures, bodyLower) ||
				matchesFeatures(rule.BodyFeatures, queryLower)
		}
		if groups > 0 && !hit {
			continue
		}
		if !matchesFeatures(rule.HostFeatures, host) {
			continue
		}
		match := ScopeMatch{
			Rule:     rule.Name,
			Decision: rule.Decision,
			Reason:   rule.Name + " (scope observer baseline)",
		}
		if rule.Decision == ScopeDeny {
			return match
		}
		if require.Decision == ScopeNone {
			require = match
		}
	}
	return require
}

func normalizeMethod(method string) string {
	m := strings.ToUpper(strings.TrimSpace(method))
	if m == "" {
		return "GET"
	}
	return m
}

func methodIn(methods []string, method string) bool {
	for _, candidate := range methods {
		if strings.EqualFold(strings.TrimSpace(candidate), method) {
			return true
		}
	}
	return false
}

// matchesFeatures reports whether any feature names an entry. An empty feature
// list is a wildcard (the group imposes no constraint).
func matchesFeatures(features []string, haystack string) bool {
	if len(features) == 0 {
		return true
	}
	if haystack == "" {
		return false
	}
	for _, feature := range features {
		f := strings.TrimSpace(feature)
		if f == "" {
			continue
		}
		if strings.Contains(haystack, strings.ToLower(f)) {
			return true
		}
	}
	return false
}
