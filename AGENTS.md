# AGENTS.md — Xalgorix 仓库工作约定

> 本项目是开源自主 AI 渗透测试引擎：单个 Go 二进制（内嵌 React 仪表盘，`127.0.0.1:9137`），本地执行 22 阶段方法论扫描，BYO-LLM。Apache-2.0。本文只写环境不自明的约定与陷阱；会随代码变化的事实一律指向权威文档，不在此复述。

## 核心设计思想 — 信息驱动的渗透循环（二开宪章，最高优先级）

> 正文与判据见 `docs/CORE_PHILOSOPHY.md`（v3.0）。以下十条是它的工程化提炼，**与它冲突的实现以它为准**。每条都必须可被代码验收，不可验收的主张不进这一节。项目/任务管理、流量代理/HITL、Eino 重编排按 `docs/REDESIGN.md` 的 R 系列里程碑推进（2026-11-21 用户裁决：这些是要做的内容，不属于宪章 §12「明确不做」），PG 主存储按 D12 + R6 推进（2026-11-22 用户裁决路线 B，规格 `docs/PG_PRIMARY_STORAGE_SPEC.md`），实现须遵循 REDESIGN.md D1–D12 与红线，并与宪章信息层分开提交（§10.2）。
>
> **落地顺序（不得跳步，前一步验收未过不进下一步）**：P0 稳定基线 → M0 Evidence 认识类型（`nature` 独立字段）→ M1 Hypothesis 前置条件 → M2 完整审计记录与只读投影 → M3 InfoItem 冷路径落 PG → M4 激活入口接真实消费者 → M5 确定性 VOI 加权 → R6 PG 主存储（R6.0 底座 → R6.1 状态权威切换 → R6.2 面板读切换 → R6.3 收口）。

1. **主循环七环**：观察 → 信念（hypothesis）→ 义务（obligation）→ 行动（plan task / tool call）→ 证据（evidence）→ 判定（verifier）→ 收工（finish gate）。任何新能力先回答它落在哪一环，答不出来的不做。
2. **对象映射不得反向**：Hypothesis = 待证主张（Belief）；PlanTask = 意图（Intent）；引擎义务（阶段义务、收工闸门、覆盖收敛、hook 拦截）= Hint，**不是人工输入**。人工输入 = `source=human` 的 InfoItem。
3. **信息为中心建模（InfoItem）**：七维坐标（内容/来源/时间/主体/目的/情境/关系）+ 全局唯一 ID + 重复观测算独立实例 + **observed / inferred 二分**（inferred 必须关联支持证据）。ledger Evidence 是它在 hypothesis 下的投影视图，仍受 40 条/假设与 4096 字符上限。
4. **三类点亮必须点名消费者**：区域点亮（`claim_next_hypothesis` 可调度池、覆盖缺口计算）、关联点亮（Verifier baseline 装配、报告同类去重、跨扫描同主体历史——**限本项目任务之间**，项目是隔离边界）、语境切片（`VerificationRequest` 字段装配、报告生成）。**旁路** = 凭上下文记忆复述证据；直接调用 ledger/surface 的确定性 API 不算旁路。
5. **字段级权威，而非全局"高置信度覆盖"**：原始请求/响应/工具输出 → 对应原始 Observation；finding 接受与验证标签 → `report_vulnerability` 报告门管线；hypothesis 生命周期与调度 → ledger；InfoItem 标注 → InfoItem 及其补全记录；独立复测 → 所属 Agent 的 Verifier；确定性验证（`verify_*`、浏览器执行信号）→ 引擎来源；模型自述 → 最低等级。同字段冲突保留冲突、来源与时间，低权威不得静默覆盖高权威；"置信度高所以判真"一律违规。
6. **事件是审计投影，不是状态权威**：状态权威 = 进程内内存（`scanctx`）+ PG 持久态（v3.0/R6.1 起）。理由三条：prompt 每次动态拼装、对话窗口有损压缩、工具超长输出落盘只留引用——事件重放不出同一决策。
7. **存储分层（D12，v3.0）**：状态与面板数据（run 状态/events/domain journal/ledger/intelligence/notes/coverage/queue/dispatch/token 归档）唯一持久态是 PG，变更边界同步事务，幂等键 `(scan_id, seq)` + 行级盖章 `instance_id`；文件只承载字节产物（工具输出归档、流量原始包、上传物、交付物、沙箱工作区），继续 `storage.WriteAtomic`。**不做跨存储分布式事务**；PG 写失败不阻断已启动 Run（记 failed + reconciler 追赶）。续跑产生的 sibling 会话目录不是持久单元——R6.2 起面板读 PG，禁止新代码再实现"选哪个目录"式投影。
8. **River 限定范围**：只做异步补全（InfoItem 坐标判读、可选嵌入、关系推断、异步审计投影、可恢复的冷路径后处理），且必须与 reconciler 崩溃追赶协议（连续水位、`pending/applied/gap/failed`）同时存在——R6.1 起该协议扩展到全部状态表。通知、PDF、retention 只有在对应迁移验收通过后才能异步化；pgvector 无具体相似度消费者前不引入。禁止接管：扫描主队列与生命周期、HITL 审批、`agentsgraph` 子图、SSE 事件流广播。
9. **单二进制形态必须保住（v3.0 修订）**：go:embed 前端 + 自包含引擎不变；PG 是**必需部署依赖**（2026-11-22 用户裁决路线 B，废止无 DSN 降级）：无 DSN 启动 fail-fast 并给出指引，仓库提供 compose 模板。
10. **搜索算法与研究项不进路线图**：A*/MCTS/POMCP/DESPOT 需三条同时满足（动作可回滚或幂等重放、有可采样的结果模型、预算可分区），当前一条都不满足；VOI 只作排序加权项（权重 ≤30%）且必须保留确定性 tie-break，同一份 ledger 快照给出同一排序。

> **R7 双图编排修订（2026-11-23）**：本节第 2/5/6/7 项在 R7 下有一个**显式例外**，详见 `docs/CORE_PHILOSOPHY.md` 顶部 v3.1/R7 块与 `docs/DUAL_GRAPH_ORCHESTRATION.md`：Intent 的权威是探索图 `intent` 节点（PlanTask/PlanView 是视图）；Belief 的权威是 `belief` 节点（Evidence 明细仍在 ledger/InfoItem）；探索图与资产图是 PG 实时权威（读写 PG），事件/图投影仍不是决策权威。**planner_worker 是默认内置拓扑**；`XALGORIX_ORCHESTRATOR=legacy` 仅为隐藏的紧急回滚，不出现在设置页；worker 并发由设置页 `XALGORIX_WORKER_CONCURRENCY`（1–10）配置；该值热生效——新扫描启动时读取 `~/.xalgorix.env`，运行中的 pool 由 `SetWorkers` 热扩容/缩容（同时同步 `agentsgraph.SetMaxConcurrent`，扩容后排队 worker 立即出队；缩容等待当前 intent 结束），并发上限严格等于设置值。planner 有生产侧准入：`Pool.AvailableSlots` 在 `apply` 时裁剪新增 intent（每轮上限 = 空闲槽位），池满不会阻塞读图、steer、kill 等纠偏动作。worker 预算默认 60 分钟；执行 lane 的合同 timebox 只可缩短到 30 分钟下限，control-plane API lane 仍用短 timebox；超时先 steer 收尾（wind-down）、宽限期内未完成再 `KillWorker`，随后结算 exhausted，禁止“intent 已 exhausted、worker 还在打容器”。`target_kind=task` 是分析 lane（`SurfaceAnalysis`：只有 notes/ledger/研究工具，没有 terminal/HTTP/browser），symbolic target（`task`、`task:*`）不构成网络授权；攻击容器必须由 `target_kind=container` 且 target 为具体 `IP:port` 的独立 intent 执行。改这些区域前先读该规格。

## 架构抽象准入（强制性工程规范）

> 每新增一个架构抽象，必须同时回答 **权威来源、实际消费者、状态生命周期、可验证收益** 四个问题。这是架构设计、功能开发、重构与代码评审必须遵守的约束，不是建议。本条是工程约束，不是新治理框架：不为落实它新增架构模块、抽象类或检查系统；`docs/CORE_PHILOSOPHY.md` 与 `docs/ARCHITECTURE.md` 仍是权威来源，本条不与它们冲突。

**触发审查的改动**：新增模块/组件/服务/管理器、接口/抽象基类/协议、Registry/Provider/Factory/Adapter/Dispatcher、Context/State/Session/Metadata 等状态载体、配置层/缓存层/事件总线/中间层、Agent/Tool/Workflow/Pipeline 等协调机制；把原逻辑拆成多个职责层；引入新的间接调用/委托/包装。

**四项必答**：

1. **权威来源（Authority）**：它管理或表达的信息唯一权威来源是什么？是否存在重复存储/重复计算/多事实来源？冲突时谁有最终决定权？它是权威数据源，还是投影/缓存/索引？——没有明确权威来源，禁止创建第二套事实源。
2. **实际消费者（Consumers）**：哪些真实模块或调用链会用它？解决了哪个现存实际问题？有没有真实调用者（而不是为未来预留）？删掉它哪些实际功能会受影响？——禁止空壳抽象、预留层、纯转发层。
3. **状态生命周期（Lifecycle）**：状态在哪创建、谁初始化、谁可修改、如何传播同步、何时失效/重置/销毁？是否存在重复状态、过期状态、生命周期不一致？——禁止生命周期不明的全局状态、隐式缓存、跨模块共享状态。
4. **可验证收益（Verifiable Benefits）**：它具体改善了什么（重复代码/耦合/复杂度/状态不一致/可测性/可靠性）？如何用测试、调用链或复杂度对比证明？与直接实现相比成本是否合理？——禁止以"提高扩展性/符合架构/方便未来"等不可验证理由作为引入抽象的唯一依据。

**准入规则**：四项有一项答不上，就优先直接实现；不得以"未来可能用"作为抽象的理由；确有必要时记录未解问题与风险；不得用接口/代理/管理器/中间层掩盖职责不清。**默认选最简单、最直接、能满足当前需求的实现**——抽象由实际复杂度驱动，不由预想需求驱动。

**反向审查**：新增抽象要证明存在必要性；删除抽象不需要证明它未来永不被使用。任何抽象一旦失去实际消费者、权威职责或可验证收益，优先删除或合并。

**评审检查项**（发现即优先提删除/合并/简化，而不是继续叠加抽象）：无消费者的抽象 / 多模块重复维护同一事实 / 多余状态副本 / 生命周期不清的状态 / 只增调用层级不降复杂度 / 无法证明收益的设计模式 / 为凑架构形式的无效代码。

## 先读文档（按任务路由）

- **理解任何改造/新功能设计前**：读 `docs/CORE_PHILOSOPHY.md`（v3.0）+ 本文件「核心设计思想」——信息驱动循环是主轴，不是可选项；v1 研究稿在 `docs/archive/`。
- **做二开改造（项目/任务管理、流量代理/HITL、Eino 引擎、前端）前**：读 `docs/REDESIGN.md` —— 实现层设计总档（决策记录 D1–D12、里程碑 R0–R6 与宪章 M0–M5 并轨）。
- **做 PG 主存储 / R6 / 状态持久化相关改动前**：读 `docs/PG_PRIMARY_STORAGE_SPEC.md` —— 对象清单、幂等键、写失败姿态、批次切分与测试接缝的唯一规格。
- **改探索图 / intent 生命周期 / 图投影 / 运行期合规前**：读 `docs/INFO_GRAPH_CONTRACT.md` —— intent 契约、结论事务、确认门、不变量码表与 `/graph/validate` 的运行期判据。`done` intent 只能经 `ConcludeIntent` 事务产生，禁止直接 `Transition` 到 done。
- **改情报（intelligence）的读取 / worker 种子 / 跨 intent 交棒 / 越界授权前**：读 `docs/INTEL_HANDOFF_SPEC.md` —— `read_intelligence` 的身份-only 列表 + fingerprint 详情契约（M1a/M1b 已实现）、可达性投影与 capability 数据模型（M2+）。种子与自动注入的 prompt 不得含情报正文，正文只能由模型显式 `read_intelligence(fingerprint=…)` 拉取。
- **做跑分 / 长时基准任务前**：读 `docs/BENCHMARK_OPTIMIZATION.md` —— 第四轮 5400/23000 的根因、lane 分离/并发/截止检测/网络可达性的整改与下一轮验收指标。
- **改任何 `internal/` 代码前**：读 `docs/ARCHITECTURE.md` —— 分层、包职责、数据流、并发模型的权威来源（v4.5.44 后 `internal/web` 已按文件拆分，`server.go` 只是薄核心+路由枢纽）。
- **改任何 `webui/` 代码前**：读 `UI_DESIGN.md`（仓库根）—— 设计令牌、组件边界、反模式、验收清单。
- **贡献流程 / 环境要求**：`CONTRIBUTING.md`。CI 门在 `.github/workflows/ci.yml`。

## 构建事实（Makefile 不会告诉你的部分）

- `make build` 会先 `npm ci` + 构建 webui 到 `internal/web/static` 再 `go build`。**只改 Go、不重跑 `make webui` 时，`go:embed` 会嵌入旧的前端产物**——改了前端必须重建。
- 版本号经 `-ldflags "-X main.version=..."` 注入（`Makefile` 的 `VERSION`），`main.go` 有 `debug.ReadBuildInfo` 回退。发版相关改动同步看 `scripts/release.py`。
- webui 没有 lint/test 脚本，质量门是 `cd webui && npm run typecheck`（tsc -b）；后端门是 `make lint`（golangci-lint 版本钉在 Makefile 的 `GOLANGCI_LINT_VERSION`）+ `go test ./... -race`。
- webui 独立开发用 `make webui-dev`（Vite 自动拉起 `mock-backend.mjs`，8787 端口，无需 Go 后端）。
- `make image-toolbox` 有输入哈希门控：`tools/toolbox/build-inputs.sh` 把 recipe + helper 源码 + 基础镜像 digest 的 sha256 写进镜像 label `xalgorix.build_inputs`，一致就跳过 `docker build`（`TOOLBOX_FORCE=1` 强制；只改 `internal/` 永远不触发）。`tools/toolbox/Dockerfile` 里 gobuild 产物只允许出现在 profile 安装层（`core-tools`/`full-tools`/`web-tools`）**之上**——放到下面会让改一行 helper 重跑 6GB 安装层。

## 不可破坏的架构不变量

1. **扫描隔离**：一切 per-scan 状态（vulns/notes/terminal/browser/ledger/coverage/tokens/toolArchive）经 `scanctx.ScanContext` 以 contextID 路由到各工具包的 store。禁止引入包级全局的可变扫描状态；`agentsgraph.Graph` 必须挂在 scanctx 上（扫描间不可交叉）。
2. **安全链六环，缺一不可**（改工具代码不得绕过任何一环）：
   - `agent_guard.shouldBlockForOutOfScope` → `scopeguard.IsLocalOrListener`：**无条件**拦截指向操作者机器（loopback/本机网卡/仪表盘监听地址）的调用，含 IPv4 变体写法与 IDNA 混淆；
   - `RequestGuard`（`tools.GuardRedirects`）：重定向每一跳复查；
   - `DialGuard`（`tools.GuardDials`，挂在 `net.Dialer.Control`）：DNS 解析后的真实 IP 复查（防 DNS rebinding）；
   - `sandbox.CheckResolve/CheckRead`：所有文件系统读写的路径策略（读黑名单 `~/.ssh`、`/etc/shadow` 等优先于白名单）；
   - 代理 fail-closed：`ProxyRequired` 时工具不得回落直连；
   - 密钥环境变量经 `tools.SanitizeEnviron`/`SecretEnvKey` 从 terminal/python 子进程剥离。
3. **持久化纪律（v3.0）**：状态与面板数据的持久化走 PG 事务（变更边界，幂等键 `(scan_id, seq)`）；仍留文件层的字节产物（工具输出归档、流量原始包、上传物、交付物）一律 `storage.WriteAtomic`（O_EXCL + fsync + rename + 目录 fsync）。`llm/keystore.go` 的手写原子写（`CreateTemp` 随机 `.tmp` 后缀、无目录 fsync）与 `tools/notes` 的固定 `.tmp` 是历史遗留，勿模仿、勿扩散。
4. **API 永不回传密钥**，只回 `*_configured` 布尔；机器令牌走 SHA-256 摘要 + 常数时间比较（`api_token.go`），不得引入明文比较。
5. **findings 验证语义三选一**：`verified`（独立 Verifier 复现）/ `exploit-proven`（自带具体利用证据）/ `needs-manual-verification`（Verifier 不确定时**保留**，不得丢弃）。`report_vulnerability` 的门管线顺序（dedup→fabrication→method→proof→CVSS 复算→verifier→severity cap）不可重排。
6. **事件流单向**：工具与 agent 只发事件到 per-scan channel → `processEvent`（每会话单 goroutine）→ SSE 事件流广播（`/api/stream`，原 WS 已移除）。不得在工具代码里直接 broadcast 或写 HTTP 响应。
7. **Verifier 与闸门不可改**：`verifier.go` 的判定逻辑、工具面裁剪、轮次/时限，以及 `report_vulnerability` 的闸门顺序，在改造 InfoItem/点亮/VOI 时一律不动。
7.5 **工具执行面可切换，语义不变（v4.6 起）**：`XALGORIX_RUNTIME_BACKEND=native|docker`。native 是默认且行为逐字节保留；docker 时每扫描一个 toolbox 容器（`internal/runtime`），但安全链六环仍在平台侧 dispatch 前判定，容器不是安全边界。`XALGORIX_TOOLBOX_NETWORK_MODE=internal|host`（默认 internal）选择容器网络：host 共享宿主网络命名空间以直达 VPN/靶场网段，六环不因此放宽。`Exec` 绝不重试，只有 EnsureProject/StopProject/RemoveProject 幂等。容器实际状态权威是运行时（`xalgorix.*` 标签），期望态与 `desired_gen/applied_gen` 水位在 PG `toolbox_projects`；River 的 `toolbox` 队列只做保洁与追赶，不接管扫描循环/调度/审批/SSE。容器一律 `--cap-drop=ALL`，按 profile 加回 NET_RAW/NET_ADMIN/SYS_PTRACE，rootfs 只读，**工具容器内禁止出现 docker socket**。令牌/job 载荷内不得含密钥。

8. **信息层不得有孤儿事实**：任何被持久化的 finding 必须同时产出 `finding_ref` evidence 与一条 `purpose=finding` 的 InfoItem（`report_vulnerability` 落库后由引擎投影，不依赖模型二次调用）；工具输出中的敏感文件/凭据命中由引擎侧捕获为 `tool_observation`（**凭据原值落库**——操作员要能直接复用，掩码的 `token=***` 是死信息；掩码只保留在离开操作员边界的外发通道：agent 事件流 `redactSecrets` 与审批载荷 `maskSecretHeaders`）。收工闸门在「有 finding、无 evidence」时拒绝收工。

9. **意图结论不得绕行**：`done` intent 必须经 `graph.Repository.ConcludeIntent` 单事务写入——校验 owner/lease/确认门/前置条件，幂等产出节点并连 `yields` 边，再迁移状态。禁止直接 `Transition` 到 `done` 或先写状态后写产出。运行期由 `graph.ValidateStore` / `GET /api/scans/{id}/graph/validate` 只读检查；检查器不修数据。

## 已知脆弱点（改动这些区域前先读对应测试）

- `web/scan_lifecycle_test.go` 钉死了停止信号的契约：全局 `stopReq` 只表示**进程关闭**（SIGTERM/SIGINT），置 true 后从不清除；"Stop All" 是纯 per-instance 动作（翻转每个实例状态 + cancel + 清队列文件），**不得**置全局 flag——否则之后新起的多目标队列会在第一个目标后被判 interrupted（`orchestrator.go` 的 `shouldAdvanceQueueAfterTarget` 读取点）、session 被误标 `server_shutdown` 且跳过报告。历史上"clear-at-start"方案破坏过 Stop All，勿回退。
- `tools/browser` 的 `browserStore` 大量字段实际靠 `withBrowserTransportRecovery` 的 `actionMu` 串行化保护（锁纪律不完整）。新增浏览器操作必须走该包装，不得直接碰 `s.page`。
- `registry.Execute` 的失败语义（`registry_test.go` 已钉死）：非 nil error 与非空 `Result.Error` 都算失败（`Success=false` + 熔断器 `RecordFailure`）；连续失败达到上限会开熔断，新增工具不得把"预期内的软失败"写成 `Result.Error` 之外的模糊通道。
- `browser.selectOption`（browser.go ~1277）把 selector/value 直接 `'%s'` 拼进 JS，含单引号即坏。
- Verifier 与主循环共享同一个 LLM client，安全性依赖"主循环阻塞在 report_vulnerability 内"这一时序不变量（verifier.go 顶部注释）——不得把验证并发化。
- Anthropic 流式 `ChatStream` 的 output tokens 在 `message_start` 与 `message_delta` 双重累加；当前主循环只用非流式 API，别在生产路径启用它。

## 授权边界

本仓库本身就是已授权渗透测试工具。仍然：扫描目标只能来自用户显式提供的 target/instruction；不要引导或允许工具指向 `scopeguard` 判定为 operator machine 的地址；Docker `--privileged` 是既定运行姿态，不是需要"修复"的问题。
