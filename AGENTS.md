# AGENTS.md — Xalgorix 仓库工作约定

> 本项目是开源自主 AI 渗透测试引擎：单个 Go 二进制（内嵌 React 仪表盘，`127.0.0.1:9137`），本地执行 22 阶段方法论扫描，BYO-LLM。Apache-2.0。本文只写环境不自明的约定与陷阱；会随代码变化的事实一律指向权威文档，不在此复述。

## 核心设计思想 — 信息驱动的渗透循环（二开宪章，最高优先级）

> 正文与判据见 `docs/CORE_PHILOSOPHY.md`（v2.1）。以下十条是它的工程化提炼，**与它冲突的实现以它为准**。每条都必须可被代码验收，不可验收的主张不进这一节。项目/任务管理、流量代理/HITL、Eino 重编排按 `docs/REDESIGN.md` 的 R 系列里程碑推进（2026-11-21 用户裁决：这些是要做的内容，不属于宪章 §12「明确不做」），实现须遵循 REDESIGN.md D1–D9 与红线，并与宪章信息层分开提交（§10.2）。
>
> **落地顺序（不得跳步，前一步验收未过不进下一步）**：P0 稳定基线 → M0 Evidence 认识类型（`nature` 独立字段）→ M1 Hypothesis 前置条件 → M2 完整审计记录与只读投影 → M3 InfoItem 冷路径落 PG → M4 激活入口接真实消费者 → M5 确定性 VOI 加权。

1. **主循环七环**：观察 → 信念（hypothesis）→ 义务（obligation）→ 行动（plan task / tool call）→ 证据（evidence）→ 判定（verifier）→ 收工（finish gate）。任何新能力先回答它落在哪一环，答不出来的不做。
2. **对象映射不得反向**：Hypothesis = 待证主张（Belief）；PlanTask = 意图（Intent）；引擎义务（阶段义务、收工闸门、覆盖收敛、hook 拦截）= Hint，**不是人工输入**。人工输入 = `source=human` 的 InfoItem。
3. **信息为中心建模（InfoItem）**：七维坐标（内容/来源/时间/主体/目的/情境/关系）+ 全局唯一 ID + 重复观测算独立实例 + **observed / inferred 二分**（inferred 必须关联支持证据）。ledger Evidence 是它在 hypothesis 下的投影视图，仍受 40 条/假设与 4096 字符上限。
4. **三类点亮必须点名消费者**：区域点亮（`claim_next_hypothesis` 可调度池、覆盖缺口计算）、关联点亮（Verifier baseline 装配、报告同类去重、跨扫描同主体历史）、语境切片（`VerificationRequest` 字段装配、报告生成）。**旁路** = 凭上下文记忆复述证据；直接调用 ledger/surface 的确定性 API 不算旁路。
5. **字段级权威，而非全局"高置信度覆盖"**：原始请求/响应/工具输出 → 对应原始 Observation；finding 接受与验证标签 → `report_vulnerability` 报告门管线；hypothesis 生命周期与调度 → ledger；InfoItem 标注 → InfoItem 及其补全记录；独立复测 → 所属 Agent 的 Verifier；确定性验证（`verify_*`、浏览器执行信号）→ 引擎来源；模型自述 → 最低等级。同字段冲突保留冲突、来源与时间，低权威不得静默覆盖高权威；"置信度高所以判真"一律违规。
6. **事件是审计投影，不是状态权威**：状态权威 = `scanctx` + `scan.json`/`ledger.json`。理由三条：prompt 每次动态拼装、对话窗口有损压缩、工具超长输出落盘只留引用——事件重放不出同一决策。
7. **存储分层**：热路径仍是 `scanctx` + `storage.WriteAtomic` 原子写 JSON；冷路径 PG 承载 InfoItem（JSONB）、关系边、审计投影、项目/任务/审批。JSON 是写入源、PG 是检索副本，`scan_id + seq` 幂等 upsert，**不做跨存储分布式事务**。
8. **River 限定范围**：只做异步补全（InfoItem 坐标判读、可选嵌入、关系推断、异步审计投影、可恢复的冷路径后处理），且必须与 reconciler 崩溃追赶协议（连续水位、`pending/applied/gap/failed`）同时存在。通知、PDF、retention 只有在保留无 DSN 等价路径且对应迁移验收通过后才能异步化；pgvector 无具体相似度消费者前不引入。"信息项写入与入队同事务"**仅当该写入本身在 DB 侧**——热路径写 JSON 不满足，故只约束冷路径。禁止接管：扫描主队列与生命周期、HITL 审批、`agentsgraph` 子图、SSE 事件流广播。
9. **单二进制形态必须保住**：无 DSN 时降级为"纯文件 + 无跨扫描检索"，核心扫描能力不减。禁止任何"没有 PG 就跑不起来"的改动。
10. **搜索算法与研究项不进路线图**：A*/MCTS/POMCP/DESPOT 需三条同时满足（动作可回滚或幂等重放、有可采样的结果模型、预算可分区），当前一条都不满足；VOI 只作排序加权项（权重 ≤30%）且必须保留确定性 tie-break，同一份 ledger 快照给出同一排序。

## 先读文档（按任务路由）

- **理解任何改造/新功能设计前**：读 `docs/CORE_PHILOSOPHY.md`（v2.1）+ 本文件「核心设计思想」——信息驱动循环是主轴，不是可选项；v1 研究稿在 `docs/archive/`。
- **做二开改造（项目/任务管理、流量代理/HITL、Eino 引擎、前端）前**：读 `docs/REDESIGN.md` —— 实现层设计总档（决策记录 D1–D9、里程碑 R0–R5 与宪章 M0–M4 并轨）。
- **改任何 `internal/` 代码前**：读 `docs/ARCHITECTURE.md` —— 分层、包职责、数据流、并发模型的权威来源（v4.5.44 后 `internal/web` 已按文件拆分，`server.go` 只是薄核心+路由枢纽）。
- **改任何 `webui/` 代码前**：读 `UI_DESIGN.md`（仓库根）—— 设计令牌、组件边界、反模式、验收清单。
- **贡献流程 / 环境要求**：`CONTRIBUTING.md`。CI 门在 `.github/workflows/ci.yml`。

## 构建事实（Makefile 不会告诉你的部分）

- `make build` 会先 `npm ci` + 构建 webui 到 `internal/web/static` 再 `go build`。**只改 Go、不重跑 `make webui` 时，`go:embed` 会嵌入旧的前端产物**——改了前端必须重建。
- 版本号经 `-ldflags "-X main.version=..."` 注入（`Makefile` 的 `VERSION`），`main.go` 有 `debug.ReadBuildInfo` 回退。发版相关改动同步看 `scripts/release.py`。
- webui 没有 lint/test 脚本，质量门是 `cd webui && npm run typecheck`（tsc -b）；后端门是 `make lint`（golangci-lint 版本钉在 Makefile 的 `GOLANGCI_LINT_VERSION`）+ `go test ./... -race`。
- webui 独立开发用 `make webui-dev`（Vite 自动拉起 `mock-backend.mjs`，8787 端口，无需 Go 后端）。

## 不可破坏的架构不变量

1. **扫描隔离**：一切 per-scan 状态（vulns/notes/terminal/browser/ledger/coverage/tokens/toolArchive）经 `scanctx.ScanContext` 以 contextID 路由到各工具包的 store。禁止引入包级全局的可变扫描状态；`agentsgraph.Graph` 必须挂在 scanctx 上（扫描间不可交叉）。
2. **安全链六环，缺一不可**（改工具代码不得绕过任何一环）：
   - `agent_guard.shouldBlockForOutOfScope` → `scopeguard.IsLocalOrListener`：**无条件**拦截指向操作者机器（loopback/本机网卡/仪表盘监听地址）的调用，含 IPv4 变体写法与 IDNA 混淆；
   - `RequestGuard`（`tools.GuardRedirects`）：重定向每一跳复查；
   - `DialGuard`（`tools.GuardDials`，挂在 `net.Dialer.Control`）：DNS 解析后的真实 IP 复查（防 DNS rebinding）；
   - `sandbox.CheckResolve/CheckRead`：所有文件系统读写的路径策略（读黑名单 `~/.ssh`、`/etc/shadow` 等优先于白名单）；
   - 代理 fail-closed：`ProxyRequired` 时工具不得回落直连；
   - 密钥环境变量经 `tools.SanitEnv`/`SecretEnvKey` 从 terminal/python 子进程剥离。
3. **磁盘持久化一律 `storage.WriteAtomic`**（O_EXCL + fsync + rename + 目录 fsync）。`llm/keystore.go` 与 `tools/notes` 的固定 `.tmp` 是历史遗留，勿模仿、勿扩散。
4. **API 永不回传密钥**，只回 `*_configured` 布尔；机器令牌走 SHA-256 摘要 + 常数时间比较（`api_token.go`），不得引入明文比较。
5. **findings 验证语义三选一**：`verified`（独立 Verifier 复现）/ `exploit-proven`（自带具体利用证据）/ `needs-manual-verification`（Verifier 不确定时**保留**，不得丢弃）。`report_vulnerability` 的门管线顺序（dedup→fabrication→method→proof→CVSS 复算→verifier→severity cap）不可重排。
6. **事件流单向**：工具与 agent 只发事件到 per-scan channel → `processEvent`（每会话单 goroutine）→ SSE 事件流广播（`/api/stream`，原 WS 已移除）。不得在工具代码里直接 broadcast 或写 HTTP 响应。
7. **Verifier 与闸门不可改**：`verifier.go` 的判定逻辑、工具面裁剪、轮次/时限，以及 `report_vulnerability` 的闸门顺序，在改造 InfoItem/点亮/VOI 时一律不动。
8. **信息层不得有孤儿事实**：任何被持久化的 finding 必须同时产出 `finding_ref` evidence 与一条 `purpose=finding` 的 InfoItem（`report_vulnerability` 落库后由引擎投影，不依赖模型二次调用）；工具输出中的敏感文件/凭据命中由引擎侧捕获为 `tool_observation`（密钥必须掩码）。收工闸门在「有 finding、无 evidence」时拒绝收工。

## 已知脆弱点（改动这些区域前先读对应测试）

- `web/scan_lifecycle_test.go` 钉死了停止信号的契约：全局 `stopReq` 只表示**进程关闭**（SIGTERM/SIGINT），置 true 后从不清除；"Stop All" 是纯 per-instance 动作（翻转每个实例状态 + cancel + 清队列文件），**不得**置全局 flag——否则之后新起的多目标队列会在第一个目标后被判 interrupted（`orchestrator.go` 的 `shouldAdvanceQueueAfterTarget` 读取点）、session 被误标 `server_shutdown` 且跳过报告。历史上"clear-at-start"方案破坏过 Stop All，勿回退。
- `tools/browser` 的 `browserStore` 大量字段实际靠 `withBrowserTransportRecovery` 的 `actionMu` 串行化保护（锁纪律不完整）。新增浏览器操作必须走该包装，不得直接碰 `s.page`。
- `registry.Execute` 的失败语义（`registry_test.go` 已钉死）：非 nil error 与非空 `Result.Error` 都算失败（`Success=false` + 熔断器 `RecordFailure`）；连续失败达到上限会开熔断，新增工具不得把"预期内的软失败"写成 `Result.Error` 之外的模糊通道。
- `browser.selectOption`（browser.go ~1277）把 selector/value 直接 `'%s'` 拼进 JS，含单引号即坏。
- Verifier 与主循环共享同一个 LLM client，安全性依赖"主循环阻塞在 report_vulnerability 内"这一时序不变量（verifier.go 顶部注释）——不得把验证并发化。
- Anthropic 流式 `ChatStream` 的 output tokens 在 `message_start` 与 `message_delta` 双重累加；当前主循环只用非流式 API，别在生产路径启用它。

## 授权边界

本仓库本身就是已授权渗透测试工具。仍然：扫描目标只能来自用户显式提供的 target/instruction；不要引导或允许工具指向 `scopeguard` 判定为 operator machine 的地址；Docker `--privileged` 是既定运行姿态，不是需要"修复"的问题。
