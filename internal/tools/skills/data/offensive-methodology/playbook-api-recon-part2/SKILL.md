---
name: playbook-api-recon-part2
description: "api-recon — 参考手册（第 2/2 部分）"
tags: [playbook, api-recon, recon]
intent: offensive
---
## F. 排障

| 现象 | 原因 → 处理 |
|---|---|
| 静态 API 很少 | endpoint 方言不匹配 → 放宽正则（D 节） |
| chunk 数 ≪ manifest | CSS-only 或未部署 chunk；404 已重试 |
| runtime 仍显示登录页 | 渲染门错误 → 复查 A1：键名、容器、编码、domain |
| 进壳但模块空白 | 内容门 → forge 菜单（A3）；`routes` path 可能不对 |
| 每路由只有 bootstrap/locale | 权限码不全 → I 节权限树还原；检查 `role_permissions` + `permissions/all` 双 stub |
| 侧栏有项但子页空白 | tree 缺 intermediate 节点或 code 与 `userRouteAuth` 不一致 |
| 每个 API 都跳登录 | 拦截器门 → 确认 `neutralize`；嵌套字段需扩展 walk 逻辑 |
| WS 帧为 0 | 需用户交互后才 subscribe；加长 `perRouteMs` |
| 响应体空 | 仅 `forward: true` 时有真实响应 |
| Chromium 缺失 | 安装 chromium 或设置 `config.chromium` / `CHROMIUM` |
| Mock 很多仍回登录 | Hook 太晚或缺 `location.href` setter → document-start + preload |
| 列表全空 | L3 空数组正常；继续点 Tab/设置/详情 |
| 误把 Redux action 当路由 | 过滤含 get/set/change/clear/toggle/upload 的内部 path |
| Vue 仍跳登录 | preload 非 document-start → 改注入时机；或 `neutralizeVueRouter: false` 时手动清守卫 |
| 响应里有 URL 但未进 log | 开 `extractUrlsFromResponse`；或从 `__API_RECON_DETAIL__` 人工提取 |
| 不知 Authorization 头名 | 开 `observe.xhrHeaders` 或 DevTools 查看请求头 |
| runtime 极慢 / 超时 | 改 `waitUntil: domcontentloaded`；降 `routeTimeout`；勿用 `networkidle2` |
| 代理连接失败 | 检查 `proxy` / 环境变量；Puppeteer 与 curl 代理端口一致 |

---

## G. hardened 目标

服务端逐步校验会话（不可 forge 的签名 cookie、服务端渲染且不可 stub 的菜单）时，runtime 会在 shell 处卡住。预期行为：

- **静态足够做 endpoint 枚举** — 模块 path 在代码里
- 若授权允许，用**真实会话**跑同一 harness：`forward: true`、无需 neutralize，捕获真实 methods/params/responses

---

## H. 单次任务清单

1. 确认授权范围
2. **阅读** `（辅助脚本未随附，按文中步骤手工执行）` → 按目标调整 → 运行 → 审 `api_static.txt`、`routes.txt`
3. **Phase 1b**：path 锚点扩窗 + 绑定层 → `param_candidates.json`（J 节）
4. 逆向 A1/A2/A3 → 写站点专属 `config.json`
5. **阅读并调整** `runtime_harvest.js` / `preload.js` 后再执行
6. `runtimeMode=depth`：`npm install` → 运行调整后的 harvest 脚本
7. `runtimeMode=coverage/both`：document-start 注入调整后的 preload → browser MCP 动态枚举 + **参数触发矩阵**
8. 模块不渲染 → **I 节权限树还原** → patch stubs → 重跑
9. 参数多样本 diff + 错误反推 → `params_merged.json`
10. 合并 → `site_map.json` + `api_merged.txt`，诚实标注覆盖、缺口及脚本改动点

---

## I. 权限树还原（Phase 4 深化）

当 forge 简单 `menus: [{ path, show: true }]` 无效、子模块仍不 mount 时使用。

### I1. 定位 auth 模块

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

记录：**权限 API path**、**响应字段名**、**消费 chunk 文件名**。

### I2. 提取 routeMap

```bash
python3 （辅助脚本未随附，按文中步骤手工执行） recon/js recon/
# 产出 recon/route_map.json
```

若 `[!] no routeMap pattern found`：放宽 `extract_route_map.py` 中正则，或手工 grep：

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. 构建权限树 + stub

```bash
python3 （辅助脚本未随附，按文中步骤手工执行） recon/js recon/ --config recon/config.json
```

脚本逻辑：
1. 解析 `userRouteAuth={MONITOR:{url:...},...}`（含 webpack 别名 `He=o.DASHBOARD`）
2. 用 `route_map.json` 解析 alias → 真实 path
3. 按 code 前缀推断 parent（`MONITOR_ALERT` → `MONITOR`）
4. 输出 `permissions_tree.json`、`permissions_all_stub.json`、`role_permissions_stub.json`
5. `--config` 时自动写入 `config.json` 的 `stubs` 与扩展 `routes`

**按目标调整**（在脚本顶部）：
- `DEFAULT_ROOTS`：顶级模块 code 列表
- `DEFAULT_PREFIX_PARENT`：`PREFIX_` → parent 映射
- `DEFAULT_EXTRA_PARENT`：非前缀关系的 orphan 节点

### I4. 校验 stub 一致性

```bash
# permissions 数量应 ≈ userRouteAuth 条目数
wc -l recon/perm_codes_all.txt
# routes 应覆盖 route_map 全部 link
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. 重跑 runtime 并对比

```bash
node recon/runtime_harvest.js recon/config.json
# 对比 forge 前后 runtime_api.json 条数；检查 /attack、/asset 等是否出现模块 API
```

| forge 前 | forge 后（成功） |
|---|---|
| 每路由相同 3–5 条 bootstrap | 不同路由触发不同 module API |
| 仅 `/api/locale/language` | 出现 `/api/web/...` 模块 endpoint |
| `routes.txt` 个位路由 | `routes` 80–110+ 来自 route_map |

### I6. 仍失败时

- **coverage 模式**：点击侧栏 + Tab，权限 gating 可能在交互后才请求
- **stub 字段**：对比真实 API（curl + 真实 session）与 stub 的 nesting
- **额外守卫**：grep `hasPermission|checkRole|func.` 等按钮级检查，扩展 `role_permissions.permissions`
- **静态兜底**：模块 API path 仍在 `api_static.txt`，runtime 仅补 METHOD/body；参数保留 `param_candidates.json` + 已录样本

---

## J. 参数逆向（Phase 1b / 5b / 5c）

**方法论，非通用脚本。** 找 path 用正则；找参数用锚点扩窗 + UI 绑定链 + 多样本 diff + 错误反推。

### J1. 锚点扩窗 — 从 path 找组包对象

```bash
# 以 Phase 1 已知 path 为锚
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. 包装层与传输形态

```bash
# axios / 统一 request
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart
grep -rhoaE 'FormData|\.append\(' js | head

# 路径参数
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. 校验门 — 必填 / 格式 / 枚举

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. 绑定层 — 表单 → API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

runtime 补位：DevTools → Network → 请求 → **发起程序**（call stack）从 `fetch`/`send` 往上追组包函数。

### J5. 加密参数

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

**勿在密文上猜字段** — Hook 加密函数**入参**，在加密前录 plaintext payload；结论写 `config.json` / `param_candidates.json`。

### J6. 参数触发矩阵（Phase 3 必做）

对每模块按操作各录一次，diff 请求 body/query：

| 操作 | 关注 |
|---|---|
| 列表首屏 | 分页默认值 |
| 搜索 | keyword、filters |
| 高级筛选 | optional 字段 |
| 新建/编辑 | 完整 entity |
| 批量/导出 | `ids[]`、`exportType` |
| 排序/翻页 | `sortField`、`order` |

产出 `param_samples.json`：`[{ "path", "method", "action": "search", "body", "query", "headers" }]`

### J7. 置信度规则

| 置信度 | 条件 |
|---|---|
| **高** | 静态 callsite + runtime ≥2 样本一致 |
| **中** | 仅静态，或仅 1 次 runtime |
| **低** | 响应/错误反推，未二次验证 |
| **待触发** | 静态已知字段，UI/权限未跑到 |

### J8. 场景快配

| 场景 | 顺序 |
|---|---|
| REST 列表页 | J1 组包对象 → J6 四次 diff → J3 rules |
| 新建/编辑表单 | J3 Form name → J4 submit 链 → runtime 提交 + 故意留空看 400 |
| GraphQL | J2 variables 声明 → runtime 各 operation 录 variables |
| 加密 body | J5 Hook 入参 → 加密前字段即真实 params |

### J9. 与 api-recon 阶段映射

| api-recon | 参数 recon |
|---|---|
| Phase 1 静态 | J1 锚点扩窗 |
| Phase 2 A2 拦截器 | 全局注入字段（tenantId、sign） |
| Phase 3 runtime | J6 触发矩阵 + `param_samples.json` |
| Phase 4 权限树 | 不同模块表单不同 → 权限够才触发全字段 |
| Phase 5 合并 | `params_merged.json` + 置信度；勿单样本定必填 |

### J10. 排障

| 现象 | 处理 |
|---|---|
| 静态有字段名 runtime 从未出现 | 标注「待触发」；补权限树 / 点高级筛选 / 联动 select 各 option |
| 同 path 不同 body 形状 | 正常 — 按 `action` 分条记录，勿强行合并 schema |
| stub 响应假但想看 params | **看 outbound 请求** body/headers，勿从 stub 响应反推 |
| 400 报 nested field | 注意外层包装 `data`/`bizData`/`variables` |
| GraphQL 只见 operation 名 | 展开 `variables` JSON；静态找 `$var: Type` |

---

> （续前：playbook-api-recon 第 2/2 部分）
