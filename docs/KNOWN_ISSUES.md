# 历史问题台账（不阻塞 CORE v2.1 改造）

> 本文件只记录在当前工作树上发现、但**不属于 CORE v2.1 M0–M5 实施范围**的问题。
> 不在这里直接修复；修复时另立任务，并按问题边界补齐回归测试。
>
> 记录日期：2026-11-20。代码和测试可能随后续并发改造变化，修复前必须重新核实。
>
> 状态说明：
>
> - **代码确认**：静态实现或现有测试已能确定行为。
> - **待动态验证**：存在明确代码风险，但触发条件或生产影响尚未通过动态实验确认。
> - **测试不稳定**：已观察失败，但尚不能仅凭现有证据确定根因。

## 高优先级

| ID | 状态 | 问题 | 证据位置 | 影响 |
|---|---|---|---|---|
| H-1 | ✅已修（2026-11-21） | Stop All 不再设置全局 `stopReq`；admission/queue 只读 per-instance 状态 | `internal/web/server.go`、`queue_state.go`；回归 `TestStopReqGlobalFlagDoesNotInterruptPendingScan`、`TestHandleStopAll_DoesNotSetGlobalShutdownFlag` | 已闭合 |
| H-2 | ✅已修（2026-11-21） | `/api/stop` 与实例动作仅接受 POST；CSRF 中间件覆盖所有状态变更 | `internal/web/server.go`、`auth_session.go`；`csrf_test.go` | 已闭合 |
| H-3 | ✅已修（2026-11-21） | `Result.Error` 非空时置 `Success=false` 并记录 breaker failure | `internal/tools/registry.go`；`registry_test.go` | 已闭合 |
| H-4 | ✅已修（2026-11-21） | `generateReport` 接收 caller 的 `scanDir`，移除 `currentScanDir` 临时改写 | `internal/web/report.go`、`scan_query.go`；回归 `TestGenerateReportUsesCallerScanDirConcurrently` | 已闭合 |
| H-5 | ✅已修（2026-11-21） | ledger 快照写入在 `mu` 临界区内完成，按快照顺序落盘 | `internal/scanctx/ledger.go` | 已闭合 |
| H-6 | ✅已修（2026-11-21） | 持久化失败回滚 queued/running 状态并回收 delegation 预算 | `internal/tools/agentsgraph/agentsgraph.go`、`checkpoint.go` | 已闭合 |
| H-7 | ✅已修（2026-11-21） | Verifier 使用 `context.WithDeadline` 绑定 LLM 请求，循环每轮复查 deadline | `internal/agent/verifier.go` | 已闭合 |

## 中优先级

| ID | 状态 | 问题 | 证据位置 | 影响 |
|---|---|---|---|---|
| M-1 | ✅已修（2026-11-21） | `settingsMu` 改 RWMutex；`configSnapshot()` 覆盖 status/version/rate-limit/agentmail/auth/LLM settings/provider discovery/scan-session/notify/retention/report；剩余 `s.cfg` 读仅为启动期、持锁 keypool 与 `sess.cfg` 副本 | `internal/web/settings_env.go`、`server.go`、`ws_hub.go`、`scheduler.go`、`keypool.go`、`scan_session.go` | 已闭合 |
| M-2 | ✅已修（2026-11-21） | QueueState 持久化/恢复 Model、ProviderProfile、CodeScan、SourceRepo、ScanContext/Paths | `internal/web/server.go`、`queue_state.go`；回归 `TestQueueState_PreservesAllConfig` | 已闭合 |
| M-3 | ✅已修（2026-11-21） | `markQueueStatePaused` 改用 `storage.WriteAtomic` | `internal/web/queue_state.go` | 已闭合 |
| M-4 | ✅已修（2026-11-21） | narration 仍非阻塞，但丢弃计数进入终态遥测（`dropped_narration`）；tool/claim 事件继续阻塞发送 | `internal/agent/agent.go`、`liveness.go`；`TelemetryStats()` | 已按审计边界收口；narration 不作为完整审计记录 |
| M-5 | ✅已修（2026-11-21） | scan cleanup 调用 `httpclient.ClearSessionAuth`，同时清理 role A/B | `internal/tools/httpclient/sessionauth.go`、`internal/web/server.go`；回归 `TestClearSessionAuthRemovesBothRoles` | 已闭合 |
| M-6 | ✅已修（2026-11-21） | 429/quota 路径按实际 endpoint key 冷却；兼容旧 `last` 回退 | `internal/llm/client.go`、`keyrotator.go`；回归 `TestMarkRateLimitedKeyCoolsExactKeyNotLastPick` | 已闭合 |
| M-7 | ✅已修（2026-11-21） | flow 完成后清空内存 Profile/device_code，持久化 Store 仍是凭据源 | `internal/auth/driver_device.go`；`driver_device_test.go` | 已闭合 |
| M-8 | ✅已修（2026-11-21） | 写入 clone map，持久化成功后才提交；唯一临时文件 + rename | `internal/llm/keystore.go`；回归 `TestKeyStoreSetRollsBackOnWriteFailure` | 已闭合 |
| M-9 | ✅已修（2026-11-21） | 响应读取上限 32MB；预览文件改用唯一 `CreateTemp` | `internal/tools/proxy/proxy.go` | 已闭合 |
| M-10 | ✅已修（2026-11-21） | selector/value 经 JSON 编码注入；检查 Eval 返回值 | `internal/tools/browser/browser.go` | 已闭合 |
| M-11 | ✅已修（2026-11-21） | `ExecuteContext` 覆盖 terminal/Python/httpclient/proxy；browser 使用 rod `Page.Context`，`actionMu` 内绑定/清理每次调用的 ctx，不关闭 Chromium；未适配工具继续硬超时并计数 | `internal/tools/registry.go`、`internal/agent/agent.go`、`internal/tools/browser/{browser,transport}.go`；`context_cancel_test.go` | 已闭合 |
| M-12 | ✅已修（2026-11-21） | fileedit 读写改走 `internal/safefile`：openat 逐段 O_NOFOLLOW、fd 基线写临时文件、renameat 原子替换 | `internal/safefile/*`、`internal/tools/fileedit/fileedit.go`；`safefile_test.go` | 已闭合 |
| M-13 | ✅已修（2026-11-21） | Linux `enforce`：专用 UID/GID + 独立 nftables 表，仅放行 loopback trafficproxy 端口，其余 DROP；terminal/Python 降权并注入代理；browser 在 enforce 下 fail-closed；非 Linux 明确 unmanaged | `internal/egress/{egress,egress_linux,egress_other}.go`、`internal/tools/{terminal,python,browser}`、`internal/trafficproxy/manager.go` | 已闭合 |

## 前端

| ID | 状态 | 问题 | 证据位置 | 影响 |
|---|---|---|---|---|
| F-1 | ✅已修（2026-11-21） | `useDeleteVuln` 失效 scan detail、findings list/summary、task 投影 | `webui/src/api/queries.ts` | 已闭合 |
| F-2 | ✅已修（2026-11-21） | `authStatus` 设置 10s timeout，后端挂起不再永久 Loading | `webui/src/api/client.ts` | 已闭合 |
| F-3 | ✅已失效（R5 K-3） | Overview 已改项目级 findings 投影，无逐 scan fan-out | `webui/src/pages/overview.tsx` | 已闭合 |
| F-4 | ✅已失效（R5 K-4） | findings summary 已收敛到集中 hook，无裸 fetch | `webui/src/api/queries.ts` | 已闭合 |
| F-5 | ✅已修（2026-11-21，R5） | 三处批量删除改 `Promise.allSettled` + 部分失败 toast | `scans.tsx`、`findings.tsx`、`scan-detail.tsx` | 已闭合 |
| F-6 | ✅已修（2026-11-21，R5） | 仅距底 <48px 时自动滚动 | `components/live-feed.tsx` | 已闭合 |
| F-7 | ✅已修（2026-11-21，R5） | 补 `aria-expanded/aria-controls/useId`、阶段条 aria/tabIndex | `live-feed.tsx`、`phase-progress.tsx` | 已闭合 |
| F-8 | ✅已修（2026-11-21，R5） | 存量高频 mutation 接 sonner；R3 新页已覆盖 | `webui/src/main.tsx`、各页面 | 已闭合主要入口 |
| F-9 | ✅已修（2026-11-21，R5） | 删除 login 页硬编码统计 | `pages/login.tsx` | 已闭合 |

## 测试与构建

| ID | 状态 | 问题 | 证据位置 | 影响 |
|---|---|---|---|---|
| T-1 | 测试不稳定（2026-11-21 更新：根因已确认，环境性） | `internal/proxy` 关闭测试间歇失败：`TestLocalProxyClose` 曾 race 模式 10 次失败 3 次、普通模式 10 次失败 4 次；全量 race 观察到 `TestConcurrentLocalURLAndClose` 报“remained open after Close”；后续全量普通测试又观察到 `TestManagerCloseBeforeLocalServeStarts` 报“listener remained open when Close ran before Serve”，随后单独重跑该包通过。**2026-11-21 追加**：本机（WSL2，内核 6.6.114.1-microsoft-standard-WSL2）出现接近恒定的失败后，用纯 stdlib 最小复现（`net.Listen("tcp","127.0.0.1:0")` → `Close()` → `DialTimeout` 同地址，10 轮）确认 dial 约 50% 概率在 listener 已 Close 后仍成功——该 host 的 loopback 对“已关闭端口的 dial 探测”不可靠，dial-probe 断言机制在此环境失效，与 proxy 代码无关（三个失败测试均为 T-1 记录的同一批；`internal/proxy` 全部文件 mtime 早于 R1 变更窗口，且与 `internal/platform` 无导入关系） | `internal/proxy/local_test.go`、`local.go`、`manager.go` | 断言机制对 WSL2 loopback 不可靠：修复任务应**替换 dial-probe 断言**（如 listener Close 钩子/确定性关闭状态断言），不得通过放宽断言或增加 sleep 掩盖；产品代码无缺陷。**2026-11-21 修复（R2b）**：local 代理随 MITM 化迁入 `internal/trafficproxy`，三处 dial-probe 断言全部替换为确定性状态断言（`TestLocalProxyClose` 直接断言 Manager closed/listener/server/tunnels 状态 + `LocalURL` 必须失败；`TestConcurrentLocalURLAndClose` 以 LocalURL 失败替代 dial；`TestManagerCloseBeforeLocalServeStarts` 用 listener close 钩子），无放宽、无 sleep |
| T-2 | ✅已修（2026-11-21） | 新 `webui` CI job：locked install + typecheck + build + `git diff --exit-code internal/web/static` | `.github/workflows/ci.yml` | 已闭合 |
| T-3 | ✅已修（2026-11-21） | CI 增加 30% 覆盖率下限 | `.github/workflows/ci.yml` | 已闭合 |
| T-4 | ✅已修（2026-11-21） | 新增 `internal/platformutil`：Unix/Windows 进程终止、磁盘空间、文件锁；迁移 scanctx/terminal/python/web/auth/resources；`GOOS=windows GOARCH=amd64 go build ./cmd/xalgorix` 通过，CI 恢复 Windows build 矩阵 | `internal/platformutil/*`、`.github/workflows/ci.yml`；Windows 为单进程 kill + LockFileEx，不伪装 Unix 进程组 | 已闭合 |
| T-5 | ✅已按边界收口（2026-11-21） | PR 保留 `docker compose config`、`helm lint/template`、hadolint、WebUI build stage；完整 Kali 镜像仍只在发布 workflow | `.github/workflows/ci.yml`、`docker-compose.yml`、`deploy/chart` | 已按用户确认的轻量边界闭合 |
| T-6 | ✅已修（2026-11-21） | release build 前执行 `npm ci && npm run build`，干净树断言继续兜底 | `scripts/release.py` | 已闭合 |
| T-7 | ✅已修（2026-11-21） | Helm `appVersion` 同步至 `4.6.133` | `deploy/chart/Chart.yaml` | 已闭合 |

## 处理原则

1. 本台账不改变 CORE v2.1 的 M0–M5 路线图。
2. M0–M5 不得隐式顺手修复本台账条目，除非该条目是当前里程碑验收的必要前置，并在独立提交/测试中说明。
3. 修复前重新核实现状；并发改造可能已经改变上述代码。
4. 新增发现继续追加，不删除历史记录；修复后标记解决日期和对应测试。
