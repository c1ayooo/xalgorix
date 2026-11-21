import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type {
  InfoAssociationParams,
  InfoContextParams,
  InfoRegionParams,
  ListParams,
  ProjectRequest,
  ScanRequest,
  ScanSchedule,
  TaskRequest,
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
  agentMail: ["settings", "agentmail"] as const,
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
  tasks: (projectId: string) => ["projects", projectId, "tasks"] as const,
  task: (id: string) => ["task", id] as const,
  taskRuns: (id: string) => ["task", id, "runs"] as const,
  taskFindings: (id: string) => ["task", id, "findings"] as const,
  approvals: (filter: string, projectId?: string) =>
    ["approvals", filter, projectId ?? "all"] as const,
  approvalsSummary: ["approvals", "summary"] as const,
  infoRegion: (params: InfoRegionParams) =>
    ["info", "region", params] as const,
  infoContext: (params: InfoContextParams) =>
    ["info", "context", params] as const,
  infoAssociation: (params: InfoAssociationParams) =>
    ["info", "association", params] as const,
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
export function useFindingsList(projectId?: string) {
  return useQuery({
    queryKey: ["findings", "list", projectId ?? "all"],
    queryFn: () => api.listFindings(projectId),
    refetchInterval: 15000,
    placeholderData: (prev) => prev,
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

export function useAgentMail() {
  return useQuery({
    queryKey: qk.agentMail,
    queryFn: api.agentMail,
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

export function useUpdateAgentMail() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.updateAgentMail,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.agentMail });
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
      qc.invalidateQueries({ queryKey: qk.agentMail });
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

export function useApprovals(
  opts: { status?: "pending" | "decided" | "all"; projectId?: string } = {},
) {
  const filter = opts.status ?? "pending";
  return useQuery({
    queryKey: qk.approvals(filter, opts.projectId),
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
// Guidance：人工在环插入意图（契约 §3.1）。4zreco 模式：发送即同步接受，
// 无乐观回显——列表从服务端 refetch 渲染；request_id 幂等由调用方生成并持有。
// ---------------------------------------------------------------------------

export function useTaskGuidance(id?: string) {
  return useQuery({
    queryKey: id ? ["task", id, "guidance"] : ["task", "none", "guidance"],
    queryFn: () => api.taskGuidance(id!),
    enabled: !!id,
    retry: false,
    refetchInterval: 8000,
    placeholderData: (prev) => prev,
  });
}

export function useSendTaskGuidance() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      message,
      requestId,
      instanceId,
    }: { id: string; message: string; requestId: string; instanceId?: string }) =>
      api.sendTaskGuidance(id, message, requestId, instanceId),
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: ["task", vars.id, "guidance"] });
    },
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
    enabled: enabled && !!params.projectId && !!params.scanId && !!params.ids,
    placeholderData: (prev) => prev,
    retry: false,
  });
}
