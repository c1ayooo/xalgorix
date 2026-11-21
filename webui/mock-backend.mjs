import http from "node:http";
import { WebSocketServer } from "ws";

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
        { id: "v1", title: "SQL Injection in /search", severity: "critical", endpoint: "https://acme.com/search?q=", cvss: 9.8, cve: "CVE-2024-1234" },
        { id: "v2", title: "Reflected XSS in /login", severity: "high", endpoint: "https://acme.com/login", cvss: 7.5 },
        { id: "v3", title: "Open Redirect on /go", severity: "medium", endpoint: "https://acme.com/go", cvss: 4.3 },
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
  agentMail: { pod: "ops-pod-1", apiKey: "", hasApiKey: true },
  schedules: [],
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

const server = http.createServer(async (req, res) => {
  const method = req.method || "GET";
  const url = req.url.split("?")[0];
  state.reqCounter += 1;

  // CORS preflight (the Vite dev server proxies same-origin so this is rarely
  // hit, but harmless to support).
  if (method === "OPTIONS") {
    res.writeHead(204, {
      "access-control-allow-origin": "*",
      "access-control-allow-methods": "GET,POST,DELETE,OPTIONS",
      "access-control-allow-headers": "content-type",
    });
    return res.end();
  }

  // -------- Auth (auth disabled in the mock so login is bypassed) ----------
  if (method === "GET" && url === "/api/auth/status") {
    return send(res, { auth_enabled: false, authenticated: true });
  }
  if (method === "POST" && url === "/api/auth/login") {
    await readBody(req);
    return send(res, { status: "ok" });
  }
  if (method === "POST" && url === "/api/auth/logout") {
    return send(res, { status: "logged_out" });
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
    return send(res, { instances: list, resources: state.resources });
  }
  const instOne = url.match(/^\/api\/instances\/([^/]+)$/);
  if (instOne && method === "GET") {
    const inst = state.instances.find((i) => i.id === instOne[1]);
    return inst ? send(res, inst) : send(res, { error: "not found" }, 404);
  }
  const instStop = url.match(/^\/api\/instances\/([^/]+)\/stop$/);
  if (instStop && method === "POST") {
    const inst = state.instances.find((i) => i.id === instStop[1]);
    if (!inst) return send(res, { error: "not found" }, 404);
    const body = await readBody(req);
    inst.status = "finished";
    inst.finished_at = nowIso();
    // 软取消语义：reason 供引擎下一 Run briefing 参考。
    inst.stop_reason = body.reason ? `manual: ${body.reason}` : "manual";
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

  // -------- Scans (records) -------------------------------------------------
  if (method === "GET" && url === "/api/scans") {
    return send(res, state.scans);
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
      const vulns =
        inst?.vulns ??
        (id === "scan_01"
          ? [
              { id: "v1", title: "SQL Injection in /search", severity: "critical", endpoint: "https://acme.com/search?q=", cvss: 9.8, cve: "CVE-2024-1234" },
              { id: "v2", title: "Reflected XSS in /login", severity: "high", endpoint: "https://acme.com/login", cvss: 7.5 },
              { id: "v3", title: "Open Redirect on /go", severity: "medium", endpoint: "https://acme.com/go", cvss: 4.3 },
              { id: "v4", title: "Verbose error in /api/v1/users", severity: "low", endpoint: "https://acme.com/api/v1/users", cvss: 2.0 },
              { id: "v5", title: "Missing security headers", severity: "info", endpoint: "https://acme.com/", cvss: 0 },
            ]
          : [
              { id: "v6", title: "S3 bucket misconfiguration", severity: "high", endpoint: "https://cdn.globex.io", cvss: 7.1 },
              { id: "v7", title: "Outdated jQuery 1.4.2", severity: "low", endpoint: "https://globex.io/static/jquery.js", cvss: 3.1 },
            ]);
      return send(res, { ...scan, events: [], vulns });
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
  if (method === "GET" && url === "/api/settings/agentmail") {
    return send(res, {
      pod: state.agentMail.pod,
      apiKey: "",
      hasApiKey: state.agentMail.hasApiKey,
    });
  }
  if (method === "POST" && url === "/api/settings/agentmail") {
    const body = await readBody(req);
    state.agentMail = {
      pod: typeof body.pod === "string" ? body.pod : state.agentMail.pod,
      apiKey: body.apiKey || state.agentMail.apiKey,
      hasApiKey: !!body.apiKey || state.agentMail.hasApiKey,
    };
    return send(res, {
      pod: state.agentMail.pod,
      apiKey: "",
      hasApiKey: state.agentMail.hasApiKey,
    });
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

  // -------- Chat (palette → ask) -------------------------------------------
  if (method === "POST" && url === "/api/chat") {
    await readBody(req);
    return send(res, {
      reply:
        "I am a mock assistant. The real backend would answer questions about the active scan here.",
    });
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
    const kinds = [
      { type: "phase", content: "Entering phase: reconnaissance", agent_id: inst.id },
      { type: "thought", content: "Surface is wide; prioritize the api host first", agent_id: inst.id },
      { type: "tool_call", tool_name: "nuclei", content: `nuclei -rl 60 -severity low,medium ${inst.targets}`, agent_id: inst.id },
      { type: "tool_result", content: "42 templates ran; 0 matches in passive set", agent_id: inst.id },
      { type: "tool_call", tool_name: "httpx", content: `httpx -silent -status-code ${inst.targets}`, agent_id: `${inst.id}/spec-injection` },
      { type: "tool_result", content: "3 endpoints responded 200", agent_id: `${inst.id}/spec-injection` },
      { type: "thought", content: "401 on /admin suggests authz surface; queue authz matrix", agent_id: `${inst.id}/spec-injection` },
      { type: "decision", content: "Claimed hypothesis: IDOR on /api/v1/orders", agent_id: `${inst.id}/spec-injection` },
      { type: "tool_call", tool_name: "nmap", content: `nmap -sV -p 443 ${inst.targets}`, agent_id: inst.id },
      { type: "tool_result", content: "1 service: nginx 1.24.0", agent_id: inst.id },
      { type: "http", content: "GET /api/v1/users/42 => 200 (role A)", agent_id: `${inst.id}/spec-injection` },
      { type: "http", content: "GET /api/v1/users/42 (anonymous) => 401", agent_id: `${inst.id}/spec-injection` },
      { type: "phase", content: "Entering phase: vulnerability testing", agent_id: inst.id },
      { type: "tool_call", tool_name: "sqlmap", content: `sqlmap -u 'https://${inst.targets}/search?q=1' --batch --level=2`, agent_id: `${inst.id}/spec-injection` },
      { type: "vuln", content: "SQL Injection in /search (critical, cvss 9.8)", agent_id: `${inst.id}/spec-injection` },
      { type: "tool_result", content: "Verifier re-exploited: confirmed via time-based payload", agent_id: inst.id },
    ];
    const total = kinds.length;
    const slice = kinds.slice(offset, offset + limit);
    const base = Date.now() - total * 60_000;
    const events = slice.map((k, idx) => ({
      ...k,
      instance_id: inst.id,
      timestamp: new Date(base + (offset + idx) * 60_000).toISOString(),
    }));
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
    const project = state.projects.find((p) => p.id === task.project_id);
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
    const wire = JSON.stringify({
      type: evtType,
      content: message,
      timestamp: g.created_at,
      agent_id: g.instance_id,
      instance_id: g.instance_id,
      guidance: g,
    });
    for (const client of wsClients) {
      if (client.readyState === 1) client.send(wire);
    }
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
// WebSocket: stream synthetic events to keep the live feed populated.
// ---------------------------------------------------------------------------
const wss = new WebSocketServer({ server, path: "/ws" });
// 连接注册表：POST 产生的服务端事件（guidance_injected 等）向全部客户端广播，
// 与契约 §3.1 的 WS 事件约定一致。
const wsClients = new Set();
wss.on("connection", (ws) => {
  wsClients.add(ws);
  ws.on("close", () => wsClients.delete(ws));
  let i = 0;
  const t = setInterval(() => {
    const run = runningInstance();
    if (!run) return; // pause the feed once everything is stopped
    const isEmail = i % 6 === 0;
    const isApprovalPing = i % 9 === 4;
    if (isApprovalPing) {
      const pending = state.approvals.find(
        (a) => a.status === "pending" && a.instance_id === run.id,
      );
      if (pending) {
        ws.send(
          JSON.stringify({
            type: "approval_requested",
            content: pending.payload.command || `${pending.payload.method} ${pending.payload.url}`,
            tool_name: pending.kind === "command" ? "terminal_execute" : "http_request",
            timestamp: nowIso(),
            agent_id: run.id,
            instance_id: run.id,
            current_phase: run.current_phase,
            approval: pending,
          }),
        );
      }
      i++;
      return;
    }
    const specialist = i % 3 === 2;
    // LLM 对话回合合成：assistant 分析文本分片 delta 推送（每 ~9 tick 一次），
    // 前端按 message_id 累积成打字气泡，done 后固化为事件行。
    if (i % 9 === 5) {
      const rounds = [
        "The httpx sweep found 3 live endpoints on the api host. Combined with the 401/200 differential we recorded earlier, /api/v1/orders is the strongest IDOR candidate so far.",
        "Nuclei came back clean on the passive set, so I'm moving the injection lane to claim the next hypothesis while the coordinator keeps widening endpoint coverage.",
        "Two signals contradict on /admin: the role-A session got 200, but the fresh cookie replay got 401. Treating the first as stale-session noise and re-verifying before reporting.",
      ];
      const text = rounds[(i / 9 | 0) % rounds.length];
      const msgId = `msg_${run.id}_${i}`;
      const pieces = text.match(/.{1,28}/g) || [text];
      pieces.forEach((piece, pi) => {
        setTimeout(() => {
          if (ws.readyState !== 1) return;
          ws.send(
            JSON.stringify({
              type: pi === pieces.length - 1 ? "assistant_done" : "assistant_delta",
              message_id: msgId,
              delta: pi === pieces.length - 1 ? undefined : piece,
              content: pi === pieces.length - 1 ? text : undefined,
              agent_id: run.id,
              instance_id: run.id,
              timestamp: nowIso(),
              current_phase: run.current_phase,
            }),
          );
        }, pi * 220);
      });
      i++;
      return;
    }
    ws.send(
      JSON.stringify({
        type: isEmail ? "email_received" : "tool_call",
        content: isEmail
          ? "Email from attacker@evil.test triaged"
          : specialist
            ? `httpx -silent ${run.parent_target || run.targets} endpoint ${i}`
            : `nmap -sV ${run.parent_target || run.targets} probe ${i}`,
        tool_name: isEmail ? "agentmail" : specialist ? "httpx" : "nmap",
        timestamp: nowIso(),
        // coordinator = 主循环（agent_id 即实例 id）；specialist 用独立标识，
        // 演示会话控制台的 agent 视角切换。instance_id 与 agent_id 正交：
        // Run 过滤走前者，agent 视角走后者。
        agent_id: specialist ? `${run.id}/spec-injection` : run.id,
        instance_id: run.id,
        current_phase: run.current_phase,
      }),
    );
    i++;
  }, 1500);
  ws.on("close", () => clearInterval(t));
});
