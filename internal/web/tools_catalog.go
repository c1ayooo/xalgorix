package web

// tools_catalog.go — read-only catalog of the built-in tool surface the scan
// agent registers (internal/agent/agent.go NewAgent + the register*Tool
// helpers). It backs GET /api/tools, the "Available" tab of the Tools page.
//
// This is a DIRECTORY, not a registry: nothing here executes, gates or
// validates. The authoritative registration lives in internal/agent; when a
// tool is added/renamed there, mirror the name here (the catalog is what the
// operator reads, so drift is a documentation bug, never an execution one).
//
// Categories are stable keys ("terminal", "network", …) rendered through the
// frontend i18n dictionary; approval mirrors approval.classifyCall (command /
// http / browser kinds, plus MCP-bridged rows which are command-class by
// default). An empty approval value means the tool is not approval-gated.

// ToolCatalogParameter is one tool argument as shown in the catalog.
type ToolCatalogParameter struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Desc     string `json:"desc,omitempty"`
}

// ToolCatalogEntry is one built-in tool.
type ToolCatalogEntry struct {
	Name        string                 `json:"name"`
	Category    string                 `json:"category"`
	Description string                 `json:"description"`
	Approval    string                 `json:"approval,omitempty"`
	Parameters  []ToolCatalogParameter `json:"parameters"`
}

// toolCatalogResponse is the GET /api/tools envelope.
type toolCatalogResponse struct {
	Tools      []ToolCatalogEntry `json:"tools"`
	Categories []string           `json:"categories"`
}

func param(name string, required bool, desc string) ToolCatalogParameter {
	return ToolCatalogParameter{Name: name, Required: required, Desc: desc}
}

// builtinToolCatalog returns the static catalog in registration order.
func builtinToolCatalog() []ToolCatalogEntry {
	return []ToolCatalogEntry{
		// ── Execution ────────────────────────────────────────────────────
		{
			Name: "terminal_execute", Category: "terminal", Approval: "command",
			Description: "Execute a shell command in the scan workspace; returns stdout, stderr and exit code.",
			Parameters:  []ToolCatalogParameter{param("command", true, "shell command to run"), param("workdir", false, "working directory")},
		},
		{
			Name: "python_action", Category: "terminal", Approval: "command",
			Description: "Run Python 3 code in a subprocess for scripting and payload generation.",
			Parameters:  []ToolCatalogParameter{param("code", true, "python source"), param("timeout", false, "timeout seconds")},
		},

		// ── Files ────────────────────────────────────────────────────────
		{
			Name: "str_replace_editor", Category: "files",
			Description: "View, create or edit workspace files (view / create / str_replace / insert).",
			Parameters: []ToolCatalogParameter{
				param("command", true, "view|create|str_replace|insert"), param("path", true, "file path"),
				param("old_str", false, "text to find"), param("new_str", false, "replacement text"),
				param("insert_line", false, "line to insert after"), param("view_range", false, "line range, e.g. 1-50"),
				param("file_text", false, "full content for create"),
			},
		},
		{
			Name: "list_files", Category: "files",
			Description: "List files and directories at the given path.",
			Parameters:  []ToolCatalogParameter{param("path", true, "directory path")},
		},
		{
			Name: "search_files", Category: "files",
			Description: "Search file contents with ripgrep.",
			Parameters:  []ToolCatalogParameter{param("pattern", true, "regex"), param("path", false, "root directory"), param("include", false, "glob filter")},
		},

		// ── Network ─────────────────────────────────────────────────────
		{
			Name: "http_request", Category: "network", Approval: "http",
			Description: "Structured HTTP request (status, headers, body) through the six-ring guard chain.",
			Parameters: []ToolCatalogParameter{
				param("url", true, "target URL"), param("method", false, "HTTP method"),
				param("headers", false, "JSON headers"), param("body", false, "request body"),
			},
		},
		{
			Name: "send_request", Category: "network", Approval: "http",
			Description: "Send an HTTP request through the configured outbound proxy (fail-closed when proxy is required).",
			Parameters:  []ToolCatalogParameter{param("method", true, "HTTP method"), param("url", true, "target URL"), param("headers", false, "JSON headers"), param("body", false, "request body")},
		},
		{
			Name: "list_requests", Category: "network",
			Description: "List this run's MITM traffic archive (raw request/response); seq=<n> returns one exchange's wire packets.",
			Parameters:  []ToolCatalogParameter{param("count", false, "max rows"), param("filter", false, "URL substring"), param("method", false, "HTTP method"), param("seq", false, "exact exchange id")},
		},

		// ── Browser ─────────────────────────────────────────────────────
		{
			Name: "browser_action", Category: "browser", Approval: "browser",
			Description: "Headless Chromium control: navigation, snapshots, clicks, form fills, JS execution, cookie/session handling.",
			Parameters:  []ToolCatalogParameter{param("command", true, "action verb"), param("url", false, "target URL"), param("selector", false, "element selector"), param("text", false, "text payload")},
		},
		{
			Name: "page_agent", Category: "browser",
			Description: "Page-level agent for multi-step in-browser interaction (goal, iframe and selector driven).",
			Parameters:  []ToolCatalogParameter{param("command", true, "action"), param("action", false, "step action"), param("selector", false, "element selector"), param("text", false, "input text"), param("timeout", false, "timeout seconds")},
		},
		{
			Name: "discover_client_routes", Category: "browser",
			Description: "Extract client-side route declarations from a rendered page.",
			Parameters:  []ToolCatalogParameter{param("url", false, "page URL"), param("proxy", false, "proxy override")},
		},

		// ── Recon ───────────────────────────────────────────────────────
		{
			Name: "web_search", Category: "recon",
			Description: "Web search for up-to-date information (provider-native or Google/Bing/Brave/DuckDuckGo).",
			Parameters:  []ToolCatalogParameter{param("query", true, "search query"), param("max_results", false, "max results")},
		},
		{
			Name: "cve_search", Category: "recon",
			Description: "Look up CVEs through the NIST NVD API.",
			Parameters:  []ToolCatalogParameter{param("query", true, "CVE/product query")},
		},
		{
			Name: "exploit_search", Category: "recon",
			Description: "Search public exploit sources for a product/version or CVE.",
			Parameters:  []ToolCatalogParameter{param("query", true, "product, version or CVE")},
		},
		{
			Name: "code_search", Category: "recon",
			Description: "Search the attached source tree for sinks and patterns (whitebox).",
			Parameters:  []ToolCatalogParameter{param("query", true, "search query"), param("sinks", false, "sink classes"), param("glob", false, "glob filter"), param("path", false, "root path"), param("max", false, "max hits")},
		},
		{
			Name: "oob_callback", Category: "recon",
			Description: "Out-of-band interaction oracle: generate a callback token or poll for callbacks.",
			Parameters:  []ToolCatalogParameter{param("action", false, "generate|poll"), param("token", false, "callback token")},
		},

		// ── Notes & intelligence ────────────────────────────────────────
		{
			Name: "add_note", Category: "notes",
			Description: "Record a durable note for this scan (credentials, discovered surface, decisions).",
			Parameters:  []ToolCatalogParameter{param("key", false, "note key"), param("value", true, "note content")},
		},
		{
			Name: "read_notes", Category: "notes",
			Description: "Read the notes recorded for this scan.",
			Parameters:  []ToolCatalogParameter{param("key", false, "optional note key")},
		},
		{
			Name: "record_intelligence", Category: "intelligence",
			Description: "Record a credential / lead / sensitive-data item into task intelligence (fingerprint deduped).",
			Parameters:  []ToolCatalogParameter{param("kind", true, "credential|lead|sensitive_data"), param("title", true, "short title"), param("content", true, "detail, ≤4k"), param("endpoint", false, "endpoint"), param("confidence", false, "0.0-1.0")},
		},
		{
			Name: "read_intelligence", Category: "intelligence",
			Description: "Identity-only listing by default; pass a fingerprint (or I-N id) to read one item's full content.",
			Parameters:  []ToolCatalogParameter{param("fingerprint", false, "fp= value or I-N item id")},
		},
		{
			Name: "search_info", Category: "intelligence",
			Description: "Read-only InfoItem activation over the PostgreSQL cold path (region / association / context).",
			Parameters:  []ToolCatalogParameter{param("mode", true, "region|association|context"), param("subject", false, "subject identifier"), param("ids", false, "association start ids"), param("max_depth", false, "1-3"), param("limit", false, "1-200")},
		},
		{
			Name: "read_tool_output", Category: "notes",
			Description: "Read an archived tool output that was spilled to disk and replaced by a reference id.",
			Parameters:  []ToolCatalogParameter{param("id", true, "archive id")},
		},

		// ── Deterministic analysis ──────────────────────────────────────
		{
			Name: "detect_honeypot", Category: "analysis",
			Description: "Score an observed service against the static honeypot signature library (0-1 plus matched signatures).",
			Parameters: []ToolCatalogParameter{
				param("port", false, "service port"), param("banner", false, "protocol banner / Server header"),
				param("title", false, "HTTP title"), param("body", false, "response body excerpt"),
				param("cert_subject", false, "TLS subject"), param("cert_issuer", false, "TLS issuer"),
			},
		},
		{
			Name: "assess_nuclei_hit", Category: "analysis",
			Description: "Deterministically triage one scanner/nuclei hit: class, confirmed|needs_review verdict, reasons and review checklist.",
			Parameters: []ToolCatalogParameter{
				param("template_id", false, "template id"), param("name", false, "result name"),
				param("tags", false, "comma-separated tags"), param("type", false, "result type"),
				param("status", false, "HTTP status"), param("response", false, "response excerpt"),
				param("extracted", false, "comma-separated extracted values"), param("matched_at", false, "matched URL"),
				param("template", false, "template YAML"), param("template_path", false, "template file path"),
			},
		},
		{
			Name: "parse_poc_template", Category: "analysis",
			Description: "Parse a nuclei template into normalized fields (id, severity, CVE/CWE, CVSS, tags, digest) keeping raw YAML intact.",
			Parameters:  []ToolCatalogParameter{param("content", false, "raw YAML"), param("path", false, "template file path")},
		},
		{
			Name: "diff_responses", Category: "analysis",
			Description: "Structurally diff two captured HTTP responses or JSON bodies; filters timestamps and trace ids.",
			Parameters:  []ToolCatalogParameter{param("file_a", true, "baseline file"), param("file_b", true, "comparison file")},
		},
		{
			Name: "solve_slider_captcha", Category: "analysis",
			Description: "Solve an image-based slider captcha from background + piece images; returns offset, distance and optional drag track.",
			Parameters: []ToolCatalogParameter{
				param("background", true, "background image path/data URL/base64"), param("piece", true, "piece image path/data URL/base64"),
				param("initial_x", false, "piece visible left edge"), param("min_offset", false, "search lower bound"),
				param("max_offset", false, "search upper bound"), param("with_track", false, "include drag track"),
				param("seed", false, "track seed"),
			},
		},

		// ── Skills ──────────────────────────────────────────────────────
		{
			Name: "read_skill", Category: "skills",
			Description: "Load a methodology skill (built-in corpus merged with operator-authored skills).",
			Parameters:  []ToolCatalogParameter{param("skill_name", true, "skill name")},
		},
		{
			Name: "list_skills", Category: "skills",
			Description: "List available methodology skills with categories and tags.",
			Parameters:  []ToolCatalogParameter{param("category", false, "category filter")},
		},
		{
			Name: "search_skills", Category: "skills",
			Description: "Search skills by intent, tag or free text.",
			Parameters:  []ToolCatalogParameter{param("query", true, "search text"), param("category", false, "category filter"), param("max", false, "max results")},
		},

		// ── Plan & ledger ───────────────────────────────────────────────
		{
			Name: "build_plan", Category: "plan",
			Description: "Author the structured task plan (tasks with phase, vuln class, dependencies).",
			Parameters:  []ToolCatalogParameter{param("tasks", true, "JSON array of tasks")},
		},
		{
			Name: "update_plan", Category: "plan",
			Description: "Advance a plan task: active/completed/skipped or a typed disposition with rationale.",
			Parameters:  []ToolCatalogParameter{param("task_id", false, "plan task id"), param("status", false, "status or disposition"), param("notes", false, "rationale")},
		},
		{
			Name: "record_hypothesis", Category: "ledger",
			Description: "Record a testable hypothesis (belief) in the shared evidence ledger.",
			Parameters:  []ToolCatalogParameter{param("vuln_class", true, "vulnerability class"), param("preconditions", true, "preconditions"), param("endpoint", false, "endpoint"), param("parameter", false, "parameter"), param("confidence", false, "0.0-1.0")},
		},
		{
			Name: "add_hypothesis_evidence", Category: "ledger",
			Description: "Attach evidence to a hypothesis, separating observed facts from inferred interpretation.",
			Parameters:  []ToolCatalogParameter{param("hypothesis_id", true, "hypothesis id"), param("summary", true, "what was observed"), param("kind", false, "evidence kind"), param("nature", false, "observed|inferred"), param("supports", false, "supporting evidence ids")},
		},
		{
			Name: "update_hypothesis", Category: "ledger",
			Description: "Advance a hypothesis lifecycle (queued/testing/proven/rejected/blocked/exhausted).",
			Parameters:  []ToolCatalogParameter{param("hypothesis_id", true, "hypothesis id"), param("status", true, "lifecycle status"), param("next_action", false, "next step"), param("assigned_to", false, "owner")},
		},
		{
			Name: "read_ledger", Category: "ledger",
			Description: "Read the shared hypothesis/evidence ledger (open / schedulable / all).",
			Parameters:  []ToolCatalogParameter{param("filter", false, "open|schedulable|all"), param("limit", false, "max hypotheses")},
		},
		{
			Name: "claim_next_hypothesis", Category: "ledger",
			Description: "Atomically claim the highest-value untested hypothesis.",
			Parameters:  []ToolCatalogParameter{param("vuln_class", false, "restrict to a class")},
		},

		// ── Deterministic verifiers & probes ────────────────────────────
		{
			Name: "verify_oob", Category: "verify",
			Description: "Confirm a blind vulnerability by polling the OAST oracle for a planted token.",
			Parameters:  []ToolCatalogParameter{param("token", true, "planted token"), param("vuln_class", false, "vulnerability class"), param("endpoint", false, "endpoint")},
		},
		{
			Name: "verify_timing", Category: "verify",
			Description: "Bounded timing-differential verifier for blind server-side injection.",
			Parameters:  []ToolCatalogParameter{param("url", true, "probe URL"), param("expected_delay_ms", false, "delay threshold"), param("trials", false, "repetitions")},
		},
		{
			Name: "verify_sqli", Category: "verify",
			Description: "Deterministic error-based SQL-injection confirmer (baseline vs quote payloads).",
			Parameters:  []ToolCatalogParameter{param("url", false, "probe URL"), param("parameter", false, "injected parameter"), param("hypothesis_id", false, "hypothesis id")},
		},
		{
			Name: "verify_ssti", Category: "verify",
			Description: "Deterministic SSTI confirmer using randomized arithmetic payloads.",
			Parameters:  []ToolCatalogParameter{param("url", false, "probe URL"), param("parameter", false, "injected parameter"), param("hypothesis_id", false, "hypothesis id")},
		},
		{
			Name: "verify_path_traversal", Category: "verify",
			Description: "Deterministic directory-traversal confirmer with literal ../ sequences.",
			Parameters:  []ToolCatalogParameter{param("url", false, "probe URL"), param("hypothesis_id", false, "hypothesis id")},
		},
		{
			Name: "verify_authbypass", Category: "verify",
			Description: "Confirm authentication/authorization bypass via path-shape variants (deny→allow flip).",
			Parameters:  []ToolCatalogParameter{param("url", false, "protected route"), param("hypothesis_id", false, "hypothesis id")},
		},
		{
			Name: "verify_xxe", Category: "verify",
			Description: "Deterministic XXE confirmer (local-file entity probes with randomized controls).",
			Parameters:  []ToolCatalogParameter{param("url", false, "probe URL"), param("hypothesis_id", false, "hypothesis id"), param("file", false, "target file")},
		},
		{
			Name: "verify_csrf", Category: "verify",
			Description: "Confirm CSRF by replaying a state-changing request without the anti-CSRF token.",
			Parameters:  []ToolCatalogParameter{param("url", false, "probe URL"), param("method", false, "HTTP method"), param("data", false, "request body"), param("content_type", false, "content type")},
		},
		{
			Name: "authz_matrix", Category: "verify",
			Description: "Replay one request as role A / role B / anonymous and record the access-control differential.",
			Parameters:  []ToolCatalogParameter{param("url", true, "endpoint"), param("method", false, "HTTP method"), param("parameter", false, "parameter"), param("expect", false, "expected access")},
		},
		{
			Name: "probe_hypothesis", Category: "probe",
			Description: "Issue one scope-gated baseline request for a ledger hypothesis and record the response as evidence.",
			Parameters:  []ToolCatalogParameter{param("hypothesis_id", true, "hypothesis id"), param("base_url", false, "base URL"), param("method", false, "HTTP method")},
		},
		{
			Name: "probe_body_contract", Category: "probe",
			Description: "Discover required/recognized JSON body fields by mutating and diffing validation responses.",
			Parameters:  []ToolCatalogParameter{param("url", true, "endpoint"), param("seed_body", false, "seed JSON"), param("fields", false, "candidate fields")},
		},

		// ── Whitebox ingestion ─────────────────────────────────────────
		{
			Name: "ingest_har", Category: "source",
			Description: "Turn a logged-in HAR into authenticated scan context and seed authorization hypotheses.",
			Parameters:  []ToolCatalogParameter{param("path", true, "HAR file path"), param("target", false, "target scope"), param("role", false, "role label")},
		},
		{
			Name: "scan_source_sinks", Category: "source",
			Description: "Sweep the attached source tree for dangerous sinks and seed source→sink hypotheses.",
			Parameters:  []ToolCatalogParameter{param("max_per_class", false, "cap per sink class")},
		},
		{
			Name: "scan_source_routes", Category: "source",
			Description: "Extract HTTP route declarations from the attached source and seed reachable-path hypotheses.",
			Parameters:  []ToolCatalogParameter{param("max", false, "max routes")},
		},

		// ── Delegation ─────────────────────────────────────────────────
		{
			Name: "create_agent", Category: "agent",
			Description: "Create a specialist agent profile for delegation (persistent role definition).",
			Parameters:  []ToolCatalogParameter{param("name", true, "specialist role"), param("task", true, "bounded task"), param("target", false, "target")},
		},
		{
			Name: "spawn_agent", Category: "agent",
			Description: "Spawn a delegated specialist to work one bounded task in parallel.",
			Parameters:  []ToolCatalogParameter{param("name", true, "specialist role"), param("task", true, "bounded task"), param("target", false, "target")},
		},
		{
			Name: "check_agent", Category: "agent",
			Description: "Check whether a delegated agent has finished and collect its status.",
			Parameters:  []ToolCatalogParameter{param("agent_id", true, "agent id")},
		},
		{
			Name: "wait_agent", Category: "agent",
			Description: "Wait for a delegated agent to finish (bounded by timeout).",
			Parameters:  []ToolCatalogParameter{param("agent_id", true, "agent id"), param("timeout", false, "seconds, max 3600")},
		},

		// ── Reporting ──────────────────────────────────────────────────
		{
			Name: "report_vulnerability", Category: "reporting",
			Description: "Report a finding through the gate pipeline (dedup → fabrication → method → proof → CVSS → verifier → severity cap).",
			Parameters:  []ToolCatalogParameter{param("title", true, "finding title"), param("severity", true, "severity"), param("description", true, "description"), param("exploitation_proof", false, "proof"), param("verification_method", false, "verification method"), param("cvss", false, "CVSS score"), param("cvss_vector", false, "CVSS vector"), param("cwe_id", false, "CWE id"), param("hypothesis_id", false, "hypothesis id")},
		},
		{
			Name: "finish", Category: "reporting",
			Description: "Finish the run with a summary; blocked while obligations are unmet.",
			Parameters:  []ToolCatalogParameter{param("summary", true, "completion summary")},
		},
	}
}

// toolCatalogCategories returns the distinct categories in catalog order.
func toolCatalogCategories(entries []ToolCatalogEntry) []string {
	seen := make(map[string]bool, len(entries))
	out := make([]string, 0, 8)
	for _, e := range entries {
		if seen[e.Category] {
			continue
		}
		seen[e.Category] = true
		out = append(out, e.Category)
	}
	return out
}
