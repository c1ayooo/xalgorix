import type {
  Approval,
  ApprovalsResponse,
  ApprovalsSummary,
  AuthProfile,
  AuthStatus,
  AgentMailSettings,
  CatalogEntry,
  EnvironmentSettings,
  InstancesResponse,
  LLMSettings,
  LLMSettingsRequest,
  ListParams,
  OAuthStartResponse,
  Paginated,
  Project,
  ProjectRequest,
  QueueStatus,
  RateLimitSettings,
  Run,
  ScanEventsPage,
  ScanInstance,
  ScanListItem,
  ScanRecord,
  ScanRequest,
  ScanSchedule,
  SettingsBackup,
  SettingsImportResult,
  StatusResponse,
  Task,
  TaskRequest,
  VersionInfo,
  WSEvent,
  Guidance,
  GuidanceSendResult,
  InfoAssociationParams,
  InfoAssociationResult,
  InfoContextParams,
  InfoContextResult,
  InfoRegionParams,
  InfoRegionResult,
} from "@/types/api";
import type { FlatFinding } from "@/lib/findings";
import type { TokenUsageResponse } from "@/types/token-usage";

/**
 * Status of a single provider's API key as reported by
 * GET /api/settings/llm/keys (mirrors Go's providerKeyStatus in
 * internal/web/handlers_router.go). Credentials arrive masked from the
 * server — `masked_key` is only present when a key is configured.
 */
export interface ProviderKeyStatus {
  provider_id: string;
  display_name: string;
  has_key: boolean;
  masked_key?: string;
  base_url: string;
  header_style: string;
}

// Auth session expiry handling. When any API call returns 401 we dispatch a
// global event so the auth store (in store/auth.ts) can flip the user back
// to the login screen without each component having to handle it.
// We avoid importing the store directly to keep this module free of
// circular deps with the rest of the app.
const AUTH_EXPIRED_EVENT = "xalgorix:auth-expired";
let lastAuthExpiredDispatch = 0;

function dispatchAuthExpired() {
  // Debounce: when multiple SWR keys fail at once we'd otherwise fire
  // dozens of events in a single tick.
  const now = Date.now();
  if (now - lastAuthExpiredDispatch < 1000) return;
  lastAuthExpiredDispatch = now;
  if (typeof window !== "undefined") {
    window.dispatchEvent(new CustomEvent(AUTH_EXPIRED_EVENT));
  }
}

export const AUTH_EXPIRED = AUTH_EXPIRED_EVENT;

/**
 * Structured HTTP error thrown by `http()`. Carries the status code, the
 * raw response body, and any JSON-decoded body so callers (login form,
 * settings, etc.) can render a friendly message instead of leaking the
 * raw `HTTP 401 Unauthorized: {"error":"…"}` envelope into the UI.
 */
export class HttpError extends Error {
  status: number;
  statusText: string;
  body: string;
  data: unknown;
  retryAfter?: number;
  constructor(opts: {
    status: number;
    statusText: string;
    body: string;
    data: unknown;
    retryAfter?: number;
  }) {
    // `message` stays useful for unhandled-error toasts / console logs, but
    // UI code should branch on `.status` and use `.data?.error` when it
    // wants a polished string.
    const fromData =
      opts.data &&
      typeof opts.data === "object" &&
      "error" in (opts.data as Record<string, unknown>)
        ? String((opts.data as { error?: unknown }).error ?? "")
        : "";
    const detail = fromData || opts.body;
    super(
      `HTTP ${opts.status}${opts.statusText ? ` ${opts.statusText}` : ""}${
        detail ? `: ${detail}` : ""
      }`,
    );
    this.name = "HttpError";
    this.status = opts.status;
    this.statusText = opts.statusText;
    this.body = opts.body;
    this.data = opts.data;
    this.retryAfter = opts.retryAfter;
  }
}

async function http<T>(
  path: string,
  init?: RequestInit & { json?: unknown; timeoutMs?: number },
): Promise<T> {
  const headers: HeadersInit = {
    Accept: "application/json",
    ...(init?.headers || {}),
  };
  let body = init?.body;
  if (init?.json !== undefined) {
    body = JSON.stringify(init.json);
    (headers as Record<string, string>)["Content-Type"] = "application/json";
  }

  // Optional client-side timeout. Without this a hung/unreachable backend
  // (e.g. an origin returning a Cloudflare 5xx after the connection, or a
  // stalled upload) leaves the request pending forever — the UI just spins.
  // When timeoutMs is set we abort and surface a clear, catchable error.
  const timeoutMs = init?.timeoutMs;
  const controller = timeoutMs ? new AbortController() : null;
  const timer =
    controller && timeoutMs
      ? setTimeout(() => controller.abort(), timeoutMs)
      : null;

  let res: Response;
  try {
    res = await fetch(path, {
      credentials: "same-origin",
      ...init,
      headers,
      body,
      ...(controller ? { signal: controller.signal } : {}),
    });
  } catch (err) {
    // Network-level failure: server unreachable, DNS, CORS, abort. Surface
    // it as an HttpError with status 0 so callers can distinguish it from
    // a real HTTP response.
    const aborted = controller?.signal.aborted;
    throw new HttpError({
      status: 0,
      statusText: aborted ? "Request timed out" : "Network error",
      body: aborted
        ? `The server did not respond within ${Math.round((timeoutMs ?? 0) / 1000)}s. It may be overloaded or unreachable.`
        : err instanceof Error
          ? err.message
          : String(err),
      data: null,
    });
  } finally {
    if (timer) clearTimeout(timer);
  }

  if (!res.ok) {
    // Surface session expiry / auth failure to the rest of the app, but
    // never on the login endpoint itself (that 401 is just "bad password"
    // and the form already shows the error inline).
    if (res.status === 401 && path !== "/api/auth/login") {
      dispatchAuthExpired();
    }
    let rawBody = "";
    try {
      rawBody = await res.text();
    } catch {
      /* ignore */
    }
    let parsed: unknown = null;
    if (rawBody) {
      try {
        parsed = JSON.parse(rawBody);
      } catch {
        /* not JSON, leave as null */
      }
    }
    const retryHeader = res.headers.get("Retry-After");
    const retryAfter = retryHeader ? Number(retryHeader) : undefined;
    throw new HttpError({
      status: res.status,
      statusText: res.statusText,
      body: rawBody,
      data: parsed,
      retryAfter: Number.isFinite(retryAfter) ? retryAfter : undefined,
    });
  }
  const ct = res.headers.get("content-type") || "";
  if (ct.includes("application/json")) {
    return (await res.json()) as T;
  }
  return (await res.text()) as unknown as T;
}

/**
 * Build a query string from list params, omitting empty/default values so
 * the URL stays clean (e.g. `?page=2&size=50&q=foo&status=running`).
 */
function listQuery(params: ListParams): string {
  const sp = new URLSearchParams();
  if (params.page) sp.set("page", String(params.page));
  if (params.size) sp.set("size", String(params.size));
  if (params.q && params.q.trim()) sp.set("q", params.q.trim());
  if (params.status && params.status !== "all") sp.set("status", params.status);
  if (params.mode && params.mode !== "all") sp.set("mode", params.mode);
  const qs = sp.toString();
  return qs ? `?${qs}` : "";
}

export const api = {
  authStatus: () =>
    http<AuthStatus>("/api/auth/status", { timeoutMs: 10_000 }),
  login: (username: string, password: string) =>
    http<{ status: string }>("/api/auth/login", {
      method: "POST",
      json: { username, password },
    }),
  logout: () =>
    http<{ status: string }>("/api/auth/logout", { method: "POST" }),

  status: () => http<StatusResponse>("/api/status"),
  version: () => http<VersionInfo>("/api/version"),
  listScans: () => http<ScanListItem[] | null>("/api/scans"),
  // 服务端汇总的发现计数（契约 §3.2 支持可选 project_id）。
  findingsSummary: (projectId?: string) =>
    http<{
      totals: { critical: number; high: number; medium: number; low: number; info: number };
      as_of: string;
      etag: string;
    }>(
      `/api/findings/summary${projectId ? `?project_id=${encodeURIComponent(projectId)}` : ""}`,
    ),
  // Server-side paginated + filtered scan list. Returns the envelope shape
  // { items, total, page, size }.
  listScansPage: (params: ListParams) =>
    http<Paginated<ScanListItem>>(`/api/scans${listQuery(params)}`),
  getScan: (id: string) => http<ScanRecord | null>(`/api/scans/${id}`),
  // Lazily page older events for a scan whose detail response only tailed the
  // most recent ones (see ScanRecord.events_truncated).
  scanEvents: (id: string, offset: number, limit: number) =>
    http<ScanEventsPage>(
      `/api/scans/${id}/events?offset=${offset}&limit=${limit}`,
    ),
  // Token-attribution diagnostics for a scan (observability only).
  scanTokenUsage: (id: string) =>
    http<TokenUsageResponse>(`/api/scans/${id}/token-usage`),
  // Flattened + deduped findings across all scans, computed server-side in a
  // single walk. Replaces the previous per-scan getScan() fan-out.
  // project_id 可选过滤（契约 §3.2）：缺省 = 全部（兼容旧行为）。
  listFindings: (projectId?: string) =>
    http<FlatFinding[] | null>(
      `/api/findings${projectId ? `?project_id=${encodeURIComponent(projectId)}` : ""}`,
    ),
  deleteScan: (id: string) =>
    http<{ status: string }>(`/api/scans/${id}`, { method: "DELETE" }),
  deleteVuln: (scanId: string, vulnId: string) =>
    http<{ status: string; removed: number; remaining: number }>(
      `/api/scans/${scanId}/vulns/${vulnId}`,
      { method: "DELETE" },
    ),

  instances: (projectId?: string) =>
    http<InstancesResponse>(
      `/api/instances${projectId ? `?project_id=${encodeURIComponent(projectId)}` : ""}`,
    ),
  // Server-side paginated + filtered instances list. Resources and the
  // distinct `modes` list are returned alongside the paged `instances`.
  instancesPage: (params: ListParams) =>
    http<InstancesResponse>(`/api/instances${listQuery(params)}`),
  instance: (id: string) => http<ScanInstance>(`/api/instances/${id}`),
  instanceEvents: (id: string) =>
    http<WSEvent[]>(`/api/instances/${id}/events`),
  // 分页历史（会话控制台"加载更早"回翻；契约同 instances events 端点）。
  runEventsPage: (id: string, offset: number, limit = 100) =>
    http<WSEvent[]>(
      `/api/instances/${id}/events?offset=${offset}&limit=${limit}`,
    ),
  // reason 可选（契约 §3 停止带 reason 增补）：软取消语义，供引擎下一 Run 参考。
  stopInstance: (id: string, reason?: string) =>
    http<{ status: string }>(`/api/instances/${id}/stop`, {
      method: "POST",
      json: reason ? { reason } : undefined,
    }),
  pauseInstance: (id: string) =>
    http<{ status: string }>(`/api/instances/${id}/pause`, { method: "POST" }),
  resumeInstance: (id: string) =>
    http<{ status: string }>(`/api/instances/${id}/resume`, { method: "POST" }),
  restartInstance: (id: string) =>
    http<{ status: string }>(`/api/instances/${id}/restart`, {
      method: "POST",
    }),
  startSavedInstance: (id: string) =>
    http<{ status: string; instance_id?: string }>(
      `/api/instances/${id}/start`,
      { method: "POST" },
    ),

  startScan: (req: ScanRequest) =>
    http<{ status: string; instance_id: string }>("/api/scan", {
      method: "POST",
      json: req,
    }),
  uploadLogo: (file: File) => {
    const body = new FormData();
    body.append("file", file);
    return http<{ path: string; filename: string }>("/api/upload-logo", {
      method: "POST",
      body,
      timeoutMs: 120_000,
    });
  },
  uploadContext: (files: File[]) => {
    const body = new FormData();
    // Multiple artifacts (e.g. a Postman collection + its environment) are sent
    // under the same "file" field; the server saves them into one directory and
    // parses them together.
    for (const f of files) body.append("file", f);
    return http<{
      path: string;
      filename: string;
      files?: number;
      endpoints: number;
      formats: string[];
      has_auth: boolean;
    }>("/api/upload-context", {
      method: "POST",
      body,
      // APK/HAR parsing can take a little while, but a KB spec is instant —
      // if we're past this the backend is wedged, so fail with a clear error
      // instead of spinning forever.
      timeoutMs: 120_000,
    });
  },
  stopAll: () => http<{ status: string }>("/api/stop", { method: "POST" }),

  queueStatus: () => http<QueueStatus>("/api/queue/status"),
  queueResume: () =>
    http<{
      status: string;
      resumed_queues?: number;
      from_index?: number;
      targets_left?: number;
      error?: string;
    }>("/api/queue/resume", { method: "POST" }),
  queueClear: () =>
    http<{ status: string }>("/api/queue/clear", { method: "POST" }),

  rateLimit: () => http<RateLimitSettings>("/api/settings/rate-limit"),
  updateRateLimit: (req: RateLimitSettings) =>
    http<RateLimitSettings>("/api/settings/rate-limit", {
      method: "POST",
      json: req,
    }),

  agentMail: () => http<AgentMailSettings>("/api/settings/agentmail"),
  updateAgentMail: (req: { pod: string; apiKey: string }) =>
    http<AgentMailSettings>("/api/settings/agentmail", {
      method: "POST",
      json: req,
    }),

  llmSettings: () => http<LLMSettings>("/api/settings/llm"),
  updateLLMSettings: (req: LLMSettingsRequest) =>
    http<LLMSettings>("/api/settings/llm", {
      method: "POST",
      json: req,
    }),

  environmentSettings: () =>
    http<EnvironmentSettings>("/api/settings/environment"),
  updateEnvironmentSettings: (values: Record<string, string>) =>
    http<EnvironmentSettings>("/api/settings/environment", {
      method: "POST",
      json: { values },
    }),

  // Settings backup & migration. Export is a file download (not JSON
  // consumption), so it goes through fetch directly with the same
  // same-origin credentials and 401 handling as http().
  exportSettings: async (): Promise<Blob> => {
    const res = await fetch("/api/settings/export", { credentials: "same-origin" });
    if (!res.ok) {
      if (res.status === 401) dispatchAuthExpired();
      throw new HttpError({
        status: res.status,
        statusText: res.statusText,
        body: await res.text().catch(() => ""),
        data: null,
      });
    }
    return res.blob();
  },
  importSettings: (backup: SettingsBackup) =>
    http<SettingsImportResult>("/api/settings/import", {
      method: "POST",
      json: backup,
    }),

  reportUrl: (scanId: string) => `/api/report/${scanId}`,

  legacyImportStatus: () =>
    http<{ count: number; dismissed: boolean }>("/api/legacy-import/status"),
  dismissLegacyImport: () =>
    http<{ count: number; dismissed: boolean }>(
      "/api/legacy-import/status",
      { method: "POST" },
    ),

  // 旁路提问（in-scan chat 通道，永不进 loop）。真实后端回 response，mock 回 reply。
  chat: (message: string, instanceId?: string) =>
    http<{ response?: string; reply?: string }>("/api/chat", {
      method: "POST",
      json: { message, instance_id: instanceId },
    }),

  listSchedules: () => http<ScanSchedule[]>("/api/schedules"),
  createSchedule: (schedule: Omit<ScanSchedule, "id" | "next_run">) =>
    http<ScanSchedule>("/api/schedules", {
      method: "POST",
      json: schedule,
    }),
  updateSchedule: (id: string, schedule: Partial<ScanSchedule>) =>
    http<ScanSchedule>(`/api/schedules/${id}`, {
      method: "PUT",
      json: schedule,
    }),
  deleteSchedule: (id: string) =>
    http<{ status: string }>(`/api/schedules/${id}`, { method: "DELETE" }),
  triggerSchedule: (id: string) =>
    http<{ status: string; instance_id: string }>(`/api/schedules/${id}/trigger`, {
      method: "POST",
    }),

  // ---------------------------------------------------------------
  // Provider catalog (read-only) + auth profiles (v4.4.22).
  //
  // The catalog is now compiled into the binary, so /api/providers
  // is GET-only. Profile CRUD lives under /api/auth/profiles/*.
  // Credential fields on profile responses arrive masked from the
  // server (internal/web/masks.go) — the dashboard never touches
  // plaintext credentials in the browser.
  // ---------------------------------------------------------------

  listProviders: () => http<CatalogEntry[]>("/api/providers"),
  discoverProviderModels: (provider: string, profile?: string) =>
    http<{ models: string[]; source: "remote" }>(
      `/api/providers/${encodeURIComponent(provider)}/models${
        profile ? `?profile=${encodeURIComponent(profile)}` : ""
      }`,
    ),

  // ---------------------------------------------------------------
  // Multi-provider key store + model router (LiteLLM-style).
  // Backed by /api/settings/llm/keys (GET/POST/DELETE) and
  // /api/settings/llm/test-route (POST). Return shapes mirror the
  // JSON written by internal/web/handlers_router.go.
  // ---------------------------------------------------------------

  providerKeys: () =>
    http<{
      providers: ProviderKeyStatus[];
      configured_count: number;
      router_enabled: boolean;
      known_model_patterns: string[];
    }>("/api/settings/llm/keys"),

  saveProviderKeys: (
    keys: {
      provider_id: string;
      api_key: string;
      base_url?: string;
      header_style?: string;
    }[],
  ) =>
    http<{ status: string; saved: number; message: string }>(
      "/api/settings/llm/keys",
      { method: "POST", json: { keys } },
    ),

  deleteProviderKey: (providerId: string) =>
    http<{ status: string; message: string }>("/api/settings/llm/keys", {
      method: "DELETE",
      json: { provider_id: providerId },
    }),

  testModelRoute: (model: string) =>
    http<{
      resolved: boolean;
      provider_id?: string;
      display_name?: string;
      bare_model?: string;
      base_url?: string;
      header_style?: string;
      has_key?: boolean;
      model?: string;
      error?: string;
    }>("/api/settings/llm/test-route", { method: "POST", json: { model } }),

  listAuthProfiles: () => http<AuthProfile[]>("/api/auth/profiles"),
  createAPIKeyProfile: (req: {
    provider: string;
    profileId: string;
    apiKey: string;
    apiBaseOverride?: string;
  }) =>
    http<AuthProfile>("/api/auth/profiles/api-key", {
      method: "POST",
      json: req,
    }),
  oauthStart: (req: { provider: string; profileId?: string; preferPaste?: boolean }) =>
    http<OAuthStartResponse>("/api/auth/profiles/oauth/start", {
      method: "POST",
      json: req,
    }),
  oauthComplete: (req: {
    provider: string;
    flowId: string;
    code?: string;
    state?: string;
    setupToken?: string;
  }) =>
    http<AuthProfile>("/api/auth/profiles/oauth/complete", {
      method: "POST",
      json: req,
    }),
  refreshAuthProfile: (key: string) =>
    http<AuthProfile>(
      `/api/auth/profiles/${encodeURIComponent(key)}/refresh`,
      { method: "POST" },
    ),
  // Returns Promise<void>: the backend produces 204 No Content
  // on success and the wire response carries no body. Typing the
  // mutation as void (rather than the http<T>() generic default)
  // lets useDeleteAuthProfile's onSuccess callback skip data
  // handling entirely.
  deleteAuthProfile: (key: string): Promise<void> =>
    http<void>(`/api/auth/profiles/${encodeURIComponent(key)}`, {
      method: "DELETE",
    }),

  // ---------------------------------------------------------------
  // Projects / Tasks / Runs / Approvals — R3 二开（docs/API_CONTRACT.md）。
  // 后端未就绪时这些调用 404，页面以 ErrorState 呈现，不影响既有功能。
  // ---------------------------------------------------------------

  listProjects: () => http<{ projects: Project[] }>("/api/projects"),
  createProject: (req: ProjectRequest) =>
    http<Project>("/api/projects", { method: "POST", json: req }),
  updateProject: (
    id: string,
    req: Partial<{ name: string; scope: ProjectRequest["scope"]; archived: boolean }>,
  ) => http<Project>(`/api/projects/${id}`, { method: "PATCH", json: req }),
  deleteProject: (id: string) =>
    http<{ status: string }>(`/api/projects/${id}`, { method: "DELETE" }),

  listTasks: (projectId: string) =>
    http<{ tasks: Task[] }>(`/api/projects/${projectId}/tasks`),
  getTask: (id: string) => http<Task>(`/api/tasks/${id}`),
  createTask: (projectId: string, req: TaskRequest) =>
    http<Task>(`/api/projects/${projectId}/tasks`, { method: "POST", json: req }),
  updateTask: (id: string, req: Partial<TaskRequest>) =>
    http<Task>(`/api/tasks/${id}`, { method: "PATCH", json: req }),
  deleteTask: (id: string) =>
    http<{ status: string }>(`/api/tasks/${id}`, { method: "DELETE" }),
  startTaskRun: (id: string, overrides?: Partial<ScanRequest>) =>
    http<{ status: string; run_id: string; instance_id: string }>(
      `/api/tasks/${id}/runs`,
      { method: "POST", json: overrides ?? {} },
    ),
  taskRuns: (id: string, page = 1, size = 50) =>
    http<Paginated<Run>>(
      `/api/tasks/${id}/runs?page=${page}&size=${size}`,
    ),
  taskFindings: (id: string) =>
    http<FlatFinding[]>(`/api/tasks/${id}/findings`),

  taskGuidance: (id: string, limit = 50) =>
    http<{ items: Guidance[] }>(`/api/tasks/${id}/guidance?limit=${limit}`),
  sendTaskGuidance: (id: string, message: string, requestId: string, instanceId?: string) =>
    http<GuidanceSendResult>(`/api/tasks/${id}/guidance`, {
      method: "POST",
      json: {
        message,
        request_id: requestId,
        ...(instanceId ? { instance_id: instanceId } : {}),
      },
    }),

  listApprovals: (
    opts: { status?: "pending" | "decided" | "all"; projectId?: string; taskId?: string; page?: number; size?: number } = {},
  ) => {
    const sp = new URLSearchParams();
    if (opts.status && opts.status !== "all") sp.set("status", opts.status);
    if (opts.projectId) sp.set("project_id", opts.projectId);
    if (opts.taskId) sp.set("task_id", opts.taskId);
    if (opts.page) sp.set("page", String(opts.page));
    if (opts.size) sp.set("size", String(opts.size));
    const qs = sp.toString();
    return http<ApprovalsResponse>(`/api/approvals${qs ? `?${qs}` : ""}`);
  },
  approvalsSummary: () => http<ApprovalsSummary>("/api/approvals/summary"),
  decideApproval: (id: string, decision: "approved" | "rejected", reason?: string) =>
    http<Approval>(`/api/approvals/${id}/decision`, {
      method: "POST",
      json: { decision, reason },
    }),
  batchDecideApprovals: (
    ids: string[],
    decision: "approved" | "rejected",
    reason?: string,
  ) =>
    http<{ approved: number; rejected: number; skipped: number }>(
      "/api/approvals/batch-decision",
      { method: "POST", json: { ids, decision, reason } },
    ),

  // R5 信息空间（只读；契约 §5）。项目隔离由后端 project_id 强制。
  infoRegion: (params: InfoRegionParams) => {
    const sp = new URLSearchParams({ project_id: params.projectId });
    if (params.subjectKind) sp.set("subject_kind", params.subjectKind);
    if (params.subject) sp.set("subject", params.subject);
    if (params.purpose) sp.set("purpose", params.purpose);
    if (params.nature) sp.set("nature", params.nature);
    if (params.after) sp.set("after", params.after);
    if (params.before) sp.set("before", params.before);
    if (params.limit) sp.set("limit", String(params.limit));
    return http<InfoRegionResult>(`/api/info/region?${sp.toString()}`);
  },
  infoContext: (params: InfoContextParams) => {
    const sp = new URLSearchParams({
      project_id: params.projectId,
      scan_id: params.scanId,
    });
    if (params.subject) sp.set("subject", params.subject);
    if (params.subjectKind) sp.set("subject_kind", params.subjectKind);
    if (params.since) sp.set("since", params.since);
    if (params.until) sp.set("until", params.until);
    if (params.limit) sp.set("limit", String(params.limit));
    return http<InfoContextResult>(`/api/info/context?${sp.toString()}`);
  },
  infoAssociation: (params: InfoAssociationParams) => {
    const sp = new URLSearchParams({
      project_id: params.projectId,
      scan_id: params.scanId,
      ids: params.ids,
    });
    if (params.edgeTypes?.length) sp.set("edge_types", params.edgeTypes.join(","));
    if (params.maxDepth) sp.set("max_depth", String(params.maxDepth));
    if (params.limit) sp.set("limit", String(params.limit));
    return http<InfoAssociationResult>(`/api/info/association?${sp.toString()}`);
  },
};
