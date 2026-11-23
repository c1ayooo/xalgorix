# 会话页模块映射表（七环 × 字段权威）

> 状态：**已落地**（2026-11-23）。范围：任务详情「会话」页
> （`webui/src/components/sessions-tab.tsx` 与 `components/sessions/modules/*`）。
> 上位约束：`docs/CORE_PHILOSOPHY.md`（§2 七环、§3.5 字段级权威、§5.1 事件非权威、§9 判定边界）、
> `docs/REDESIGN.md` §7.2、`UI_DESIGN.md` §6。
>
> 本文是代码旁的可验收契约：注册表里的 `ContentModule.ring` / `authority` 必须与下表一致。

## 1. 为什么需要这张表

此前页面把内容固定成三个角色组（planner / decision / system），每个组的可见性与内容都靠
"事件里有没有某个 tool_call"推断：

- **Planner 为空**：引擎自建计划（`refreshPlanForSurface`）不发射 `build_plan`/`update_plan`，
  谓词永不命中；且计划明细没有 REST 投影、未持久化，续跑后彻底丢失。
- **只有三个模块**：`SessionRole` 是闭集合，新增任何角色都要改谓词、rail、workspace 三处。
- **Workers 误导**：worker 列表来自历史事件去重，状态图标复用 run 级 status，于是
  "已结束的 worker"和"正在跑的 worker"外观一致（详见 §4）。

## 2. 模块清单（注册表 = 唯一权威）

| id | 七环 | 字段权威 | 数据源 | 左栏徽标 | 空态语义 |
|---|---|---|---|---|---|
| `main`（非注册表项） | 人工输入 | human | `GET /api/conversations/{id}/transcript` + guidance SSE | — | 无消息（真实空） |
| `plan` | 2 信念（PlanTask = Intent） | engine | `GET /api/scans/{id}/plan` | `completed+skipped/total` | `present:false` → absent；`partial:true` → 琥珀横幅（not_persisted/truncated） |
| `decision` | 2 信念（Hypothesis） | telemetry（事件流；账本权威见「账本」tab） | run-events（`record_hypothesis`/`add_hypothesis_evidence`） | 事件计数 | absent（本 run 无决策动作） |
| `obligation` | 3 义务 | engine（只读投影，不重算闸门） | plan 未完成/跳过项 + agents 空闲行 | `counters.unfinished` | absent（无未完成义务） |
| `workers` | 4 行动 | engine（agentsgraph） | `GET /api/instances/{id}/agents` + 左栏 `GET /api/tasks/{id}/workers` | `running/limit(+queued)` | `available:false` / `agents:[]` / 全 completed / 部分 queued 四态分开 |
| `system` | 5 证据（审计投影） | telemetry | run-events（phase/paused/resumed/stopped/error/finished/recovery） | 事件计数 | absent |

规则（代码里是 `ContentModule.visible`）：

1. **默认常驻**：只有"不适用"（无 Run、无数据源）才隐藏；"引擎没产生"必须显示 absent 空态。
   `plan` / `workers` 在任何有 Run 的情况下都可见——这正是"Planner 区域凭空消失"的修正。
2. 每个模块必须声明 `ring` 与 `authority`，`registry.tsx` 的类型强制两者必填。
3. 主区标题取 `titleKey ?? labelKey`；头部展示 `authority` 徽标，避免把遥测当状态。
4. 主会话视图（无 `?agent=`）在转录上方直接渲染 `PlanRail`——同一份 `PlanView` 投影同时供 Planner 模块与主视图，避免"必须先点左栏才知道规划了什么"。

## 3. 扩展配方（新增模块）

1. 在 `components/sessions/modules/` 新建渲染组件（只吃 props，不自己取数）。
2. 在 `registry.tsx` 追加一条 `ContentModule`：`id / slots / order / ring / authority / labelKey /
   deepLink / visible / rail / render`。
3. 在 `i18n/translations.ts` 的 `en` 与 `zh-CN` 同时补键（缺一即 typecheck 不报但会回退英文）。
4. 新增数据源时先加投影端点（见 `API_CONTRACT.md §3.6a`），**禁止**用事件重放拼装状态。
5. 运行 `cd webui && npm run typecheck` + `make webui`（否则 `go:embed` 仍是旧产物）。

## 4. Worker 并发语义（为什么"两个 worker 只有一个在干活"）

| 层 | 旧行为 | 现行为 |
|---|---|---|
| 列表来源 | 历史事件里出现过的 `agent_id` 去重 | `GET /api/instances/{id}/agents`（agentsgraph 状态权威） |
| 行状态 | 复用 run 级 `status`（跑就转圈） | 每行自身 `queued/running/completed/failed` |
| 分组 | 混在一起 | 运行中 / 排队（位置 #n）/ 已结束（默认折叠） |
| 活跃判据 | 5s `live` 窗口（事件可能被 `safeSend(...,0)` 丢弃） | 状态点权威；`live` 仅作辅助提示 |
| 做了什么 | 无 | `claimed_hypotheses`（当前认领的假设/端点） |
| 终态 | 空 | checkpoint 重建；持久 `running` 降级为 `failed` |
| 左栏 | 单个 Workers 入口，只显示运行中计数 | 每个 run 下**逐 worker 常驻一行**（任务级 `/workers` 聚合，含 completed/failed）；点击一次写入 `?run=&agent=` 打开该 worker 转录；`live` 只作状态点 |

调度侧配套（P1/P2）：

- 并发上限 `XALGORIX_MAX_CONCURRENT_AGENTS`（默认 3，上限 10），`limit = min(它, 生命周期委派上限)`。
- `create_agent`（同步）会在结果里附"阻塞协调器且占用一个并发槽，并行请用 `spawn_agent`"的可纠正提示。
- 专家 lane 的 `vuln_class` 池耗尽时，可**借用其他 lane 未归属**的 queued 假设（`ClaimNextUnassignedAs`），
  绝不夺取其他 agent 已归属的工作；彻底无活时发 `agent_idle`，行内显示"空闲 — 当前无可认领工作"。
- 共享工具/迭代预算在 ≥2 个活跃 agent 时做 fair-share（每个 agent 保留 `cap/(n+1)` 给他者），
  单 agent 路径字节等价不变。
- 波次重排（`XALGORIX_WAVE_RESCHEDULE`，默认关）与多 target 并发（`XALGORIX_TARGET_CONCURRENCY`，默认 1=串行）
  都是 opt-in；详见 `REDESIGN.md` §7.2 与 `API_CONTRACT.md`。

## 5. 不可破坏的边界

- 不改 Verifier 判定/工具面/轮次时限；不并发化同一 Agent 的 Verifier（宪章 §9.2/§12）。
- 不改 `report_vulnerability` 闸门顺序与收工闸门；`obligation` 模块只读展示，不重算结论。
- 事件流单向；工具/agent 不直接 broadcast。
- 投影端点不引入第二套持久权威：plan 明细走既有 record 写入点（R6.1 迁 PG），worker 终态读 graph checkpoint。

## 6. 验收证据

| 层 | 用例 |
|---|---|
| agentsgraph | `TestSnapshotReportsConcurrentAndQueuedAgents`、`TestStateSinkEmitsLifecycleEvents`、`TestReadCheckpointState*` |
| agent | `TestPlanSnapshot*`、`TestDelegationSnapshotReportsPoolAndQueue`、`TestClaimNextHypothesis_SpecialistBorrowsUnownedWork`、`TestBudgetFairShare*`、`TestWaveRescheduleIsOptIn` |
| web | `TestScanPlanProjection_*`、`TestInstanceAgentsProjection_*`、`TestTargetConcurrencyLimitDefaultsToSerial` |
| 前端 | `npm run typecheck`；`make webui` 重建嵌入产物 |
