---
name: playbook-src-idor-test-part4
description: "写不写只认 reportvulnerability 的字段与证据要求。进站有会话时最低探针见 dig-scope §4.2.3（对象图、换 id、哨兵值）；单号用列表/回包里的他人 id，不为第二号磨注册。默认先用读/列表差分证明跨用户·跨租户。写越权仍要测，但不是对着别人已有数据改/删。顺序见下「写越权怎么打」。禁止批量改删、禁止真资损。（第 4/4 部分）"
tags: [playbook, src-hunting, idor, test]
intent: offensive
---
1. 对照管理员建用户 vs 自己注册/改资料，差出来的字段才塞。没有对照就从 Swagger / JS 抄。
2. 只对自己的号塞一次；过了立刻改回。不要改别人已有账号的角色。
3. `__proto__` / 嵌套 `user.role` 也试；这和 PP 模板 RCE 不是一条（PP 见 `prototype-pollution-test.md`）。

算成：自己号变成高权，或余额/认证状态真变。字段吃了、权限没变 → 没成。

假点：只能改展示名；公开运营开关；服务端白名单吞掉多余键。单站没中不删短表这行。没注册/改资料口不要发明字段。

## 12. QUICK IDOR CHECKLIST

```
□ 有对照号更好；单号用列表/回包/邻号，不为第二号磨注册
□ Map all API calls that contain object IDs (Burp History export filter)
□ Test all HTTP verbs on each endpoint（写：先 POST 添加，再删自己加的那条；勿删别人已有对象）
□ Test ID in all locations: path, body, header, query, cookie
□ Try sequential IDs (−1, +1 from your own）
□ 密文 id：JS 有公钥就自己加密相邻数字（见「密文 ID」）
□ 列表租户字段试 0 / -1 / 空（换真实他 ID 失败也试；见「哨兵租户」）
□ 有 Registry/`/v2/`：自己号打 `_catalog`，能否列出并 pull 他租户镜像（见「制品库 catalog」）
□ BaaS 匿名 session 后再 listRows，对照无 Cookie 的空表（见「匿名会话读报名表」）
□ 云开发匿名登录后再打 `lowcode-datasource` 的 `sys_user`/`wedaGetRecords`；HTTP 网关 `LOGIN_TYPE_DISABLED` 别停（见「云开发匿名用户表」）
□ 详情带 specifyUsers/openid：抄 openid 打信箱（见「详情抄 openid 再打信箱」）
□ 发布页 artifactMap 的 nodeId 打 pagechunk（见「资料库 nodeId 未授权读全文」）
□ 助手 GetHistoryList 可选头：不登录调列表/详情（见「助手历史未授权读他人任务」）
□ 前端写死 LeanCloud/supabase anon：打业务表（见「写死 appKey 打业务表」）
□ 知识列表+详情未登录出内部话术（见「未授权内部话术正文」）
□ 开放支付进件网关假签枚举 appId，活应用再换 merchantId（见「开放支付假签枚举 appId」）
□ 短信短链首页 Welcome / 302 官网 / Hello 壳别停，猜 /open /app /s/ /www 看 302 query 手机（见「短链 302 query 带手机」）
□ 未登录隐私号/虚拟号口失败时看是不是真下发真实手机，对象号可遍历（见「隐私号失败回真实号」）
□ 报名/入驻/发票抬头自动完成：不登录打 suggest（prefix）和专票/工商补全口（title）；前端标 auth 仍打（见「公司名/抬头自动完成」）
□ 入驻 H5 招商电话页：JS 查 BD 口类目空着打，不要填死数字（见「入驻 H5 空参出招商通讯录」）
□ 后台 SPA 把头 User-Id 当身份：名单只有数字号别停，把头换成这个号打当前人信息口（见「自定义身份头当会话」）
□ 浏览页/目录写访客请登录别停，同站 search（Keyword+Page）未登录也打，跟详情 URL（见「列表过滤详情不闸」第 15 步）
□ 目录请登录才能下别停，同站文件列表口+权限口 PreviewUrl 未登录也打（见「列表过滤详情不闸」第 17 步）
□ 对外搜索口 `materialType`/`tab` 改成内部类型；path 缺 v2 报 auth failed 别停（见「列表过滤详情不闸」第 9 步）
□ 入驻/审核 query 只带业务 id 出空壳时加审核状态=已通过，回包 uid 跟邮箱口（见「列表过滤详情不闸」第 10 步）
□ 文档站公开 itemList 只有对外产品别停，item 纯数字打详情/page（见「列表过滤详情不闸」第 19 步）
□ 收集表/问卷填报详情 relative 挂答卷 sheet 别停，不登录打答卷表（见「列表过滤详情不闸」第 20 步）
□ supabase anon JWT：rest 表之外打 Storage REST + x-upsert 盖官方前缀（见 file-upload STS 第 9 步）
□ Try UUIDs/GUIDs collected from your own account data
□ Test sub-resources (attachments, comments, transactions)
□ Test admin endpoints directly (BFLA)
□ Test POST/PUT body for extra fields (mass assignment)
□ Compare JSON response field count vs documented fields (hidden fields)
□ Test state/status：只改自己的单 / 能改回去的测试字段
□ 商家绑促销/券：改 productId 挂到别人的货，C 端价掉才算（见 logic-test.md §1.4）
```

---


商家绑促销/券到 SKU：只校验券归属、不校验商品归属 → 自己的券 + 别人的 productId，C 端价掉才算。正文在 logic-test.md §1.4。

## 公示目录接口吐整行含附件（短表有指针）

认：名录/目录/成员列表类公开接口（无鉴权、pageSize 可放大），回包给的是数据库整行，除公示列外还有文件路径字段（常见 bmbfj/attachment/fileUrl/icon）与电话、法人等列。

打：
1. 不登录带 pageSize=500 分页拉满，count 对账（防止漏页）；
2. 全字段扫描：统计文件类字段非空行、正则 `1[3-9]\d{9}` 电话行；
3. 文件相对路径拼 imagePre/图床基址（app.js 的 imagePre、或回包里绝对 URL），URL 编码后直接 GET，验魔数（jpeg/pdf/zip）确认是本体不是占位；
4. 拿到样本即可成，剩余靠字段统计量化（N份附件、M条电话）。

算成：下载到盖章件/报名表等材料本体，或批量导出电话+姓名。
假点：附件404/需签名鉴权；字段全为占位值（如0000@qq.com）；接口本就只返回公示列无文件字段。


> 术语：`dig-scope` 指 `playbook-src-rule-dig-scope-workflow`。

> （续前：playbook-src-idor-test 第 4/4 部分）
