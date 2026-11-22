import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type {
  ApprovalRuleRequest,
  ColdPathSyncState,
  ScanAuditView,
  ExplorationView,
  ConversationPatch,
  InfoAssociationParams,
  IntelTriageStatus,
  InfoContextParams,
  InfoRegionParams,
  ListParams,
  ProjectRequest,
  ProjectScope,
  Role,
  ScanRequest,
  ScanSchedule,
  TaskRequest,
  AgentProfile,
  AgentProfilePatch,
  AgentProfileRequest,
  InvocationList,
  InvocationListParams,
  McpServer,
  McpServerPatch,
  McpServerRequest,
  UserSkill,
  UserSkillPatch,
  UserSkillRequest,
} from "@/types/api";

export const qk = {
  authStatus: ["auth", "status"] as const,
  status: ["status"] as const,
  version: ["version"] as const,
  scans: ["scans"] as const,
  // Paginated scans share the ["scans"] prefix so existing
  // invalidateQueries({ queryKey: qk.scans }) calls refresh them too.
  scansPage: (params: ListParams) => ["scans", "page", params] as const,
  scan: (id: string) => ["scan", id] as const,
  instances: ["instances"] as const,
  // Paginated instances share the ["instances"] prefix for the same reason.
  instancesPage: (params: ListParams) => ["instances", "page", params] as const,
  instance: (id: string) => ["instance", id] as const,
  instanceEvents: (id: string) => ["instance", id, "events"] as const,
  queue: ["queue"] as const,
  rateLimit: ["settings", "rate-limit"] as const,
  llmSettings: ["settings", "llm"] as const,
  environmentSettings: ["settings", "environment"] as const,
  schedules: ["schedules"] as const,
  legacyImport: ["legacy-import", "status"] as const,
  // v4.4.22: shared cache keys for the compiled-in catalog and the
  // credential profile list. Mutations on the profile surface
  // invalidate both keys when relevant.
  authProfiles: ["auth", "profiles"] as const,
  providers: ["providers"] as const,
  // Shared between /findings and /overview so the totals widget
  // reads a single cache entry across both pages.
  findingsSummary: ["findings", "summary"] as const,
  // Flattened + deduped findings list, computed server-side in one walk.
  findingsList: ["findings", "list"] as const,
  // R3: projects / tasks / runs / approvals. Tasks/runs/findings carry the
  // projectId so switching projects re-keys every cache entry.
  projects: ["projects"] as const,
  projectScope: (projectId: string) => ["projects", projectId, "scope"] as const,
  tasks: (projectId: string) => ["projects", projectId, "tasks"] as const,
  task: (id: string) => ["task", id] as const,
  taskRuns: (id: string) => ["task", id, "runs"] as const,
  taskFindings: (id: string) => ["task", id, "findings"] as const,
  taskIntelligence: (id: string) => ["task", id, "intelligence"] as const,
  approvals: (filter: string, projectId?: string, taskId?: string) =>
    ["approvals", filter, projectId ?? "all", taskId ?? "all"] as const,
  approvalsSummary: ["approvals", "summary"] as const,
  approvalRules: (projectId?: string, kind?: string) =>
    ["approvals", "rules", projectId ?? "all", kind ?? "all"] as const,
  infoRegion: (params: InfoRegionParams) =>
    ["info", "region", params] as const,
  infoContext: (params: InfoContextParams) =>
    ["info", "context", params] as const,
  infoAssociation: (params: InfoAssociationParams) =>
    ["info", "association", params] as const,
  coldPathSync: (scanId: string) => ["info", "coldpath", scanId] as const,
  scanAudit: (scanId: string) => ["info", "audit", scanId] as const,
  scanExploration: (scanId: string) => ["info", "exploration", scanId] as const,
  // 集成模块（MCP / Skills / Agents）：定义 + 调用记录。配置类无轮询，
  // mutation 失效刷新；调用记录 15s 轻轮询。
  mcpServers: ["mcp", "servers"] as const,
  skills: ["skills"] as const,
  agentProfiles: ["agent-profiles"] as const,
  mcpInvocations: (params: InvocationListParams) =>
    ["mcp", "invocations", params] as const,
  skillInvocations: (params: InvocationListParams) =>
    ["skills", "invocations", params] as const,
  agentInvocations: (params: InvocationListParams) =>
    ["agent-profiles", "invocations", params] as const,
  // RBAC account management (admin-only page).
  users: ["users"] as const,
};

export function useAuthStatus() {
  return useQuery({
    queryKey: qk.authStatus,
    queryFn: api.authStatus,
    staleTime: 60_000,
  });
}

export function useStatus() {
  return useQuery({
    queryKey: qk.status,
    queryFn: api.status,
    refetchInterval: 5000,
  });
}

export function useVersion() {
  return useQuery({
    queryKey: qk.version,
    queryFn: api.version,
    staleTime: Infinity,
  });
}

export function useScansList() {
  return useQuery({
    queryKey: qk.scans,
    queryFn: api.listScans,
    refetchInterval: 15000,
  });
}

// Flattened + deduped findings across all scans, served by GET /api/findings
// in a single server-side walk. Replaces the per-scan getScan() fan-out the
// findings page used to do (an N+1 that shipped every scan's full event log
// to the browser just to render the findings table).
export function useFindingsList(projectId?: string, enabled = true) {
  return useQuery({
    queryKey: ["findings", "list", projectId ?? "all"],
    queryFn: () => api.listFindings(projectId),
    refetchInterval: 15000,
    placeholderData: (prev) => prev,
    // 未选项目时缺省参数会扫全量记录（跨项目最坏情况），调用方可显式关掉。
    enabled,
  });
}

// 服务端稳定计数（契约 §3.2 可选 project_id）。集中于此让 /overview 与
// /findings 共享同一缓存条目并消掉两处裸 fetch（UI_DESIGN K-4）。
export function useFindingsSummary(projectId?: string) {
  return useQuery({
    queryKey: ["findings", "summary", projectId ?? "all"],
    queryFn: () => api.findingsSummary(projectId),
    refetchInterval: 10_000,
    staleTime: 5_000,
    placeholderData: (prev) => prev,
  });
}

// Server-side paginated + filtered scans, used by the /scans page. Keeps the
// previous page's data visible while the next page loads (placeholderData) so
// paging/filtering does not flash an empty table.
export function useScansPage(params: ListParams) {
  return useQuery({
    queryKey: qk.scansPage(params),
    queryFn: () => api.listScansPage(params),
    refetchInterval: 15000,
    placeholderData: (prev) => prev,
  });
}

export function useTokenUsage(id?: string, running?: boolean) {
  return useQuery({
    queryKey: id ? ["scan", id, "token-usage"] : ["scan", "none", "token-usage"],
    queryFn: () => api.scanTokenUsage(id!),
    enabled: !!id,
    // Keep the prompt-tokens chart live while the scan is running.
    refetchInterval: running ? 5000 : false,
    retry: false,
  });
}

export interface ScanTrafficOptions {
  endpoint?: string;
  method?: string;
  /** Hostname join key — pins traffic to one discovered asset. */
  host?: string;
  limit?: number;
  enabled?: boolean;
  /** Live views poll; archive-only consumers leave it off. */
  refetchInterval?: number | false;
  staleTime?: number;
}

// 流量代理 raw 交换（finding 证据 / 实时动态 HTTP 视图）。归档是扫描结束后
// 仍在的文件；实时视图传 refetchInterval 轮询增量，详情弹窗懒加载即可。
export function useScanTrafficQuery(
  scanId: string | undefined,
  options: ScanTrafficOptions = {},
) {
  const {
    endpoint,
    method,
    host,
    limit = 20,
    enabled = true,
    refetchInterval = false,
    staleTime = 30_000,
  } = options;
  return useQuery({
    queryKey: [
      "scan",
      scanId ?? "none",
      "traffic",
      endpoint ?? "",
      method ?? "",
      host ?? "",
      limit,
    ],
    queryFn: () => api.scanTraffic(scanId!, { endpoint, method, host, limit }),
    enabled: enabled && !!scanId,
    staleTime,
    refetchInterval,
    retry: false,
  });
}

// 兼容旧调用面：finding 详情按 endpoint/method 过滤的单次读取。
export function useScanTraffic(
  scanId: string | undefined,
  endpoint?: string,
  method?: string,
  enabled = true,
) {
  return useScanTrafficQuery(scanId, { endpoint, method, enabled });
}

// Run 文件浏览器：单层目录列表。扫描运行中每 5s 轮询当前目录，让新产物
// 自动出现；目录切换时 path 进 key，各目录独立缓存（懒展开）。
export function useScanFiles(scanId: string | undefined, path: string, running?: boolean) {
  return useQuery({
    queryKey: ["scan", scanId ?? "none", "files", path],
    queryFn: () => api.scanFiles(scanId!, path),
    enabled: !!scanId,
    refetchInterval: running ? 5000 : false,
    retry: false,
    placeholderData: (prev) => prev,
  });
}

// 单个文件的文本读取（二进制由后端标记，不带 content）。只在选中文件时启用。
export function useScanFileContent(scanId: string | undefined, path: string | null) {
  return useQuery({
    queryKey: ["scan", scanId ?? "none", "file-content", path],
    queryFn: () => api.scanFileContent(scanId!, path!),
    enabled: !!scanId && !!path,
    retry: false,
  });
}

export function useScan(id?: string) {
  return useQuery({
    queryKey: id ? qk.scan(id) : ["scan", "none"],
    queryFn: () => api.getScan(id!),
    enabled: !!id,
    refetchInterval: (query) => {
      const scan = query.state.data;
      if (!scan) return 2000;
      const status = (scan.status || "").toLowerCase();
      return status === "running" || status === "pending" || status === "paused"
        ? 2000
        : false;
    },
  });
}

export function useInstances(projectId?: string) {
  return useQuery({
    // 项目作用域 key（["instances"] 前缀失效仍命中全部作用域）。
    queryKey: ["instances", projectId ?? "all"],
    queryFn: () => api.instances(projectId),
    refetchInterval: 8000,
    placeholderData: (prev) => prev,
  });
}

// Server-side paginated + filtered instances, used by the /instances page.
export function useInstancesPage(params: ListParams) {
  return useQuery({
    queryKey: qk.instancesPage(params),
    queryFn: () => api.instancesPage(params),
    refetchInterval: 8000,
    placeholderData: (prev) => prev,
  });
}

export function useInstanceEvents(id?: string) {
  return useQuery({
    queryKey: id ? qk.instanceEvents(id) : ["instance", "none", "events"],
    queryFn: () => api.instanceEvents(id!),
    enabled: !!id,
    staleTime: 1000,
    refetchInterval: 5000,
  });
}

export function useQueueStatus() {
  return useQuery({
    queryKey: qk.queue,
    queryFn: api.queueStatus,
    refetchInterval: 10000,
  });
}

export function useRateLimit() {
  return useQuery({
    queryKey: qk.rateLimit,
    queryFn: api.rateLimit,
  });
}

export function useLLMSettings() {
  return useQuery({
    queryKey: qk.llmSettings,
    queryFn: api.llmSettings,
  });
}

export function useEnvironmentSettings() {
  return useQuery({
    queryKey: qk.environmentSettings,
    queryFn: api.environmentSettings,
  });
}

export function useStartScan() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: ScanRequest) => api.startScan(req),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.instances });
      qc.invalidateQueries({ queryKey: qk.scans });
      qc.invalidateQueries({ queryKey: qk.status });
    },
  });
}

export function useStopAll() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.stopAll(),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.instances });
      qc.invalidateQueries({ queryKey: qk.status });
    },
  });
}

export function useStopInstance() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, reason }: { id: string; reason?: string }) =>
      api.stopInstance(id, reason),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.instances });
      qc.invalidateQueries({ queryKey: ["instances"] });
    },
  });
}

export function usePauseInstance() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.pauseInstance(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.instances }),
  });
}

export function useResumeInstance() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.resumeInstance(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.instances }),
  });
}

export function useRestartInstance() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.restartInstance(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.instances }),
  });
}

export function useStartSavedInstance() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.startSavedInstance(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.instances }),
  });
}

export function useDeleteScan() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteScan(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.instances });
      qc.invalidateQueries({ queryKey: qk.scans });
    },
  });
}

export function useDeleteVuln() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ scanId, vulnId }: { scanId: string; vulnId: string }) =>
      api.deleteVuln(scanId, vulnId),
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: qk.scans });
      qc.invalidateQueries({ queryKey: qk.instances });
      // F-1: the deleted finding is also embedded in the open scan detail and
      // in the findings list/summary caches.
      qc.invalidateQueries({ queryKey: qk.scan(vars.scanId) });
      qc.invalidateQueries({ queryKey: ["findings"] });
      qc.invalidateQueries({ queryKey: ["task"] });
    },
    onError: (err) => {
      console.error("Failed to delete vulnerability:", err);
    },
  });
}

export function useUpdateRateLimit() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.updateRateLimit,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.rateLimit });
      qc.invalidateQueries({ queryKey: qk.environmentSettings });
    },
  });
}

export function useUpdateLLMSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.updateLLMSettings,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.llmSettings });
      qc.invalidateQueries({ queryKey: qk.environmentSettings });
      qc.invalidateQueries({ queryKey: qk.authProfiles });
      qc.invalidateQueries({ queryKey: qk.version });
    },
  });
}

export function useUpdateEnvironmentSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.updateEnvironmentSettings,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.environmentSettings });
      qc.invalidateQueries({ queryKey: qk.llmSettings });
      qc.invalidateQueries({ queryKey: qk.rateLimit });
      qc.invalidateQueries({ queryKey: qk.version });
      qc.invalidateQueries({ queryKey: qk.instances });
    },
  });
}

export function useImportSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.importSettings,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.environmentSettings });
      qc.invalidateQueries({ queryKey: qk.llmSettings });
      qc.invalidateQueries({ queryKey: qk.authProfiles });
      qc.invalidateQueries({ queryKey: qk.version });
    },
  });
}

export function useQueueResume() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.queueResume,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.queue });
      qc.invalidateQueries({ queryKey: qk.instances });
    },
  });
}

export function useQueueClear() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.queueClear,
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.queue }),
  });
}

export function useSchedulesList() {
  return useQuery({
    queryKey: qk.schedules,
    queryFn: api.listSchedules,
    refetchInterval: 15000,
  });
}

export function useCreateSchedule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.createSchedule,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.schedules });
    },
  });
}

export function useUpdateSchedule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, schedule }: { id: string; schedule: Partial<ScanSchedule> }) =>
      api.updateSchedule(id, schedule),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.schedules });
    },
  });
}

export function useDeleteSchedule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.deleteSchedule,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.schedules });
    },
  });
}

export function useTriggerSchedule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.triggerSchedule,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.schedules });
      qc.invalidateQueries({ queryKey: qk.instances });
    },
  });
}

// Legacy-import banner: fetched once on first load. The server-side count
// is only meaningful on the run that did the import (in-memory only,
// resets on restart). Stale-time Infinity prevents background refetch
// from re-showing a dismissed banner mid-session.
export function useLegacyImportStatus() {
  return useQuery({
    queryKey: qk.legacyImport,
    queryFn: api.legacyImportStatus,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
}

export function useDismissLegacyImport() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.dismissLegacyImport,
    onSuccess: (data) => {
      qc.setQueryData(qk.legacyImport, data);
    },
  });
}

// Auth profile picker source. Drives the credentials list on the LLM
// Settings tab and the provider/model selector on /new-scan and
// /schedules. The staleTime keeps switching between pages snappy;
// mutations elsewhere invalidate qk.authProfiles so this stays fresh
// without a polling interval.
export function useAuthProfiles() {
  return useQuery({
    queryKey: qk.authProfiles,
    queryFn: api.listAuthProfiles,
    staleTime: 30_000,
  });
}

// Provider catalog — compiled into the binary in v4.4.22, so the
// list is effectively immutable across a session. Cached forever
// since the only way to change it is a server upgrade.
export function useProviders() {
  return useQuery({
    queryKey: qk.providers,
    queryFn: api.listProviders,
    staleTime: Infinity,
  });
}

export function useDiscoverProviderModels() {
  return useMutation({
    mutationFn: ({ provider, profile }: { provider: string; profile?: string }) =>
      api.discoverProviderModels(provider, profile),
  });
}

// ---------------------------------------------------------------------------
// Auth profile mutation hooks. The catalog is read-only in v4.4.22, so
// there is no provider-create / -update / -delete and no openclaw or
// legacy-migrate mutation. Profile CRUD is the only state-changing
// surface left.
// ---------------------------------------------------------------------------

export function useCreateAPIKeyProfile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.createAPIKeyProfile,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.authProfiles });
      qc.invalidateQueries({ queryKey: qk.llmSettings });
    },
  });
}

export function useOAuthStart() {
  // No cache invalidation: the start handshake is an out-of-band
  // flow that finalizes via /complete or via the loopback callback,
  // which is what produces the new profile. Components poll
  // useAuthProfiles and watch for the new entry.
  return useMutation({
    mutationFn: api.oauthStart,
  });
}

export function useOAuthComplete() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.oauthComplete,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.authProfiles });
      qc.invalidateQueries({ queryKey: qk.llmSettings });
    },
  });
}

export function useRefreshAuthProfile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (key: string) => api.refreshAuthProfile(key),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.authProfiles });
      qc.invalidateQueries({ queryKey: qk.llmSettings });
    },
  });
}

export function useDeleteAuthProfile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (key: string) => api.deleteAuthProfile(key),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.authProfiles });
      qc.invalidateQueries({ queryKey: qk.llmSettings });
    },
  });
}

// ---------------------------------------------------------------------------
// R3: projects / tasks / runs / approvals hooks.
//
// 这些端点在后端 R1/R2 落地前会 404：查询统一 retry:false，让页面立刻
// 进入 ErrorState 而不是退避重试刷屏。审批单轮询自适应：有待审单 5s，
// 无单 30s —— OpsPulse 倒计时的实时性只花在有单时的成本上。
// ---------------------------------------------------------------------------

export function useProjects() {
  return useQuery({
    queryKey: qk.projects,
    queryFn: api.listProjects,
    retry: false,
    refetchInterval: 15000,
  });
}

export function useCreateProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.createProject,
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.projects }),
  });
}

export function useUpdateProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      req,
    }: {
      id: string;
      req: Partial<{ name: string; scope: ProjectRequest["scope"]; archived: boolean }>;
    }) => api.updateProject(id, req),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.projects }),
  });
}

export function useDeleteProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.deleteProject,
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.projects }),
  });
}

/**
 * 项目范围/围栏视图：项目页的配置模块读它，快速创建任务写它。
 * 20s 轻轮询只为了让 active_runs / fence_bound 在改围栏后自己收敛，
 * 保存与试算都由 mutation 显式失效。
 */
export function useProjectScope(projectId?: string) {
  return useQuery({
    queryKey: projectId ? qk.projectScope(projectId) : ["projects", "none", "scope"],
    queryFn: () => api.getProjectScope(projectId!),
    enabled: !!projectId,
    retry: false,
    refetchInterval: 20000,
  });
}

/** 保存范围（含围栏）：成功后项目列表与围栏视图同时刷新。 */
export function useUpdateProjectScope() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, scope }: { id: string; scope: ProjectScope }) =>
      api.updateProjectScope(id, scope),
    onSuccess: (view) => {
      qc.invalidateQueries({ queryKey: qk.projectScope(view.project_id) });
      qc.invalidateQueries({ queryKey: qk.projects });
    },
  });
}

/** 试算（不落库）：项目页「校验」按钮与快速创建前的目标预检共用。 */
export function useValidateProjectScope() {
  return useMutation({
    mutationFn: ({
      id,
      req,
    }: {
      id: string;
      req: { scope?: ProjectScope; targets?: string[] };
    }) => api.validateProjectScope(id, req),
  });
}

/**
 * 快速创建任务：一次交互 = 建任务 + 立即发起运行。
 *
 * 两步不是原子的（后端 createTask 与 POST runs 是两个契约端点），失败语义
 * 因此必须显式：任务已创建但启动失败时返回 launchError，调用方提示「任务已
 * 保存，可在任务页重试启动」而不是把已建的任务当成失败丢掉。
 */
export function useQuickCreateTask() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({
      projectId,
      req,
      launch,
    }: {
      projectId: string;
      req: TaskRequest;
      launch?: Partial<ScanRequest>;
    }) => {
      const task = await api.createTask(projectId, req);
      if (!launch) return { task, run: null, launchError: null };
      try {
        const run = await api.startTaskRun(task.id, launch);
        return { task, run, launchError: null };
      } catch (err) {
        return {
          task,
          run: null,
          launchError: err instanceof Error ? err.message : String(err),
        };
      }
    },
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: qk.tasks(vars.projectId) });
      qc.invalidateQueries({ queryKey: qk.projects });
      qc.invalidateQueries({ queryKey: qk.instances });
      qc.invalidateQueries({ queryKey: qk.status });
    },
  });
}

export function useTasks(projectId?: string) {
  return useQuery({
    queryKey: projectId ? qk.tasks(projectId) : ["projects", "none", "tasks"],
    queryFn: () => api.listTasks(projectId!),
    enabled: !!projectId,
    retry: false,
    refetchInterval: 15000,
    placeholderData: (prev) => prev,
  });
}

export function useTask(id?: string) {
  return useQuery({
    queryKey: id ? qk.task(id) : ["task", "none"],
    queryFn: () => api.getTask(id!),
    enabled: !!id,
    retry: false,
    refetchInterval: 10000,
  });
}

export function useCreateTask() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ projectId, req }: { projectId: string; req: TaskRequest }) =>
      api.createTask(projectId, req),
    onSuccess: (_data, vars) =>
      qc.invalidateQueries({ queryKey: qk.tasks(vars.projectId) }),
  });
}

export function useUpdateTask() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, req }: { id: string; req: Partial<TaskRequest> }) =>
      api.updateTask(id, req),
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: ["task", vars.id] });
      qc.invalidateQueries({ queryKey: ["projects"] });
    },
  });
}

export function useDeleteTask() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.deleteTask,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["projects"] });
    },
  });
}

export function useStartTaskRun() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      overrides,
    }: { id: string; overrides?: Partial<ScanRequest> }) =>
      api.startTaskRun(id, overrides),
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: ["projects"] });
      qc.invalidateQueries({ queryKey: ["task", vars.id, "runs"] });
      qc.invalidateQueries({ queryKey: ["task", vars.id] });
      qc.invalidateQueries({ queryKey: qk.instances });
      qc.invalidateQueries({ queryKey: qk.status });
    },
  });
}

export function useTaskRuns(id?: string) {
  return useQuery({
    queryKey: id ? qk.taskRuns(id) : ["task", "none", "runs"],
    queryFn: () => api.taskRuns(id!),
    enabled: !!id,
    retry: false,
    refetchInterval: 8000,
    placeholderData: (prev) => prev,
  });
}

/** 会话转录：分页读最新一页；运行中短间隔轮询（SSE 之外无需新通道）。 */
/** 会话侧栏：项目内全部 run（含任务名、标题、置顶、归档）。 */
export function useProjectConversations(projectId?: string, includeArchived = false) {
  return useQuery({
    queryKey: ["conversation", "project", projectId ?? "none", includeArchived],
    queryFn: () => api.projectConversations(projectId!, includeArchived),
    enabled: !!projectId,
    retry: false,
    refetchInterval: 8000,
    placeholderData: (prev) => prev,
  });
}

export function useTranscript(instanceId?: string, beforeSeq?: number, limit = 80) {
  return useQuery({
    queryKey: ["conversation", instanceId ?? "none", "transcript", beforeSeq ?? 0, limit],
    queryFn: () => api.conversationTranscript(instanceId!, beforeSeq, limit),
    enabled: !!instanceId,
    retry: false,
    refetchInterval: (query) => (query.state.data?.running ? 2500 : false),
    placeholderData: (prev) => prev,
  });
}

export function useUpdateConversation(taskId?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ instanceId, patch }: { instanceId: string; patch: ConversationPatch }) =>
      api.updateConversation(instanceId, patch),
    onSuccess: () => {
      if (taskId) qc.invalidateQueries({ queryKey: qk.taskRuns(taskId) });
      qc.invalidateQueries({ queryKey: ["conversation"] });
    },
  });
}

export function useTaskFindings(id?: string) {
  return useQuery({
    queryKey: id ? qk.taskFindings(id) : ["task", "none", "findings"],
    queryFn: () => api.taskFindings(id!),
    enabled: !!id,
    retry: false,
    refetchInterval: 15000,
    placeholderData: (prev) => prev,
  });
}

// Run 账本投影（任务详情「账本」tab）。运行中 8s 轮询跟随假设生命周期变化；
// 终态 run 一次拉取即可，但仍给 30s 低频兜底（快照只在引擎写时变化）。
// Run 全量事件（会话页角色分组的数据源：thought/decision/phase/plan 等
// 转录七类之外的过程明细）。优先取实例内存缓冲（完整、含运行中增量）；
// 缓冲不可用（引擎重启/实例逐出/404）时回落持久事件 journal
// （GET /api/scans/{id}/events，events.jsonl 窗口读取，类型不过滤）。
// 运行中 5s 轮询跟随（§9 instance events 节奏），SSE 增量由组件侧防抖 refetch 补充。
export function useRunEvents(instanceId?: string, live?: boolean) {
  return useQuery({
    queryKey: ["run-events", instanceId ?? "none"],
    queryFn: async () => {
      try {
        const events = await api.instanceEvents(instanceId!);
        if (events.length > 0) return { source: "buffer" as const, events };
      } catch {
        // 缓冲缺席 → journal 兜底
      }
      // journal 只能 offset 从头读：先探 total，再取最新窗口（上限 1000，
      // 与服务端 maxEventsWindow 对齐）。
      const head = await api.scanEvents(instanceId!, 0, 1);
      const total = head.total ?? 0;
      const page = await api.scanEvents(instanceId!, Math.max(0, total - 1000), 1000);
      return { source: "journal" as const, events: page.events };
    },
    enabled: !!instanceId,
    refetchInterval: live ? 5000 : false,
    placeholderData: (prev) => prev,
  });
}

export function useScanSecurity(scanId?: string, running?: boolean) {
  return useQuery({
    queryKey: ["scan", scanId ?? "none", "security"],
    queryFn: () => api.scanSecurity(scanId!),
    enabled: !!scanId,
    retry: false,
    refetchInterval: running ? 10000 : 30000,
    placeholderData: (prev) => prev,
  });
}

export function useScanLedger(scanId?: string, running?: boolean) {
  return useQuery({
    queryKey: ["scan", scanId ?? "none", "ledger"],
    queryFn: () => api.scanLedger(scanId!),
    enabled: !!scanId,
    retry: false,
    refetchInterval: running ? 8000 : 30000,
    placeholderData: (prev) => prev,
  });
}

// 任务情报（任务详情「情报」tab）：findings 同款 15s 节奏；分诊成功后由
// mutation 失效刷新。
export function useTaskIntelligence(taskId?: string) {
  return useQuery({
    queryKey: taskId
      ? qk.taskIntelligence(taskId)
      : ["task", "none", "intelligence"],
    queryFn: () => api.taskIntelligence(taskId!),
    enabled: !!taskId,
    retry: false,
    refetchInterval: 15000,
    placeholderData: (prev) => prev,
  });
}

export function useIntelTriage(taskId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      fingerprint,
      status,
    }: { fingerprint: string; status: IntelTriageStatus }) =>
      api.triageIntelligence(taskId, fingerprint, status),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.taskIntelligence(taskId) });
    },
  });
}

export function useApprovals(
  opts: {
    status?: "pending" | "decided" | "all";
    projectId?: string;
    taskId?: string;
  } = {},
) {
  const filter = opts.status ?? "pending";
  return useQuery({
    queryKey: qk.approvals(filter, opts.projectId, opts.taskId),
    queryFn: () => api.listApprovals(opts),
    retry: false,
    // 自适应轮询在组件层用 refetchInterval 函数表达：pending>0 → 5s。
    refetchInterval: (query) => {
      const data = query.state.data as
        | { approvals: { status: string }[]; pending_total: number }
        | undefined;
      return data && data.pending_total > 0 ? 5000 : 30000;
    },
    placeholderData: (prev) => prev,
  });
}

export function useApprovalsSummary() {
  return useQuery({
    queryKey: qk.approvalsSummary,
    queryFn: api.approvalsSummary,
    retry: false,
    refetchInterval: (query) => {
      const data = query.state.data;
      return data && data.pending_total > 0 ? 5000 : 30000;
    },
  });
}

export function useDecideApproval() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      decision,
      reason,
    }: { id: string; decision: "approved" | "rejected"; reason?: string }) =>
      api.decideApproval(id, decision, reason),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["approvals"] });
    },
  });
}

export function useBatchDecideApprovals() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      ids,
      decision,
      reason,
    }: { ids: string[]; decision: "approved" | "rejected"; reason?: string }) =>
      api.batchDecideApprovals(ids, decision, reason),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["approvals"] });
    },
  });
}

// ---------------------------------------------------------------------------
// 审批豁免规则（approve & remember，契约 §3.4）。列表挂在 ["approvals"]
// 前缀下：规则增删会连带刷新审批相关缓存。

export function useApprovalRules(
  opts: { projectId?: string; kind?: "command" | "http" } = {},
) {
  return useQuery({
    queryKey: qk.approvalRules(opts.projectId, opts.kind),
    queryFn: () => api.listApprovalRules(opts),
    retry: false,
    placeholderData: (prev) => prev,
  });
}

export function useCreateApprovalRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: ApprovalRuleRequest) => api.createApprovalRule(req),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["approvals"] });
    },
  });
}

export function useDeleteApprovalRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteApprovalRule(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["approvals"] });
    },
  });
}

// ---------------------------------------------------------------------------
// Guidance：人工在环插入意图（契约 §3.1）。4zreco 模式：发送即同步接受，
// 无乐观回显——列表从服务端 refetch 渲染；request_id 幂等由调用方生成并持有。
// ---------------------------------------------------------------------------

export function useTaskObjective(id?: string) {
  return useQuery({
    queryKey: id ? ["task", id, "objective"] : ["task", "none", "objective"],
    queryFn: () => api.taskObjective(id!),
    enabled: !!id,
    retry: false,
    refetchInterval: 15000,
    placeholderData: (prev) => prev,
  });
}

export function useTaskGuidance(id?: string) {
  return useQuery({
    queryKey: id ? ["task", id, "guidance"] : ["task", "none", "guidance"],
    queryFn: () => api.taskGuidance(id!),
    enabled: !!id,
    retry: false,
    // Initial history only. New turns and status changes arrive over SSE.
    placeholderData: (prev) => prev,
  });
}

export function useSendTaskGuidance() {
  return useMutation({
    mutationFn: ({
      id,
      message,
      requestId,
      instanceId,
    }: { id: string; message: string; requestId: string; instanceId?: string }) =>
      api.sendTaskGuidance(id, message, requestId, instanceId),
  });
}

// 旁路提问（chat_questions.go）：受理即返回，回答经 chat_reply SSE 回流转录
// ——转录的 SSE 桥已监听该事件类型触发 refetch，这里不做任何缓存失效。
export function useSendTaskQuestion() {
  return useMutation({
    mutationFn: ({
      id,
      message,
      requestId,
      instanceId,
    }: { id: string; message: string; requestId: string; instanceId?: string }) =>
      api.sendTaskQuestion(id, message, requestId, instanceId),
  });
}

// ---------------------------------------------------------------------------
// R5 信息空间（只读；契约 §5）。项目切换即重新取数，过滤条件进入 query key。
// ---------------------------------------------------------------------------

export function useInfoRegion(params: InfoRegionParams, enabled = true) {
  return useQuery({
    queryKey: qk.infoRegion(params),
    queryFn: () => api.infoRegion(params),
    enabled: enabled && !!params.projectId,
    placeholderData: (prev) => prev,
    retry: false,
  });
}

export function useInfoContext(params: InfoContextParams, enabled = true) {
  return useQuery({
    queryKey: qk.infoContext(params),
    queryFn: () => api.infoContext(params),
    enabled: enabled && !!params.projectId && !!params.scanId,
    placeholderData: (prev) => prev,
    retry: false,
  });
}

export function useInfoAssociation(params: InfoAssociationParams, enabled = true) {
  return useQuery({
    queryKey: qk.infoAssociation(params),
    queryFn: () => api.infoAssociation(params),
    // scanId 缺省 = 项目级聚合形态；单扫描形态由调用方传入。
    enabled: enabled && !!params.projectId && !!params.ids,
    placeholderData: (prev) => prev,
    retry: false,
  });
}

// Cold-path sync state for one run. Polls at the shared 15s cadence; endpoint
// failures surface through `error` so the page can degrade to hiding the
// badge instead of blocking the three tabs.
export function useColdPathSync(scanId: string, enabled = true) {
  return useQuery({
    queryKey: qk.coldPathSync(scanId),
    queryFn: (): Promise<ColdPathSyncState> => api.coldPathSync(scanId),
    enabled: enabled && !!scanId,
    refetchInterval: 15000,
    retry: false,
  });
}

// Domain audit projection for one run (charter M2, read-only). Append-only
// history: poll at the 15s cadence while the run is live; after the run
// terminates the journal no longer grows, so a single fetch suffices. Errors
// surface through `error` — the timeline section renders its own honest
// failure instead of blocking the ledger tab.
// Exploration projection (4zreco dual-graph source): one node/edge model
// feeding both the topology graph (探索链路) and the stream board (播报板).
// 15s poll while the run is live, single fetch after — same discipline as
// the audit projection.
export function useScanExploration(scanId: string, running = false) {
  return useQuery({
    queryKey: qk.scanExploration(scanId),
    queryFn: (): Promise<ExplorationView> => api.scanExploration(scanId),
    enabled: !!scanId,
    refetchInterval: running ? 15000 : false,
    retry: false,
  });
}

export function useScanAudit(scanId: string, running = false) {
  return useQuery({
    queryKey: qk.scanAudit(scanId),
    queryFn: (): Promise<ScanAuditView> => api.scanAudit(scanId),
    enabled: !!scanId,
    refetchInterval: running ? 15000 : false,
    retry: false,
  });
}

// ── 集成模块（MCP / Skills / Agents）─────────────────────────────────────────

export function useMcpServers() {
  return useQuery({
    queryKey: qk.mcpServers,
    queryFn: api.listMcpServers,
    placeholderData: (prev) => prev,
    retry: false,
  });
}

export function useSkills() {
  return useQuery({
    queryKey: qk.skills,
    queryFn: api.listSkills,
    placeholderData: (prev) => prev,
    retry: false,
  });
}

export function useAgentProfiles() {
  return useQuery({
    queryKey: qk.agentProfiles,
    queryFn: api.listAgentProfiles,
    placeholderData: (prev) => prev,
    retry: false,
  });
}

function useInvocationsQuery<T extends InvocationList>(
  key: readonly unknown[],
  fn: () => Promise<T>,
) {
  return useQuery({
    queryKey: key,
    queryFn: fn,
    placeholderData: (prev) => prev,
    retry: false,
    refetchInterval: 15_000,
  });
}

export function useMcpInvocations(params: InvocationListParams) {
  return useInvocationsQuery(qk.mcpInvocations(params), () =>
    api.listInvocations("mcp", params));
}

export function useSkillInvocations(params: InvocationListParams) {
  return useInvocationsQuery(qk.skillInvocations(params), () =>
    api.listInvocations("skills", params));
}

export function useAgentInvocations(params: InvocationListParams) {
  return useInvocationsQuery(qk.agentInvocations(params), () =>
    api.listInvocations("agents", params));
}

/** invalidateIntegration 刷新同前缀的定义缓存（三模块共用 mutation 收尾）。 */
export function useInvalidateIntegrations() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: qk.mcpServers });
    void qc.invalidateQueries({ queryKey: qk.skills });
    void qc.invalidateQueries({ queryKey: qk.agentProfiles });
    void qc.invalidateQueries({ queryKey: ["mcp", "invocations"] });
    void qc.invalidateQueries({ queryKey: ["skills", "invocations"] });
    void qc.invalidateQueries({ queryKey: ["agent-profiles", "invocations"] });
  };
}

export function useCreateMcpServer() {
  const invalidate = useInvalidateIntegrations();
  return useMutation({
    mutationFn: (req: McpServerRequest) => api.createMcpServer(req),
    onSuccess: invalidate,
  });
}

export function useUpdateMcpServer() {
  const invalidate = useInvalidateIntegrations();
  return useMutation({
    mutationFn: ({ id, patch }: { id: string; patch: McpServerPatch }) =>
      api.updateMcpServer(id, patch),
    onSuccess: invalidate,
  });
}

export function useDeleteMcpServer() {
  const invalidate = useInvalidateIntegrations();
  return useMutation({
    mutationFn: (id: string) => api.deleteMcpServer(id),
    onSuccess: invalidate,
  });
}

export function useRefreshMcpServer() {
  const invalidate = useInvalidateIntegrations();
  return useMutation({
    mutationFn: (id: string) => api.refreshMcpServer(id),
    onSuccess: invalidate,
  });
}

export function useCreateSkill() {
  const invalidate = useInvalidateIntegrations();
  return useMutation({
    mutationFn: (req: UserSkillRequest) => api.createSkill(req),
    onSuccess: invalidate,
  });
}

export function useUpdateSkill() {
  const invalidate = useInvalidateIntegrations();
  return useMutation({
    mutationFn: ({ id, patch }: { id: string; patch: UserSkillPatch }) =>
      api.updateSkill(id, patch),
    onSuccess: invalidate,
  });
}

export function useDeleteSkill() {
  const invalidate = useInvalidateIntegrations();
  return useMutation({
    mutationFn: (id: string) => api.deleteSkill(id),
    onSuccess: invalidate,
  });
}

export function useCreateAgentProfile() {
  const invalidate = useInvalidateIntegrations();
  return useMutation({
    mutationFn: (req: AgentProfileRequest) => api.createAgentProfile(req),
    onSuccess: invalidate,
  });
}

export function useUpdateAgentProfile() {
  const invalidate = useInvalidateIntegrations();
  return useMutation({
    mutationFn: ({ id, patch }: { id: string; patch: AgentProfilePatch }) =>
      api.updateAgentProfile(id, patch),
    onSuccess: invalidate,
  });
}

export function useDeleteAgentProfile() {
  const invalidate = useInvalidateIntegrations();
  return useMutation({
    mutationFn: (id: string) => api.deleteAgentProfile(id),
    onSuccess: invalidate,
  });
}

// ── RBAC account management (admin-only) ──────────────────────────────────

export function useUsers() {
  return useQuery({
    queryKey: qk.users,
    queryFn: api.listUsers,
    staleTime: 30_000,
    retry: false,
  });
}

export function useCreateUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.createUser,
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.users }),
  });
}

export function useUpdateUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      patch,
    }: {
      id: string;
      patch: { role?: Role; disabled?: boolean; password?: string };
    }) => api.updateUser(id, patch),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.users }),
  });
}

export function useDeleteUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.deleteUser,
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.users }),
  });
}

/** 类型仅作 API 返回形状标注的本地引用（避免 unused 告警由 hooks 消费）。 */
export type { AgentProfile, McpServer, UserSkill };

