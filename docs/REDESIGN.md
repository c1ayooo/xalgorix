# Xalgorix 二开架构方案（REDESIGN）

> **地位**：实现层设计总档。服从 `docs/CORE_PHILOSOPHY.md`（v2.1 宪章）与 `AGENTS.md`；与宪章冲突时宪章赢，与代码现状冲突时本文是改造目标。
>
> **裁决澄清（2026-11-21，用户原话"这是要做的内容"）**：宪章 v2.1 的落地顺序（P0→M0–M5）是**信息层**的验收顺序；**项目/任务管理、流量代理/HITL、Eino 重编排是二开要做的内容**（R 系列里程碑照旧推进），不属于宪章 §12「明确不做」。曾有一次基于误读的入口摘除已当日全部恢复（git 历史可查）。本文 D1–D9 决策继续有效；R3/R3.1 前端已建成并接入路由。
>
> **主语**：本方案 = 在七环主循环与三原语（State / Operation / Obligation）之上，加四层工程承载：项目/任务管理（平台层）、流量代理 + HITL（约束层）、Eino 重编排（引擎层）、前端改造（呈现层）。信息层不在此重复设计，按宪章 M0–M4 执行。

---

## 1. 决策记录（ADR 摘要，2026-11-20 会话确认）

| # | 决策 | 内容 |
|---|---|---|
| D1 | 存储 | PostgreSQL 只做冷路径（InfoItem/关系边/审计投影/项目/任务/审批）；热路径 JSON 原子写不变；无 DSN 降级纯文件（宪章 §6） |
| D2 | 队列 | River 仅异步补全层（`internal/jobs`）；禁接管扫描生命周期/HITL/agentsgraph/WS（宪章 §6） |
| D3 | 引擎 | Eino **核心重编排**：ADK React Agent 重写主循环，工具→Eino Tool，安全闸→Callback/Middleware；legacy 引擎保留双跑开关直至金样本对等 |
| D4 | 代理 | 流量代理**默认打开**：一切出站 HTTP 经本地 MITM 代理，归因到 Run |
| D5 | HITL 范围 | 命令执行类工具 + 高危 HTTP 请求进审批；GET/被动侦察不拦；**超时 = 拒绝** |
| D6 | 项目模型 | Project → Task → Run 三层；Task:Run = 1:N；Schedule 降为任务属性；InfoItem 归属 Project，`task_id` 是情境坐标维度 |
| D7 | 隔离 | 数据（行级 project_id）/范围（项目级 scope）/凭据（项目私有）/审批（按项目过滤）/UI（项目切换器）五维隔离；跨项目仅 OpsPulse 待审批计数 |
| D8 | 前端 | 在现有 Vite SPA + go:embed 骨架与 xalgorix 令牌上改造；借 4zreco 模式（态势条/审批队列/四态纪律/深链），不搬框架与视觉；zh-CN 默认；遵循 `UI_DESIGN.md` |
| D9 | 研究项 | A*/MCTS/POMDP 不进路线图（宪章 §7 三条件）；VOI 仅加权排序 |

---

## 2. 目标架构总览

```
webui（React SPA，项目切换器 + 任务页 + 审批 UI + 信息全景）
   │ REST + WS
internal/web（路由/编排/队列/恢复 —— 保留，多项目化）
   │
   ├─ internal/platform（新）：projects/tasks/runs CRUD、审批 API、PG 访问、River jobs
   ├─ internal/engine（新，R4）：Eino ADK 主循环
   │     ├─ ChatModel 适配器（包 internal/llm：多协议 + KeyRotator 保留）
   │     ├─ 51 工具 → Eino InvokableTool（经 registry 包装）
   │     ├─ 安全闸六环 + 审批闸 → tool Callbacks（不可绕过）
   │     └─ Obligation（阶段/收工/覆盖/hook/审批）→ Middleware/节点
   ├─ internal/trafficproxy（新，R2）：本地 MITM 代理，默认开
   ├─ internal/scanctx 热路径（不变）＋ InfoItem 冷路径落 PG（M4）
   └─ PostgreSQL（冷路径系统记录）＋ River（异步补全）
```

---

## 3. 项目 / 任务 / 执行模型（D6/D7）

### 3.1 实体与挂载

- **Project**：授权边界。`scope`（目标清单 + 允许端口）、凭据（项目私有，新表；现有全局 `auth-profiles.json` 不动）、审批队列、信息空间归属。
- **Task**：Goal + Context 宿主（instruction、上下文上传物、phase 策略、验收说明、schedule 属性）。跨 Run 的累积视图全部是**冷路径投影**（任务级 findings = 各 Run findings 的 PG 投影 + 去重/升级），不合并热路径账本。
- **Run**：现 Scan 降级。`scanctx` / `scan.json` / `ledger.json` 热路径形态**原样保留**，Run 是它们的挂载点与 PG 关联点（`runs.legacy_scan_id` 兼容旧记录）。

### 3.2 PG schema（冷路径，迁移文件管理）

```sql
projects(id uuid PK, name, slug UNIQUE, scope jsonb, created_at, archived_at)
tasks(id uuid PK, project_id FK, name, goal text, context jsonb,
      phase_policy jsonb, schedule jsonb NULL, created_at, archived_at)
runs(id uuid PK, task_id FK, legacy_scan_id text NULL, mode text,
     status text, scan_dir text, started_at, finished_at)
info_items(id uuid PK, project_id FK, task_id NULL, run_id NULL,
      kind text CHECK(kind IN ('observed','inferred','human')),
      content jsonb, source jsonb, subject jsonb, purpose text,
      context jsonb, confidence real, valid_from, valid_until, created_at,
      scan_id text NULL, seq bigint NULL, UNIQUE(scan_id, seq))  -- 幂等 upsert 键
info_edges(id bigserial PK, project_id, src uuid FK, dst uuid FK,
      kind text CHECK(kind IN ('supports','contradicts','references','same_session')),
      weight real, created_at, UNIQUE(src, dst, kind))
approvals(id uuid PK, project_id FK, task_id FK, run_id FK,
      kind text CHECK(kind IN ('command','http')), payload jsonb, slice jsonb,
      status text CHECK(status IN ('pending','approved','rejected','timeout','cancelled')),
      deadline_at, decided_at, decided_by, reason, created_at)
-- 审计投影表（只读，M3）：events_proj 等；River 自带 river_job
```

### 3.3 隔离与降级

- 所有冷路径查询强制 `project_id` 条件（代码层 repository 统一注入，禁止裸 SQL）。
- scopeguard 授权边界升级：项目 `scope` 是目标合法性的来源之一（任务级 targets 必须落在项目 scope 内）。
- **无 DSN 降级**：单隐式项目 `default`，项目/任务/审批 UI 显示为单项目模式，扫描能力不减（宪章 §6 红线）。

---

## 4. 流量代理（D4，R2）

- **形态**：单进程共享 loopback MITM 代理（`internal/trafficproxy`）。归因：scanctx 为每 Run 签发 `Proxy-Authorization: Bearer <run_token>`，代理侧还原 project/task/run。
- **接入点（默认开）**：httpclient / send_request / browser（`launcher.Proxy`）/ terminal（curl/wget 包装脚本与 python sitecustomize 注入 `HTTP(S)_PROXY`，与现有速率包装同一挂点）。
- **出站链**：trafficproxy →（代理池轮换 | egressproxy | 直连），`ProxyRequired` fail-closed 语义保留；代理上游拨号仍过 `GuardDials`；scopeguard 六环**全部保留在前置层**，代理只做内容观测与高危判定，不接管 scope 拦截。
- **TLS**：安装级 CA（`~/.xalgorix/ca`，0600）；浏览器上下文与 httpclient transport 信任；curl 包装加 `--cacert`。
- **产出**：每条请求/响应 → observed InfoItem（七维坐标免 LLM 提取：主体=host/endpoint、来源=tool+run、情境=phase）+ 审计投影。

## 5. HITL 审批管线（D5，R2 起）

### 5.1 语义

- 审批 = **Operation 的前置 Obligation**（引擎硬约束）；裁决结果落 `source=human` 的 InfoItem（经区域点亮回到循环视野）。
- 范围：`command` = terminal_execute / python_action / browser 危险操作，命中只读侦察白名单（被动 subfinder/httpx、`nmap -sV` 类，policy 可配）自动放行；`http` = in-scope 但高危——写方法（POST/PUT/PATCH/DELETE）+ 高危特征（管理面路径、破坏性载荷特征、认证变更操作），GET 默认放行记录。
- **超时 = 拒绝**：`XALGORIX_APPROVAL_TIMEOUT`（默认 600s，任务级可覆盖）；到时写 `timeout` 状态，agent 收到含原因的拒绝 tool-result，走替代路径。

### 5.2 接入（两步）

1. **R2（先于 Eino）**：在 `registry.Execute` 前插审批闸（与现有六环同层的第 0.5 环），阻塞等待 channel 或 deadline；approvals 表 + WS 事件 + REST API 先行落地——安全收益不等引擎重写。**R2a 已落地（2026-11-21）**：闸装在 `tools.Registry`（`SetApprovalGate`，熔断/参数校验之后、`tool.Execute` 之前——执行前最后一环；拒绝走 `Result{Success:false}` 不烧熔断器）；判定与白名单在 `internal/approval`（只读侦察白名单为「尽力而为分类器，存疑即进审批」，默认名单不含 awk/sed/sort，`$()`/未引用重定向/`system(`/`PATH=` 冒名等段形状一律拒绝）；存储 `0003_approvals_guidance.sql` + `_platform/platform.json` 双实现，启动期孤儿 pending 对账；payload 持久化前经 agent secretValues 按值脱敏 + header 名单打码（红线：API 永不回传密钥）；**verifier registry 免审批**（report 闸门内审批卡死会撞 15 分钟 watchdog 竞态；闸的同步阻塞语义恰好保持 verifier 时序不变量）；`XALGORIX_APPROVAL_MODE` 默认 **off**（D5 的「默认」约束拦截范围；默认 enforce 会让无头定时扫描每条非白名单命令 pend 600s），env 或 Settings 置 `enforce` 启用。
2. **R4（Eino 到位后）**：闸迁入 tool Callback，等待语义换 `adk.Interrupt` + checkpoint（PG 持久化，跨重启审批不丢）+ `runner.Resume`；R2 的表与 API 不变，前端无感。

### 5.3 状态机

`pending → approved | rejected | timeout | cancelled`；Run 停止时未决单批量 `cancelled`；同一 Run 同时最多 N 张未决单（默认 1，防审批风暴阻塞）。

## 6. Eino 重编排（D3，R4）

- **包**：`internal/engine`，Eino ADK（`adk.NewChatModelAgent`，钉版本；v0.9.x agentic runtime）。
- **ChatModel 适配器**：实现 Eino `ToolCallingChatModel`，内部转发 `internal/llm.Client`（四协议、KeyRotator、重试/退避、token 记账全保留）。
- **工具**：registry 的 51 个工具逐个包成 `tool.InvokableTool`（参数保持 string 透传，不重造 schema）；XML 方言保留为弱模型兼容开关，native function calling 成为主路径。
- **不可绕过的约束层**：六环安全闸 + 审批闸 = tool OnStart Callback（判定/拒绝/挂起）；Obligation（阶段义务、收工闸门、coverage epoch、hook）= Middleware / finish-gate 节点。Callback 链在 Agent 构造期装配，运行期不可增删。
- **Verifier 对齐项（显式风险）**：verifier.go 与闸门顺序不动（宪章 §11）。现有时序不变量依赖"主循环阻塞在 report_vulnerability 内"——ADK 若并行执行工具，Verifier 必须改用 `client.Clone()` 独立实例，此为 R4 对等测试的必测项。
- **预算/活性**：scanBudget / 语义看门狗 / 迭代纪律映射为 ADK middleware 或图内节点；金样本回归（同一 ledger 快照 → 同一调度序列）作为双跑切换的验收门。
- **开关**：`XALGORIX_ENGINE=legacy|eino`，当前配置默认 eino；`legacy` 是即时回滚路径。金样本调度对等与 Verifier 时序门仍是待补证据。

## 7. 前端方案（D8）

### 7.1 借用 4zreco 模式（借模式不搬框架/视觉）

| 模式 | 落点 |
|---|---|
| OpsPulse 态势条 | topbar 升级：当前项目 + 全项目待审批计数（含倒计时最近一张）+ 活跃 Run + token 消耗 |
| 审批队列 | 审批页（行内决策 + `j/k/a/r` 键盘流 + 批量）+ scan-detail 事件流内审批卡（含剩余时间倒计时 + 语境切片摘要） |
| 四态纪律 + sonner | 解决 UI_DESIGN K-1~K-8 债务的首批工单 |
| 深链约定 | 审批深链（通知一键直达）、`?tab=&focus=` 消费后 replaceState |
| G6 画布规范 | 信息全景图（R5）：点亮关系可视化，独立画布规范 |

### 7.2 结构变化

- 侧栏顶部项目切换器；导航重组为项目内语境：**项目仪表盘 / 任务 / 发现 / 审批 / 周期 / 信息空间（R5）/ 设置**。
- **任务详情页**（新最重页面）：概览（七环状态：当前 Run 停在哪一环 + 缺口清单 + VOI 排序的可调度池）｜发现（任务级投影）｜事件（LiveFeed 复用）｜运行（Run 列表，跳转现有 scan-detail 降级为 Run 详情）｜报告｜消耗。
- 现有 scan-detail 成熟组件（PhaseProgress / LiveFeed / SeverityBadge）降级为 Run 级复用件。
- zh-CN 默认语言（K-10 顺带全量收敛：硬编码英文清点进词典）。
- 每落地一条模式回写 `UI_DESIGN.md` 为 [已确认]；前端永远消费已就绪的后端能力。

## 8. 存量迁移

- R1 提供 legacy 导入器（复用 `legacy_import.go` 模式）：存量 scan.json → `default` 项目下的 Run（只读保留原目录）；schedules → 任务 schedule 属性。
- `queue_state` / admission gate / auto-resume 机制在 legacy 引擎存活期原样保留；Eino 对等后冻结 legacy。

## 9. 里程碑（与宪章 v2.1 §10 并轨；宪章编号以 CORE_PHILOSOPHY.md 为准）

| 阶段 | 内容 | 验收 |
|---|---|---|
| R0 | PG 接入 + migrations + River + `internal/jobs` + DSN 配置 | 无 DSN 全功能可跑（降级测试） |
| R1 | 项目/任务/Run 模型 + API + legacy 导入 | 五维隔离的 repository 测试；旧扫描可见 |
| 宪章 M0–M2、M5 | 宪章信息层小步（并行，不动热路径）：nature、前置条件、审计 journal、确定性 VOI | 见宪章 §10 |
| R2 | trafficproxy 默认开 + 审批闸（registry 层）+ approvals API/WS | 无审批放行类目单；超时=拒绝路径测试；CA 管理测试 |
| R3 | 前端第一批：切换器/任务页/审批 UI/OpsPulse + zh-CN 默认 | UI_DESIGN 验收清单过 |
| 宪章 M3 | InfoItem 冷路径落 PG | 幂等 + 崩溃追赶（reconciler）+ pending/applied/gap/failed 可查询；检索性能基准见宪章 §10.1 |
| 宪章 M4 | 激活入口接真实消费者 | 区域/关联/语境各至少一个消费者带契约测试 |
| R4 | Eino 重编排 + 双跑开关 + interrupt/resume 接审批 | 金样本对等门；Verifier 时序测试 |
| R5 | 前端第二批：信息全景/点亮可视化 + K 债务收敛 | 语境切片页可用 |

> **落地状态（2026-11-21）**：R0、R3/R3.1、宪章 M0–M5 已落地；**R1 已落地**——`internal/platform`（PG + 无 DSN 文件降级双实现）、`0002_projects_tasks_runs.sql`、`0004_platform_task_fields_and_findings.sql`、`/api/projects|tasks` 全套端点、`project_id` 过滤、legacy 导入器（scan→Run 双键幂等挂载 + schedules→任务属性），验收「五维隔离的 repository 测试；旧扫描可见」由 `internal/platform` + `internal/web` 测试钉住（事实见 ARCHITECTURE.md「Platform layer」节）。R1 收口补丁同时修复：PG 任务 `instruction`/`context_paths` 持久化、显式项目过滤 fail-closed、默认端口与 `*.domain` scope、Task 全部上下文进入 Run、任务 findings 的 PG 冷路径投影（无 DSN 全量分页降级）、retention/手动删除清理 Run 挂载、缺失 Run 的 `missing` 状态。下一批 = R2（trafficproxy 默认开 + 审批闸 + approvals API/WS）。**R2a 已落地（2026-11-21）**：HITL 审批管线——`internal/approval` 判定/白名单 + `tools.Registry` 审批闸（拒绝不烧熔断器）+ approvals/guidance 存储（`0003_approvals_guidance.sql` + `_platform/platform.json` 双实现 + 启动期孤儿对账）+ `/api/approvals` 全套与 `/api/tasks/{id}/guidance` REST + 5 类 WS 事件 + Run 停止批量 cancelled + guidance queued→briefing 并入；`XALGORIX_APPROVAL_MODE` 默认 off（见 §5.2 R2a 注记，verifier 免审批理由同）。下一批 = R2b（仅剩 trafficproxy MITM 默认开 + CA 管理；先修 KNOWN_ISSUES T-1）。**R4 状态**：Eino v0.9.23 的 ChatModel、Registry→InvokableTool、ADK Runtime、工具安全 middleware、审批 interrupt/resume、PG/文件 checkpoint（`0005_engine_checkpoints.sql`）、legacy hook/预算/liveness/finish gate observer adapter 与 `scan_session` 双跑接线已落地；配置默认 eino，`XALGORIX_ENGINE=legacy` 可即时回滚。**尚未完成**：金样本调度对等门与 Verifier 时序回归证据。**过渡期事实**：legacy scheduler 触发的扫描（含导入的 schedule 任务）仍走原直发路径、不经项目 scope 校验，R2 挂接 scheduler→run 时收口。

## 10. 红线（继承宪章 + 本方案新增）

- 宪章 §9 六条约束与 §12「明确不做」全部适用。
- Verifier 与 13 道闸门顺序不动；任何层的重编排不得绕过 Callback 约束链。
- 不得让 River rescue 隐式重跑扫描；审批等待不是 job。
- 无 DSN 必须可跑；热路径 JSON 写入源地位不变。
- 审批默认只拦"命令执行 + 高危 HTTP"，不得扩大为全流量审批（退化成人工驱动）。
