---
name: api-recon-playbook
description: "收集网站API接口时调用该skill。（方法篇）"
tags: [cwe-200, t1595-002, recon, api-surface]
intent: offensive
---
> 执行路线见 `read_skill("api-recon-phases")`；扩展参考见 `playbook-api-recon`。

# API Recon（前端接口侦察）

在**已授权**前提下，尽可能完整地发现：**后端 API**（路径、方法、参数、响应体）、**前端路由**、**UI 功能触发点**（Tab、弹窗、表格操作等）。

---

## 边界与禁止（Agent 必读 · 违反即越界）

本 skill **仅做 API / 参数面侦察**，不是漏洞挖掘或渗透利用阶段。

### 任务边界

| 范围 | 允许 | 禁止 |
|---|---|---|
| **目标** | 枚举 path、method、参数、路由、UI 触发点 | SQLi/XSS/越权/爆破/fuzz 漏洞、改包攻击、破坏性操作 |
| **鉴权** | Hook + stub/mock 绕过**客户端**登录门 | 向用户索要或猜测账号密码；尝试真实登录表单提交 |
| **运行时** | 无凭据下 hook 接口，用 mock 响应让 SPA 进入登录后壳层 | 依赖真实后端会话才能继续的流程 |

### 无凭据动态分析（Phase 3 默认）

1. 通过 `preload.js` / `runtime_harvest.js` **拦截并 stub** 登录、权限、菜单等 bootstrap 接口；
2. 对业务查询接口返回 **结构正确、业务码成功、数据可为空** 的 mock body；
3. 使前端在无后端或 401 环境下仍能渲染登录后页面，从而触发更多 XHR/fetch/WebSocket；
4. **空数据、空白表格、占位 UI 均属预期**——勿为此转向真实登录或漏洞测试。

**一句话**：用 mock 撑开前端路由与组件挂载，**只录 outbound 请求**；后端返回什么不重要，重要的是前端**还会发哪些接口**。

### 流程硬禁止

| 禁止 | 替代做法 |
|---|---|
| Phase 1 完成前 grep/curl/Read 主 entry `index-*.js` 提取 API path | 跑 `OUTDIR/harvest_static.py` |
| 手写 `extract_apis.py` 等替代 harvest 的脚本 | 改 `OUTDIR/harvest_static.py` 后重跑 |
| 同一 grep/命令失败 ≥2 次仍重复 | 换策略：读 tool_logs、改 harvest、查 reference |
| 跳过门禁 A/B，直接跑 `scripts/` 原版 | 复制到 OUTDIR 并按目标改 |
| 真实用户名/密码、OTP、OAuth 等鉴权 | stub/mock（见上文） |
| 以「拿真实数据」为由跳过 stub，做越权/注入测试 | 只录 outbound，属 recon 边界 |
| 删除、导出敏感数据、批量写等不可逆操作 | coverage 点击亦同 |
| 未完成 runtime + 动态枚举，声称已获全部页面和接口 | 见「完成定义」或标注局限 |
| 未完成参数触发矩阵 + diff，声称已掌握全部参数 | Phase 3b 矩阵 + Phase 5 diff |
| 用单一 runtime 样本推断必填/可选 | 多样本 diff 或校验规则/错误反推 |

---

## 两层模型 + 运行模式

| 层 | 产出 | 上限 |
|---|---|---|
| **静态**（JS bundle） | 全量 endpoint 路径、路由草案、组包点字段候选 | 无 HTTP 方法；参数须 Phase 1b；漏掉运行时拼接 URL |
| **运行时**（活会话） | 方法 + body + 响应 + 动态 URL + WS/SSE；多样本 diff 补全参数 | 页面须实际渲染才会发请求；单样本不足以定必填/可选 |

| 运行模式 | 引擎 | 适用 |
|---|---|---|
| **depth** | `runtime_harvest.js`（Puppeteer） | API 清单、METHOD/params/响应体、WS/SSE、可复现批量跑 |
| **coverage** | browser + `preload.js` | 点 Tab/弹窗/表格，功能点覆盖更深 |
| **both** | 先 depth 再 coverage | 最完整，耗时最长 |

**参数方法论**（无通用脚本）：path 用 harvest/正则；参数用 **锚点扩窗 + UI 绑定链 + 多样本 diff + 错误反推**（grep 配方见 [`playbook-api-recon`](`playbook-api-recon`) J 节）。

---

## 完成定义

全部满足方可声称 recon 完成：

- [ ] **静态**：Phase 1 harvest 产出 `api_static.txt`、`routes.txt`、`js/`
- [ ] **运行时**：至少 depth 或 coverage 之一；coverage/both 须 **Hook 生效 + 动态枚举环**
- [ ] **进壳**：访问业务 path 时非 `/login`（注意 hash 路由）
- [ ] **参数**：coverage/both 完成参数触发矩阵 + `param_samples.json`；Phase 5 合并 `params_merged.json`
- [ ] **深度**（若模块页空白）：Phase 4 权限树还原并重跑，直至出现 **module 级 API**（非仅 locale/bootstrap）
- [ ] **交付**：Phase 5 产出齐全（见 Phase 5 产出表）；`record_asset` 写入服务与端点资产

---

## 脚本与门禁

`scripts/` 仅为参考模板，**禁止**直接跑原版并当最终结果。

**规则**：先读 → 按目标改 → 写入 `OUTDIR`（如 `recon/`）→ 记 `CHANGES.md`；不匹配则按方法论重写，只借结构。

| 门禁 | 何时 | 参考脚本 → OUTDIR 副本 | 常见必改项 |
|---|---|---|---|
| **A（静态）** | Phase 0 后、**第一次**跑 harvest/spider 前 | `harvest_static.py` / `spider_mpa.py` | **多数站点默认 regex 可直接跑**；仅 manifest/方言不匹配时改 endpoint 正则、webpack/Vite `publicPath`、MPA exclude/cookie |
| **B（运行时）** | Phase 2 后、跑 depth/coverage 前 | `runtime_harvest.js` / `preload.js` + `config.json` | Cookie/localStorage 键、neutralize 成功值、stubs、login 正则、api 前缀、hash/history |

**SPA 强制顺序**（不可交换；Phase 编号优先于「先探索再脚本」）：

| 步骤 | 必须 | 禁止 |
|---|---|---|
| Phase 0 完成后 | 下一条 Bash = `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | curl/grep/Read 主 entry `index-*.js`（通常 >500KB） |
| 门禁 A | 复制脚本 → 按需小改 → **立刻运行** | 先手工提取 API 再决定是否 harvest |
| Phase 1 完成前 | `wc -l` 校验产出；404 改 harvest 重试 | 手写 extract 脚本；对未下载 URL 反复 grep |
| Phase 1b 起 | grep 仅 `OUTDIR/js/*.js` | 用主 bundle 代替 harvest |

- ✅ 复制 `harvest_static.py` → （可选）改 regex → **立即运行**
- ❌ curl 主 bundle → grep 多次 → 写临时 extract → 最后才 harvest
- **MPA**：Phase 0 后下一条 Bash = `python3 OUTDIR/spider_mpa.py ...`

---

## 工具与输出约束

| 约束 | 说明 |
|---|---|
| 大文件 | >100KB 的 `index-*.js` **禁止** Read/grep 进上下文；用 OUTDIR 脚本批处理 |
| grep 输出 | 必须 `\| head -20` 或 `-m 5`；对话只保留 path 摘要，勿贴 bundle 片段 |
| 校验 | 用 `wc -l`、`ls \| wc -l`；勿 Read 整目录 |
| regex 初探 | 可选、≤1 次、仅 ≤50KB 小 chunk 或 HTML；正式静态以 harvest 为准 |
| reference | 配方/模板/排障见 [`playbook-api-recon`](`playbook-api-recon`)，勿重复 inline 全文 |

---

## 执行路线图

```
Phase 0 分类 + OUTDIR
  → 门禁 A → Phase 1 harvest（★ 立刻运行 ★）
  → Phase 1b 参数逆向
  → Phase 2 鉴权三道门 → config.json
  → 门禁 B → Phase 3 运行时 + 参数矩阵
  → Phase 4 权限树（必要时）→ 重跑 Phase 3
  → Phase 5 合并报告 + record_asset批量插入所有发现的服务、端点api资产，无论如何插入时不允许漏掉已发现的资产
```

按序勾选；**前一项未完成不得进入下一 Phase**。

1. [ ] **Phase 0**：初探 SPA/MPA；创建 `OUTDIR` → [Phase 0](#phase-0--分类)
2. [ ] **门禁 A + Phase 1**：复制脚本 → **立刻** harvest → `wc -l` 校验 → [Phase 1](#phase-1--静态)
3. [ ] **Phase 1b**：锚点扩窗 + 绑定层 → `param_candidates.json` → [Phase 1b](#phase-1b--参数逆向)
4. [ ] **Phase 2**：鉴权三道门 → `config.json` → [Phase 2](#phase-2--鉴权三道门)
5. [ ] **门禁 B**：调整 runtime 脚本 → [Phase 3](#phase-3--运行时)
6. [ ] **Phase 3**：depth / coverage / both；确认进壳；参数触发矩阵 → `param_samples.json`
7. [ ] **Phase 4**（若需要）：权限树 → patch stubs → 重跑 Phase 3 → [Phase 4](#phase-4--权限树还原)
8. [ ] **Phase 5**：合并产出 + 报告 + `record_asset` → [Phase 5](#phase-5--合并与报告)

---
