import http from "node:http";

// ---------------------------------------------------------------------------
// Mock backend for the SPA dev environment.
//
// Shapes here mirror the Go server in internal/web/server.go and the TS types
// in webui/src/types/api.ts. State is held in-memory and mutated by the
// SPA's actions (Stop all, Restart, Delete, Save settings, Resume queue, etc.)
// so the UI feels real end-to-end — without those mutations the dashboard
// looked broken because every action would optimistically succeed and then
// the next poll would revert to the seed state.
// ---------------------------------------------------------------------------

const send = (res, body, status = 200, headers = {}) => {
  res.writeHead(status, {
    "content-type": "application/json",
    "access-control-allow-origin": "*",
    ...headers,
  });
  res.end(typeof body === "string" ? body : JSON.stringify(body));
};
const ok = (res, body = { status: "ok" }) => send(res, body);

// Run 文件浏览器 fixture：形状与真实 scanDir 一致（scan.json / events.jsonl /
// ledger.json / tool-outputs/ / tmp/<agentID>/ …）。不在 MOCK_SCAN_TEXT 里的
// 文件按二进制处理（content 接口只标记、不落内容）。
const MOCK_MTIME = Date.parse("2026-11-22T10:00:00Z");
const dirEntry = (name, type, size = 0) => ({ name, type, size, mtime: MOCK_MTIME });
const MOCK_SCAN_TREE = {
  ".": [
    dirEntry("scan.json", "file", 1_240),
    dirEntry("scan.meta.json", "file", 512),
    dirEntry("events.jsonl", "file", 88_210),
    dirEntry("ledger.json", "file", 6_430),
    dirEntry("domain_journal.jsonl", "file", 21_002),
    dirEntry("coverage.json", "file", 1_204),
    dirEntry("token-usage.jsonl", "file", 30_112),
    dirEntry("tool-outputs", "dir"),
    dirEntry("tmp", "dir"),
    dirEntry("xalgorix_report_inst_01.pdf", "file", 482_112),
  ],
  "tool-outputs": [
    dirEntry("to_000001", "file", 12_004),
    dirEntry("to_000002", "file", 512),
  ],
  tmp: [dirEntry("agent-recon", "dir")],
  "tmp/agent-recon": [
    dirEntry("nmap-result.txt", "file", 2_240),
    dirEntry("probe.js", "file", 340),
  ],
};
const MOCK_SCAN_FILE_INDEX = new Map();
for (const [dir, entries] of Object.entries(MOCK_SCAN_TREE)) {
  for (const e of entries) {
    if (e.type === "file") {
      MOCK_SCAN_FILE_INDEX.set(dir === "." ? e.name : `${dir}/${e.name}`, e.size);
    }
  }
}
const MOCK_SCAN_TEXT = {
  "scan.json": '{"id":"scan_01","target":"acme.com","status":"finished"}',
  "events.jsonl":
    '{"seq":1,"type":"phase","msg":"phase 1 start"}\n{"seq":2,"type":"tool","tool":"http_probe","msg":"GET /login"}',
  "tool-outputs/to_000001":
    "tool: http_probe\nstatus: 200\nnote: oversized output archived verbatim",
  "tmp/agent-recon/nmap-result.txt":
    "PORT     STATE  SERVICE\n443/tcp  open   https\n8080/tcp open   http-alt",
};

const nowIso = () => new Date().toISOString();

let state = {
  instances: [
    {
      id: "inst_01",
      name: "Acme Corp pentest",
      project_id: "proj_acme",
      targets: "acme.com",
      parent_target: "acme.com",
      status: "running",
      started_at: nowIso(),
      iterations: 42,
      tool_calls: 188,
      vuln_count: 7,
      total_tokens: 124_000,
      scan_mode: "deep",
      instruction: "Standard recon + active probing",
      severity_filter: ["high", "critical"],
      phases: [1, 2, 3, 4],
      company_name: "Acme",
      current_phase: 3,
      vulns: [
        { id: "v1", title: "/search 存在 SQL 注入", severity: "critical", endpoint: "https://acme.com/search?q=", cvss: 9.8, cve: "CVE-2024-1234" },
        { id: "v2", title: "/login 存在反射型 XSS", severity: "high", endpoint: "https://acme.com/login", cvss: 7.5 },
        { id: "v3", title: "/go 存在开放重定向", severity: "medium", endpoint: "https://acme.com/go", cvss: 4.3 },
      ],
    },
    {
      id: "inst_02",
      name: "Globex bounty",
      project_id: "proj_globex",
      targets: "globex.io",
      parent_target: "globex.io",
      status: "finished",
      started_at: nowIso(),
      finished_at: nowIso(),
      iterations: 80,
      tool_calls: 412,
      vuln_count: 2,
      total_tokens: 256_000,
      scan_mode: "balanced",
      company_name: "Globex",
      vulns: [
        { id: "v6", title: "S3 bucket misconfiguration", severity: "high", endpoint: "https://cdn.globex.io", cvss: 7.1 },
        { id: "v7", title: "Outdated jQuery 1.4.2", severity: "low", endpoint: "https://globex.io/static/jquery.js", cvss: 3.1 },
      ],
    },
  ],
  scans: [
    {
      id: "scan_01",
      instance_id: "inst_01",
      name: "Acme nightly",
      target: "acme.com",
      parent_target: "acme.com",
      started_at: nowIso(),
      status: "running",
      scan_mode: "deep",
      instruction: "Standard",
      severity_filter: ["high", "critical"],
      vuln_count: 7,
      iterations: 42,
      tool_calls: 188,
      total_tokens: 124_000,
      current_phase: 3,
      phases: [1, 2, 3, 4],
    },
    {
      id: "scan_02",
      name: "Globex weekly",
      target: "globex.io",
      started_at: nowIso(),
      finished_at: nowIso(),
      status: "finished",
      scan_mode: "balanced",
      vuln_count: 2,
      iterations: 80,
      tool_calls: 412,
      total_tokens: 256_000,
    },
  ],
  queue: {
    available: true,
    targets: ["acme.com", "globex.io", "initech.com"],
    current_idx: 0,
    remaining: 2,
    instruction: "Standard recon + active probing",
    scan_mode: "deep",
    started_at: nowIso(),
  },
  resources: {
    cpu_cores: 8,
    cpu_load_1m: 1.23,
    ram_total_mb: 16_384,
    ram_available_mb: 9_216,
    disk_free_mb: 245_760,
    level: "healthy",
    reason: "",
    max_instances: 4,
    manual_max_instances: 4,
    effective_max_instances: 4,
  },
  rateLimit: { requests: 60, window: 60 },
  schedules: [
    {
      id: "sched_01",
      name: "Acme nightly audit",
      targets: ["acme.com"],
      interval: "daily",
      run_at: "02:00",
      run_day: 1,
      timezone: "Asia/Shanghai",
      enabled: true,
      next_run: new Date(Date.now() + 3600e3).toISOString(),
      scan_mode: "single",
      scan_intensity: "active",
      recon_mode: "active",
      severity_filter: [],
      created_at: nowIso(),
    },
    {
      id: "sched_02",
      name: "Globex weekly recon",
      targets: ["globex.io"],
      interval: "weekly",
      run_day: 1,
      run_at: "03:30",
      timezone: "Asia/Shanghai",
      enabled: false,
      scan_mode: "single",
      severity_filter: [],
      created_at: nowIso(),
    },
  ],
  authProfiles: [],
  reqCounter: 0,
  // R3: projects / tasks / runs / approvals seed（docs/API_CONTRACT.md）。
  projects: [
    {
      id: "proj_acme",
      name: "Acme Corp",
      slug: "acme-corp",
      scope: { targets: ["acme.com", "api.acme.com"], allow_ports: [80, 443, 8080] },
      created_at: nowIso(),
      task_count: 2,
      active_runs: 1,
    },
    {
      id: "proj_globex",
      name: "Globex Bounty",
      slug: "globex-bounty",
      scope: { targets: ["globex.io"] },
      created_at: nowIso(),
      task_count: 1,
      active_runs: 0,
    },
  ],
  guidances: [
    {
      id: "gd_01",
      task_id: "task_acme_nightly",
      run_id: "run_01",
      instance_id: "inst_01",
      message: "绕过 Cloudflare 时优先试缓存投毒路径，别再直接打 /admin",
      status: "consumed",
      created_at: nowIso(),
      consumed_at: nowIso(),
    },
  ],
  // 任务情报（契约 §3.7 mock）：按 task_id 聚合的观测项 + 操作者分诊覆盖层。
  intelTriages: new Map(), // taskId -> Map(fingerprint -> triage)
  convMeta: new Map(), // instance_id -> { title?, pinned?, archived? }（会话元数据覆盖层）
  tasks: [
    {
      id: "task_acme_nightly",
      project_id: "proj_acme",
      name: "Nightly full assessment",
      goal: "Prove exploitable vulnerabilities on acme.com with verified findings",
      instruction: "Focus on auth flows and the /api/v1 surface",
      phase_policy: { phases: [1, 2, 3, 4], scan_mode: "single", intensity: "active" },
      schedule: { interval: "daily", time: "02:00", timezone: "Asia/Shanghai" },
      created_at: nowIso(),
      run_count: 3,
      last_run_at: nowIso(),
      last_run_status: "running",
      finding_count: 7,
    },
    {
      id: "task_acme_authz",
      project_id: "proj_acme",
      name: "Authorization matrix",
      goal: "Prove or reject IDOR/BOLA on api.acme.com with the two-account matrix",
      phase_policy: { phases: [8, 9], scan_mode: "single", intensity: "active" },
      created_at: nowIso(),
      run_count: 1,
      last_run_at: nowIso(),
      last_run_status: "finished",
      finding_count: 2,
    },
    {
      id: "task_globex_recon",
      project_id: "proj_globex",
      name: "Perimeter recon",
      goal: "Map the globex.io attack surface passively before engagement",
      phase_policy: { phases: [1], intensity: "passive" },
      created_at: nowIso(),
      run_count: 0,
      finding_count: 0,
    },
  ],
  runs: [
    {
      id: "run_01",
      task_id: "task_acme_nightly",
      instance_id: "inst_01",
      mode: "single",
      status: "running",
      started_at: nowIso(),
      vuln_count: 7,
      total_tokens: 124_000,
      trigger: "schedule",
    },
    {
      id: "run_02",
      task_id: "task_acme_authz",
      instance_id: "inst_02",
      mode: "single",
      status: "finished",
      started_at: nowIso(),
      finished_at: nowIso(),
      vuln_count: 2,
      total_tokens: 48_000,
      trigger: "manual",
    },
  ],
  approvals: [
    {
      id: "appr_01",
      project_id: "proj_acme",
      project_name: "Acme Corp",
      task_id: "task_acme_nightly",
      task_name: "Nightly full assessment",
      run_id: "run_01",
      instance_id: "inst_01",
      kind: "command",
      payload: {
        command: "sqlmap -u 'https://acme.com/search?q=1' --batch --risk=2 --level=3",
        workdir: "/data/scans/inst_01",
        reason: "exploitation-class tool (sqlmap)",
      },
      slice: {
        subject: "acme.com/search",
        purpose: "probe SQL injection hypothesis H-3",
        facts: [
          { content: "Anonymous GET /search?q=1' returns HTTP 500 with SQL error fragment", kind: "observed", confidence: 0.9 },
          { content: "WAF detected in banner (Cloudflare)", kind: "observed", confidence: 0.7 },
          { content: "Error-based SQLi is plausible on q parameter", kind: "inferred", confidence: 0.6 },
        ],
        contradiction_count: 0,
      },
      status: "pending",
      created_at: nowIso(),
      deadline_at: new Date(Date.now() + 9 * 60_000).toISOString(),
    },
    {
      id: "appr_02",
      project_id: "proj_acme",
      project_name: "Acme Corp",
      task_id: "task_acme_nightly",
      run_id: "run_01",
      instance_id: "inst_01",
      kind: "http",
      payload: {
        method: "POST",
        url: "https://api.acme.com/admin/users/delete",
        headers: { "Content-Type": "application/json" },
        body_excerpt: '{"user_id": 42}',
        reason: "destructive action on admin surface",
      },
      slice: {
        subject: "api.acme.com/admin",
        purpose: "verify broken access control (BFLA)",
        facts: [
          { content: "GET /admin/users with role-A session returns 200", kind: "observed", confidence: 0.95 },
          { content: "Same request anonymous returns 401", kind: "observed", confidence: 0.95 },
          { content: "authz_matrix flagged vertical escalation candidate", kind: "inferred", confidence: 0.8 },
        ],
        contradiction_count: 1,
      },
      status: "pending",
      created_at: nowIso(),
      // 60s 后超时 —— 用来现场验证"超时=拒绝"路径。
      deadline_at: new Date(Date.now() + 60_000).toISOString(),
    },
    {
      id: "appr_03",
      project_id: "proj_globex",
      project_name: "Globex Bounty",
      task_id: "task_globex_recon",
      kind: "command",
      payload: {
        command: "nuclei -l targets.txt -severity critical -rl 60",
        reason: "active scanning outside recon window",
      },
      slice: { subject: "globex.io", purpose: "critical-template sweep", facts: [], contradiction_count: 0 },
      status: "pending",
      created_at: nowIso(),
      deadline_at: new Date(Date.now() + 8 * 60_000).toISOString(),
    },
    {
      id: "appr_04",
      project_id: "proj_acme",
      project_name: "Acme Corp",
      kind: "command",
      payload: { command: "nmap -sV -p 443 api.acme.com", reason: "service detection" },
      status: "approved",
      created_at: nowIso(),
      deadline_at: nowIso(),
      decided_at: nowIso(),
      decided_by: "operator:admin",
    },
  ],
  // 集成模块（契约 §integration-*）：MCP 服务器 / 用户技能 / Agent 档案。
  mcpServers: [
    {
      id: "mcp_01",
      name: "recon",
      transport: "stdio",
      command: "npx",
      args: ["-y", "@example/recon-mcp"],
      env: { API_TOKEN: "****d1234" },
      url: "",
      headers: {},
      enabled: true,
      require_approval: true,
      timeout_seconds: 120,
      tool_count: 6,
      last_healthy_at: nowIso(),
      last_error: "",
      created_by: "operator:admin",
      created_at: nowIso(),
    },
    {
      id: "mcp_02",
      name: "ticketing",
      transport: "http",
      url: "https://mcp.corp.example.com/sse",
      env: {},
      headers: { Authorization: "****9f876" },
      enabled: false,
      require_approval: true,
      timeout_seconds: 120,
      tool_count: 0,
      last_healthy_at: undefined,
      last_error: "connect: x509: certificate signed by unknown authority",
      created_by: "operator:admin",
      created_at: nowIso(),
    },
  ],
  skills: [
    {
      id: "skill_01",
      name: "testing-payment-price-tampering",
      category: "user",
      description: "Prove a server accepts a manipulated price (checkout/coupon/transfer).",
      tags: ["payment", "business-logic"],
      intent: "offensive",
      body: "# Testing Payment Price Tampering\n\n1. Replay the checkout request with a negative quantity.",
      enabled: true,
      created_by: "operator:admin",
      created_at: nowIso(),
    },
    {
      id: "skill_02",
      name: "hardening-csp-rollout",
      category: "user",
      description: "Roll out a strict CSP without breaking the legacy bundle.",
      tags: ["csp", "hardening"],
      intent: "defensive",
      body: "# CSP rollout\n\n- Start with report-only.",
      enabled: false,
      created_by: "operator:admin",
      created_at: nowIso(),
    },
  ],
  agentProfiles: [
    { id: "ap_01", role: "api-fuzzing", focus: "REST/GraphQL surface fuzzing", vuln_classes: ["api-auth", "mass-assignment"], evidence_contract: "a proven differential or a rejected hypothesis with its control", stopping_rule: "stop when the assigned endpoint × class matrix is exhausted", tool_scope_hidden: ["browser_action"], enabled: true, builtin: false, created_by: "operator:admin", created_at: nowIso() },
  ],
  invocations: [
    { id: "inv_01", module: "mcp", ts: nowIso(), instance_id: "inst_01", scan_id: "sess_01", agent_id: "", name: "mcp_recon_lookup", server: "recon", status: "ok", args: '{"query":"acme.com"}', outcome: '{"subdomains":["api.acme.com"]}' },
    { id: "inv_02", module: "mcp", ts: nowIso(), instance_id: "inst_01", scan_id: "sess_01", agent_id: "", name: "mcp_ticketing_create_ticket", server: "ticketing", status: "error", args: '{"title":"x"}', error: "server not connected" },
    { id: "inv_03", module: "skills", ts: nowIso(), instance_id: "inst_01", scan_id: "sess_01", agent_id: "", name: "testing-payment-price-tampering", status: "ok", outcome: "loaded (model-requested)" },
    { id: "inv_04", module: "skills", ts: nowIso(), instance_id: "inst_01", scan_id: "sess_01", agent_id: "", name: "no-such-skill", status: "error", error: "skill not found: no-such-skill" },
    { id: "inv_05", module: "agents", ts: nowIso(), instance_id: "inst_01", scan_id: "sess_01", agent_id: "sub_1_1", name: "api-fuzzing", status: "ok", args: '{"task":"own the api-auth lane"}', outcome: "spawned" },
    { id: "inv_06", module: "agents", ts: nowIso(), instance_id: "inst_01", scan_id: "sess_01", agent_id: "sub_1_1", name: "delegate_result", status: "ok", outcome: "completed: 2 findings" },
  ],
  // 审批豁免规则（approve & remember，契约 §3.4）。
  approvalRules: [
    {
      id: "arule_01",
      project_id: "proj_acme",
      project_name: "Acme Corp",
      kind: "command",
      match: { program: "curl", contains: "api.acme.com" },
      reason: "curl write flags approved once against the acme api",
      created_by: "operator:admin",
      created_from_approval_id: "appr_04",
      created_at: nowIso(),
      expires_at: new Date(Date.now() + 30 * 86_400_000).toISOString(),
      hit_count: 12,
      last_hit_at: nowIso(),
    },
    {
      id: "arule_02",
      project_id: "proj_acme",
      project_name: "Acme Corp",
      kind: "http",
      match: { method: "POST", host: "api.acme.com", path_prefix: "api/auth/login" },
      reason: "auth-probe lane remembered from the login fuzzing approval",
      created_by: "operator:admin",
      created_at: nowIso(),
      hit_count: 4,
      last_hit_at: nowIso(),
    },
  ],
};

const providerCatalog = [
  { id: "openai", displayName: "OpenAI", baseURL: "https://api.openai.com/v1", headerStyle: "openai", authMethods: ["api_key"], models: ["gpt-5.6", "gpt-5.6-terra", "gpt-5.6-luna"] },
  { id: "google", displayName: "Google Gemini", baseURL: "https://generativelanguage.googleapis.com/v1beta", headerStyle: "gemini", authMethods: ["api_key"], models: ["gemini-3.5-flash", "gemini-3.6-flash"] },
];

const llmSettings = {
  model: "",
  apiBase: "",
  apiKey: "",
  hasApiKey: false,
  reasoningEffort: "high",
  ollamaCompatible: false,
  llmMaxRetries: 3,
  memoryCompressorTimeout: 120,
  maxIterations: 40,
  geminiApiKey: "",
  hasGeminiApiKey: false,
  envFile: "~/.xalgorix.env",
  provider: "",
  authMethod: "api_key",
  profiles: [],
};

// Flatten every seeded instance vuln into the FlatFinding shape the
// /findings page and Overview totals consume, so the list, the summary
// widget and the per-scan counts all agree.
function allFindings() {
  const out = [];
  for (const inst of state.instances) {
    for (const v of inst.vulns || []) {
      out.push({
        ...v,
        scan_id: inst.id,
        scan_target: inst.parent_target || inst.targets,
        scan_started_at: inst.started_at,
        verified: v.severity === "critical" || v.severity === "high",
        tags: [],
      });
    }
  }
  return out;
}

function findingsSummary() {
  const totals = { critical: 0, high: 0, medium: 0, low: 0, info: 0 };
  for (const f of allFindings()) {
    const sev = (f.severity || "").toLowerCase();
    if (sev in totals) totals[sev] += 1;
  }
  return {
    totals,
    as_of: nowIso(),
    etag: `mock-${totals.critical}${totals.high}${totals.medium}${totals.low}${totals.info}`,
  };
}

function runningInstance() {
  return state.instances.find((i) => i.status === "running");
}

function buildScanFromInstance(inst) {
  return {
    id: inst.id,
    name: inst.name,
    target: inst.targets,
    parent_target: inst.parent_target,
    started_at: inst.started_at,
    finished_at: inst.finished_at,
    status: inst.status,
    scan_mode: inst.scan_mode,
    instruction: inst.instruction,
    severity_filter: inst.severity_filter,
    phases: inst.phases,
    current_phase: inst.current_phase,
    company_name: inst.company_name,
    vuln_count: inst.vuln_count,
    iterations: inst.iterations,
    tool_calls: inst.tool_calls,
    total_tokens: inst.total_tokens,
  };
}

async function readBody(req) {
  return new Promise((resolve) => {
    let buf = "";
    req.on("data", (c) => (buf += c));
    req.on("end", () => {
      try {
        resolve(buf ? JSON.parse(buf) : {});
      } catch {
        resolve({});
      }
    });
  });
}

// ---------------------------------------------------------------------------
// SSE 事件流（/api/stream，替代原 /ws WebSocket）。
// ---------------------------------------------------------------------------
// 连接注册表：POST 产生的服务端事件（guidance_injected 等）向全部客户端广播，
// 与契约 §3.1 的事件流约定一致；?instance= 过滤语义与 internal/web/sse_hub.go
// 对齐（带过滤的客户端只收该实例的事件帧）。
const sseClients = new Set();
let sseSeq = 0;

function broadcastEvent(evt) {
  const seq = ++sseSeq;
  const frame = `id: ${seq}\nevent: ${evt.type}\ndata: ${JSON.stringify(evt)}\n\n`;
  for (const client of sseClients) {
    if (client.filter && evt.instance_id && client.filter !== evt.instance_id) {
      continue;
    }
    try {
      client.res.write(frame);
    } catch {
      sseClients.delete(client);
    }
  }
}

// 保活注释帧：代理/浏览器空闲超时的最低要求（后端 30s，mock 用 15s）。
setInterval(() => {
  for (const client of sseClients) {
    try {
      client.res.write(": ping\n\n");
    } catch {
      sseClients.delete(client);
    }
  }
}, 15000);

// -------- 会话投影 helper（chat_transcript.go 同语义）----------------------

/** 实例事件 fixture（确定性合成，/api/instances/:id/events 与转录共用）。
 *  演示文案全部中文；message 事件 = LLM 每轮结论（转录渲染为 assistant 气泡）。 */
function mockInstanceEvents(inst) {
  const kinds = [
    { type: "phase", content: "进入阶段：信息侦察", agent_id: inst.id },
    { type: "thought", content: "目标面较宽，优先摸清 api 主机与认证入口", agent_id: inst.id },
    {
      type: "tool_call",
      tool_name: "build_plan",
      content: "build_plan",
      tool_args: {
        tasks: JSON.stringify([
          { id: "recon", title: "侦察 + 端点清点", phase: 1, depends_on: [] },
          { id: "test-idor", title: "测试 /api/v1/orders 越权（IDOR）", phase: 8, vuln_class: "idor", depends_on: ["recon"] },
          { id: "test-sqli", title: "测试 /search SQL 注入", phase: 6, vuln_class: "sqli", depends_on: ["recon"] },
        ]),
      },
      agent_id: inst.id,
    },
    {
      type: "tool_call",
      tool_name: "update_plan",
      content: "update_plan recon",
      tool_args: { task_id: "recon", status: "completed", notes: "端点清点完成" },
      agent_id: inst.id,
    },
    {
      type: "tool_call",
      tool_name: "update_plan",
      content: "update_plan test-idor",
      tool_args: { task_id: "test-idor", status: "active" },
      agent_id: inst.id,
    },
    { type: "tool_call", tool_name: "nuclei", content: `nuclei -rl 60 -severity low,medium ${inst.targets}`, agent_id: inst.id },
    { type: "tool_result", content: "被动集 42 个模板跑完，无命中", agent_id: inst.id },
    {
      type: "message",
      content: "本轮侦察结论：\n\n- 目标边界确认：`acme.com` 与 `api.acme.com`，均由 Cloudflare 代理\n- 被动漏扫无命中，攻击面集中在 API\n- 下一轮计划：优先验证 `/api/v1/orders` 的越权（IDOR），其次 `/search` 的注入",
      agent_id: inst.id,
    },
    { type: "tool_call", tool_name: "httpx", content: `httpx -silent -status-code ${inst.targets}`, agent_id: `${inst.id}/spec-injection` },
    { type: "tool_result", content: "3 个端点返回 200", agent_id: `${inst.id}/spec-injection` },
    { type: "thought", content: "/admin 返回 401，存在横向越权测试价值，登记 authz 矩阵", agent_id: `${inst.id}/spec-injection` },
    { type: "decision", content: "认领假设：/api/v1/orders 存在 IDOR", agent_id: `${inst.id}/spec-injection` },
    { type: "tool_call", tool_name: "nmap", content: `nmap -sV -p 443 ${inst.targets}`, agent_id: inst.id },
    { type: "tool_result", content: "1 个服务：nginx 1.24.0", agent_id: inst.id },
    { type: "http", content: "GET /api/v1/users/42 => 200（角色 A）", agent_id: `${inst.id}/spec-injection` },
    { type: "http", content: "GET /api/v1/users/42（匿名）=> 401", agent_id: `${inst.id}/spec-injection` },
    {
      type: "message",
      content: "Worker 结论（spec-injection）：\n\n- 同一接口，角色 A 可见 `users/42`，匿名 401——存在对象级访问控制差异\n- 已认领 IDOR 假设，下一步用两个低权账号交叉验证",
      agent_id: `${inst.id}/spec-injection`,
    },
    { type: "phase", content: "进入阶段：漏洞测试", agent_id: inst.id },
    { type: "tool_call", tool_name: "sqlmap", content: `sqlmap -u 'https://${inst.targets}/search?q=1' --batch --level=2`, agent_id: `${inst.id}/spec-injection` },
    { type: "vuln", content: "/search 存在 SQL 注入（critical，CVSS 9.8）", agent_id: `${inst.id}/spec-injection` },
    { type: "tool_result", content: "验证器独立复现：时间盲注 payload 确认", agent_id: inst.id },
    {
      type: "message",
      content: "**本轮结论：/search 的 `q` 参数确认 SQL 注入（时间盲注），CVSS 9.8，已由验证器独立复现。**\n\n建议：该参数直接拼接进查询语句，需参数化修复；同版本代码路径建议全局排查。",
      agent_id: inst.id,
    },
  ];
  const base = Date.now() - kinds.length * 60_000;
  return kinds.map((k, idx) => ({
    ...k,
    instance_id: inst.id,
    timestamp: new Date(base + idx * 60_000).toISOString(),
  }));
}

/** 侧栏行：run + 会话元数据（标题/置顶/归档）+ 任务名。 */
function conversationRows(includeArchived) {
  const taskNames = new Map(state.tasks.map((t) => [t.id, t.name]));
  return state.runs
    .filter((r) => r.instance_id)
    .filter((r) => includeArchived || !state.convMeta.get(r.instance_id)?.archived)
    .map((r) => {
      const meta = state.convMeta.get(r.instance_id) ?? {};
      const taskName = taskNames.get(r.task_id) || "";
      return {
        ...r,
        title: meta.title || `${taskName || "任务"} · ${String(r.started_at ?? "").slice(5, 16).replace("T", " ")}`,
        pinned: meta.pinned ?? false,
        archived: meta.archived ?? false,
        task_name: taskName,
      };
    });
}

/** 转录投影：guidance → human + 事件 fixture → assistant/tool/finding/system。 */
function buildMockTranscript(instanceId, run) {
  const inst = state.instances.find((i) => i.id === instanceId);
  if (!inst) return [];
  const taskNames = new Map(state.tasks.map((t) => [t.id, t.name]));
  const rows = [];

  // 人工 turn（服务端先持久化再广播；guidance 行带 run/instance 归属）。
  for (const g of state.guidances) {
    if (g.instance_id !== instanceId && (!run || g.run_id !== run.id)) continue;
    if (!g.message?.trim()) continue;
    rows.push({ kind: "human", role: "human", text: g.message, status: g.status, at: g.created_at });
  }

  // 事件 fixture（与 /api/instances/:id/events 同源，时间轴对齐）。
  const events = mockInstanceEvents(inst);
  let lastTool = "";
  for (const ev of events) {
    const at = ev.timestamp;
    if (ev.type === "tool_call") {
      lastTool = ev.tool_name || "";
      rows.push({ kind: "tool_use", tool: lastTool, args: ev.tool_args, text: ev.content, agent_id: ev.agent_id, at });
    } else if (ev.type === "tool_result") {
      rows.push({ kind: "tool_result", tool: ev.tool_name || lastTool, output: ev.content, agent_id: ev.agent_id, at });
    } else if (ev.type === "vuln") {
      rows.push({ kind: "finding", text: ev.content, at });
    } else if (ev.type === "message") {
      // LLM 每轮结论（chat_transcript.go 同款映射；mock 无工具回显消息）。
      rows.push({ kind: "assistant", role: "agent", text: ev.content, agent_id: ev.agent_id, at });
    }
  }

  // 审批投影（requested / decided）。
  for (const a of state.approvals) {
    if (a.instance_id !== instanceId) continue;
    const summary = a.payload.command || `${a.payload.method ?? ""} ${a.payload.url ?? ""}`.trim();
    rows.push({
      kind: "approval",
      text: `审批请求：${a.kind} · ${summary}`,
      status: a.status,
      at: a.created_at,
      approval: {
        kind: a.kind,
        status: a.status,
        decided_by: a.decided_by,
        reason: a.reason,
        decided: a.status !== "pending",
      },
    });
  }

  // 终态系统行。
  if (inst.finished_at) {
    rows.push({ kind: "system", text: inst.status === "finished" ? "finished" : `aborted: ${inst.status}`, at: inst.finished_at });
  }

  rows.sort((a, b) => (Date.parse(a.at) || 0) - (Date.parse(b.at) || 0));
  return rows.map((row, idx) => ({ seq: idx + 1, ...row, task_name: taskNames.get(run?.task_id) }));
}

const server = http.createServer(async (req, res) => {
  const method = req.method || "GET";
  const url = req.url.split("?")[0];
  state.reqCounter += 1;

  // CORS preflight (the Vite dev server proxies same-origin so this is rarely
  // hit, but harmless to support).
  if (method === "OPTIONS") {
    res.writeHead(204, {
      "access-control-allow-origin": "*",
      "access-control-allow-methods": "GET,POST,PATCH,DELETE,OPTIONS",
      "access-control-allow-headers": "content-type",
    });
    return res.end();
  }

  // -------- Auth (auth disabled in the mock so login is bypassed) ----------
  // The mock signs nobody in: it answers as an open deployment that has
  // already completed setup, so the SPA takes the auth-disabled fast path.
  if (method === "GET" && url === "/api/auth/status") {
    return send(res, {
      auth_enabled: false,
      authenticated: true,
      needs_setup: false,
      user: null,
    });
  }
  if (method === "POST" && url === "/api/auth/login") {
    await readBody(req);
    return send(res, { status: "ok" });
  }
  if (method === "POST" && url === "/api/auth/setup") {
    await readBody(req);
    return send(res, { status: "ok" });
  }
  if (method === "POST" && url === "/api/auth/password") {
    await readBody(req);
    return send(res, { status: "ok" });
  }
  if (method === "POST" && url === "/api/auth/logout") {
    return send(res, { status: "logged_out" });
  }

  // -------- RBAC account management (admin-only; mock returns a fixed set) --
  if (method === "GET" && url === "/api/users") {
    return send(res, [
      {
        id: "u_mockadmin0000000",
        username: "admin",
        role: "admin",
        disabled: false,
        created_at: new Date(Date.now() - 86400_000).toISOString(),
        updated_at: new Date().toISOString(),
      },
      {
        id: "u_mockviewer000000",
        username: "audit",
        role: "viewer",
        disabled: false,
        created_at: new Date(Date.now() - 3600_000).toISOString(),
        updated_at: new Date().toISOString(),
      },
    ]);
  }
  if (method === "POST" && url === "/api/users") {
    const body = await readBody(req);
    return send(res, {
      id: "u_mock" + Math.random().toString(16).slice(2, 10),
      username: body.username || "new-user",
      role: body.role || "viewer",
      disabled: false,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    }, 201);
  }
  if (method === "PATCH" && url.startsWith("/api/users/")) {
    const body = await readBody(req);
    return send(res, {
      id: url.split("/")[3],
      username: "admin",
      role: body.role || "admin",
      disabled: body.disabled ?? false,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    });
  }
  if (method === "DELETE" && url.startsWith("/api/users/")) {
    return send(res, { status: "deleted" });
  }

  // -------- SSE event stream（/api/stream，替代原 /ws）----------------------
  if (method === "GET" && url === "/api/stream") {
    res.writeHead(200, {
      "content-type": "text/event-stream; charset=utf-8",
      "cache-control": "no-cache",
      connection: "keep-alive",
    });
    const filter =
      new URLSearchParams(req.url.split("?")[1] || "").get("instance") || "";
    const client = { res, filter };
    sseClients.add(client);
    req.on("close", () => sseClients.delete(client));
    return; // 连接保持打开，合成事件由 broadcastEvent 定时推流
  }

  // -------- Version / status ------------------------------------------------
  if (method === "GET" && url === "/api/version") {
    return send(res, { version: "4.2.9", commit: "dev" });
  }
  if (method === "GET" && url === "/api/status") {
    const run = runningInstance();
    return send(res, {
      running: !!run,
      scan_id: run ? `scan_${run.id.slice(-2)}` : "",
      instance_id: run?.id ?? "",
      current_phase: run?.current_phase ?? 0,
      vulns: run?.vuln_count ?? 0,
      running_instances: state.instances.filter((i) => i.status === "running")
        .length,
    });
  }

  // -------- Instances -------------------------------------------------------
  if (method === "GET" && url === "/api/instances") {
    const qs = new URLSearchParams(req.url.split("?")[1] || "");
    const projectId = qs.get("project_id");
    const list = projectId
      ? state.instances.filter((i) => i.project_id === projectId)
      : state.instances;
    return send(res, { instances: list, total: list.length, resources: state.resources });
  }
  const instOne = url.match(/^\/api\/instances\/([^/]+)$/);
  if (instOne && method === "GET") {
    const inst = state.instances.find((i) => i.id === instOne[1]);
    return inst ? send(res, inst) : send(res, { error: "not found" }, 404);
  }
  // 实例状态翻转辅助：同步 instance / run 投影 / 任务 last_run_status，
  // 并广播 instance_updated（WS→查询桥依赖；真实后端 server.go 同语义）。
  const setInstanceStatus = (instanceId, status) => {
    const inst = state.instances.find((i) => i.id === instanceId);
    if (inst) inst.status = status;
    for (const r of state.runs) {
      if (r.instance_id === instanceId) r.status = status;
    }
    for (const tk of state.tasks) {
      if (state.runs.some((r) => r.task_id === tk.id && r.instance_id === instanceId)) {
        tk.last_run_status = status;
      }
    }
    const wire = {
      type: "instance_updated",
      content: instanceId,
      instance_id: instanceId,
      timestamp: nowIso(),
    };
    broadcastEvent(wire);
  };
  const instStop = url.match(/^\/api\/instances\/([^/]+)\/stop$/);
  if (instStop && method === "POST") {
    const inst = state.instances.find((i) => i.id === instStop[1]);
    if (!inst) return send(res, { error: "not found" }, 404);
    const body = await readBody(req);
    inst.status = "stopped";
    inst.finished_at = nowIso();
    // 软取消语义：reason 供引擎下一 Run briefing 参考。
    inst.stop_reason = body.reason ? `manual: ${body.reason}` : "manual";
    // run/任务投影同步翻转为终态（真实后端：stopped；一次性守卫随之解除）。
    setInstanceStatus(inst.id, "stopped");
    return ok(res, { status: "stopped" });
  }
  const instRestart = url.match(/^\/api\/instances\/([^/]+)\/restart$/);
  if (instRestart && method === "POST") {
    const inst = state.instances.find((i) => i.id === instRestart[1]);
    if (!inst) return send(res, { error: "not found" }, 404);
    inst.status = "running";
    inst.finished_at = undefined;
    inst.stop_reason = undefined;
    return ok(res, { status: "restarted" });
  }
  const instStart = url.match(/^\/api\/instances\/([^/]+)\/start$/);
  if (instStart && method === "POST") {
    const inst = state.instances.find((i) => i.id === instStart[1]);
    if (!inst) return send(res, { error: "not found" }, 404);
    inst.status = "running";
    inst.started_at = nowIso();
    return ok(res, { status: "started" });
  }
  // 暂停/恢复（真实后端 server.go 语义：仅 running 可暂停 / 仅 paused 可恢复，
  // run 投影同步翻转并广播 instance_updated——WS→查询桥的测试依赖）。
  const instPause = url.match(/^\/api\/instances\/([^/]+)\/pause$/);
  if (instPause && method === "POST") {
    const inst = state.instances.find((i) => i.id === instPause[1]);
    if (!inst) return send(res, { error: "not found" }, 404);
    if (inst.status !== "running") {
      return send(res, { error: "only a running instance can be paused" }, 409);
    }
    inst.stop_reason = "user_paused";
    setInstanceStatus(inst.id, "paused");
    return ok(res, { status: "paused" });
  }
  const instResume = url.match(/^\/api\/instances\/([^/]+)\/resume$/);
  if (instResume && method === "POST") {
    const inst = state.instances.find((i) => i.id === instResume[1]);
    if (!inst) return send(res, { error: "not found" }, 404);
    if (inst.status !== "paused") {
      return send(res, { error: "only a paused instance can be resumed" }, 409);
    }
    inst.stop_reason = undefined;
    setInstanceStatus(inst.id, "running");
    return ok(res, { status: "resumed" });
  }

  // -------- Scans (records) -------------------------------------------------
  if (method === "GET" && url === "/api/scans") {
    // listScansPage (paginated table) expects { items, total }; bare-list
    // consumers (useScansList → overview/reports) expect the plain array.
    const qs = new URLSearchParams(req.url.split("?")[1] || "");
    // project_id isolation（与真实后端 handleListScans §3.2 对齐）：
    // scan.id == instance.id，经 run.instance_id → run.task_id → task.project_id 联查。
    const projectFilter = qs.get("project_id");
    const scoped = projectFilter
      ? state.scans.filter((scan) => {
          const run = state.runs.find((r) => r.instance_id === scan.id);
          if (!run) return false;
          const task = state.tasks.find((tk) => tk.id === run.task_id);
          return task?.project_id === projectFilter;
        })
      : state.scans;
    if (qs.get("page") != null) {
      const size = Math.max(1, Number(qs.get("size") ?? 50) || 50);
      const pg = Math.max(1, Number(qs.get("page")) || 1);
      return send(res, {
        items: scoped.slice((pg - 1) * size, pg * size),
        total: scoped.length,
      });
    }
    return send(res, scoped);
  }
  const scanOne = url.match(/^\/api\/scans\/([^/]+)$/);
  if (scanOne) {
    const id = scanOne[1];
    if (method === "DELETE") {
      const before = state.scans.length + state.instances.length;
      state.scans = state.scans.filter((s) => s.id !== id);
      state.instances = state.instances.filter((i) => i.id !== id);
      if (state.scans.length + state.instances.length === before) {
        return send(res, { error: "not found" }, 404);
      }
      return ok(res, { status: "deleted" });
    }
    if (method === "GET") {
      // Backend resolves /api/scans/{id} against both scan record ids
      // (scan_01) and live instance ids (inst_01). Mirror that.
      let scan = state.scans.find((s) => s.id === id);
      let inst = state.instances.find((i) => i.id === id);
      if (!scan && inst) scan = buildScanFromInstance(inst);
      if (!scan) return send(res, { error: "not found" }, 404);
      // Merge phases/vulns from the matching instance when available so the
      // scan-detail "Findings" tab is populated.
      if (!inst) inst = state.instances.find((i) => i.name === scan.name);
      // Run/task linkage hints (mirrors the backend's attachRunLinkage): the
      // run-page guidance composer needs task_id for the unified HITL channel.
      const mount =
        state.runs.find((r) => r.instance_id && r.instance_id === (inst?.id ?? scan.instance_id)) ??
        state.runs.find((r) => r.legacy_scan_id === scan.id);
      const linkage = mount ? { run_id: mount.id, task_id: mount.task_id } : {};
      const vulns =
        inst?.vulns ??
        (id === "scan_01"
          ? [
              { id: "v1", title: "/search 存在 SQL 注入", severity: "critical", endpoint: "https://acme.com/search?q=", cvss: 9.8, cve: "CVE-2024-1234" },
              { id: "v2", title: "/login 存在反射型 XSS", severity: "high", endpoint: "https://acme.com/login", cvss: 7.5 },
              { id: "v3", title: "/go 存在开放重定向", severity: "medium", endpoint: "https://acme.com/go", cvss: 4.3 },
              { id: "v4", title: "Verbose error in /api/v1/users", severity: "low", endpoint: "https://acme.com/api/v1/users", cvss: 2.0 },
              { id: "v5", title: "Missing security headers", severity: "info", endpoint: "https://acme.com/", cvss: 0 },
            ]
          : [
              { id: "v6", title: "S3 bucket misconfiguration", severity: "high", endpoint: "https://cdn.globex.io", cvss: 7.1 },
              { id: "v7", title: "Outdated jQuery 1.4.2", severity: "low", endpoint: "https://globex.io/static/jquery.js", cvss: 3.1 },
            ]);
      return send(res, { ...scan, ...linkage, events: [], vulns });
    }
  }

  // -------- Start a new scan -----------------------------------------------
  if (method === "POST" && (url === "/api/scan" || url === "/api/start")) {
    const body = await readBody(req);
    const targetsArr = Array.isArray(body.targets) ? body.targets : [];
    const primary = targetsArr[0] || "demo.local";
    const id = `inst_${String(state.instances.length + 1).padStart(2, "0")}`;
    const saveOnly = body.save_only === true;
    const newInst = {
      id,
      name: body.name || `Scan ${id}`,
      project_id: body.project_id || state.projects[0]?.id,
      targets: targetsArr.join(",") || primary,
      parent_target: primary,
      status: saveOnly ? "saved" : "running",
      started_at: nowIso(),
      iterations: 0,
      tool_calls: 0,
      vuln_count: 0,
      total_tokens: 0,
      scan_mode: body.scan_mode || "single",
      instruction: body.instruction || "",
      severity_filter: body.severity_filter || [],
      phases: body.phases || [1, 2, 3, 4],
      company_name: body.company_name || "",
      current_phase: 1,
      vulns: [],
    };
    state.instances.unshift(newInst);
    state.scans.unshift({
      ...buildScanFromInstance(newInst),
      id: `scan_${String(state.scans.length + 1).padStart(2, "0")}`,
    });
    return ok(res, { status: saveOnly ? "saved" : "started", instance_id: id, id });
  }
  if (method === "POST" && url === "/api/stop") {
    for (const i of state.instances) {
      if (i.status === "running") {
        i.status = "finished";
        i.finished_at = nowIso();
        i.stop_reason = "stop_all";
      }
    }
    return ok(res, { status: "stopped" });
  }

  // -------- Queue -----------------------------------------------------------
  if (method === "GET" && url === "/api/queue/status") {
    return send(res, state.queue);
  }
  if (method === "POST" && url === "/api/queue/resume") {
    if (!state.queue.available) {
      return send(res, { status: "noop", error: "queue is empty" });
    }
    state.queue.current_idx = (state.queue.current_idx ?? 0) + 1;
    state.queue.remaining = Math.max(0, (state.queue.remaining ?? 0) - 1);
    return ok(res, {
      status: "resumed",
      from_index: state.queue.current_idx,
      targets_left: state.queue.remaining,
    });
  }
  if (method === "POST" && url === "/api/queue/clear") {
    state.queue = { available: false };
    return ok(res, { status: "cleared" });
  }

  // -------- Settings --------------------------------------------------------
  if (method === "GET" && url === "/api/settings/rate-limit") {
    return send(res, state.rateLimit);
  }
  if (method === "POST" && url === "/api/settings/rate-limit") {
    const body = await readBody(req);
    state.rateLimit = {
      requests: Number(body.requests) || state.rateLimit.requests,
      window: Number(body.window) || state.rateLimit.window,
    };
    return send(res, state.rateLimit);
  }

  // -------- Report download (anchor href in Reports page) -------------------
  const reportMatch = url.match(/^\/api\/report\/([^/]+)$/);
  if (reportMatch && method === "GET") {
    const id = reportMatch[1];
    const html = `<!doctype html><html><body style="font-family:sans-serif">
<h1>Xalgorix scan report</h1>
<p>Report for scan <code>${id}</code> (mock build).</p>
</body></html>`;
    return send(res, html, 200, { "content-type": "text/html; charset=utf-8" });
  }

  // -------- Findings list + on-disk summary --------------------------------
  if (method === "GET" && url === "/api/findings") {
    const qs = new URLSearchParams(req.url.split("?")[1] || "");
    const projectId = qs.get("project_id");
    let out = allFindings();
    if (projectId) {
      const ids = new Set(
        state.instances.filter((i) => i.project_id === projectId).map((i) => i.id),
      );
      out = out.filter((f) => ids.has(f.scan_id));
    }
    return send(res, out);
  }
  if (method === "GET" && url === "/api/findings/summary") {
    const qs = new URLSearchParams(req.url.split("?")[1] || "");
    const projectId = qs.get("project_id");
    if (!projectId) return send(res, findingsSummary());
    const ids = new Set(
      state.instances.filter((i) => i.project_id === projectId).map((i) => i.id),
    );
    const totals = { critical: 0, high: 0, medium: 0, low: 0, info: 0 };
    for (const f of allFindings()) {
      if (!ids.has(f.scan_id)) continue;
      const sev = (f.severity || "").toLowerCase();
      if (sev in totals) totals[sev] += 1;
    }
    return send(res, {
      totals,
      as_of: nowIso(),
      etag: `mock-${projectId}-${totals.critical}${totals.high}${totals.medium}${totals.low}${totals.info}`,
    });
  }

  // -------- Per-instance persisted event history ---------------------------
  // 合成历史（确定性生成，按 offset/limit 分页）：让"加载更早"回翻与
  // 会话视角（coordinator/specialist）有真实数据可拼。
  const instEventsMatch = url.match(/^\/api\/instances\/([^/]+)\/events$/);
  if (instEventsMatch && method === "GET") {
    const inst = state.instances.find((i) => i.id === instEventsMatch[1]);
    if (!inst) return send(res, []);
    const qs = new URLSearchParams(req.url.split("?")[1] || "");
    const offset = Number(qs.get("offset") || 0);
    const limit = Math.min(Number(qs.get("limit") || 100), 300);
    const events = mockInstanceEvents(inst).slice(offset, offset + limit);
    return send(res, events);
  }

  // -------- R5: Info space (project-scoped, read-only) ----------------------
  if (method === "GET" && url.startsWith("/api/info/")) {
    const qs = new URLSearchParams(req.url.split("?")[1] || "");
    const projectId = qs.get("project_id") || "";
    if (projectId === "proj-disabled" || process.env.MOCK_INFO_DISABLED === "1") {
      return send(res, { available: false, reason: "cold_path_disabled", error: "Info space requires PostgreSQL cold path (no DSN configured)" }, 503);
    }
    const runId = qs.get("scan_id") || state.instances[0]?.id || "run_mock_1";
    const now = Date.now();
    const item = (n, nature, subject, purpose, text, supports = []) => ({
      id: `info_${runId}_${n}`,
      scan_id: runId,
      source_seq: n,
      nature,
      coordinates: {
        content: { text, media_type: "text/plain" },
        source: { kind: "tool", name: n % 2 ? "httpx" : "nuclei", agent_id: runId },
        observed_at: new Date(now - n * 60000).toISOString(),
        subject: { kind: "host", identifier: subject },
        purpose,
        context: { phase: n < 3 ? "recon" : "vulnerability-testing", task: "task_mock" },
        relation_ids: n > 1 ? [`info_${runId}_${n - 1}`] : [],
      },
      supports,
      confidence: nature === "observed" ? 1 : 0.72,
      coordinate_status: "complete",
      created_at: new Date(now - n * 60000).toISOString(),
    });
    const items = [
      item(1, "observed", "api.example.com", "endpoint inventory", "GET /api/v1/orders returned 200 with an authenticated session.", []),
      item(2, "observed", "api.example.com", "auth boundary", "Anonymous request to /api/v1/orders returned 401.", []),
      item(3, "inferred", "api.example.com", "authorization", "IDOR candidate: /api/v1/orders differs by user scope.", [`info_${runId}_1`]),
      item(4, "observed", "admin.example.com", "surface mapping", "Admin console responds with a login form.", []),
      item(5, "inferred", "admin.example.com", "weak isolation", "Admin console may share the same session cookie.", [`info_${runId}_4`]),
      item(6, "observed", "api.example.com", "traffic", "TLS certificate CN=api.example.com, issued by mock-ca.", []),
    ];
    if (url.endsWith("/region")) {
      const nature = qs.get("nature");
      const subject = qs.get("subject");
      const filtered = items.filter((it) => (!nature || it.nature === nature) && (!subject || it.coordinates.subject.identifier.includes(subject)));
      return send(res, { available: true, items: filtered, truncated: false, incomplete: false });
    }
    if (url.endsWith("/context")) {
      return send(res, { available: true, items: items.slice(0, 4), truncated: false, incomplete: false });
    }
    if (url.endsWith("/association")) {
      const hits = items.slice(1, 4).map((it, idx) => ({ item: it, path: [`info_${runId}_1`, it.id], depth: (idx % 2) + 1 }));
      return send(res, { available: true, hits, truncated: false, incomplete: true, gap_reason: "mock watermark pending" });
    }
    return send(res, { available: false, reason: "unknown_info_endpoint" }, 404);
  }

  // -------- R5: Cold-path sync state (contract §3.3 reuse) ------------------
  // disabled = no-DSN normal state (not a failure); failed 才是红色。
  if (method === "GET" && url.startsWith("/api/scans/") && url.endsWith("/coldpath")) {
    const scanId = decodeURIComponent(url.slice("/api/scans/".length, -"/coldpath".length));
    if (!scanId) return send(res, { error: "scan id is required" }, 400);
    const known =
      state.instances.some((inst) => inst.id === scanId) ||
      state.scans.some((sc) => sc.id === scanId);
    if (!known) return send(res, { error: "scan not found" }, 404);
    if (process.env.MOCK_INFO_DISABLED === "1") {
      return send(res, { scan_id: scanId, cold_path: "disabled", source_seq: 0, applied_seq: 0, state: "disabled" });
    }
    const nowIso2 = new Date().toISOString();
    return send(res, {
      scan_id: scanId,
      cold_path: "enabled",
      source_seq: 24,
      applied_seq: 24,
      state: "applied",
      updated_at: nowIso2,
    });
  }

  // -------- Run files browser (read-only artifact/workspace view) ----------
  // Mirrors scan_files.go: single-level listing, bounded text read, binary
  // flagged without content. preview/archive are native <a> routes in real
  // deployment — the mock serves download inline and 501s the archive.
  const filesMatch = url.match(/^\/api\/scans\/([^/]+)\/files(\/[a-z]+)?$/);
  if (filesMatch && method === "GET") {
    const scanId = decodeURIComponent(filesMatch[1]);
    const action = filesMatch[2] || "";
    const known =
      state.instances.some((inst) => inst.id === scanId) ||
      state.scans.some((sc) => sc.id === scanId);
    if (!known) return send(res, { error: "not found" }, 404);
    const qs = new URLSearchParams(req.url.split("?")[1] || "");
    const rel = qs.get("path") || ".";
    if (action === "") {
      const entries = MOCK_SCAN_TREE[rel];
      if (!entries) return send(res, { error: "not found" }, 404);
      return send(res, { path: rel, entries, truncated: false });
    }
    if (action === "/content" || action === "/download") {
      if (!MOCK_SCAN_FILE_INDEX.has(rel)) {
        return send(res, { error: "not found" }, 404);
      }
      const text = MOCK_SCAN_TEXT[rel];
      if (action === "/content") {
        if (text == null) {
          return send(res, {
            path: rel,
            size: MOCK_SCAN_FILE_INDEX.get(rel),
            mtime: MOCK_MTIME,
            truncated: false,
            binary: true,
          });
        }
        return send(res, {
          path: rel,
          size: text.length,
          mtime: MOCK_MTIME,
          truncated: false,
          binary: false,
          content: text,
        });
      }
      res.writeHead(200, {
        "content-type": text == null
          ? "application/octet-stream"
          : "text/plain; charset=utf-8",
        "content-disposition": `attachment; filename="${rel.split("/").pop()}"`,
      });
      return res.end(text == null ? "mock binary content" : text);
    }
    return send(res, { error: "not supported by the mock backend" }, 501);
  }

  // -------- Schedules ------------------------------------------------------
  if (method === "GET" && url === "/api/schedules") {
    return send(res, state.schedules);
  }

  // -------- Provider catalog + stored auth profiles ------------------------
  if (method === "GET" && url === "/api/providers") {
    return send(res, providerCatalog);
  }
  if (method === "GET" && url === "/api/auth/profiles") {
    return send(res, state.authProfiles);
  }

  // -------- LLM / environment / legacy-import settings ---------------------
  if (method === "GET" && url === "/api/settings/llm") {
    return send(res, llmSettings);
  }
  if (method === "GET" && url === "/api/settings/environment") {
    return send(res, { envFile: "~/.xalgorix.env", variables: [], restartRequired: false });
  }
  if (method === "GET" && url === "/api/legacy-import/status") {
    return send(res, { count: 0, dismissed: true });
  }

  // -------- R3: Projects -----------------------------------------------------
  if (method === "GET" && url === "/api/projects") {
    return send(res, { projects: state.projects.filter((p) => !p.archived_at) });
  }
  if (method === "POST" && url === "/api/projects") {
    const body = await readBody(req);
    const id = `proj_${state.projects.length + 1}`;
    const slug =
      String(body.name || `project-${id}`)
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-+|-+$/g, "") || id;
    const proj = {
      id,
      name: String(body.name || id),
      slug,
      scope: body.scope || { targets: [] },
      created_at: nowIso(),
      task_count: 0,
      active_runs: 0,
    };
    state.projects.push(proj);
    return send(res, proj, 201);
  }
  const projOne = url.match(/^\/api\/projects\/([^/]+)$/);
  if (projOne) {
    const proj = state.projects.find((p) => p.id === projOne[1]);
    if (!proj) return send(res, { error: "not found" }, 404);
    if (method === "GET") return send(res, proj);
    if (method === "PATCH") {
      const body = await readBody(req);
      if (typeof body.name === "string") proj.name = body.name;
      if (body.scope) proj.scope = body.scope;
      if (body.archived === true) proj.archived_at = nowIso();
      if (body.archived === false) delete proj.archived_at;
      return send(res, proj);
    }
    if (method === "DELETE") {
      if ((proj.active_runs ?? 0) > 0) {
        return send(res, { error: "project has active runs" }, 409);
      }
      state.projects = state.projects.filter((p) => p.id !== proj.id);
      return ok(res, { status: "deleted" });
    }
  }

  // -------- R3: Tasks ----------------------------------------------------------
  const projTasks = url.match(/^\/api\/projects\/([^/]+)\/tasks$/);
  if (projTasks && method === "GET") {
    return send(res, {
      tasks: state.tasks.filter(
        (t) => t.project_id === projTasks[1] && !t.archived_at,
      ),
    });
  }
  // R-goal 目标意图分析预览（确定性 mock：不假装智能，给可编辑的草案形状）。
  const projAnalyzeGoal = url.match(/^\/api\/projects\/([^/]+)\/analyze-goal$/);
  if (projAnalyzeGoal && method === "POST") {
    const body = await readBody(req);
    const goal = String(body.goal || "").trim();
    if (!goal) return send(res, { error: "goal is required" }, 400);
    const project = state.projects.find((p) => p.id === projAnalyzeGoal[1]);
    if (!project) return send(res, { error: "not found" }, 404);
    return send(res, {
      proposal: {
        statement: `Deliver verified findings proving: ${goal}`,
        criteria: [
          {
            kind: "finding_class",
            vuln_class: "sql injection",
            verify_by: "verifier",
            required: true,
            rationale: "mock draft — edit freely before saving",
          },
        ],
        constraints: [],
        notes: "Mock analysis: rewrite the draft statement and criteria to match the real objective.",
      },
    });
  }
  if (projTasks && method === "POST") {
    const body = await readBody(req);
    const id = `task_${state.tasks.length + 1}`;
    const task = {
      id,
      project_id: projTasks[1],
      name: String(body.name || id),
      goal: String(body.goal || ""),
      instruction: body.instruction || undefined,
      phase_policy: body.phase_policy || undefined,
      schedule: body.schedule || undefined,
      objective: body.objective || undefined,
      created_at: nowIso(),
      run_count: 0,
      finding_count: 0,
    };
    state.tasks.push(task);
    return send(res, task, 201);
  }
  const taskRunsPost = url.match(/^\/api\/tasks\/([^/]+)\/runs$/);
  if (taskRunsPost && method === "POST") {
    const task = state.tasks.find((t) => t.id === taskRunsPost[1]);
    if (!task) return send(res, { error: "not found" }, 404);
    await readBody(req);
    // 一次性守卫（与真实后端 handleTaskRunStart 的 409 一致）：
    // 活跃 run（pending/running/paused）存在时拒绝重复发起。
    const activeRun = state.runs.find(
      (r) =>
        r.task_id === task.id &&
        ["running", "pending", "paused"].includes(r.status),
    );
    if (activeRun) {
      return send(
        res,
        {
          error:
            "task already has an active run (pending/running/paused); stop or resume it before starting another",
          run_id: activeRun.id,
          instance_id: activeRun.instance_id,
        },
        409,
      );
    }
    const project = state.projects.find((p) => p.id === task.project_id);
    // R-goal 第 0 轮目标分析模拟：任务有 goal 但 objective 未结构化时，
    // 派发后延迟落盘 + goal_analyzed 广播（真实后端在 runMultiScan 里做）。
    if (!task.objective && (task.goal || "").trim()) {
      setTimeout(() => {
        task.objective = {
          kind: "engagement",
          statement: `Deliver verified findings proving: ${task.goal.trim()}`,
          criteria: [
            {
              id: "C1",
              kind: "finding_class",
              vuln_class: "sql injection",
              verify_by: "verifier",
              required: true,
            },
          ],
        };
        broadcastEvent({
          type: "goal_analyzed",
          content: `Goal analysis (analyzed) for task ${task.name}`,
          result_meta: {
            task_id: task.id,
            project_id: task.project_id,
            status: "analyzed",
            statement: task.objective.statement,
            criteria: task.objective.criteria.length,
          },
          timestamp: nowIso(),
        });
      }, 1500);
    }
    // 任务:运行 = 1:1（REDESIGN D11，2026-11-22）：终态 Run 重派同一实例，
    // 复用同一 run 记录——与真实后端 handleTaskRunStart 一致；仅无历史 Run
    // 时才新建。
    const priorRun = state.runs.find((r) => r.task_id === task.id);
    if (priorRun) {
      const inst = state.instances.find((i) => i.id === priorRun.instance_id);
      if (inst) {
        inst.status = "pending";
        inst.stop_reason = undefined;
        inst.started_at = nowIso();
        inst.instruction = task.instruction || task.goal;
      }
      priorRun.status = "pending";
      priorRun.started_at = nowIso();
      task.last_run_at = priorRun.started_at;
      task.last_run_status = "pending";
      return ok(res, { status: "pending", run_id: priorRun.id, instance_id: priorRun.instance_id });
    }
    const instId = `inst_${String(state.instances.length + 1).padStart(2, "0")}`;
    state.instances.unshift({
      id: instId,
      name: task.name,
      targets: (project?.scope.targets.join(",") || "demo.local"),
      status: "running",
      started_at: nowIso(),
      iterations: 0,
      tool_calls: 0,
      vuln_count: 0,
      total_tokens: 0,
      scan_mode: task.phase_policy?.scan_mode || "single",
      instruction: task.instruction || task.goal,
      current_phase: 1,
      vulns: [],
    });
    const run = {
      id: `run_${String(state.runs.length + 1).padStart(2, "0")}`,
      task_id: task.id,
      instance_id: instId,
      mode: task.phase_policy?.scan_mode || "single",
      status: "running",
      started_at: nowIso(),
      vuln_count: 0,
      total_tokens: 0,
      trigger: "manual",
    };
    state.runs.unshift(run);
    task.run_count = (task.run_count ?? 0) + 1;
    task.last_run_at = run.started_at;
    task.last_run_status = "running";
    // status 与真实后端一致：入队为 "pending"（同 POST /api/scan），admission 后才 running。
    return ok(res, { status: "pending", run_id: run.id, instance_id: instId });
  }
  const taskRunsGet = url.match(/^\/api\/tasks\/([^/]+)\/runs$/);
  if (taskRunsGet && method === "GET") {
    const runs = state.runs.filter((r) => r.task_id === taskRunsGet[1]);
    return send(res, { items: runs, total: runs.length, page: 1, size: runs.length || 1 });
  }
  const taskFindingsGet = url.match(/^\/api\/tasks\/([^/]+)\/findings$/);
  if (taskFindingsGet && method === "GET") {
    const task = state.tasks.find((t) => t.id === taskFindingsGet[1]);
    if (!task) return send(res, { error: "not found" }, 404);
    // 任务级累积投影（mock：该任务各 Run 所挂实例 vulns 的拼接）。
    const taskRuns = state.runs.filter((r) => r.task_id === task.id);
    const out = [];
    for (const r of taskRuns) {
      const inst = state.instances.find((i) => i.id === r.instance_id);
      for (const v of inst?.vulns || []) {
        out.push({
          ...v,
          scan_id: inst?.id ?? r.instance_id ?? r.id,
          scan_target: inst?.parent_target || inst?.targets || "demo.local",
          scan_started_at: inst?.started_at ?? r.started_at ?? nowIso(),
        });
      }
    }
    return send(res, out);
  }
  // GET /api/tasks/{id}/objective — 只读达成度投影（mock 级近似：finding_class
  // 判据在该任务已有发现时判 met，其余 undetermined；达成度不阻断任何东西）。
  const taskObjectiveGet = url.match(/^\/api\/tasks\/([^/]+)\/objective$/);
  if (taskObjectiveGet && method === "GET") {
    const task = state.tasks.find((t) => t.id === taskObjectiveGet[1]);
    if (!task) return send(res, { error: "not found" }, 404);
    const objective = task.objective ?? null;
    const structured = !!objective;
    const findings = state.instances
      .filter((i) =>
        state.runs.some(
          (r) => r.task_id === task.id && r.instance_id === i.id,
        ),
      )
      .flatMap((i) => i.vulns || []);
    const criteria = (objective?.criteria ?? []).map((c, idx) => {
      const hit = c.kind === "finding_class" && findings.length > 0;
      return {
        ...c,
        id: c.id ?? `C${idx + 1}`,
        state: hit ? "met" : "undetermined",
        reason: hit ? "该任务已有同类发现（mock 投影）" : undefined,
      };
    });
    const met = criteria.filter((c) => c.state === "met").length;
    const undetermined = criteria.length - met;
    const attainment = criteria.length ? met / criteria.length : 0;
    const report = {
      structured,
      criteria,
      met,
      unmet: 0,
      undetermined,
      manual_pending: 0,
      attainment,
    };
    const latestRun = state.runs.find((r) => r.task_id === task.id);
    return send(res, {
      objective,
      structured,
      report,
      runs: latestRun
        ? [
            {
              run_id: latestRun.id,
              scan_id: latestRun.instance_id ?? latestRun.id,
              status: latestRun.status,
              report,
            },
          ]
        : [],
      ring: latestRun ? "act" : "observe",
      ring_basis: latestRun ? "最新 run transcript（mock）" : "尚无运行",
      gaps: [],
      schedulable: [],
    });
  }
  const taskOne = url.match(/^\/api\/tasks\/([^/]+)$/);
  if (taskOne) {
    const task = state.tasks.find((t) => t.id === taskOne[1]);
    if (!task) return send(res, { error: "not found" }, 404);
    if (method === "GET") return send(res, task);
    if (method === "PATCH") {
      const body = await readBody(req);
      for (const key of ["name", "goal", "instruction", "phase_policy", "schedule"]) {
        if (body[key] !== undefined) task[key] = body[key];
      }
      // 三态语义与真实后端一致：缺席=不动、null=清除、对象=替换。
      if (body.objective !== undefined) task.objective = body.objective || undefined;
      task.updated_at = nowIso();
      return send(res, task);
    }
    if (method === "DELETE") {
      state.tasks = state.tasks.filter((t) => t.id !== task.id);
      return ok(res, { status: "deleted" });
    }
  }

  // -------- R3: Approvals -------------------------------------------------------
  const pendingApprovals = () =>
    state.approvals.filter((a) => a.status === "pending");
  if (method === "GET" && url === "/api/approvals/summary") {
    const pending = pendingApprovals().sort((a, b) =>
      a.deadline_at < b.deadline_at ? -1 : 1,
    );
    return send(res, {
      pending_total: pending.length,
      nearest_deadline: pending[0]?.deadline_at,
    });
  }
  if (method === "GET" && url === "/api/approvals") {
    const q = new URLSearchParams(req.url.split("?")[1] || "");
    const status = q.get("status") || "pending";
    const projectId = q.get("project_id");
    let list = state.approvals;
    if (projectId) list = list.filter((a) => a.project_id === projectId);
    if (status === "pending") list = list.filter((a) => a.status === "pending");
    else if (status === "decided")
      list = list.filter((a) => a.status !== "pending");
    return send(res, { approvals: list, pending_total: pendingApprovals().length });
  }
  const apprDecision = url.match(/^\/api\/approvals\/([^/]+)\/decision$/);
  if (apprDecision && method === "POST") {
    const appr = state.approvals.find((a) => a.id === apprDecision[1]);
    if (!appr) return send(res, { error: "not found" }, 404);
    if (appr.status !== "pending") return send(res, { error: "not pending" }, 409);
    const body = await readBody(req);
    appr.status = body.decision === "approved" ? "approved" : "rejected";
    appr.decided_at = nowIso();
    appr.decided_by = "operator:admin";
    appr.reason = body.reason || undefined;
    return send(res, appr);
  }
  if (url === "/api/approvals/batch-decision" && method === "POST") {
    const body = await readBody(req);
    let approved = 0;
    let rejected = 0;
    let skipped = 0;
    for (const id of body.ids || []) {
      const appr = state.approvals.find((a) => a.id === id);
      if (!appr || appr.status !== "pending") {
        skipped += 1;
        continue;
      }
      appr.status = body.decision === "approved" ? "approved" : "rejected";
      appr.decided_at = nowIso();
      appr.decided_by = "operator:admin";
      appr.reason = body.reason || undefined;
      if (appr.status === "approved") approved += 1;
      else rejected += 1;
    }
    return send(res, { approved, rejected, skipped });
  }

  // -------- 审批豁免规则（approve & remember，契约 §3.4）--------------------
  // -------- 集成模块：MCP 服务器 / 用户技能 / Agent 档案（契约 §integration-*）

  const invocationMatch = url.match(/^\/api\/(mcp-servers|skills|agent-profiles)\/invocations$/);
  if (invocationMatch && method === "GET") {
    const module = invocationMatch[1] === "mcp-servers" ? "mcp" : invocationMatch[1] === "skills" ? "skills" : "agents";
    const q = new URLSearchParams(req.url.split("?")[1] || "");
    const page = Math.max(1, Number.parseInt(q.get("page") || "1", 10) || 1);
    const size = Math.max(1, Number.parseInt(q.get("size") || "50", 10) || 50);
    let list = state.invocations.filter((r) => r.module === module);
    const instanceId = q.get("instance_id");
    const name = (q.get("name") || "").toLowerCase();
    const status = q.get("status");
    if (instanceId) list = list.filter((r) => r.instance_id === instanceId);
    if (name) list = list.filter((r) => r.name.toLowerCase().includes(name));
    if (status) list = list.filter((r) => r.status === status);
    list = [...list].reverse(); // newest first
    const start = (page - 1) * size;
    return send(res, { items: list.slice(start, start + size), total: list.length, page, size });
  }

  if (method === "GET" && url === "/api/mcp-servers") {
    return send(res, { servers: state.mcpServers });
  }
  if (url === "/api/mcp-servers" && method === "POST") {
    const body = await readBody(req);
    if (!body.name || !body.transport) {
      return send(res, { error: "name and transport are required" }, 400);
    }
    if (state.mcpServers.some((m) => m.name === body.name)) {
      return send(res, { error: "duplicate name" }, 409);
    }
    const server = {
      id: `mcp_${state.mcpServers.length + 1}_${Date.now().toString(36)}`,
      command: "", args: [], env: {}, url: "", headers: {},
      timeout_seconds: 120, tool_count: 0, last_error: "",
      created_by: "operator:admin", created_at: nowIso(),
      ...body,
    };
    state.mcpServers.unshift(server);
    broadcastEvent({ type: "mcp_servers_changed", content: "MCP server created", timestamp: nowIso() });
    return send(res, server, 201);
  }
  const mcpDetail = url.match(/^\/api\/mcp-servers\/([^/]+)$/);
  if (mcpDetail) {
    const idx = state.mcpServers.findIndex((m) => m.id === mcpDetail[1]);
    if (idx < 0) return send(res, { error: "not found" }, 404);
    if (method === "GET") return send(res, state.mcpServers[idx]);
    if (method === "PATCH") {
      const body = await readBody(req);
      // 打码回显语义：**** 前缀 = 未修改。
      for (const field of ["env", "headers"]) {
        if (body[field]) {
          for (const [k, v] of Object.entries(body[field])) {
            if (typeof v === "string" && v.startsWith("****")) body[field][k] = state.mcpServers[idx][field]?.[k] ?? "";
          }
        }
      }
      state.mcpServers[idx] = { ...state.mcpServers[idx], ...body, updated_at: nowIso() };
      broadcastEvent({ type: "mcp_servers_changed", content: "MCP server updated", timestamp: nowIso() });
      return send(res, state.mcpServers[idx]);
    }
    if (method === "DELETE") {
      const [removed] = state.mcpServers.splice(idx, 1);
      broadcastEvent({ type: "mcp_servers_changed", content: "MCP server deleted", timestamp: nowIso() });
      return send(res, { ok: true, removed: removed.id });
    }
  }
  const mcpRefresh = url.match(/^\/api\/mcp-servers\/([^/]+)\/refresh$/);
  if (mcpRefresh && method === "POST") {
    const server = state.mcpServers.find((m) => m.id === mcpRefresh[1]);
    if (!server) return send(res, { error: "not found" }, 404);
    if (server.transport === "http" && /127\.|localhost/.test(server.url || "")) {
      return send(res, { error: "endpoint resolves to the operator machine" }, 400);
    }
    server.tool_count = server.enabled ? 6 : 0;
    server.last_healthy_at = nowIso();
    server.last_error = server.enabled ? "" : "disabled";
    broadcastEvent({ type: "mcp_servers_changed", content: "MCP server refreshed", timestamp: nowIso() });
    return send(res, server);
  }

  if (method === "GET" && url === "/api/skills") {
    return send(res, { skills: state.skills });
  }
  if (url === "/api/skills" && method === "POST") {
    const body = await readBody(req);
    if (!/^[a-z0-9]+(-[a-z0-9]+)*$/.test(body.name || "")) {
      return send(res, { error: "name must be kebab-case" }, 400);
    }
    if (state.skills.some((k) => k.name === (body.name || "").toLowerCase())) {
      return send(res, { error: "duplicate name" }, 409);
    }
    const skill = {
      id: `skill_${state.skills.length + 1}_${Date.now().toString(36)}`,
      category: "user", tags: [], intent: "", created_by: "operator:admin", created_at: nowIso(),
      ...body,
      name: (body.name || "").toLowerCase(),
    };
    state.skills.unshift(skill);
    broadcastEvent({ type: "skills_changed", content: "Skill created", timestamp: nowIso() });
    return send(res, skill, 201);
  }
  const skillDetail = url.match(/^\/api\/skills\/([^/]+)$/);
  if (skillDetail) {
    const idx = state.skills.findIndex((k) => k.id === skillDetail[1]);
    if (idx < 0) return send(res, { error: "not found" }, 404);
    if (method === "GET") return send(res, state.skills[idx]);
    if (method === "PATCH") {
      const body = await readBody(req);
      state.skills[idx] = { ...state.skills[idx], ...body, updated_at: nowIso() };
      broadcastEvent({ type: "skills_changed", content: "Skill updated", timestamp: nowIso() });
      return send(res, state.skills[idx]);
    }
    if (method === "DELETE") {
      const [removed] = state.skills.splice(idx, 1);
      broadcastEvent({ type: "skills_changed", content: "Skill deleted", timestamp: nowIso() });
      return send(res, { ok: true, removed: removed.id });
    }
  }

  if (method === "GET" && url === "/api/agent-profiles") {
    const builtin = ["recon-discovery", "authz-logic", "business-logic", "injection-serverside", "client-source"].map((role, i) => ({
      id: `builtin:${role}`, role, builtin: true, enabled: true, focus: "", vuln_classes: [], evidence_contract: "", stopping_rule: "", created_at: nowIso(),
    }));
    return send(res, { profiles: [...builtin, ...state.agentProfiles] });
  }
  if (url === "/api/agent-profiles" && method === "POST") {
    const body = await readBody(req);
    const RESERVED = ["recon-discovery", "authz-logic", "business-logic", "injection-serverside", "client-source"];
    if (RESERVED.includes(body.role)) {
      return send(res, { error: "role is reserved by a built-in specialist profile" }, 409);
    }
    if (!/^[a-z0-9]+(-[a-z0-9]+)*$/.test(body.role || "")) {
      return send(res, { error: "role must be kebab-case" }, 400);
    }
    if (state.agentProfiles.some((p) => p.role === body.role)) {
      return send(res, { error: "duplicate role" }, 409);
    }
    const profile = {
      id: `ap_${state.agentProfiles.length + 1}_${Date.now().toString(36)}`,
      vuln_classes: [], tool_scope_hidden: [], builtin: false,
      created_by: "operator:admin", created_at: nowIso(),
      ...body,
    };
    state.agentProfiles.unshift(profile);
    broadcastEvent({ type: "agent_profiles_changed", content: "Agent profile created", timestamp: nowIso() });
    return send(res, profile, 201);
  }
  const profileDetail = url.match(/^\/api\/agent-profiles\/([^/]+)$/);
  if (profileDetail) {
    if (profileDetail[1].startsWith("builtin:")) {
      return send(res, { error: "built-in profiles are read-only" }, 404);
    }
    const idx = state.agentProfiles.findIndex((p) => p.id === profileDetail[1]);
    if (idx < 0) return send(res, { error: "not found" }, 404);
    if (method === "GET") return send(res, state.agentProfiles[idx]);
    if (method === "PATCH") {
      const body = await readBody(req);
      state.agentProfiles[idx] = { ...state.agentProfiles[idx], ...body, updated_at: nowIso() };
      broadcastEvent({ type: "agent_profiles_changed", content: "Agent profile updated", timestamp: nowIso() });
      return send(res, state.agentProfiles[idx]);
    }
    if (method === "DELETE") {
      const [removed] = state.agentProfiles.splice(idx, 1);
      broadcastEvent({ type: "agent_profiles_changed", content: "Agent profile deleted", timestamp: nowIso() });
      return send(res, { ok: true, removed: removed.id });
    }
  }

  if (method === "GET" && url === "/api/approval-rules") {
    const q = new URLSearchParams(req.url.split("?")[1] || "");
    const projectId = q.get("project_id");
    const kind = q.get("kind");
    let list = state.approvalRules;
    if (projectId) list = list.filter((r) => r.project_id === projectId);
    if (kind) list = list.filter((r) => r.kind === kind);
    return send(res, { rules: list });
  }
  if (url === "/api/approval-rules" && method === "POST") {
    const body = await readBody(req);
    if (!body.project_id || !body.kind || !body.match) {
      return send(res, { error: "project_id, kind and match are required" }, 400);
    }
    if (body.kind === "command" && !body.match.program) {
      return send(res, { error: "command rule requires match.program" }, 400);
    }
    if (body.kind === "http" && (!body.match.method || !body.match.host)) {
      return send(res, { error: "http rule requires match.method and match.host" }, 400);
    }
    const project = state.projects.find((p) => p.id === body.project_id);
    if (!project) return send(res, { error: "project not found" }, 404);
    const rule = {
      id: `arule_${state.approvalRules.length + 1}_${Date.now().toString(36)}`,
      project_id: body.project_id,
      project_name: project.name,
      kind: body.kind,
      match: body.match,
      reason: body.reason || undefined,
      created_by: "operator:admin",
      created_from_approval_id: body.created_from_approval_id || undefined,
      created_at: nowIso(),
      expires_at: body.expires_at || undefined,
      hit_count: 0,
    };
    state.approvalRules.unshift(rule);
    broadcastEvent({
      type: "approval_rules_changed",
      content: "Approval rule created",
      rule,
      timestamp: nowIso(),
    });
    return send(res, rule);
  }
  const ruleDelete = url.match(/^\/api\/approval-rules\/([^/]+)$/);
  if (ruleDelete && method === "DELETE") {
    const idx = state.approvalRules.findIndex((r) => r.id === ruleDelete[1]);
    if (idx < 0) return send(res, { error: "not found" }, 404);
    const [removed] = state.approvalRules.splice(idx, 1);
    broadcastEvent({
      type: "approval_rules_changed",
      content: "Approval rule deleted",
      timestamp: nowIso(),
    });
    return send(res, { ok: true, removed: removed.id });
  }

  // -------- R3.1: Guidance（人工在环插入意图）--------------------------------
  const taskGuidance = url.match(/^\/api\/tasks\/([^/]+)\/guidance$/);
  if (taskGuidance && method === "POST") {
    const task = state.tasks.find((t) => t.id === taskGuidance[1]);
    if (!task) return send(res, { error: "not found" }, 404);
    const body = await readBody(req);
    const message = typeof body.message === "string" ? body.message : "";
    const requestId = typeof body.request_id === "string" ? body.request_id : "";
    if (!message || Array.from(message).length > 4000) {
      return send(res, { error: "message must be 1..4000 code points" }, 400);
    }
    if (!requestId) return send(res, { error: "request_id required" }, 400);
    // 幂等：同 request_id 返回首次结果。
    const existing = state.guidances.find((g) => g._request_id === requestId);
    if (existing) {
      return send(res, {
        status: existing.status === "queued" ? "queued" : "injected",
        guidance_id: existing.id,
        run_id: existing.run_id,
        accepted: false,
      });
    }
    const run = state.runs.find(
      (r) => r.task_id === task.id && r.status === "running",
    );
    const g = {
      id: `gd_${String(state.guidances.length + 1).padStart(2, "0")}`,
      task_id: task.id,
      run_id: run?.id,
      instance_id: run?.instance_id,
      message,
      status: run ? "injected" : "queued",
      created_at: nowIso(),
      _request_id: requestId,
    };
    state.guidances.unshift(g);
    // 无乐观回显的另一半：服务端持久化后广播，transcript 从流里渲染人工 turn。
    const evtType = g.status === "injected" ? "guidance_injected" : "guidance_queued";
    broadcastEvent({
      type: evtType,
      content: message,
      timestamp: g.created_at,
      agent_id: g.instance_id,
      instance_id: g.instance_id,
      guidance: g,
    });
    return send(res, {
      status: g.status,
      guidance_id: g.id,
      run_id: g.run_id,
      accepted: true,
    });
  }
  if (taskGuidance && method === "GET") {
    const items = state.guidances
      .filter((g) => g.task_id === taskGuidance[1])
      .slice(0, 50)
      .map(({ _request_id, ...rest }) => rest);
    return send(res, { items });
  }

  // -------- 会话页 / 对话页（docs/SESSIONS_CHAT_1TO1_PLAN.md V1 前置）--------
  // 与 internal/web/chat_transcript.go 的投影语义对齐：guidance → human、
  // tool_call/tool_result、vuln → finding、finished → system、审批 → approval。
  const projConvs = url.match(/^\/api\/projects\/([^/]+)\/conversations$/);
  if (projConvs && method === "GET") {
    const qs = new URLSearchParams(req.url.split("?")[1] || "");
    const includeArchived = qs.get("include_archived") === "true";
    const taskIds = new Set(state.tasks.filter((t) => t.project_id === projConvs[1]).map((t) => t.id));
    const rows = conversationRows(includeArchived).filter((r) => taskIds.has(r.task_id));
    return send(res, { items: rows });
  }

  const convSub = url.match(/^\/api\/conversations\/([^/]+)\/(transcript|attachments)$/);
  if (convSub) {
    const instanceId = convSub[1];
    const run = state.runs.find((r) => r.instance_id === instanceId);
    if (convSub[2] === "transcript" && method === "GET") {
      const qs = new URLSearchParams(req.url.split("?")[1] || "");
      const limit = Math.min(Math.max(Number(qs.get("limit") || 80), 1), 500);
      const beforeSeq = Number(qs.get("before_seq") || 0);
      const items = buildMockTranscript(instanceId, run);
      const total = items.length;
      const end = beforeSeq > 0 ? Math.min(beforeSeq - 1, total) : total;
      const start = Math.max(0, end - limit);
      const page = items.slice(start, end);
      return send(res, {
        items: page,
        total,
        has_more: start > 0,
        earliest_seq: page[0]?.seq ?? 0,
        latest_seq: total,
        running: state.instances.find((i) => i.id === instanceId)?.status === "running",
      });
    }
    if (convSub[2] === "attachments" && method === "POST") {
      // multipart 只取文件名做回显；mock 不落盘，路径仅供 chip 展示。
      const raw = await new Promise((resolve) => {
        let data = "";
        req.setEncoding("binary");
        req.on("data", (chunk) => (data += chunk));
        req.on("end", () => resolve(data));
      });
      const names = [...raw.matchAll(/filename="([^"]+)"/g)].map((m) => m[1]);
      const attachments = (names.length ? names : ["attachment.bin"]).map((name) => ({
        path: `uploads/${instanceId}/${name}`,
        name,
        size: Buffer.byteLength(raw, "binary"),
      }));
      return send(res, { attachments });
    }
  }

  const convOne = url.match(/^\/api\/conversations\/([^/]+)$/);
  if (convOne && method === "PATCH") {
    const instanceId = convOne[1];
    const run = state.runs.find((r) => r.instance_id === instanceId);
    if (!run) return send(res, { error: "not found" }, 404);
    const body = await readBody(req);
    const meta = state.convMeta.get(instanceId) ?? {};
    if (typeof body.title === "string" && body.title.trim()) meta.title = body.title.trim();
    if (typeof body.pinned === "boolean") meta.pinned = body.pinned;
    if (typeof body.archived === "boolean") meta.archived = body.archived;
    state.convMeta.set(instanceId, meta);
    const row = conversationRows(true).find((r) => r.instance_id === instanceId);
    return send(res, row ?? run);
  }

  // 持久事件 journal（契约同 internal/web handleScanEvents）：角色会话在实例
  // 缓冲缺席（引擎重启）时的兜底数据源，{events,total,offset,limit} 信封。
  const scanEventsMatch = url.match(/^\/api\/scans\/([^/]+)\/events$/);
  if (scanEventsMatch && method === "GET") {
    const scanId = decodeURIComponent(scanEventsMatch[1]);
    const inst =
      state.instances.find((i) => i.id === scanId) ||
      state.instances.find((i) => i.name === scanId) ||
      state.scans.find((sc) => sc.id === scanId && sc.instance_id && state.instances.find((i) => i.id === sc.instance_id));
    const inst2 = inst?.id ? state.instances.find((i) => i.id === inst.id) : undefined;
    const events = inst2 ? mockInstanceEvents(inst2) : [];
    const qs2 = new URLSearchParams(req.url.split("?")[1] || "");
    const offset = Math.max(Number(qs2.get("offset") || 0), 0);
    const limit = Math.min(Math.max(Number(qs2.get("limit") || 300), 1), 1000);
    return send(res, {
      events: events.slice(offset, offset + limit),
      total: events.length,
      offset,
      limit,
    });
  }

  // -------- Run 账本投影（契约 §3.6）+ 任务情报（契约 §3.7）-----------------
  // 确定性 fixture：已知 scan 返回同一份假设快照（含 observed/inferred、
  // 引擎 source、finding_ref）；情报按 task 维度聚合，分诊 PATCH 落内存覆盖层。
  const ledgerMatch = url.match(/^\/api\/scans\/([^/]+)\/ledger$/);
  if (ledgerMatch && method === "GET") {
    const scanId = decodeURIComponent(ledgerMatch[1]);
    const known =
      state.instances.some((inst) => inst.id === scanId) ||
      state.scans.some((sc) => sc.id === scanId);
    if (!known) return send(res, { error: "scan not found" }, 404);
    const t0 = new Date(Date.now() - 3600_000).toISOString();
    const t1 = new Date(Date.now() - 1800_000).toISOString();
    const ev = (n, kind, summary, nature, source, confidence) => ({
      id: `ev_${n}`,
      kind,
      summary,
      nature,
      ...(source ? { source } : {}),
      confidence,
      created_at: t1,
    });
    return send(res, {
      scan_id: scanId,
      ledger_present: true,
      ledger_damaged: false,
      incomplete: false,
      counts: { queued: 2, testing: 1, proven: 1, rejected: 1, blocked: 0, exhausted: 0 },
      hypotheses: [
        {
          id: "H-2",
          dedup_key: "sqli|api.example.com/login|username",
          title: "Login form /username 允许布尔盲注",
          vuln_class: "sqli",
          target: "https://api.example.com",
          endpoint: "https://api.example.com/login",
          parameter: "username",
          status: "testing",
          confidence: 0.72,
          voi: 0.81,
          voi_estimated: true,
          assigned_to: "inst_01/spec-injection",
          next_action: "时间盲注基线对照（sleep 3 vs 1）",
          attempts: 2,
          origin: "recon",
          created_at: t0,
          updated_at: t1,
          evidence: [
            ev(1, "baseline", "正常请求 200/118ms，参数原样回显", "observed", "", 0.9),
            ev(2, "probe", "' OR 1=1-- 响应 500，日志出现 SQL 错误特征", "observed", "", 0.8),
          ],
        },
        {
          id: "H-1",
          dedup_key: "xss|acme.com/search|q",
          title: "/search 的 q 参数反射未编码输出",
          vuln_class: "xss",
          target: "https://acme.com",
          endpoint: "https://acme.com/search",
          parameter: "q",
          status: "proven",
          confidence: 1,
          assigned_to: "inst_01/spec-xss",
          attempts: 3,
          origin: "recon",
          created_at: t0,
          updated_at: t1,
          evidence: [
            ev(3, "exploit", "<svg onload> 原样出现在响应 HTML", "observed", "verify_xss", 0.95),
            ev(4, "finding_ref", "已按 XALG-2 上报", "observed", "", 1),
          ],
        },
        {
          id: "H-3",
          dedup_key: "authbypass|api.example.com/admin",
          title: "/admin 在无 Cookie 时返回 200（疑似网关配置）",
          vuln_class: "authbypass",
          target: "https://api.example.com",
          endpoint: "https://api.example.com/admin",
          status: "queued",
          confidence: 0.4,
          next_action: "对照直连与经网关两条路径的状态码",
          attempts: 0,
          origin: "llm",
          created_at: t0,
          updated_at: t0,
          evidence: [
            ev(5, "artifact", "直接访问返回登录页 HTML（内容与登录后一致）", "observed", "", 0.6),
          ],
        },
        {
          id: "H-4",
          dedup_key: "sqli|api.example.com/export|date",
          title: "/export 的 date 参数（已否决：参数化查询）",
          vuln_class: "sqli",
          target: "https://api.example.com",
          endpoint: "https://api.example.com/export",
          parameter: "date",
          status: "rejected",
          confidence: 0.1,
          attempts: 2,
          origin: "auto",
          created_at: t0,
          updated_at: t0,
          evidence: [
            ev(6, "control", "注入载荷被参数化驱动转义，行为与基线一致", "observed", "", 0.9),
          ],
        },
        {
          id: "H-5",
          dedup_key: "idor|api.example.com/invoice/1",
          title: "IDOR 探测排队中",
          vuln_class: "idor",
          target: "https://api.example.com",
          endpoint: "https://api.example.com/invoice/1",
          status: "queued",
          confidence: 0.3,
          attempts: 0,
          origin: "llm",
          created_at: t0,
          updated_at: t0,
        },
      ],
    });
  }

  const intelMatch = url.match(/^\/api\/tasks\/([^/]+)\/intelligence$/);
  if (intelMatch && method === "GET") {
    const taskId = decodeURIComponent(intelMatch[1]);
    const task = state.tasks.find((tk) => tk.id === taskId);
    if (!task) return send(res, { items: [], counts: { credential: 0, lead: 0, sensitive_data: 0 }, triage_available: true });
    const run = state.runs.find((r) => r.task_id === taskId);
    const scanId = run?.instance_id ?? run?.id ?? "inst_01";
    const t0 = new Date(Date.now() - 5400_000).toISOString();
    const t1 = new Date(Date.now() - 900_000).toISOString();
    const items = [
      {
        id: "I-1",
        fingerprint: "fp_mock_cred_admin01",
        kind: "credential",
        title: "Admin 面板接受默认凭据 admin:admin",
        content: "POST /app/admin/login {username:\"admin\", password:\"admin\"} → 302 /admin/dashboard；响应 Set-Cookie SESSIONID=…（已脱敏）。建议首轮利用后立即改密并记录。",
        endpoint: "acme.com/app/admin/login",
        confidence: 0.9,
        created_at: t0,
        updated_at: t1,
        scan_ids: [scanId],
      },
      {
        id: "I-2",
        fingerprint: "fp_mock_lead_invoices",
        kind: "lead",
        title: "发票 id 连续递增（疑似可遍历）",
        content: "GET /invoice/1001 与 /invoice/1002 响应结构一致，id 为连续整数；配合未验证的会话权限，值得在取得任意有效会话后做 IDOR 矩阵。",
        endpoint: "api.acme.com/invoice",
        confidence: 0.6,
        created_at: t0,
        updated_at: t0,
        scan_ids: [scanId],
      },
      {
        id: "I-3",
        fingerprint: "fp_mock_data_backup",
        kind: "sensitive_data",
        title: "/backup.sql 可匿名下载（约 4MB，含表结构）",
        content: "GET https://acme.com/backup.sql → 200 text/sql，头部含 CREATE TABLE users 与样例行；不属于漏洞类目本身，按敏感数据披露记录。",
        endpoint: "acme.com/backup.sql",
        confidence: 0.95,
        created_at: t0,
        updated_at: t0,
        scan_ids: [scanId],
      },
    ];
    const triages = state.intelTriages.get(taskId);
    if (triages) {
      for (const it of items) {
        const tg = triages.get(it.fingerprint);
        if (tg) it.triage = { task_id: taskId, fingerprint: it.fingerprint, ...tg };
      }
    }
    return send(res, {
      items,
      counts: { credential: 1, lead: 1, sensitive_data: 1 },
      triage_available: true,
    });
  }
  const intelTriageMatch = url.match(/^\/api\/tasks\/([^/]+)\/intelligence\/([^/]+)$/);
  if (intelTriageMatch && (method === "PATCH" || method === "PUT")) {
    const taskId = decodeURIComponent(intelTriageMatch[1]);
    const fingerprint = decodeURIComponent(intelTriageMatch[2]);
    const body = await readBody(req);
    const status = body.status;
    if (!["open", "confirmed", "dismissed"].includes(status)) {
      return send(res, { error: "status must be open|confirmed|dismissed" }, 400);
    }
    let triages = state.intelTriages.get(taskId);
    if (!triages) {
      triages = new Map();
      state.intelTriages.set(taskId, triages);
    }
    triages.set(fingerprint, { status, decided_by: "operator", decided_at: nowIso() });
    return send(res, { fingerprint, status });
  }

  // Default. For an unhandled GET /api/* route fall back to an empty array
  // so a forgotten endpoint degrades to an empty collection instead of an
  // object that crashes array consumers with "(...).map is not a function".
  // Non-GET keeps the neutral ok envelope.
  if (method === "GET" && url.startsWith("/api/")) return send(res, []);
  send(res, { ok: true });
});

const port = Number(process.env.PORT || 8080);
server.listen(port, () => console.log(`mock backend on ${port}`));

// Server-side approval timeout simulation: deadline_at elapsed → status=timeout.
// Mirrors XALGORIX_APPROVAL_TIMEOUT enforcement (decision by timeout = rejection).
setInterval(() => {
  const now = Date.now();
  for (const a of state.approvals) {
    if (a.status === "pending" && new Date(a.deadline_at).getTime() <= now) {
      a.status = "timeout";
      a.decided_at = a.deadline_at;
      a.decided_by = "timeout";
    }
  }
}, 1000);

// ---------------------------------------------------------------------------
// SSE: stream synthetic events to keep the live feed populated.
// ---------------------------------------------------------------------------
// 连接注册表在文件顶部（sseClients / broadcastEvent）；这里只负责合成事件。
let tick = 0;
setInterval(() => {
  if (sseClients.size === 0) return;
  const run = runningInstance();
  if (!run) return; // pause the feed once everything is stopped
  const i = tick++;
  const isApprovalPing = i % 9 === 4;
  if (isApprovalPing) {
    const pending = state.approvals.find(
      (a) => a.status === "pending" && a.instance_id === run.id,
    );
    if (pending) {
      broadcastEvent({
        type: "approval_requested",
        content: pending.payload.command || `${pending.payload.method} ${pending.payload.url}`,
        tool_name: pending.kind === "command" ? "terminal_execute" : "http_request",
        timestamp: nowIso(),
        agent_id: run.id,
        instance_id: run.id,
        current_phase: run.current_phase,
        approval: pending,
      });
    }
    return;
  }
  // 审批规则命中演示（契约 §3.4）：每隔一段时间挑一条命中过的规则广播，
  // 让 LiveFeed 的 approval_rule_hit 紫色行有真实样例可看。
  if (i % 27 === 17) {
    const hit = state.approvalRules.find((r) => r.hit_count > 0);
    if (hit) {
      broadcastEvent({
        type: "approval_rule_hit",
        content: `命中审批豁免规则——免审批直接放行`,
        timestamp: nowIso(),
        agent_id: run.id,
        instance_id: run.id,
        rule: { ...hit, hit_count: hit.hit_count + 1 },
      });
    }
    return;
  }
  const specialist = i % 3 === 2;
  // LLM 对话回合合成：assistant 分析文本分片 delta 推送（每 ~9 tick 一次），
  // 前端按 message_id 累积成打字气泡，done 后固化为事件行。
  if (i % 9 === 5) {
    const rounds = [
      "httpx 扫到 api 主机 3 个存活端点。结合此前记录的 401/200 差异，/api/v1/orders 是目前最强的 IDOR 候选，下一轮做双账号交叉验证。",
      "被动漏扫无命中，注入泳道转去认领下一条假设，协调器继续扩端点覆盖。",
      "/admin 出现信号矛盾：角色 A 会话拿到 200，新 cookie 重放却是 401。先按过期会话噪声处理，上报前重新验证。",
    ];
    const text = rounds[(i / 9 | 0) % rounds.length];
    const msgId = `msg_${run.id}_${i}`;
    const pieces = text.match(/.{1,28}/g) || [text];
    pieces.forEach((piece, pi) => {
      setTimeout(() => {
        broadcastEvent({
          type: pi === pieces.length - 1 ? "assistant_done" : "assistant_delta",
          message_id: msgId,
          delta: pi === pieces.length - 1 ? undefined : piece,
          content: pi === pieces.length - 1 ? text : undefined,
          agent_id: run.id,
          instance_id: run.id,
          timestamp: nowIso(),
          current_phase: run.current_phase,
        });
      }, pi * 220);
    });
    return;
  }
  broadcastEvent({
    type: "tool_call",
    content: specialist
      ? `httpx -silent ${run.parent_target || run.targets} endpoint ${i}`
      : `nmap -sV ${run.parent_target || run.targets} probe ${i}`,
    tool_name: specialist ? "httpx" : "nmap",
    timestamp: nowIso(),
    // coordinator = 主循环（agent_id 即实例 id）；specialist 用独立标识，
    // 演示会话控制台的 agent 视角切换。instance_id 与 agent_id 正交：
    // Run 过滤走前者，agent 视角走后者。
    agent_id: specialist ? `${run.id}/spec-injection` : run.id,
    instance_id: run.id,
    current_phase: run.current_phase,
  });
}, 1500);
