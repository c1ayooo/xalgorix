# 双图编排（R7 / M1–M2）实现规格

> 状态：**M1–M2 已实现；planner_worker 是内置默认拓扑，`XALGORIX_ORCHESTRATOR=legacy` 仅为隐藏的紧急回滚**。
> 上位约束：`docs/CORE_PHILOSOPHY.md` v3.0、`docs/REDESIGN.md` D1–D12、
> `docs/WORK_UNIT_DESIGN.md`、`docs/PG_PRIMARY_STORAGE_SPEC.md`。
> 本文记录实现事实与**必须同步修订的 CORE 例外**，不替代宪章正文。

## 1. 启用方式

**planner_worker 是内置默认拓扑，不是设置项。** 启动即生效：

```bash
./build/xalgorix --web
```

- `XALGORIX_WORKER_CONCURRENCY`：1–10，默认 3；可在设置页 Runtime 分类修改，新扫描生效。
- `XALGORIX_ORCHESTRATOR`：**仅供紧急回滚**的隐藏环境变量；默认 `planner_worker`，设为 `legacy` 才回到旧单根 Agent 行为。它不出现在设置页。
- planner_worker 需要 `XALGORIX_DATABASE_URL` 与已应用的迁移 `0019`–`0023`（PG 本来就是必需依赖）。

## 2. 权威模型（实现事实）

| 对象 | 权威 | 代码 |
|---|---|---|
| Intent | PG `exploration_nodes(kind='intent')` | `internal/graph/pg.go` |
| Belief | PG `exploration_nodes(kind='belief')`；Evidence 明细仍在 ledger | `internal/agent/graph_projection.go` |
| Fact | PG `exploration_nodes(kind='fact')`，引用 evidence/InfoItem | 同上 |
| Finding | 报告门成功后投影为 `finding` 节点 | 同上 |
| Goal | `objective.Evaluate` 写 `goal` 节点状态 | 同上 |
| Hint | `source=human` 的 guidance → `hint` 节点 | `internal/web/graph_guidance.go` |
| Asset | PG `assets` / `asset_edges` / `task_asset_links` | `internal/assets` |
| Activity | `run_events` / 转录 | 审计投影 |

**PlanTask 是 intent 节点的读写视图**（migration `0021` 的 `v_plan_tasks`）；`build_plan` / `update_plan` / `claim_next_task` 通过 `internal/agent/plan_graph.go` 读写图节点，不再拥有独立的持久权威。

**图节点业务键**：`unit_key` + `(scan_id, kind, unit_key)` 部分唯一索引（migration `0023`）。intent=task id，belief=hypothesis id，fact=evidence id，finding=finding id，goal=criterion id，hint=guidance id。

**run 身份**：`scan_id` 列存 D11 `instance_id`；续跑/兄弟会话共享同一份图计划（`ScanContext.InstanceID`）。

## 3. 执行拓扑（planner_worker）

```text
main chat (per user message; SurfaceMain)
  ├─ user message → exactly one lightweight chat turn
  ├─ propose_intent → 操作者确认（卡片端点 / 更早回合 confirm_intent）
  │    └─ confirmed → add intent (origin=human, high priority)
  ├─ add_hint (context only)
  └─ finish only after a [SYSTEM] frontier-drained wake; never executes tools

planner loop (orchestrator.Planner)
  ├─ fresh LLM call per round (heartbeat 60s + worker-settled notify)
  ├─ reads graph counters/plan/ledger/findings/hints
  ├─ writes 0..4 intents + retire list (strict JSON)
  └─ never executes tools / never reports findings

worker pool (orchestrator.Pool, N = 1..10)
  ├─ PG CAS claim exploration_nodes(kind='intent', state='open')
  ├─ agentsgraph.Dispatch(name, task, targets, unitID)
  ├─ wait for result; mark graph intent done/blocked/exhausted
  └─ notify planner only
```

**Worker 可见身份 = unit（2026-11-24）**：`Dispatch` 的 `unitID` 贯穿 agentsgraph 快照、`agent_*` 生命周期事件与 `run_workers.unit_id`（migration 0029）。面板（`/agents`、`/tasks/{id}/workers`）以 `units[]` 呈现一个 intent 一行，图状态为主、agent 执行状态为明细；重派/重试聚合同一行。转录 `?unit=` 取该 unit 所有 agent 的事件并集后再分页。状态权威仍是 agentsgraph（live）+ PG（终态），这套投影只是身份聚合，不产生第二套状态。

### 主 Agent（main chat）

按用户消息触发，不是持续扫描循环：
- 具体工作先调用 `propose_intent` 生成**草案**（进程内、不写探索图），并明确告诉操作者需要确认；不再直接调用 `add_intent`（该名字从工具面移除，误调返回错误）。
- 确认路径二选一：转录提案卡片的 confirm 端点（确定性，不经模型），或**更早回合**的明确肯定消息触发 `confirm_intent`。同回合提案 + 确认一律拒绝；确认后才写入 `origin=human` intent。
- `add_hint` 只承载上下文/偏好，不得写成具体工作项；`finish` 在 frontier 仍有 open/running/paused intent 或存在未确认提案时被拒绝。
- 用户消息进入 `mainInbox`；每条消息最多 6 个模型回合。
- planner 在“本轮无新 intent 且 frontier 为空”时通过 `OnDrained` 发 `[SYSTEM]` 唤醒 main，main 再决定是否 finish。
- planner 只消费 active hint；注入到主 Agent 的 live guidance 不再自动投影为 hint（否则 planner 会绕过确认自行生成同一 intent），只有 queued guidance 保留自动 hint 投影。

### Worker 工具面（`SurfaceWorker`）

允许：执行工具、`add_note`、`record_hypothesis`、`add_hypothesis_evidence`、`claim_next_hypothesis`、`read_info_item`、`record_deviation`、`report_vulnerability`。
禁止：`spawn_agent`/`create_agent`/`check_agent`/`wait_agent`、`build_plan`、`add_intent`、`retire_intent`、`steer_work`、`kill_work`、`claim_next_task`、`graph_overview`、`finish`、`update_plan`。

> 意图由 scheduler settle，worker 不写计划状态；这避免两个 worker/模型共同改同一 intent。

### 主 Agent 工具面（main chat）

实际工具（直接 LLM tool calling，不经 registry 执行面）：`propose_intent`、`confirm_intent`（仅在存在 pending 草案时提供）、`add_hint`、`finish`，以及只读 `list_intents`/`list_units`/`list_findings`/`list_hypotheses`/`read_info_item`（`read_*` 前缀自动可用）。
禁止：terminal/http/browser/scan/verify、`report_vulnerability`、`build_plan`、`add_intent`（直接写图）、`spawn_agent`、`steer_work`/`kill_work`（worker 控制不通过 main chat 暴露；需要进程级停止用 Stop）。

### Planner 工具面（`SurfacePlanner`）

允许：`read_*`、`list_*`、`search_*`、`graph_overview`、`read_plan`、`build_plan`、`update_plan`、`add_intent`、`retire_intent`、`steer_work`、`kill_work`、`add_note`。
禁止：执行工具、`report_vulnerability`、delegation。

> 工具面由 `tools.Registry.SetToolFilter` + `Retain` 在注册期强制；不是提示词约束。

### 任务意图与控制面保护（2026-11-23）

首版 planner/worker 把 `sess.target` 当成“扫描目标”，实测 worker 直接 `curl https://tsecbench...`（控制面），违反了任务提示词里的“平台域名只用于 API”。根因不是提示词没写，而是框架把控制面当成了合法 worker target。现改为**任务意图驱动 + 每个 intent 自带 target**：

- **Planner-first**：planner 先读任务创建时的 goal/instruction（权威用户意图）、notes/ledger/findings/图；没有 `recon` seed，不再从“扫描目标”反推任务。
- **Intent 自带 target**：每个 intent 必须带 `target` + `target_kind`：
  - `control_plane_api`：平台 API 调用；只允许 orchestrator surface（`http_request` + notes），物理上没有 terminal/browser/scan。
  - `container` / `web` / `task`：真实攻击目标，worker 以 intent target 为作用域。
- **框架拒绝**：planner 拒绝“控制面 host + 非 API kind”的 intent；pool 对没有 target、或把控制面当 scan target 的 intent 直接 settle blocked，不再回退全局 scan target。
- **CTF 流程**：planner 先产出 API orchestration intent（list/start/submit/close）→ orchestrator worker 调 API 并把 container_addr 写 notes → planner 读到 notes 后产出 `target_kind=container` 的 intent → worker 攻击 container。
- 普通 Web 任务同样由 planner 从任务提示词产出 `target_kind=web` 的 intent，不依赖全局 scan target。

> 这取代了“控制面-only → legacy”的临时兜底；planner_worker 自己处理任务，不假设“扫描”是默认动作。

## 4. 闭幕与完成

- `hookPlannerWorkerFinishGate`（`internal/agent/hooks.go`）：
  - 图里还有 open/running/paused intent → 拒绝 finish；
  - 没有任何 done/blocked intent → 拒绝 finish；
  - 其余交给既有 ledger/goal 闸门。
- Verifier、`report_vulnerability` 闸门顺序、HITL、SSE 单向、扫描隔离不变。
- 图不可读写时 `ScanState.GraphUnavailable` → `graph_unavailable` → 可恢复 pause。

## 5. 读 API

- `GET /api/scans/{id}/graph`：节点 + 边 + counters + decoded payload。
- `GET /api/scans/{id}/plan`：优先 `v_plan_tasks`。
- `GET /api/scans/{id}/units`：优先 `v_units`。
- `GET /api/scans/{id}/assets`：本 run 链接的资产。
- 前端 `?run=&agent=` 语义不变；主会话过滤已识别根 `agent_<ts>`。

## 6. 必须同步修订的 CORE 例外

> 这是实现与现行宪章文本的**显式例外**，不是静默偏离。M3 收口时必须回写正文。

1. **§2 对象映射**：Intent = exploration `intent` 节点（PlanTask 为视图）；Belief = exploration `belief` 节点（Hypothesis 生命周期在节点，Evidence 明细仍在 ledger/InfoItem）；Fact/Finding/Goal/Hint 见图。
2. **§5 字段级权威**：探索对象生命周期归图；finding 接受归报告门；goal 判定归确定性 evaluator；InfoItem 标注归 InfoItem；模型自述最低。
3. **§6/D12**：探索图与资产图是 **PG 实时权威（读写 PG）**；其余扫描状态仍内存+PG；事件/图投影不是决策权威。PG 不可用时 run 暂停（可恢复），不以内存计划静默续跑。
4. **M0–M5 顺序**：本实现以 M0–M5/R6.3 已落地为前提；Eino 单运行时、Verifier/闸门冻结、InfoItem observed/inferred 语义不变。

## 7. 已知缺口（M3 收口）

- **R8 契约与运行期合规**见 `docs/INFO_GRAPH_CONTRACT.md`：`done` intent 只能经 `ConcludeIntent` 事务产出 `yields`；不变量与漂移由 `graph.ValidateStore` / `GET /api/scans/{id}/graph/validate` 只读暴露，检查器不修数据。
- CORE_PHILOSOPHY 顶部已加 v3.1/R7 修订块（§2/§5/§6 原段落未逐段改写，以修订块为准）；REDESIGN 已加 R7 节。逐段正文改写仍可在后续文档批完成。
- `XALGORIX_WORKER_CONCURRENCY` 已进入设置页环境变量（按分类自动渲染），保存后对新扫描生效；拓扑本身不再作为设置项。
- 资产摄取已接 raw traffic/tool-output observed（`internal/assetobs`，按会话去重、InfoItem 为 provenance）与 `record_asset` 工具（worker 面，可带 `info_item_id`）；`engine_inventory` derived 仍在。跨任务资产运营 UI 未启用。
- Planner 是 bounded 工具循环（≤4 轮）：`graph_overview`/`read_plan`/`read_info_item` 只读，`add_intent`/`retire_intent` 写单个意图；最终仍以 strict JSON 收口，写入统一经 `apply()` 的 caps/dedupe/控制面过滤。预取 overview 仍作首轮上下文。
- `steer_work`/`kill_work` 已接到主 Agent 的 graph unit 句柄（`agentsgraph.SteerUnit` / `KillUnit`，按 plan task id 寻址）；planner 的 steer/kill 仍走单次 JSON 调用，不执行工具。
- digest 冷压缩、main_sessions 分段、跨任务资产运营 UI 未启用。
- planner_worker 的真实 run 验收（普通目标 + benchmark）仍待操作者执行；它已是默认拓扑。
- 4zreco 为 AGPL-3.0：本实现为语义参考，clean-room 重写，未复制源码。
