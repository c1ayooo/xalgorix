# API 契约 — 项目 / 任务 / 执行 / 审批（前端先行，R3）

> **用途**：前端（webui）已按本契约实现并用 `mock-backend.mjs` 验收。后端 agent 按此实现 REST 端点；字段名/形状以本文件为唯一权威，与 `docs/REDESIGN.md` §3 schema 对齐。改契约必须先改本文件 + `webui/src/types/api.ts` + `webui/mock-backend.mjs` 三处。
>
> **通用约定**：JSON；鉴权沿用现有 session/CSRF 与机器令牌中间件（审批决策端点属于状态变更，走同一 CSRF 检查）；错误沿用 `{"error": "..."}` 包络；时间一律 RFC3339 UTC（`deadline_at` 等前端做本地倒计时）。所有列表端点支持 `project_id` 过滤（隔离红线：冷路径查询强制 project 维度）；显式传入 `project_id` 而平台仓库不可用时返回 503，绝不退化为跨项目全量结果。
>
> **降级**：后端未实现期间，前端页面呈现 ErrorState（不 crash）。无 DSN 单机模式下后端可返回单个隐式项目 `default`（见 REDESIGN §3.3）。

## 0. 事件流（SSE，2026-11-22 起）

> 原 `/ws` WebSocket（gorilla hub + 机器令牌可直接在 WS 上发 ScanRequest）已**彻底移除**（破坏性变更）：前端只单向收事件，SSE 语义吻合且省去连接劫持的中间件特例。机器客户端改走 `POST /api/scan` 启动扫描 + REST 轮询取事件（`GET /api/instances/{id}/events`）；`/api/stream` 仅限 cookie 会话（EventSource 无法携带 Bearer 头）。

| Method+Path | 请求 | 响应 |
|---|---|---|
| GET `/api/stream` | `?instance=`（可选，服务端过滤该实例事件）`&since=`（可选，显式续传点，优先用 Last-Event-ID 头） | `text/event-stream`；帧 = `id: <seq>` + `event: <type>` + `data: <WSEvent JSON>`；30s `:: ping` 注释帧保活 |

- `seq`：进程内单调计数，只在实时扇出时打点（REST 历史/持久化 journal 不带）——SSE `id:` 行同源，EventSource 重连自动回传 `Last-Event-ID`，服务端从全局环形缓冲（1000 条窗口）补放缺口；进程重启缓冲清空，前端以 REST 历史合并兜底（`mergeFeedEvents` 按键去重）。
- 事件形状与既有 `WSEvent` JSON 完全一致（新增可选 `seq` 字段）；事件清单见各节的「事件流事件」。慢客户端（发送缓冲满）直接断开——与原 WS 纪律一致；上限 50 并发连接。
- `WSEvent` 类型名保留（历史名称），前端 `store/events.ts`（原 `store/ws.ts`）经 EventSource 消费。`onmessage` 只接收匿名/`message` 帧；契约内其他命名类型必须通过 `addEventListener(type, handler)` 注册，新增事件类型时同步登记。对话 Guidance 不做周期轮询或发送后刷新，初始 REST 快照之外的增量与状态变化只由本流驱动。

**委派与计划的生命周期失效帧（2026-11-23 增补）**：

| 事件 | 触发点 | `result_meta` | 消费纪律 |
|---|---|---|---|
| `agent_queued` | agentsgraph 准入时并发槽已满 | `{agent_id, lane, status:"queued", limit, running, queued}` | 仅失效 `["instance-agents", id]`，不得据此拼装状态 |
| `agent_started` | 准入取得并发槽 / 队首提升 | 同上（status:"running"） | 同上 |
| `agent_completed` | 子 agent 正常结束 | 同上（status:"completed"） | 同上 |
| `agent_failed` | 失败或父扫描停止 | 同上（status:"failed"） | 同上 |
| `plan_updated` | 引擎/模型计划发生实际变化（指纹变化才发） | `{source, total, completed, skipped, unfinished, plan_revision}` | 仅失效 `["scan-plan", id]` |

> 权威仍是 `GET /api/instances/{id}/agents` 与 `GET /api/scans/{id}/plan`（宪章 §5.1/§6：事件是审计投影，不是状态）。`safeSend(...,0)` 可丢弃，因此任何面板都不得把"没收到帧"当作"状态未变"。

## 1. Projects

```ts
interface Project {
  id: string;            // uuid
  name: string;
  slug: string;          // URL/切换器用，唯一
  scope: ProjectScope;
  created_at: string;
  archived_at?: string;  // 存在即归档（列表默认不含归档）
  task_count?: number;   // 列表响应附带
  active_runs?: number;  // 列表响应附带
}

// 一个授权边界，两层判定（2026-11-22 增补）：
//   targets/allow_ports → dispatch 层：运行启动时可以点名哪些目标（只写主机名）
//   fence               → 运行时层：受审批管辖的调用（命令 / 高危 HTTP / 代理侧
//                          子进程流量）可以真的碰到什么。越界调用不进入待审队列，
//                          直接以 rejected + decided_by="fence" 落审计行。
interface ProjectScope {
  targets: string[];
  allow_ports?: number[];
  fence?: {
    enforce: boolean;                 // 启用后至少需要一个 targets 条目
    deny_targets?: string[];          // 覆盖 targets 的排除主机（主机或 *.domain，不带端口）
    allow_http_methods?: string[];    // 允许的写方法（大写标准方法）；空 = 不限制
    deny_command_patterns?: string[]; // 命中即硬拦截的命令子串（大小写不敏感）
  };
}
```

| Method+Path | 请求 | 响应 |
|---|---|---|
| GET `/api/projects` | — | `{ projects: Project[] }` |
| POST `/api/projects` | `{ name, scope: { targets: string[], allow_ports? } }`（slug 服务端从 name 生成，冲突加后缀，name 无 ASCII 字符时 slug 兜底为服务端生成的唯一 id（显示名 name 本身保留原文）；**scope.targets 只写主机名**——自带 `:port` 的条目返回 400，端口统一由 allow_ports 控制；**allow_ports 为空 = 不限制端口**；非空时同时约束显式端口与 `http`/`https` 默认端口，无 scheme 裸主机要求 `80` 和 `443` 都可放行；`*.example.com` 仅匹配子域，不匹配 apex） | `Project` (201) |
| GET `/api/projects/{id}` | — | `Project` |
| PATCH `/api/projects/{id}` | `{ name?, scope?, archived? }` | `Project` |
| DELETE `/api/projects/{id}` | — | `{ status: "deleted" }`（仅允许无活跃 Run 的项目；否则 409） |
| GET `/api/projects/{id}/scope` | — | `{ project_id, scope, fence, problems: string[], active_runs, approval_mode: "enforce"\|"off", fence_bound, warning? }`（`fence_bound = fence.enforce && approval_mode=="enforce"`——围栏是配置还是真的在拦，由它回答） |
| PUT `/api/projects/{id}/scope` | `ProjectScope`（原样替换） | 同上视图；写入校验失败 400 `{ error, problems: string[] }`（围栏启用却无 targets、deny_targets 带端口/协议、方法不在标准集合、命令片段超限）；围栏在 `active_runs > 0` 时也允许改，响应带 `warning`（对后续审批立即生效，已在待审的行保持原判） |
| POST `/api/projects/{id}/scope/validate` | `{ scope?: ProjectScope, targets?: string[] }`（scope 缺省取已存值） | `{ ok, problems, entries: [{ target, in_scope, reason? }], scope, fence, fence_bound, approval_mode }`——不落库；`entries` 同时回答 dispatch 判定与围栏判定（任一不通过即 `in_scope=false`） |

## 2. Tasks

```ts
interface Task {
  id: string;
  project_id: string;
  name: string;
  goal: string;                      // 目标陈述（进入 system prompt / 循环 Goal）
  instruction?: string;              // 自定义指令（现 ScanRequest.instruction 同义）
  phase_policy?: {
    phases?: number[];               // 22 阶段子集
    severity_filter?: string[];
    scan_mode?: "single" | "wildcard" | "dast";
    intensity?: "active" | "passive";
  };
  schedule?: {                       // 存在即周期任务；触发产生新 Run
    interval: "hourly" | "daily" | "weekly" | "monthly";
    time?: string;                   // "HH:MM"
    timezone?: string;               // IANA
  };
  context_paths?: string[];          // 上传物路径（复用现有 upload-context）；Run 启动时全部与请求 scan_context 合并
  audit_mode?: "inherit" | "off" | "enforce"; // 任务级 HITL/审计开关（缺省 inherit）
  //   inherit = 跟随进程默认 XALGORIX_APPROVAL_MODE；
  //   off     = 仅审计：受管调用照常执行，留在 tool_call 审计轨迹，不建审批行；
  //   enforce = 命令（terminal/python/port_scan/browser 危险操作）与经 MITM 代理的
  //             高危 HTTP 写请求创建审批行并阻塞，直到操作者批准/拒绝（超时=拒绝）。
  //   生效时点在 run 派发：模式快照到该 run 的 ScanContext，任务/全局后续修改不影响在途 run。
  objective?: {                     // 结构化目标；缺省 = "未结构化"（不推断、不折算达成度）
    kind: "engagement" | "capability" | "finding_class" | "coverage";
    statement: string;              // 目标陈述（人工输入或 goal 分析改写）
    criteria?: {
      id?: string;                  // 服务端归一为 C1/C2…；客户端指定无效
      kind: "finding_class" | "capability" | "coverage";
      vuln_class?: string;          // kind=finding_class 必填（用会出现在 finding 标题里的小写词组，如 "sql injection"）
      capability?: string;          // kind=capability 必填（如 shell）
      verify_by: "deterministic" | "verifier" | "manual";
      required?: boolean;           // 缺省 true；required 且 verify_by≠manual 的判据未达成时 run 不主动收工（R-goal）
    }[];
    constraints?: string[];         // 操作者 may/may-not 规则（goal 分析从 goal 文本抽取）；只收窄不放大，随 objective 进独立提示词小节
  };
  created_at: string;
  updated_at?: string;
  archived_at?: string;
  // 列表/详情响应附带的投影统计（冷路径）：
  run_count?: number;
  last_run_at?: string;
  last_run_status?: string;
  last_run_completion?: "full" | "partial" | ""; // 终态读法："full" = 双图走完（已完成）；"partial" = 有界/物理停止（未达成意图）。没有 "failed" 读法
  finding_count?: number;
  content_changed_since_run?: boolean; // 任务可运行内容（goal/instruction/objective/phase_policy/context_paths/llm_chain/active_llm）相对上次派发是否变过；false = 这份内容已跑过，UI 不得再给裸「运行」，编辑内容后翻回 true。改名/改定时/改 audit_mode/归档不改变它
}
```

> **任务级出口代理（2026-11-24）**：`Task.proxy`（以及 POST/PATCH 的 `proxy` 字段）取值 `""`（跟随全局池）/ `"direct"`（显式直连）/ `"N"`（全局池第 N 项，1-based）。保存时按**当前**池校验：越界索引 400，`direct` 在 `XALGORIX_PROXY_REQUIRED` 下 400；派发时再次校验（池可能已被热更改过）。Run 启动时把选择快照为进程内 per-run egress（`scanctx` id 为键）；运行中的 Run 不受任务后续修改影响。**出口与流量记录是两层**：任务选的是出口池；MITM 开启时由本机 MITM 转发到该出口，MITM 关闭时 `httpclient`/`send_request`/`terminal`/`python`/`portscan`/`browser` 直接使用该出口（required 语义不变，失败即 fail-closed），host 网络的 toolbox 容器把该出口注入 `HTTP(S)_PROXY`；bridge 网络且 MITM 关闭时容器需 `XALGORIX_TOOLBOX_PROXY_URL` 指向可达地址。`GET /api/proxies` 返回 `{enabled, required, entries:[{index,label}]}`（label 已掩码）。全局代理设置（USE_PROXY/PROXY_URL/PROXY_FILE/ROTATION/REQUIRED/TRAFFICPROXY*/TLS_SKIP_VERIFY）保存后**热生效**：重新初始化上游池与 MITM 管理器，非法组合整单拒绝并回滚到旧策略。
>
> > **任务热更新（2026-11-24）**：对**活跃 Run** PATCH 任务时，持久化后立刻把变更投影到运行期，不重启 Run：写一条 `source=human` 的 InfoItem + `hint` 节点（`purpose` 由任务更新承载），更新 planner / worker pool 的 live brief 并 `Notify` 重规划（**不 kill/steer 在途 worker**），向主 Agent 注入结构化“任务配置已更新”消息，热换模型链 endpoints（下一次 LLM 调用生效），更新该 Run 的出口钉选并**强制关闭该 Run 的全部隧道**（下一次连接走新出口），同步 `AuditMode`。`name`/`schedule` 只持久化、不通知。`run_revision` 覆盖 goal/instruction/objective/phase_policy/context_paths/llm_chain/active_llm/**proxy**。
>
> **流量记录与出口分层（2026-11-24）**：`XALGORIX_TRAFFICPROXY`（设置分组「流量记录（MITM）」）只管拦截/留证/审批 wire gate；出口由「出口代理」分组（USE_PROXY/URL/FILE/ROTATION/REQUIRED/EGRESS_ROUTER）管理。`XALGORIX_EGRESS_ROUTER=auto`（默认）时，记录关闭 + 配了出口会在平台内启动 **forward-only** 路由监听（复用 per-run listener，不拦截不记录），任务换代理下一次连接生效、容器无需重建；`off` 保留旧语义（记录关=直连，容器代理改动下次 Run 生效）。记录关闭时仍累计 `run_egress_usage`（`(scan_id, proxy_label)` 聚合 connections/requests，掩码 label，无报文/凭据），由 flusher 周期性写 PG。
>
`GET /api/pool` 追加 `events:[{at,reason,from,to,cooldown_until}]`：最近 20 次进程级 key 自动切换（fingerprint，无密钥），供实时动态 LLM 面板展示；`reason ∈ rate_limit|quota_exhausted|overloaded|auth`。
>
> **任务运行闸门（2026-11-24）**：一个任务只挂一个 Run。派发被接受时后端把可运行内容的修订值（SHA-256，覆盖 goal/instruction/objective/phase_policy/context_paths/llm_chain/active_llm）记进 `tasks.context.run_revision`（不动 `updated_at`），读路径据此产出 `content_changed_since_run` 与 `last_run_completion`。前端据此决定是否显示「运行」：终态且内容未变 → 隐藏运行按钮，状态章读作「已完成」或「未达成意图」；改内容后按钮回来，点击即重派同一 Run 热续跑。该标记是 best-effort（写失败按"内容已变"处理，只放开不锁死）；`POST /api/tasks/{id}/runs` 本身不加 409，定时调度与显式 API 调用不受影响。

| Method+Path | 请求 | 响应 |
|---|---|---|
| GET `/api/projects/{id}/tasks` | — | `{ tasks: Task[] }` |
| POST `/api/projects/{id}/tasks` | `{ name, goal, instruction?, phase_policy?, schedule?, audit_mode? }` | `Task` (201) |
| GET `/api/tasks/{id}` | — | `Task` |
| PATCH `/api/tasks/{id}` | 同 POST 字段（部分更新） | `Task` |
| DELETE `/api/tasks/{id}` | — | `{ status: "deleted" }` |
| POST `/api/tasks/{id}/runs` | `ScanRequest` 的部分覆盖（targets 缺省取项目 scope.targets；任务 `context_paths` 与请求 `scan_context` 全部合并；其余字段同现有 `/api/scan`） | `{ status: "pending", run_id, instance_id }`（instance_id 兼容现有实例页/scan-detail 直链；`status` 为真实 dispatch 状态，与 `POST /api/scan` 一致——入队后为 `pending`，admission 授予并发槽后才转 `running`，权威状态轮询 `GET /api/instances`）。**任务:运行 = 1:1（2026-11-22 增补，修订本行此前的"续驱=发起新 Run"语义）**：任务永久只挂**一个** Run——活跃（`pending/running/paused`）时返回 `409 { error, run_id, instance_id }`（守卫与调度触发同规则不变）；**终态 Run（stopped/failed/finished/saved）再次 POST = 重派同一 instance**：`run_id`/`instance_id` 与上次完全相同（挂载复用，永不新建 Run 记录），同一 run 上 notes/ledger/execution checkpoint 连续，agent 按**热启动降级阶梯**续跑（eino checkpoint → 事件 journal 对话转录投影 → resume briefing + stop_reason 兜底；转录是提示词上下文，**状态权威 = 进程内内存 + PG 持久态**（R6.1 起，REDESIGN D12/宪章 v3.0：转录从 PG `run_events` 读该 run 全历史，ledger/notes 按 instance 连续性读，D11 修订注记同 REDESIGN §9）。续驱方式 = PATCH task 的 goal/instruction 后重发 POST runs（新意图覆盖旧 instruction）。stopping 中的 Run 返回 409（等终态再重派）。定时调度触发保持每周期一条 Run 记录（1:N，后续里程碑收敛） |
| GET `/api/tasks/{id}/runs` | `?page&size` | `Paginated<Run>` |
| GET `/api/tasks/{id}/findings` | — | `FlatFinding[]`（任务级累积投影，跨 Run 按 target/endpoint/title/severity 去重，最新 Run 胜出；读 PG `run_findings` 冷路径（R6 起 DSN 必需），不设 500 Run 上限；旧文件分页遍历仅存留为无库测试模式的兜底） |
| GET `/api/tasks/{id}/objective` | — | `TaskObjectiveView`（只读：结构化目标 + 判据状态 + 达成度 + 所处环节 + 缺口 + 可调度假设）。任务无目标时 `structured=false` 且不折算达成度 |
| POST `/api/projects/{id}/analyze-goal` | `{ goal, instruction? }` | `{ proposal: { statement, criteria: [{ kind, vuln_class?, capability?, verify_by, required, rationale? }], constraints?, notes? } }`。**R-goal 纯预览端点（2026-11-22）**：一次 LLM 意图分析，把模糊 goal 改写为可验证陈述 + 可判定判据草案（判据上限同 objective），**不落盘**——任务表单「分析目标」按钮消费后由操作者编辑/确认保存。空 goal → 400；项目不存在 → 404；LLM 失败/无法解析 → 502（body 携带原因，表单不阻塞手填）；服务未装配 LLM → 503。run 启动时若任务仍无结构化目标，引擎自动执行同一分析并持久化（第 0 轮，`XALGORIX_GOAL_ANALYSIS` 默认 on；超时/失败回退原始 goal 直跑），伴随 `goal_analyzed` 事件流事件（ResultMeta: `task_id` / `project_id` / `status=analyzed\|skipped` / `statement?` / `criteria?`） |

```ts
interface TaskObjectiveView {
  objective: Task["objective"] | null;
  structured: boolean;                       // false = 目标未结构化
  report: {
    structured: boolean;
    criteria: {
      id: string; kind: string; vuln_class?: string; capability?: string;
      verify_by: string; required?: boolean;
      state: "met" | "unmet" | "undetermined" | "manual_pending";
      reason?: string;                       // 为什么是这个状态
      evidence_finding_id?: string;          // 判定依据的已接受发现
      evidence_tag?: string;                 // verified / exploit-proven / needs-manual-verification
    }[];
    met: number; unmet: number; undetermined: number; manual_pending: number;
    attainment: number;                     // 仅展示，不参与任何判定
  };
  runs: { run_id: string; scan_id: string; status: string; report: TaskObjectiveView["report"] }[];
  ring: "observe" | "believe" | "obligation" | "act" | "evidence" | "verdict" | "close" | "unknown";
  ring_basis: string;                       // 派生依据（事实，不臆造）
  gaps: string[];
  schedulable: { id: string; vuln_class?: string; endpoint?: string; title?: string; confidence: number; voi?: number }[];
}
```

> 判据语义：`finding_class` 只认已接受且带 `verified` / `exploit-proven` 标签的发现；仅有 `needs-manual-verification` 记为未达成，无候选记为不可判定。`capability` 的 `verify_by=deterministic` 还要求带具体利用证据；`verify_by=manual` 恒为待人工确认。`coverage` 比较已完成/计划任务计数。判据状态与 finding 列表解耦，**不存在"部分算成功"**。达成度**不阻断收工**，也不改变任何 finding 标签。

```ts
interface Run {
  id: string;
  task_id: string;
  legacy_scan_id?: string;   // 旧扫描目录关联（R6 起记录权威在 PG run_scan_records/runs）
  instance_id?: string;      // 活跃/历史实例，前端用 /scans/{instance_id} 直链
  mode: string;              // 同 scan_mode
  status: string;            // running/pending/paused/finished/stopped/failed/saved/missing
                             // missing = 挂载存在但扫描记录已不存在（目录与持久行均无）且无待调度证据；
                             // 不计入 active_runs，也不阻止项目删除
  started_at?: string;
  finished_at?: string;
  vuln_count: number;
  total_tokens: number;
  trigger?: "manual" | "schedule" | "rerun";
}
```

> **scan-detail 反查提示（2026-11-22 增补）**：`GET /api/scans/{id}` 响应附加**仅响应字段** `run_id?` / `task_id?`（服务端按 instance_id 反查 run 挂载，读取时解析、不落盘；空 = 未挂载的前平台遗留扫描）。Run 详情页据此接入统一 HITL 通道：`task_id` 存在才渲染 guidance composer，缺省不渲染（不猜任务）。

## 3. Approvals（HITL，超时=拒绝）

```ts
interface Approval {
  id: string;
  project_id: string;
  project_name?: string;     // 列表附带（跨项目 OpsPulse 显示用）
  task_id?: string;
  task_name?: string;
  run_id?: string;
  instance_id?: string;      // 事件流/详情直链
  kind: "command" | "http";
  payload: {
    // kind=command
    command?: string;        // 完整命令行
    workdir?: string;
    // kind=http
    method?: string;
    url?: string;
    headers?: Record<string, string>;  // 密钥类 header 值打码
    body_excerpt?: string;   // ≤2KB
    reason?: string;         // 命中高危判定的原因（规则名/特征）
  };
  slice?: {                  // 语境切片（宪章 §4：审批单是切片的消费者）
    subject?: string;        // 主体（host/endpoint）
    purpose?: string;
    facts: { content: string; kind: "observed" | "inferred" | "human"; confidence?: number }[];
    contradiction_count?: number;
  };
  status: "pending" | "approved" | "rejected" | "timeout" | "cancelled";
  created_at: string;
  deadline_at: string;       // created_at + XALGORIX_APPROVAL_TIMEOUT；前端倒计时，归零显示 timeout
  decided_at?: string;
  decided_by?: string;       // "operator:<user>" | "timeout" | "system" | "fence"
  reason?: string;           // 拒绝/批准附言
}
```

| Method+Path | 请求 | 响应 |
|---|---|---|
| GET `/api/approvals` | `?status=pending\|decided\|all&project_id=&task_id=&page=&size=`（`decided` = approved/rejected/timeout/cancelled） | `{ approvals: Approval[], pending_total: number }`（`pending_total` 为**全项目**待审批数，供 OpsPulse） |
| GET `/api/approvals/summary` | — | `{ pending_total: number, nearest_deadline?: string }`（OpsPulse 专用轻量端点，5s 轮询） |
| POST `/api/approvals/{id}/decision` | `{ decision: "approved" \| "rejected", reason? }` | `Approval`（对非 pending 单返回 409） |
| POST `/api/approvals/batch-decision` | `{ ids: string[], decision: "approved" \| "rejected", reason? }` | `{ approved: number, rejected: number, skipped: number }` |

**事件流事件**（供事件流实时显示，前端同时以轮询兜底）：
- `approval_requested`：payload 为 `Approval`（pending）。
- `approval_decided`：`{ id, status, decided_by, decided_at }`。

前端在 LiveFeed 的 `TYPE_COLOR` 中已为 `approval_requested`/`approval_decided` 留色；事件流内嵌紧凑审批卡（行内决策 + 深链 `/approvals?focus=<id>`）。

**后端行为要点**（与 REDESIGN §5 对齐）：超时由服务端判定写 `timeout`；Run 停止时未决单批量 `cancelled`；同一 Run 未决上限默认 1。

**项目围栏（2026-11-22 增补，`decided_by="fence"`）**：项目 scope 的 `fence.enforce=true` 且 HITL 模式为 `enforce` 时，每一张审批请求在**进入待审队列之前**先过围栏：命令类按字面量抽取目的地（URL / host:port / IPv4 / 点号主机名，常见文件名后缀除外），HTTP 类按 method + URL 的 host:port 判定；`deny_targets` 覆盖 `targets`，`allow_ports` 非空时端口必须命中，回环地址不参与判定。越界调用不会等待人工决策——引擎立即以 `status=rejected, decided_by="fence"` 关闭该行（审计保留），并把 ⛔ FENCE DENIAL 交回模型；代理侧同一判定直接 403。**"approve & remember" 规则不能扩大边界**：围栏判定先于规则匹配。

**停止带 reason（2026-11-21 增补，4zreco 软取消语义）**：`POST /api/instances/{id}/stop` 请求体接受可选 `{ "reason": string }`（向后兼容空体）。reason 落 `stop_reason` 供引擎在下一 Run 的 briefing 中参考（人工转向的结构化反馈，对应 4zreco"软取消 → 规划者据此重规划"）。前端停止按钮已带 reason 输入弹窗。

## 3.1 Guidance — 人工在环插入意图（HITL-intent，2026-11-20 增补）

> 语义（宪章 §3/§8）：人工意图 = **source=human 的 InfoItem**，经区域点亮进入循环视野。对齐 4zreco 模式：**无乐观回显**——后端在 POST 返回前持久化该人工 turn 并广播；前端统一从服务端数据渲染。幂等靠客户端 `request_id`（同 id 重试不重复注入）。

```ts
interface Guidance {
  id: string;
  task_id: string;
  run_id?: string;        // 注入/排队时针对的运行
  instance_id?: string;
  message: string;        // ≤4000 码点
  created_by?: string;    // 服务端已验证会话的 username；开放模式/历史行可为空
  status: "injected" | "queued" | "consumed";
  // injected = 已注入运行中 agent 的下一轮对话（注入消息带稳定标记
  //   `[OPERATOR GUIDANCE gi:<id>]: `，id 即本行 id）；queued = 无活跃 Run，挂到任务，
  // 下次 Run 启动时作为 briefing 的一部分（source=human）进入点亮视野——
  //   briefing 并入无逐行标记载体，如实保持 injected，不自动翻转；
  // consumed = 引擎已消费：domain journal 观察者在 hypothesis/evidence 产出
  //   （记录 summary / InfoItem 文本）命中 `gi:<id>` 标记后由后端翻转。
  created_at: string;
  consumed_at?: string;
}
```

| Method+Path | 请求 | 响应 |
|---|---|---|
| POST `/api/tasks/{id}/guidance` | `{ message: string, request_id: string, instance_id?: string }` | `{ status: "injected" \| "queued", guidance_id: string, run_id?: string }`（重复 request_id 返回首次结果，`accepted: false` 标记） |
| GET `/api/tasks/{id}/guidance` | `?limit=` | `{ items: Guidance[] }`（倒序，最近在前） |

**事件流事件**：`guidance_injected` / `guidance_queued` / `guidance_consumed`，payload 为 `Guidance`。

**身份归因**（2026-11-22 增补）：`created_by` 由**服务端**从已验证 dashboard session 写入（JWT claims 的 username）；请求体**不收任何身份字段**，浏览器自报不信任。认证关闭（开放模式）与历史行为空串——空 = 无身份，不推断归属。会话视图的「仅本人」过滤（`created_by == 当前会话 username`）是客户端投影，认证开启时空身份行不展示为本人消息。`guidance_injected` 广播前服务端已持久化并更新对象状态（帧内 status 不会是过期的 `queued`）；`guidance_consumed` 只更新原行 `status/consumed_at`，不产生新 turn。

**注入语义**（后端实现要点）：
- 目标 Run **running** → `SendMessage` 把消息写入 pending-injection 队列（`internal/agent/agent.go`），`BeforeTurn` 在下一个模型轮把队列排进 ADK turn nudge——即「下一轮 user turn」，`status=injected`。Eino 重编排后 `a.messages` 只是 Run 启动种子，run 开始后没有读者；2026-11-22 修复前的注入是死信（消息进了无人消费的缓冲，R2a consumed 闭环因此静默失效）。
- 目标 Run 暂停/停止/无 Run → `status=queued`，Run 启动时并入 briefing（等价 4zreco"暂停后发消息继续"的 xalgorix 变体：不必先暂停，排队即可）；
- `message` 超过 4000 码点 → 400；`request_id` 缺失 → 400；
- **仪表盘 composer 全量统一（2026-11-22 增补）**：任务详情会话页、`/chat` 与 Run 详情页（scan-detail）的意图 composer 全部走本端点；仪表盘前端已不再消费 `/api/chat` 旁路（该端点保留为未文档化的机器遗留面）。Run 详情页按 scan-detail 反查提示拿 `task_id`，未挂载扫描不渲染 composer；三处 composer 共享同一 request_id 幂等原语（`webui/src/lib/guidance.ts`）。
- **consumed 翻转闭环**：injected 行由 domain journal 观察者（`internal/web/guidance_observer.go`）闭环——引擎产出（hypothesis/evidence 记录的 summary、InfoItem 文本）引用 `gi:<id>` → `UpdateGuidanceStatus(consumed)` + `guidance_consumed` 事件流事件；briefing 并入路径无逐行标记载体，如实保持 injected（操作者从 injected 即可理解该 turn 已带入上下文）；store 失败只留日志，观察者永远 fail-open。

### 3.1a 旁路提问（2026-11-22 增补，`internal/web/chat_questions.go`）

对**运行中**的 Run 提出一个不经扫描主循环的问题：独立、无工具的一次 LLM 调用，上下文 = 提问时刻的扫描快照（实例事实 + 最近事件尾 + findings，`writeScanChatSnapshot`），回答以 journal 事件回流转录。4zreco 对话页 `/btw` 旁路语义的 xalgorix 变体。

| Method+Path | 请求 | 响应 |
|---|---|---|
| POST `/api/tasks/{id}/questions` | `{ message: string, request_id?: string, instance_id?: string }` | 202 `{ status: "pending", accepted: true, run_id?, instance_id?, request_id? }`；同一 Run 已有一条在途 → 409（不排队，操作者重试）；目标归属校验同 guidance（跨任务 instance → 400） |

- **回答的传输通道就是转录**：`chat_question`（human 行）与 `chat_reply`（assistant 行，`role:"chat"`）写进 run journal 并经 SSE 广播；`chat_reply` 失败时 `error` 非空、`result_meta.status="failed"`，转录如实渲染失败行（前端本地化「回答失败」框），从不静默。响应体不带回答——零乐观回显，转录即传输。
- **超时 120s**（`questionAnswerTimeout`，对齐 goal 分析预算）；每次调用现建 client（catalog → legacy resolver 决策序与 `/api/scan` 一致），尊重调用方 deadline。
- **权威边界**（宪章 §3.5）：回答是模型自述 = 最低等级，只读快照投影，不进 InfoItem 判定路径、不构成第二执行通道（无工具、无状态写入）；提问本身不是 guidance 行，不参与 injected/queued/consumed 生命周期。
- **前端语义**：运行中 Composer 显示「提问 | 指令」切换，默认提问（保证有回应、不消耗扫描轮次）；指令模式即本节 guidance 端点；空闲/终态 Run 维持 guidance 单通道不变。SSE 命名事件表（`NAMED_STREAM_EVENT_TYPES`）与转录桥（`TRANSCRIPT_EVENT_TYPES`）已收录 `chat_question` / `chat_reply`。
- **转录 phase 字段**（同批增补）：`TranscriptItem.phase?` 携带工具事件的推断方法论阶段（`current_phase`），前端在阶段变化处插「阶段 N」分隔行，让上一轮终态与新一轮工作在视觉上分开（实例槽跨 scan 轮复用时，转录是跨轮 journal 的拼接）。
- **诚实流式指示**（同批增补）：转录页「实时流式中…」改为跟随实例真实事件年龄（`use-instance-activity`，5s 窗口），停滞时显示 amber「已 {duration} 无新输出」，本页未观测到事件时显示中性等待行——running 状态投影不再冒充流式。

### 3.1b 主 Agent 意图提案 — 确认后写入（2026-11-23 增补，`internal/agent/main_chat.go` + `internal/web/handlers_intent_proposals.go`）

主 Agent 不再把对话消息直接写成 intent。具体工作先由 `propose_intent` 形成**草案**（`proposal-<ts>-<seq>`），草案只存在运行中 Agent 的进程内存里，**不进入探索图**；操作者确认后才由引擎写入 `origin=human` 的 intent。

| Method+Path | 请求 | 响应 |
|---|---|---|
| POST `/api/conversations/{instanceID}/intent-proposals/{proposalID}/confirm` | 空体 | 200 `{ status:"confirmed", proposal_id, task_id }`；草案不存在/已裁决或 Run 不存活 → 409 |
| POST `/api/conversations/{instanceID}/intent-proposals/{proposalID}/reject` | 空体 | 200 `{ status:"rejected", proposal_id }`；同上 409 |

- **确认路径**：转录卡片点击走上述端点（确定性，不经模型）；文字路径是主 Agent 的 `confirm_intent` 工具，仅当草案来自**更早的回合**且当前用户消息是明确肯定（确认/执行/开始/派发/confirm/go ahead…）时才被接受。同一回合内提案 + 确认一律拒绝。
- **写入语义**：确认时复用原直接插入的全部校验（target 必填、control-plane host 不得作 web/container 目标、phase 1–22、依赖解析），payload `origin=human`、`task_id` 在提案时预生成，`UpsertNode` 的 `unit_key` 保证幂等。
- **事件**：`intent_proposed`（`result_meta`: `proposal_id/task_id/title/target/target_kind/phase/vuln_class/endpoint/notes/status=pending`）与 `intent_proposal_resolved`（`proposal_id/task_id/status=confirmed|rejected`）写 run journal 并经 SSE 广播；转录把同一 `proposal_id` 折叠成一张卡，后到的 resolved 只更新状态。
- **主回复投影**：主 Agent 的 `message` 事件带 `result_meta.surface="main"`；转录映射 `role="main"`，人工投影（`?human=1`）包含它和 proposal 卡，worker 叙述仍留在完整转录。
- **hint 边界**：只有 **queued** guidance（无活跃 agent、留给下次 Run）自动投影为 active hint；注入到主 Agent 的 live guidance 不再自动成为 hint，否则 planner 会绕过确认直接生成同一 intent。主 Agent 的 `add_hint` 是上下文类提示的唯一即时写入路径。
- **生命周期**：草案随 Run 结束/进程重启消失，端点如实 409，不伪造确认；审计回合（`确认派发意图：…` / `放弃意图提案：…`）在确认成功后作为 injected guidance 行落库并广播 `guidance_injected`。

### 3.1c 工作单元泳道 — 一个 intent 一个可见 worker（2026-11-24 增补，`internal/web/handlers_agents.go` + `chat_transcript.go`）

`GET /api/conversations/{id}/transcript` 新增可选 `?unit=<intent_id>`：返回**该工作单元所有派发实例**的事件并集（重派/重试/租约回收追加到同一条 lane），过滤发生在分页之前，因此 unit 自己的历史就是分页权威。`?agent=<agent_id>` 保留为 legacy 单实例泳道；两者同时出现时 `unit` 优先。

- **投影契约**：`GET /api/instances/{id}/agents` 与 `GET /api/tasks/{id}/workers` 的 `units[]` 是可见 worker 行（`unit_id/title/state/status/agent_ids/agent_status/claimed_hypotheses`）；`state` 是探索图 intent 状态的原文，`status` 是 UI 词表映射（open→queued、running→running、paused→paused、done→completed、blocked/exhausted/stopped→failed）。`agents[]` 保留为实例级明细，legacy `spawn_agent`（无 unit）仍按 agent 一行。
- **unit → agent 解析**：投影优先读 `run_workers.unit_id`（migration 0029，生命周期边界写入），live run 再并上 agentsgraph 内存快照；图不可用时 `units` 为空，消费方回退 `agents`，worker 列表不会整体 404。
- **前端深链**：unit 行写 `?agent=unit:<unit_id>`（复用既有 `?agent=` 通道，不新增 URL 语义）；转录请求发 `?unit=`，主区头部状态取 unit 的图状态。

## 3.2 项目维度过滤（overview 项目化，2026-11-20 增补；运行页接入 2026-11-22）

现有端点追加**可选** `project_id` 查询参数（缺省 = 全部，兼容旧行为）：

- `GET /api/instances?project_id=` → 只返回该项目的实例
- `GET /api/findings?project_id=` / `GET /api/findings/summary?project_id=` → 项目内发现/汇总
- `GET /api/scans?project_id=`（分页版同理）

前端 overview 页与**运行页（/scans，2026-11-22 起）**均按当前项目（`useActiveProjectId`）带参查询；运行页此前未带参导致跨项目运行泄漏进列表，已修复。

**实例容器投影（只读，2026-11-23 增补）**：`GET /api/instances` 的每个实例可带可选 `container` 对象（`{project_id,state,container_id,image_digest,profile,desired_state,reconcile_status,last_error,observed_at}`）。`state` 只来自容器运行时（`ListProjects`）；`desired_state`/`reconcile_status`/`last_error` 只来自 PG `toolbox_projects`，两者分开展示、互不推导。列表每请求最多一次运行时枚举 + 一次批量期望态读取，绝不做逐实例 inspect；运行时不可用时字段整体省略（不伪造 `absent`）。native 模式不返回该字段。

## 3.3 信息空间（R5，只读）

项目级 InfoItem 激活入口。`project_id` 必填；`context`/`association` 的 `scan_id` 必须属于该项目。项目过滤通过 platform runs 的 `instance_id`/`legacy_scan_id` 并集实现，**不向 info_items 增加 project_id**。

```text
GET /api/info/region?project_id=&task_id=&scan_id=&layer=&subject_kind=&subject=&purpose=&nature=&kind=&after=&before=&limit=&cursor=
→ { available: true, items: InfoItem[], truncated, has_more, next_cursor?, incomplete, gap_reason? }

GET /api/info/heatmap?project_id=&task_id=&scan_id=&subject=&subject_kind=&purpose=&q=&type=&after=&before=&limit=
→ { available: true, source: "graph", types: ["observation","inference","belief","intent","fact","finding"],
    rows: [{ subject, subject_kind?, total, counts: {observation,inference,belief,intent,fact,finding}, last_observed? }],
    truncated, subject_total, unlabeled, incomplete, gap_reason? }

GET /api/info/relations?project_id=&task_id=&scan_id=&after=&before=&limit=
→ { available: true, source: "graph",
    edges: [{ src_subject, src_kind?, dst_subject, dst_kind?, rel,
              category: "chain"|"info", count, src_title?, dst_title?, last_at? }],
    truncated, incomplete, gap_reason? }

GET /api/info/context?project_id=&scan_id=&subject=&subject_kind=&since=&until=&limit=
→ { available: true, items: InfoItem[], truncated, incomplete, gap_reason? }

GET /api/info/association?project_id=&scan_id=&ids=id1,id2&edge_types=supports,...&max_depth=1..3&limit=
→ { available: true, hits: [{ item: InfoItem, path: string[], rels: string[], depth: number }], truncated, incomplete, gap_reason? }
```

- **作用域收窄（IG1b / 热力图增补）**：`task_id` 把白名单收窄到该任务的 run 集（跨项目 404）；`scan_id` 把白名单收窄到单个 run（不属于当前项目/任务则为 **400**）。`layer=aggregate|atom|all` 默认 all；region 的 `kind` 是聚合节点 kind 逗号列表（仅图可用；冷路径收到 `kind` 返回 **400**，不得静默忽略）。
- **`heatmap`（2026-11-24 增补；同日扩为项目热力图）**：区域点亮的聚合形态，只返回计数与坐标，不返回正文。阶段固定为 `observation`（原子 observed）/`inference`（原子 inferred，且必须有 `supports`，与 Region 的可激活规则一致）/`belief`/`intent`（行动）/`fact`/`finding`（聚合节点）。行按 `total DESC, subject ASC, subject_kind ASC` 确定性排序；`limit` 是**主体行数**上限（默认 40、最大 200，不是条目数）；`q` 对 subject 做大小写无关子串过滤（项目地图搜索，过滤发生在行上限之前）。`truncated` 表示主体总数超过行上限；`subject_total` 是过滤后的真实主体数；`unlabeled` 是无主体坐标、无法上图的条目数。无 graph reader 时 **503** `{available:false, reason:"heatmap_unavailable"}`，不得返回空图冒充无数据。前端 `/info` 用同一响应画项目星云：距离/大小/颜色 = 信息量。
- **`relations`（2026-11-24 增补）**：主体级关系聚合，把同一 run 内的 `info_space_edges` 两端 join 到 `info_space_items`，**排除同主体边**，继承 project/task/run 白名单、时间窗与"无支持推断不上图"的激活规则。`category`：`chain` = `spawns/derived_from/yields/proves/supports/covers/depends_on`（动作/证据链），`info` = `references/contradicts/same_session/same_subject/materializes`（信息关联）；未知 rel 归 `chain`（未来生产者不被隐藏）。`limit` 默认 400、最大 800（边行数，不是条目数）；排序把 chain 放前面再按 count 降序，保证截断时优先保留攻击链。`src_title/dst_title` 是两端对象最新摘要的引用，不复制正文。**没有 pivot/tunnel 一等关系**：路径是真实关系链，`rel` 方向就是存储方向，不得反推或改名。无 reader 时 **503** `{available:false, reason:"relations_unavailable"}`。

- `limit` 默认 50、上限 200；关联深度 1..3；`truncated`/`incomplete`/`gap_reason` 必须原样透传。
- **`hits[].rels`（2026-11-22 增补）**：长度恒为 `len(path)-1`，第 `i` 项是 `path[i] → path[i+1]` 这条边的真实关系类型（`supports|contradicts|references|same_session|same_subject`）。遍历方向不影响类型语义。字段为加性可选：缺失时前端只能中性描边，**不得**用 `item.supports` 反推制造假边。
- **Region 游标分页（2026-11-22 增补）**：`after`/`before` 是 `observed_at` 的严格时间边界（语义不变，不可当分页用——同一时刻并列行会跨页丢失/重复）。分页用 keyset：`next_cursor` 形如 `<observed_at RFC3339Nano>|<id>`，把上一页的 `next_cursor` 作为 `cursor` 传回即取下一页；排序固定 `observed_at DESC, id ASC`，游标条件为 `observed_at < cursor_at OR (observed_at = cursor_at AND id > cursor_id)`。`has_more` 表示还有下一页；非法 `cursor` 返回 **400**（不是 503）。`RegionResult` 不提供全量总数，前端不得用估算值冒充。
- **`context` 维持单 Run 语义（2026-11-22 明示）**：`scan_id` 必填且须属该项目，返回该 Run 内固定维度的混合信息视图；跨 Run 语境不是本端点能力，前端不得呈现为跨 Run 切片。
- 无 PostgreSQL 冷路径时返回 **503**：`{ available: false, reason: "cold_path_disabled", error }`，不得用空数组冒充“无数据”。
- 有冷路径但读取失败时 `503` + `reason: "cold_path_unavailable"`。
- InfoItem 形状沿用 `internal/infoitem.Item`：`id/scan_id/source_seq/nature/coordinates/supports/confidence/coordinate_status/...`。
- 既有 `GET /api/scans/{id}/audit`（领域审计投影）与 `GET /api/scans/{id}/coldpath`（pending/applied/gap/failed/disabled）为同一信息空间的审计/同步状态来源，前端可复用。审计投影自 R8 起携带 `deviations: [{intent_id?, summary, actor, at, seq}]`（executor 计划偏离，只读）。

- **WorkUnit 投影（2026-11-23 增补）**：`GET /api/scans/{id}/units` → `{ scan_id, source: live|record|none, units: [{kind: task|hypothesis, id, subject, state, owner?, lease_until?, vuln_class?, phase?, expired?}], counters: {tasks_open, tasks_claimed, tasks_expired, hypotheses_queued, hypotheses_testing, hypotheses_expired} }`——intent 单位=plan task、belief 单位=ledger hypothesis 的统一派发视图（只读；认领协议见 docs/WORK_UNIT_DESIGN.md）。

- **安全链视图（2026-11-22 增补）**：`GET /api/scans/{id}/security` → `{ scan_id, blocks: [{ts, category, tool?, reason}], counters: {scopeguard|target_scope|phase|auth|activity|goal|ledger|report|benchmark|other}, truncated }`——守卫拦截事件从事件 journal 提取（⛔/⚠️ 标记分类：scopeguard 操作者机器守卫 / target_scope 目标边界 / phase 阶段工具面 / auth 认证前置 / activity 活动策略 / goal 收工义务 / ledger 假设精度 / report 报告门），newest-first、上限 200 条；守卫规则目录为静态内容由前端本地化渲染。命令历史见 `/events`，原始 HTTP 交互见 `/traffic`。

## 3.4 Approval Rules — 审批豁免规则（approve & remember，2026-11-21 增补）

> 语义（REDESIGN §5.2 R2a 注记）：把一次操作者裁决持久化为可复用的 allow 规则，**命令闸与 wire 闸共用查询**——同一意图最多问一次。规则是操作者裁决的持久化（溯源 `created_from_approval_id` + `created_by: "operator:<user>"`）；`created_from_approval_id` 随审批行删除置 NULL，规则本身存活。

```ts
interface ApprovalRule {
  id: string;
  project_id: string;
  project_name?: string;     // 列表附带
  kind: "command" | "http";  // 沿用审批 kind 词汇
  decision?: "allow" | "deny"; // v4.6：allow=豁免（缺省/空等价 allow）；deny=范围观测 Agent 自动拒绝
  match: {
    // kind=command
    program?: string;        // 程序 basename（"curl" 匹配 /usr/bin/curl）
    // kind=http
    method?: string;         // 精确、大小写不敏感
    host?: string;           // 精确或子域后缀（"target-x.com" 命中 api.target-x.com）
    path_prefix?: string;    // 路径前缀（"" = 任意）
    contains?: string;       // 共享收窄子串：命令文本 / URL+body，大小写不敏感
  };
  reason?: string;
  created_by?: string;       // "operator:<user>"
  created_from_approval_id?: string;
  created_at: string;
  expires_at?: string;       // RFC3339；匹配期判定，过期行仍列出（UI 显示状态）
  hit_count: number;         // 命中计数（运行数据：调审批默认值的依据）
  last_hit_at?: string;
}
```

| Method+Path | 请求 | 响应 |
|---|---|---|
| GET `/api/approval-rules` | `?project_id=&kind=` | `{ rules: ApprovalRule[] }`（新在前；过期行**不**过滤——状态由 UI 呈现） |
| POST `/api/approval-rules` | `{ project_id!, kind!, decision?, match!, reason?, expires_at?, created_from_approval_id? }` | `ApprovalRule`（kind/match 不协调 → 400；project 不存在 → 404） |
| DELETE `/api/approval-rules/{id}` | — | `{ ok: true }`（再次删除 → 404） |

**事件流事件**：
- `approval_rule_hit`（instance 域）：payload `Rule`——规则命中、调用无审批行放行的审计投影（"为什么这张单没有行"）；审批行本尊仍是状态权威。
- `approval_rules_changed`（dashboard 域）：创建/删除广播，多 Tab 提示刷新。

**匹配语义**（实现于 `internal/approval`，两层逐字复用）：
- 命令规则 = 程序名 + 可选 contains；规则的程序**绕过 nmap/curl 专项旗标检查**（规则信任 = 该程序旗标面信任，仅由 contains 收窄——严格窄于 `XALGORIX_APPROVAL_ALLOWLIST` 的等价放行）。其余段保持基础分类语义。
- HTTP 规则 = method + host + path 前缀 + 可选 contains。
- **硬边界：deny 形状永不被规则豁免**——`$()`/`<(`/重定向/`PATH=` 冒名/`system(` 等段形状即使规则命中也直接拒绝（规则只能豁免分类器已干净判定的 Require 类目，不能豁免"分类器看不见的东西"）。
- 失败姿态：store 缺失/读取失败一律回落正常审批流（读失败绝不自动放行）。

**范围观测 Agent（v4.6，`internal/web/scope_observer.go`）**：两层闸在项目围栏（fence）之后、人工挂起之前统一调用观测者。判定顺序 = 项目内 `decision=deny` 规则 → 项目内 `decision=allow` 规则 → 内置基线（`internal/approval` `DefaultScopeRules`）：批量删除 / 文件上传 / OSS 对象写 → **自动拒绝**（终态行 `decided_by="scope-observer"`，模型收到 ⛔ 文案）；目标自身 web 控制面写与批量导出 → 保持人工义务（不自动放行）。定时与非定时运行同一路径；store 缺失或读失败一律回落人工审批。

**预置规则**：服务启动、项目创建、项目 scope 变更时，按项目 scope 前 5 个 host 幂等写入基线 deny 规则（`created_by="preset:scope-observer"`，可见可删）；require 类基线只在内置分类器内生效，避免被“豁免”语义误放行。

## 3.5 Run 文件浏览器（只读，2026-11-22 增补）

> 单次 Run（scanDir）的过程产物/工作空间在前端的唯一查看入口。参考 4zreco Files tab：懒展开目录树 + 文本/图片预览 + 下载 + 目录 ZIP 导出。**全端点只读**，鉴权沿用 `authMiddleware`；scanID 解析复用 `resolveScanDirByID`；路径访问全部经 `os.OpenRoot(scanDir)`（symlink 逃逸拒绝，最终组件为 symlink 的条目列为 `link` 但拒开）。

| Method+Path | 请求 | 响应 |
|---|---|---|
| GET `/api/scans/{id}/files` | `?path=`（scanDir 相对 POSIX 路径，缺省 `.`） | `{ path, entries: [{ name, type: "dir"\|"file"\|"link"\|"other", size, mtime }], truncated }` — 单层列表，dirs-first，上限 1000 条 |
| GET `/api/scans/{id}/files/content` | `?path=`（必填） | `{ path, size, mtime, truncated, binary, content? }` — 文本内联上限 2 MiB；二进制（NUL + content-type 双嗅探）`binary:true` 无 `content` |
| GET `/api/scans/{id}/files/preview` | `?path=` | 图片字节（inline）— 扩展名白名单 **且** `DetectContentType` 须为 `image/*` 双校验；SVG/HTML 永不内联 |
| GET `/api/scans/{id}/files/download` | `?path=` | 附件流（`Content-Disposition: attachment`，RFC 5987 filename*） |
| GET `/api/scans/{id}/files/archive` | `?path=`（目录，缺省整个 scanDir） | 流式 ZIP — 预检上限 2000 文件 / 1 GiB 未压缩，超限 **413** 且不启动 ZIP；symlink 跳过 |

- 错误形状 `{"error": "..."}`：遍历/非 portable 路径 → 400；缺失 → 404；目录上 content/download、文件上 list → 400；归档超限 → 413；symlink 条目开读 → 400 `"symlinked entries cannot be opened"`。
- `mtime` 为 Unix 毫秒。前端 `webui/src/components/scan-files-browser.tsx`（Run 详情页「文件」tab），运行中当前目录 5s 轮询。

## 3.6 Run 账本投影（只读，2026-11-22 增补）

> hypothesis/evidence 快照的只读投影（任务详情「账本」tab，即 4zreco 探索链路的 xalgorix 原生形态）。状态权威 = PG `run_ledger_snapshots` 行（R6.1 起，宪章 v3.0 §5.1/REDESIGN D12——内存是运行时权威、PG 是持久权威，本端点读快照本身而非事件重放；`<scanDir>/ledger.json` 仅余 nil 后端测试模式与 pre-import 目录的读兜底）；严格只读，不参与漏洞判定。

| Method+Path | 请求 | 响应 |
|---|---|---|
| GET `/api/scans/{id}/ledger` | — | `{ scan_id, ledger_present, ledger_damaged, incomplete, note?, counts: {queued, testing, proven, rejected, blocked, exhausted}（六桶零填充）, hypotheses: Hypothesis[] }` |

- **Hypothesis/evidence 形状 = scanctx 原生 JSON**（`internal/scanctx/ledger.go`：id/title/vuln_class/target/endpoint/parameter/role/status/confidence/voi/assigned_to/next_action/attempts/evidence[]，evidence 含 nature observed\|inferred、source（引擎 verifier 名）、agent_id、finding_id）。
- **诚实标志**：`ledger_present=false`（无快照：pre-ledger 扫描或尚未认领任何假设）→ `incomplete=true` + note；`ledger_damaged=true`（快照存在但不解析）→ `incomplete=true` + note 且**不带部分假设**——损坏永不折叠为"干净空账本"。空但合法的快照读作 present+clean（跑过但没认领是真实状态）。
- 排序是**展示序**（活跃 testing/queued/blocked 在前、settled proven/rejected/exhausted 在后、组内 updatedAt 新者先），调度序（VOI 加权）是另一件事，不在此复刻。未知 scan → 404。

## 3.6a 计划投影 + Worker 池投影（只读，2026-11-23 增补）

> 修复两个"面板读事件"的缺陷：Planner 区域曾从 `build_plan`/`update_plan` 的 tool_call 反推计划（引擎自建计划不产生这些调用 → 区域常年为空），Workers 组曾从历史事件去重 `agent_id` 并把 run 级状态当每行状态（→ 两个 worker 只有一个在干活却都显示"运行中"）。两个端点都只读，都不是判定来源。

| Method+Path | 请求 | 响应 |
|---|---|---|
| GET `/api/scans/{id}/plan` | — | `PlanView`（见下；R7 起优先读 `v_plan_tasks`） |
| GET `/api/scans/{id}/units` | — | `{ scan_id, source: graph\|live\|record\|none, units: [{kind, id, subject, state, owner?, lease_until?, vuln_class?, phase?, expired?}], counters }`（R7 起优先 `v_units`） |
| GET `/api/scans/{id}/graph` | — | `{ scan_id, source: graph\|none, counters, nodes: [{...Node, payload_object?}], edges: [{src_id,dst_id,rel}], conformance: {ok, errors, warnings, truncated, first?, first_detail?, projection_missing?, projection_stale?}, partial }`；`kind ∈ goal\|intent\|belief\|fact\|finding\|hint\|digest`，`rel ∈ spawns\|derived_from\|yields\|proves\|supports\|covers\|depends_on` |
| GET `/api/scans/{id}/graph/validate` | — | `Report{scan_id, checked_at, ok, errors, warnings, violations:[{code,severity,kind?,unit_key?,node_id?,detail}], truncated?}`；只读运行期合规检查，错误码见 `docs/INFO_GRAPH_CONTRACT.md` §2；图不可用 503 |
| GET `/api/scans/{id}/assets` | — | `{ scan_id, source: assets\|none, items: [{id, project_id, kind, key, display_name?, first/last_info_item_id?, archived_at?, archived_by?, merged_into_id?}], total }` |
| GET `/api/projects/{id}/assets` | `?kind=`（可重复/逗号分隔）、`?task_id=`、`?run_id=`、`?q=`、`?include_archived=true`、`?limit=`（缺省 200，上限 1000） | `{ project_id, source: assets\|none, items: Asset[], total }`；默认隐藏 archived/merged 墓碑 |
| POST `/api/projects/{id}/assets` | `{kind, key, display_name?, info_item_id?}` | `Asset`（201）；身份按 `(project, kind, key)` 去重，InfoItem 仅作 provenance 引用 |
| GET `/api/projects/{id}/assets/{assetID}` | — | `{ asset, links: [{scan_id, task_id?, source?, created_at}], edges: [{src_id, dst_id, rel}] }`；未知资产 404 |
| GET `/api/projects/{id}/assets/edges` | — | `{ project_id, edges: [{src_id, rel, dst_id}] }`；确定性排序 `src_id, rel, dst_id`，含 `exposes`（host → endpoint，URL 观测写入） |
| POST `/api/projects/{id}/assets/merge` | `{source_id, target_id}` | `{status:"merged", project_id, source_id, target_id}`；links/edges 迁移到 target，source 留 `archived_at + merged_into_id` 墓碑；自并/已归档/跨项目 409/404 |
| POST `/api/projects/{id}/assets/archive` | `{ids: number[], archived: bool}` | `{project_id, updated, archived}`；merged 墓碑不可单独恢复（`updated` 不计） |
| — | 资产结构边（2026-11-24） | `exposes`（host → endpoint）由 `assetobs` 在同一 URL 观测中写入，是资产页「目标 → 路径」下钻的唯一依据；`resolves_to/hosts/serves/bound_to` 语义不变。资产页第一层只列目标（root_domain/subdomain/ip/service/app），路径在目标详情中展示；无父目标的路径单列「未归属路径」，不得猜归属。 |
| GET `/api/instances/{id}/agents` | — | `{ available, concurrency:{limit,running,queued}, agents: AgentEntry[], units: WorkerUnit[], partial_reason? }`。**`units` 是可见 worker 行**：一个 intent（工作单元）一行，`status` 取图状态（open→queued/running/paused/done→completed/blocked·exhausted·stopped→failed），`agent_ids`/`agent_status` 是执行明细（重派/重试聚合同一行）；`agents` 保留为实例级投影，legacy spawn（无 unit）仍按 agent 一行 |
| GET `/api/instances/{id}/container` | — | `{ instance_id, project_id, backend, available, reason?, actual?:{state,container_id?,image?}, desired?:{desired_state,reconcile_status,desired_gen,applied_gen,profile,net_profile,image_digest,container_id,last_error,created_at,updated_at}, tools:{available,profile,source,reason,items?:[{name,bin,category,purpose,capabilities}]}, observed_at }`。只读：不创建/启动/停止/调和容器；native 返回 `backend:"native", available:false`；`desired` 白名单**不含** `spec`（Env/代理凭据/mounts/socket 路径）。`tools` 从运行中容器的 `/opt/xalgorix/toolpack.yaml` 读取并按 profile 过滤，容器未运行则 `available:false` |
| GET `/api/tasks/{id}/workers` | `?page&size` | `{ items: [{ ...Run, workers: { available, concurrency, agents: AgentEntry[], units: WorkerUnit[], partial_reason? } }], total, page, size }`；per-run 视图与 `/agents` 同语义（`units` 为主，`agents` 为明细） |

```jsonc
// PlanView
{
  "present": true,
  "source": "engine" | "model" | "mixed" | "none",
  "plan_revision": "<surface fingerprint>",
  "surface_revision": "<same fingerprint, kept separate so staleness is visible>",
  "current_phase": 6,
  "counters": { "total": 12, "completed": 5, "skipped": 2, "unfinished": 5 },
  "tasks": [{
    "id": "test-sqli", "title": "Test SQLi", "phase": 6,
    "vuln_class": "sqli", "endpoint": "/search", "status": "pending|active|completed|skipped",
    "depends_on": ["recon"], "notes": "...", "disposition": "blocked_missing_auth",
    "origin": "auto|auto-plan|auto-plan-surface|llm", "prerequisite": false, "whole_target": false
  }],
  "partial": false, "partial_reason": "not_persisted|truncated|cold_path"
}

// AgentEntry
{
  "agent_id": "sub_1_1699999999999999999",
  "lane": "injection-serverside",
  "status": "queued|running|completed|failed",
  "task": "...", "target": "https://example.test",
  "started_at": "...", "completed_at": "...", "error": "...",
  "queued_position": 1, "partial_events": 3, "collected": false,
  "claimed_hypotheses": [{ "id": "h_...", "vuln_class": "sqli", "endpoint": "/search", "status": "testing" }]
}
```

- **plan 解析顺序（R7 起）**：探索图 `v_plan_tasks`（intent 节点，`unit_key=task_id`；owner/lease 来自图）→ live agent 的 `PlanSnapshot()` → 终态 `ScanRecord.PlanView` → 仅计数时 `partial:"not_persisted"` → `present:false`。图零行时回退旧路径以兼容迁移期 run。**绝不把"明细缺失"渲染成"没有计划"**。
- **units/graph/assets**：都优先读 PG 权威（`v_units` / `exploration_nodes+edges` / `task_asset_links`）；图不可用时 `graph_unavailable` 让 run 进入可恢复 pause，而不是用内存计划静默续跑。
- **agents 解析顺序**：`inst.agent.DelegationSnapshot()`（agentsgraph 内存状态）→ 终态读 `<scanDir>/execution-graph.json`（graph 自己写的 checkpoint，非第二套持久权威）；checkpoint 里的 `running/queued` 一律降级为 `failed`（进程/运行结束后不可能还在跑），并置 `partial_reason:"checkpoint"`。未知实例 → `available:false, partial_reason:"no_run"`，200 而非 404（面板需要中性态）。
- **`claimed_hypotheses`** 是 ledger 的只读 join（仅 `status=testing` 且 `assigned_to` 非空），回答"这个 worker 现在到底在做什么"。
- **`GET /api/tasks/{id}/workers`** 是任务级只读聚合：对任务内每个 run 复用上面同一 worker 投影（live agentsgraph → checkpoint），只返回委派过 worker 的 run。会话页左栏据此把**每个 run 的全部 worker（含 completed/failed）**常驻成行，历史不随实时输出消失；run 投影字段（title/status/时间）与 `/runs` 同源。
- **限额语义**：`limit = min(XALGORIX_MAX_CONCURRENT_AGENTS(默认3,上限10), 生命周期委派上限)`；`running + queued` 不会超过 `limit`。
- 只读：两个端点都不参与 finding 判定、闸门或收工结论。

## 3.7 情报（任务级聚合 + 操作者分诊，2026-11-22 增补）

> 凭据/线索/敏感数据三分情报（任务详情「情报」tab）。观测项由 `record_intelligence` 工具写入各 Run 的 `<scanDir>/intelligence.json`（热路径，`storage.WriteAtomic`，指纹去重）；任务级读**走 scan dir 聚合，无 DSN 完整可用**（宪章 §9）；分诊是操作者标注，独立存（0009 `intel_triage` + `_platform/platform.json` 双实现，字段权威分离：引擎观测不可变、human 标注独立、读时 overlay）。

**工具**（agent 面，无审批语义——notes 同级的纯本地记录）：
- `record_intelligence`：参数 `kind: credential\|lead\|sensitive_data`（必填）、`title`（必填）、`content`（必填，≤4k 字符）、`endpoint?`、`confidence?` ∈[0,1] 缺省 0.5。指纹 = sha256(kind+规范化 content+endpoint) 前 16 hex；同指纹重记录**不重复建项**（内容不可变，仅 confidence 可升）。`tool_result` 事件 `result_meta` 携带 `intel_fingerprint/intel_kind/intel_deduped`（前端失效 hook 的信号）。
- `read_intelligence`：列出已记录项（避免模型自我重复）。

| Method+Path | 请求 | 响应 |
|---|---|---|
| GET `/api/tasks/{id}/intelligence` | — | `{ items: [{ ...Item, fingerprint, kind, title, content, endpoint?, confidence, created_at, updated_at, scan_ids[], triage? }], counts: {credential, lead, sensitive_data}（零填充）, triage_available, damaged_runs?, note? }` |
| PATCH `/api/tasks/{id}/intelligence/{fingerprint}` | `{status: open\|confirmed\|dismissed}` | `{ fingerprint, status }` — 未知词汇 400；re-triage 覆盖旧标签（decided_at 随动） |

- 聚合语义：runs 按 ListRuns 序（新→旧）遍历，同指纹**最新 run 的记录胜出**、旧 run 只并入 `scan_ids`；某 run 快照损坏 → `damaged_runs` 计数 + note，**不折叠为干净视图**。未知任务 → 200 空聚合（与 findings 子资源语义一致）。
- 边界：本工具只存**目标侧**情报；操作者侧密钥永不入此库（API 不回传密钥红线针对的是操作者自身凭据，目标侧发现与 VulnSummary 回传 poc 同先例）。

## 3.8 流量代理 raw 交换归档（只读，2026-11-22 增补）

> 流量代理为归因流量持久化的原始 HTTP 请求/响应包（wire 保真：header 值与 body **不脱敏**，2026-11-22 操作者裁决修正 R2b 的「header 只记名」红线——摘要层 InfoItem 仍掩码，raw 归档是权威包体）。存储 = `<scanDir>/traffic/tr_<seq>.json`（`storage.WriteAtomic`，一交换一文件，重启续号不覆盖）——流量原始包是**字节产物**，R6 PG 主存储切换后仍留文件层（宪章 v3.0 §6.2，DSN 为启动必需）；findings 详情弹窗按 finding 的 endpoint/method 懒加载渲染。

| Method+Path | 请求 | 响应 |
|---|---|---|
| GET `/api/scans/{id}/traffic` | `?endpoint=`（URL 子串，不区分大小写；缺省=全部）、`?method=`（精确方法）、`?host=`（资产 host，精确且忽略端口）、`?refs=`（finding 落库时绑定的精确证据，`session:seq,session:seq`）、`?seq=`（本 session 的单条记录）、`?limit=`（最新窗口，缺省 20，上限 100） | `{ scan_id, exchanges: HttpExchange[], total_matched, truncated, present }` |

- **run 级合并**：实例 id 请求（面板/实时视图）会合并该 run 名下的全部 sibling session 目录（checkpoint/resume、多目标派发各自从 seq 1 起号），按 `at` 升序后取最新 `limit`；每条带 `session`（session 目录 basename），`(session, seq)` 才是跨 session 唯一键。
- **HttpExchange**：`{ seq, at, method, url, session?, host?, status?, channel?（bearer\|subprocess）, high_risk?（AssessHTTP 命中原因）, denied?（代理闸 403 拒绝）, upstream?, request_raw?, response_raw?, request_truncated?, response_truncated? }`。`request_raw` = 发往目标的 origin-form 包（run token 已随 Proxy-Authorization 逐跳剥离，**不出现在包里**）；`response_raw` = 解码后表示（无 chunked 帧）；双侧各截断于 128 KiB 并带 `*_truncated` 标记。
- **证据绑定**：`report_vulnerability` 落库时按 endpoint+method+资产 host 从该 run 的归档中匹配（上限 20 条）并把 `traffic_refs: [{session?, seq}]` 写进 finding；详情优先用 `?refs=` 精确取包，旧 finding 无 refs 时才回退 endpoint/host 匹配。`session`/`seq` 只接受 basename 与正整数，路径穿越的 ref 被忽略。
- **`present` 语义**：`false` = 该扫描无归档（代理关闭/pre-R2b 旧扫描/纯无归因流量），`true` + 空 `exchanges` = 有归档但无匹配——两种空态前端分开渲染。
- 升级 passthrough（WebSocket 盲转发）与盲 CONNECT 无明文半程，**不产生 raw 记录**（与观测元数据边界一致）。未知 scan → 404；非 GET → 405。

## 3.9 集成模块：MCP 服务器 / 用户技能 / Agent 档案 + 调用记录（2026-11-22 增补）

> 三个操作者可配置的引擎输入（0008 `mcp_servers`/`user_skills`/`agent_profiles` + `_platform/platform.json` 双实现；FileStore 模式无 DSN 完整可用）。定位是**配置，不是判定**（宪章 §3.5）：MCP 服务器行不为工具输出背书、用户技能是 prompt 输入（Hint）、档案约束委派而不绕过 Verifier/收工闸门。引擎消费点：扫描启动 best-effort 装配（`internal/web/integration_inputs.go`，store 不可用 = 不带操作者内容照常扫描）。

**MCP**（`internal/tools/mcp`，官方 go-sdk v1.8.0）：stdio（argv 直 spawn、`SanitizeEnviron` 后追加服务器自带 Env）与 streamable HTTP（DialGuard 逐跳复查 + scopeguard 拒操作者机器端点，写入与连接时双重校验）。工具以 `mcp_<server>_<tool>` 注册进 registry → 熔断/参数校验/审批闸/五闸六环全继承；审批策略把 `mcp_*` 归 command 类（`require_approval=false` 的服务器由 broker 快照豁免）。Verifier 不接入。`POST {id}/refresh` 为操作者侧健康探测（回填 `tool_count/last_healthy_at/last_error`）。

**Skills**：用户技能与内置 800+ 语料合并进 `read_skill/list_skills/search_skills`（固定 category `user`，同名用户技能遮蔽内置；`user` 文档带出处 banner）；每次成功加载写 domainjournal `skill_load` 审计。内置语料另经 `/api/skills/builtin`（索引）与 `/api/skills/builtin/{name}`（正文）只读投影给面板，**不需要 platform store**、无写路径；正文剥离 frontmatter，结构化字段（description/tags/intent/category）单独返回。该读取路径不做 alias/模糊解析（那是 agent 的 lookup 路径）。

**Agent 档案**：用户档案与 5 个内置专家合并（内置行 API 只读、role 保留字 409）；参与自动委派波次 + `spawn_agent` 按名派生；`tool_scope_hidden` 数据驱动工具面裁剪。内置行现在返回**完整规范契约**（`focus`/`vuln_classes`/`evidence_contract`/`stopping_rule`）与**生效的** `tool_scope_hidden`（`agent.BuiltinSpecialistProfiles()` 按 `applyRoleToolScope` 同一解析：角色映射→`other` 回退），面板只读展示，不在 web 层复制规范字符串。派生生命周期写 domainjournal `agent_delegate`。

**调用记录**：per-scan 权威 = domainjournal 三个新 Op（`skill_load`/`agent_delegate`/`mcp_call`）；跨扫描检索投影 = web 层 best-effort JSONL（`<dataDir>/_invocations/{mcp,skills,agents}.jsonl`，8MiB 单文件轮转，缺失文件 = 空列表不是错误）。投影缺失不影响扫描（`RecordApprovalRuleHit` 先例：定义行才是权威）。

| Method+Path | 请求 | 响应 |
|---|---|---|
| GET `/api/mcp-servers` | — | `{ servers: McpServer[] }`（env/headers 打码） |
| POST `/api/mcp-servers` | `{name, transport: stdio\|http, command?, args?, env?, url?, headers?, enabled, require_approval, timeout_seconds?}` | `McpServer`（201）— 重名 409、操作者机器端点 400 |
| GET/PATCH/DELETE `/api/mcp-servers/{id}` | PATCH 同 create 的可选字段（transport 不可改；打码值提交 = 未修改） | `McpServer` / `{ok:true}` |
| POST `/api/mcp-servers/{id}/refresh` | — | `McpServer`；探测失败 502 `{error, server}`（健康投影已回填） |
| GET `/api/skills` | — | `{ skills: UserSkill[] }` |
| GET `/api/skills/builtin` | — | `{ skills: [{name, category, description, intent?, tags?}] }`（内置语料索引，只读，无 store 依赖） |
| GET `/api/skills/builtin/{name}` | `?category=`（可选，消歧） | `{name, category, description, intent?, tags?, body}`（frontmatter 已剥离）；未知 404 |
| POST `/api/skills` | `{name(kebab), description, tags?, intent?, body(≤128KiB), enabled}` | `UserSkill`（201）— 重名 409、格式 400 |
| GET/PATCH/DELETE `/api/skills/{id}` | PATCH 可选字段 | `UserSkill` / `{ok:true}` |
| GET `/api/agent-profiles` | — | `{ profiles: AgentProfile[] }`（内置行前置，`builtin:true` 只读，id 形如 `builtin:<role>`） |
| POST `/api/agent-profiles` | `{role(kebab), focus, vuln_classes?, evidence_contract, stopping_rule, tool_scope_hidden?, enabled}` | `AgentProfile`（201）— 保留字/重名 409、契约三元组缺失 400 |
| GET/PATCH/DELETE `/api/agent-profiles/{id}` | PATCH 可选字段 | `AgentProfile` / `{ok:true}` |
| GET `/api/mcp-servers/invocations` | `?page&size&instance_id&name&status(ok\|error)` | `{items,total,page,size}` |
| GET `/api/skills/invocations` | 同上 | 同上 |
| GET `/api/agent-profiles/invocations` | 同上 | 同上 |

## 3.9a 内置工具目录 + 工具调用记录（2026-11-22 增补）

> 侧栏「工具」页的两块只读数据：目录是**静态投影**（`internal/web/tools_catalog.go`，镜像 `internal/agent` 的注册面），记录是 §3.9 调用记录投影的第四模块 `tools`。两者都不构成判定：目录不代表单个 Run 的启用/裁剪状态（`tool_scope_hidden`、阶段工具面、Verifier 工具面仍在 Run 内决定），记录不外溢为审计权威（per-scan 权威仍是 domainjournal）。

| Method+Path | 请求 | 响应 |
|---|---|---|
| GET `/api/tools` | `?category=<key>&q=<substring>`（可选；q 匹配 name/description，大小写不敏感） | `{ tools: [{name, category, description, approval?, parameters:[{name, required, desc?}]}], categories: string[] }` |
| GET `/api/tools/invocations` | `?page&size&instance_id&name&status(ok\|error)` | `{items,total,page,size}` |

- **记录归属**：`tool_result` 事件按模块唯一归属——`read_skill` → skills、`mcp_*` → mcp、`spawn_agent/create_agent` 与委派完成 → agents；其余具名工具（`terminal_execute`、`http_request`、`browser_action`、`verify_*` 等）→ tools。`ToolName` 为空的事件不记录。投影文件 `<dataDir>/_invocations/tools.jsonl`，轮转/缺失语义与 §3.9 一致。
- **approval 字段**：镜像 `internal/approval` 的 `classifyCall`（command / http / browser）；缺省 = 不参与审批闸（只读工具、ledger/plan 工具、Verifier 工具）。是否实际弹审批仍由 Run 的审批策略（off / rules / remember）与工具参数决定。

- 凭据红线：`mcp-servers` 的 `env/headers` 永不回传明文（`maskAuthCredential` 打码），提交 `****` 前缀 = 未修改；`/api/mcp-servers` 加入机器令牌黑名单（session-only）。
- `McpServer`：`{id,name,transport,command?,args?,env?,url?,headers?,enabled,require_approval,timeout_seconds,tool_count,last_healthy_at?,last_error?,created_by?,created_at,updated_at?}`。

## 3.10 仪表盘认证与 RBAC（2026-11-22 增补）

> 人类会话从"env 单操作者 + 内存不透明 cookie"升级为 **JWT（HS256）+ 本地账号存储 + RBAC**。签名密钥为随机值：首次启动 `crypto/rand` 生成 32B hex，落盘 `<dataDir>/auth/jwt.key`（0600，`storage.WriteAtomic`），永不进 env、永不回传 API（密钥红线不变）。`auth/` 为 dataDir 保留顶层名。登录态为 `xalgorix_session` Cookie 中的三段式 JWT（HttpOnly/SameSite=Strict/Secure@TLS/24h，claims: `sub/role/jti/iat/exp/iss`）——SSE（EventSource）与文件直链因此零改动；登出吊销 jti（进程内黑名单，重启清空但 Cookie 已随登出清除）。

**角色与策略**（`auth_rbac.go`，机器 Bearer 令牌不走 RBAC，维持自身黑名单）：

| 角色 | 权限 |
|---|---|
| `admin` | 全权（含设置/密钥/账号管理） |
| `operator` | 业务操作全放行（扫描/任务/项目/审批/定时/报告/chat）；`/api/settings`、`/api/auth/profiles`、`/api/mcp-servers`、`/api/skills`、`/api/agent-profiles` 的**写**操作 403；`/api/users` 完全不可见（403） |
| `viewer` | 仅 GET/HEAD（任何写 403）；自助改密 `/api/auth/password` 与登出除外 |

**首次启动**：无任何账号且 env 无凭据时 `needs_setup=true`，`POST /api/auth/setup` 可创建首个 admin（此后该端点永久 403）；env 配置 `XALGORIX_USERNAME/PASSWORD(_HASH)` 的既有部署在启动时自动播种为 admin（之后账号存储优先于 env）。

| Method+Path | 请求 | 响应 |
|---|---|---|
| GET `/api/auth/status` | — | `{auth_enabled, authenticated, needs_setup, user: {username, role}\|null}` |
| POST `/api/auth/login` | `{username, password}` | `{status:"ok", user:{username,role}}` + Set-Cookie；失败 401、限速 429（`Retry-After`） |
| POST `/api/auth/setup` | `{username, password}` | 首个 admin（201 语义 200 + Cookie）；store 非空或 env 可播种 → 403 |
| POST `/api/auth/password` | `{current_password, new_password}` | 自助改密（任何角色，需当前密码）；错密码 400 |
| POST `/api/auth/logout` | — | 吊销 jti + 清 Cookie |
| GET `/api/users` | — | `ManagedUser[]`（admin-only；**永不回传 password_hash**） |
| POST `/api/users` | `{username, password, role}` | `ManagedUser`（201）；重名 409、校验 400（用户名 `^[a-zA-Z0-9._-]{1,64}$`、密码 8-72） |
| PATCH `/api/users/{id}` | `{role?, disabled?, password?}` | `ManagedUser`；**最后一个 enabled admin 的停用/降级/删除一律 409** |
| DELETE `/api/users/{id}` | — | `{status:"deleted"}`；同上守卫 |

- 账号存储：`<dataDir>/auth/users.json`（版本化快照 + `storage.WriteAtomic`，web 层自有 store，**不经 platform**——登录是硬能力，平台 503 降级语义不适用于认证）。
- 未知角色 claim 一律 fail-closed（403）。

- `UserSkill`：`{id,name,category,description,tags?,intent?,body,enabled,created_by?,created_at,updated_at?}`。
- `AgentProfile`：`{id,role,focus,vuln_classes,evidence_contract,stopping_rule,tool_scope_hidden?,enabled,builtin,created_by?,created_at,updated_at?}`。
- `InvocationRecord`：`{id,module,ts,instance_id?,scan_id?,agent_id?,name,server?,status,error?,args?,outcome?}`（`args` 为 ≤512 字节 digest，敏感键打码）。

## 3.11 报告交付（手动生成，2026-11-23 增补）

报告只在操作者手动点击时生成；扫描结束与 wildcard 子域结束**不再**自动出 PDF，也不再广播 `report_ready`。规格见 `docs/REPORT_DELIVERY_SPEC.md`。

- `POST /api/tasks/{id}/reports` — body `{run_ids?, formats:["pdf"|"md"|"html"], template:"standard", language:"zh-CN"|"en", mode:"template"|"agent", model?}` → `202 Report`。`run_ids` 缺省为最新 run；同一任务同时只允许一个 `queued|generating` 报告（第二请求 409）。
- `GET /api/tasks/{id}/reports` → `{items: Report[]}`（revision 倒序）。
- `GET /api/reports?limit=200` → `{items: Report[]}`（跨任务，Reports 页数据源）。
- `GET /api/reports/{reportID}` → `Report`（含 `artifacts[]`、`progress`、`task_name`、`targets`）。
- `GET /api/reports/{reportID}/download/{format}?kind=total|sub&scan_id=` → 文件附件。
- `POST /api/reports/{reportID}/cancel` — 取消 queued/generating；`DELETE /api/reports/{reportID}` — 删除版本（生成中返回 409，需先取消）。
- `GET /api/scans/{scanID}/findings/{vulnID}/export?format=md|html|json&download=1` — 单漏洞 SRC 提交件。

`Report` 形状：`{id,project_id?,task_id,revision,run_ids[],scan_ids[],status,template,language,mode,model?,prompt_version?,formats[],error?,created_at,started_at?,finished_at?,artifacts[],progress?,task_name?,targets?}`；`artifacts[]` 为 `{kind:"total"|"sub",scan_id?,target?,format,bytes,sha256?,url}`。

存储：元数据在 PG `reports` / `report_artifacts`（迁移 `0028_reports.sql`），字节产物在 `<dataDir>/reports/<taskID>/<reportID>/`（`report.json` + 请求的 PDF/MD/HTML；子报告在 `sub/`）。**报告内容含 HTTP raw 请求/响应，导出前统一脱敏**（Cookie/Authorization/token/密码/邮箱/手机号/身份证）；无抓包时明确标注并附重建 PoC，不伪造原文。进程重启时残留的 `queued|generating` 行由启动 reconciliation 标记 failed。

## 4. 前端已实现清单（本契约的消费方）

- `webui/src/types/api.ts` — Project/Task/Run/Approval/ScanFile* 类型
- `webui/src/api/client.ts` + `queries.ts` — 全部端点与 hooks（approvals 5s/30s 自适应轮询；scan files 运行中 5s）
- `webui/mock-backend.mjs` — 全部端点的 mock 实现 + 审批倒计时/超时模拟 + 文件浏览器 fixture
- 页面：`/projects`、`/tasks`、`/tasks/:taskId`、`/approvals`、`/scans/:scanId`（文件 tab）；OpsPulse 态势条；项目切换器；`/mcp`、`/skills`、`/agents`（各含「配置管理 / 调用记录」双 tab，§3.9）
