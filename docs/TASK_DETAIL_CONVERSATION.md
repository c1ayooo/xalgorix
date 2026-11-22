# 任务详情与人工对话改造设计

> **⚠️ 已被取代（2026-11-22）**：本文的「对话区仅人工 Guidance」方向已被
> `docs/SESSIONS_CHAT_1TO1_PLAN.md`（会话页与对话页为两个独立页面、完整转录、
> 角色分组）取代；后续实现以该方案为准，本文仅作历史决策记录保留。
> 用户裁决补充（2026-11-22）：会话页角色组不含「思考」——thought/thinking 只在
> 主会话转录与执行轨迹内联呈现，不做独立角色会话。

> 日期：2026-11-22  
> 范围：`/tasks/:taskId` 的会话页签与 `/chat` 项目级对话页。  
> 上位约束：`docs/CORE_PHILOSOPHY.md`（v2.1）、`docs/REDESIGN.md`、`UI_DESIGN.md`。本文不替代这些文档；冲突时依次以宪章、REDESIGN、UI 规范为准。

## 1. 核心文档中的对应要求

### 1.1 信息模型与主循环

- `CORE_PHILOSOPHY.md §2`：主循环为观察 → 信念 → 义务 → 行动 → 证据 → 判定 → 收工。任务详情的概览、账本、审批和运行视图分别承载这些过程状态，不能全部挤进对话流。
- `CORE_PHILOSOPHY.md §3.2、§8`：人工输入是 `source=human` 的 InfoItem；它不是引擎义务、系统通知或模型输出。会话页因此只投影真实人工输入。
- `CORE_PHILOSOPHY.md §3.5`：字段权威必须保留来源。当前用户身份只能来自服务端已验证会话，不能由浏览器自报，也不能把无来源的历史记录推断为本人所发。
- `CORE_PHILOSOPHY.md §5.1`：SSE 事件用于实时遥测，不是状态权威。对话页使用持久化 Guidance 作为首次快照，使用 SSE 接收增量；不得把事件重放描述为完整状态恢复。
- `CORE_PHILOSOPHY.md §9`：事件流保持单向，工具与 Agent 不直接写 HTTP 响应；本次改造不改变 Verifier、报告门、扫描生命周期和存储权威。

### 1.2 Project → Task → Run 与页面结构

- `REDESIGN.md D6/D7、§3`：Task 是 Goal + Context 宿主，Run 是执行单元；跨 Run 内容只是冷路径投影，不合并热路径账本。页面选择必须限定在当前任务及其 Run 内。
- `REDESIGN.md §5`：人工指引是 HITL 输入，具有 `queued / injected / consumed` 生命周期；queued 不伪造成已注入，consumed 不重复生成一条用户消息。
- `REDESIGN.md §7.2`：任务详情由会话、概览、发现、情报、审批、账本、文件、运行、报告和消耗等组成；Run 级内容跟随当前 Run，操作明细各有归属。
- `UI_DESIGN.md §4、§6`：任务详情使用 sticky 头部、全页 Tab、桌面面板内部滚动和小屏自然堆叠；会话选择以 Run 为主，不编造 Planner 或系统审计会话。

### 1.3 实时与视觉规范

- `UI_DESIGN.md §9`、`docs/API_CONTRACT.md §0`：`/api/stream` 使用 EventSource，帧包含 `id/event/data`；命名事件必须显式监听，Last-Event-ID 用于有界补放，REST 历史仅作首次快照与补偿。
- `UI_DESIGN.md §2–§6`：保持 dark-first、dense、quiet、technical；复用 Card/Button/Badge/EmptyState/ErrorState，机器标识与时间使用等宽字体，不增加装饰、全局样式或新 UI 依赖。
- `UI_DESIGN.md §8、§13`：加载、空、错误状态齐备；通过 typecheck、响应式检查和嵌入产物重建验收。

## 2. 改造前偏差

| 偏差 | 影响 | 修正方向 |
|---|---|---|
| 会话栏包含 Planner、系统审计和 Worker 事件视角 | 将规划工具与系统遥测伪装成独立会话 | 删除伪会话，只保留 Task 下的真实 Run 会话 |
| 转写混排 assistant、工具、规划、审批、生命周期事件 | 用户无法辨认自己的对话内容 | 对话区仅渲染当前操作者的 Guidance |
| 旁路问答只在本地保存最近三对 | 刷新丢失、绕过统一人工输入模型、产生非 SSE 回显 | 对话 composer 统一为人工意图注入，移除本地问答卡 |
| Guidance 没有创建者字段 | 多账号下无法判断“本人” | 从已验证 JWT session 写入 `created_by`，客户端不信任自报身份 |
| Guidance 每 8 秒轮询，发送成功后再次 REST 刷新 | 实时链路形同虚设 | 去除轮询与发送后刷新，新消息和状态更新只由 SSE 驱动 |
| 前端只设置 `EventSource.onmessage` | 服务端命名帧如 `guidance_injected`、`tool_call` 不会触发默认回调 | 为契约内命名事件注册统一监听器 |
| 注入事件在更新本地对象状态前广播 | SSE 可能携带旧的 `queued` 状态 | 持久化成功后先更新事件对象，再广播 `injected` |

## 3. 目标结构与交互

### 3.1 任务详情

1. 保留 sticky 头部、任务目标、运行状态机和全页 Tab。
2. 会话页左侧仅列出当前任务的 Run；一段 Run 对应一段主会话。
3. 右侧头部显示“我的对话”、Run 标识及暂停/恢复/停止控制。
4. 消息区仅显示当前用户本人的人工指引，右对齐品牌色气泡，下方显示生命周期状态与本地时间。
5. 工具执行、模型输出、规划、审批和发现不再出现在对话区；分别从运行、账本、审批、发现等页签进入。
6. 新消息到达时，仅在用户原本接近底部（小于 48px）时自动滚底，避免打断历史阅读。

### 3.2 项目对话页

复用同一 `UserConversation` 投影和 Guidance composer。Run 切换继续由 URL `?task=&run=` 驱动，不再维护另一套事件过滤或本地消息逻辑。

### 3.3 “仅本人”的边界

- 开启认证：仅显示 `created_by == 当前会话 username` 的记录。
- 关闭认证：系统处于单操作者开放模式，显示该 Run 的人工指引，包括无身份的历史记录。
- 开启认证时的历史空身份记录：不推断归属，不展示为当前用户本人消息。
- `guidance_consumed` 只更新原消息状态，不新增消息。

## 4. 数据与 SSE 流程

```text
首次进入
  GET /api/tasks/{id}/guidance
    → 按 task + run + created_by 过滤
    → 渲染持久化快照

发送人工意图
  POST /api/tasks/{id}/guidance
    → 服务端从 sessionClaims 取得 created_by
    → FileStore / PostgreSQL 持久化
    → guidance_queued 或 guidance_injected SSE
    → 前端按 guidance.id 合并
    → 无乐观回显、无轮询、无发送后 REST 刷新

后续状态
  guidance_consumed SSE
    → 更新同一 guidance.id 的 status/consumed_at
```

SSE 客户端保留 EventSource 原生重连和 Last-Event-ID。由于遥测可能丢弃或重启后清空，持久化快照仍是权威补偿来源；不得声称 SSE 自身提供完整审计。

## 5. API 与存储变更

- `platform.Guidance` 与 `GuidanceInput` 新增 `created_by`。
- `POST /api/tasks/{id}/guidance` 从服务端会话写入创建者；请求体不增加身份字段。
- 文件降级存储随 `platform.json` 原子快照自动保存该字段。
- PostgreSQL 使用迁移 `0010_guidance_created_by.sql` 添加 `created_by TEXT NOT NULL DEFAULT ''`。
- `WSEvent` 不新增消息模型；继续复用 `guidance_injected / guidance_queued / guidance_consumed` 中的 Guidance payload。

## 6. 验收标准

1. 认证用户 Alice 的会话不出现 Bob 的 Guidance，也不出现 assistant、工具、规划、审批或生命周期事件。
2. 开放模式下历史人工指引仍可见；认证模式下空身份历史记录不错误归属。
3. 发送后仅通过 SSE 出现一条消息；重复 `request_id` 不增加消息。
4. `guidance_injected` 首次出现即为 `injected`；`guidance_consumed` 只更新原气泡。
5. `EventSource` 能接收所有契约内命名事件，网络重连继续由原生机制处理。
6. 375/768/1024/1280 宽度下无横向溢出，消息长文本可换行，加载/空/错误状态完整。
7. `npm run typecheck`、`go test ./internal/platform ./internal/web -race` 通过；前端改动经 `make webui` 写入嵌入产物。

## 7. 实施记录

- 已完成：纯人工共享会话组件（`user-conversation.tsx`）、两处页面接入（`sessions-tab.tsx` / `/chat`→`run-console.tsx`）、伪会话与旁路本地问答移除、Guidance 身份字段及 PG（`0010_guidance_created_by.sql`）/文件存储、SSE 命名事件监听（`store/events.ts` 逐一 `addEventListener`）、对话轮询移除（`useTaskGuidance` 无 refetchInterval）。
- 已删除：`session-rail.tsx`（Workers 角色分组形态废弃，会话栏由 `sessions-tab.tsx` 内联实现，仅主循环组）。
- 已通过：`npm run typecheck`（0 错误）；`go test ./internal/platform ./internal/web -race`（2026-11-22 复跑通过）。
- 已同步：`UI_DESIGN.md`（§6 session-rail 条目改记删除与继任组件、§9 命名事件显式监听 + 对话页零轮询）；`REDESIGN.md`（§5.2 第 6 条 Guidance 身份归因）；`docs/API_CONTRACT.md`（§3.1 身份归因语义）。
- 嵌入产物：`make webui` 重建（`app-CLs9Jh_j.js`）；隔离 HOME 起真后端（临时端口）验证仪表盘 200 + 新 bundle 命中 + `/api/stream` 200。
- 数据流冒烟（HTTP 层）：mock 与真后端各跑一轮——POST guidance（running→injected / 无 Run→queued）、重复 `request_id` 返回首次结果（`accepted:false`）、GET 单行不泄漏 `request_id`、开放模式 `created_by` 为空（`omitempty` 不上线）、4001 码点/缺 `request_id` → 400。
- **未做（环境无可用浏览器：snap 桩 chromium、google-chrome 挂起、playwright 浏览器下载被阻断）**：渲染页面的视觉验收——§6 第 6 条的 375/768/1024/1280 无横向溢出检查与截图；`guidance_consumed` 闭环的浏览器实测。留待有浏览器环境执行。
- **续批（2026-11-22，用户裁决「统一」）**：Run 详情页（`scan-detail.tsx`）composer 一并统一到 guidance API——后端 `GET /api/scans/{id}` 响应附加仅响应字段 `run_id`/`task_id`（`attachRunLinkage` 按 instance_id 反查 run 挂载，未挂载为空、composer 不渲染；测试钉 `TestScanDetailCarriesRunLinkage`）；前端 composer 改 `useSendTaskGuidance` + toast 反馈，删建议 chips 与本地问答反馈卡、`api.chat` 客户端方法、mock `/api/chat` handler 与 7 组 `scanDetail.suggest.*/guidance*` 死词典键；三处 composer 共享 `webui/src/lib/guidance.ts`（`newRequestId` + 4000 码点上限）。`/api/chat` 后端端点保留为未文档化机器遗留面（isMachineAPIPath 名单不动）。已通过：typecheck 0；`go test ./internal/web ./internal/platform -race`。
