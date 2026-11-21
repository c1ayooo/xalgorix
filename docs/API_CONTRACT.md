# API 契约 — 项目 / 任务 / 执行 / 审批（前端先行，R3）

> **用途**：前端（webui）已按本契约实现并用 `mock-backend.mjs` 验收。后端 agent 按此实现 REST 端点；字段名/形状以本文件为唯一权威，与 `docs/REDESIGN.md` §3 schema 对齐。改契约必须先改本文件 + `webui/src/types/api.ts` + `webui/mock-backend.mjs` 三处。
>
> **通用约定**：JSON；鉴权沿用现有 session/CSRF 与机器令牌中间件（审批决策端点属于状态变更，走同一 CSRF 检查）；错误沿用 `{"error": "..."}` 包络；时间一律 RFC3339 UTC（`deadline_at` 等前端做本地倒计时）。所有列表端点支持 `project_id` 过滤（隔离红线：冷路径查询强制 project 维度）；显式传入 `project_id` 而平台仓库不可用时返回 503，绝不退化为跨项目全量结果。
>
> **降级**：后端未实现期间，前端页面呈现 ErrorState（不 crash）。无 DSN 单机模式下后端可返回单个隐式项目 `default`（见 REDESIGN §3.3）。

## 1. Projects

```ts
interface Project {
  id: string;            // uuid
  name: string;
  slug: string;          // URL/切换器用，唯一
  scope: { targets: string[]; allow_ports?: number[] };
  created_at: string;
  archived_at?: string;  // 存在即归档（列表默认不含归档）
  task_count?: number;   // 列表响应附带
  active_runs?: number;  // 列表响应附带
}
```

| Method+Path | 请求 | 响应 |
|---|---|---|
| GET `/api/projects` | — | `{ projects: Project[] }` |
| POST `/api/projects` | `{ name, scope: { targets: string[], allow_ports? } }`（slug 服务端从 name 生成，冲突加后缀；**scope.targets 只写主机名**——自带 `:port` 的条目返回 400，端口统一由 allow_ports 控制；**allow_ports 为空 = 不限制端口**；非空时同时约束显式端口与 `http`/`https` 默认端口，无 scheme 裸主机要求 `80` 和 `443` 都可放行；`*.example.com` 仅匹配子域，不匹配 apex） | `Project` (201) |
| GET `/api/projects/{id}` | — | `Project` |
| PATCH `/api/projects/{id}` | `{ name?, scope?, archived? }` | `Project` |
| DELETE `/api/projects/{id}` | — | `{ status: "deleted" }`（仅允许无活跃 Run 的项目；否则 409） |

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
  created_at: string;
  updated_at?: string;
  archived_at?: string;
  // 列表/详情响应附带的投影统计（冷路径）：
  run_count?: number;
  last_run_at?: string;
  last_run_status?: string;
  finding_count?: number;
}
```

| Method+Path | 请求 | 响应 |
|---|---|---|
| GET `/api/projects/{id}/tasks` | — | `{ tasks: Task[] }` |
| POST `/api/projects/{id}/tasks` | `{ name, goal, instruction?, phase_policy?, schedule? }` | `Task` (201) |
| GET `/api/tasks/{id}` | — | `Task` |
| PATCH `/api/tasks/{id}` | 同 POST 字段（部分更新） | `Task` |
| DELETE `/api/tasks/{id}` | — | `{ status: "deleted" }` |
| POST `/api/tasks/{id}/runs` | `ScanRequest` 的部分覆盖（targets 缺省取项目 scope.targets；任务 `context_paths` 与请求 `scan_context` 全部合并；其余字段同现有 `/api/scan`） | `{ status: "pending", run_id, instance_id }`（instance_id 兼容现有实例页/scan-detail 直链；`status` 为真实 dispatch 状态，与 `POST /api/scan` 一致——入队后为 `pending`，admission 授予并发槽后才转 `running`，权威状态轮询 `GET /api/instances`） |
| GET `/api/tasks/{id}/runs` | `?page&size` | `Paginated<Run>` |
| GET `/api/tasks/{id}/findings` | — | `FlatFinding[]`（任务级累积投影，跨 Run 按 target/endpoint/title/severity 去重，最新 Run 胜出；有 DSN 时读 PG `run_findings` 冷路径，无 DSN 时全量分页遍历 scan.json，不设 500 Run 上限） |

```ts
interface Run {
  id: string;
  task_id: string;
  legacy_scan_id?: string;   // 旧 scan.json 关联
  instance_id?: string;      // 活跃/历史实例，前端用 /scans/{instance_id} 直链
  mode: string;              // 同 scan_mode
  status: string;            // running/pending/paused/finished/stopped/failed/saved/missing
                             // missing = 挂载存在但 scan.json 已不存在且无待调度证据；
                             // 不计入 active_runs，也不阻止项目删除
  started_at?: string;
  finished_at?: string;
  vuln_count: number;
  total_tokens: number;
  trigger?: "manual" | "schedule" | "rerun";
}
```

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
  decided_by?: string;       // "operator:<user>" | "timeout" | "system"
  reason?: string;           // 拒绝/批准附言
}
```

| Method+Path | 请求 | 响应 |
|---|---|---|
| GET `/api/approvals` | `?status=pending\|decided\|all&project_id=&task_id=&page=&size=`（`decided` = approved/rejected/timeout/cancelled） | `{ approvals: Approval[], pending_total: number }`（`pending_total` 为**全项目**待审批数，供 OpsPulse） |
| GET `/api/approvals/summary` | — | `{ pending_total: number, nearest_deadline?: string }`（OpsPulse 专用轻量端点，5s 轮询） |
| POST `/api/approvals/{id}/decision` | `{ decision: "approved" \| "rejected", reason? }` | `Approval`（对非 pending 单返回 409） |
| POST `/api/approvals/batch-decision` | `{ ids: string[], decision: "approved" \| "rejected", reason? }` | `{ approved: number, rejected: number, skipped: number }` |

**WS 事件**（供事件流实时显示，前端同时以轮询兜底）：
- `approval_requested`：payload 为 `Approval`（pending）。
- `approval_decided`：`{ id, status, decided_by, decided_at }`。

前端在 LiveFeed 的 `TYPE_COLOR` 中已为 `approval_requested`/`approval_decided` 留色；事件流内嵌紧凑审批卡（行内决策 + 深链 `/approvals?focus=<id>`）。

**后端行为要点**（与 REDESIGN §5 对齐）：超时由服务端判定写 `timeout`；Run 停止时未决单批量 `cancelled`；同一 Run 未决上限默认 1。

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

**WS 事件**：`guidance_injected` / `guidance_queued` / `guidance_consumed`，payload 为 `Guidance`。

**注入语义**（后端实现要点）：
- 目标 Run **running** → 走 in-scan chat 通道（`internal/web/chat.go`）注入下一轮 user turn，`status=injected`；
- 目标 Run 暂停/停止/无 Run → `status=queued`，Run 启动时并入 briefing（等价 4zreco"暂停后发消息继续"的 xalgorix 变体：不必先暂停，排队即可）；
- `message` 超过 4000 码点 → 400；`request_id` 缺失 → 400；
- **consumed 翻转闭环**：injected 行由 domain journal 观察者（`internal/web/guidance_observer.go`）闭环——引擎产出（hypothesis/evidence 记录的 summary、InfoItem 文本）引用 `gi:<id>` → `UpdateGuidanceStatus(consumed)` + `guidance_consumed` WS 事件；briefing 并入路径无逐行标记载体，如实保持 injected（操作者从 injected 即可理解该 turn 已带入上下文）；store 失败只留日志，观察者永远 fail-open。

## 3.2 项目维度过滤（overview 项目化，2026-11-20 增补）

现有端点追加**可选** `project_id` 查询参数（缺省 = 全部，兼容旧行为）：

- `GET /api/instances?project_id=` → 只返回该项目的实例
- `GET /api/findings?project_id=` / `GET /api/findings/summary?project_id=` → 项目内发现/汇总
- `GET /api/scans?project_id=`（分页版同理）

前端 overview 页已按当前项目带参查询；后端实现前参数被忽略（行为同现状）。

## 3.3 信息空间（R5，只读）

项目级 InfoItem 激活入口。`project_id` 必填；`context`/`association` 的 `scan_id` 必须属于该项目。项目过滤通过 platform runs 的 `instance_id`/`legacy_scan_id` 并集实现，**不向 info_items 增加 project_id**。

```text
GET /api/info/region?project_id=&subject_kind=&subject=&purpose=&nature=&after=&before=&limit=
→ { available: true, items: InfoItem[], truncated, incomplete, gap_reason? }

GET /api/info/context?project_id=&scan_id=&subject=&subject_kind=&since=&until=&limit=
→ { available: true, items: InfoItem[], truncated, incomplete, gap_reason? }

GET /api/info/association?project_id=&scan_id=&ids=id1,id2&edge_types=supports,...&max_depth=1..3&limit=
→ { available: true, hits: [{ item: InfoItem, path: string[], depth: number }], truncated, incomplete, gap_reason? }
```

- `limit` 默认 50、上限 200；关联深度 1..3；`truncated`/`incomplete`/`gap_reason` 必须原样透传。
- 无 PostgreSQL 冷路径时返回 **503**：`{ available: false, reason: "cold_path_disabled", error }`，不得用空数组冒充“无数据”。
- 有冷路径但读取失败时 `503` + `reason: "cold_path_unavailable"`。
- InfoItem 形状沿用 `internal/infoitem.Item`：`id/scan_id/source_seq/nature/coordinates/supports/confidence/coordinate_status/...`。
- 既有 `GET /api/scans/{id}/audit`（领域审计投影）与 `GET /api/scans/{id}/coldpath`（pending/applied/gap/failed/disabled）为同一信息空间的审计/同步状态来源，前端可复用。

## 4. 前端已实现清单（本契约的消费方）

- `webui/src/types/api.ts` — Project/Task/Run/Approval 类型
- `webui/src/api/client.ts` + `queries.ts` — 全部端点与 hooks（approvals 5s/30s 自适应轮询）
- `webui/mock-backend.mjs` — 全部端点的 mock 实现 + 审批倒计时/超时模拟
- 页面：`/projects`、`/tasks`、`/tasks/:taskId`、`/approvals`；OpsPulse 态势条；项目切换器
