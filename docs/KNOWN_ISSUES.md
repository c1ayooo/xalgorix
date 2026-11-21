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
| H-1 | 代码确认 | `stopReq` 在 Stop All 后可能保持为 true，污染同进程后续扫描的完成状态和队列推进 | `internal/web/server.go`、`scan_session.go`、`orchestrator.go`；需先读 `scan_lifecycle_test.go` | 正常扫描可能被标为 stopped，多目标队列提前中断 |
| H-2 | 代码确认 | 停止入口可由 GET 触发，绕过仅保护状态变更方法的 CSRF 检查 | `internal/web/server.go` 路由、`auth_session.go` 的 `isCSRFSafe` | 破坏性操作可被预取、爬虫或跨站 GET 误触发 |
| H-3 | 代码确认 | `Registry.Execute` 在 Go error 为 nil 时无条件设置 `Success=true`，未检查 `Result.Error` | `internal/tools/registry.go` | 工具软失败被当成功，熔断器持续被重置 |
| H-4 | 代码确认 | 非默认语言 PDF 使用共享 `currentScanDir` 传输出目录 | `internal/web/scan_query.go`、`report.go` | 并发报告可能写入其他扫描目录 |
| H-5 | 代码确认 | ledger 在状态锁释放后才写快照，旧快照可能晚于新快照落盘 | `internal/scanctx/ledger.go` | 崩溃恢复可能丢失较新的证据或状态 |
| H-6 | 代码确认 | agentsgraph 持久化失败后可能留下 queued/running 幽灵记录，且不关闭 done | `internal/tools/agentsgraph/agentsgraph.go` | 消耗 delegation 预算，wait/finish 可能永久阻塞 |
| H-7 | 代码确认 | Verifier 的 wall-clock deadline 未传入实际 LLM 请求，最终强制轮也不复查 deadline | `internal/agent/verifier.go`、`internal/llm/client.go` | 单次慢请求或最终轮可越过验证时限 |

## 中优先级

| ID | 状态 | 问题 | 证据位置 | 影响 |
|---|---|---|---|---|
| M-1 | 代码确认 | 运行时配置写入持有 `settingsMu`，多个读取方未持锁 | `internal/web/settings_env.go`、`server.go`、`ws_hub.go`、`scheduler.go` | 修改设置时可形成数据竞争和 slice 浅拷贝问题 |
| M-2 | 代码确认 | QueueState 未保存模型、ProviderProfile、代码扫描模式和源码等执行配置 | `internal/web/server.go`、`queue_state.go` | 重启恢复后执行语义可能改变 |
| M-3 | 代码确认 | `markQueueStatePaused` 直接截断写 queue 文件 | `internal/web/queue_state.go` | 崩溃可留下空文件，暂停状态可能丢失 |
| M-4 | 代码确认 | 普通遥测事件在通道满时会静默丢弃 | `internal/agent/agent.go` 的 `emit` | 事件历史不完整；不能作为领域审计依据 |
| M-5 | 代码确认 | 扫描 cleanup 未显式清除 httpclient 的 role A/B session-auth map | `internal/tools/httpclient/sessionauth.go`、`internal/web/server.go` | 明文认证头长期留在进程内，map 随扫描数增长 |
| M-6 | 代码确认 | KeyRotator 失败冷却使用共享 `last`，并发时可能冷却非实际失败 key | `internal/llm/keyrotator.go` | 错误 key 被冷却，失败 key 继续轮换 |
| M-7 | 代码确认 | device-code flow 成功后保留含 token 的完整 Profile 至进程结束 | `internal/auth/driver_device.go` | access/refresh token 长期驻留内存 |
| M-8 | 代码确认 | KeyStore 先改内存再保存，失败不回滚，且使用固定 `.tmp` | `internal/llm/keystore.go` | 内存与磁盘不一致，存在临时文件碰撞风险 |
| M-9 | 代码确认 | `send_request` 无上限读取响应，临时文件名只由 method+URL 生成 | `internal/tools/proxy/proxy.go` | 大响应内存压力；并发身份请求互相覆盖，固定路径可被 symlink 影响 |
| M-10 | 代码确认 | `browser.selectOption` 将 selector/value 直接拼入 JS，且未检查返回 false | `internal/tools/browser/browser.go` | 引号/反斜杠/换行会破坏脚本，未选择仍报告成功 |
| M-11 | 待动态验证 | 工具外层超时只停止等待，未向工具执行传递本次调用的取消信号 | `internal/agent/agent.go` | 部分忽略 scan context 的工具可能在超时后继续运行 |
| M-12 | 待动态验证 | 文件路径检查与后续打开之间存在 TOCTOU 窗口 | `internal/sandbox/policy.go`、`internal/tools/fileedit` | 需要并发文件系统写能力，实际可利用性未验证 |
| M-13 | 待动态验证 | 浏览器、terminal/Python 和代理目的端并不统一受 Go DialGuard 覆盖 | `internal/tools/browser`、`terminal`、`python`、`request_guard.go` | 不能把 Go transport 守卫描述为统一 OS 级 egress 边界 |

## 前端

| ID | 状态 | 问题 | 证据位置 | 影响 |
|---|---|---|---|---|
| F-1 | 代码确认 | 删除漏洞后未失效当前 scan detail、findings list/summary 缓存 | `webui/src/api/queries.ts` 的 `useDeleteVuln` | 终态详情页可能持续显示已删除漏洞 |
| F-2 | 代码确认 | `authStatus` 未传 timeoutMs | `webui/src/api/client.ts`、`store/auth.ts` | 后端挂起时页面可长期 Loading |
| F-3 | 代码确认 | Overview 对每个 scan 单独请求详情 | `webui/src/pages/overview.tsx` | N+1 请求 |
| F-4 | 代码确认 | 两处 findings summary 直接裸 fetch | `overview.tsx`、`findings.tsx` | 绕过 401、超时和统一错误处理 |
| F-5 | 代码确认 | 批量删除顺序执行且缺少整体错误处理 | `scans.tsx`、`findings.tsx`、`scan-detail.tsx` | 首个失败中止后续，调用方丢弃 Promise |
| F-6 | 代码确认 | 实时流始终自动滚到底部 | `components/live-feed.tsx` | 用户查看历史时被打断 |
| F-7 | 代码确认 | 展开行缺少 `aria-expanded/aria-controls`，阶段磁贴状态仅靠颜色/title | `live-feed.tsx`、`phase-progress.tsx` | 可访问性不足 |
| F-8 | 代码确认 | Sonner 已挂载但无 toast 调用 | `webui/src/main.tsx` | mutation 成功缺少反馈 |
| F-9 | 代码确认 | 登录页展示硬编码统计数字 | `pages/login.tsx` | 本地自托管产品展示误导性数据 |

## 测试与构建

| ID | 状态 | 问题 | 证据位置 | 影响 |
|---|---|---|---|---|
| T-1 | 测试不稳定（2026-11-21 更新：根因已确认，环境性） | `internal/proxy` 关闭测试间歇失败：`TestLocalProxyClose` 曾 race 模式 10 次失败 3 次、普通模式 10 次失败 4 次；全量 race 观察到 `TestConcurrentLocalURLAndClose` 报“remained open after Close”；后续全量普通测试又观察到 `TestManagerCloseBeforeLocalServeStarts` 报“listener remained open when Close ran before Serve”，随后单独重跑该包通过。**2026-11-21 追加**：本机（WSL2，内核 6.6.114.1-microsoft-standard-WSL2）出现接近恒定的失败后，用纯 stdlib 最小复现（`net.Listen("tcp","127.0.0.1:0")` → `Close()` → `DialTimeout` 同地址，10 轮）确认 dial 约 50% 概率在 listener 已 Close 后仍成功——该 host 的 loopback 对“已关闭端口的 dial 探测”不可靠，dial-probe 断言机制在此环境失效，与 proxy 代码无关（三个失败测试均为 T-1 记录的同一批；`internal/proxy` 全部文件 mtime 早于 R1 变更窗口，且与 `internal/platform` 无导入关系） | `internal/proxy/local_test.go`、`local.go`、`manager.go` | 断言机制对 WSL2 loopback 不可靠：修复任务应**替换 dial-probe 断言**（如 listener Close 钩子/确定性关闭状态断言），不得通过放宽断言或增加 sleep 掩盖；产品代码无缺陷 |
| T-2 | 代码确认 | 普通 PR CI 未执行前端依赖安装、typecheck 或构建 | `.github/workflows/ci.yml` | 前端回归无自动保护，嵌入产物可能陈旧 |
| T-3 | 代码确认 | 覆盖率仅上传，无最低阈值 | `.github/workflows/ci.yml` | 覆盖率下降不会阻塞合并 |
| T-4 | 代码确认 | Windows 不在 CI 构建/测试矩阵 | `.github/workflows/ci.yml` | Windows 专用代码缺少 CI 证据 |
| T-5 | 代码确认 | Docker/Helm/Compose 没有 PR 级验证 | `.github/workflows/docker.yml`、`deploy/chart` | 部署模板和镜像供应链问题可能到发布才暴露 |
| T-6 | 代码确认 | 发布脚本不重建 WebUI，依赖已提交静态产物 | `scripts/release.py` | 前端源码与嵌入产物可能不一致 |
| T-7 | 代码确认 | Helm 默认 `appVersion` 落后于 Makefile 版本 | `deploy/chart/Chart.yaml`、`Makefile` | 未指定 tag 的部署可能使用旧镜像 |

## 处理原则

1. 本台账不改变 CORE v2.1 的 M0–M5 路线图。
2. M0–M5 不得隐式顺手修复本台账条目，除非该条目是当前里程碑验收的必要前置，并在独立提交/测试中说明。
3. 修复前重新核实现状；并发改造可能已经改变上述代码。
4. 新增发现继续追加，不删除历史记录；修复后标记解决日期和对应测试。
