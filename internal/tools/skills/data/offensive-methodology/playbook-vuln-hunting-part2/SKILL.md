---
name: playbook-vuln-hunting-part2
description: "Vuln Hunting 参考（第 2/2 部分）"
tags: [playbook, vuln-hunting, vuln, hunting]
intent: offensive
---
**失败放行**
```python
# 让前置校验失败（如缺字段/错格式），看后续步骤是否仍被执行
# 典型：上传接口校验文件类型失败，但文件已落盘
```

### 证据形态
```
GET /api/item/abc（非数字 id）返回 500，响应体含
"SQLSTATE[42S02]: Base table or view not found: 1146 Table 'app.users' doesn't exist"
—— 报错未做统一处理，向外泄露表名与数据库类型。
```

---

## 维度 7：组合

### 检查清单
- [ ] 「能改的数据」里有没有「决定权限/价格/状态的字段」
- [ ] 「写进去」的内容会不会在「别处」被解析
- [ ] 「上传」的文件会不会被「后端解析」
- [ ] 「拿到的东西」（token/ticket/临时凭证）能不能用在别处

### 手法

**提权链**
```bash
# 1) 低权账号能改的字段里有没有能影响权限的
curl -s -X PUT "https://t/api/user/profile" -H "Authorization: Bearer $TOKEN_LOW" \
  -H 'Content-Type: application/json' -d '{"dept_id":1,"is_manager":true}' | head -c 300
# 2) 重新登录拿新权限，再看能不能访问管理接口
curl -s -H "Authorization: Bearer $TOKEN_NEW" "https://t/api/admin/users" | head -c 300
```

**上传 + 解析**
```bash
# 上传后后端会不会解析？试 SVG（内含 XXE）/ 压缩包（Zip Slip）/ 图片（ImageTragick）
# 名字带 ../ 观察是否落到意外路径
curl -s -F "file=@payload.svg" "https://t/api/upload" -H "Authorization: Bearer $TOKEN" | head -c 300
```

**缓存投毒**
```bash
# 改内容后，另一个身份/无痕窗口看到的还是旧内容？
# 观察响应头里的 cache 指令：X-Cache / Cache-Control / Age
curl -s -D - -o /dev/null "https://t/api/news/1" | grep -iE "cache|age|etag"
```

### 证据形态
```
上传接口接受 SVG 并原样存储（响应体返回可访问 URL /uploads/xxx.svg）；
该 SVG 内含 <!ENTITY xxe SYSTEM "file:///etc/passwd">，
管理员在后台「查看上传」页渲染该文件时触发实体解析。
（验证只用无害实体，不读真实密钥文件。）
```

---

## 维度 8：同族迁移（variant hunting，U7/上游 v0.4.35）

确认一个漏洞后，最高效的下一步不是换新目标，而是**把同一机理复制到同族资产**——
这是人类研究者最常用的"一破全破"杠杆。

### 检查清单

1. **同产品/同版本/同框架**：确认指纹（footer/JS/header/报错页/版本号），在**任务
   范围内**全量搜同款组件——同 CMS 的其他站点、同网关后的兄弟微服务、同测试控制器
   前缀、同中间件（Nacos/Druid/Actuator/MinIO/Redis）。
2. **同资产族**：同一根域名/ICP/子域批次的站点；同 IP 段内同端口同 banner 的服务；
   同一套 SSO/网关下的不同业务系统。
3. **同机理变体**：未授权读 → 兄弟只读端点/导出接口；IDOR → 相邻对象 id/批量接口；
   登录绕过 → 同产品的其他登录入口；上传 → 同框架的其他上传点。
4. **复用线索**：把已确认的 PoC、路径、参数、凭据写进 variant 意图的 summary，
   worker 直接拿去验证，不重新摸索。
5. **边界**：variant 必须锚定 task_scope 内资产；跨域/跨客户资产一律不碰（授权红线）。

### 反模式

- 确认一个漏洞就换新目标（浪费已建立的指纹与路径认知）。
- 只搜同域名不搜同产品（同 CMS 的其他客户站点往往同样脆弱，但**必须在本任务授权
  范围内**）。
- variant 意图不写清"验证什么同款机理"，worker 只能重新盲搜。

## 证据记录模板（写回时照此组织）

**finding（已确认）**：
```
【行为】正常请求 vs 异常请求 的具体差异
【前提】账号 A（uid=?，角色=?）/ 对象 id=? / 并发数=?
【复现】完整可复现步骤（命令 + 关键参数）
【影响】成功后发生了什么（读了谁的数据 / 多了几张券 / 覆盖了谁的配置）
【反证】换身份/换参数顺序/换客户端后仍成立
【无害性】证明过程未造成破坏
```

**fact（未确认）**：
```
【嫌疑点】哪个接口的哪个行为可疑
【现象】观察到什么（报错差异/状态码差异/响应长度差异）
【为何未确认】缺什么条件（需要一个 X 账号 / 某参数没找到 / 竞态未复现）
【下一步】规划者可以派什么意图来确认
```

---

## 平台工具速查

| 场景 | 用法 |
|---|---|
| 拿基线流量 | `traffic_search`（必须指定 host）→ `traffic_get(id)` 看原文 |
| 大响应 | `traffic_blob(hash)` 分段取 |
| 并发脚本 | 写到**本意图工作目录**，用 `python3` 跑，2~5 并发 |
| 参数差异对照 | 两个响应存成文件后用 `diff_responses` 工具（结构化差分，自动滤噪声；不要用 `diff` 逐字节比——时间戳不同就报"不同"） |
| 目录/参数枚举 | `ffuf` / `feroxbuster` / `gobuster` |
| 端点枚举 | `katana`（爬路径） |
| 中间件协议 | 宿主环境页「中间件协议」组（gmqtt/pika/pymongo/zeep/netmiko…） |

> （续前：playbook-vuln-hunting 第 2/2 部分）
