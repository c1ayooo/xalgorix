---
name: playbook-api-recon
description: "api-recon — 参考手册"
tags: [playbook, api-recon, recon]
intent: offensive
---
# api-recon — 参考手册

Grep 配方、`config.json` 模板与排障。所有 grep 针对 `js/` 目录执行。bundle 单行时可先 `js-beautify` 或 `sed 's/}/}\n/g'`，通常带上下文窗口的 raw grep 即可。

## 脚本说明

`scripts/` 内所有文件均为**参考模板**，执行前必须按目标站点调整。典型改动点：

| 脚本 | 常见需调整项 |
|---|---|
| `harvest_static.py` | endpoint 正则、webpack/Vite manifest 解析、微前端 publicPath、重试/并发 |
| `runtime_harvest.js` | neutralize 字段名与成功值、stub 匹配规则与 body 结构、routes 来源、WS 录制、`waitUntil`/`routeTimeout`/`proxy` |
| `preload.js` | `loginPathRe`、L1 stubs、`neutralize.fields`、`apiPattern`、是否启用 L3、`recordDetail`、`observe.*`、`neutralizeVueRouter` |
| `spider_mpa.py` | `--exclude` 破坏性链接、cookie、depth/max、同域过滤 |
| `extract_route_map.py` | `routeMap` / `routeLink` 正则、KEY 命名模式 |
| `build_perm_tree.py` | `userRouteAuth` 解析、`ROOTS`/`PREFIX_PARENT` 层级启发式、stub 外层字段名 |
| `config.json` | 以上全部站点专属参数的统一入口 |

调整后的文件建议放在任务工作目录（如 `recon/`），报告中注明相对参考脚本的具体改动。

---

## A. 逆向三道门

### A1. 渲染门 — 「如何判断已登录？」

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

找链路 `isLogin = f(getUser())` → `getUser = decode(storage.read(KEY))`，确定 **存储键**、**容器**（Cookie vs localStorage）、**编码**：

| 编码 | config 伪造方式 |
|---|---|
| 明文字符串 / `"1"` / token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | 无签名/`alg:none` JWT，或 bundle 内密钥签名 |
| 加密（SM2/AES/RSA） | 找硬编码密钥；渲染门仅需可解码 blob 时可 forge；否则静态兜底 |

→ 写入 `cookies` / `localStorage`。

### A2. 拦截器门 — 「什么触发跳 /login？」

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(未登录|请重新登录|登录已过期|unauthorized|登录失效|授权|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

确定：**字段名**、**成功值**（通常 `0` 或 `200`）、**触发跳转的失败值**。用 junk session 验证：

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

→ 写入 `neutralize.fields` + `neutralize.success`。

### A3. 内容门 — 「菜单/权限从哪来？」

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

**两层数据**（常见企业后台）：

| API | 典型 payload | 消费方 |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | 路由守卫、按钮级 ACL |
| `.../permissions/all` | `tree[{ code, position, children }]` | 侧栏菜单渲染 |
| bundle 内 `userRouteAuth` | `{ CODE: { url, name? } }` | code → 前端 path |
| bundle 内 `routeMap` | `{ KEY: { name, link } }` | 别名解析（webpack `o.DASHBOARD`） |

读消费方代码确认：`getResultTree(tree, permissions)` 如何过滤、`v-if` / `hasAuth(code)` 检查哪个字段。

**手工 forge**（小站点）：构建 permissive payload → `stubs`。

**完整权限树还原**（大站点，侧栏/子模块仍空白）：见 **I 节**。

---

## B. config.json 模板

```json
{
  "baseUrl": "https://target/",
  "runtimeMode": "both",
  "chromium": "/usr/bin/chromium",

  "cookies": [
    { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
  ],
  "localStorage": { "token": "faketoken", "isLogin": "1" },

  "neutralize": {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": { "success": true, "message": "ok" }
  },
  "forward": true,
  "loginUrlPattern": "/login",
  "apiPattern": "/api/|/rest/|/graphql",

  "mockTier": "L1+L2",
  "recordDetail": true,
  "observe": {
    "storageReads": false,
    "cookieReads": false,
    "xhrHeaders": true
  },
  "neutralizeVueRouter": true,
  "stubs": [
    {
      "match": "permissions/all|/menu|role_permissions",
      "body": {
        "response_code": 0, "code": 0,
        "data": {
          "permissions": ["*"],
          "menus": [
            { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
            { "name": "alert", "path": "/alert", "show": true, "children": [] }
          ]
        }
      }
    }
  ],

  "explore": {
    "clickTabs": true,
    "clickTables": true,
    "pushStateFallback": true,
    "maxMenuItems": 50
  },

  "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
  "waitMs": 1500, "perRouteMs": 900, "headless": true,
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "proxy": "",

  "captureResponses": true, "recordWs": true, "respMax": 600
}
```

字段说明：
- `runtimeMode`：`depth`（Puppeteer）、`coverage`（browser MCP）、`both`
- `cookies[].value` 前缀：`b64json:` → base64(JSON)；`json:` → 原始 JSON；无前缀 → 字面量
- `forward: true` 转发真实请求并改写码字段；`false` 完全离线 stub
- `mockTier`：coverage 模式 preload 启用层级，如 `L1+L2`、`L1+L2+L3`
- `routes` 来自 `routes.txt`；forge 菜单后 harness 自动追加 `<a href>`
- `captureResponses` / `recordWs` 仅 depth 模式有效
- `waitUntil`：大型 SPA 用 `domcontentloaded`，避免 `networkidle2` 挂起
- `routeTimeout`：单路由 `page.goto` 超时（毫秒）
- `proxy`：Puppeteer `--proxy-server`；也可设 `HTTP_PROXY` / `HTTPS_PROXY`

### B1. 双 stub 模板（role_permissions + permissions/all）

```json
"stubs": [
  {
    "match": "role_permissions",
    "body": {
      "response_code": 0,
      "data": {
        "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
        "role_type": "SUPER_ADMIN"
      }
    }
  },
  {
    "match": "permissions/all",
    "body": {
      "response_code": 0,
      "data": [
        {
          "code": "MONITOR",
          "position": 1,
          "children": [
            { "code": "MONITOR_ALERT", "position": 1, "children": [] }
          ]
        }
      ]
    }
  }
]
```

外层字段名（`response_code` / `code` / `data`）须与 A2 拦截器门一致；`permissions` 须覆盖 tree 中所有 leaf code。

---

## C. coverage 模式：preload 配置

编辑 `（辅助脚本未随附，按文中步骤手工执行）` 顶部 `CONFIG` 对象，或通过 CDP 注入前替换：

```javascript
const CONFIG = {
  loginPathRe: /\/(login|signin)(\/|$|\?)/i,
  mockTier: 'L1+L2',
  forward: true,
  recordDetail: true,
  extractUrlsFromResponse: true,
  neutralizeVueRouter: true,
  observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
  neutralize: { fields: ['response_code', 'code'], success: 0 },
  stubs: [ /* 同 config.json stubs */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

验证：`window.__API_RECON_PRELOAD__ === true` 且 pathname 稳定。

导出录制结果：

```javascript
JSON.stringify({
  apis: [...window.__API_RECON_LOG__],
  detail: window.__API_RECON_DETAIL__,
  routes: [...(window.__API_RECON_ROUTES__ || [])],
  observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

---

## D. preload / runtime Hook 能力

preload（coverage）与 runtime_harvest（depth）内置的浏览器 Hook 能力及覆盖范围：

| Hook 能力 | 对 API 发现的价值 | 覆盖 |
|---|---|---|
| Hook fetch / XHR.open | 录请求 URL/方法 | ✅ `recordDetail` + `__API_RECON_LOG__` |
| Hook XHR.setRequestHeader | 发现 Authorization 等头 | ✅ `observe.xhrHeaders` |
| Hook localStorage/cookie 读 | 确认会话键名 | ⚠️ 可选 `observe.storageReads/cookieReads` |
| Vue 获取路由 | 补全 frontendRoutes | ✅ `__API_RECON_ROUTES__`（已加载路由） |
| Vue 路由守卫中和 / 登录跳转阻断 | 撑开模块触发 API | ✅ `neutralizeVueRouter` + 原生跳转中和 |
| React 获取路由 | 补路由 | ⚠️ 静态 + 点击；无专用 Hook |
| 页面跳转阻断（登录 path） | 留页分析 | ⚠️ 仅阻断登录 path，避免挡业务导航 |
| Hook 加密库（CryptoJS/SM 等） | 加密参数 → 明文 API body | ❌ 须手工 Hook 加密函数入参；结论写 config |
| 反调试 bypass | 否则 runtime 录不到 API | ❌ 须手工处理；静态仍可用 |

---

## E. Endpoint 提取正则（静态过少时）

在 `harvest_static.py` 的 `extract_endpoints` 放宽，或手动：

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---