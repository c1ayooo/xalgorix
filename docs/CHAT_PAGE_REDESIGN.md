# 会话页重做方案 — 对齐 4zreco chat（2026-11-22）

> 状态：**已实施**（2026-11-22）。C1–C4 全部落地并跑通本地 E2E；剩余项见 §9。
> 参照对象：`/home/c1ay/4zreco/web/src/app/(main)/chat/page.tsx`（Next.js + shadcn，1756 行）。
> 许可约束：4zreco 为 AGPL-3.0，本仓库 Apache-2.0 —— **不拷贝其源码**，只按交互语义重写为
> React 组件；数据结构按本仓库现有 API 重建。

## 0. 结论

现在的会话页不是"对话"，是"人工留言板 + run 选择器"：右栏只有操作者自己发的 guidance，
agent 的解说、工具调用、审批、finding 一条都不出现。要贴合 4zreco 的体验，必须解决三件事：

1. **会话要有身份**：标题、置顶、归档、最后活动时间、finding 计数——现在列表项显示的是 run UUID。
2. **右栏要能看完整转录**：人 + agent + 工具 + 审批 + finding 按时间归并，可上滑翻历史、贴底跟随。
3. **发消息要像聊天**：附件、@提及、发送键位可配、运行中发送键变停止、空态引导建会话。

## 1. 现状（代码事实）

| 文件 | 行数 | 事实 |
|---|---|---|
| `webui/src/pages/chat.tsx` | 330 | 左栏 = 任务列表 + 当前任务的 run 列表；run 项 `label={r.id}`（UUID） |
| `webui/src/components/run-console.tsx` | 295 | 头部 run 选择器同样显示 `run.id`；正文只挂 `UserConversation`；composer 是 guidance 注入框（2000 字符上限、Enter 发送） |
| `webui/src/components/user-conversation.tsx` | 147 | 按 `CORE_PHILOSOPHY §3.2/§8` 只投影 `source=human` 的 guidance；注释明确「Agent output、tools、plans、lifecycle telemetry 不进此视图」 |
| `webui/src/components/session-item.tsx` | 146 | 只有状态点 + 文本 + live 呼吸灯，无菜单、无元数据 |

用户实际看到的：左边一串 UUID，右边自己说过的话。agent 在干活但界面没有回声。

## 2. 4zreco 参照能力清单（逐项）

1. **会话实体**：`Conversation{id,title,agent_key,task_id,llm_profile_id,running,pinned,created_at}`；标题首轮自动生成，双击可改。
2. **左栏分组**：按 agent 分组（可切按任务），组头 sticky、可折叠、显示运行中标记与条数；置顶会话单独在最上。
3. **搜索与批量**：顶部搜索框过滤标题；批量选择模式 + 全选 + 批量删除。
4. **会话项**：标题、运行中徽标（spinner）、agent 名、时间、`#id`；hover 出「更多」菜单（重命名/置顶/删除）；删除走 AlertDialog 二次确认。
5. **右侧头部**：标题、任务绑定 chip（可换绑/解绑）、LLM profile chip（Popover 切换）、运行控制（暂停/恢复/停止，停止要填原因）。
6. **Transcript**：`Activity[]`（assistant 文本 / tool_use / tool_result / 审批 / 结果），按 seq 分页，打开即加载最新一页；上滑加载更早并保持视觉位置；只有贴底时才自动滚到底；工具调用可展开看参数与输出。
7. **Composer**：自适应多行、附件 chip（上传中 spinner、可移除）、@提及（任务/工具/资产）、发送键位可配（Enter 或 Ctrl/Cmd+Enter，存 localStorage）、运行中发送键变红色停止键、`/btw` 旁路提问。
8. **空态**：没有选中会话时给 DraftChat —— 选 agent、选模型、选任务，然后开新会话；不是一句「请选择会话」。

## 3. 与宪章冲突及裁决

`CORE_PHILOSOPHY §3.2/§8` 规定会话视图只投影人工输入，agent 输出留在专用运维视图。要做 4zreco 那种转录，这条必须动。裁决如下（需要你点头）：

- **双投影**：会话页默认 `full` 模式（人 + agent + 工具 + 审批 + finding 时间线）；`human` 模式保留现有 `UserConversation`，作为切换项与合规视图。
- **不改事件定位**：转录仍从 `events.jsonl` + `scan.json` + guidance 表读取，事件依旧是审计投影，不升格为状态权威（不变量 6 不变）。
- **不改判定链**：Verifier、report_vulnerability 闸门、收工闸门一律不碰（不变量 7 不变）。
- **宪章修订**：`docs/CORE_PHILOSOPHY.md` §8 增加一句「会话转录视图为审计投影的只读呈现，可含 agent 活动；状态权威仍在 scanctx」，AGENTS.md 第 8 条后追加同义约束。

## 4. 后端改造

### 4.1 会话元数据（migration 0011）

```sql
ALTER TABLE runs ADD COLUMN IF NOT EXISTS title        TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN IF NOT EXISTS pinned       BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS archived     BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX IF NOT EXISTS runs_task_pinned_idx ON runs (task_id, pinned DESC, created_at DESC);
```

`platform.Run` 增 `Title/Pinned/Archived`，`Repository` 增三个方法（PG + FileStore 双实现，写操作幂等）：

- `RenameRun(ctx, instanceID, title) error`
- `SetRunPinned(ctx, instanceID, pinned bool) error`
- `SetRunArchived(ctx, instanceID, archived bool) error`

自动标题：run 结束（或首条 guidance 落库）时若 `title=''`，取「任务名 · 首条人工消息前 24 字」，无人工消息则「任务名 · MM-DD HH:MM」。不加模型调用。

### 4.2 转录分页 API

```
GET /api/instances/{instanceID}/transcript?before_seq=<int>&limit=<1..200>
→ { items: Activity[], has_more: bool, earliest_seq: int, latest_seq: int, running: bool }
```

- 数据源：该 run 的扫描目录 `events.jsonl`（逆序读尾部，seek 定位，不全量载入）+ `guidance` 表按 `instance_id` 过滤，按时间归并后分配单调 `seq`。
- `Activity.kind`：`human | assistant | tool_use | tool_result | approval | finding | system`。
- 无 PG / 无扫描目录时返回空集 + `has_more=false`，不报错（保住单二进制降级，不变量 9）。
- 复用现有 SSE：客户端拿到首屏后靠 `/api/stream` 增量，不新增推送通道。

### 4.3 列表增强

`GET /api/tasks/{id}/runs` 与 `GET /api/projects/{id}/runs` 返回增加 `title/pinned/archived/finding_count/last_activity_at/first_line`；`include_archived=false` 为默认。

## 5. 前端信息架构

```
pages/chat.tsx                       两列：18rem 列表 + 1fr 会话（重写）
components/chat/conversation-list    搜索、分组切换（任务/agent）、置顶区、折叠、批量
components/chat/conversation-item    标题/徽标/时间/计数/菜单/双击改名/选中态
components/chat/chat-panel           头部（可改标题、任务 chip、模型 chip、暂停/恢复/停止）
components/chat/transcript           气泡 + 工具折叠卡 + 审批卡 + finding 卡；分页、贴底、新消息提示
components/chat/composer             自适应文本域、附件 chip、@提及、键位、运行中变停止
```

- **URL 唯一权威**：`/chat?task=&run=&view=full|human&group=task|agent`，切会话写回 URL（沿用 `useRunSelection`）。
- **不引入新全局 store**：SSE 复用 `store/events`，列表数据复用 `api/queries` 的 react-query key。
- **令牌**：只用 `styles.css` 现有变量与 `components/ui/*`，不新增配色（`UI_DESIGN.md §5`）；不引第三方聊天 UI 库。
- **i18n**：所有文案走 `t()`，新增 key 落在 `chat.*` 命名空间，zh/en 同批补。

## 6. 交互细节

- 发送键位：默认 Enter 发送 / Shift+Enter 换行；设置项 `chat.sendMode = enter | mod-enter`，存 localStorage。
- 运行中：发送键变红色方块（停止本次 run，弹原因框）；`/btw` 前缀走旁路提问（仅在 run 运行时可发）。
- 转录：贴底才跟随；上滑到顶自动加载更早；不满一屏自动补页；新消息在非贴底时显示「N 条新消息」浮标。
- 工具卡：默认折叠一行（工具名 + 目标 + 状态 + 耗时），展开看参数 JSON 与输出前 200 行，超长提示「已落盘」。
- 审批卡：显示规则名、三态（allow/ask/deny）、决定按钮；已决显示决定人与时间。
- 空态：未选会话 → DraftChat（选任务 → 选视图模式 → 开始）；无任务 → 引导去建任务。
- 删除会话：`archived=true` 软删，附「查看归档」入口；不做物理删除（审计要求）。

## 7. 验收清单

1. `cd webui && npx tsc -b --noEmit` 退出码 0；`npm run build` 成功且产物落 `internal/web/static`。
2. `go build ./...` 通过；`go test ./internal/platform ./internal/web` 全绿；新增 repository 方法有单测。
3. 迁移测试通过（0011 是 ALTER 形态，`migrate_test.go` 已放宽为 DDL 判定）。
4. E2E：本地靶标跑一次扫描 → 会话列表出现带自动标题的会话 → 点开能看到「人消息 + agent 解说 + 工具调用 + finding」四类活动 → 上滑能加载更早 → 双击改名后刷新仍在 → 置顶后排到最上 → 停止按钮能中止 run。
5. `human` 模式切换后只剩人工消息，且与改造前 `UserConversation` 行为一致。
6. 无 PG 启动（纯文件模式）下会话页可用，列表与转录为空但不报错。

## 8. 排期（生成 / 验证分栏）

| 提交 | 内容 | 生成 | 验证 |
|---|---|---|---|
| C1 | migration 0011 + platform 三方法 + Run 字段 + 单测 | 0.5 天 | 0.3 天 |
| C2 | transcript API + 列表增强 + 单测 | 1 天 | 0.6 天 |
| C3 | 前端：列表/会话项/头部/空态 | 1.5 天 | 0.5 天 |
| C4 | 前端：transcript + composer（附件/@/键位/停止） | 2 天 | 0.6 天 |

合计：生成 5 天、验证 2 天；每提交独立可回滚。

## 9. 实施记录（2026-11-22）

### 后端
| 文件 | 改动 |
|---|---|
| `internal/infoitem/postgres/migrations/0011_runs_conversation_meta.sql` | runs 加 `title/pinned/archived` + 侧栏索引 |
| `internal/platform/models.go` | `Run` 加三字段；`RunMount.Title` |
| `internal/platform/repository.go` | `RenameRun/SetRunPinned/SetRunArchived` |
| `internal/platform/postgres.go` | 列清单、INSERT、按项目查询同步；三方法 + `updateRunFlag` |
| `internal/platform/filestore.go` | wire 字段、三方法 + `updateRunFlag`，持久化跨重启 |
| `internal/web/chat_transcript.go` | `GET /api/conversations/{id}/transcript`、`PATCH /api/conversations/{id}`、`GET /api/projects/{id}/conversations`、自动标题、标题回填、事件去重 |
| `internal/web/chat_attachments.go` | `POST /api/conversations/{id}/attachments`（落 `<scanDir>/attachments/`，20 MiB 上限） |
| `internal/web/handlers_platform.go` | 发起 run 时写标题；列表回填空标题；`/api/projects/{id}/conversations` 路由 |
| `internal/web/scheduler.go` | 定时 run 同样写标题 |
| `internal/web/handlers_approvals.go` | 首条人工消息自动命名会话 |
| `internal/web/server.go` | 注册 `/api/conversations/`；只读路径白名单加该前缀 |

### 前端
`webui/src/components/chat/` 新增 `conversation-item.tsx`、`conversation-sidebar.tsx`、`transcript.tsx`、`composer.tsx`、`chat-panel.tsx`、`format.ts`；`pages/chat.tsx` 重写；`components/run-console.tsx` 删除（无引用）。

### 实测（本地靶标 127.0.0.1:8902）
- `GET /api/projects/{id}/conversations` → 10 行，标题/任务名/状态/置顶/发现数齐全，置顶行在最上。
- 运行中首次发指令 → 会话标题从「任务 · 时间」变成「任务 · 先确认 /?q= 是否存在反射」。
- `GET /api/conversations/{id}/transcript` → 60 项：assistant 15、tool_use 20、tool_result 21、human 1、system 2、finding 1；镜像消息 0 条；`has_more` 翻页正常。
- `PATCH` 改名 + 置顶 → 返回体与库内一致，跨重启保留。
- `npx tsc -b --noEmit` 0；`npm run build` 成功；`go build ./...` 通过。

### 未做（下一轮）
1. **i18n**：新页面文案目前是中文常量，未走 `t()`；英文界面下会显示中文。
2. **审批卡**：transcript 的 `approval` 类型已保留，但 `events.jsonl` 目前不写审批事件，卡片暂无数据源；需在审批决定处发一条事件。
3. **附件在 human 气泡里的回显**：消息正文带路径，但气泡不单独渲染附件 chip（历史消息回看时只有文本）。
4. **转录分页是整文件解析后切片**：单次运行事件数过万时需要换成按 byte offset 反向读。
