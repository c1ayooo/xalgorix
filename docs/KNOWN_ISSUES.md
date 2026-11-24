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
| M-4 | ✅已修（范围：正常完成/auto-finalize，2026-11-21） | narration 仍非阻塞，丢弃计数进入正常完成路径的终态遥测（`dropped_narration`）；tool/claim 事件继续阻塞发送；停止、取消、hook 终止和 Eino 初始化失败不保证附带该统计 | `internal/agent/agent.go`、`liveness.go`；`TelemetryStats()` | 已按审计边界收口；不得把该统计表述为覆盖所有终态 |
| M-5 | ✅已修（2026-11-21） | scan cleanup 调用 `httpclient.ClearSessionAuth`，同时清理 role A/B | `internal/tools/httpclient/sessionauth.go`、`internal/web/server.go`；回归 `TestClearSessionAuthRemovesBothRoles` | 已闭合 |
| M-6 | ✅已修（2026-11-21） | 429/quota 路径按实际 endpoint key 冷却；兼容旧 `last` 回退 | `internal/llm/client.go`、`keyrotator.go`；回归 `TestMarkRateLimitedKeyCoolsExactKeyNotLastPick` | 已闭合 |
| M-7 | ✅已修（2026-11-21） | flow 完成后清空内存 Profile/device_code，持久化 Store 仍是凭据源 | `internal/auth/driver_device.go`；`driver_device_test.go` | 已闭合 |
| M-8 | ✅已修（2026-11-21） | 写入 clone map，持久化成功后才提交；唯一临时文件 + rename | `internal/llm/keystore.go`；回归 `TestKeyStoreSetRollsBackOnWriteFailure` | 已闭合 |
| M-9 | ✅已修（2026-11-21） | 响应读取上限 32MB；预览文件改用唯一 `CreateTemp` | `internal/tools/proxy/proxy.go` | 已闭合 |
| M-10 | ✅已修（2026-11-21） | selector/value 经 JSON 编码注入；检查 Eval 返回值 | `internal/tools/browser/browser.go` | 已闭合 |
| M-11 | ✅已修（2026-11-21） | `ExecuteContext` 覆盖 terminal/Python/httpclient/proxy；terminal 的 venv/自动安装继承取消上下文，取消/超时返回失败结果；browser 使用原始 `rod.Page.Context`，`actionMu` 内绑定/清理每次调用的 ctx，不关闭 Chromium；Eino Runtime 对不协作工具强制硬超时并计数 | `internal/tools/registry.go`、`internal/agent/{agent,eino_runtime}.go`、`internal/engine/tools.go`、`internal/tools/browser/{browser,transport}.go`、`context_cancel_test.go`、`internal/engine/tools_test.go` | 已闭合 |
| M-12 | ✅已修（2026-11-21） | fileedit 读写改走 `internal/safefile`：openat 逐段 O_NOFOLLOW、fd 基线写临时文件、renameat 原子替换；目录 fsync 通过独立只读 fd 完成且错误不再忽略 | `internal/safefile/*`、`internal/tools/fileedit/fileedit.go`；`safefile_test.go` | 已闭合 |
| M-13 | ✅已修（2026-11-21；root/nft E2E 未执行） | Linux `enforce`：egress 键统一为 `ScanContext.ID`；专用 UID/GID + 独立 nftables 表；启动前把扫描工作区交给该 UID/GID，curl/wget/Python 使用工作区 CA 副本；browser fail-closed；proxy 模式不宣称 Enforced；非 Linux unmanaged | `internal/egress/{egress,egress_linux,egress_other}.go`、`internal/web/scan_session.go`、`internal/tools/{terminal,python,browser}`；`egress_linux_test.go` | 代码与单元回归闭合；真实 root/nftables 降权执行仍需环境验证 |

| M-14 | ✅已修（2026-11-21） | `GET /api/approvals/summary` 经真实 mux 不可达：`/api/approvals/` 前缀先匹配 `handleApprovalDetail`，GET 返 405；`handleApprovals` 内 summary 分支仅被直调 handler 的测试覆盖 | `internal/web/server.go`（`registerPlatformAPIRoutes`）、`internal/web/handlers_approvals.go`；回归 `TestApprovalsSummaryRoute`（走真实 mux，含 200/405/decision 分发） | 已闭合：精确模式先于子树匹配 |

| M-15 | ✅已按 D10/R-goal 修订（2026-11-22；本条 2026-11-23 回写） | 目标达成度已升级为**收工义务**：required 且 `verify_by≠manual` 的判据未达成时，主动 finish 被 `hookGoalFinishGate` 拒绝；计划排空尾转有界 goal obligation（≤3 次）。仍不改变 finding 标签与 `objective.Evaluate` 的确定性 | `internal/agent/goal_gate.go`、`internal/objective`、`REDESIGN.md` D10 | 已闭合；`verify_by=manual` 判据永不阻断，资源预算/操作者停止/进程关停仍可终止 |
| M-16 | 已知限制（2026-11-21 随目标功能落地） | `capability` 类判据（如 shell）依赖 finding 的 `exploitation_proof` 与漏洞类别名匹配（`shell`→rce/cmdi 等），无具体利用证据时判为未达成/不可判定 | `internal/objective/evaluate.go` | 能力类判据比漏洞类更依赖证据形态；别名表是显式小表，未知能力名退回严格 token 匹配，避免误判 |
| M-17 | 已知限制（2026-11-21 随目标功能落地） | 目标标记（`Task.ObjectiveRef`）在模型调用 `build_plan` 重建计划后会被重新标记，但**不属于覆盖分母**，也不参与排序；模型自建计划不会携带引擎的覆盖底线任务 | `internal/agent/plan_tools.go`、`internal/agent/planner.go` | 目标只提供优先级上下文，不改变方法论义务；由 `build_plan` 的覆盖底线逻辑单独保证覆盖 |

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
| F-10 | ✅已修（2026-11-21；typecheck 验证） | 执行轨迹只采纳成功的 `build_plan/update_plan`；结果缺失工具名时禁止跨 agent 配对；分页/截断规划显示 `incomplete`；历史 Run 的无结果步骤显示 `无结果` 而非永久运行中 | `webui/src/lib/execution-trace.ts`、`components/{execution-trace,plan-rail}.tsx`、`run-console.tsx`、`i18n/translations.ts` | 已闭合；前端仍无自动化测试运行器，当前证据为 `npm run typecheck` |

| F-11 | ✅已修（2026-11-21） | OpsPulse 未实现 REDESIGN §7.1 的 token 消耗展示 | `webui/src/components/ops-pulse.tsx`（活跃 Run `total_tokens` 求和）、`i18n/translations.ts` `pulse.tokens`；`npm run typecheck` 通过 | 已闭合 |

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

## 信息层闭环（2026-11-22 实测修复）

| ID | 状态 | 问题 | 证据位置 | 影响 |
|---|---|---|---|---|
| I-1 | ✅已修 | `einoApprovalBroker` 返回 typed nil 却被装进 `engine.ApprovalBroker` 接口，`ApprovalTool` 的 `broker == nil` 兜底失效；真实扫描第一次触发审批类工具即 `nil pointer dereference`，扫描标 `eino_runtime_error` | `internal/web/scan_session.go`（接口层不装 typed nil）、`internal/web/eino_approval.go`（方法层 nil 兜底）；E2E：同一靶标两次运行 0 panic | 已闭合 |
| I-2 | ✅已修 | finding 与信息层无连接：`report_vulnerability` 只写 findings/scan.json，InfoItem 仅由 `add_hypothesis_evidence` 产生；模型直接报告时 info_items 恒为 0 | `internal/tools/reporting/reporting.go` 的 `projectFindingInfoItem` / `ensureFindingLedgerEvidence`；`domainjournal.OpFindingRecorded`；实测 XSS 靶标产出 `purpose=finding` 的 InfoItem | 已闭合 |
| I-3 | ✅已修 | 纯工具观测无兜底：预算耗尽/断流时模型未调用台账工具，观测直接丢失 | `internal/tools/terminal/observation.go`（引擎侧模式匹配，密钥掩码）、`scanctx.AppendToolObservation`、`OpToolObservation`；实测一次 dirbust 产出 4 条 observed InfoItem | 已闭合 |
| I-4 | ✅已修 | 平台 run 与扫描数据身份不通：`runs.legacy_scan_id` 为空，`ProjectScanIDs` 拿不到 `info_items.scan_id`，`/api/info/region` 恒空 | `platform.Repository.SetRunLegacyScanID`（PG/File 双实现）+ `internal/web/scan_session.go` 启动时回写；实测 region 返回 finding 与观测条目 | 已闭合 |
| I-5 | ✅已修 | `incomplete()` 把「没有水位行的 id」也算未应用，项目里混入 instance_id 时永远 `incomplete=true` | `internal/infoitem/postgres/store.go`（改为 JOIN 已知水位行、只判非 applied）；实测 `incomplete=false` | 已闭合 |
| I-6 | ✅已修 | 迁移测试假设每个迁移都 `CREATE TABLE`，0010 是 `ALTER TABLE` 导致 CI 假红 | `internal/infoitem/postgres/migrate_test.go`（接受 CREATE TABLE/ALTER TABLE/CREATE INDEX） | 已闭合 |

**验收判据**：同一本地靶标一次运行后，`info_items` 同时含 `sensitive_artifact_served`（引擎观测）与 `purpose=finding`（报告投影），`/api/info/region` 返回二者且 `incomplete=false`。

| I-7 | ✅已修（2026-11-22） | eino AfterTool 丢弃 tool-result metadata：`report_vulnerability` 的 `vuln_id` 到不了 web 层，`scan.json.vulns` 运行中恒空（违反宪章 §8「信息层不得有孤儿事实」），发现页整个会话不可见直至收尾合并 | `internal/agent/eino_observer.go`（AfterTool 透传 Metadata）+ `wsEvt.ResultMeta` 填充；实测 TSec 会话 scan.json vulns=8、发现页运行中可见 | 已闭合 |

## 安全链（2026-11-22 实测）

| ID | 状态 | 问题 | 证据位置 | 影响 |
|---|---|---|---|---|
| S-1 | 未修（运维决策项） | python/terminal 子进程出网不经 Go `DialGuard`（第 3 环只覆盖 Go 原生 dial），字符串扫描是唯一防线；`[egress] OS-level isolation is unmanaged`（uid+nft 强制走代理需 root 运行或授权 nft）。变体构造（动态拼接 URL、7 段式 `1.2.7.0.0.0.1` 首次调用）可执行 | `internal/egress/egress_linux.go`（preparePlatform 需 root+nft）、实测 21:23:07 一次变体探测返回内容后二次才被拦 | 子进程流量无 OS 级围栏；启用路径 = root 运行或授权 nft cap，属运维决策 |
| S-2 | 未修（记录） | scopeguard 字符串级检测对「运行期动态拼接的 loopback 字面量」天然盲（首次漏检、二次拦截）；canonicalHosts 对 7 段式数字串本身拒绝 ✓ | `internal/scopeguard/scopeguard.go`（canonicalHosts）、实测 21:23 事件对 | 与 S-1 同根；S-1 落地后此条自然闭合 |

## 处理原则

1. 本台账不改变 CORE v2.1 的 M0–M5 路线图。
2. M0–M5 不得隐式顺手修复本台账条目，除非该条目是当前里程碑验收的必要前置，并在独立提交/测试中说明。
3. 修复前重新核实现状；并发改造可能已经改变上述代码。
4. 新增发现继续追加，不删除历史记录；修复后标记解决日期和对应测试。
