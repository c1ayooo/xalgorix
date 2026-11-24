# WorkUnit + Claim — 派活协议设计（M4 扩展，2026-11-23）

## 0. 宪章映射（CORE_PHILOSOPHY v3.0 对齐，2026-11-23）

WorkUnit **不是新顶层原语**（宪章 §2/§8）——它是本仓既有对象的统一派发身份：

| 单位 kind | 宪章对象 | 权威 |
|---|---|---|
| intent（task） | PlanTask（Intent） | `ScanState.Plan`（live）/ `ScanRecord.PlanView`（persisted） |
| belief（hypothesis） | Hypothesis（Belief，六态） | `scanctx.LedgerStore`（ledger.json） |

认领协议作用于这两类对象：**独占 + 租约 + 审计投影**，排序保持确定性（§7.1/§7.2：confidence + 稳定 tie-break；VOI ≤30% 已在 ledger 的 `betterHypothesis` 中）。全部实现保持「纯排序与原子认领分离」。

### 实施状态

| 项 | 状态 | 证据 |
|---|---|---|
| B1 hypothesis 租约/回收/续租 | ✅ | `ledger.go`（`LeaseUntil`、`reclaimExpiredLocked`、`OpReclaimHypothesis`）；`ledger_lease_test.go` |
| B2 plan-task 认领（`claim_next_task`，独占+租约） | ✅ | `planner.go`（`ClaimNext`/`reclaimExpiredTasks`/`taskSubject`）、`plan_tools.go`；`plan_claim_test.go` |
| B2 spawn 去重（活跃 claim 拒绝） | ✅ | `delegation_claim.go`；`delegation_claim_test.go` |
| B3 证据生成计划（确定性）+ 控制面排除 | ✅ | `AutoPlanFromState`/`buildEnginePlan` 过滤 + `XALGORIX_CONTROL_PLANE_HOSTS`；`plan_units_test.go` |
| B4 单位统一投影 | ✅ | `GET /api/scans/{id}/units`（`scan_units.go`，含租约过期标记）；`scan_units_test.go` |
| B4 收工判据 | ✅（既有） | 收工闸门已对 open plan tasks / proven-unreported 拒绝（`hookFinishGatekeeper`/`hookLedgerFinishGate`），本方案不改闸门顺序 |
| R7/M1 图权威接续 | ✅ | 迁移 `0019`–`0023`：`exploration_nodes/edges` 为 intent/belief/fact/finding/goal/hint 权威；PlanTask/PlanView/`/units` 改为 `v_plan_tasks`/`v_units` 视图；`unit_key` 幂等键；`internal/graph` PG CAS claim + lease/reclaim |
| R7/M1 belief/fact 投影 | ✅ | `internal/agent/graph_projection.go`：Hypothesis→belief、Evidence→fact、accepted finding→finding、objective criterion→goal、guidance→hint；边 supports/proves/spawns |
| R7/M2 派发单位 | ✅（默认 legacy） | `internal/orchestrator` worker pool（`XALGORIX_WORKER_CONCURRENCY` 1–10）从图 claim；`agentsgraph.Dispatch`；主 Agent `SurfaceMain`、worker `SurfaceWorker`、planner `SurfacePlanner` |
| R7/M2 收工判据 | ✅（默认 legacy） | `hookPlannerWorkerFinishGate`：图 frontier 仍 open/running/paused 或没有任何 settled intent → 拒绝 finish |


> **立项依据**：TSec 第二轮实测暴露——自动委派波把 specialist 派去侦察平台域名（30+ 分钟 × 1/3 并发）；模型对同一题重复 spawn（a-17、c-09 各有 running+queued 两个 worker）。根因不是模型笨，是**planner 的派活缺少「单位」与「认领」**：spawn 的任务是自由文本，没有独占、没有租约、没有完成判据。本文定义修复协议，分阶段落地，**不改变** Verifier、`report_vulnerability` 闸门与 HITL 语义。

## 1. 问题陈述（与宪章对照）

| 现状 | 宪章冲突 |
|---|---|
| planner 生成 22 阶段模板任务，绑定"扫描目标" | 单位来源是模板而非证据 —— §3「信息为中心」 |
| spawn 自由文本，无独占/租约 | 模型自述（最低权威）驱动执行 —— §5「字段级权威」 |
| 覆盖/判定/收工三处各自从事件推算 | 事件被当状态权威 —— §6 |
| 重复 worker / 平台域名侦察 | 不可验收的"派活" —— §1「每环可被代码验收」 |

## 2. 模型：WorkUnit 一等公民

**定义**：可独立认领、可验证完成、可审计的最小攻击工作单位。

kind 三选一（同构，不是三套系统）：
- `asset`：一个端点/路由/服务/参数（来源：inventory、InfoItem）
- `hypothesis`：一个待证主张（来源：ledger，M1 前置条件）
- `challenge`：一道评测题的容器（来源：challenges API；asset 的特例）

```go
type WorkUnit struct {
    ID         string   // 全局唯一
    Kind       string   // asset | hypothesis | challenge
    Subject    string   // endpoint / hypothesis id / unique_code
    Context    string   // project/run 坐标（InfoItem 七维对齐）
    State      string   // open | claimed | testing | proven | refuted | blocked | closed
    Owner      string   // agent_id
    LeaseUntil time.Time
    Priority   float64  // severity/VOI（≤30% 权重）+ 确定性 tie-break
    Evidence   []string // finding_ref / tool_observation
}
```

**生成规则**：单位由确定性证据生成（inventory 扫描、challenges 列表、覆盖缺口计算），模型不可自由造单位；控制面主机（`XALGORIX_CONTROL_PLANE_HOSTS`）永不生成单位。

## 3. 认领协议（claim）

```go
claim_next_unit(selector, owner) (unit, ok) // 纯函数 + 原子认领；无可用 → ok=false
renew_unit(unit_id, owner) bool             // 续租；失败=失去所有权
release_unit(unit_id, owner, state)         // 完成/失败/弃用
```

规则：
1. **独占**：同一 unit 只有一个 owner（内存原子；PG 写审计投影）
2. **租约**：claim 带 `LeaseUntil`（默认 15 分钟，续租延长）；过期自动回收为 `open`，审计行记录 reclaim
3. **拒绝即给路**：重复认领返回「可领单位列表」，冲突转化为推进
4. **投影非权威**：claim/renew/release 写 PG 审计行；状态权威 = 内存 units 表 + PG 持久态（宪章 §6/§7）

## 4. 与现有组件的映射

| 现有 | 角色 |
|---|---|
| `ledger` / hypothesis | hypothesis unit 载体（M1 补前置条件 + lease） |
| `plan task` | unit 的**投影**（plan view 由 units 推导） |
| `info_items` / domain journal | 单位生成与完成的证据来源；claim 事件写审计投影 |
| `DelegationView` / agentsgraph | owner/lease 运行时权威，与 units 同步 |
| `coverage` / `objective` / `finish gate` | **统一改读 units**（单一事实源） |
| `spawn_agent` | 从"自由任务"改为"认领入口"（携带 unit_id） |

## 5. 分阶段落地（不得跳步）

| 阶段 | 内容 | 验收 |
|---|---|---|
| **B1（M1 收尾）** | hypothesis unit 补 lease/回收；claim 写审计投影 | 超时自动回收可测 |
| **B2（M4，已完成首片）** | `spawn_agent` 认领语义：unit 键提取（`unique_code`/私网 IP）、活跃 owner 独占、重复 spawn 拒绝并提示可领；`DelegationView` 暴露 unit_key | `TestDelegationUnitKey` / `TestDuplicateSpawnBlocked` |
| **B3** | planner 从 units 生成 plan；plan 视图改投影；控制面主机不生成单位 | 同一信息快照 → 同一 plan（确定性测试） |
| **B4** | coverage/objective/finish gate 统一读 units | 「open units=0 或豁免明确」才能收工 |

## 6. B2 首片实现（本次提交）

- `internal/agent/delegation_claim.go`：`delegationUnitKey`（`challenge:<unique_code>` 优先，`endpoint:<ip[:port]>` 兜底）、`duplicateSpawnBlocked`（running/queued 持有；completed/failed 释放）、`shouldBlockForDelegationDuplicate`（BeforeTool 守卫，`⛔ DELEGATION DUPLICATE`）
- `allTargetsControlPlane` + `XALGORIX_CONTROL_PLANE_HOSTS`：全部目标均为控制面时跳过自动委派波（平台域名不再生成侦察 lane）
- 安全视图新增 `delegation` 分类（拦截可见）
- 测试：unit 键提取、活跃 claim 拒绝、完成释放、控制面排除

## 7. 明确不做

- 不引入 A*/MCTS/POMCP/DESPOT（宪章 §10 三条件不满足）
- 不改 Verifier 判定、`report_vulnerability` 闸门顺序、HITL 审批语义
- claim 不把事件当状态权威（审计只是投影）
- 草案不顺手改 M0–M3 范围
