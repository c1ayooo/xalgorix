# 会话页与对话页 1:1 复刻方案

> 状态：**已实施（A 档）**（2026-11-22；验收记录与已知缺口见
> `docs/SESSIONS_CHAT_1TO1_ACCEPTANCE.md`；S3 B 档聚合接口待裁决，未实施）
> 参照：本地 `/home/c1ay/4zreco`
> 范围：任务详情「会话」页 + 项目级「对话」页
>
> 许可约束：4zreco 为 AGPL-3.0，本仓库为 Apache-2.0。只复刻视觉、布局和交互语义，
> 不直接复制其源码；实现按本仓库 React + Vite + Tailwind 结构重写。

## 1. 结论

「会话」和「对话」是**两个独立页面**，不是同一套 UI 的两个入口。

| 页面 | 当前路由 | 4zreco 参照 | 关系 |
|---|---|---|---|
| **会话页** | `/tasks/:taskId?tab=sessions` | `web/src/app/(main)/function/tasks/detail/_tabs/sessions-tab.tsx` | 任务内多角色、多会话的运维视图 |
| **对话页** | `/chat` | `web/src/app/(main)/chat/page.tsx` | 项目级会话中心，列表 + 草稿/详情 |

两页分别复刻、分别验收。不得把会话页合并进对话页，也不得强行让两页共用同一页面骨架。

可共用的只有无业务语义的基础件：Button、Dialog、Dropdown、Markdown 渲染、工具折叠卡、
状态徽标等。会话分组、路由参数、空态、选中逻辑和 Composer 语义各自独立。

## 2. 目标与非目标

### 2.1 目标

1. 两页在相同数据、相同浏览器、相同主题下，布局、尺寸、颜色、字体、间距、滚动行为与
   4zreco 对应页面一致。
2. 对齐核心交互：列表选择、草稿新建、历史分页、贴底滚动、工具展开、审批卡、
   运行控制、Composer 发送/停止、移动端折叠。
3. 保留本仓库既有能力：项目隔离、任务与 Run 模型、认证、SSE、审计、归档语义、
   full/human 双投影、无 PostgreSQL 降级。
4. 改造集中在 `webui/src`；只有视觉复刻无法用现有数据满足时，才增加后端数据适配。

### 2.2 非目标

- 不复制 4zreco 的 Next.js、Agent、LLM Profile、旁路提问等业务栈。
- 不为了视觉一致伪造 Agent、消息、未读数、模型或审批数据。
- 不修改 Verifier、`report_vulnerability` 闸门、收工闸门和扫描主循环。
- 不全局替换 Xalgorix 的主题令牌和公共 UI 组件。
- 不把全局侧栏、Topbar、OpsPulse 纳入第一轮复刻范围。

## 3. 当前事实

### 3.1 会话页

- 入口：`webui/src/pages/task-detail.tsx`，`?tab=sessions&run=`。
- 主体：`webui/src/components/sessions-tab.tsx`。
- 现状：
  - `lg` 起 `14rem + 1fr` 双栏；
  - 左侧仅一个「主循环」Run 分组，标题显示 Run UUID；
  - 右侧头部为通用会话标题 + 运行控制；
  - 正文只挂 `UserConversation`，仅显示当前操作者人工消息；
  - 内联维护一套独立 Composer，未复用对话页 `components/chat/composer.tsx`。
- 结论：当前实现是「Run 选择 + 人工留言板」，不是 4zreco 的会话页。

### 3.2 对话页

- 入口：`webui/src/pages/chat.tsx`，`?run=&view=full|human&archived=1`。
- 组件：
  - `components/chat/conversation-sidebar.tsx`
  - `components/chat/conversation-item.tsx`
  - `components/chat/chat-panel.tsx`
  - `components/chat/transcript.tsx`
  - `components/chat/composer.tsx`
- 现状：
  - 已有标题、置顶、归档、搜索、任务分组、完整转录和附件基础能力；
  - `lg` 起 `20rem + 1fr`，参照为 `md` 起 `18rem + 1fr`；
  - 无选中会话时自动回退第一条，参照应显示 `DraftChat`；
  - 新建对话走弹窗并立即创建 Run，参照在首条消息发送时才创建；
  - 列表缺少筛选、计数、多选、加载更多和分组折叠持久化；
  - 转录使用按钮加载更早，参照为顶部自动加载并恢复滚动锚点；
  - Composer 缺少附件 Chip、@ 引用、发送键位配置和失败恢复；
  - 任务详情会话页与对话页各自维护一套 Composer 和消息样式。

### 3.3 参照页面事实

**4zreco 会话页**

- 页面容器按 `100svh` 预留头部和 padding；`lg` 起 `18rem + 1fr` 双栏。
- 左侧按 `mainagent / planner / system / worker` 四类角色分组。
- 主 Agent 支持新建会话分段；Worker 支持暂停、恢复、删除和发消息。
- 右侧是当前会话完整转录，含工具调用、审批、Token、模型状态和运行时长。
- 支持逐会话历史分页、SSE 增量、未读数、滚动锚点恢复和移动端折叠。

**4zreco 对话页**

- `md` 起 `18rem + minmax(0,1fr)` 双栏；小屏列表在上、详情在下。
- 左侧：新建对话、Agent 筛选、计数、多选、置顶、角色分组、加载更多。
- 右侧无选中时显示 `DraftChat`；选中后显示完整详情。
- 详情包含 Agent/任务/模型信息、完整转录、附件、@ 引用和运行控制。
- 历史分页与实时消息按 `seq` 合并；仅在用户贴底时自动滚底。

## 4. 架构与约束

1. **页面独立**：会话页重写 `sessions-tab.tsx`；对话页重写 `pages/chat.tsx` 与
   `components/chat/*`。两页不共享页面容器。
2. **共享范围**：只抽取纯展示件（转录行、工具卡、Markdown、附件 Chip、状态图标），
   前提是两页的数据已归一为同一套展示类型。
3. **数据层不改语义**：服务端状态继续走 `api/queries.ts`；不引入新全局 store；
   实时数据继续复用 `store/events.ts` 的 SSE。
4. **URL 唯一权威**：
   - 会话页：`?tab=sessions&run=...`；
   - 对话页：`?run=&view=full|human&archived=1`。
5. **令牌边界**：优先在聊天页面容器内做参照样式覆盖；不改 `styles.css` 全局主题。
   如确需字体或圆角差异，登记为页面级例外，避免全站视觉漂移。
6. **构建**：改前端后必须 `make webui`，否则 `go:embed` 仍嵌入旧产物。
7. **无 PG 可用**：会话页和对话页在无 PostgreSQL、无扫描目录时返回空态，不报错。

## 5. 会话页实施方案

### 5.1 页面结构

```text
SessionsPage（任务详情会话内容）
├─ 左栏：会话列表
│  ├─ 头部：折叠开关 / 实时状态 / 任务 Token 汇总
│  └─ 角色分组
│     ├─ 主 Agent 会话（新建入口）
│     ├─ Planner 会话
│     ├─ System 会话
│     └─ Worker 会话
└─ 右栏：当前会话
   ├─ 头部：标题 / 实时 / 模型 / Token / 时长 / 控制
   ├─ 转录：完整 Activity 流，可展开工具与审批
   ├─ Composer：主 Agent 可输入；Worker 按状态可暂停/恢复/发消息
   └─ 空态 / 加载 / 错误
```

### 5.2 改造文件

- 重写 `webui/src/components/sessions-tab.tsx`。
- 保留 `webui/src/pages/task-detail.tsx` 的 sticky 头部、Tab 条和其他 Tab。
- 新增会话页局部组件，建议放在 `webui/src/components/sessions/`：
  - `session-rail.tsx`
  - `session-item.tsx`
  - `session-workspace.tsx`
  - `session-composer.tsx`
- 复用 `components/chat/transcript.tsx` 前，先确认其展示类型可承载会话页 Activity；
  否则抽出 `components/transcript/` 纯展示层，两页分别适配。

### 5.3 视觉与响应式

- 容器高度按任务详情头部实际 chrome 计算，禁止再叠一套 `100svh` 魔法数。
- `lg` 起 `18rem + 1fr`；小屏单列，会话栏默认折叠，展开高度约 `40svh`。
- 卡片圆角、边框、间距、头部行高与 4zreco 会话页一致。
- 消息区内部滚动，Composer 钉底，页面级不滚。

### 5.4 数据适配

当前后端只提供 Run 级转录，没有 4zreco 式的独立 Planner/System/Worker 会话实体。
因此分两档实施：

**A. 纯前端映射**

- Run → 主 Agent 会话。
- `agent_id` / 事件类型 → Worker、System 分组。
- 每个分组复用现有 Run 转录或事件分页。
- Planner 无独立数据时不显示该分组，不伪造。

**B. 完整角色会话（需要后端适配）**

新增只读会话聚合接口，建议契约：

```text
GET /api/tasks/{taskId}/sessions
→ {
    items: [{
      id, role: mainagent|planner|system|worker,
      title, status, live, unread, running,
      instance_id?, intent_id?,
      total_tokens?, last_activity_at?
    }]
  }

GET /api/tasks/{taskId}/sessions/{sessionId}/activities?before_seq=&limit=
→ { items: Activity[], has_more, earliest_seq }
```

写入侧仍走现有 guidance / Worker 控制 / Run 控制接口，不新增第二套执行通道。
是否采用 B，以“角色分组和独立历史是否必须真实可用”为裁决点。

### 5.5 交互清单

- 会话切换写回 `?run=`；无合法 Run 时按现有 `useRunSelection` 规则回退。
- 向上滚动到顶自动加载更早，prepend 后恢复滚动位置。
- 非贴底收到新消息时显示“回到底部”和未读数。
- 主 Agent 发送、运行中停止沿用现有 guidance 与 stop 能力。
- Worker 会话只在真实存在对应数据时提供暂停、恢复和发消息；无数据时隐藏。
- 新建主会话、删除 Worker 等 4zreco 专属能力，未接后端前不显示假按钮。

## 6. 对话页实施方案

### 6.1 页面结构

```text
ChatPage
├─ 左栏
│  ├─ 新建对话
│  ├─ 筛选 / 计数 / 多选
│  ├─ 置顶会话
│  ├─ 分组会话（可折叠）
│  └─ 加载更多
└─ 右栏
   ├─ 无选中：DraftChat
   │  ├─ 空态引导
   │  ├─ Agent/任务选择（仅使用真实数据）
   │  └─ Composer
   └─ 有选中：ChatView
      ├─ 头部：标题 / 状态 / 任务 / Token / 运行控制
      ├─ Transcript
      └─ Composer
```

### 6.2 改造文件

- `webui/src/pages/chat.tsx`
- `webui/src/components/chat/conversation-sidebar.tsx`
- `webui/src/components/chat/conversation-item.tsx`
- `webui/src/components/chat/chat-panel.tsx`
- `webui/src/components/chat/transcript.tsx`
- `webui/src/components/chat/composer.tsx`

### 6.3 骨架与尺寸

- 小屏：列表在上、详情在下；列表高度约 `minmax(10rem, 15rem)`。
- `md` 起：`18rem + minmax(0,1fr)` 单行双栏。
- 页面使用参照的 padding 和视口高度；为聊天路由解除 `max-w-7xl` 限制，
  在 `app-shell.tsx` 做路由级分支，不影响其他页面。
- 保证 `min-h-0 / min-w-0 / overflow-hidden` 高度链完整。

### 6.4 列表与草稿

- 无 `?run=` 时显示 `DraftChat`，不再自动选第一条。
- 首条消息发送流程：
  1. 选择任务；
  2. 用户输入并（可选）上传附件；
  3. 创建 Run；
  4. 发送 guidance；
  5. 写入 `?run=` 并切换到详情。
- 置顶、分组折叠、选择状态、加载更多对齐参照。
- 多选只提供现有能力支持的批量归档，不把归档包装成物理删除。
- 切换筛选、退出多选、取消重命名时清空临时状态。

### 6.5 转录与 Composer

- 打开会话加载最新一页；顶部自动加载更早并恢复滚动位置。
- 历史与实时消息按 `seq` 去重合并。
- 距底部阈值内才自动贴底；否则显示回到底部/新消息提示。
- 七类转录项逐项对齐：human、assistant、tool_use、tool_result、approval、
  finding、system。
- 工具调用折叠显示摘要、状态和耗时；展开显示参数与输出。
- Composer：
  - 自适应高度、附件 Chip、发送键位配置；
  - Enter / Shift+Enter 与 IME composition 安全；
  - 运行中发送键变停止键；
  - 发送失败恢复文本和附件；
  - `@` 引用、`/btw`、模型切换等无对应 API 的能力暂不接入。

## 7. 4zreco 能力与本仓库数据映射

| 4zreco 能力 | 本仓库现有数据 | 处理 |
|---|---|---|
| 会话标题/置顶/归档 | `Run.title/pinned/archived` | 直接使用 |
| 会话列表/任务归属 | `ConversationRow` | 直接使用 |
| 完整转录 | `TranscriptItem` | 直接使用 |
| 附件上传 | `ChatAttachment` + 上传接口 | 使用；历史回显缺口单独补 |
| 运行控制 | `/api/instances/{id}/pause|resume|stop` | 直接使用 |
| 人工消息 | guidance API + SSE | 直接使用 |
| Agent 筛选 | 无独立 Agent 会话实体 | 不伪造；按任务/真实 agent_id 决定 |
| LLM Profile | 无对应会话字段 | 暂不接入 |
| @ 引用 | 无 mentions API | 暂不接入 |
| 旁路提问 | 无 side-questions API | 暂不接入 |
| Todo Popover | 无对应数据 | 暂不接入 |
| Planner/System/Worker 会话 | 仅 Run/agent 事件 | 映射或增加聚合接口 |

## 8. 实施顺序

| 阶段 | 内容 | 页面 | 可回滚点 |
|---|---|---|---|
| P0 | 固定参照截图、视口、主题和状态；记录 computed style | 两页 | 无代码 |
| C1 | 对话页容器、双栏、高度链、DraftChat 路由语义 | 对话页 | 独立提交 |
| C2 | 对话页列表、会话项、筛选/置顶/分组/多选 | 对话页 | 独立提交 |
| C3 | 对话页 Transcript、滚动分页、Composer | 对话页 | 独立提交 |
| S1 | 会话页容器、角色分组、会话项 | 会话页 | 独立提交 |
| S2 | 会话页 Workspace、转录、控制与 Composer | 会话页 | 独立提交 |
| S3 | 按需增加会话聚合数据适配 | 会话页 | 后端独立提交 |
| V1 | 两页断点、状态、长历史、可访问性验收 | 两页 | 验收记录 |

先做对话页，因为其数据契约与现有实现最接近；会话页随后独立重写，避免两套需求互相牵制。

## 9. 验收标准

### 9.1 视觉

- 同一 Chrome、同一视口、同一主题下与 4zreco 截图叠加对照。
- 主要区域位置和尺寸一致；字体、行高、颜色取 computed style 一致。
- 页面级不出现意外滚动条，Composer 始终在视口内。

### 9.2 断点

- 1440×900
- 1024×768
- 768×1024
- 390×844
- 暗色、亮色各一轮

### 9.3 状态

- 空列表、无选中、草稿、首次加载、加载失败、运行中、暂停、终态、归档。
- 长列表、长转录、工具展开、审批卡、附件、超长文本、窄屏。
- SSE 断线重连、历史分页、贴底与非贴底。

### 9.4 工程

- `cd webui && npm run typecheck` 0 错误。
- `npm run build` 成功。
- `make webui` 更新 `internal/web/static` 嵌入产物。
- 若增加后端接口：`go test ./... -race` 通过，并补接口契约与测试。
- 不使用 mock 伪造验收结论；完整 conversation/transcript 联调使用真实后端，
  或先补齐 mock 的对应路由。

## 10. 风险与裁决点

1. **角色会话数据不足**：4zreco 的 Planner/System/Worker 会话依赖其领域模型。
   本仓库不新增聚合接口时，只能显示真实可得的 Run/Agent 分组，不能宣称完整功能一致。
2. **全局外壳差异**：本轮保留 Xalgorix 侧栏、Topbar、OpsPulse。
   若要求整页截图完全一致，需要另立全局壳层改造，影响所有页面。
3. **字体差异**：4zreco 使用本地 Geist 字体，本仓库按名称引用可能回退系统字体。
   为保证字宽一致，需要引入同版本字体文件，并登记为页面级例外。
4. **删除语义**：本仓库以归档保留审计记录，不照搬 4zreco 的物理删除。
   多选操作应明确显示为归档。
5. **两页共享组件**：共享范围仅限纯展示层。若为复用强行统一数据结构，
   会造成第二种领域模型，违背单一数据源原则。

## 11. 完成定义

- 会话页和对话页分别完成 1:1 视觉与交互走查。
- 两页互不依赖对方的页面骨架。
- 所有展示数据均可追溯到本仓库真实 API；无对应数据的能力明确缺失而非伪造。
- 类型检查、构建和嵌入产物更新完成。
- 验收记录附对照截图、视口、主题和已知差异清单。
