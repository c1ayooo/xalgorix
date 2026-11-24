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
| `workers` | 4 行动 | engine（agentsgraph + 图单元状态） | `GET /api/instances/{id}/agents` + 左栏 `GET /api/tasks/{id}/workers`（`units[]` 为主，`agents[]` 为明细） | `running/limit(+queued)` | `available:false` / `units:[] && agents:[]` / 全 completed / 部分 queued 四态分开 |
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
| 左栏 | 单个 Workers 入口，只显示运行中计数 | 每个 run 行可展开其 **worker 子行**（任务级 `/workers` 聚合，含 completed/failed；选中 run 默认展开）；点击子行一次写入 `?run=&agent=` 打开该 worker 转录；`live` 只作状态点 |

### 4.1 可见身份 = 工作单元（2026-11-24 增补）

R7 池派发的 worker 拥有一个权威 intent（unit）。面板把**unit 作为可见 worker 行**，agent 实例只是明细：

- `units[]` 由 `/agents` 与 `/workers` 下发：`unit_id/title/state/status/agent_ids/agent_status/claimed_hypotheses`；行主状态取图状态，`agent_status` 只作执行细节。
- 重派、重试、租约回收都聚到同一行（`agent_ids` 追加，`N×` 徽标），不会出现"同一题两个 worker"。
- 点击 unit 行写 `?agent=unit:<unit_id>`；转录请求发 `?unit=`，服务端取该 unit 全部 agent 的事件并集后再分页，因此 worker 泳道历史跨尝试连续。
- legacy `spawn_agent`（无 unit）仍按 agent 一行，`?agent=<agent_id>` 语义不变。
- 跨 worker 共享仍走黑板：`read_ledger` / `read_info_item` / ledger evidence；unit 行只解决"谁是那个 worker"的投影问题，不改状态权威。

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
| agentsgraph | `TestSnapshotReportsConcurrentAndQueuedAgents`、`TestStateSinkEmitsLifecycleEvents`、`TestReadCheckpointState*`、`TestDispatchRunsAndCollects` |
| agent | `TestPlanSnapshot*`、`TestDelegationSnapshotReportsPoolAndQueue`、`TestClaimNextHypothesis_SpecialistBorrowsUnownedWork`、`TestBudgetFairShare*`、`TestWaveRescheduleIsOptIn`、`TestToolSurface*`、`TestPlannerWorkerFinishGate` |
| web | `TestScanPlanProjection_*`、`TestInstanceAgentsProjection_*`、`TestScanGraphProjection_*`、`TestScanAssetsProjection_*`、`TestScanUnits*`、`TestTargetConcurrencyLimitDefaultsToSerial`、`TestWorkerUnits_*`、`TestUnitTranscriptItems_*`、`TestUnitAgentIDs_*` |
| orchestrator | `TestPoolClaimsAndSettles`、`TestPoolFailureSettlesBlocked`、`TestSeedIfEmpty`、`TestPlannerApplyAddsAndDedupes` |
| 前端 | `npm run typecheck`；`make webui` 重建嵌入产物 |

## 7. R7 planner_worker 语义（默认内置）

planner_worker 是默认拓扑，不是设置项。`XALGORIX_WORKER_CONCURRENCY=1..10` 可在设置页修改；`XALGORIX_ORCHESTRATOR=legacy` 仅为隐藏的紧急回滚。

- **主会话（无 `?agent=`）**：main chat 按用户消息触发，具体工作先 `propose_intent` 形成草案，操作者确认（转录提案卡端点或更早回合的 `confirm_intent`）后才写入 intent；`add_hint` 只承载上下文；保留 `finish`。不执行、不巡检、不跑 plan。主会话转录含人工消息、main chat 回复（`role=main`）、提案卡与 hint 记录；worker/planner 事件按 `agent_id` 分泳道。
- **默认选择**：会话页不自动选主 Agent；打开任务时主区为空态，用户点 run/worker 才展示内容。
- **左栏结构（2026-11-23 两段式树）**：左栏分「会话」与「工作区」两段。「会话」段 = 主 Agent 的 Run 列表，Run 行右侧独立 chevron 展开该 Run 的 worker 子行（Run 行主体负责选中，两个点击目标互不吞并；选中 Run 默认展开）；「工作区」段 = `plan/decision/obligation/workers/system` **叶子行**，点击打开对应七环投影，不显示 chevron。只有真实列表（主 Agent 列表、Run 的 worker 子行）可折叠——模块条目不再「组头 + 同名条目」重复层级。折叠态存 `xalgorix.sessions.groups.<taskId>.main`，worker 展开集合存 `xalgorix.sessions.workers.<taskId>`；折叠组含选中项时组头保留品牌色圆点。
- **Plan 模块**：`/plan` 优先 `v_plan_tasks`；`plan_updated` 由 LLM planner 或 human intent 发出，前端只做失效重读。任务行可显示 `owner`/`lease_until`。
- **Workers 模块**：worker pool 通过 `agentsgraph.Dispatch` 产生标准 `sub_*` 行；`/agents` 与 `/tasks/{id}/workers` 的 `units[]` 按 unit（intent）聚合成可见行，`agents[]` 保留为实例明细；claim 的 intent 由 graph owner/lease 表示，unit 行状态取图状态、不复用 run 状态。所有执行事件流都在 unit 行（`?unit=` 并集），legacy spawn 仍在 agent 行。
- **收工**：main chat 保留 `finish`；planner 在“本轮无新 intent 且 frontier 排空”时用 `[SYSTEM]` 唤醒 main，main 决定是否 finish；有 open/running intent 或未确认提案时 finish 被拒。
- **Planner 活动**：planner 轮次的 `plan_updated` 只做失效；规划文本/JSON 不进入主转录。独立 planner session 的 activity 投影仍是 M3 余项。
- **空态**：`plan.present:false` = 图里没有 intent；`workers.agents:[]` = 当前 run 没有 worker 行；两者都不是前端渲染丢失。
- **边界**：图是 PG 实时权威；PlanTask/PlanView 是视图；事件/图投影不参与判定。详见 `docs/DUAL_GRAPH_ORCHESTRATION.md`。
