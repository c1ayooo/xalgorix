# 会话页与对话页 1:1 复刻 — P0 参照基准与验收记录

> 状态：实施中（2026-11-22）
> 上游方案：`docs/SESSIONS_CHAT_1TO1_PLAN.md`
> 参照仓库：`/home/c1ay/4zreco`（AGPL-3.0，仅提取视觉/布局/交互语义，不复制源码）

## P0：参照 computed style 基准（自参照源码提取）

### 对话页（4zreco `chat/page.tsx`）

| 区域 | 类基准 |
|---|---|
| 页容器 | `flex h-[calc(100svh-3rem)] min-w-0 flex-col overflow-hidden p-3 sm:p-4 md:h-[calc(100svh-4rem)] md:p-6`（本仓库 chrome = topbar 3.5rem + OpsPulse 2.25rem， Reserve 5.75rem） |
| 双栏网格 | `grid min-h-0 min-w-0 flex-1 grid-cols-1 grid-rows-[minmax(10rem,15rem)_minmax(0,1fr)] gap-3 md:grid-cols-[18rem_minmax(0,1fr)] md:grid-rows-[minmax(0,1fr)] md:gap-4` |
| 左栏面板 | `flex flex-col overflow-hidden rounded-lg border bg-card` |
| 左栏头部 | `flex flex-col gap-2 border-b p-2`：新建（Button sm w-full）→ 筛选 Select（sm w-full）→ 计数/多选行（`flex items-center gap-2 px-1`，计数 `text-muted-foreground text-xs tabular-nums` flex-1） |
| 列表体 | `flex min-w-0 flex-col gap-0.5 p-2`；空态 `px-2 py-6 text-center text-muted-foreground text-xs` |
| 分组头 | `sticky top-0 z-10 flex min-w-0 items-center gap-1.5 rounded-md bg-card px-1.5 py-1 text-left font-medium text-xs transition-colors hover:bg-accent/50`；折叠点 `size-1.5 bg-primary`；运行数 Spinner；计数 `tabular-nums opacity-60` |
| 会话项 | 行 `group flex min-w-0 items-center gap-1 rounded-md pr-1 transition-colors`，active `bg-accent text-accent-foreground` / hover `bg-accent/50`；主体按钮 `min-w-0 flex-1 rounded-md px-2 py-1.5 text-left`；行1 = Pin(size-3 text-primary)+标题(text-sm truncate)+运行中 Badge(secondary+Spinner size-3)；行2 = `text-muted-foreground text-xs gap-1`（agent·日期·#id）；重命名输入 `border-input bg-background px-2 py-1 text-sm` |
| 菜单 | MoreHorizontal ghost icon-sm；组1 重命名/置顶，分隔，组2 删除（destructive→本仓库为归档） |
| 右栏面板 | `flex min-w-0 flex-col overflow-hidden rounded-lg border bg-card` |
| 详情头部 | `flex min-w-0 flex-wrap items-center gap-2 border-b px-4 py-2.5`：图标 size-4 + 标题 `max-w-48 truncate font-medium text-sm` + mono 标识(hidden sm) + LiveBadge(`bg-info/15 text-info text-xs`+`size-1 animate-pulse`)+右侧 meta `ml-auto … text-muted-foreground text-xs tabular-nums gap-x-3` |
| 归属行 | `flex flex-wrap items-center gap-x-3 border-b px-4 pb-1`（TaskRow：图标 size-3 + text-xs + 更换/选择 `text-primary text-xs hover:underline`） |
| 转录区 | ScrollArea `min-h-0 min-w-0 flex-1`；内容 `min-w-0 max-w-full px-4 py-3`；hasMore 提示 `pb-2 text-center text-muted-foreground/70 text-xs` |
| Composer | `border-t p-3`；chips `mb-2 flex flex-wrap gap-1.5`，chip `rounded-md border bg-muted/50 px-2 py-1 text-xs`（clip size-3 + 名 max-w-[160px] truncate + 大小 + X）；输入行 `flex flex-wrap items-center gap-2`；textarea `max-h-40 min-h-10 min-w-0 flex-1 resize-none` rows=1；运行中→destructive 停止钮（Square `size-3.5 fill-current`），否则 ArrowUp 发送钮 |
| 草稿态 | 居中 `flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-4 text-center`；徽标圈 `size-12 rounded-full bg-primary/10` + 图标 size-6；标题 `font-medium text-sm`；描述 `max-w-md text-muted-foreground text-xs`；选择器 `w-full sm:w-40` |
| 滚动行为 | 贴底阈值 60px；`scrollTop<=80` 触发上翻；prepend 后 `prevTop + (scrollHeight - prevH)` rAF 恢复；切换会话跳底 |
| 分页 | 历史页 200/页、列表页 100/页（可见条数递增 + 「加载更多」ghost sm `mt-1 w-full`） |
| 列表轮询 | 2s；运行中消息轮询 1s（本仓库以 SSE→refetch 桥替代，无 1s 轮询） |

### 会话页（4zreco `sessions-tab.tsx`）

| 区域 | 类基准 |
|---|---|
| 页容器 | `grid h-[calc(100svh-10rem)] min-h-0 grid-cols-1 gap-4`；小屏行 = 折叠 `auto_minmax(0,1fr)` / 展开 `minmax(0,40svh)_minmax(0,1fr)`；`lg:h-[calc(100svh-13rem)] lg:grid-cols-[18rem_1fr] lg:grid-rows-[minmax(0,1fr)]`（本仓库由 task-detail 视口定高容器供给 h-full，不再叠 100svh） |
| 左栏 | `flex min-h-0 flex-col overflow-hidden rounded-lg border bg-card`；头部 `border-b px-3 py-2`：折叠开关（chevron `-rotate-90` 折叠时，`lg:hidden`/`lg:pointer-events-none`）+「会话列表」text-xs font-medium muted + 折叠态当前会话名 + 隐藏未读合计；链路章 `text-success 实时（size-1 animate-pulse）` / `text-warning WifiOff 重连中` |
| Token 汇总 | `mt-2 flex min-w-0 flex-wrap items-center gap-1 text-muted-foreground text-xs`「任务总计 · …」Badge secondary `h-5 px-1.5 font-mono tabular-nums` |
| 列表体 | `flex w-full flex-col gap-3 p-2`；组序 mainagent → planner → system → worker；组头 `flex items-center gap-1.5 px-2 py-1 font-medium text-muted-foreground text-xs` + 图标 size-3.5 |
| 会话项 | `group/session flex w-full items-center rounded-md transition-colors`，active/hover 同对话页；按钮 `flex min-w-0 flex-1 items-center gap-1 px-2 py-1.5 text-left text-sm`；状态图标 size-3.5（running=Loader2 info 自旋 / paused=Pause warning / pending=Clock muted / done=CircleCheck success / stopped=CircleSlash warning / blocked=CircleX destructive / exhausted=ZapOff destructive）；实时章 `bg-info/15 text-info text-xs`（size-1 pulse）；未读 `min-w-4 rounded-full bg-info/15 px-1 text-xs text-info tabular-nums`（99+ 封顶） |
| Worker 加载更早 | `mt-0.5 flex items-center justify-center gap-1 rounded-md px-2 py-1 text-muted-foreground text-xs hover:bg-accent/50` |
| 右栏 | `flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-lg border bg-card`；头部 `flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 border-b px-3 py-2 sm:px-4 sm:py-2.5`：标题 `truncate font-medium text-sm` + 实时章 + `↑ 更早历史`（`text-xs text-muted-foreground`）+ 右 meta（TokenMetrics 手机短标签「入/缓/出」`sm:hidden`、桌面长标签 `max-sm:hidden`；时长 Clock size-3 + fmtDuration；主会话「可交互」） |
| 转录区 | ScrollArea `min-h-0 min-w-0 flex-1`；内容 `min-w-0 max-w-full p-4`；加载更早提示 `flex items-center justify-center gap-2 pb-2 text-muted-foreground text-xs`；首载 loader / 错误重试 / 空态均 `pl-9 text-xs` |
| 主 Composer | `border-t p-3`；chips 同对话页；textarea `max-h-36 min-h-9 overflow-y-auto` rows=1；底行 paperclip + /btw 发送 + 停止(destructive Square)/发送(default ArrowUp `ml-auto`) icon-xs |
| 滚动行为 | 贴底 60px；`scrollTop<=80` 上翻 + rAF 恢复；ResizeObserver 贴底重钉；STREAM_WINDOW 5s；1.5s 重渲染节拍；MAX_KEEP 4000 |

### 参照转录行（`transcript.tsx`）

| 行 | 类基准 |
|---|---|
| 人工 | `mt-3 mb-2 flex min-w-0 justify-end gap-2`；右列 `max-w-[85%] flex-col items-end gap-1.5`；气泡 `whitespace-pre-wrap rounded-lg rounded-tr-sm bg-primary px-3 py-1.5 text-primary-foreground text-sm [overflow-wrap:anywhere]`；时间 `text-xs text-muted-foreground tabular-nums`（MM/DD HH:mm）；头像 `size-6 rounded-full bg-primary/10` + 图标 size-3.5 text-primary；附件 chip `rounded-md border bg-card px-2 py-1 text-xs shadow-sm`（名 max-w-[180px]） |
| 助答 | `mt-1 mb-2 flex flex-col gap-1`；体 `break-words rounded-lg bg-muted px-3 py-2`（Markdown / 错误 destructive）+ 时间 |
| 工具卡 | 行按钮 `flex w-full items-start gap-2 py-1 text-left hover:bg-muted/40`：chevron size-3 → 图标 size-3.5（running=info/ok=success/失败=destructive）→ 工具名 `shrink-0 font-medium text-info` → 命令 `min-w-0 flex-1 truncate font-mono text-muted-foreground` → 状态 `ml-auto font-medium`（执行中…/✓/✗失败）；展开 `ml-7 max-h-72 overflow-auto whitespace-pre-wrap break-all rounded bg-muted/50 p-2 font-mono text-xs` |
| 消息块 | 同工具卡布局，MessageSquare/Flag 图标，正文 truncate + 「含推理」 |
| 轮次分隔 | `my-2 flex items-center gap-2 font-medium text-xs text-muted-foreground` + 两侧 `h-px flex-1 bg-border` |
| 实时尾 | `flex items-center gap-2 pt-1 pl-2 text-muted-foreground text-xs` + `size-1.5 animate-pulse rounded-full bg-info`「实时流式中…」 |
| 泳道章 | `mt-0.5 shrink-0 rounded px-1 text-xs font-medium` + chart-N 底章色 |

## 已知差异（数据诚实约束，见方案 §7/§10）

1. 无 Agent 实体 → 列表筛选与分组按任务（真实 `task_id/task_name`），不伪造 Agent 维度。
2. 无 LLM Profile / @ 引用 / 旁路提问 / Todo 数据 → 对应行不渲染。
3. tool_use/tool_result 无配对 ID → 按相邻同名工具配对；无错误标志与耗时 → 状态只区分 执行中/完成，不显示耗时。
4. 归档替代物理删除（多选操作文案为「归档」，AlertDialog 说明保留审计）。
5. 发送键位配置（Enter/Cmd+Enter 切换）无设置面 → 仅 Enter 发送 / Shift+Enter 换行（IME 安全），配置项不接入。
6. 参照 `bg-info/15` 实时章 → 本仓库沿用 emerald `/10`+`/30` 双档惯例（UI_DESIGN §5.1 合法变体）。
7. 会话页 Worker/System 分组为 A 档前端映射（选中 run 的 agent_id 泳道 / system 行过滤）；Planner 无独立数据不显示；B 档聚合接口待裁决（方案 §5.4/§10.1）。

## V1 验收记录（2026-11-22）

### 工程

- `cd webui && npm run typecheck` → 0 错误。
- `npm run build`（= `make webui` 的构建步）→ 成功；产物已写入 `internal/web/static/assets/app-DXu-6JnM.js`（hash 名防陈旧缓存），嵌入产物已更新。
- 后端零改动（S3 取 A 档，无新增接口）→ 无需 `go test`；mock 端点经 curl 验证。
- mock-backend 补齐 4 条对话路由（`/api/projects/{id}/conversations`、`/api/conversations/{id}/transcript`、PATCH、`/attachments`），转录投影与 `chat_transcript.go` 同语义（guidance→human、tool_call/result、vuln→finding、finished→system、审批→approval），全部 curl 验证通过。

### 走查方式

本会话无 node REPL 浏览器工具，降级为 playwright（npx 缓存模块）驱动系统 Chrome headless 走查；截图存 `gui-test-screenshots/chat-1to1/`。

### 断点与主题矩阵（暗色 4 断点 + 亮色 2 张）

| 视口 | 对话页草稿 | 对话页详情 | 会话页 |
|---|---|---|---|
| 1440×900 | ✓ chat-draft-1440 | ✓ chat-view-1440（+亮色） | ✓ sessions-main-1440（+亮色） |
| 1024×768 | ✓ chat-draft-1024 | ✓ chat-view-1024 | ✓ sessions-main-1024 |
| 768×1024 | ✓ chat-draft-768 | ✓ chat-view-768 | ✓ sessions-main-768（单列折叠栏） |
| 390×844 | ✓ chat-draft-390 | ✓ chat-view-390 | ✓ sessions-main-390（折叠标题栏 · run_01） |

- 对话页：`md` 起 `18rem + minmax(0,1fr)` 双栏、小屏 `minmax(10rem,15rem)+1fr` 两行、页面级无滚动、composer 钉底；`/chat` 已解除 `max-w-7xl`（app-shell 路由级分支）。
- 会话页：`lg` 起 `18rem + 1fr`；小屏会话栏折叠为标题条（含当前会话名与链路章），展开 40svh。
- 亮色主题（1440）双页对比度正常。

### 状态与交互矩阵

| 状态/交互 | 结果 |
|---|---|
| 空列表 → DraftChat（无 `?run=` 不再自动选第一条） | ✓ |
| 草稿懒创建：选任务→输入→Enter→创建 Run→guidance→写 `?run=` 切详情 | ✓ flow-draft-send-ok2（URL=/chat?run=inst_03，人工气泡经 SSE→refetch 到位，无乐观回显） |
| 草稿发送失败（mock 一次性守卫 409）文本保留 | ✓ flow-draft-send |
| 多选：表头三态勾选、已选计数、归档/完成 | ✓ flow-multiselect / flow-archive-done |
| 批量归档后空列表回退草稿、归档视图含已归档行 | ✓ chat-archived-list |
| 会话页 Worker 泳道（A 档 agent_id 映射）只读转录 | ✓ sessions-lane（仅该泳道工具行，无 composer） |
| 会话页运行控制（暂停/停止弹窗）、token/时长 meta | ✓ sessions-main-1440 |
| 转录七类行：human/assistant/tool_use/result/approval/finding/system | ✓（工具卡折叠摘要+状态、审批卡 amber+打开队列、发现卡、系统行、实时流式中尾标） |
| SSE 断线章（实时/重连中） | ✓ rail 头部（数据源 events store status） |

### 已知缺口（如实登记，不伪造）

1. **长转录分页/贴底/回到底部**：逻辑按契约实现（`before_seq` 上翻 + rAF 位置恢复 + 60px 贴底阈值 + 新消息计数 pill），但 mock 转录均 <80 行无法触发视觉验证；待真后端 9137 长历史冒烟。
2. **发送键位配置**（Enter/Cmd+Enter 切换）：无设置面，未接入（仅 Enter 发送 / Shift+Enter 换行，IME 安全）。
3. **草稿态附件**：上传 API 需要 run 目录，草稿态不提供回形针；创建后可传。
4. **工具卡耗时**：转录行无耗时字段，只显示 执行中…/完成；use/result 按相邻同名配对（无配对 ID）。
5. **S3 B 档**（Planner/Worker 独立会话聚合接口）：待裁决后另立后端工单。
6. 走查中顺带修复：任务详情 tab 条窄断点逐字竖排（`whitespace-nowrap`，task-detail.tsx）。
7. 截图对比基准为参照源码提取的类基准（P0 表），非 4zreco 运行时叠加截图；两仓库全局壳层（侧栏/Topbar/OpsPulse）按方案 §10.2 保留差异。


## 追加轮（2026-11-22 下午）：角色会话按实际设计补齐 + 输出中文化

### 角色扩展（用户裁决：thought/decision 等过程流必须是角色，对齐宪章七环）

- rail 角色组 = 主 Agent（各 Run，可交互）/ **规划**（build_plan/update_plan → 复用 PlanRail
  任务清单进度 + 原始规划事件行）/ **思考**（thought/thinking 流，violet Brain 行）/
  **决策**（decision 流，sky 边卡 = 认领假设记录）/ **系统审计**（phase+生命周期）/
  Workers（agent 泳道）。组仅在真实事件存在时出现，不编造。
- 数据源 = `GET /api/instances/{id}/events` 全量事件缓冲（`useRunEvents`，运行中 5s 轮询
  + SSE→400ms 防抖 refetch，与 rail/工作区共享 `["run-events"]` 查询键）；转录七类契约不动。
- 新组件 `components/sessions/role-events.ts`（角色谓词）+ `role-event-stream.tsx`
  （只读渲染）；`?agent=` 深链扩展取值 plan|thought|decision|system|泳道名。
- 持久化（已闭合，2026-11-22 下午）：`useRunEvents` 在实例内存缓冲缺席（引擎重启/
  实例逐出/404）时自动回落持久事件 journal——`GET /api/scans/{id}/events`
  （events.jsonl 窗口读取，offset 探 total 后取最新 1000 条窗口，与服务端
  maxEventsWindow 对齐；journal 写入不过滤类型，thought/decision/phase 全持久）。
  角色会话历史自此跨引擎重启存活。mock 补齐该端点信封（{events,total,offset,limit}）。
- 走查：1440×900 暗色，主会话/思考流/规划会话（PlanRail 进度 1/3 + 当前任务 + 原始行）三张
  截图通过（`sessions-roles-*.png`）；rail 条目标题与组头分离（态势研判 · 任务规划/思考流/决策记录）。

### 输出中文化（用户裁决：系统提示词保持英文，输出必须中文）

- 根因：`config.DefaultLanguage = "en"`——输出语言指令机制（OutputLanguageDirective）
  已存在但默认不激活，LLM 输出（含 "Worker…" 开头的派发叙述）全英文。
- 修复 1：`DefaultLanguage` en→zh-CN；`OutputLanguageDirective` 豁免条件从
  `==DefaultLanguage` 改为 `=="en"`（模型自身默认是英文，zh-CN 必须下指令）；
  `XALGORIX_LANGUAGE=en` 可还原英文输出。语言测试锚点同步更新。
- 修复 2：引擎固定播报本地化——新增 `internal/agent/op_text.go`（`op`/`opf`，
  按 config.Language 选 en/zh 模板），覆盖 MCP 状态、专家组暂缓、看门狗、白盒播种、
  用户消息回执、目标失联、上下文压缩、LLM 空响应、验证器四态、终态播报
  （Scan ended/stopped/canceled/interrupted/paused 家族）共 30+ 站点。
  **保持英文不动**：系统提示词、喂回 LLM 的 re-test/验证窗口指令（改变措辞会改变循环行为）。
- 测试：`internal/config`、`internal/agent` 全绿（两个测试锚定英文播报文案，显式
  `Language:"en"` 固定语义）；`make webui` 产物已重建。

### 追加轮 2（2026-11-22 晚）：思考流落地（推理模型 thinking 透传）

用户确认跑分使用推理模型 → 实现引擎侧 reasoning 透传（此前 `reasoning_content` 只探测存在即丢弃）：

- **llm**：`chatWithRetry`/`doChatWithUsage` 增加 `reasoningOut` 出参；新增
  `ChatWithUsageToolCallsMeta`（content 之外返回原始 reasoning 文本，`reasoningText`
  helper 容忍 string/array-of-parts 两种形状）；普通 `Chat`/`ChatWithUsage`/
  `ChatWithUsageToolCalls` 形状不变，verifier 等旁路保持丢弃语义。
- **engine**：`ChatMetaClient` 能力接口（调用时探针，测试 fake 仅实现 ChatClient 也兼容）；
  `ChatModel.WithOnReasoning(fn)` —— 仅在 recovery **接受**的响应上触发一次（失败重试
  不产生思考行），fire-and-forget，不参与门控/重试/判定。
- **agent**：`RuntimeConfig.OnReasoning` 注入回调，每轮合并一条 `thinking` 事件
  （16k rune 封顶）直发事件通道。
- **web**：`scan_session.go` 撤销 thinking 过滤闸（原契约"计数后丢弃不存不发"）——
  thinking 现与其他事件同样入 journal/缓冲/SSE 广播；迭代计数语义不变（thinking 本就是
  Iterations 的计数源）。转录端点不受影响（七类 switch 外的类型照旧跳过）。
- **前端**：思考角色谓词本就接受 thinking，零改动；引擎不发该事件时组诚实隐藏。
- 测试：`TestChatWithUsageToolCallsMeta_CapturesReasoning`（httptest，含普通调用形状
  不变断言）、`TestChatModelOnReasoning`（回调一次 + 能力探针回落）；全量
  `go test ./... -race` 通过。
- **生效条件**：思考流在引擎侧——正在跑分的旧二进制需 `make build` 重启后才会发
  thinking 事件；5176 观测入口无需任何操作即可展示新二进制的思考流。

### 追加轮 3（2026-11-22 晚）：思考流撤回 + 角色渲染收敛到 ExecutionTrace（用户裁决）

用户裁决：思考流没啥用；此前为角色设计的动态内容（ExecutionTrace）被绕开 → 双向修正：

- **思考流撤回**：agent 的 OnReasoning 回调移除、scan_session 的 thinking 过滤闸恢复
  （原设计语义：thinking 只作 Iterations 计数源，不存不发）。llm 的
  `ChatWithUsageToolCallsMeta` 与 engine 的 `WithOnReasoning` 能力保留（有测试覆盖、
  无回调时零行为），未来要接是一行接线。rail 思考组移除，思考谓词/键清理
  （SessionRole = plan|decision|system）。
- **角色渲染收敛**：`role-event-stream.tsx` 重写——规划/决策/系统审计三个角色会话
  统一复用既有 `ExecutionTrace` 分组动态内容（规划卡、工具配对、轮次分隔、审批、
  发现、泳道章全是既有渲染器），不再手写第二套行渲染；规划组保留 PlanRail 概览头。
  手写 RoleRow 删除。
- 验证：typecheck 0；`go build ./...` 通过；agent/web 包测试通过；`make webui` 已重建。
