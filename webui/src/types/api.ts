// Types mirror Go structs in internal/web/server.go.
// These are inferred from the actual backend, not invented.

export interface VulnSummary {
  id: string;
  source_scan_id?: string;
  title: string;
  severity: string;
  target?: string;
  endpoint: string;
  cvss: number;
  cvss_vector?: string;
  description?: string;
  impact?: string;
  method?: string;
  cve?: string;
  cwe_id?: string;
  owasp?: string;
  technical_analysis?: string;
  poc_description?: string;
  poc_script?: string;
  remediation?: string;
  fix?: string;
  exploitation_proof?: string;
  verification_method?: string;
  verified?: boolean;
  // Machine-readable labels. Always carries a verification tag:
  // "verified" (independently reproduced) or "needs-manual-verification"
  // (preserved but not confirmed — a human must review it).
  tags?: string[];
}

// One raw HTTP request/response pair from the per-scan traffic archive
// (GET /api/scans/{id}/traffic). request_raw / response_raw are wire-fidelity
// renderings the proxy captured; nothing is masked.
export interface HttpExchange {
  seq: number;
  at: string;
  method: string;
  url: string;
  // Derived by the server from the exchange URL (lowercased hostname, port
  // stripped). It is the join key against discovered assets (run target +
  // sub-scan targets) and lets the UI group raw packets per asset.
  host?: string;
  status?: number;
  channel?: string; // bearer / subprocess
  high_risk?: string; // AssessHTTP reason when the request pended the proxy gate
  denied?: boolean; // 403 by the proxy-side approval gate
  upstream?: string;
  request_raw?: string;
  response_raw?: string;
  request_truncated?: boolean;
  response_truncated?: boolean;
}

// GET /api/scans/{id}/traffic response. present distinguishes "no traffic
// archive exists for this scan" from an empty match.
export interface ScanTrafficView {
  scan_id: string;
  exchanges: HttpExchange[];
  total_matched: number;
  truncated: boolean;
  present: boolean;
}

export interface WSEvent {
  type: string;
  // SSE 实时帧的全局流序号（/api/stream id: 行同源）。仅实时扇出携带，
  // REST 历史条目没有——只作 live 排序锚点，不参与 dedupe（dedupe 走
  // ts|instance|type|tool|msg 键）。
  seq?: number;
  content?: string;
  tool_name?: string;
  tool_args?: Record<string, string>;
  output?: string;
  error?: string;
  agent_id?: string;
  instance_id?: string;
  timestamp?: string;
  vulns?: VulnSummary[];
  target_index?: number;
  total_targets?: number;
  target?: string;
  total_tokens?: number;
  sub_target_index?: number;
  sub_target_total?: number;
  parent_target?: string;
  current_phase?: number;
  // HITL（契约 §3/§3.1）：approval_requested 事件携带完整审批单，
  // guidance_* 事件携带人工意图记录。事件流据此渲染行内决策条。
  approval?: Approval;
  guidance?: Guidance;
  // 对话流（4zreco transcript 模式）：assistant_delta 增量推送 LLM 输出，
  // message_id 标识同一条消息；assistant_done 携带全文固化入库。
  message_id?: string;
  delta?: string;
}

export interface AssessmentOutcome {
  completion?: "full" | "partial";
  plan_present?: boolean;
  plan_tasks_total?: number;
  plan_tasks_completed?: number;
  plan_tasks_skipped?: number;
  plan_tasks_unfinished?: number;
}

export interface ScanInstance extends AssessmentOutcome {
  id: string;
  name?: string;
  targets: string;
  parent_target?: string;
  status: string;
  started_at: string;
  finished_at?: string;
  stop_reason?: string;
  iterations: number;
  tool_calls: number;
  vuln_count: number;
  total_tokens: number;
  scan_mode: string;
  instruction?: string;
  severity_filter?: string[];
  phases?: number[];
  recon_mode?: "active" | "passive";
  scan_intensity?: "active" | "passive";
  company_name?: string;
  logo_path?: string;
  vulns?: VulnSummary[];
  current_phase?: number;
  phase_status?: Record<string, string>;
  phases_worked?: number[];
  sub_scans?: SubScanSummary[];
  sub_scan_total?: number;
  sub_scan_completed?: number;
  sub_scan_running?: number;
  sub_scan_remaining?: number;
  discovery?: SubScanSummary;
  sub_scan_skipped?: number;
}

export interface SubScanSummary extends AssessmentOutcome {
  id: string;
  target: string;
  started_at?: string;
  finished_at?: string;
  status: string;
  vuln_count: number;
  total_tokens: number;
  stop_reason?: string;
}

export interface ScanRecord extends AssessmentOutcome {
  id: string;
  instance_id?: string;
  // RESPONSE-ONLY linkage hints (server resolves the platform run mount at
  // read time; empty = unmounted legacy scan). The guidance composer needs
  // task_id to reach POST /api/tasks/{id}/guidance and hides without it.
  run_id?: string;
  task_id?: string;
  name?: string;
  target: string;
  parent_target?: string;
  started_at: string;
  finished_at?: string;
  status: string;
  stop_reason?: string;
  scan_mode?: string;
  instruction?: string;
  severity_filter?: string[];
  discord_webhook?: string;
  discord_webhook_configured?: boolean;
  telegram_configured?: boolean;
  recon_mode?: "active" | "passive";
  scan_intensity?: "active" | "passive";
  events: WSEvent[];
  // GET /api/scans/{id} returns only the most recent events inline. When the
  // scan's log is longer, events_truncated is true and events_total is the
  // full count; older events are paged via GET /api/scans/{id}/events.
  events_total?: number;
  events_truncated?: boolean;
  vulns: VulnSummary[];
  total_tokens: number;
  iterations: number;
  tool_calls: number;
  company_name?: string;
  logo_path?: string;
  phases?: number[];
  current_phase?: number;
  // phase_status is the authoritative per-phase disposition derived by the
  // backend from engine evidence (plan tasks, hypothesis ledger, recon
  // dimensions, terminal lifecycle): "completed" | "active" | "pending" |
  // "blocked" | "not_applicable" | "not_selected", keyed by phase id.
  phase_status?: Record<string, string>;
  // phases_worked lists phases the engine observed concrete work for.
  phases_worked?: number[];
  sub_scans?: SubScanSummary[];
  sub_scan_total?: number;
  sub_scan_completed?: number;
  sub_scan_running?: number;
  sub_scan_remaining?: number;
  discovery?: SubScanSummary;
  sub_scan_skipped?: number;
}

/** One page of a scan's event log, from GET /api/scans/{id}/events. */
export interface ScanEventsPage {
  events: WSEvent[];
  total: number;
  offset: number;
  limit: number;
}

/**
 * Read-only artifact/workspace browser (GET /api/scans/{id}/files*).
 * `type` distinguishes openable entries from symlinks (listed for visibility,
 * refused on open) and special files (devices/fifos).
 */
export type ScanFileType = "dir" | "file" | "link" | "other";

/** One row of a single-level directory listing. */
export interface ScanFileEntry {
  name: string;
  type: ScanFileType;
  size: number;
  /** Unix epoch milliseconds. */
  mtime: number;
}

/** Single-level listing of one directory inside the scan dir. */
export interface ScanFileListing {
  path: string;
  entries: ScanFileEntry[];
  /** True when the server capped the listing (deeper levels are fetched lazily). */
  truncated: boolean;
}

/** Bounded inline text read of one file. Binary files carry no content. */
export interface ScanFileContent {
  path: string;
  size: number;
  mtime: number;
  truncated: boolean;
  binary: boolean;
  /** Present only for text reads; omitted for binary files. */
  content?: string;
}

export interface ScanListItem {
  id: string;
  target: string;
  started_at: string;
  status: string;
  scan_mode?: string;
  stop_reason?: string;
  // RESPONSE-ONLY run/task mount linkage (D11; stamped from the platform
  // store at read time). Empty = unmounted legacy scan — honest "no task".
  task_id?: string;
  task_name?: string;
  trigger?: string;
  vuln_count: number;
  total_tokens: number;
  sub_scan_total?: number;
  sub_scan_completed?: number;
  sub_scan_running?: number;
  sub_scan_remaining?: number;
}

/** Generic server-side pagination envelope: { items, total, page, size }. */
export interface Paginated<T> {
  items: T[];
  total: number;
  page: number;
  size: number;
}

/** GET /api/scans/{id}/security — guard refusals + counters for one scan. */
export interface SecurityBlock {
  ts: string;
  category: string;
  tool?: string;
  reason: string;
}

export interface ScanSecurity {
  scan_id: string;
  blocks: SecurityBlock[];
  counters: Record<string, number>;
  truncated: boolean;
}

/** Query params accepted by the paginated list endpoints. */
export interface ListParams {
  page: number;
  size: number;
  q?: string;
  status?: string;
  mode?: string;
  /** Project isolation filter (wire name: project_id). */
  projectId?: string;
}

export interface InstancesResponse {
  instances: ScanInstance[];
  // Present when the request used server-side pagination/filtering.
  total?: number;
  page?: number;
  size?: number;
  /** Distinct scan modes across all instances, for the filter dropdown. */
  modes?: string[];
  resources: {
    cpu_cores: number;
    cpu_load_1m: number;
    ram_total_mb: number;
    ram_available_mb: number;
    disk_free_mb: number;
    process_rss_mb?: number;
    go_heap_alloc_mb?: number;
    go_heap_sys_mb?: number;
    goroutines?: number;
    level: string;
    reason: string;
    max_instances: number;
    manual_max_instances: number;
    effective_max_instances: number;
    running_instances?: number;
    unreflected_admissions?: number;
    available_instance_slots?: number;
    active_tool_leases?: number;
    active_heavy_tool_leases?: number;
    heavy_tool_slots?: number;
    light_tool_slots?: number;
    tool_mem_limit_mb?: number;
    scan_memory_budget_mb?: number;
    heavy_tool_cpu_load?: number;
    go_memory_limit_mb?: number;
  };
}

export interface StatusResponse {
  running: boolean;
  scan_id: string;
  instance_id: string;
  current_phase: number;
  vulns: number;
  running_instances: number;
}

/**
 * 工具运行时（执行面）只读投影。/api/runtime 返回的字段全部是派生值：
 * 容器实际状态由运行时持有，期望态与调和水位在 PostgreSQL。
 */
export interface RuntimeStatus {
  backend: "native" | "docker";
  transport?: "socket" | "runnerd" | string;
  profile?: "core" | "full" | "web" | string;
  image?: string;
  image_digest?: string;
  project_limit: number;
  projects_active: number;
  exec_queue_depth: number;
  project_slot_used: number;
  project_slot_max: number;
  healthy: boolean;
  reason?: string;
  reconciled_at: string;
}

export interface VersionInfo {
  version: string;
  ai?: {
    configured: boolean;
    provider: string;
    model?: string;
    gateway?: string;
  };
}

// RBAC role carried in JWT claims, /api/users and the auth status payload.
export type Role = "admin" | "operator" | "viewer";

export interface AuthUser {
  username: string;
  role: Role;
}

export interface AuthStatus {
  auth_enabled: boolean;
  authenticated: boolean;
  // True when no account exists and no env credentials can seed one — the
  // login page then offers the first-admin bootstrap form.
  needs_setup: boolean;
  // Authenticated identity (cookie session); null when anonymous.
  user: AuthUser | null;
}

export interface ManagedUser {
  id: string;
  username: string;
  role: Role;
  disabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface ScanRequest {
  targets: string[];
  instruction?: string;
  scan_mode?: string;
  model?: string;
  api_key?: string;
  api_base?: string;
  discord_webhook?: string;
  severity_filter?: string[];
  name?: string;
  save_only?: boolean;
  phases?: number[];
  recon_mode?: "active" | "passive";
  scan_intensity?: "active" | "passive";
  company_name?: string;
  logo_path?: string;
  // Server-side path to an uploaded context artifact (OpenAPI/Swagger, HAR, or
  // Postman collection). The engine parses it into a seeded attack surface and
  // harvests any captured auth. Set via POST /api/upload-context.
  scan_context?: string;
  // Authenticated-session material applied automatically to http_request
  // ("Cookie: …; Authorization: Bearer …"). Enables post-login testing.
  target_auth?: string;
  // A SECOND account's auth (same format), surfaced to the agent to prove
  // horizontal access-control flaws (IDOR/BOLA). Not auto-applied.
  target_auth_b?: string;
  // Optional "<provider>:<profileId>" key naming the AuthProfile to
  // use for this scan. When unset the legacy / catalog-default path
  // applies. Mirrors the Go ScanRequest.ProviderProfile field on
  // /api/scan and is honored only for authenticated operators
  // (Requirement 11.1, 11.5).
  provider_profile?: string;
}

// ---------------------------------------------------------------------------
// Provider catalog + auth profile contract types
//
// Mirror the JSON shapes returned by the catalog and profile HTTP
// surface added in this feature. The on-the-wire field names are
// pinned by the design — keep these types in sync with
// internal/providers/types.go (CatalogEntry ↔ providers.Entry) and
// internal/auth/profile.go (AuthProfile ↔ auth.Profile, masked at
// the HTTP boundary by internal/web/masks.go).
// ---------------------------------------------------------------------------

// CatalogEntry mirrors providers.Entry. The compiled-in LLM
// provider catalog returned by GET /api/providers in v4.4.22+.
export interface CatalogEntry {
  id: string;
  displayName: string;
  baseURL: string;
  models?: string[];
  headerStyle: "openai" | "openai_responses" | "anthropic" | "gemini";
  // v4.4.22 surfaces AuthMethods so the dashboard can render the
  // matching sub-form (api_key / oauth / none) without duplicating
  // the catalog's policy. Older servers omit this field entirely.
  authMethods?: Array<"api_key" | "oauth" | "none">;
  flow?: "" | "pkce" | "device_code" | "setup_token" | "claude_cli_reuse" | "codex_cli_reuse";
  clientID?: string;
  authorizationEndpoint?: string;
  tokenEndpoint?: string;
  deviceAuthorizationEndpoint?: string;
  revocationEndpoint?: string;
  scopes?: string[];
  audience?: string;
  // Free-form per-provider caveat surfaced as a hint in the LLM
  // tab (beta status, env-var overrides, etc.).
  notes?: string;
}

// AuthProfileType discriminates between the two stored credential
// shapes returned by GET /api/auth/profiles.
export type AuthProfileType = "api_key" | "oauth";

// AuthProfile mirrors auth.Profile. Credential strings (apiKey,
// accessToken, refreshToken) are MASKED on the wire — the server
// returns "****" for empty/short values and "****<last8>" for longer
// ones (see internal/web/masks.go, Requirements 5.1, 5.2). The
// expiresAt and updatedAt fields are RFC3339 / ISO 8601 timestamps.
export interface AuthProfile {
  // Canonical "<provider>:<profileId>" key the rest of the system
  // uses to reference this profile. Older servers (pre-v4.4.22)
  // may omit it; consumers that need a key should fall back to
  // ${provider}:${profileId}.
  key?: string;
  provider: string;
  profileId: string;
  type: AuthProfileType;
  // API_Key fields. Masked credential string when type === "api_key".
  apiKey?: string;
  // hasApiKey / hasAccessToken are convenience booleans returned by
  // the LLM settings handler (handlers_profiles.go returns the
  // masked credential directly; the LLM tab surface adds these to
  // make truthiness checks straightforward).
  hasApiKey?: boolean;
  apiBaseOverride?: string;
  // OAuth fields. Masked credential strings when type === "oauth".
  accessToken?: string;
  hasAccessToken?: boolean;
  refreshToken?: string;
  expiresAt?: string;
  scopes?: string[];
  tokenType?: string;
  requiresReauth?: boolean;
  updatedAt?: string;
}

// OAuthStartMode discriminates the three shapes returned by
// POST /api/auth/profiles/oauth/start. "loopback" includes authURL
// for an ephemeral 127.0.0.1 callback; "device" includes userCode
// and verificationURI for the device-code flow; "paste" returns
// authURL plus a flowId the dashboard feeds back to
// POST /api/auth/profiles/oauth/complete.
export type OAuthStartMode = "loopback" | "device" | "paste";

// OAuthStartSubmode disambiguates the three "paste" variants the
// dashboard renders differently. "paste_code" is the PKCE OOB
// fallback (textarea for the authorization code); "setup_token"
// is the setup_token driver (textarea for the one-time
// vendor-issued token). The empty / unspecified case is the
// claude_cli_reuse confirm-and-import UI (no input field — the
// credential file is already on disk and Complete reads it
// directly). Mirrors the Submode field on auth.StartResult (H9).
export type OAuthStartSubmode = "" | "paste_code" | "setup_token";

// OAuthStartResponse mirrors the auth.StartResult JSON envelope.
// Optional fields are populated per mode:
//   loopback: authURL, expiresAt
//   device:   userCode, verificationURI, expiresAt
//   paste:    authURL, expiresAt, submode
export interface OAuthStartResponse {
  flowId: string;
  mode: OAuthStartMode;
  // submode is only populated for mode === "paste"; older servers
  // omit it entirely. The dashboard treats undefined the same as
  // "" (claude_cli_reuse confirm-and-import) to stay backward-
  // compatible.
  submode?: OAuthStartSubmode;
  authURL?: string;
  userCode?: string;
  verificationURI?: string;
  expiresAt?: string;
}

export interface QueueStatus {
  available: boolean;
  queue_count?: number;
  total_remaining?: number;
  instance_id?: string;
  targets?: string[];
  current_idx?: number;
  remaining?: number;
  instruction?: string;
  scan_mode?: string;
  recon_mode?: "active" | "passive";
  scan_intensity?: "active" | "passive";
  paused?: boolean;
  active_target?: string;
  active_scan_id?: string;
  wildcard_active_target?: string;
  wildcard_active_scan_id?: string;
  wildcard_sub_index?: number;
  wildcard_subdomains_total?: number;
  started_at?: string;
}

export interface RateLimitSettings {
  requests: number;
  window: number;
}

export interface LLMSettings {
  model: string;
  apiBase: string;
  apiKey: string;
  hasApiKey: boolean;
  reasoningEffort: string;
  ollamaCompatible: boolean;
  llmMaxRetries: number;
  memoryCompressorTimeout: number;
  maxIterations: number;
  geminiApiKey: string;
  hasGeminiApiKey: boolean;
  envFile: string;
  // v4.4.22: catalog-aware fields driving the new LLM Settings tab.
  // Provider mirrors the active provider id. AuthMethod tracks
  // which branch the resolver currently dispatches through.
  // Profiles is the masked list of saved profiles for the active
  // provider only.
  provider?: string;
  authMethod?: "" | "api_key" | "oauth" | "none";
  activeProfileKey?: string;
  profiles?: LLMProfileSummary[];
}

// LLMProfileSummary is the masked, dashboard-friendly view of one
// auth.Profile filtered to the active provider in the LLM tab.
export interface LLMProfileSummary {
  key: string;
  provider: string;
  profileId: string;
  type: AuthProfileType;
  hasAccessToken: boolean;
  hasApiKey: boolean;
  apiBaseOverride?: string;
  expiresAt?: string;
  requiresReauth?: boolean;
}

// LLMSettingsRequest is the POST body shape accepted by
// PUT /api/settings/llm. The v4.4.22 fields (provider/authMethod/
// profileId/activeProfileKey/apiBaseOverride) drive the catalog-
// aware path; if any of those is supplied the handler dispatches
// through applyCatalogLLMSettings. Otherwise the legacy fields
// (model/apiBase/apiKey/...) take the v4.4.21 free-text path.
export interface LLMSettingsRequest {
  model?: string;
  apiBase?: string;
  apiKey?: string;
  reasoningEffort?: string;
  ollamaCompatible?: boolean;
  llmMaxRetries?: number;
  memoryCompressorTimeout?: number;
  maxIterations?: number;
  geminiApiKey?: string;
  // v4.4.22 catalog-aware fields.
  provider?: string;
  authMethod?: "api_key" | "oauth" | "none";
  profileId?: string;
  apiBaseOverride?: string;
  activeProfileKey?: string;
}

export interface EnvironmentVariableSetting {
  key: string;
  label: string;
  category: string;
  description: string;
  defaultValue?: string;
  placeholder?: string;
  inputType:
    | "text"
    | "url"
    | "path"
    | "secret"
    | "number"
    | "boolean"
    | "select"
    | "multiselect";
  options?: string[];
  sensitive: boolean;
  requiresRestart: boolean;
  value: string;
  hasValue: boolean;
}

export interface EnvironmentSettings {
  envFile: string;
  variables: EnvironmentVariableSetting[];
  restartRequired?: boolean;
}

export interface ScanSchedule {
  id: string;
  name: string;
  interval: string;
  // Wall-clock time of day the schedule fires, "HH:MM" in 24h form and
  // interpreted in `timezone`. Empty/absent keeps the legacy behavior of
  // running one interval after creation or the last run. For the "hourly"
  // interval only the minutes apply.
  run_at?: string;
  // Day within the interval: weekday for "weekly" (0=Sunday … 6=Saturday) and
  // day of month for "monthly" (1-31, clamped to the last day of shorter
  // months). Ignored for "hourly" and "daily".
  run_day?: number;
  // IANA timezone name `run_at`/`run_day` are read in, e.g.
  // "America/Argentina/Buenos_Aires". Empty means the server's local time.
  timezone?: string;
  next_run: string;
  last_run?: string;
  enabled: boolean;
  targets: string[];
  instruction?: string;
  scan_mode: string;
  severity_filter?: string[];
  phases?: number[];
  recon_mode?: "active" | "passive";
  scan_intensity?: "active" | "passive";
  company_name?: string;
  logo_path?: string;
  discord_webhook?: string;
  model?: string;
  // Optional "<provider>:<profileId>" key naming the AuthProfile this
  // schedule should run scans under (provider-catalog-and-oauth, R14.4).
  // Mirrors ScanRequest.provider_profile; persisted on the schedule
  // and re-applied on each trigger.
  provider_profile?: string;

  // ── project binding & target mode (v4.6) ────────────────────────────────
  // Project the schedule is bound to; every dispatch is scope-checked against
  // its scope (D7). Empty keeps the legacy implicit-default-project behavior.
  project_id?: string;
  // What a tick does:
  //   "scan"         launch a scan from `targets` (legacy default);
  //   "create_task"  create a NEW task in the bound project, then run it;
  //   "trigger_task" run the existing `task_id`.
  mode?: "scan" | "create_task" | "trigger_task";
  task_id?: string;
  // Goal of the task created by a create_task tick.
  task_goal?: string;
  // Per-task context files attached to a created task.
  context_paths?: string[];

  // ── execution window (v4.6) ─────────────────────────────────────────────
  // Daily execution window "HH:MM" in `timezone`; both empty = always eligible.
  // A start after the end is a cross-midnight window.
  window_start?: string;
  window_end?: string;
  // What happens to the running dispatch when the window closes.
  window_end_action?: "pause" | "finish";
  runtime_instance_id?: string;
}

// Response shape of GET /api/findings/summary. Polled every 10s by the
// Findings and Overview pages; counts are deduplicated server-side by
// (target, endpoint, title, severity) so the totals strip and the
// row list always agree. The same query key (qk.findingsSummary) is
// used on both pages so the React Query cache is shared.
export interface FindingsSummaryResponse {
  totals: {
    critical: number;
    high: number;
    medium: number;
    low: number;
    info: number;
  };
  as_of: string;
  etag: string;
}

/**
 * Settings backup & migration (GET/POST /api/settings/export|import).
 * The backup carries plaintext secrets — the export route is operator-only.
 */
export interface SettingsBackup {
  version: number;
  exported_at: string;
  env: Record<string, string>;
  profiles?: Array<Record<string, unknown>>;
  llm_keys?: Array<{
    provider_id: string;
    api_key: string;
    header_style?: string;
    base_url?: string;
  }>;
}

export interface SettingsImportResult {
  ok: boolean;
  env_applied: number;
  env_skipped?: string[];
  profiles_applied: number;
  profile_warnings?: string[];
  llm_keys_applied: number;
  llm_key_warnings?: string[];
  restart_required?: boolean;
  general_warnings?: string[];
}

// ---------------------------------------------------------------------------
// Projects / Tasks / Runs / Approvals — R3 二开契约。
// 形状以 docs/API_CONTRACT.md 为唯一权威；后端实现见 internal/platform（R1/R2）。
// ---------------------------------------------------------------------------

/**
 * 项目围栏（HITL 运行时约束，REDESIGN §5 × D7）。
 *
 * targets/allow_ports 管的是「运行启动时可以点名哪些目标」（dispatch 校验）；
 * fence 管的是「运行期间受审批管辖的调用可以真的碰到什么」——命令字面量、
 * 高危 HTTP、代理侧子进程流量。越界的调用不会出现在待审队列里，而是直接
 * 以 rejected/decided_by="fence" 落审计行并回给模型 ⛔ 拒绝文本。
 */
export interface ProjectFence {
  enforce: boolean;
  /** 显式排除（覆盖 targets）：第三方 SaaS / 共享基础设施 / 禁止触碰的子域。 */
  deny_targets?: string[];
  /** 允许的写方法（大写）；空 = 不限制方法。 */
  allow_http_methods?: string[];
  /** 命中即硬拦截的命令子串（大小写不敏感）。 */
  deny_command_patterns?: string[];
}

export interface ProjectScope {
  targets: string[];
  allow_ports?: number[];
  fence?: ProjectFence;
}

/** GET/PUT /api/projects/{id}/scope 的响应。 */
export interface ProjectScopeView {
  project_id: string;
  scope: ProjectScope;
  fence: ProjectFence;
  /** 写入校验问题（非空 = 该范围不可保存/不可启用）。 */
  problems: string[];
  /** 该项目执行中的 run 数：>0 时改围栏会立即影响后续审批。 */
  active_runs: number;
  /** HITL 模式（enforce/off）。off 时围栏只是配置，不是生效的控制。 */
  approval_mode: string;
  /** 围栏是否真的在拦（enforce 且有 HITL enforce）。 */
  fence_bound: boolean;
  warning?: string;
}

/** 单个候选目标的试算结果。 */
export interface ScopeTargetCheck {
  target: string;
  in_scope: boolean;
  reason?: string;
}

/** POST /api/projects/{id}/scope/validate 的响应（试算，不落库）。 */
export interface ScopeValidateResult {
  ok: boolean;
  problems: string[];
  entries: ScopeTargetCheck[];
  scope: ProjectScope;
  fence: ProjectFence;
  fence_bound: boolean;
  approval_mode: string;
}

export interface Project {
  id: string;
  name: string;
  slug: string;
  scope: ProjectScope;
  created_at: string;
  archived_at?: string;
  task_count?: number;
  active_runs?: number;
}

export interface TaskPhasePolicy {
  phases?: number[];
  severity_filter?: string[];
  scan_mode?: "single" | "wildcard" | "dast";
  intensity?: "active" | "passive";
}

/** Structured task objective (REDESIGN §3.1 "验收说明"). */
export type ObjectiveKind =
  | "engagement"
  | "capability"
  | "finding_class"
  | "coverage";

export type ObjectiveCriterionKind = "finding_class" | "capability" | "coverage";

export type ObjectiveVerifyBy = "deterministic" | "verifier" | "manual";

export type ObjectiveCriterionState =
  | "met"
  | "unmet"
  | "undetermined"
  | "manual_pending";

export interface ObjectiveCriterion {
  id?: string;
  kind: ObjectiveCriterionKind;
  vuln_class?: string;
  capability?: string;
  verify_by: ObjectiveVerifyBy;
  required?: boolean;
}

export interface Objective {
  kind: ObjectiveKind;
  statement?: string;
  criteria?: ObjectiveCriterion[];
  /** 操作者 may/may-not 规则（goal 分析抽取）；只收窄不放大。 */
  constraints?: string[];
}

/** POST /api/projects/{id}/analyze-goal 的判据草案行（R-goal）。 */
export interface GoalProposalCriterion {
  kind: ObjectiveCriterionKind;
  vuln_class?: string;
  capability?: string;
  verify_by: ObjectiveVerifyBy;
  required: boolean;
  rationale?: string;
}

/** 目标意图分析草案：改写陈述 + 可判定判据 + 操作约束，供表单预填后编辑。 */
export interface GoalProposal {
  statement: string;
  criteria?: GoalProposalCriterion[];
  constraints?: string[];
  notes?: string;
}

export interface ObjectiveCriterionStatus extends ObjectiveCriterion {
  id: string;
  state: ObjectiveCriterionState;
  reason?: string;
  evidence_finding_id?: string;
  evidence_tag?: string;
}

export interface ObjectiveReport {
  structured: boolean;
  criteria: ObjectiveCriterionStatus[];
  met: number;
  unmet: number;
  undetermined: number;
  manual_pending: number;
  attainment: number;
}

export interface ObjectiveSchedulable {
  id: string;
  vuln_class?: string;
  endpoint?: string;
  title?: string;
  confidence: number;
  voi?: number;
}

/** GET /api/tasks/{id}/objective — read-only objective evaluation. */
export interface TaskObjectiveView {
  objective: Objective | null;
  structured: boolean;
  report: ObjectiveReport;
  runs: {
    run_id: string;
    scan_id: string;
    status: string;
    report: ObjectiveReport;
  }[];
  ring: string;
  ring_basis: string;
  gaps: string[];
  schedulable: ObjectiveSchedulable[];
}

export interface TaskSchedule {
  interval: "hourly" | "daily" | "weekly" | "monthly";
  time?: string; // "HH:MM"
  timezone?: string; // IANA
}

export interface Task {
  id: string;
  project_id: string;
  name: string;
  goal: string;
  instruction?: string;
  phase_policy?: TaskPhasePolicy;
  schedule?: TaskSchedule;
  context_paths?: string[];
  /** Structured objective; absent = "not structured" (never inferred). */
  objective?: Objective;
  created_at: string;
  updated_at?: string;
  archived_at?: string;
  run_count?: number;
  last_run_at?: string;
  last_run_status?: string;
  finding_count?: number;
}

export interface Run {
  id: string;
  task_id: string;
  legacy_scan_id?: string;
  instance_id?: string;
  mode: string;
  /** Conversation title shown in the chat sidebar (empty for pre-0011 mounts). */
  title?: string;
  pinned?: boolean;
  archived?: boolean;
  status: string;
  started_at?: string;
  finished_at?: string;
  vuln_count: number;
  total_tokens: number;
  trigger?: "manual" | "schedule" | "rerun";
}

/** 语境切片：审批单附带的场景化信息全景（宪章 §4 的第一个一等消费者）。 */
export interface SliceFact {
  content: string;
  kind: "observed" | "inferred" | "human";
  confidence?: number;
}

export interface ApprovalSlice {
  subject?: string;
  purpose?: string;
  facts: SliceFact[];
  contradiction_count?: number;
}

export interface ApprovalPayload {
  // kind = command
  command?: string;
  workdir?: string;
  // kind = http
  method?: string;
  url?: string;
  headers?: Record<string, string>;
  body_excerpt?: string;
  // 命中高危判定的原因（规则名/特征），两种 kind 都可能有
  reason?: string;
}

export type ApprovalStatus =
  | "pending"
  | "approved"
  | "rejected"
  | "timeout"
  | "cancelled";

export interface Approval {
  id: string;
  project_id: string;
  project_name?: string;
  task_id?: string;
  task_name?: string;
  run_id?: string;
  instance_id?: string;
  kind: "command" | "http";
  payload: ApprovalPayload;
  slice?: ApprovalSlice;
  status: ApprovalStatus;
  created_at: string;
  deadline_at: string;
  decided_at?: string;
  decided_by?: string;
  reason?: string;
}

export interface ApprovalsResponse {
  approvals: Approval[];
  /** 全项目待审批数（跨项目过滤不改变该总数），供 OpsPulse 显示。 */
  pending_total: number;
}

export interface ApprovalsSummary {
  pending_total: number;
  nearest_deadline?: string;
}

// 审批豁免规则（approve & remember，契约 §3.4）：两层闸共用查询，
// 同一意图最多问一次。
export interface ApprovalRuleMatch {
  // kind = command
  program?: string; // 程序 basename
  // kind = http
  method?: string;
  host?: string;
  path_prefix?: string;
  // 共享收窄子串：命令文本 / URL+body
  contains?: string;
}

export interface ApprovalRule {
  id: string;
  project_id: string;
  project_name?: string;
  kind: "command" | "http";
  // allow = 豁免（approve & remember，历史语义）；deny = 范围观测 Agent 自动拒绝。
  // 空值等价 allow。
  decision?: "allow" | "deny";
  match: ApprovalRuleMatch;
  reason?: string;
  created_by?: string;
  created_from_approval_id?: string;
  created_at: string;
  expires_at?: string;
  hit_count: number;
  last_hit_at?: string;
}

export interface ApprovalRuleRequest {
  project_id: string;
  kind: "command" | "http";
  decision?: "allow" | "deny";
  match: ApprovalRuleMatch;
  reason?: string;
  expires_at?: string;
  created_from_approval_id?: string;
}

// 「批准并记住」的前端预填草稿（lib/approval-rule.ts 产出，卡片表单可编辑）。
export type ApprovalRuleDraft = {
  kind: "command" | "http";
  match: ApprovalRuleMatch;
};

export interface TaskRequest {
  name: string;
  goal: string;
  instruction?: string;
  phase_policy?: TaskPhasePolicy;
  schedule?: TaskSchedule | null;
  /** 任务上下文文件（上传落点为项目所属任务目录）。 */
  context_paths?: string[];
  /** Structured objective; null clears it (PATCH), undefined leaves it. */
  objective?: Objective | null;
}

export interface ProjectRequest {
  name: string;
  scope: ProjectScope;
}

/** 人工在环插入意图（source=human 的 InfoItem；契约 §3.1）。 */
export type GuidanceStatus = "injected" | "queued" | "consumed";

export interface Guidance {
  id: string;
  task_id: string;
  run_id?: string;
  instance_id?: string;
  message: string;
  status: GuidanceStatus;
  created_by?: string;
  created_at: string;
  consumed_at?: string;
}

export interface GuidanceSendResult {
  status: "injected" | "queued";
  guidance_id: string;
  run_id?: string;
  accepted?: boolean; // false = 重复 request_id（幂等命中）
}

/** 旁路提问受理回执（chat_questions.go）：回答经 chat_reply 事件回流转录。 */
export interface QuestionSendResult {
  status: "pending";
  accepted: boolean;
  run_id?: string;
  instance_id?: string;
  request_id?: string;
}

// ---------------------------------------------------------------------------
// R5 信息空间（CORE_PHILOSOPHY §4；contract §5）
// ---------------------------------------------------------------------------

export type InfoNature = "observed" | "inferred";
export type InfoCoordinateStatus = "" | "complete" | "partial" | "pending";

export interface InfoContent {
  text: string;
  media_type?: string;
}

export interface InfoSource {
  kind: string;
  name?: string;
  agent_id?: string;
  evidence_id?: string;
}

export interface InfoSubject {
  kind?: string;
  identifier: string;
}

export interface InfoCoordinates {
  content: InfoContent;
  source: InfoSource;
  observed_at: string;
  subject: InfoSubject;
  purpose?: string;
  context?: Record<string, string>;
  relation_ids?: string[];
}

export interface InfoItem {
  id: string;
  scan_id: string;
  source_seq: number;
  nature: InfoNature;
  coordinates: InfoCoordinates;
  supports?: string[];
  valid_from?: string;
  valid_until?: string;
  confidence?: number;
  coordinate_status?: InfoCoordinateStatus;
  created_at: string;
}

export interface InfoRegionResult {
  available: boolean;
  items: InfoItem[];
  truncated: boolean;
  /** Keyset 分页：还有下一页（不是全量总数）。 */
  has_more?: boolean;
  /** `<observed_at RFC3339Nano>|<id>`，原样传回 `cursor` 取下一页。 */
  next_cursor?: string;
  incomplete: boolean;
  gap_reason?: string;
}

export interface InfoContextResult {
  available: boolean;
  items: InfoItem[];
  truncated: boolean;
  incomplete: boolean;
  gap_reason?: string;
}

export interface InfoAssociationHit {
  item: InfoItem;
  path: string[];
  /** 逐跳关系类型：len(rels) = len(path)-1，第 i 项是 path[i] → path[i+1]。 */
  rels?: string[];
  depth: number;
}

export interface InfoAssociationResult {
  available: boolean;
  hits: InfoAssociationHit[];
  truncated: boolean;
  incomplete: boolean;
  gap_reason?: string;
  /** 项目级聚合形态：实际查询的白名单 scan 数（诚实披露聚合面）。 */
  scans_consulted?: number;
}

export interface InfoRegionParams {
  projectId: string;
  /** 可选：收窄到单个任务的 run 扫描集（后端校验归属，404 拒绝跨项目）。 */
  taskId?: string;
  subjectKind?: string;
  subject?: string;
  purpose?: string;
  nature?: InfoNature | "";
  /** observed_at 严格时间边界（过滤，不是分页）。 */
  after?: string;
  /** observed_at 严格时间边界（过滤，不是分页）。 */
  before?: string;
  /** keyset 游标：上一页响应的 `next_cursor` 原样回传。 */
  cursor?: string;
  limit?: number;
}

export interface InfoContextParams {
  projectId: string;
  scanId: string;
  subject?: string;
  subjectKind?: string;
  since?: string;
  until?: string;
  limit?: number;
}

export interface InfoAssociationParams {
  projectId: string;
  /** 单扫描形态必填；省略时走项目级聚合（无 scan_id 参数）。 */
  scanId?: string;
  /** 可选：项目级聚合收窄到单个任务的 run 扫描集。 */
  taskId?: string;
  ids: string;
  edgeTypes?: string[];
  maxDepth?: number;
  limit?: number;
}

/** GET /api/scans/{id}/coldpath — 冷路径同步状态（契约 §3.3 复用）。 */
export type ColdPathState = "pending" | "applied" | "gap" | "failed" | "disabled";

export interface ColdPathSyncState {
  scan_id: string;
  cold_path: "enabled" | "disabled";
  source_seq: number;
  applied_seq: number;
  state: ColdPathState;
  last_error?: string;
  updated_at?: string;
}

// ---------------------------------------------------------------------------
// 领域审计投影（宪章 M2 只读投影；GET /api/scans/{id}/audit）
// 投影不参与判定；incomplete/gap 必须如实显示。
// ---------------------------------------------------------------------------

export type DomainAuditActorKind = "agent" | "engine" | "human" | "system";

export interface DomainAuditActor {
  kind: DomainAuditActorKind;
  id?: string;
  source?: string;
}

/** 审计记录携带的假设状态子集（不含证据正文）。 */
export interface DomainAuditState {
  status?: string;
  assigned_to?: string;
  confidence?: number;
  voi?: number;
  voi_estimated?: boolean;
  attempts?: number;
}

export type DomainAuditOperation =
  | "seed_hypothesis"
  | "record_hypothesis"
  | "claim_hypothesis"
  | "assign_hypothesis"
  | "set_status"
  | "add_evidence"
  | "record_attempt"
  | "set_voi"
  | "merge_ledger"
  | "reset_ledger"
  | "info_item_enriched"
  | "info_item_projected"
  | "observe_traffic"
  | "record_objective"
  | "skill_load"
  | "agent_delegate"
  | "mcp_call"
  | "finding_recorded"
  | "tool_observation";

export interface DomainAuditRecord {
  scan_id: string;
  seq: number;
  at: string;
  actor: DomainAuditActor;
  operation: DomainAuditOperation;
  hypothesis_id?: string;
  evidence_id?: string;
  summary?: string;
  before?: DomainAuditState;
  after?: DomainAuditState;
}

export interface DomainAuditClaim {
  hypothesis_id: string;
  actor: DomainAuditActor;
  at: string;
  seq: number;
  summary?: string;
}

export interface DomainAuditGap {
  from: number;
  to: number;
}

/** domainjournal.Projection — 只读重建，带显式重复/乱序/缺口报告。 */
export interface DomainAuditProjection {
  scan_id: string;
  status: "" | "complete" | "incomplete";
  incomplete: boolean;
  disordered: boolean;
  duplicate_seqs?: number[];
  gaps?: DomainAuditGap[];
  last_seq: number;
  timeline: DomainAuditRecord[];
  claims?: DomainAuditClaim[];
  hypothesis_final?: Record<string, DomainAuditState>;
}

/** GET /api/scans/{id}/audit — 带诚实标志的审计投影视图。 */
export interface ScanAuditView {
  scan_id: string;
  journal_present: boolean;
  journal_damaged: boolean;
  incomplete: boolean;
  note?: string;
  projection?: DomainAuditProjection;
}

// ---------------------------------------------------------------------------
// 探索投影（4zreco 双图同源：拓扑图 + 播报板读同一份 node/edge 模型；
// GET /api/scans/{id}/exploration — journal+ledger 的只读归约）
// ---------------------------------------------------------------------------

export type ExplorationKind =
  | "goal"
  | "agent"
  | "hypothesis"
  | "evidence"
  | "finding"
  | "observation";

export type ExplorationRel = "claim" | "yields" | "proves" | "supports";

export interface ExplorationNode {
  id: string;
  kind: ExplorationKind;
  /** journal 首现 seq（板 = 按 seq 降序的播报流）。 */
  seq: number;
  /** RFC3339 首现时间。 */
  at: string;
  title: string;
  summary?: string;
  /** 假设六态 / finding 的 verified 布尔串。 */
  state?: string;
  /** evidence: observed|inferred。 */
  nature?: string;
  severity?: string;
  /** agent id / engine source。 */
  actor?: string;
  up: number;
  down: number;
}

export interface ExplorationEdge {
  src: string;
  dst: string;
  rel: ExplorationRel;
}

export interface ExplorationView {
  scan_id: string;
  journal_present: boolean;
  journal_damaged: boolean;
  ledger_present: boolean;
  incomplete: boolean;
  note?: string;
  nodes: ExplorationNode[];
  edges: ExplorationEdge[];
}

// ── 集成模块：MCP 服务器 / 用户技能 / Agent 档案（契约 §integration-*） ──

/** POST/GET /api/mcp-servers — MCP 服务器定义（env/headers 永远是打码回显）。 */
export interface McpServer {
  id: string;
  name: string;
  transport: "stdio" | "http";
  command?: string;
  args?: string[];
  /** 打码回显："****" 前缀；提交原样回传表示「未修改」。 */
  env?: Record<string, string>;
  url?: string;
  headers?: Record<string, string>;
  enabled: boolean;
  require_approval: boolean;
  timeout_seconds: number;
  tool_count: number;
  last_healthy_at?: string;
  last_error?: string;
  created_by?: string;
  created_at: string;
  updated_at?: string;
}

export interface McpServerRequest {
  name: string;
  transport: "stdio" | "http";
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  url?: string;
  headers?: Record<string, string>;
  enabled: boolean;
  require_approval: boolean;
  timeout_seconds?: number;
}

export interface McpServerPatch {
  name?: string;
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  url?: string;
  headers?: Record<string, string>;
  enabled?: boolean;
  require_approval?: boolean;
  timeout_seconds?: number;
}

// ── 内置工具目录（GET /api/tools，只读） ──

/** 一个内置工具参数。 */
export interface ToolCatalogParameter {
  name: string;
  required: boolean;
  desc?: string;
}

/** 一个内置工具（目录项，不代表本 Run 的实际启用状态）。 */
export interface ToolCatalogEntry {
  name: string;
  /** 稳定分类 key（terminal/network/browser/...），前端经 i18n 渲染。 */
  category: string;
  description: string;
  /** 审批类别：command/http/browser；缺省 = 不参与审批闸。 */
  approval?: string;
  parameters?: ToolCatalogParameter[];
}

export interface ToolCatalog {
  tools: ToolCatalogEntry[];
  categories: string[];
}

/** GET /api/skills — 用户技能（内置语料在引擎侧，不在此列）。 */
export interface UserSkill {
  id: string;
  name: string;
  category: string;
  description: string;
  tags?: string[];
  intent?: string;
  body: string;
  enabled: boolean;
  created_by?: string;
  created_at: string;
  updated_at?: string;
}

export interface UserSkillRequest {
  name: string;
  description: string;
  tags?: string[];
  intent?: string;
  body: string;
  enabled: boolean;
}

export interface UserSkillPatch {
  name?: string;
  description?: string;
  tags?: string[];
  intent?: string;
  body?: string;
  enabled?: boolean;
}

/** GET /api/agent-profiles — 内置行（builtin=true，只读）与用户行合并。 */
export interface AgentProfile {
  id: string;
  role: string;
  focus: string;
  vuln_classes?: string[];
  evidence_contract: string;
  stopping_rule: string;
  tool_scope_hidden?: string[];
  enabled: boolean;
  builtin: boolean;
  created_by?: string;
  created_at: string;
  updated_at?: string;
}

export interface AgentProfileRequest {
  role: string;
  focus: string;
  vuln_classes?: string[];
  evidence_contract: string;
  stopping_rule: string;
  tool_scope_hidden?: string[];
  enabled: boolean;
}

export interface AgentProfilePatch {
  role?: string;
  focus?: string;
  vuln_classes?: string[];
  evidence_contract?: string;
  stopping_rule?: string;
  tool_scope_hidden?: string[];
  enabled?: boolean;
}

/** 调用记录（跨扫描检索投影；模块 = mcp | skills | agents）。 */
export type InvocationModule = "mcp" | "skills" | "agents";

export interface InvocationRecord {
  id: string;
  module: InvocationModule;
  ts: string;
  instance_id?: string;
  scan_id?: string;
  agent_id?: string;
  name: string;
  server?: string;
  status: "ok" | "error";
  error?: string;
  args?: string;
  outcome?: string;
}

export interface InvocationList {
  items: InvocationRecord[];
  total: number;
  page: number;
  size: number;
}

export interface InvocationListParams {
  page?: number;
  size?: number;
  instanceId?: string;
  name?: string;
  status?: "ok" | "error";
}

// ── Run 账本投影（GET /api/scans/{id}/ledger，契约 §3.6）────────────────────

export type LedgerHypothesisStatus =
  | "queued"
  | "testing"
  | "proven"
  | "rejected"
  | "blocked"
  | "exhausted";

export interface LedgerEvidence {
  id: string;
  kind: string;
  summary: string;
  request?: string;
  response?: string;
  finding_id?: string;
  agent_id?: string;
  /** 引擎 verifier 名（如 verify_sqli）；模型自写证据无此字段。 */
  source?: string;
  nature?: "observed" | "inferred";
  supports?: string[];
  /** 冷路径 InfoItem 的稳定链接（`evidence:<scan>:<EV-id>`），可深链到 /info。 */
  info_item_id?: string;
  confidence?: number;
  created_at: string;
}

export interface LedgerHypothesis {
  id: string;
  dedup_key: string;
  title: string;
  vuln_class: string;
  target?: string;
  endpoint?: string;
  parameter?: string;
  role?: string;
  data_flow?: string;
  preconditions?: string[];
  baseline?: string;
  status: LedgerHypothesisStatus;
  confidence: number;
  voi?: number;
  voi_estimated?: boolean;
  assigned_to?: string;
  next_action?: string;
  attempts: number;
  evidence?: LedgerEvidence[];
  origin?: string;
  created_at: string;
  updated_at: string;
}

export interface ScanLedgerView {
  scan_id: string;
  ledger_present: boolean;
  ledger_damaged: boolean;
  incomplete: boolean;
  note?: string;
  counts: Record<LedgerHypothesisStatus, number>;
  hypotheses: LedgerHypothesis[];
}

// ── 任务情报（契约 §3.7）────────────────────────────────────────────────────

export type IntelKind = "credential" | "lead" | "sensitive_data";
export type IntelTriageStatus = "open" | "confirmed" | "dismissed";

export interface IntelTriage {
  task_id: string;
  fingerprint: string;
  status: IntelTriageStatus;
  decided_by: string;
  decided_at: string;
}

export interface IntelItem {
  id: string;
  fingerprint: string;
  kind: IntelKind;
  title: string;
  content: string;
  endpoint?: string;
  confidence?: number;
  created_at: string;
  updated_at: string;
  /** 记录了同一观测（同指纹）的各 Run。 */
  scan_ids: string[];
  triage?: IntelTriage;
}

export interface TaskIntelResponse {
  items: IntelItem[];
  counts: Record<IntelKind, number>;
  triage_available: boolean;
  damaged_runs?: number;
  note?: string;
}

/** 会话转录的一行（docs/CHAT_PAGE_REDESIGN.md §4.2）。 */
export type TranscriptKind =
  | "human"
  | "assistant"
  | "tool_use"
  | "tool_result"
  | "approval"
  | "finding"
  | "system";

export interface TranscriptItem {
  seq: number;
  kind: TranscriptKind;
  role?: string;
  text?: string;
  tool?: string;
  args?: unknown;
  output?: string;
  status?: string;
  agent_id?: string;
  at: string;
  truncated?: boolean;
  /** Inferred methodology phase of tool events (absent = none); drives round dividers. */
  phase?: number;
  /** Structured approval payload; present on approval rows so the UI can localize them. */
  approval?: {
    kind: string;
    status: string;
    decided_by?: string;
    reason?: string;
    decided: boolean;
  };
}

export interface TranscriptPage {
  items: TranscriptItem[];
  total: number;
  has_more: boolean;
  earliest_seq: number;
  latest_seq: number;
  running: boolean;
}

export interface ConversationRow extends Run {
  task_name?: string;
}

export interface ConversationPatch {
  title?: string;
  pinned?: boolean;
  archived?: boolean;
}

export interface ChatAttachment {
  path: string;
  name: string;
  size: number;
}

/** 计划任务投影（PlanTask = 意图 Intent，宪章 §2）。 */
export interface PlanTaskView {
  id: string;
  title: string;
  phase: number;
  vuln_class?: string;
  endpoint?: string;
  status: "pending" | "active" | "completed" | "skipped" | string;
  depends_on?: string[];
  notes?: string;
  disposition?: string;
  /** auto* / llm：引擎自建 vs 模型自建（来源必须保留，§3.5）。 */
  origin?: string;
  prerequisite?: boolean;
  whole_target?: boolean;
}

export interface PlanCountersView {
  total: number;
  completed: number;
  skipped: number;
  unfinished: number;
}

/** GET /api/scans/{id}/plan —— 计划的只读状态投影（替代事件反推）。 */
export interface ScanPlanView {
  present: boolean;
  source: "engine" | "model" | "mixed" | "none";
  plan_revision?: string;
  surface_revision?: string;
  current_phase?: number;
  counters: PlanCountersView;
  tasks: PlanTaskView[];
  /** partial=true：只能给出计数，任务明细未持久化/被裁剪，禁止渲染成空计划。 */
  partial?: boolean;
  partial_reason?: "not_persisted" | "truncated" | "cold_path" | string;
}

/** 单个委派 agent 的状态投影（worker 行的权威来源）。 */
export interface DelegatedAgent {
  agent_id: string;
  lane: string;
  status: "queued" | "running" | "completed" | "failed";
  task?: string;
  target?: string;
  started_at?: string;
  completed_at?: string;
  error?: string;
  queued_position?: number;
  partial_events?: number;
  collected?: boolean;
  claimed_hypotheses?: {
    id: string;
    vuln_class?: string;
    endpoint?: string;
    status?: string;
  }[];
}

/** GET /api/instances/{id}/agents —— worker 池投影（运行中/排队/上限）。 */
export interface InstanceAgentsView {
  available: boolean;
  concurrency: { limit: number; running: number; queued: number };
  agents: DelegatedAgent[];
  partial_reason?: "checkpoint" | "no_run" | string;
}

/** GET /api/tasks/{id}/workers 的一条 run：run 投影 + 该 run 的 worker 历史。 */
export interface TaskWorkerRun extends Run {
  workers: InstanceAgentsView;
}

/** GET /api/tasks/{id}/workers —— 任务级 worker 历史（会话页左栏）。 */
export interface TaskWorkersView {
  items: TaskWorkerRun[];
  total: number;
  page: number;
  size: number;
}
