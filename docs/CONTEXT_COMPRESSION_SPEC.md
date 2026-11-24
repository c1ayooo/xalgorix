# 上下文压缩规格 — digest 节点与信息空间投影（IG6）

> **状态**：IG6a–IG6d 已落地（2026-11-24）。实现：`internal/agent/context_snapshot.go`（投影与 digest 写入）、`eino_observer.go`（压缩改投影）、`worker_pool.go`（worker 种子）、`main_chat.go`（hint 投影与 `list_hints`）、`domainjournal` + `scanctx`（`OpContextDigest`/`AppendContextDigest`）、`internal/web/scan_session.go`（resume 读取 digest）。
>
> **定位**：补充 `docs/CORE_PHILOSOPHY.md` §3/§4/§5/§6/§8 与 `docs/INFO_AS_GRAPH_SPEC.md` §3.2/§4/§6/§7，把“上下文压缩”从匿名文本摘要改造为信息空间的一次合法投影。
>
> **边界**：不改 Verifier、`report_vulnerability` 门管线、`ConcludeIntent`、claim 谓词、工具面、SSE 单向、扫描隔离；不新增用户可见环境变量（沿用 `XALGORIX_CONTEXT_COMPACT_*` / `XALGORIX_BOUNDED_CONTEXT` / `XALGORIX_TOOL_ARCHIVE_*`）。
>
> **代码基线**：2026-11-24，`internal/agent/agent_messages.go`、`internal/agent/eino_observer.go`、`internal/agent/main_chat.go`、`internal/tools/notes`、`internal/graph`、`internal/infoitem`。
>
> 与 `docs/INFO_GRAPH_CONTRACT.md` 冲突时以该契约为准；本规格不修改 intent 生命周期与确认门。

---

## 0. 结论

1. **压缩不是“对话摘要算法”，而是信息空间的一次投影。** 产物有法定身份：`graph.KindDigest` 节点（`internal/graph/types.go:48`），状态机 `active → superseded`（`types.go:381`），`DefaultState=active`（`types.go:337`）。IG6 之前该节点**没有任何写入者**；现在每次压缩恰好产出一个 `active` 节点并把同区域旧节点置 `superseded`。
2. **现行实现是旁路。** `einoObserver.CompactMessages`（`internal/agent/eino_observer.go:520`）把老消息压成一条匿名 `role=user` 消息：无 ID、无坐标、无 `supports`、不入图、不入 InfoItem、重启即失。按 CORE §4，这正是“凭上下文记忆复述证据”的定义。
3. **目标**：热路径从内存权威 store **确定性渲染**“战况快照”；变更边界把产物落成 digest 节点 + InfoItem（`inferred` + `supports`）；消费者按 INFO_AS §4.4 契约点名（预算/排序/截断/缺口）。
4. **主会话裁剪同源问题**：`main_chat.go:292` 裁到 24 条时直接丢弃 `[USER]` 消息。按 CORE §2，人工输入 = `source=human` 的 InfoItem；正确做法是裁剪前把操作者消息投影为 `hint`（observed），丢的是聊天记录，不是人工输入。

---

## 1. 代码事实与缺口

### 1.1 现有机制（三层 + 主会话）

| 层 | 机制 | 事实位置 |
|---|---|---|
| L1 单结果截断 | 工具输出保留 12KB 头 + 3KB 尾，中间提示重取 | `agent_messages.go:17` |
| L2 归档 + 老化 | ≥`ToolArchiveMinBytes` 的原始输出写 `tool-outputs/`；最近 `ToolArchiveActiveWindow`（默认 8）条工具结果保持原文，更老的换 300B stub；`read_tool_output` 可取回 | `agent_messages.go:520-665`、`eino_observer.go:236,520-585` |
| L3 阈值压缩 | 达到窗口 `ContextCompactRatio`（默认 0.75）或 100 条消息时保留最近 40 条，老消息压成 digest 文本 + notes 全量回注 | `eino_observer.go:520-585`、`agent_messages.go:380` |
| L4 主会话 | 用户 chat 按条数裁到 24 条，无投影、无持久化 | `main_chat.go:292,1247` |

`internal/agent/agent_messages.go` 中 `shouldPruneBeforeLLM` / `pruneMessages` / `forcePruneMessages` / `ageOutToolOutputs` 已无生产调用者（仅测试引用）；生产路径只有 `einoObserver.CompactMessages`（`eino_runtime.go:74`）。

### 1.2 缺口（按严重度）

1. **digest 在默认路径上基本失效**：`compactMessages` 只认 `role=="user"/"assistant"` 与 XML `<function=` 文本；eino 路径的工具消息 `role="tool"`，native tool call 在 `llm.Message.ToolCalls`（`llm/client.go:40`）。因此 “Tools Executed / Key Findings / Errors” 三节在生产路径上几乎恒为空，只剩 URL mentions + notes。
2. **递归摘要**：digest 本身作为一条 user 消息留在窗口，下一轮再被 digest；被丢弃的老消息没有归档（只有 ≥1.5KB 的工具输出有 archive），信息二次丢失。
3. **计量不准**：触发用固定 4 bytes/token；中文约 3 B/字符（1 token/汉字），CJK 重的扫描会迟触发。每次响应的 `usage.PromptTokens` 精确值仅用于统计（`agent.go:3030`），未参与触发。
4. **notes 无总量预算**：`formatStore` 只截单值 500 字节，条数不限，且每次压缩全量回注；端点库存类 note 会让压缩后立刻再超阈值。
5. **非确定性**：digest 遍历 `map` 输出顺序随机，同一状态两次压缩结果不同，破坏 prompt 前缀缓存与 golden 测试。
6. **无出处、无坐标、不可审计**：digest 没有 `supports`，却作为历史注入，可被模型当作“已测/已确认”的依据；违反 CORE §3.2 不变式 3 与 §5 字段级权威。
7. **人工输入被当聊天记录丢弃**：主会话裁剪不投影 hint，违反 CORE §2 的对象映射。

---

## 2. 与 CORE / INFO_AS 的判据对应

| 文档条款 | 现状问题 | 本规格的解决 |
|---|---|---|
| CORE §4 旁路定义 | 从对话文本关键词猜证据 | 热路径改从 ledger/coverage/findings/InfoItem 确定性投影；直查确定性 API 属合法实现 |
| CORE §3.2 不变式 3 | digest 无 `supports` 却被当历史 | 每行携带可解析 ID；digest InfoItem `nature=inferred` + `supports` 非空 |
| CORE §5 字段级权威 | 关键词猜 finding，可能把未过报告门的候选写成既成事实 | 只渲染报告门接受的 finding 与验证标签；候选区标注 UNVERIFIED |
| CORE §6 / §7 状态权威 | digest 只在内存 | digest 节点 + InfoItem 在变更边界落 PG（`(scan_id, seq)` 幂等，best-effort + 水位） |
| CORE §8 无孤儿事实 | digest 无出处 | `derived_from` 边 + `graph_info_links` + InfoItem `supports` |
| CORE §8 River 范围 | 若引入 LLM 摘要会进热循环 | 只允许 River 冷路径产出 inferred digest InfoItem，不进热路径 |
| INFO_AS §1.2 不变式 6 | map 遍历随机 | 所有渲染固定顺序、排序键 |
| INFO_AS §3.2 坐标表 | digest 无坐标 | `kind=digest` / `subject=区域` / `purpose=summary` / `nature=inferred` |
| INFO_AS §4.4 查询契约 | 压缩产物无消费者契约 | 逐消费者列明 limit、排序、fallback、`Incomplete` |
| INFO_AS §9 热路径纪律 | 不得为点亮把热路径全走 PG | 热路径只用 `scanctx` 内存确定性 API；PG 只做持久化与跨会话读取 |

---

## 3. 对象模型

### 3.1 digest 节点

```text
kind     = digest
state    = active | superseded          // 状态机已存在
origin   = engine
priority = 0（不参与调度）
subject  = {kind: run|lane|intent|window, identifier: ...}
purpose  = summary
nature   = inferred
```

`payload` 只存渲染结果与引用，不复制 ledger/InfoItem 正文：

```json
{
  "digest_id": "dg_000007",
  "region": {"kind": "lane", "identifier": "agent:worker-3", "run_id": "run-..."},
  "window": {"from_seq": 120, "to_seq": 168, "archived_ids": ["to_000042"]},
  "snapshot": "...固定顺序的渲染文本...",
  "refs": ["H-12","EV-3","XALG-0042","to_000042"],
  "sections": {"hints": 3, "beliefs_open": 7, "beliefs_dead": 4, "findings": 2},
  "bytes_before": 412000,
  "bytes_after": 21000,
  "prompt_tokens": 91344,
  "compaction_seq": 7,
  "created_at": "..."
}
```

**区域（subject）口径**：`run:<run_id>`（整轮扫描）、`lane:agent:<id>`（worker 主循环）、`intent:<unit_key>`（单意图）、`window:<from>-<to>`（按 seq 的消息区间）。同一区域的新 digest 把旧的 `Transition → superseded`，不删除历史。

### 3.2 边与链接

- `derived_from` → 上一条 digest（同区域）与本次被摘要的 belief/fact（若适用）；
- `supports` → 快照断言的证据（仅当 digest 明确断言事实时；通常只用 `derived_from`）；
- `graph_info_links(role='materializes')` → 对应 InfoItem。

### 3.3 InfoItem

- `nature=inferred`，`purpose=summary`，`supports` = 快照引用的全部 ID（非空）；
- `source=engine:context-compactor`（引擎确定性来源，不是模型自述）；
- `context={run_id, lane, phase}`，`coordinate_status` 如实标注缺失坐标；
- 通过 domain journal → River 冷路径写入；失败记 `pending/failed` 水位，不阻塞下一轮装配。

### 3.4 不复制正文

`snapshot` 只包含 ID + 一行渲染，正文仍在 ledger/InfoItem/tool-outputs。消费者需要全文时用 `read_ledger` / `read_info_item` / `read_tool_output` / `search_info`。

---

## 4. 触发与计量

### 4.1 主触发：真实 prompt tokens

每次成功响应把 `usage.PromptTokens` 写入 `Agent.lastPromptTokens`（原子值）。下一次装配前：

```text
ContextCompactTokens > 0 :  tokenTrigger = lastPromptTokens >= ContextCompactTokens
否则（AUTO，<0）         :  tokenTrigger = lastPromptTokens >= LLMContextWindow × ContextCompactRatio
```

- 显式 `XALGORIX_CONTEXT_COMPACT_TOKENS>0` 是**绝对 token 预算**，优先于窗口比例；AUTO 模式默认 `ratio=0.75`（`XALGORIX_CONTEXT_COMPACT_RATIO`）；
- `XALGORIX_CONTEXT_COMPACT_TOKENS=0` 仍表示禁用自动压缩（`compactThresholdBytes()==0` 直接返回）；
- 压缩后窗口收缩到约 25–30%，下一次装配自然不再触发；无需额外冷却。

### 4.2 兜底：字节估计（修正 CJK）

仅在 `lastPromptTokens==0`（首轮、provider 不返回 usage）时使用；有 usage 时以 provider 为准，不让估计覆盖真实值：

```text
estTokens = cjkRuneCount + ceil(nonCJKBytes / 4)
```

比较口径与 §4.1 相同（显式预算优先，否则窗口比例）。原始 `4 bytes/token` 字节上限仍保留为最后一道硬边界（`compactThresholdBytes`），但不再是唯一的提前量。

`cjkRuneCount` 统计 CJK 统一表意文字、全角标点、假名、谚文等；其余按 4 B/token。仍以现有 `compactThresholdBytes` 为上限表达。

### 4.3 硬边界

沿用 `maxMessagesBeforePrune=100`，与 token/byte 触发取“或”。`ContextCompactTokens=0` 时全部自动压缩关闭，仅保留协议重置与硬溢出兜底。

---

## 5. 快照渲染规格（IG6c）

### 5.1 固定顺序与预算

总预算默认取 `LLMContextWindow × 25%`（无窗口配置时 24KB，硬上限 128KB），每节硬上限如下；超预算时按“动作 → 最近观测 → 覆盖”的顺序降级，**已死方向与已确认发现不得先丢**。热路径只读内存权威（ledger/coverage/notes/报告门 store），graph 的 hint/intent 每次压缩读一次，不逐轮读 PG。

| # | 段落 | 权威来源 | 上限 | 每行格式 |
|---|---|---|---|---|
| 1 | RUN/LANE 头 | `scanctx` / graph | 6 行 | run、lane、agent、iteration、compaction |
| 2 | 人工输入 | `hint` 节点（active，origin=human） | 8 | `hint ID: 文本[:200]` |
| 3 | 计划意图 | `intent` 节点（open/running/paused） | 12 | `ID 状态 目标 类别` |
| 4 | 待证信念 | ledger（queued/testing/blocked） | 15 | `ID 类别 endpoint 置信度 next_action` |
| 5 | **已死方向** | ledger（rejected/exhausted） | 15 | `ID 类别 endpoint attempts`（必保） |
| 6 | 已确认发现 | 报告门 accepted + 验证标签 | 12 | `XALG ID 严重度 endpoint 标签` |
| 7 | 最近观测 | ledger evidence（`nature=observed`，按时间倒序） | 10 | `evidence ID 类别 hyp ID 一行摘要` |
| 8 | 覆盖缺口 | `CoverageGaps(ScanState)`（引擎覆盖矩阵） | 10 | `类别 @ endpoint` |
| 9 | 笔记 | `notes.FormatForContextBudgetID` | 6KB | 排序后的 key: value |
| 10 | 最近动作 | 最近 K 条工具结果（老化窗口） | 10 | `工具名/结果首行 [archive_id]` |
| 11 | 恢复指引 | 固定文本 | 4 行 | 可用 `read_*` 工具 + 禁止凭记忆复述 |

> 注：第 7 段用 ledger 的 observed evidence 作为热路径观测视图；PG `info_items` 是冷路径原子层，由 resume/检索消费者读取（`search_info`/`read_info_item`）。这与 INFO_AS §9 的“热路径不得全走 PG”一致。

### 5.2 权威标注

每行必须标注来源等级，防止 digest 越权：

- `[gate]` 报告门接受的 finding / 验证标签；
- `[ledger]` 假设与证据；
- `[graph]` intent/hint/goal 生命周期；
- `[info]` InfoItem 观测；
- `[engine]` 覆盖、缺口、预算等引擎计算；
- `[digest]` 本快照自身的推断（不得作为收工依据）。

### 5.3 确定性

- 固定段落顺序；每段内按 `id ASC` 或 `observed_at DESC, id ASC`；
- 禁止遍历 map 直接输出；所有 set/map 先排序；
- 不含 wall-clock 相对时间（用记录里的绝对时间）；
- golden 测试：同一快照输入两次渲染字节一致。

### 5.4 与老消息的关系

只允许“最近窗口外的原始 transcript”老化；快照每轮从权威 store 重算，**永不被再次摘要**。新 digest 用 `derived_from` 指向上一条 digest + 本次老化区间，血缘显式，避免 ACE 所述 context collapse。

---

## 6. 消费者与契约（CORE §4 / INFO_AS §4.4）

| 消费者 | 数据来源 | 预算 | 排序 | 失败/缺口语义 |
|---|---|---|---|---|
| 下一轮消息装配（`einoObserver.CompactMessages`） | `scanctx` 内存权威（ledger/coverage/notes/findings）+ graph hint/intent（每次压缩一次） | 25% 窗口，硬上限 128KB | 固定段序 + 段内确定性排序 | graph 读失败 → 跳过该节并记日志；绝不用 PG 失败阻塞模型调用 |
| worker 起手种子（`DispatchWorkerTaskWithSurface`） | 同一投影（内存权威 + graph） | 8KB | 同上 | 无状态 → 不注入；不伪造快照 |
| resume briefing（重启续跑） | PG `kind=digest`（active）payload 的 snapshot | 12KB | `created_at DESC`，最多到预算 | 无 active digest → 不注入；图不可用 → 退回原 briefing |
| 报告/审计视图 | `/api/scans/{id}/graph` 的 digest 节点（active/superseded 链） | 只读分页 | `id ASC` | 必须能回答“某条证据在 seq=N 被裁出上下文，进了哪个 digest”（payload `refs` + `compaction_seq`） |

新增消费者必须先在本表登记预算与排序，否则不得读取 digest。

---

## 7. 主会话裁剪 = hint 投影

### 7.1 规则

- `trimMainChatHistory` 裁掉 `history[1:len-(max-1)]`；
- 对其中 `role=="user"` 且含 `[USER] ` 标记的消息，提取标记后的原文，投影为 `hint` 节点：
  - `kind=hint`，`state=active`，`origin=human`，`priority=50`；
  - `payload={guidance_id, message, source:"human", captured_from:"main_chat_trim"}`；
  - `unit_key = "hint-chat-" + sha256(text)[:16]`（幂等，重复裁剪不产生重复节点）。
- 跳过：显式确认（`mainChatExplicitConfirm`）、`[SYSTEM]` 消息、本规格产生的 `[CONTEXT]` 提示、replay transcript。

### 7.2 失败姿态

- 投影失败（graph 后端不可用）时**不静默丢弃**：本轮不裁剪，下一轮重试；
- 若 `len(history) > max+8`（持续失败），才强制裁剪，并发出显式 `error` 事件报告被丢弃的条数；
- 任何情况下不得把人工输入记录成 `inferred`。

### 7.3 生命周期

hint 的 `consumed` 生命周期暂未启用：hint 一旦被某个 worker 或 planner 读取就标记 consumed，会让后续 worker 丢失同一条指引，且无法判断“真正执行过”。当前 hint 保持 `active` 直到扫描结束；`consumed` 语义列入 §12 开放问题。`list_hints` 已支持按 `state` 过滤，为后续启用留好读路径。

---

## 8. 迁移里程碑

### IG6a — 修 bug + 确定性 + 真实计量（已落地）

- `compactMessages` 识别 `role=="tool"` 与 `llm.Message.ToolCalls`；XML 分支保留；
- 工具/URL/发现输出全部排序去随机；
- “Key Findings” 改为 “Candidate Finding Mentions (UNVERIFIED — report gate decides acceptance)”；
- 主触发改用 `usage.PromptTokens`，字节法兜底并修正 CJK；
- notes 注入加总预算（排序 + 截断 + omitted 计数）；
- 压缩日志带 before/after bytes、prompt tokens、是否 token 触发、notes 预算。

**验收**：`compactMessages` golden 稳定；native tool call 与 tool 结果能进入 digest；token 超线但字节未超线时仍压缩；notes 注入不超过预算。

### IG6b — digest 节点落地 — 已落地（2026-11-24）

- 新增 `digest` 节点写入（`active`）+ 同区域旧节点 `Transition → superseded`（`persistContextDigest`）；
- `derived_from` 连上一条 digest，并连本次引用到的 hint/intent 节点；
- InfoItem（`inferred` + `supports`）经 `scanctx.AppendContextDigest` → domain journal（`OpContextDigest`）→ 既有 journal/River→PG 冷路径写入；append 失败只记日志，不阻塞装配。`supports` 为空时不写 InfoItem（无出处的 inferred 不得发布）；
- `/api/scans/{id}/graph` 的 kind 过滤已包含 `digest`，digest 链（active → superseded）可见。

**验收**：`TestPersistContextDigestChainAndInfoItem`（节点链、`derived_from`、journal 记录为 inferred + 非空 supports + `purpose=summary`）、`TestContextDigestRequiresSupports`。

### IG6c — 热路径改投影 — 已落地（2026-11-24）

- `einoObserver.CompactMessages` 用 `buildContextSnapshot` 替换文本 digest；无权威状态可用（CLI/单测接线）时回退到 `compactMessages` + notes，保证窗口不会被替换成空内容；
- 段序：hints → intents → open/dead beliefs → gated findings → observed evidence → coverage gaps → notes → aged-window actions → recovery；每段硬上限、总预算 25% 窗口（上限 128 KiB）、worker 种子 8 KiB；
- 每行带 `[graph]/[ledger]/[gate]/[info]/[engine]/[digest]` 权威标注；finding 只来自报告门；rejected/exhausted 段必保；
- 快照每轮从权威 store 重算，新 digest 以 `derived_from` 连旧 digest，不做“摘要的摘要”。

**验收**：`TestContextSnapshotRendersAuthoritativeSections`、`TestContextSnapshotDeterministic`、`TestCompactMessagesProjectsSnapshotAndDigestNode`。

### IG6d — 冷路径与消费面 — 已落地（2026-11-24）

- 冷路径：digest InfoItem 走既有 domain journal → River → PG（不新增队列、不接管扫描循环）；
- worker 起手种子：`DispatchWorkerTaskWithSurface` 注入 8 KiB 区域快照（hints/plan/open+dead beliefs/findings/coverage/notes），worker 不再从零重踩死路；
- resume：`formatResumeBriefing` 读取最新 active digest（每 run 最多 12 KiB），notes 改用预算版；
- 主会话 `list_hints` 只读工具；hint 的 `consumed` 生命周期仍未启用（hint 保持 active，避免一个 worker 消费后其他 worker 丢失指引），列为 §12 开放问题。

**验收**：`TestWorkerSeedIsBoundedAndCarriesDeadEnds`、`TestMainChatListHints`、`TestLatestDigestBriefingReadsActiveDigest`、`TestLatestDigestBriefingIsBounded`。

---

## 9. 机器可验收清单

1. `TestCompactMessagesNativeToolCalls`：assistant `ToolCalls` + `role=tool` 结果都进入 digest；
2. `TestCompactMessagesDeterministic`：同一输入两次输出字节一致；
3. `TestCompactUsesPromptTokens`：字节远低于阈值但 `PromptTokens` 超线 → 压缩；
4. `TestCompactUsesExplicitTokenBudget`：`ContextCompactTokens>0` 时按绝对预算触发，不被大窗口比例吞掉；
5. `TestCompactFallsBackToCJKEstimator`：无 usage 时 CJK 估计在字节上限之前触发；
6. `TestCompactBelowTokenThreshold`：不压缩；
7. `TestNotesBudget`：超量 notes 输出 ≤ 预算 + 截断标记，键有序；
8. `TestMainChatTrimPersistsOperatorHints`：被裁用户消息 → `hint` 节点（origin=human），确认语不落 hint；
9. `TestMainChatTrimKeepsHistoryWhenHintWriteFails`：后写失败时本轮不裁剪；
10. `TestContextSnapshotRendersAuthoritativeSections`：hints/intents/open+dead beliefs/gated findings/observed evidence/coverage/actions 全部进入快照，且带权威标注；
11. `TestContextSnapshotDeterministic`：同一状态两次渲染字节与 refs 完全一致；
12. `TestPersistContextDigestChainAndInfoItem`：压缩写 digest 节点（active）、旧节点 superseded、`derived_from` 边、journal 记录为 inferred + 非空 supports + `purpose=summary`；
13. `TestContextDigestRequiresSupports`：无 supports 的 inferred 不得进入 journal；
14. `TestCompactMessagesProjectsSnapshotAndDigestNode`：生产压缩路径使用快照（不是 `## Compacted History`）并落一个 active digest；
15. `TestWorkerSeedIsBoundedAndCarriesDeadEnds`：worker 种子 ≤8KB 且含已死方向；
16. `TestMainChatListHints`：主会话可读 hint（含 state 过滤）；
17. `TestLatestDigestBriefingReadsActiveDigest` / `TestLatestDigestBriefingIsBounded`：resume 读 active digest，superseded 不泄漏，≤12KB；
18. 基准回归：同目标 prompt tokens 下降，findings/评分不降（`docs/BENCHMARK_OPTIMIZATION.md` 口径）。

---

## 10. 非目标

- 热路径 LLM 摘要（不可复现、成本与编造风险）；
- 新增用户可见环境变量或设置页开关；
- pgvector / 相似度检索；
- 修改 Verifier、报告门顺序、`ConcludeIntent`、claim 谓词、工具面；
- 以事件投影作为状态权威；
- digest 复制 ledger/InfoItem 正文。

---

## 11. 风险与对策

| 风险 | 对策 |
|---|---|
| 快照自身变成新的长上下文 | 每段硬上限 + 总预算；超预算先丢动作/最近观测 |
| 内存源与 PG 不一致 | 热路径只信内存权威；PG 只是持久化与跨会话读，读时按水位标 `Incomplete` |
| 压缩后立刻再触发 | token 触发随响应更新；窗口保留 25–30%；notes 有预算 |
| digest 被当作“已确认” | 每行权威标注；`[digest]` 不得作为收工依据；`[gate]` 只来自报告门 |
| hint 无界增长 | 主会话裁剪按内容哈希幂等投影，重复消息不产生新节点；`list_hints` 有 limit；`consumed` 生命周期见 §12 |
| 与 `INFO_GRAPH_CONTRACT` 冲突 | digest 不参与 claim/调度；不改 intent 状态机 |

---

## 12. 开放问题

1. digest 区域粒度：默认 `lane:agent:<id>`，是否需要整轮 `run:` 级汇总？
2. 快照总预算是否固定 25%，还是随 `ContextCompactRatio` 联动（`window × (1-ratio)`）？
3. worker 起手种子是否必须包含“已死方向”全部条目，还是按 intent 坐标过滤？
4. hint 消费语义：当前决定保持 `active` 到扫描结束（避免一个 worker 消费后其他 worker 丢失指引）；未来若引入 `consumed`，必须按“扫描结束”或“操作者显式确认”而不是“被读过”触发。
5. 报告/审计视图：当前复用 `/api/scans/{id}/graph`（`kind=digest`，active/superseded 链）+ resume briefing；是否需要独立 `/api/scans/{id}/digests` 取决于面板查询量，暂不新增。
