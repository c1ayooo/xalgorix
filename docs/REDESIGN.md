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
| D3 | 引擎 | Eino **单一运行时**：ADK ChatModelAgent 承载主循环，工具→Eino Tool，安全闸/义务→Callback/Middleware；旧循环与双引擎开关已删除 |
| D4 | 代理 | 流量代理**默认打开**：一切出站 HTTP 经本地 MITM 代理，归因到 Run |
| D5 | HITL 范围 | 命令执行类工具 + 高危 HTTP 请求进审批；GET/被动侦察不拦；**超时 = 拒绝** |
| D6 | 项目模型 | Project → Task → Run 三层；Task:Run = 1:N；Schedule 降为任务属性；InfoItem 归属 Project，`task_id` 是情境坐标维度 |
| D7 | 隔离 | 数据（行级 project_id）/范围（项目级 scope）/凭据（项目私有）/审批（按项目过滤）/UI（项目切换器）五维隔离；跨项目仅 OpsPulse 待审批计数 |
| D8 | 前端 | 在现有 Vite SPA + go:embed 骨架与 xalgorix 令牌上改造；借 4zreco 模式（态势条/审批队列/四态纪律/深链），不搬框架与视觉；zh-CN 默认；遵循 `UI_DESIGN.md` |
| D9 | 研究项 | A*/MCTS/POMDP 不进路线图（宪章 §7 三条件）；VOI 仅加权排序 |
| D10 | 目标义务（R-goal，2026-11-22） | **goal 从纯观测升级为收工义务**：required 且引擎可判定的判据未达成时，root agent 的主动收工被 goal gate 拒绝（`internal/agent/goal_gate.go`，注册在 gatekeeper/ledger gate 之后），计划完成/收敛空转的自动收工旁路被转为有界的 goal obligation（`goalObligationLimit=3` 次后如实收场）。**评估保持确定性**（`internal/objective.Evaluate` 纯函数，不引入 LLM 判定）；`verify_by=manual` 判据永不阻断；`FinishAttempts>MaxFinishRejections` 阀保留，耗尽终态 `finish_gate_exhausted` + objective 处置快照如实记 goal_unmet。资源预算/操作者停止/进程关停三物理上限不变。finding 标签权威不变（仍只在 `report_vulnerability`）。模糊 goal 的意图分析走创建期预览端点 + run 启动第 0 轮自动分析（`internal/goalanalysis` + `XALGORIX_GOAL_ANALYSIS`，超时/失败回退原始 goal，fail-open）。 |
| D11 | 任务:运行 1:1（2026-11-22 增补，修订 D6 的手工 Run 语义） | **一个任务永久只有一个 Run 记录**：POST `/api/tasks/{id}/runs` 对已终态（stopped/failed/finished/saved）的 Run **重派同一 instance**（`reactivateTerminalDispatchSnapshot` 把 `.dispatches/<id>.json` 终态墓碑**重写**为 pending——不是绕过 `registerMultiScan` 的 exact-stop 守卫，重写后守卫看到 pending 自然放行，重复 POST 被进程内 reservation 拒绝）；Run 挂载复用 `RunsByInstanceID`，永不新建第二条。**热启动降级阶梯**：L1 eino ADK checkpoint 跨进程恢复在当前 eino 版本不可行（`Runtime.Resume` 需要仅存在于进程内中断事件的 targetID，checkpoint 未持久化它）——如实降级；L2 = `conversation_replay.go` 从 events journal 重建**有界转录**（96K chars 保尾，单行 rune-safe 截断）注入为 user 消息；L3 = `formatResumeBriefing` + **stop_reason**（新增）。转录是提示词投影，**状态权威见 D12 修订（v3.0 前 = scan.json + notes + ledger；R6.1 起 = 内存 + PG）**（宪章 §6：事件重放不出同一决策）。**目标达成即停**：`goalAchievedAutoStop`（goal_gate.go，BeforeTurn 内、plan-complete 尾之前）在 required 非 manual 判据全部 met 时走 `emitPlanCompleteFinish` 优雅终态 + stop_reason `goal_achieved`——D10 的补集（gate 拒绝提前收工；auto-stop 达成即收工），无 objective 的扫描行为零变化。**定时调度保持 1:N 不变**（每周期一个 Run 记录，后续里程碑再收敛）。运行页项目隔离：前端 `/scans` 按 `useActiveProjectId` 传 `project_id`（后端 §3.2 早已支持）。 |
| D12 | PG 主存储（2026-11-22 用户裁决，修订 D1；宪章 v2.1 → v3.0） | **状态与面板数据的唯一持久态是 PostgreSQL**，文件只承载字节产物（工具输出归档/流量原始包/上传物/交付物/沙箱工作区）。要点：① PG 必需——无 DSN 启动 fail-fast + compose 模板，v2.1「无 DSN 降级纯文件」废止；② 写入形态 = 内存权威 + 变更边界 PG 同步事务，幂等键保持 `(scan_id, seq)`、行级盖章 `instance_id`（fill-once `legacy_scan_id` 退役）；③ PG 写失败**不阻断**已启动 Run（内存撑住 + 记 failed + reconciler 按 M3 水位协议扩展追赶，缺口如实标 gap）；④ runs 表接管 scan.json 并停写；⑤ 续跑重放从 PG 读该 run **全历史** events（保尾预算不变）；⑥ 存量目录一次性导入器补行，目录保留；⑦ 批次 R6.0–R6.3 不得跳步，规格与测试接缝见 `docs/PG_PRIMARY_STORAGE_SPEC.md`。背景：续跑后各面板绑不一致 sibling 目录导致看板为空（09d06a3e 与读层修复是同类补丁），根因是"以每会话目录为持久单元"，D12 从结构上消灭该类问题。不变项：主循环七环、Verifier/闸门冻结、审计完整性纪律、VOI 确定性、单二进制形态。 |

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
- **Task**：Goal + Context 宿主（instruction、上下文上传物、phase 策略、**结构化目标 objective**、schedule 属性）。跨 Run 的累积视图全部是**冷路径投影**（任务级 findings = 各 Run findings 的 PG 投影 + 去重/升级），不合并热路径账本。
  - **objective（已落地，2026-11-21）** = { kind(engagement|capability|finding_class|coverage), statement, criteria[] }；criteria 每条给出 kind（漏洞类 / 能力 / 覆盖）、verify_by（deterministic | verifier | manual）与 required。缺省 objective = **未结构化**：界面如实显示，不推断达成度（`REDESIGN.md` §3.1 的"验收说明"自此有字段承载）。
  - objective 是**意图**，不是授权：它作为独立提示词小节进入循环、作为 `source=human` 的 InfoItem 进入信息层，并为漏洞类判据标记相关计划任务；**不改变什么算漏洞**，finding 标签仍只由 `report_vulnerability` 管线产出。
  - 达成度由 `internal/objective` 纯函数评估器给出（只读已接受发现 + ledger + 覆盖计数）。**边界（D10 修订，2026-11-22）**：达成度仍不改完成状态、不改 finding 标签，但 goal 作为**收工义务**参与关闭判定——required 且引擎可判定（verify_by ≠ manual）的判据未达成时，主动 finish 被拒、自动收工旁路转为有界义务（R-goal，`internal/agent/goal_gate.go`）；操作者停止 / 资源预算 / 进程关停 / finish 阀耗尽仍如实收场并标记 goal_unmet。
- **Run**：现 Scan 降级。`scanctx` / `scan.json` / `ledger.json` 热路径形态**原样保留**，Run 是它们的挂载点与 PG 关联点（`runs.legacy_scan_id` 兼容旧记录）。

### 3.2 PG schema（冷路径，迁移文件管理）

```sql
projects(id uuid PK, name, slug UNIQUE, scope jsonb, created_at, archived_at)
tasks(id uuid PK, project_id FK, name, goal text, context jsonb,
      phase_policy jsonb, schedule jsonb NULL, created_at, archived_at)
runs(id uuid PK, task_id FK, legacy_scan_id text NULL, mode text,
     status text, scan_dir text, started_at, finished_at)
info_items(id text, scan_id text, source_seq bigint,          -- PK(scan_id,id)
      nature text CHECK(nature IN ('observed','inferred')),    -- 认识类型；人工输入在 source 标注
      content jsonb, coordinates jsonb, source jsonb, subject jsonb,
      purpose text, context jsonb, relation_ids jsonb, supports jsonb,
      confidence real, coordinate_status text,                 -- complete/partial/pending
      valid_from, valid_until, observed_at, created_at)
info_item_edges(scan_id text, source_seq bigint, from_id text, to_id text,
      type text CHECK(type IN ('supports','contradicts','references',
                               'same_session','same_subject')),
      weight real, created_at,
      PK(scan_id, source_seq, from_id, to_id, type))           -- 幂等 upsert 键
-- info_items/info_item_edges 均无 project_id 列，项目隔离见 §3.3；
-- 实际 DDL 以 internal/infoitem/postgres/migrations/0001_infoitem_coldpath.sql 为准
approvals(id uuid PK, project_id FK, task_id FK, run_id FK,
      kind text CHECK(kind IN ('command','http')), payload jsonb, slice jsonb,
      status text CHECK(status IN ('pending','approved','rejected','timeout','cancelled')),
      deadline_at, decided_at, decided_by, reason, created_at)
-- 审计投影与同步水位（只读，M3）：info_audit_records(scan_id,seq PK)、
-- info_sync_watermarks(applied_seq, status pending/applied/gap/failed)；River 自带 river_job
```

### 3.3 隔离与降级

- 平台表（projects/tasks/runs/approvals）查询强制 `project_id` 条件（代码层 repository 统一注入，禁止裸 SQL）。
- **InfoItem 冷路径不加 `project_id` 列**：`infoitem` 包保持平台无关，项目隔离在 Web 层前置强制执行——请求带 `scan_id` 时必须先属于该项目，区域/关联查询只接收 `ProjectScanIDs` 白名单内的 scan 集合（`internal/web/scan_info.go` 顶部注释）。
- 无 DSN 的降级披露分两类，语义一致但形态不同：信息空间 API（`/api/info/*`）返 **503 + `reason=cold_path_disabled`**（不得以空数组冒充无数据，见 API_CONTRACT）；单扫描同步状态 `/api/scans/{id}/coldpath` 返 **200 + `state=disabled`**——`disabled` 不是 `failed`。
- scopeguard 授权边界升级：项目 `scope` 是目标合法性的来源之一（任务级 targets 必须落在项目 scope 内）。
- **无 DSN 降级**：单隐式项目 `default`，项目/任务/审批 UI 显示为单项目模式，扫描能力不减（宪章 §6 红线）。

---

## 4. 流量代理（D4，R2）

- **形态**：单进程共享 loopback MITM 代理（`internal/trafficproxy`）。归因：scanctx 为每 Run 签发 `Proxy-Authorization: Bearer <run_token>`，代理侧还原 project/task/run。
- **接入点（默认开）**：httpclient / send_request / browser（`launcher.Proxy`）/ terminal（curl/wget 包装脚本注入 `HTTP(S)_PROXY`，与现有速率包装同一挂点；python 由父进程注入 `REQUESTS_CA_BUNDLE`/`SSL_CERT_FILE`）。
- **出站链**：trafficproxy →（代理池轮换 | egressproxy | 直连），`ProxyRequired` fail-closed 语义保留；代理上游拨号仍过 `GuardDials`；scopeguard 六环**全部保留在前置层**，代理只做内容观测与高危判定，不接管 scope 拦截。
- **边界分层**：默认 `proxy` 模式只是代理路由姿态，`egress.Status.Enforced=false`，不得对子进程宣称 OS 强制隔离。可选 `XALGORIX_EGRESS_MODE=enforce`（Linux root + `nft`）才提供按专用 UID 的 OS 边界；启动前须把扫描工作区交给该 UID/GID，并只向子进程提供工作区内的公开 CA 副本。browser 无法用 rod launcher 换 UID，enforce 下 fail-closed；条件不满足时整个扫描拒绝启动。
- **TLS**：安装级 CA（`~/.xalgorix/ca`，0600）；浏览器上下文与 httpclient transport 信任；curl 包装加 `--cacert`，enforce 模式改用工作区 CA 副本以避免降权子进程遍历 root-only 目录。
- **产出（归因如实）**：**Go 工具 transport 流量**（httpclient / send_request / verifier 复测）以 `Proxy-Authorization: Bearer` 携带 run_token；**terminal/Python 子进程**（curl/wget/python）通过代理 URL userinfo 或 `.curlrc`/`.wgetrc` 的 Basic 凭据携带同一 token（userinfo 形态为 `<token>:xalgorix`——部分 python 运行时对无密码 userinfo 不发 Basic，代理侧 Basic 归因只读第一个 `:` 前的用户名，占位密码无需保密）。两类归因均产出 observed InfoItem（七维坐标免 LLM 提取：主体=endpoint、来源=trafficproxy+run、情境=phase）+ 审计投影；子进程通道额外触发代理侧高危 HTTP 审批。**browser 流量**经代理转发但无 token 通道，按 system 级观测元数据记录、不产出 InfoItem（browser 危险操作的 Operation 层义务已由工具闸 `classifyBrowser` 覆盖；UA 标记方案因向攻击面泄漏 token 被否决，per-run loopback 端口归因是未来可选方向）。
- **token 生命周期**：会话 cleanup `UnregisterRun` 后，残留在旧子进程 env/config 的 token 在代理侧 `resolveToken` miss → 流量诚实降级 system 级（不产 InfoItem、不建审批行）；代理对 Basic 凭据**注册表未命中即不消费**（上游代理自身认证的测试流量不受归因影响）。扫描目录里残留的 `.curlrc`/`.wgetrc` token 行是**死凭据**（0600、目录随审计保留策略留存、新会话签发新 slug 不复用），不构成跨会话归因或重放面。
- **协议升级（WebSocket 等）**：Upgrade 请求回落盲隧道式原始字节中继，观测降级为 CONNECT 级元数据并标注 `upgrade_passthrough`；hijack 前对可见的 method+URL 仍做高危判定（子进程通道照常进闸），hijack 后无请求体视野、盲 CONNECT 无明文视野——这两段保持 passthrough（documented 边界）。

## 5. HITL 审批管线（D5，R2 起）

### 5.1 语义

- 审批 = **Operation 的前置 Obligation**（引擎硬约束）；裁决结果落 `source=human` 的 InfoItem（经区域点亮回到循环视野）。
- 范围：`command` = terminal_execute / python_action / browser 危险操作，命中只读侦察白名单（被动 subfinder/httpx、`nmap -sV` 类，policy 可配）自动放行；`http` = in-scope 但高危——写方法（POST/PUT/PATCH/DELETE）+ 高危特征（管理面路径、破坏性载荷特征、认证变更操作），GET 默认放行记录。
- **超时 = 拒绝**：`XALGORIX_APPROVAL_TIMEOUT`（默认 600s，任务级可覆盖）；到时写 `timeout` 状态，agent 收到含原因的拒绝 tool-result，走替代路径。

### 5.2 接入（两步）

1. **R2（先于 Eino）**：在 `registry.Execute` 前插审批闸（与现有六环同层的第 0.5 环），阻塞等待 channel 或 deadline；approvals 表 + WS 事件 + REST API 先行落地——安全收益不等引擎重写。**R2a 已落地（2026-11-21）**：闸装在 `tools.Registry`（`SetApprovalGate`，熔断/参数校验之后、`tool.Execute` 之前——执行前最后一环；拒绝走 `Result{Success:false}` 不烧熔断器）；判定与白名单在 `internal/approval`（只读侦察白名单为「尽力而为分类器，存疑即进审批」，默认名单不含 awk/sed/sort，`$()`/未引用重定向/`system(`/`PATH=` 冒名等段形状一律拒绝）；存储 `0003_approvals_guidance.sql` + `_platform/platform.json` 双实现，启动期孤儿 pending 对账；payload 持久化前经 agent secretValues 按值脱敏 + header 名单打码（红线：API 永不回传密钥）；**verifier registry 免审批**（report 闸门内审批卡死会撞 15 分钟 watchdog 竞态；闸的同步阻塞语义恰好保持 verifier 时序不变量）；`XALGORIX_APPROVAL_MODE` 默认 **off**（D5 的「默认」约束拦截范围；默认 enforce 会让无头定时扫描每条非白名单命令 pend 600s），env 或 Settings 置 `enforce` 启用。
2. **R4（已落地，现役路径）**：生产扫描的审批走 `internal/engine/approval.go` 的 `ApprovalTool`（`StatefulInterrupt` + PG/文件 checkpoint + `Runtime.Resume`，跨重启审批不丢），不再给 scan registry 安装 `SetApprovalGate`；R2a 的 Registry 闸代码保留，表与 API 不变，前端无感。
3. **R2b 代理侧高危闸（已落地，2026-11-21）**：terminal/python 调用本身过**命令闸**（registry 层，按命令文本裁决），但单条 wire 请求（一条命令可发多请求、URL 可动态构造）没有工具闸视野——高危 wire 请求由代理在明文半程 roundTrip 前挂起裁决，触发需**四条件同时满足**：归因成功（token 命中）∧ 来源是子进程通道（Basic 解析路径）∧ `AssessHTTP`=Require ∧ enforce 模式（hook 由 `NewServer` 仅在 enforce 下装配）。依赖方向：trafficproxy 不得 import web，代理暴露 `SetHighRiskGate(HighRiskGate)` 包级 hook，web 适配器（`proxy_gate.go`）复用 `ApprovalService.submit` 同一条行创建/槽位 FIFO/WS 广播/deadline 超时路径，payload 走同一 `buildApprovalPayload` 脱敏；store 缺失/scanctx 已消失/闸报错一律 **fail-open 放行**（扫描永不依赖平台）。**Bearer 流量永不建行**（transport 级请求已逐条过工具闸——Bearer 通道结构性免碰撞），**无归因流量永不建行**（system 级红线）。**verifier 复测通道如实边界**：transport 复测（httpclient/send_request）走 Bearer 免碰撞；curl 复测共用同 sctx 工作区 `.curlrc` token → 走子进程 Basic 通道，enforce 下高危形复测会 pend 代理闸至 verifier 3 分钟 race 上限 → 落 inconclusive（needs-manual-verification **保留**，findings 不丢；验证效能降级 + 操作者队列多一行——已接受，verifier.go 不改行为）。**双层裁决（有意设计，非缺陷）**：enforce 下带写旗标的 curl（`curl -d` 类）会在命令闸与 wire 闸各裁决一次，操作者对同一意图可能答两次——两层视野粒度不同（命令文本 vs 单条 wire 请求），操作者按「批准并豁免」心智模型使用。挂起路径活性边界：内层 per-conn goroutine 同步阻塞（keep-alive 串行化后续请求，审批风暴天然被限为每隧道一张在途单）；客户端断连容忍（403 写死 ResponseWriter 不 panic）；upgrade 请求 hijack 前按可见 method+URL 判定（子进程通道照常进闸），hijack 后与盲 CONNECT 保持 passthrough（§4 边界）。
4. **R2a guidance consumed 翻转闭环（已落地，2026-11-21，原延期项）**：路径 A（SendMessage 注入）的消息前缀带稳定标记载体 `[OPERATOR GUIDANCE gi:<id>]: `（id = guidance 行 id，uuid，非密钥，本就要进 prompt）；注入成功即挂 web 层观察者（`internal/web/guidance_observer.go`）——复用 domainjournal 全局 observer 缝（`RegisterObserver`，coldpath/service.go 同款先例；append goroutine 只拿到 path/scanID/seq，观察者在自己的 drain goroutine 上经 `ReadRecords` 读持久记录，append 路径仅做非阻塞 channel 投递，缓冲满则 detach goroutine，通知不丢——本缝无 reconciler 兜底）。hypothesis/evidence 记录文本（summary、InfoItem content）命中 pending `gi:<id>` → `UpdateGuidanceStatus(consumed)`（3s 超时，失败只留日志、行保持 pending）+ `guidance_consumed` WS（web 层发射，事件流单向不变）。**路径 B 裁决（briefing 并入，scan_session.go）**：merge 文本无逐行标记载体、无法可靠关联，如实保持 injected 不自动翻转（操作者从 injected 即知该 turn 已带入上下文）。观察者生命周期 fail-open：pending 清空 / 扫描会话上下文消失（scanctx.Deactivate）/ 实例无 sctx 或无 store 时不挂或自退役，guidance 投递永不依赖平台。测试钉：`TestGuidanceConsumedClosedLoop`（POST→真 ledger evidence 引用标记→consumed+WS）、`TestGuidanceObserverFlipsConsumedOnMarker`（多标记/无标记/外来 id）、`TestGuidanceObserverStoreErrorKeepsPending`（store 错误不翻转不自退役）、`TestBriefingMergeGuidanceStaysInjected`（路径 B）、`TestUpdateGuidanceStatusConsumedIdempotent`（重复 consumed 翻转不刷新 consumed_at，internal/platform）。

5. **审批豁免规则（approve & remember，已落地，2026-11-21）**：把第 3 条注记的「批准并豁免」心智模型落成机制——一次操作者裁决持久化为 `approval_rules` 行（`0006_approval_rules.sql` + `_platform/platform.json` 双实现），**命令闸（einoApprovalBroker）与 wire 闸（proxyHighRiskGate）共用同一查询**（`ApprovalService.matchRule`），同一意图最多问一次。匹配语义在 `internal/approval`（`Policy.MatchCommandRule`/`MatchHTTPRule`，两层逐字复用）：命令规则 = 程序名 + 可选 contains（规则的程序绕过 nmap/curl 专项旗标检查——信任=旗标面信任，仅由 contains 收窄，严格窄于 env allowlist 的等价放行）；HTTP 规则 = method + host(精确/子域后缀) + path 前缀 + 可选 contains。**硬边界：deny 形状（`$()`/重定向/`PATH=` 冒名/`system(`）永不被规则豁免**——规则只豁免分类器已干净判定的 Require 类目，不能豁免"分类器看不见的东西"。失败姿态：store 缺失/读失败一律回落审批流（读失败绝不自动放行）。命中走 `approval_rule_hit` WS（"为何没有审批行"的审计投影；审批行仍是状态权威）+ `hit_count/last_hit_at` 计数（调审批默认值的运行数据）；`created_from_approval_id` 溯源裁决来源（审批行被 retention 删除时 ON DELETE SET NULL，规则存活）。REST `/api/approval-rules` + `approval_rules_changed` dashboard 事件；前端 `/approvals` 页头规则 Dialog + 审批卡「批准并记住」（契约 §3.4）。副作用：host 域 HTTP 规则顺带覆盖 verifier 的子进程 curl 复测——第 3 条注记的「已接受降级」（复测 pend 至 inconclusive）在该场景下收窄。

6. **Guidance 身份归因（`created_by`，已落地，2026-11-22，`docs/TASK_DETAIL_CONVERSATION.md`）**：人工指引 = `source=human` 的 InfoItem 前身，多账号下「本人会话」投影需要字段级权威（宪章 §3.5）——`platform.Guidance`/`GuidanceInput` 增 `created_by`，**唯一写入点 = `handleTaskGuidance` 从服务端已验证 JWT session 取 username**（`sessionClaimsFrom`；请求体不收身份字段，浏览器自报不信任；开放模式/历史行为空串，空 = 无身份，**不推断归属**）。存储：PG 迁移 `0010_guidance_created_by.sql`（`TEXT NOT NULL DEFAULT ''`）+ 文件降级随 `platform.json` 原子快照自动携带。消费：任务详情会话 tab 与 `/chat` 共用 `user-conversation.tsx` 投影——认证开启只显示 `created_by == 当前会话 username` 的行，关闭认证显示该 Run 全部人工指引（单操作者开放模式）；`guidance_consumed` 只更新原行状态，不生成新用户消息。对话页零轮询、无乐观回显：REST 仅首次快照，`guidance_injected/queued/consumed` 全由 SSE 按 `guidance.id` 合并；服务端**先持久化并更新对象状态、再广播** `injected`（防 SSE 携带旧 `queued`）。**通道全量统一（2026-11-22 续）**：Run 详情页（scan-detail）composer 亦改走本端点——`GET /api/scans/{id}` 响应附加仅响应字段 `run_id`/`task_id`（按 instance_id 反查 run 挂载，`attachRunLinkage`；未挂载遗留扫描为空，composer 不渲染），仪表盘前端退役 `/api/chat` 旁路（端点保留为未文档化机器遗留面），三处 composer 共享 `lib/guidance.ts` 幂等原语；测试钉 `TestScanDetailCarriesRunLinkage`。

### 5.3 状态机

`pending → approved | rejected | timeout | cancelled`；Run 停止时未决单批量 `cancelled`；同一 Run 同时最多 N 张未决单（默认 1，防审批风暴阻塞）。

## 6. Eino 运行时（D3，已完成单一化）

- **包**：`internal/engine`，钉住 Eino v0.9.23（ADK `ChatModelAgent` + `Runner`）。
- **唯一入口**：`internal/agent.Agent.Run` 完成 prompt、ScanState、预算、执行 checkpoint 等共享 prelude 后，无条件调用 `runLoop`。不存在 `XALGORIX_ENGINE`、legacy 分岔或回滚路径。
- **ChatModel 适配器**：实现 Eino `ToolCallingChatModel`，内部转发 `internal/llm.Client`（四协议、KeyRotator、重试/退避、token 记账保留）。OpenAI 兼容端点走 native tools；Anthropic/Gemini/Responses 端点由适配器做 XML 协议回退并转回 `schema.ToolCalls`，ADK 不感知差异。
- **工具**：registry 的每个工具包成 `tool.InvokableTool`；`RegistryTool` 统一经 `Registry.ExecuteContext`，参数校验、取消、硬超时、熔断、scope 和审批均无法绕过。工具结果元数据经 context 回传给 observer。
- **约束层**：安全六环 + HITL 审批 = tool middleware/interrupt；Obligation（阶段义务、收工闸门、coverage epoch、hook）= ADK middleware/observer。Callback 链在 Agent 构造期装配，运行期不可增删。
- **Verifier 时序**：`ExecuteSequentially=true` 结构性保证 report→Verifier 串行；`verifier_timing_test.go` 与 Eino 路径行为测试钉住 `maxInFlight==1`。
- **恢复策略**：`ChatModel` 在同一个 ADK 轮次内执行有界供应商重试；空响应/无工具响应、context overflow、限流/配额等待由 scan observer 决定。工具副作用不会因 provider retry 被重放。
- **上下文与结果**：observer 在每次模型请求前执行 bounded-context compaction/aging；工具结果统一格式化，超大输出归档到 `<scanDir>/tool-outputs` 并附 `read_tool_output` 恢复标记。
- **Checkpoint**：`scan_session` 总是打开 PG/文件 `CheckPointStore`；`Agent.Run` 在未注入时按扫描目录创建文件 store（无目录则内存 store）。审批 interrupt 可跨重启恢复。
- **验收**：`TestGoldenSchedulingParity`（单运行时金样本：claim 顺序、tool_call 序列、finish 语义、请求数、on-wire tools 数组）；`TestRuntimeToolsNodeConfigPinsVerifierSerialExecution`；`TestEinoFinishAcceptedThenToolFailureAllowsRetry`；provider 限流/暂停/恢复测试。

## 7. 前端方案（D8）

### 7.1 借用 4zreco 模式（借模式不搬框架/视觉）

| 模式 | 落点 |
|---|---|
| OpsPulse 态势条 | topbar 升级：当前项目 + 全项目待审批计数（含倒计时最近一张）+ 活跃 Run + 活跃 Run 的 token 消耗 |
| 审批队列 | 审批页（行内决策 + `j/k/a/r` 键盘流 + 批量）+ scan-detail 事件流内审批卡（含剩余时间倒计时 + 语境切片摘要） |
| 四态纪律 + sonner | 解决 UI_DESIGN K-1~K-8 债务的首批工单 |
| 深链约定 | 审批深链（通知一键直达）、`?tab=&focus=` 消费后 replaceState |
| G6 画布规范 | 信息全景图（R5）：点亮关系可视化，独立画布规范 |

### 7.2 结构变化

- 侧栏顶部项目切换器；导航重组为项目内语境：**项目仪表盘 / 任务 / 发现 / 审批 / 周期 / 信息空间（R5）/ 设置**。
- **任务详情页**（新最重页面）：会话（默认，仅当前操作者的人工指引）｜概览（七环状态：当前 Run 停在哪一环 + 缺口清单 + VOI 排序的可调度池）｜发现（任务级投影）｜情报｜审批｜账本｜文件｜运行（Run 列表，跳转现有 scan-detail 降级为 Run 详情）｜报告｜消耗。对话的新消息与状态更新只由 SSE 驱动，REST 仅提供首次持久化快照与补偿；工具、规划、模型输出和系统事件不混入人工对话。
- 现有 scan-detail 成熟组件（PhaseProgress / LiveFeed / SeverityBadge）降级为 Run 级复用件。
- zh-CN 默认语言（K-10 顺带全量收敛：硬编码英文清点进词典）。
- 每落地一条模式回写 `UI_DESIGN.md` 为 [已确认]；前端永远消费已就绪的后端能力。

## 8. 存量迁移

- R1 提供 legacy 导入器（复用 `legacy_import.go` 模式）：存量 scan.json → `default` 项目下的 Run（只读保留原目录）；schedules → 任务 schedule 属性。
- `queue_state` / admission gate / auto-resume 机制保持原样；旧引擎已删除，Eino 是唯一运行时。

## 9. 里程碑（与宪章 v2.1 §10 并轨；宪章编号以 CORE_PHILOSOPHY.md 为准）

> **状态速览（2026-11-21）**：后端 R1 主体已落地（`internal/web/handlers_platform.go`：projects/tasks CRUD + `POST /api/tasks/{id}/runs` 复用 /api/scan 分发链 + findings/guidance/objective；approvals + approval-rules；`/api/info/`），`/api/scan` 保留为遗留兼容；前端 R3/R3.1/R5（信息空间/对话中心/会话控制台）已建成接入。**"新建扫描"产品语义退役**——一切执行从任务发起，入口清理与验证见 UI_DESIGN §15 对应证据段。

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
| R6.0 | PG 主存储底座（D12）：迁移 DDL、启动 DSN 必需门、compose 模板、platform.json 退役为迁移源、存量状态导入器 | 无 DSN 启动被拒且报错可读；导入器补行后旧面板读数与迁移前一致 |
| R6.1 | 状态权威切换：ledger/journal/events/intelligence/notes/coverage 写入切 PG；runs 表接管 scan.json 停写 | 金样本调度对等 + Verifier 时序门 + 追赶协议四态（pending/applied/gap/failed）测试全绿 |
| R6.2 | 面板读切换：events/transcript/exploration/security/intel/objective 改读 PG；续跑重放接 PG 全历史 | 回归回路（redispatch_projections 九子测试）平移为「写 PG → 读 PG」语义不变地通过 |
| R6.3 | 收口：读层 sibling 合并辅助代码退役、retention 扩展（PG 行随 run 生命周期）、ARCHITECTURE/API_CONTRACT 回写 | `make build` + `go test ./... -race` + lint + typecheck 全门禁 |

> **R6 状态（2026-11-22）**：规格已裁决并成文（`docs/PG_PRIMARY_STORAGE_SPEC.md`，grilling 两轮用户逐条确认），宪章 v3.0 修订完成，批次未开工。前置事实：09d06a3e + 读层修复（2026-11-22）已让续跑面板在文件模式下全绿，是 R6.2 前的过渡正确性保障。

> **落地状态（2026-11-21）**：R0、R3/R3.1、宪章 M0–M5 已落地；**R1 已落地**——`internal/platform`（PG + 无 DSN 文件降级双实现）、`0002_projects_tasks_runs.sql`、`0004_platform_task_fields_and_findings.sql`、`/api/projects|tasks` 全套端点、`project_id` 过滤、legacy 导入器（scan→Run 双键幂等挂载 + schedules→任务属性），验收「五维隔离的 repository 测试；旧扫描可见」由 `internal/platform` + `internal/web` 测试钉住（事实见 ARCHITECTURE.md「Platform layer」节）。R1 收口补丁同时修复：PG 任务 `instruction`/`context_paths` 持久化、显式项目过滤 fail-closed、默认端口与 `*.domain` scope、Task 全部上下文进入 Run、任务 findings 的 PG 冷路径投影（无 DSN 全量分页降级）、retention/手动删除清理 Run 挂载、缺失 Run 的 `missing` 状态。下一批 = R2（trafficproxy 默认开 + 审批闸 + approvals API/WS）。**R2a 已落地（2026-11-21）**：HITL 审批管线——`internal/approval` 判定/白名单 + `tools.Registry` 审批闸（拒绝不烧熔断器）+ approvals/guidance 存储（`0003_approvals_guidance.sql` + `_platform/platform.json` 双实现 + 启动期孤儿对账）+ `/api/approvals` 全套与 `/api/tasks/{id}/guidance` REST + 5 类 WS 事件 + Run 停止批量 cancelled + guidance queued→briefing 并入；`XALGORIX_APPROVAL_MODE` 默认 off（见 §5.2 R2a 注记，verifier 免审批理由同）。下一批 = R2b（仅剩 trafficproxy MITM 默认开 + CA 管理；先修 KNOWN_ISSUES T-1）。**R2b 已落地（2026-11-21）**：`internal/trafficproxy`（`internal/proxy` 保持纯上游池叶包）——loopback MITM 默认开（`XALGORIX_TRAFFICPROXY`，off 逐字节还原旧行为）、stdlib CA 持久化 `~/.xalgorix/ca`（0600/0700、损坏 fail-closed 不重签）、CONNECT 拦截 + 明文半程逐请求观测（有界单写 sink，溢出丢弃计数，header 只记名不记值）、run_token 归因（web 签发 randomSlug，剥离前读取，无 token 流量 system 级不产 InfoItem）、`OpObserveTraffic` 入 domain journal（无 DSN 照写、PG 投影自动缺席）、GuardDials 第三环嫁接（直接上游拨号 net.Dialer.Control，池地址豁免，per-scan provision 端口经归因注册表保真）、四消费端全接线（off 模式走旧路径）。KNOWN_ISSUES T-1 同批修复（三处 dial-probe 断言换确定性状态断言）。**R4 状态**：Eino v0.9.23 的 ChatModel、Registry→InvokableTool、ADK Runtime、工具安全 middleware、审批 interrupt/resume、PG/文件 checkpoint（`0005_engine_checkpoints.sql`）、hook/预算/liveness/finish gate observer adapter 已落地；Eino 是唯一运行时，旧循环与 `XALGORIX_ENGINE` 开关已删除。**R4 已收口（2026-11-21）**：金样本调度对等门与 Verifier 时序回归证据落地。金样本对等门 = `TestGoldenSchedulingParity`（`internal/agent/runtime_golden_test.go`）：同一 ledger.json 快照（7 假设覆盖 VOI 加权、同分 tie-break、类过滤、blocked/exhausted/rejected 不可认领态、observed/inferred 证据）喂单一 Eino 运行时 + scripted LLM 序列，断言 claim 序列等于 M5 加权期望（H-1→H-4→H-2→H-3）、tool_call（name+规范化 args）序列、finished 恰一次且非 aborted、模型请求数等于脚本长度（finish 后零多余轮）。Verifier 时序门 = `TestGate45VerifierChainStaysSerialInsideToolNode`（Gate 4.5 全链在 ADK tool node 内串行、verdict 落地为 Verified+TagVerified+证据入 proof）、`TestRuntimeToolsNodeConfigPinsVerifierSerialExecution`（ExecuteSequentially 绑定显式断言）、`TestEinoGate45VerifierChainStaysSerialAndVerdictLands` 与 `TestEinoPathVerifierRestoresClientContext`（真 verifyFinding 链在 eino 路径：main→verifier→retest→verifier→main 全串行、服务端 maxInFlight==1、超时取消后 ctx 恢复且 inconclusive 保留）。收口批同修四处引擎侧缺陷（评审一轮补三处）：observer BeforeTool 覆盖 activity/phase/auth/out-of-scope/target-scope 五闸并保持位序与文案；`ChatModel.Generate` 改从 model.Options 读取 ADK 每次调用绑定的工具面（此前工具定义从未随请求发出，native tool calling 在 eino 路径无法触发）；native tools 为主通道，非 OpenAI 协议由 ChatModel 适配器做 XML 回退并转回 Eino tool calls（`TestGoldenSchedulingParity` 钉住 on-wire tools 数组）；工具执行失败转为工具结果而非图致命错误 + finish 被闸接受但执行失败时解除 StopCheck 武装允许重试（`TestEinoFinishAcceptedThenToolFailureAllowsRetry`）；finish 成功后经 `RuntimeConfig.StopCheck` + run-ctx 取消阻断 finish 之后的残余模型轮。**R5 已落地（2026-11-21）**：信息空间页 `/info`（区域/关联/语境切片三 Tab，REDESIGN §7.2 导航最后一项）+ 关联图边 kind 语义着色与图例（只渲染真实存在的边，supports 之外的 kind 给诚实空态——引擎仅产 supports 边）+ 深链 `?tab=&focus=` 消费后 replaceState + coldpath 同步状态五态徽标（disabled≠failed）+ 跨扫描关联点亮（项目级聚合，ProjectScanIDs 白名单、跳数/limit 截断保持、`scans_consulted` 诚实披露、scan 数上限保护）+ K-10 存量英文按页收敛（scans/schedules/settings/环境行/findings/instances/new-scan/命令面板/provider-keys/scan-detail guidance）。UI_DESIGN §6 画布规范已回写 [已确认]。语境切片页可用验收 = 三 Tab 对真实后端可查询、隔离与降级语义正确（无 DSN → 503 `cold_path_disabled` 显式说明而非空数据）；真后端冒烟证据由主会话执行。**过渡期事实已于本批收口（scheduler→Run 挂接，2026-11-21；评审修复同批）**：legacy scheduler 触发与手动 trigger 端点不再直发——先经 schedule 任务（`tasks.context.legacy_schedule_id`，只读反查 `TaskByLegacyScheduleID`；**运行期 POST/PUT /api/schedules 经 `ensureScheduleTask` 幂等 find-or-create 同步补链接**，不等重启导入）关联项目做 scope 校验。语义分层：**有 store 时任何异常态（任务缺失/项目缺失/临时 store 错误）= 跳过本周期不派发（fail-closed），不把坏链接降级成「schedule 说什么就打什么」**；scope 越界拒绝：tick 路径照常推进 LastRun/NextRun + 日志防 30s 重试风暴，trigger 端点 400（临时 store 错误 503）；**空 scope（default 导入态）兼容裁决：项目 scope 为空时 schedule 自身 targets 即授权源（操作者显式配置，放行）**；无 platform store 同此放行且直发不挂载（宪章 §6.6，与有 store 的 skip 语义分开）。run 以 `trigger="schedule"` 同步挂载后才派发；`checkAndRunSchedules` 锁拆分——schedulesMu 只覆盖快照+推进，反查/校验/挂载/派发全部锁外。**R2a guidance consumed 翻转闭环已落地（2026-11-21，原 R2a 延期项）**：`gi:<id>` 稳定标记注入 + domainjournal 观察者翻转 consumed（机制、路径 B「briefing 并入保持 injected」裁决与测试钉见 §5.2 第 4 条；API_CONTRACT §3.1 状态注释同步）。**R2b raw 修正已落地（2026-11-22）**：操作者裁决修正「header 只记名不记值 / body 从不落盘」红线——归因流量的 raw 请求/响应包（header 值、body，**不脱敏**）持久化进 per-scan 流量归档 `scanctx.TrafficArchive`（`<scanDir>/traffic/tr_<seq>.json`，`storage.WriteAtomic`，重启续号不覆盖；双侧各截 128 KiB 带截断标记；run token 已剥离不出现在包里；升级 passthrough 与盲 CONNECT 无明文半程不捕获；**摘要层 InfoItem 仍掩码**，context 带 `traffic_seq`/`raw_archive` 引用）。消费端：`GET /api/scans/{id}/traffic`（endpoint/method 过滤 + 最新窗口，`present` 区分无归档与空匹配，API_CONTRACT §3.8）→ findings 详情弹窗「HTTP 交互」区块渲染 wire 包（懒加载，无归档整段不渲染）。捕获在 trafficproxy forward 路径（响应体 tee 有界捕获，不增流媒体延迟；归档写仍在单写 sink 离请求路径）。危险词表同批扩充：`drop database`/`dropdatabase`（Mongo）/`drop schema`/`alter table`/`flushall`/`flushdb`（Redis）进 `AssessHTTP` destructive 清单（ positive+negative 测试钉住）。

> **追加落地（2026-11-21，结构化任务目标）**：任务目标从自由文本升级为 **objective + 可判定判据**——`platform.Objective{kind, statement, criteria[]}`（迁移 `0007_task_objective.sql`，PG/文件双实现）、判据评估器 `internal/objective`（纯函数，只观测已接受发现，产出 met/unmet/undetermined/manual_pending 与展示用达成度）、`GET /api/tasks/{id}/objective`（判据状态 + 证据引用 + 所处环节 + 缺口 + 可调度假设）、任务详情页用真实面板替换原"缺口清单"占位、任务列表显示目标徽标。运行时：目标以独立提示词小节 `## OBJECTIVE (operator-stated)` 进入循环、以 `source=human` 的 observed InfoItem 进入信息层（`domainjournal.OpRecordObjective`）、以 `agent.ObjectiveSpec` 标记服务漏洞类判据的计划任务（`Task.ObjectiveRef`，只标记、不增删不重排、目标缺省时与改动前逐字节一致，`TestGoldenSchedulingParity` 保持通过）。**边界**：目标达成度不改完成状态与 finding 标签（仍只由 `report_vulnerability` 管线产出）；收工语义按 D10/R-goal 修订——未达成的 required 判据从 2026-11-22 起参与关闭判定（下条）。

> **R-goal 已落地（2026-11-22，D10 目标义务 + 创建期意图分析）**：**(a) 意图分析**——`internal/goalanalysis` 叶包（一次 LLM 调用：改写空泛 goal 为可验证陈述 + ≤12 条可判定判据 + 操作者约束；JSON 输出、容错解析、逐条校验丢弃、`platform.NormalizeObjective` 收口；fail-open）；`POST /api/projects/{id}/analyze-goal` 纯预览端点（不落盘，表单「分析目标」按钮消费）；run 启动第 0 轮自动分析（`runMultiScan` 入口 `roundZeroGoalAnalysis`：task 来源且 objective 未结构化时分析 + `UpdateTask` 持久化 + `goal_analyzed` WS 事件（ResultMeta 携带 task/status/statement），超时 120s（`goalAnalysisTimeout`）/失败回退原始 goal 直跑；scheduler 派发同路径并补齐此前丢失的 objective 搭载）；`XALGORIX_GOAL_ANALYSIS` 默认 on（只关自动路径，显式端点不受控）。objective 新增 `constraints[]`（操作者 may/may-not 规则，只收窄不放大，随 objective 进 `## OBJECTIVE` 提示词小节）。**(b) 收工义务**——`hookGoalFinishGate`（OnFinishAttempt 注册链最后一位；required 且 verify_by≠manual 的判据 unmet/undetermined 即拒，拒绝理由逐条列出 + 行动指引；无 objective/specialist/阀耗尽 → 原样放行）与 `hookGoalObligation`（OnIterationStart 注册在 hookLedgerSeed 后：把计划排空尾部转成的 goal obligation 以 directive 投递，每次内容变更只投一次）；`autoFinalizeIfPlanComplete` 在 lead 验证窗之后检查 `goalGateOpenCriteria`，未达成则 `deferFinalizeForGoalObligation`（重置 idle 尾 + 计数有界 ≤3，之后如实终局）。评估与 web 面板共用 `objective.Evaluate` 纯函数（无 LLM 判定）。验收钉：`internal/goalanalysis`（prompt 契约/容错/默认值/降级）、`goal_analysis_test.go`（端点 200/400/404/405/502/503 + round-0 持久化/采纳/回退/开关/legacy 不触达）、`goal_gate_test.go`（unmet 拒绝含理由、verified 放行、manual/optional 不阻断、capability 需具体利用证据、coverage 计数口径、阀、specialist）、`goal_obligation_test.go`（尾部转义务有界、goal 达成终局如旧、directive 投递去重、注册接线）、`TestGoldenSchedulingParity` 保持绿（无 objective 行为逐字节不变）。

## 10. 红线（继承宪章 + 本方案新增）

- 宪章 §9 约束与 §12「明确不做」全部适用（§12 该表以 v3.0 为准）。
- Verifier 与 13 道闸门顺序不动；任何层的重编排不得绕过 Callback 约束链。
- 不得让 River rescue 隐式重跑扫描；审批等待不是 job。
- ~~无 DSN 必须可跑；热路径 JSON 写入源地位不变~~（D12 修订，2026-11-22）：PG 必需、无 DSN fail-fast；**内存权威 + 变更边界 PG 同步持久化**的地位不变——任何"面板数据回写文件"或"读路径绕过 PG 直读残留状态文件"的改动都视同违反 D12。
- 审批默认只拦"命令执行 + 高危 HTTP"，不得扩大为全流量审批（退化成人工驱动）。

> **任务详情信息层补全已落地（2026-11-22，十 tab 处方不动）**：用户裁决"任务详情页缺信息的冷热、激活、信息空间、事实黑板"——后端 M0–M5/R5 投影早已存在，缺的是任务详情页的消费面。① **点亮查询任务维度**：`platform.TaskScanIDs(projectID, taskID)`（归属校验，跨项目 ErrNotFound；filestore/postgres 双实现 + 测试）；`/api/info/region|association` 增可选 `task_id`（`narrowInfoScans` 白名单只收窄不放宽，association 聚合沿用 20-scan 上限与 truncated 披露，无参数行为逐字节不变）。② **任务详情页消费**：账本 tab = 黑板双层——状态（ledger）+ 历史（`GET /api/scans/{id}/audit` 首个前端消费者 `audit-timeline.tsx`：认领历史 + 时间线截断 50 条诚实总数 + 缺口/乱序/重复/损坏 amber 照搬后端，投影不参与判定）；`coldpath-badge.tsx` 五态章唯一语义源（/info 头部、账本、运行行仅最新 5 run、概览最新 run 四处复用，disabled≠failed）；情报 tab 上半区 `intel-info-items.tsx`（`/api/info/region?task_id=` 信息项切片 + 一键关联点亮 `association-graph` + `/info` 深链；project_id 取任务归属项目防 404）。InfoItem（冷路径）与 intelligence（工具观测）同 tab 分区、两类对象不混同。词典 +66 键；typecheck 0；platform/web 测试通过；`make webui` 重建。证据详见 UI_DESIGN §15 同日条目。

> **双图移植已落地（2026-11-22，探索链路 + 播报板，十 tab 处方扩为十二）**：用户裁决"引入 4zreco 的双图,结合 eino 框架"（新增两 tab / 播报数据=领域审计流水 / 链路=Run 级,三决策已确认）。**双图同源**：`GET /api/scans/{id}/exploration`（scan_exploration.go）把 domain journal（Eino callback/middleware 链在每个领域变更边界 fsync 写入的历史——record/claim/assign/set_status/add_evidence/finding_recorded/agent_delegate/skill_load/mcp_call/record_objective/observe_traffic/tool_observation）+ ledger.json（现状权威:假设标题/六态、finding_ref 结构链接 Evidence.FindingID）确定性归约为 node/edge 模型（goal/agent/hypothesis/evidence/finding/observation × claim/yields/proves/supports,只画结构存在的边）。探索链路 tab = 拓扑视角（五列分层 SVG）,播报板 tab = 流水视角（4zreco 直播位纪律:直播位整体替换+新徽标/暂停位冻结+未读横幅）,两 tab 同一查询缓存。**引擎侧零改动**（Eino callback 链已有写入即数据源,行动层工具调用不入 journal——遥测与领域审计分离）;投影不参与判定;无 DSN 完整可用（journal 是每扫描文件）。宪章合规:对象映射不反向（plan task 不入图,无结构链接不软匹配）、只读投影、状态权威不动。验收:`go test ./internal/web/ -race`(投影测试 5 个)、typecheck 0、mock curl、`npm run build`;真后端冒烟留待主会话。
