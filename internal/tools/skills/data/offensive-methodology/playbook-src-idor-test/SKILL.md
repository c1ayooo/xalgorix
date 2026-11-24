---
name: playbook-src-idor-test
description: "写不写只认 reportvulnerability 的字段与证据要求。进站有会话时最低探针见 dig-scope §4.2.3（对象图、换 id、哨兵值）；单号用列表/回包里的他人 id，不为第二号磨注册。默认先用读/列表差分证明跨用户·跨租户。写越权仍要测，但不是对着别人已有数据改/删。顺序见下「写越权怎么打」。禁止批量改删、禁止真资损。"
tags: [playbook, src-hunting, idor, test]
intent: offensive
---
> 写不写只认 ``report_vulnerability` 的字段与证据要求`。进站有会话时最低探针见 `dig-scope` §4.2.3（对象图、换 id、哨兵值）；单号用列表/回包里的他人 id，不为第二号磨注册。默认先用读/列表差分证明跨用户·跨租户。写越权仍要测，但**不是**对着别人已有数据改/删。顺序见下「写越权怎么打」。禁止批量改删、禁止真资损。
> 短表指针用标题搜。英文 BOLA 百科已砍；写越权怎么打仍在上半。

## 一、原有知识库

# 越权漏洞（IDOR/权限绕过）测试手册

> **最小伤害：** 默认读/列表差分。写越权要测，按「先加自己的 → 再删自己加的」。不要把「只读」理解成「写 IDOR 不测不报」。

### 写越权怎么打（先加后清）

读差分够闭环就不要写。读不够、要证「能改别人的东西」时：

1. **先测添加。** 用自己的号（或未登录）调创建口，看能不能挂到别人名下 / 别人店 / 别人租户。成功 = 写越权已证。  
2. **再删自己刚加的那一条。** 用同一条接口或对应删除口清掉，别留脏数据。  
3. **不要**去改别人已有订单、改别人密码/角色、删别人原来的地址/券/商品。那些删不干净，也不是最小证伪。  
4. 现场没有创建口、只有改/删现成对象：改一个**自己能改回去**的测试字段，打一次就停。改密 / 改角色 / 改绑按 `dig-scope` §4.2.2 和 `authbypass-test.md` **可探**（拿掉旧验看过不过）；过了立刻改回；改不回就停在回包，不要把别人的密、角色、邮箱留下。  
5. 要证资损：只动**自己能改回去的**测试金额/状态，打一次立刻改回。不要真转走生产资金、不要清别人库存、不要把别人已有单改到收不回。**不是**支付/写 IDOR 不测。禁止批量、禁止对着生产主数据试删。本段是最小伤害，**不是**「写 IDOR / 接管 / 加字段不测」。

## 概念区分

- **水平越权**: 同权限级别，访问他人资源（A 访问 B 的订单）
- **垂直越权**: 低权限账户调用高权限接口（普通用户调管理员 API）
- **未授权访问**: 未登录直接访问需认证接口

---

## 测试流程

### Step 1: 找到用户标识参数

在以下位置寻找用户/资源标识：

```
URL 路径:    /api/user/12345/info
URL 参数:    ?userId=12345&orderId=ABC
请求体:      {"uid": 12345, "target_id": "user_abc"}
响应体中:    {"id": 12345, "created_by": 67890}  ← 收集这些 ID
```

**高价值接口特征**:
- 包含 `user`, `account`, `profile`, `order`, `bill`, `message` 的路径
- 响应包含他人的手机号、邮箱、真实姓名、身份证
- 修改类接口: `update`, `edit`, `delete`, `change`

### Step 2: 对象 id（有两号更好，没有也不磨注册）

```
有对照号：账号 A 登录拿 token，账号 B 的资源 ID 做对照。
单号 / 没号：用列表、回包、邻号、`0`/`-1`/空当他人 id（`dig-scope` §4.2.3）。禁止为凑第二号去磨注册。
```

### Step 3: 替换标识符测试

```python
# 水平越权测试示例
# 用 A 的 token 访问 B 的资源

import requests

token_a = "Bearer eyJ..."
victim_user_id = "B的用户ID"

# 原始请求（访问自己）
r1 = requests.get(
    "https://target.com/api/user/info",
    params={"userId": "A的ID"},
    headers={"Authorization": token_a}
)

# 越权请求（访问 B）
r2 = requests.get(
    "https://target.com/api/user/info",
    params={"userId": victim_user_id},
    headers={"Authorization": token_a}
)

# 对比响应
print("自己:", r1.json())
print("他人:", r2.json())
# 若 r2 成功返回 B 的数据 → 水平越权
```

### Step 4: 垂直越权测试

```bash
# 用普通用户 token 调用管理员接口
# 先在管理员账号下抓包找到管理接口
ADMIN_ENDPOINT="https://target.com/api/admin/users/list"
USER_TOKEN="普通用户的token"

curl -X GET "$ADMIN_ENDPOINT" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json"

# 200 成功 → 垂直越权
# 403 Forbidden → 有权限控制
```

### Step 5: 未授权访问测试

```bash
# 去掉 Authorization 头直接请求
curl -X GET "https://target.com/api/user/info?userId=12345"

# 或替换为无效 token
curl -X GET "https://target.com/api/user/info?userId=12345" \
  -H "Authorization: Bearer invalid_token_123"
```

---

## 常见绕过技巧

### ID 猜测
```
数字 ID: 尝试 ±1, ±10, 0, -1, 999999
列表上的租户/应用字段：换别人真实 ID 拦了，再试 0 / -1 / 空 / 不传（见下「哨兵租户」）
UUID: 可能从响应或 JS 中获取其他用户 UUID
手机号: 某些接口直接用手机号做标识
```

### 参数污染
```
# 同名参数多次提交
POST /api/user/info?userId=A_ID&userId=B_ID
POST body: userId=A_ID   + URL: ?userId=B_ID
```

### 路径遍历
```
/api/user/A_ID/orders  →  /api/user/B_ID/orders
/api/order/123         →  /api/order/124, 125...
```

### 编码绕过
```
userId=12345         → userId=0x3039（16进制）
userId=12345         → userId=%31%32%33%34%35（URL编码）
```

---

## PoC 证据收集

```
必须保存:
1. 请求完整内容（含 token、headers）
2. 响应完整内容（含受害者数据）
3. 对比截图（A 的正常响应 vs 越权响应）
4. 受害账号 ID 和数据的对应关系证明
```

---

### 密文 ID（短表有指针）

认：改/查地址、订单、资料的请求 id 是一长串密文；回包或列表里能看到明文小数字；JS 有 `RSAUtils` / `JSEncrypt` / `security.js`，或写死 `modulus` + `exponent`。

打：密文不是墙。公钥谁都能用。

1. 先打自己的那条，对照回包里的明文 id  
2. 用同一套前端加密，把相邻数字（明文±1、再往两边走）自己加密  
3. 只换这个密文 id，会话不动  

算成：换过去之后出**别人**的姓名/手机/地址。只证明自己的密文能解回自己的明文 → 不算。

假点：服务端按会话过滤，加密对了也只回自己的；那把钥匙是验签用的，乱加密直接拒。单站没中不删短表这行。钥的实值、某站 `security.js` 链接不进库，现场从当前站 JS 抄。

### 哨兵租户（短表有指针）

认：工单 / 资质 / 会话列表带 `appId`、`tenantId`、`orgId`；回包里有附件 URL（`sign` + `file_name` + 又一个租户字段）。

打（换真实他 ID 是已有水平越权，这里多一枪）：

1. 租户字段试 `0`、`-1`、空串、不传 —— 后端常把哨兵值当成「不过滤」  
2. `total` / 条数相对本租户基线暴涨才算打穿；加大 `pageSize`、翻 `page` 只是把量拉全，**不是洞**  
3. 类型字段（`serviceType` 一类）同样试 `0` / `-1`，可能切到另一套业务单  
4. 列表里的带签下载：原样打开常 403；**`sign` / `file_name` 不动，只把 URL 里的租户改成自己的**（签名没罩住这个字段）。正文在 `file-upload-test.md` 预签名那段  

算成：列表出现他租户的工单正文 / 客服对话，或附件真下到对方证照。主体是跨租户业务读。

假点：`0` 仍只回自己的；换租户下载 403；只有文件通道、没有业务列表。哨兵值没中 → 不删这行，下一站列表过滤照样试。

### 制品库 catalog 不过滤租户（短表有指针）

认：有容器 / 云函数 / 小程序云；能 `docker login` 或看到 Registry / Harbor / `/v2/`。

打：这是列表越权，不是未授权下文件。

1. 用**自己的号**登录制品库  
2. `GET /v2/_catalog`（Harbor 还有项目列表一类接口，同一枪）  
3. 回包仓库名对得上**别人租户** → 再 `/v2/<仓库>/tags/list`，`docker pull` 其中一个  

算成：catalog 里是他租户的私有仓，并且镜像能拉下来（层里是对方的应用/配置）。只证明自己能 login、只能拉官方公开镜像 → 不算。

假点：catalog 只回自己的；列出名字但 pull 403；本来就是公开库。单站没中不删短表这行。

和匿名 fileId 下载、和 OSS 桶策略全开都不是一条：本条是 **登录后 catalog 把全站私有仓交出来**。

### 登录前缀双胞胎（短表有指针）

认：业务 H5 把登录 RPC 写在 `/fapi/d/`（或同类需登录前缀）；同网关另有 `/fapi/n/`（或 n / unlogin / guest）未登录前缀。只打 JS 里的 d 口会看起来整段请登录。

打（不登录）：

1. 对照 d 口应请登录 / 要票  
2. 把 path 里的 `d` 改成 `n`，同一 RPC、同一对象 id 再打  
3. 换邻号；回包证件照 URL 跟着打开  

算成：不登录出他人身份证 / 证件照 / 手机。

假点：n 仍请登录；n 只有公开配置；只有自己刚交的补件。单站没中不删短表这行。

### 数字 RPC 邻 cmd（短表有指针）

认：业务页 JS 调数字 RPC 网关（`/data/{数字}/forward` 一类）。页面往往只写死一个 cmd（发票/广告一类）。邻号可能是另一套内部列表/写口，不要停在写死的那一个。`数字 RPC 网关` 是常见皮，没有这名仍打。

打（不登录）：

1. 抄 JS 里的 forward 地址，把 path 里的数字换成邻号  
2. 先空 `{"req":{}}` 看列表；对照写死 cmd 应是另一套业务  
3. 列表出内部记录再带 id 打邻写口（先假 id / 已空 id）。过了能改回的改回  

算成：内部发布/操作人正文，或字段被改。

假点：邻号仍是同一套公开接口；只出公开软件目录。密钥实值不进库。单站没中不删短表这行。

### 身份域账号 CRUD（短表有指针）

认：同产品业务前端的账号 CRUD 口回登录闸（`AuthFailure.NoLogin`）；另有独立 `*-ids` 身份子域，同一套 `/api/ms-account/` 不要 Cookie。GET list 可能 404，POST 才出数。

打（不登录）：

1. 业务前端同一条 list 对照应登录闸  
2. 改打 ids 子域 `POST /api/ms-account/user/list`，空包/`Offset+Limit` 都试  
3. 通了再 `generate` → `reset-pwd` 只带 Id（不带 Id 应参数错误）→ `delete` 清探测号  

算成：名单里是手机/企业邮箱；能建号、不验旧密改密、按 Ids 删。

假点：只打了业务前端就当没洞；list 出数但 Mail/Phone 全空且写口也闸。单站没中不删短表这行。

### 列表过滤详情不闸（短表有指针）

认：公开列表只出上架/公开；详情、hidden、tab、预览/导出用同一个业务 id。或列表有可见性查询参，默认把隐藏滤掉。或列表要登录/空包，详情只要数字 id + 业务键（workid 一类）。或对外详情把联系人/手机置空，审核/approval 详情用同一个业务 id。或文档 CMS 前端写死公开 `area` / `端` / `channel`，API 不鉴权。或对外搜索口有素材类型参（`materialType` / `tab`）默认公开值。或入驻/审核 query 只带业务 id 默认空壳。或同产品全量列表口不带可见性参，行里就带着 unpublished/offline 详情正文，公开搜索和橱窗详情仍闸。或浏览页/目录写访客请登录，同站 search API 未登录仍出内部/专有文档正文。