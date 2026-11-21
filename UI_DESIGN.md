# UI_DESIGN.md — Xalgorix Web 前端界面规范

> **根据当前仓库反向整理**（审计对象：`webui/` 前端工程，2026-11-20）。本文不是来自原始设计稿的官方规范，而是从现有页面、组件、样式中提取的稳定规则与已知问题，供人类开发者与 AI 编程助手共同执行。前端默认遵循全局 `~/.zcode/AGENTS.md` 工作流（UI 任务先经 `unslop` 路由）；本文是本项目前端的第一规范，冲突时以本文 + 仓库现状为准。
>
> 标记约定：**[已确认]** 有稳定仓库证据，可直接用于新代码；**[合法变体]** 差异由尺寸/密度/状态/主题/响应式场景决定，须按适用条件使用；**[待确认]** 证据不足或有已知问题待裁决，禁止臆断。
>
> 证据路径均为仓库相对路径（相对 `/home/c1ay/xalgorix`）。

---

## 1. 文档目的、适用范围与维护方式

- **目的**：为 `webui/` 前端（自主 AI 渗透测试的操作员控制台）建立统一判断依据，指导新增页面、组件选型与存量维护，不作为重设计依据。
- **适用范围**：`webui/src/` 下全部路由与组件；不适用于 Go 后端（`internal/`）、`mock-backend.mjs`（仅开发用）。
- **维护方式**：界面规则变化时同步更新本文对应条目并补充证据路径；新增例外须登记到 §14。修改本文前先核对 §15 证据索引确认现状未变。

## 2. 产品界面特征与设计优先级

- **产品定位**：安全作业控制台（自主扫描启动/监控、22 阶段进度、exploit-verified 漏洞台账、实时事件流）。操作员核心问题：「正在跑什么 / 跑到哪个阶段 / 新证明了什么漏洞」。
- **基调**：[已确认] **dark-first、dense、quiet、technical**——Vercel/Geist 设计语言的"安全控制台"变体。`webui/src/styles.css` 自述："Geist + Vercel-inspired design tokens"，与 Xalgorix SaaS 令牌刻意保持一致（`--brand` 注释）。
- **优先级顺序**：机器数据可扫读（mono + tabular-nums）→ 状态语义确定性（severity/status 永远同色同形）→ 信息密度 → 装饰（仅雷达十字水印一处）。
- **记忆点**：[已确认] 全局唯一装饰元素是 `body::before` 的**雷达十字准星水印**（同心圆 + 十字线 + 刻度，`color-mix` 品牌色 4–12% 透明度，右上角，径向遮罩淡出）。`webui/src/styles.css:202-241`。除此之外不引入任何装饰性全局元素。

## 3. 已确认的设计原则与非目标

1. [已确认] **单一样式源**：全部样式 = Tailwind v4 工具类 + `webui/src/styles.css`（341 行，唯一样式表）。禁止 CSS-in-JS、CSS Modules、新建全局类（`.mono`、滚动条、`pulse-dot`、`drawer-in` 等机制类除外）。
2. [已确认] **组件优先**：元素一律从 `webui/src/components/ui/`（shadcn 体系，Radix 底座）与 `components/`（领域组件）组合，禁止手写同功能元素。新建前必须先在该目录搜索。
3. [已确认] **颜色语义优先于品牌色**：品牌红只作交互强调（按钮/焦点/链接/hover）；severity、状态、成功/警告一律走语义令牌，且以 **10% 透明底 + 30% 透明边 + 实心圆点的胶囊章**形态出现，绝不大面积填充。证据：`severity-badge.tsx`、`scan-status-pill.tsx`、`verification-badge.tsx`、badge.tsx success/warning/destructive 变体。
4. [已确认] **机器数据一律等宽**：时间戳、事件类型、工具名、target/endpoint/ID、token 计数、环境变量值、终端输出全部 `font-mono`（常配 `tabular-nums`）。这是本前端的质感来源，新增数据列必须沿用。
5. **非目标**：不做营销页构图（login 左栏是唯一例外且已存在）；不引入新 UI 依赖做设计点缀；不为单页新增语义重复的令牌或变体。

## 4. 页面骨架、内容容器与信息层级

- [已确认] **全局骨架**：`flex h-screen`（App 全程 `overflow-hidden`，只有 `<main>` 滚动）= 桌面 Sidebar（`w-60`，`hidden md:block`）+ Topbar（`h-14` backdrop-blur）+ `<main class="max-w-7xl">` 居中内容。证据：`webui/src/layout/app-shell.tsx`。壳层透明，让雷达水印透出。
- [已确认] **侧栏浮动变体（2026-11-21 用户裁决）**：桌面侧栏不再通栏贴边——外壳 `p-2 pr-0` 留间隙，aside 为 `rounded-xl border border-border bg-card shadow-sm` 浮动圆角面板（shadcn floating 变体的 xalgorix 令牌版）；移动端抽屉同步 `inset-y-2 left-2 rounded-xl border bg-card`。Topbar/内容区维持贴顶通栏，不随之圆角。
- [已确认] **路由树**（`src/router.tsx`，React Router 7 data router）：`/login`；`/` 下 AppShell 包裹：`""`(Overview) / `projects` / `tasks` / `tasks/:taskId` / `approvals` / `scans` / `scans/new` / `scans/:scanId` / `schedules` / `findings` / `reports` / `integrations` / `live` / `instances` / `email` / `settings` / `404`。
- [已确认] **侧栏信息架构（2026-11-21 按 Project→Task→Run 定稿，同日用户裁决项目位次）**：主组（无标签，项目作业面）= 概览/**项目**/任务/审批/发现——**顺序即实体层级：项目（作用域）→ 任务（目标）→ 审批（约束）→ 发现（产出）**；「执行」组 = 运行/实例/实时动态/新建扫描（新建扫描是遗留直启入口，R1 后评估并入任务）；「系统」组 = 定时计划/报告/邮件分诊/集成/设置。`live` 页已项目化（`useInstances(projectId)`）。
- [已确认] **鉴权门**：`AuthBootstrap` 探测 `/api/auth/status`，状态机 `loading|anon|authed|disabled|no-backend`；`no-backend`（网络错误/404）渲染 `BackendUnreachable` 整页修复指引而非误导性登录页。`RequireAuth` 跳 `/login` 保留 `from`。证据：`src/app.tsx`。
- [已确认] **列表页信息层级**：页标题（`text-2xl font-semibold tracking-tight`）→ 工具行（左筛选/搜索，右主操作）→ 内容区 → URL 同步分页（`components/Pagination.tsx`，`?page=&size=`，页大小 25/50/100/200 默认 50）。
- [已确认] **Settings 是单路由多 Tab**：`/settings?tab=`，六个 Tab（LLM / Engagement / Notifications / Email / Environment / Account），状态存 URL。新设置页扩展 Tab 而非新路由。
- [已确认] **移动端**：<768px 侧栏折叠为 Radix Dialog 抽屉（真对话框语义 + sr-only 标题 + `.drawer-in` 动画），`matchMedia` 在 ≥768px 自动关闭防止焦点困死。证据：`app-shell.tsx:35-73`。

## 5. Token 体系（`webui/src/styles.css` 为唯一定义处）

### 5.1 颜色（双主题，CSS 变量 + `@theme inline` 映射为 Tailwind 工具类）

- [已确认] **品牌色（crimson，双主题刻意相同，oklch）**：`--brand: oklch(0.56 0.22 20)`、`--brand-hover: oklch(0.52 0.22 20)`、`--brand-foreground: oklch(0.985 0 0)`、`--brand-subtle`（亮 `oklch(0.95 0.03 20)` / 暗 `oklch(0.2 0.035 20)`）、`--brand-border`（亮 `oklch(0.85 0.07 20)` / 暗 `oklch(0.4 0.12 20)`）、`--ring: oklch(0.65 0.22 20)`。
- [已确认] **表面与文本**：亮 `--background #ffffff` / `--foreground #0a0a0a` / `--card #ffffff` / `--border #e4e4e7` / `--input #e4e4e7` / `--muted #f4f4f5`；暗 `--background #050505` / `--card #0a0a0a` / `--card-hover #0d0d0d`(+border `#2f2f2f`) / `--border #262626` / `--input #1a1a1a` / `--muted #111111`。
- [已确认] **Severity（双主题相同，语义核心，禁改禁挪用）**：`--severity-critical: #dc2626`（红）、`--severity-high: #ea580c`（橙）、`--severity-medium: #ca8a04`（琥珀）、`--severity-low: #2563eb`（蓝）、`--severity-info: #525252`（灰）。
- [已确认] **状态**：`--success`（亮 `#059669` / 暗 `#10b981`）、`--warning`（亮 `#d97706` / 暗 `#f59e0b`）、`--destructive`（亮 `#dc2626` / 暗 `#ef4444`）。
- [已确认] **选区反色惯例**：`--selection-bg` 亮色下为黑底白字、暗色下白底黑字（`::selection`）。
- [合法变体] 组件内大量使用 Tailwind 调色板双档写法（如 `text-red-600 dark:text-red-400`、`border-emerald-500/30 bg-emerald-500/10`）作为 severity/状态章实现——这是现状惯例（区别于 4zrec 的纯令牌制），新代码**跟随现状惯例**：语义场景用 `/10` 底 + `/30` 边 + 亮暗双档文字。唯一字面 hex 例外：`token-usage.tsx` 图表色的 canvas 类中值色（`#3b82f6` 等 6 个，刻意不随主题）。

### 5.2 字体

- [已确认] 字族：`--font-sans: "Geist", ui-sans-serif, system-ui, …`；`--font-mono: "Geist Mono", ui-monospace, …`。**不打包字体文件**——按名引用，未安装则回退系统字体；不要引入 webfont。
- [已确认] 字号阶梯：`text-sm`(14px，正文/按钮主体) → `text-xs`(12px，辅助) → `text-[11px]` / `text-[10px]`（micro-label/胶囊章）→ `text-2xl font-semibold tracking-tight`（页标题）。卡片标题 `text-sm font-medium`。
- [已确认] **micro-label 惯例**：小节标签/eyebrow = `text-xs font-medium uppercase tracking-wide text-muted-foreground`（见 `ui/label.tsx`）；ASCII 章再加 `font-mono tracking-wider`。

### 5.3 圆角 / 阴影 / 间距 / 动效

- [已确认] 圆角令牌：`--radius-sm: 6px`、`--radius-md: 10px`、`--radius-lg: 12px`；组件主体 `rounded-md`，行级 `rounded-sm`，胶囊/开关 `rounded-full`。
- [已确认] 阴影极简：卡片**无阴影**（`border + bg-card` 平面），popover `shadow-md`，dialog `shadow-lg`。
- [已确认] 间距惯例：卡片 `p-5`（header/content，content 另 `pt-0`）、页面节奏 `space-y-6`、网格 `gap-3/gap-4`、表格单元 `px-3 py-3`、侧栏菜单项 `px-2.5 py-1.5`。
- [已确认] 动效白名单：`transition-colors`(0.15s)、`pulse-dot`（1.6s 呼吸点，连接/活跃指示）、`drawer-in`（0.2s 滑入）、`animate-pulse`（skeleton）、spinner 图标。全局 `prefers-reduced-motion: reduce` 一刀切关闭（`styles.css:326-340`）。**新增动效必须进白名单**。

## 6. 公共组件目录、变体与复用规则

**基础组件 `src/components/ui/`**（全部 forwardRef + `cn()`）：

- **Button** [已确认]：cva 变体 `default`(品牌底)/`secondary`(描边)/`outline`/`ghost`/`destructive`/`link`；尺寸 `default h-9` / `sm h-8 text-xs` / `lg h-10` / `icon h-9 w-9`；焦点 `focus-visible:ring-1 ring-ring`；图标 `[&_svg]:size-4`。
- **Card** [已确认]：`rounded-md border bg-card` + `overflow-hidden`；`CardHeader/CardContent p-5`（Content `pt-0`）、`CardTitle text-sm`、`CardDescription text-xs`。卡片即平面，禁止卡内再嵌带边框面板（同 4zrec「卡中卡」禁令）。
- **Badge** [已确认]：变体 `default/outline/muted/success/warning/destructive`；后三者 = 10% 底 + 30% 边 + 亮暗双档文字。
- **Dialog** [已确认]：Radix；overlay `bg-black/70 backdrop-blur-sm`；内容居中 `max-w-lg p-6 shadow-lg`。
- **Input/Textarea/Select/Switch/Tabs/Separator/Skeleton/Label** [已确认]：Input `h-9 bg-input/40`；Tabs 是**下划线式**（非药丸），active 项 `after:` 1px 下划线；Switch 单色（checked = `bg-foreground`，不用品牌色）；Label 即 micro-eyebrow。

**领域组件 `src/components/`**（新建前先找这里）：

- `severity-badge.tsx` / `scan-status-pill.tsx`（11px 大写胶囊 + 实心点，点 `aria-hidden`）——一切 severity/状态展示的唯一入口，配 `lib/utils.ts` 的 `normalizeSeverity`/`severityRank`。
- `verification-badge.tsx`：verified=emerald 盾、manual-review=amber 盾，对应后端三标签（前端只显示两种形态）。
- `phase-progress.tsx`：22 阶段分段进度条（`role="progressbar"` + aria-valuenow）+ 磁贴网格；阶段数据来自**生成文件** `methodology-phases.json`（`tools/gen-webui-phases` 生成，Go 测试保证与后端同步——改阶段只能改 Go 侧再重新生成）。
- `ops-pulse.tsx` [已确认，2026-11-20]：**OpsPulse 全局态势条**（4zreco 模式移植）——topbar 下方 `h-9` 常驻单行：当前项目 | 待审批计数 + 最近截止倒计时 | 活跃 Run 数。全零时保持可见（静默"一切正常"），待审批 >0 时 amber、最近截止 <60s 转 red。是**唯一**的全局脉搏元素，不再引入第二处。
- `approval-card.tsx` [已确认，2026-11-20]：**审批卡**——行为内容（命令/请求行）mono 置顶，完整 JSON 折叠（`aria-expanded` 切换），语境切片（观测=sky 点/推断=violet 点/human=emerald 点 + 置信度 + 矛盾计数徽标）在决策按钮上方；倒计时章 amber→<60s red；"截止前未决 = 拒绝"提示常驻。审批队列页（`/approvals`）：行内决策、`j/k` 移动 `a/r` 决策 `b` 全选（input focus 时挂起）、checkbox 独立列（不与卡头重叠）、cursor 高亮用 `ring-ring/40`（全色 ring 仅用于 `?focus=` 深链）。
- `live-feed.tsx`：终端式事件流（mono 时间戳 `w-20` + 事件类型色标 `w-24` + 工具名 `w-32` + 截断消息；可展开 `<pre>` 详情，错误红底、参数/输出灰底 `max-h-72`；`TYPE_COLOR` 色标映射）。
- `metric-card.tsx` / `states.tsx`（EmptyState 虚线框 / ErrorState 红调）/ `connection-status.tsx` / `command-palette.tsx`（cmdk + Ctrl/Cmd+K）/ `markdown.tsx`（零依赖 MD 子集渲染器，专吃 LLM 输出）/ `token-usage.tsx`（内联 SVG 折线+堆叠条）/ `copy-button.tsx` / `Pagination.tsx` / `new-scan-dialog.tsx` / `legacy-import-banner.tsx`。
- `project-switcher.tsx` [已确认，2026-11-20]：侧栏顶部项目切换器（Radix DropdownMenu，选择持久化 `localStorage["xalgorix.project"]`）。**项目作用域口径唯一下沉在 `lib/use-active-project.ts` 的 `useActiveProjectId()`**（store 值 → 首个项目回退）：任务页/审批页/态势条一律用它，禁止各自再写 `projectId ?? projects[0]` 类回退（口径分叉曾导致任务页空态 bug）。
- `ui/alert-dialog.tsx` [已确认，2026-11-20]：危险操作确认（Radix）；`AlertDialogAction` 支持 `variant="destructive"`。**删除类操作一律 AlertDialog + 明确后果文案，替代 `window.confirm`**（`buttonVariants` 已从 button.tsx 导出供其复用）。
- `task-form-dialog.tsx` / 任务详情页 [已确认，2026-11-20；2026-11-21 双栏改版]：任务 = Goal+Context 宿主；表单字段：名称/目标(必填)/指令/扫描模式/强度/定时（开关+间隔+时间）。**详情页 = 双栏 mission control（4zreco sessions-tab 模式）**：左栏（主，`lg:sticky` 视口高）**常驻实时事件流**（最新 Run transcript + 行内审批卡，`LiveFeed fillHeight`），右栏（400px）Tabs = 概览/发现/运行/介入；原「事件」子页签取消（事件流即主体，不再藏子页签）。小屏（<lg）纵向堆叠：事件流在上（420px 定高）、上下文在下。**缺口的引擎数据未就绪时显示一句说明性占位，禁止编造数据**。
- `guidance-tab.tsx` [已确认，2026-11-21；同日被 `run-console.tsx` 会话控制台吸收]：插入意图 composer 的独立 Tab 版本（右栏），功能并入左栏控制台后保留文件作为历史。
- `run-console.tsx` [已确认，2026-11-21]：**任务详情左栏会话控制台**（4zreco sessions-tab 功能等价物）。头部 = Run 切换器（Select，默认最新活跃）+ 运行控制（暂停/恢复/停止，instance API）；主体 = transcript（`LiveFeed fillHeight`：agent 步骤行 + 行内审批卡 + **guidance 事件渲染为右对齐品牌色人工气泡**（`GUIDANCE_INJECTED` 色标 violet、气泡 `rounded-lg rounded-tr-sm bg-primary`，混排进步骤流——4zreco UserRow 模式）；底部 = **双模式 composer 常驻**：「注入意图」（guidance API，进 loop；无 Run 自动排队）/「提问（旁路）」（`/api/chat`，永不进 loop，问答对以 violet 卡显示在 composer 上方、本地保留最近 3 对）+ paused 时「不带消息直接恢复」ghost 按钮 + 状态提示行（运行中注入绿 / 已暂停 / 无运行排队灰）+ 码点计数。**无乐观回显**：注入经 mock WS 广播 `guidance_injected` 后从流渲染（服务端先持久化再广播，契约 §3.1）。
- `plan-rail.tsx` [已确认，2026-11-21]：**规划轨**——会话控制台顶部常驻条：已处置/总数 + 进度条 + 当前阶段（`instance.current_phase`，`/22`）+ 当前任务；展开为任务列表（状态图标 pending/in_progress/completed/skipped + `P<n>` 阶段徽标 + mono task id）。任务清单从 `build_plan`/`update_plan` 的 tool_args 重建（`lib/execution-trace.ts`），引擎自动规划未上报清单时**显示实例 `plan_tasks_*` 计数并明确说明**，禁止编造任务行。
- `execution-trace.tsx` [已确认，2026-11-21]：**分组执行轨迹**——任务详情会话控制台默认视图（`轨迹/事件` 分段切换，原始事件仍走 `LiveFeed`）。工具调用与结果按 agent+工具名 FIFO 配对成一步（参数 JSON / 输出 / 错误可展开，运行中 spinner、完成仅对勾 + sr-only、失败红叉）；`thinking/thought` 中 `Iteration N` 作轮次分隔线、自由推理为 violet `Brain` 单行；`build_plan/update_plan` 渲染为 violet 规划卡；审批复用 `ApprovalInlineBar`、发现复用 `SeverityBadge`、assistant 左气泡与人工右气泡沿用既有语义。自动滚底沿用 <48px 阈值。
- `approval-inline-bar.tsx` [已确认，2026-11-21]：事件流内嵌紧凑审批条——`approval_requested` 事件行下**默认展开**（HITL 不藏折叠里）：mono 摘要 + 倒计时 + 行内批准/拒绝 + `/approvals?focus=` 深链；409（他处已决）翻转为"已被裁决"章。`TYPE_COLOR` 补 approval_requested(amber)/approval_decided(emerald)/guidance_*(violet/emerald)。
- `association-graph.tsx` [已确认，2026-11-21]：**信息全景关联图画布规范**（替代 G6 的自绘方案，无新图库）：SVG 分层布局（源/目标两层列 + 边），节点 = mono 短 id + nature 胶囊色，边按关系 kind 着色——supports=emerald / contradicts=red / references=sky / same_session=amber（`/10` 底 `/30` 边惯例）；图例置于图下方 hairline 分隔处，**四类词表全列但只渲染真实存在的边**（当前引擎仅产 supports，其余 kind 给说明性空态，禁止编造假边）；无障碍平行列表随图提供；节点点击经 `relation_ids` 进关联 Tab。画布色值随主题令牌，不引入 canvas 中值色例外。

## 7. 表单、表格、弹窗与危险操作规则

- [已确认] **表单**：现状用受控 `useState`（RHF/zod 已声明未使用，见 §14）；弹窗表单 = Dialog + Input/Select/Textarea，主按钮在 footer。新表单维持 useState 惯例，不强行引入 RHF。
- [已确认] **表格**：手写 `<table>`（`@tanstack/react-table` 已声明未用，勿启用）；宽表列按重要性从右往左响应式隐藏；行操作 ≤3，超出收进下拉/图标按钮。
- [已确认] **危险操作**：删除/Stop all 用红色 destructive 按钮 + `window.confirm` 文案带数量；批量操作先勾选并显示影响数量。现状确认弹窗用原生 confirm（非 AlertDialog），跟随现状。
- [已确认] **上传**：走 `api` 封装的 upload 端点（120s 超时），配 `<Input type="file">`。

## 8. 加载、空、错误、成功状态

- [已确认] 加载：块级 `Skeleton`（`animate-pulse bg-muted/60`）；轮询刷新保留旧数据（全站 `placeholderData: keepPreviousData` 惯例）防表格闪烁。
- [已确认] 空：一级内容区用 `EmptyState`（虚线边框卡）；卡片内小节单行灰字提示为合法形态。
- [已确认] 错误：区域级 `ErrorState`（红调卡）+ 重试；401 全局事件 `xalgorix:auth-expired`（1s 防抖）驱动登出，不靠各页自理。网络失败归一为 `status: 0` 以区分"无后端"。
- [待确认] **成功反馈缺失**：sonner `Toaster` 已挂载（`main.tsx`）但全站无一处 `toast()` 调用——mutation 成功无反馈是**现状缺口**，新增 mutation 反馈时统一走 sonner（依赖已装），并在落地时更新本条。

## 9. 响应式与实时数据策略

- [已确认] 断点：Tailwind 默认（sm 640 / md 768 / lg 1024 / xl 1280）；`md` 是侧栏/抽屉切换点。
- [已确认] **服务端状态轮询节奏**（`src/api/queries.ts`，勿随意改）：status 5s、instances 8s、scans/findings/schedules 15s、scan detail 运行中 2s（动态 refetchInterval）、instance events 5s、queue 10s、findings summary 10s。
- [已确认] **WS 实时流**（`store/ws.ts`）：指数退避重连（1s→2s→…→30s 封顶，成功归零）；事件环形缓冲上限 1000、去重键 `ts|instance|type|tool|msg[0:80]`（最近 200 键）；`paused` 时事件**丢弃不缓存**（现状契约）；按实例 subscribe/unsubscribe。页面合并 WS 实时 + REST 历史（`mergeFeedEvents`）；终态扫描才向后翻页拉历史（`/api/scans/:id/events?offset&limit`）。
- [已确认] **计数不回退惯例**：汇总数字用服务端稳定值 + `Math.max` 合并（overview/findings），避免轮询期间计数回跳。

## 10. 主题规则

- [已确认] **默认 dark**：`<html class="dark">`（index.html 出厂即暗色），`@custom-variant dark (&:where(.dark, .dark *))` 类切换。
- [已确认] 三态主题 `light/dark/system`，持久化 `localStorage["xalgorix.theme"]`；`index.html` 内联 pre-paint 脚本防闪白（同步切换 `theme-color` meta `#ffffff`/`#050505`）；system 模式 `matchMedia` 实时跟随。
- [已确认] 暗色适配义务：新组件必须在暗色下核查对比度；叠层用 `bg-*/10` 而非实体色。

## 11. 图标、文案、i18n 与无障碍

- [已确认] 图标仅 `lucide-react`，尺寸 `size-3/3.5/4/5`；禁止 emoji 作图标。
- [已确认] **i18n 双语**：`src/i18n/`（en 源 + zh-CN 全量平行词典 ~230 键，按 `nav./topbar./severity./status./common./settings./` 及各页面命名空间）；`useI18n().t()` 回退 en→原键；语言持久化 `localStorage["xalgorix.lang"]` 并同步 `<html lang>`；无本地选择时采纳服务端 `XALGORIX_LANGUAGE`。**新增 chrome 字符串必须进词典**（en+zh-CN 同步）；已知大量正文文案硬编码英文，见 §14。
- [已确认] **术语表（2026-11-21 定稿，R1 后全站强制）**：Scan 记录一律称**运行（Run）**（`nav.scans`/`scans.title` = 运行/Runs）；发现页称**发现**；schedules 称**定时计划**（过渡期独立页，副标题已标注 R1 后降为任务属性，届时移除导航项）。新文案禁用"扫描任务"这类与「任务」冲突的旧称。
- [已确认] 无障碍基线（正例，保持）：Radix 底座、移动抽屉真对话框语义、`aria-label` 全覆盖图标按钮、`aria-current="page"` 分页、`aria-live="polite"` 指引反馈、全局 reduced-motion。已知缺口见 §14。

## 12. 技术实现约束

- [已确认] 栈：React 19 + Vite 8 + TS 7（strict）+ Tailwind v4（**CSS-first，无 tailwind.config**，令牌全在 `styles.css` `@theme inline`，以 `var()` 活引用映射故 `.dark` 免重建）+ Radix + zustand 5 + TanStack Query 5 + react-router 7。
- [已确认] **嵌入构建链**：`vite build` 产物输出到 `../internal/web/static`（hash 文件名防陈旧缓存）→ `go:embed` 进二进制。**改前端必须 `make webui`/`make build` 重建二进制才在线上 9137 生效**。
- [已确认] 状态分层：服务端状态一律 TanStack Query（`api/queries.ts` 的 `qk` key 工厂 + ~40 hooks）；客户端状态 zustand（`store/auth.ts`、`store/ws.ts`、command-palette 内联 store）；theme/i18n 是 `useSyncExternalStore` 手写微 store。API 调用一律走 `api/client.ts` 的 `http()`（含 401 事件/超时/HttpError 归一），**禁止页面裸 fetch**（现状两处违例见 §14）。
- [已确认] 类名合成 = `cn()`（clsx + tailwind-merge，`lib/utils.ts`）；变体 = cva（button/badge）；共享菜单类常量 `menuContentClass`/`menuItemClass`。
- [已确认] mock 后端：`npm run dev` 自动拉起 `mock-backend.mjs`（:8787，合成事件流每 1.5s），配 `VITE_API_TARGET` 指向真后端。

## 13. 新页面开发流程与验收清单

**流程**：读本文 → 在 `components/`、`components/ui/` 与相似页面找可复用件 → 按 §4 骨架搭页 → 数据走 `api/client.ts` + `queries.ts` → 四态齐备 → `npm run typecheck` → `make webui` 同步嵌入产物。

**验收清单**：

1. [检查] 机器数据列 `font-mono`(+`tabular-nums`)；severity/状态只用既有胶囊组件。
2. [检查] 无卡中卡；无新增全局 CSS；动效在白名单内。
3. [检查] chrome 文案进 i18n 词典（en+zh-CN）；无 emoji 图标。
4. [视口] 1280/1024/768/375 四档：宽表列逐级收窄无横滚，`md` 断点侧栏切抽屉。
5. [视口] 暗色（默认）优先核查对比度；亮色为一级主题不得劣化。
6. [键盘] Tab 顺序可用、弹层 Esc 可关、图标按钮有 aria-label。
7. [检查] 加载(Skeleton)/空(EmptyState)/错误(ErrorState)+401 全局事件 齐备；轮询用既有节奏。
8. [构建] `npm run typecheck` 0 错误；改后 `make webui` 重建（否则嵌入产物陈旧）。

## 14. 已知问题与待确认项（改动相关区域时顺带处理，勿静默扩散）

| # | 条目 | 现状证据 | 建议 |
|---|---|---|---|
| K-1 | ✅已修（2026-11-21，R5） | 批量删除与设置/定时计划 mutation 已接 sonner；存量页面继续按页收敛 | 已闭合主要高频入口 |
| K-2 | ✅已修（2026-11-21，R5） | 删除 login 页硬编码假统计 | 已闭合 |
| K-3 ✅已修（2026-11-21） | Overview 对每条 scan 发独立 `getScan`（N+1 fan-out），且全局口径漏进项目视图（隔离破口） | 已改为项目作用域 `/api/findings?project_id=` 投影（`useFindingsList`），fan-out 删除 | 已闭合；回归截图确认跨项目条目不再混入 |
| K-4 ✅已修（2026-11-21） | 两处裸 fetch `/api/findings/summary` 绕过 `http()` 封装 | 收敛为集中 `useFindingsSummary(projectId?)`（`api/queries.ts`），overview/findings 共享 | 已闭合 |
| K-5 | ✅已修（2026-11-21，R5） | `scans/findings/scan-detail` 批量删除改 `Promise.allSettled` + sonner 部分失败提示 | 已闭合 |
| K-6 | ✅已修（2026-11-21，R5） | live-feed 仅在距底 <48px 时自动滚动 | 已闭合 |
| K-7 | ✅已修（2026-11-21，R5） | phase 条加 `role/aria-label/tabIndex/focus ring` | 已闭合 |
| K-8 | ✅已修（2026-11-21，R5） | 事件展开行补 `aria-expanded/aria-controls` + `useId` | 已闭合 |
| K-9 | ✅已修（2026-11-21，R5） | 移除 `@tanstack/react-table`、`react-hook-form`、`@hookform/resolvers`、`zod`、`date-fns` | 已闭合 |
| K-10 | i18n 覆盖仅限 chrome，正文文案大量硬编码英文——**R3 起默认语言 = zh-CN**（`DEFAULT_LANGUAGE`）；R5 信息空间新页已全量双语 | `src/i18n/translations.ts` | 新代码进词典；存量按页面逐步收敛 |
| K-11 | 22 阶段数据来自生成文件，禁手改 | `src/methodology-phases.json` | 只能经 `tools/gen-webui-phases` 从 Go 侧再生成 |
| K-12 | WS paused 丢弃事件不缓存（契约见 §9） | `store/ws.ts:109-111` | 若要"暂停不丢"需改设计，先裁决再动 |

## 15. 证据索引（2026-11-20 审计）

- 令牌/主题/水印/滚动条/reduced-motion：`webui/src/styles.css`（341 行，唯一样式表）
- 入口与路由：`webui/index.html`（pre-paint 脚本）、`src/main.tsx`、`src/router.tsx`、`src/app.tsx`
- 布局：`src/layout/app-shell.tsx`、`sidebar.tsx`、`topbar.tsx`
- 状态：`src/store/{auth,ws,theme}.ts`、`src/i18n/index.ts`
- API：`src/api/client.ts`（http 封装/401 事件）、`src/api/queries.ts`（qk 工厂/轮询节奏）
- 基础组件：`src/components/ui/`（12 件）；领域组件：`src/components/`（16 件）
- 代表页：`pages/scan-detail.tsx`（最重页面：阶段+事件+token+子域）、`pages/new-scan.tsx`（最全表单）、`pages/settings.tsx`（Tab 型设置）、`pages/live.tsx`（全局事件流）
- 构建：`webui/vite.config.ts`（输出到 `../internal/web/static`、mock 插件）、`webui/mock-backend.mjs`、根 `Makefile`
- **2026-11-20 R3 二开落地证据**（项目/任务/审批/态势条，契约 `docs/API_CONTRACT.md`）：OpsPulse 态势条 `components/ops-pulse.tsx`；项目切换器 `components/project-switcher.tsx` + 口径 `lib/use-active-project.ts`；审批卡 `components/approval-card.tsx` + 队列页 `pages/approvals.tsx`（j/k/a/r/b 键盘流、批量、`?focus=` 深链消费后 replaceState）；任务页/详情页 `pages/tasks.tsx`、`pages/task-detail.tsx` + 表单 `components/task-form-dialog.tsx`；项目页 `pages/projects.tsx`；危险确认 `components/ui/alert-dialog.tsx`（`@radix-ui/react-alert-dialog` 新依赖，`buttonVariants` 随之导出）；倒计时 `lib/use-countdown.ts`；侧栏重组三组（作业/扫描/系统）；`DEFAULT_LANGUAGE="zh-CN"` + ~150 新双语键；mock 后端全端点实现（含服务端超时=拒绝模拟）。浏览器实测（1440×900 Chrome headless）四页通过：中文默认、项目隔离（Acme/Globex 任务与审批单互不可见）、态势条倒计时跳动、卡片布局无重叠。cursor 高亮 `ring-ring/40`（全色 ring 仅深链）。开发用 `VITE_MOCK_PORT` 换 mock 端口（8787 被本机 4zreco 生产占用时）。
- **2026-11-21 R3.1 人工介入 + 项目化收敛证据**（契约 §3.1/§3.2）：介入 Tab `components/guidance-tab.tsx`（插入意图 composer + 右对齐 bubble 列表 + injected/queued/consumed 三态，浏览器实测：活跃 Run 提示→发送→toast→bubble 落列）；事件流行内审批条 `components/approval-inline-bar.tsx` + `live-feed.tsx` 集成（默认展开、409 幂等翻转、实测 amber 事件行内决策卡）；任务详情事件 Tab 补 WS 实时流合并（`filterEventsForInstance`，修复只读 REST 历史导致空流）；overview K-3 重构（`useFindingsList(projectId)` 投影替代 getScan fan-out，隔离破口闭合，FINDINGS 9→7 回归验证）；K-4 收敛（`useFindingsSummary` 集中）；契约新增 §3.1 Guidance（无乐观回显 + request_id 幂等 + 4000 码点）与 §3.2 项目过滤（instances/findings/summary/scans 可选 `project_id`）；mock 对应实现含 WS approval ping。
- **2026-11-21 任务详情双栏改版证据**（用户痛点："任务详情看不到事件流"）：`pages/task-detail.tsx` 重构为 grid 双栏（`lg:grid-cols-[minmax(0,1fr)_400px]`），左栏事件流 Card 常驻 + 右栏 Tabs（概览/发现/运行/介入，「事件」页签取消）；`components/live-feed.tsx` 新增 `fillHeight` prop（根元素 flex 撑满 + 滚动区 `min-h-0 flex-1`，默认 false 不影响既有调用）。1600×900 与 760×900 双截图验收。开发环境：`VITE_MOCK_PORT=18998 npm run dev` → http://localhost:5173/（8787 被本机 4zreco 生产占用）。
- **2026-11-21 浮动侧栏 + 视口定高双栏证据**（对照 4zreco `sessions-tab.tsx` 骨架与 shadcn floating 变体）：① app-shell 桌面侧栏外壳 `p-2 pr-0` + aside `rounded-xl border shadow-sm`，移动抽屉 `inset-y-2 left-2 rounded-xl border bg-card`；② task-detail 大屏改 `lg:h-[calc(100vh-16.5rem)] lg:grid-rows-[minmax(0,1fr)]` **视口定高、面板内部滚动**（页面级不滚，对齐 4zreco `h-[calc(100svh-13rem)]` 骨架），左流右 Tabs 均 `overflow-hidden / lg:overflow-y-auto`。截图验收：浮动圆角侧栏 + 双面板视口内实时事件滚动。
- **2026-11-21 会话控制台证据**（用户裁决"任务详情功能也要与 4zreco 一致：对话/人在环路等所有内容"，双模式 composer 经 AskUserQuestion 确认）：`components/run-console.tsx`（Run 切换器 + 暂停/恢复/停止 + transcript + 双模式 composer）；`live-feed.tsx` guidance 气泡渲染（`WSEvent.guidance`）；mock WS 广播 `guidance_injected`（wsClients 注册表）。浏览器实测：注入意图 → WS 广播 → transcript 出现 `GUIDANCE_INJECTED` 行 + 右对齐品牌色气泡（已注入 RUN_01）混排进 agent 步骤流 + toast 确认。旁路问答（`/api/chat`）问答对 violet 卡本地展示（最近 3 对）。契约零新增（§3.1 WS 事件即为此设计）。
- **2026-11-21 会话控制台收尾证据**（"按照这个实施"=方案剩余项）：① **停止软取消**——停止按钮改 AlertDialog（原因输入可选，文案说明"未决审批将一并取消 + 原因帮助引擎规划下次运行"），`stopInstance(id, reason?)` 落 `stop_reason`（契约 §3 增补注记；4zreco soft-cancel→planner replans 语义；mock/instances/scan-detail 调用点同步适配）；② **agent 视角切换**——RunConsole 头部第二选择器（agentScope），transcript 按 `agent_id` 收窄（"全部 agent"默认），事件维度的 `agent_id` 与 `instance_id` 正交（Run 过滤走后者、视角走前者）。浏览器实测：切 specialist 后 transcript 只剩该 agent 的 httpx 事件；停止弹窗 reason 输入 + destructive 确认。开发环境注意：**后台 dev server 必须用工具的 run_in_background 模式启动**（shell 后台子进程会被会话回收，nohup/disown 均不可靠），`VITE_MOCK_PORT=18998` 不可漏（漏了 mock 撞 8787 的 4zreco 生产端口）。
- **2026-11-21 融合形态定稿证据**（用户裁决：设计方案信息主轴 × 4zreco 会话模式）：① `run-console.tsx` agentScope **Select 升级为会话 chips 行**（`全部 agent / ●主循环 / spec-*`，横向滚动，coordinator 绿点标记；specialist 短名去实例前缀）；② transcript 历史自管（`api.runEventsPage(offset,limit)` 分页拉取 + 「加载更早」回翻 + WS 实时叠加，不再轮询全量端点）；③ 右栏新增**「态势」Tab**（默认落地，认知层）：待审批（本任务过滤，`ApprovalInlineBar` 行内决策）+ 关键发现 top3（severityRank 排序）+ 缺口占位（依赖引擎 M3 上报，禁止编造）；④ mock 合成 16 条确定性历史（coordinator/specialist 混流，含 phase/thought/tool/http/vuln 分型）支持分页回翻。1600×900 截图验收全部到位。**主次关系定稿：会话流 = 过程明细，信息态势 = 认知状态（设计方案主轴），4zreco 只提供交互模式参考（不搬会话实体/暂停门语义）。**
- **2026-11-21 融合形态定稿证据**（用户裁决：设计方案信息主轴 × 4zreco 会话模式）：① `run-console.tsx` agentScope **Select 升级为会话 chips 行**（`全部 agent / ●主循环 / spec-*`，横向滚动，coordinator 绿点标记；specialist 短名去实例前缀）；② transcript 历史自管（`api.runEventsPage(offset,limit)` 分页拉取 + 「加载更早」回翻 + WS 实时叠加，不再轮询全量端点）；③ 右栏新增**「态势」Tab**（默认落地，认知层）：待审批（本任务过滤，`ApprovalInlineBar` 行内决策）+ 关键发现 top3（severityRank 排序）+ 缺口占位（依赖引擎 M3 上报，禁止编造）；④ mock 合成 16 条确定性历史（coordinator/specialist 混流，含 phase/thought/tool/http/vuln 分型）支持分页回翻。1600×900 截图验收全部到位。**主次关系定稿：会话流 = 过程明细，信息态势 = 认知状态（设计方案主轴），4zreco 只提供交互模式参考（不搬会话实体/暂停门语义）。**
- **2026-11-21 对话流（流式 assistant 消息）证据**（用户痛点："没有 SSE、对话内容也没有"）：传输层沿用 WS（真实后端即 WS，与 SSE 同为服务器推送；缺的是**增量语义**而非传输）。**协议**：`assistant_delta {message_id, delta, agent_id, instance_id}` 增量推送 → `assistant_done {message_id, content}` 固化（`WSEvent` 扩展 `message_id/delta`）。**ws store**：delta 累积进 `streaming` 缓冲（不进事件数组），done 时归一 `type:"assistant"` 入事件流。**渲染**：完成消息 = 左对齐 agent 气泡（`rounded-tl-sm border bg-card` + agent 名 mono 标签，与右对齐品牌色人工气泡构成对话对）；生成中 = 流尾打字气泡（`typing…` + `▌` 光标，`animate-pulse` 白名单内）。mock：LLM 回合合成（文本分片每 220ms 推一片，渗透语气真实分析、不编造漏洞结论）。浏览器实测：typing 气泡 → done 固化 → assistant 气泡混排进事件流（人工右对齐 / agent 左对齐对话对）。**后端对接要点**：引擎的 LLM 文本输出按同协议走 WS 即接上（delta 粒度不限）。
- 未覆盖区域：无 WCAG 对比度量化测量、`methodology-phases.json` 生成链细节。
- **2026-11-21 任务详情执行轨迹证据**（参考本地 4zreco sessions-tab 的 `Transcript`/`ExecView` 分组回放）：`lib/execution-trace.ts`（`extractPlan` 从 `build_plan` 任务数组 + `update_plan` 状态迁移重建规划；`planProgress` 事件优先、实例计数兜底；`buildTrace` 配对工具调用/结果、生成轮次/思考/审批/通知项）+ `components/plan-rail.tsx` + `components/execution-trace.tsx`；`run-console.tsx` 增加 `轨迹/事件` 视图切换与规划轨，历史分页结果按事件键去重（`/api/instances/{id}/events` 当前忽略 offset 返回全量缓冲）。回归：mock 合成 `build_plan`/`update_plan` 事件；1600×1000 与 390×844 截图验收，移动端 `grid-cols-1 + min-w-0` 无横向溢出。
- **2026-11-21 R5 信息空间证据**：`pages/info-space.tsx`（项目级 `/info`，区域/关联/语境切片三 Tab）+ `components/info-item-card.tsx`（observed=sky / inferred=violet，confidence/scan_id mono）+ `components/association-graph.tsx`（无新图库的 SVG 分层图 + 可访问列表）。数据走 `/api/info/{region,context,association}`，disabled 显示明确 PostgreSQL 冷路径说明；截断/缺口语义用 amber 提示。新页所有 chrome 文案已进 en/zh-CN 词典。
