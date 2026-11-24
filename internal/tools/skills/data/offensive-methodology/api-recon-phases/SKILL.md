---
name: api-recon-phases
description: "收集网站API接口时调用该skill。（执行篇：Phase 0-5）"
tags: [cwe-200, t1595-002, recon, api-surface]
intent: offensive
---
> 概念、边界与路线图见 `read_skill("api-recon-playbook")`。

## Phase 0 — 分类

拉取入口 HTML，**创建 `OUTDIR`**（勿改 skill 内 `scripts/`）：

- **SPA**：空壳 + `<div id=app>` + chunk → Phase 1–5
- **MPA**：SSR + `<form>`、无 endpoint bundle → 门禁 A 后：

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

产出 `forms.txt`、`links.txt`、`api_inline.txt`。SPA 若 forms ≈ 0 → 切 Phase 1。

---

## Phase 0b — API 契约（先试这个，可能直接省掉 Phase 1）

**先试契约文件，再考虑爬 JS。** 契约（OpenAPI/Swagger/Postman）直接给出「后端支持哪些接口、哪些参数、哪些方法、哪些要鉴权」——比爬虫准、比静态分析快（一次 GET vs 下载全部 chunk）。目标暴露 `/swagger-ui/`、`/api-docs` 时，契约往往**一次就够**。

用 `probe_body_contract` 工具（它会探测常见契约路径并解析出清单）：

```
probe_body_contract(base_url="https://target.example.com")
```

**拿到契约后**：
- 契约里的接口清单 → 直接进 Phase 4（权限树还原）与参数分析，**可以跳过 Phase 1/3**；
- 契约里的 `auth_required` 标记 → **需要鉴权的那批接口是越权挖掘的核心对象**，单独列出来看；
- 契约里没有的接口（后端支持但没写进契约的）→ 仍要靠 Phase 1/3 补。

**没找到契约很常见**（很多站点不暴露）→ 直接进 Phase 1，不要在这里耗时间。

> 契约是**接口清单，不是漏洞**。拿到清单后是否可打、怎么打，交回 vuln-hunting 的方法论判断。

---

## Phase 1 — 静态

遵守 [脚本与门禁](#脚本与门禁) · [工具与输出约束](#工具与输出约束)。

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

harvest：解析 HTML script → webpack/Vite manifest → 下载全部 lazy chunk → 产出 `js/`、`api_static.txt`、`routes.txt`、`chunkmap.txt`。

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- chunk 数 vs manifest：404 须改 harvest 重试，勿手工 curl 逐个 chunk
- `api_static.txt` 过少 → 放宽 OUTDIR 内 endpoint 正则后重跑（见 reference）

### Phase 1b — 参数逆向

path 来自 Phase 1；参数字段须单独 recon。grep 规则见 [工具与输出约束](#工具与输出约束)。

**完成标准**：重要接口能答——字段名、传输位置、类型推断、是否必填、样本值、置信度。

#### 1b.0 — 传输形态

| 形态 | 参数在哪 | 静态优先看 |
|---|---|---|
| REST JSON | body + query | path 锚点旁 `(params\|data\|body)\s*:\s*\{` |
| GraphQL | `variables` | gql 模板、`$page: Int` |
| 传统 form | urlencoded | `<form>`、`FormData` |
| 文件上传 | multipart | `FormData.append` |
| 路径参数 | `/user/:id` | 路由表 + `useParams` / `$route.params` |
| 加密/签名 | 包进 `sign`/`data` | Hook 加密函数入参（reference D 节） |

产出：每接口标注 `transport: query|json|form|graphql|encrypted`。

#### 1b.1 — 锚点扩窗

以已知 path 为锚，扩窗口找组包对象：

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| 包装层 | 参数线索 |
|---|---|
| axios 实例 | `data` / `params` |
| 统一 request | 拦截器注入全局字段 |
| OpenAPI 客户端 | 生成 method 签名 |
| React Query / SWR | hook 第二参数 |
| Vue composable | composable 入参 |

类型残留：`yup`/`zod`/rules、`Form.Item name=`、内嵌 Swagger。

→ `param_candidates.json`：`{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — 绑定层

```
Form field → onFinish/handleSubmit → transform → API payload
```

| 绑定源 | 手法 |
|---|---|
| 表单 submit | 跟 submit → transform → API |
| 表格搜索 | `getFieldsValue()` → `params` |
| 路由 | `:id` / `?tab=` |
| 拦截器 | 全局 `tenantId`、分页、sign |
| 枚举 select | `options` → API 枚举值 |

DevTools call stack 从 `fetch`/`XHR.send` 往上追组包函数。

#### 1b.3 — 组包三问（≠ Phase 2 鉴权三门）

| 问 | 要答什么 |
|---|---|
| **组装** | payload 在哪 build、transform 痕迹 |
| **校验** | required、pattern、enum |
| **传输** | path / query / body / multipart / 头 |

拦截器门（Phase 2）顺带读全局注入字段（Authorization、`X-Tenant-Id`、sign）。

#### 1b.4 — 与 Phase 3 衔接

候选字段来自静态/绑定层；**必填/可选/条件依赖**须 Phase 3 参数矩阵 + diff + Phase 5 错误反推。

---

## Phase 2 — 鉴权三道门

在 `OUTDIR/js/` grep（带 `head`），写入 `config.json`（配方见 reference）：

| 门 | 问题 | 关键词 |
|---|---|---|
| **渲染门** | 如何判断已登录？ | `isLogin`、`getToken`、Cookie/localStorage |
| **拦截器门** | 什么触发跳 `/login`？ | `response_code`、`errno`、axios interceptor |
| **内容门** | 菜单/权限从哪来？ | `menu`、`permission`、`role`、`acl`、`routes` |

禁止把 localStorage 键名当凭据——须从 chunk/请求链确认。

**出口 = 门禁 B**：结论落到 `config.json`，并改 `OUTDIR/runtime_harvest.js` / `preload.js`。

### Phase 2b — API 观察（可选）

用 OUTDIR 内 `preload.js` 确认会话键名、Authorization、嵌套 API URL：

| 配置 | 产出 |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | headers 观察 |
| `extractUrlsFromResponse: true` | 响应内子 API |
| `observe.storageReads/cookieReads: true` | 回填 config |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

coverage 每轮导出：`__API_RECON_LOG__`、`__API_RECON_DETAIL__`、`__API_RECON_ROUTES__`、`__API_RECON_OBSERVE__`。

---

## Phase 3 — 运行时

须已过门禁 B；遵守 [边界与禁止](#边界与禁止agent-必读--违反即越界) · 无凭据 mock 策略。

`config.json` 设置 `"runtimeMode": "depth" | "coverage" | "both"`（模板见 reference）。

### Hook 与 stub（depth + coverage 共用）

| 层 | 范围 | 目的 |
|---|---|---|
| L1 精确 | auth/权限/bootstrap stub | 过首屏鉴权 |
| L2 负向修正 | 所有 JSON 响应 | 未登录码 → 成功 |
| L3 兜底 | 未命中 L1 的 `/api` 等 | 空成功体，撑开 UI |

- **depth**：fake auth + `forward` 改业务码 + `stubs`；遍历 `routes`（hash/history）；产出 `runtime_api.json`
- **coverage**：**document-start** 注入 `preload.js`（CDP `addScriptToEvaluateOnNewDocument` 或 Userscript）

验证：`window.__API_RECON_PRELOAD__` 存在；业务 path 不回 `/login`。

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### 3b — coverage 动态枚举（必做）

1. 主导航/侧栏 — 每项点击，等网络 1–3s
2. Tab — `role=tab`、`.ant-tabs-tab`
3. 表格 — 首行查看/编辑/详情
4. 工具栏 — 导出、筛选、新建（**避免不可逆删除**）
5. 每进模块 — 合并 API/路由
6. SPA — 对 `routes.txt` 未覆盖 path 受控 `pushState`（MPA 禁止）

**参数触发矩阵**（必做）：每模块按操作类型各录一次，**diff 多样本**：

| 操作 | 通常多出的参数 |
|---|---|
| 列表首屏 | 分页 + 默认筛选 |
| 点搜索 | keyword、filter |
| 高级筛选 | 更多 optional |
| 新建/编辑 | 完整 entity |
| 批量/导出/排序 | `ids[]`、`exportType`、`sortField` |

**stub 下 outbound body/headers 仍真实**——以请求为准。录制 → `scan_raw.json`、`param_samples.json`、`api_detail.json`。

- **Vue**：`neutralizeVueRouter: true` + document-start preload
- **React**：`routes.txt` + 侧栏点击 + `pushState`
- **both**：先 3a depth，再 3b coverage

---

## Phase 4 — 权限树还原

**触发**：模块页空白 / 每路由仅 bootstrap（如 locale）→ 内容门未过。

| 现象 | 含义 |
|---|---|
| 进壳成功 | 渲染门 + 拦截器门已过 |
| 侧栏缺项/点击空白 | stub shape 或权限码不全 |
| 每路由 API 相同且极少 | `v-if permission` 未通过 |
| `routes.txt` 远少于 bundle | 须从 auth 模块补全 |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

典型链：`role_permissions`（flat codes）+ `permissions/all`（tree）→ `getResultTree` → `userRouteAuth[CODE].url`。

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

中间产出：`route_map.json`、`userRouteAuth.json`、`permissions_tree.json`、`*_stub.json`、`perm_codes_all.txt`。

stub 检查：外层 `response_code` 与拦截器门一致；flat codes 与 tree 对齐；`routes` 覆盖 `route_map` 全部 link。

更新 `config.json` 后**重跑 Phase 3**。大型 SPA 可调 `waitUntil`、`routeTimeout`、`perRouteMs`（见 reference A3/I 节）。

---

## Phase 5 — 合并与报告

### 产出表

| 文件 | 阶段 | 内容 |
|---|---|---|
| `js/`、`api_static.txt`、`routes.txt`、`chunkmap.txt` | 1 | 静态 bundle 与 path |
| `param_candidates.json` | 1b | 静态参数字段候选 |
| `config.json` | 2 | 三道门 + runtime 配置 |
| `runtime_api.json` | 3a | depth 详细录制（含 WS/SSE） |
| `param_samples.json`、`scan_raw.json`、`api_detail.json` | 3b | 多样本、点击日志、detail |
| `route_map.json` 等 | 4 | 权限树中间文件（若执行） |
| `params_merged.json` | 5 | 合并参数字段 + 置信度 |
| `api_merged.txt` | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| `site_map.json` | 5 | 路由、API、params、功能点、局限 |
| **record_asset** | 5 | 将所有服务、端点资产写入资产库 |

### 5b — 参数合并

从 `param_samples.json` diff，**无通用合并脚本**。置信度规则见 reference J7（高/中/低/待触发）。

### 5c — 错误反推

授权范围内可发不完整请求读 400（**属参数 recon，非漏洞测试**）：`field 'x' is required`、枚举错误等。注意 `data` 包装、`variables`、加密前 `bizData`。

报告须注明：runtimeMode、静态/运行时 API 数、参数置信度、未覆盖模块、相对参考脚本的 `CHANGES.md` 摘要。

`site_map.json` 建议结构：

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

更多字段与 grep 配方见 [`playbook-api-recon`](`playbook-api-recon`)。

---

## 通用说明

- **框架无关**：webpack/Vite/Angular lazy load 方法相同
- **传输**：REST/JSON、GraphQL、WebSocket、SSE；gRPC-web 不在范围
- **SSR**：客户端 fetch 可录；RSC/Server Actions 不完全可枚举
- **盲区**：JSVMP、WASM、HMAC/mTLS 强校验 → 静态 + 标注局限
- **参数盲区**：条件联动、hidden params、WASM 组包 → 「待触发」/「不可达」
- **静态是安全网**：runtime 被挡时静态仍能枚举 endpoint

---

## 附加资源

- Grep 配方、`config.json` 模板、排障、Hook、参数逆向 J 节、site_map 模板：**[`playbook-api-recon`](`playbook-api-recon`)**
- 参考脚本路径见 [脚本与门禁](#脚本与门禁) 表
