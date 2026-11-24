# PG 主存储规格（R6，宪章 v3.0 配套）

> **地位**：实现层规格。依据 2026-11-22 用户裁决（路线 B：修宪，面板数据 PG 主存储）。
> 修订 `docs/CORE_PHILOSOPHY.md`（v2.1 → v3.0 §5/§6/§9）与 `REDESIGN.md` D1 → D12；红线修订见 REDESIGN §10。
> 与宪章信息层分开提交（宪章 §10.2）。

## Problem Statement（问题陈述）

一次 Run 在续跑（1:1 re-dispatch，D11）后名下会出现多个 sibling 会话目录，状态与面板数据
（events / domain journal / ledger / intelligence / run 状态）散落其中。每一个读端点各自"任绑
一个目录"且绑定互不一致，续跑后任务详情看板大多为空；每新增一个消费端都要重新发明 sibling
合并逻辑（09d06a3e、本会话读层修复是同一类补丁的第 3、4 次）。根因不是某个 bug，而是存储
形态：**以每会话目录为持久单元，"run 的真相"没有单一权威存放处**。

## Solution（方案）

状态与面板数据全部进 PostgreSQL，以 run 身份（`instance_id`）打键；文件只承载字节产物
（工具输出归档、流量原始包、上传物、交付物、沙箱工作区）。引擎保持内存权威 + PG 同步持久化
（变更边界事务写，幂等键 `(scan_id, seq)` + 行级 `instance_id` 列）；PG 写失败不阻断扫描，
reconciler 按既有 M3 水位协议追赶。PG 成为必需依赖（无 DSN fail-fast + docker-compose 模板）。
`scan.json` 停写，runs 表接管；存量目录由一次性导入器补行。续跑重放升级为 PG 全历史（保尾预算
不变）。完成后，读层 sibling 合并辅助代码退役，"选哪个目录"类 bug 从结构上消失。

## User Stories（用户故事）

1. 作为操作者，我希望续跑一个任务后，会话/对话/探索链路/播报板/安全链/情报/概览立刻呈现该 run
   的全部历史，而不是只剩最新会话的空数据。
2. 作为操作者，我希望对话页面在我续跑后既显示以前的对话、也实时显示新会话的进展。
3. 作为操作者，我希望情报面板能看到所有会话（含续跑会话）产生的信息项，不因会话切换而丢失。
4. 作为操作者，我希望概览的 objective 覆盖进度在续跑后保持累积口径，而不是被新会话清零。
5. 作为操作者，我希望重启引擎后所有面板数据原样可用，不依赖扫描目录是否还在。
6. 作为操作者，我希望部署只需提供一个 PG 连接串（或用仓库 compose 模板一键起），无 DSN 时
   启动给出明确错误与指引。
7. 作为操作者，我希望存量扫描升级后仍可在面板中看到（导入器迁移，目录保留供文件面板）。
8. 作为操作者，我希望 PG 短暂不可用时正在跑的扫描不被打断，恢复后数据自动追平，缺口如实标注。
9. 作为操作者，我希望删除 run 时其全部面板状态行随之清理（PG 行随 run 生命周期，目录按
   retention 既有策略）。
10. 作为开发者，我希望新增面板消费端时只需要按 `instance_id` 查询，不必再实现 sibling 目录
    合并或目录解析。
11. 作为开发者，我希望状态权威只有一处（PG 持久态 + 进程内权威），"哪个副本是真的"不再成为
    评审议题。
12. 作为开发者，我希望引擎热路径的持久化是接口化的，测试可以注入真 PG（既有集成测试模式）。
13. 作为 AI Agent（search_info 等激活消费者），我希望跨 run 检索与面板读同一权威存储，不再
    出现"文件有但白名单查不到"的盲区。
14. 作为操作者，我希望文件面板仍然浏览扫描工作区的真实文件（工具输出、流量原始包、上传物）。
15. 作为操作者，我希望 verifier 复测与收工闸门的行为与升级前逐字节一致（金样本与闸门测试钉住）。

## Implementation Decisions（实现决策）

全部为 2026-11-22 grilling 两轮裁决（每条均有用户"按推荐"确认）：

1. **PG 必需依赖（修 §6.6）**：无 DSN 启动 fail-fast，错误信息含指引；仓库提供
   `docker-compose.yml`（app + PG）。单二进制形态保留。
2. **对象清单**：进 PG = run 状态（原 scan.json 全字段）、events（对话/遥测 journal）、
   domain journal（领域审计）、ledger（假设/证据状态权威）、intelligence、notes、coverage
   计数、queue_state、dispatch 快照、token 归档。留磁盘 = 工具输出归档、流量原始包
   `tr_*.json`（traffic 面板读 PG 摘要行 + 引用归档）、uploads/context、logo、报告交付物、
   扫描工作区目录本身（沙箱 scratch）。
3. **写入形态**：内存权威 + PG 同步持久化。变更边界仍走现有持久化调用点，后端从文件原子写
   换成 PG 事务；读永远不碰网络。幂等键保持 `(scan_id, seq)`（会话内 seq 语义不变，journal
   代码零改动），新增行级 `instance_id` 列供 run 级聚合；fill-once `legacy_scan_id` 关联
   退役（写入时由 scanctx 直接携带 instance_id）。
4. **PG 写失败姿态**：继续跑。失败记 `failed` 水位 + 审计可见；恢复后 reconciler 以幂等键
   补写（M3 `pending/applied/gap/failed` 协议扩展到全部状态表）；崩溃窗口未持久化尾部如实标
   `gap`，不假装完整（宪章 §5.2 纪律不变）。
5. **runs 表接管 scan.json**：`scan.json` 停写；字段进 runs 表（列 + jsonb，DDL 以迁移文件
   为准）；orchestrator/投影改读 PG。`platform.json` 文件仓退役为只读迁移源。
6. **存量迁移**：一次性导入器（legacy 导入器模式）解析存量目录状态文件补进 PG；目录保留。
7. **续跑重放升级**：L2 热启动从 PG 读该 run 全部历史 events（96K chars 保尾预算不变）；
   ledger 加载、checkpoint 恢复改从 PG（checkpoint 已有 PG store）。
8. **面板读路径**：events/transcript/exploration/security/intel/objective 改读 PG；
   读层 sibling 合并辅助代码（sessionDirsForProjection 等）退役；文件/归档解析保留目录解析。
9. **批次切分（R6.0–R6.3，不得跳步）**：
   - **R6.0 底座**：迁移 DDL、启动 DSN 必需门、compose 模板、platform.json → 迁移源、
     导入器。验收：无 DSN 启动被拒且报错可读；导入器把存量目录补行后旧面板读数与迁移前一致。
   - **R6.1 状态权威切换**：ledger/journal/events/intelligence/notes/coverage 写入切 PG，
     runs 表接管 scan.json。验收：金样本调度对等 + Verifier 时序门 + 追赶协议四态测试全绿。
   - **R6.2 面板读切换**：面板端点改读 PG；本会话回归回路平移为「写 PG → 读 PG」端到端。
     验收：`redispatch_projections_test` 九个子测试语义不变地通过。
   - **R6.3 收口**：读层合并辅助代码退役、retention 扩展（PG 行随 run）、文档回写
     （ARCHITECTURE/API_CONTRACT）、`make build` 全门禁。

   **批次状态（2026-11-23，以代码为准）**：
   - **R6.0 ✅**（`0012`/`0013` 迁移 + DSN 必需门 + `deploy/docker-compose.yml` +
     `internal/runstate` 目录导入器，`dir_import` watermark 防重跑）。
   - **R6.1 ✅ 主体**：ledger（`scanctx.SetLedgerBackend`）、events（`web.SetEventStateBackend`
     → `run_events`，seq = 1-based journal 行序）、domain journal（`domainjournal.SetBackend`
     → `run_journal`，`ReadRecordsResolved` 后端优先、reconciler 增 PG 源枚举）、
     scan.json 接管（`0016_run_scan_records.sql` + `web.SetScanStateBackend`，记录安全点
     单事务写记录行 + 顶层 runs 接管列，`findAllScanSummaries` 全量集改 PG sweep 优先）。
     `main.go` 注册四后端 + 启动期 `ImportDataDir`。**缺口：intelligence/notes/coverage 三快照
     的接缝与仓内接线、测试已落地（`intelligence.Backend` + `scanctx.StateFilesBackend`，
     runstate 对前者有适配、对后者缺 seq 签名适配），但 `main.go` 未注册生产后端——生产
     仍写三个 JSON 文件，R6.3 收口必须补注册 + session 侧 instance 戳**。
   - **R6.2 ✅**：events 窗口/对话转录/安全链/探索链路/播报板/任务情报/任务详情/续跑重放
     全部改读 PG（本 scan 行 → 同 instance 兄弟联合 → 遗留文件仅 pre-import 兜底）；
     回归平移 = 新增 `redispatch_projections_pg_test.go`（PG 双胞，DSN 门控）九子测试 +
     `TestScanLedgerPanelReadsPGSnapshot` 语义不变通过，原文件模式九子测试保留为
     nil-backend 孪生；retention/手动删除接入 `deleteScanRecordState` +
     `deleteDispatchSnapshotState`（PG 行随 run 生命周期，user story 9）。
     验收实测（本地 PG 实库）：金样本 `TestGoldenSchedulingParity` ✓、Verifier 时序门
     （eino 两测试）✓、runstate/web/domainjournal/platform DSN 门控集成测试 ✓、
     `go build ./...` ✓（coldpath 全包 run 有两个 River 测试在共享库上互相争抢 worker
     的隔离性 flake，单跑均过，非本批回归）。
   - **R6.3 状态（2026-11-23 回写）**：三快照生产注册已补齐、ARCHITECTURE/API_CONTRACT
     已回写、门禁已跑。读层 sibling 合并辅助代码退役已评估：**保留为 pre-import
     文件兜底**——`sessionDirsForProjection` 只在没有 PG 行（未导入的遗留目录）
     时使用；有 PG 行时 security/transcript/events/scan-list 均走 instance 联合
     读，不再枚举 sibling 目录。该结论已写入 `CORE_PHILOSOPHY.md` §17.1。
10. **并行线协调**：finding 身份键重构（findingContentKey）进行中；run_findings 升级为
    findings 唯一存储（scan.json vulns 停写）落在 R6.1，以该重构落定的键为准。

## Testing Decisions（测试决策）

只测外部行为；五个接缝全部复用既有先例，不新建接缝：

1. **引擎状态持久化后端接口**（scanctx 持久化点）：`TestGoldenSchedulingParity` 逐字节不变、
   Verifier 时序测试不变；PG 集成测试按 infoitem/postgres 集成测试模式（DSN 测试库）。
2. **平台仓**：repository 测试改 PG-only；platform.json → 导入源用 fixture 目录断言迁移前后
   面板读数一致。
3. **追赶协议**：四态水位 + 幂等重投 + 崩溃窗口 gap 标注（infoitem reconciler 测试模式）。
4. **续跑重放**：`redispatch_resume_test` 改 PG 数据源，断言跨全会话聚合。
5. **面板投影**：`redispatch_projections_test` 九个子测试平移为「写 PG → 读 PG」端到端。
   CI 增加 PG service 容器。

## Out of Scope（不做）

pgvector（仍需具体消费者基准）；跨存储分布式事务；A*/MCTS 研究项（宪章 §12 不变）；
Verifier 与 13 道闸门行为变更；文件面板/工具输出/流量原始包/上传物迁移；多进程并行写同一
run；审批/guidance/intel triage（已在 PG，不动）；定时调度 Run 1:N 语义（D11 保留）。

## Further Notes（备注）

- 修宪落点：宪章 §5.1（状态权威）、§6 全节（真相源/幂等/追赶/降级）、§6.6（PG 必需）、
  §12（跨存储事务仍不做）；REDESIGN D1 → D12、§3.2 schema、§10 红线。
- 本会话已落地的读层修复（09d06a3e 延伸）是 R6.2 前的过渡正确性保障，R6.2 后退役其合并逻辑
  但测试语义保留（平移到 PG 数据源）。
- 单二进制保留：go:embed 前端 + 自包含引擎不变；"PG 必需"是部署要求，不是进程形态变化。

## 追加：计划明细投影的 R6.1 迁移项（2026-11-23）

`GET /api/scans/{id}/plan` 需要计划任务明细（`Plan.Task[]`），但当前 `scan.json` 只持久化
`plan_present/plan_tasks_*` 五个计数。2026-11-23 的实现把 `agent.PlanView` 写进
`ScanRecord.PlanView`（与计数同一终态写入点，随既有原子写落盘），**这是过渡实现，不是第二套持久权威**。

R6.1 切换状态权威时必须一并迁移：

- 落点：PG。建议 `run_plan_tasks(run_id, task_id, title, phase, vuln_class, endpoint, status,
  depends_on[], notes, disposition, origin, prerequisite, whole_target, updated_at)`，
  或 `runs.plan_view JSONB`（任务数有界，二选一按 R6.1 批次裁决）。
- 写入点不变：仍是终态 safe point（`capturePlanDisposition`）；幂等键 `(scan_id, seq)` + 行级
  `instance_id` 盖章遵循本规格 §3。
- 读切换：`GET /api/scans/{id}/plan` 的"内存 → 记录"两跳改为"内存 → PG"，`PlanRecord.PlanView`
  文件字段随 `scan.json` 停写一并退役；`partial_reason:"not_persisted"` 只保留给迁移前的历史 run。
- 验收：金样本 plan（engine/model/mixed、依赖、typed disposition、空计划）+ 杀死进程后续跑，
  明细与迁移前一致；`partial` 语义不回归。

**R6.1 落地注记（2026-11-23）**：迁移取了第三种形态——`PlanView` 随 **verbatim 记录 JSON**
存进 `run_scan_records.scan_detail`（TEXT，迁移 `0016`），不设独立 `run_plan_tasks` 表、
不动 `runs.plan_view`。`GET /api/scans/{id}/plan` 的读链已是"内存 → 记录"，而记录读取经
`web.SetScanStateBackend` 先解析持久行（`handlers_scan_plan.go`：live → record →
counters-only `partial_reason:"not_persisted"` 兜底），`scan.json` 停写后明细天然随行。
任务数有界 + TEXT 整读整写（`0013` 字节保真纪律），独立表拆分无消费者需求，不再立项。
