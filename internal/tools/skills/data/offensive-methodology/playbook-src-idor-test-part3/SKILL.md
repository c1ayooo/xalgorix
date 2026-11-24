---
name: playbook-src-idor-test-part3
description: "写不写只认 reportvulnerability 的字段与证据要求。进站有会话时最低探针见 dig-scope §4.2.3（对象图、换 id、哨兵值）；单号用列表/回包里的他人 id，不为第二号磨注册。默认先用读/列表差分证明跨用户·跨租户。写越权仍要测，但不是对着别人已有数据改/删。顺序见下「写越权怎么打」。禁止批量改删、禁止真资损。（第 3/4 部分）"
tags: [playbook, src-hunting, idor, test]
intent: offensive
---
### 自定义身份头当会话（短表有指针）

认：管理后台 SPA 请求拦截器把自定义头（`User-Id` / `employeeId` / `X-User-Id` 一类）当登录身份，不要 Cookie。未登录名单口往往只出数字员工/BD 号，没有手机。

打（不登录）：

1. 抄 JS 拦截器里的头名  
2. 未登录打名单（空参/总部筛）。只有数字号**别停**  
3. 把头换成这个号打当前人信息口。对照：不带头或填 `1` 应查空  

算成：出他人姓名+11 位手机。

假点：头过了仍空；名单已经出手机（那是另一条）。单站没中不删短表这行。

和「匿名 CSRF 头读详情」不是一条：那条是临时 token 头，这条是**身份数字号进头**。和「入驻 H5 空参出招商通讯录」也不是一条：那条类目空着整表出手机，这条名单没手机还要再把头当身份打一枪。

### 客户详情口还吃 phone（短表有指针）

认：CRM / 企业 IM 客户详情口前端只写了外部联系人 id（`externalUserId` / `contactId`）。只带这个 id 回空壳。后端还吃 `phone`，常还要 `tagShow` / `udfShow` 才出内部字段。

打（不登录）：

1. 对照只带外部联系人 id 应空壳  
2. body 加 11 位 `phone`，并把 `tagShow` / `udfShow`（或同类展开字段）打开  
3. 空号 / 乱填应空壳；换号必须换人  

算成：出他人姓名 / 公司 / 内部 UDF，且查的就是这个完整号。

假点：phone 一律空或只出同一条测试号。身份证槽位空不算假点。单站没中不删短表这行。

### 助手历史未授权读他人任务（短表有指针）

认：助手/Agent 前端有 `GetHistoryList`；登录态只在可选头里。不是对话口工具执行（那枪见 `agent-tool-exec-test.md`）。

打（不登录）：调列表，换 guid 对照是否同一批；再把 `session_id` 丢给 `GetHistory`。翻页 `last_ts`+`direct=back`。

算成：列表/详情出现**他人**任务原文（下载、订阅、对话卡片），不是公开广场。

假点：只出广场 `GetSquareTasks`/`share_id`；换 guid 列表变空或只剩自己的。单站没中不删短表这行。

### 写死 appKey 打业务表（短表有指针）

认：前端 `AV.init` / LeanCloud 写死 `appId`+`appKey`（或 `X-LC-Id`/`X-LC-Key`）；或 nocode/supabase 落地页写死 `role=anon` JWT。

打（不登录）：带这两头打 `/1.1/classes/*`：先 `_User` 对照应 403，再扫业务表 count/limit，能写就改探测字段再删回。supabase 带 `apikey`+`Authorization: Bearer` 打 `/rest/v1/` swagger 列出的表。**rest 表 403 / 只有公开运营配置别停**：改打 `POST /storage/v1/object/{桶}/{官方前缀}`，头 `x-upsert:true`（见 `file-upload-test.md` STS 第 9 步）。

算成：业务表 `count` 海量或出现**他人**稿/邮箱/电话；PUT 改别人 `objectId` 成功。

假点：`_User` 和业务表都 403；只能读自己刚建的；只能 LIST 公开运营配置。单站没中不删短表这行。密钥实值不进库。

### 填表模型带标准答案（短表有指针）

认：问卷/测验填表模型口；前端写死业务 id 或白名单（`FORM_WHITE_LISTS` / 演示 encryptFormId 一类）；回包 schema 带 `answer` 标准答案或内部审核题干/样图。不是公开报名表标题，也不是已经交过的答卷列表。

打（不登录）：

1. 从 JS 白名单/演示 id 打 getModel / schema / getFormModel，别停在表单标题  
2. schema 里的样图 preview/imgUrl 跟着打开  
3. 管理 list/export/fillList 另打；没有填写记录不要编答卷  

算成：内部测验正文+标准答案，或证件/执照样图真下到。

假点：只有公开报名表标题；schema 没有 answer；只有自己刚填的答卷。单站没中不删短表这行。

### 未授权内部话术正文（短表有指针）

认：客服/开发者支持台 umi 有 `getKnowledgeList.json` + `getKnowledgeInfo.json`；或大厅/帮助 HTML 详情口吃数字篇号；或对外公告 JSON（bulletin / getbulletin 一类）用 `callname` + `callcontent` 当 RPC，页面只调公开菜单（`getKnowledgeByMenuId`）；或未登录 CMS `siteList` / `contentList` / `content` 能列出非官网站点，且频道名带「内部知识库」；或客服/IT chatbot 未登录检索口（菜单 id + 模糊 searchText），正文在 `buttonList`/`searchList` 的 `behavior.value`，不在标题 `content`。不要只认 umi 那一套 json。

打（不登录）：对照 `queryUserInfo`/`queryFeedbackList` 应 deny。列表 `categoryId` 从 1 试，再把 `id` 丢给 Info。**没有 json 列表也打 HTML 详情**（`showKnowledgeInfo.htm?knowledgeId=` / `help_detail.htm?help_id=`），用现代 UA（IE 可能触 netd）。公告口：页面公开菜单对照条数很少时，把 `callname` 换成 `getKnowledgeList`（`callcontent` 带翻页），再 `getKnowledge` 打详情 id。CMS：先 `siteList` 抄非官网 siteId，再 `contentList` 看频道名，换 siteId 打 `content` 详情；默认官网 Banner 不是这枪。网关报 `loginMode is null` 别停，头加 `loginMode: 0`；siteList 仍缺 siteId 失败时，直接带内部 siteId 打 contentList。content 也要带 siteId，缺了会当没正文。chatbot：`chat_dir_id` 一类目录口 401 别停，改打检索口（`search_recommend` 一类），`searchText` 填常用字、`id` 填菜单号；正文看 `behavior.value`。

算成：列表 `pager.items` 上千或 count 海量，且 Info/HTML/详情/`behavior.value` 出**内部**话术/协查/短信/运营知识库正文，不是公开 FAQ / 对外协议。

假点：只有公开帮助稿/错误码/对外协议/官网 Banner；Info 只要标题；工单口也放行（那是另一条）；只打了默认官网站点；dir 节点 401 就停；检索口只出标题 content。单站没中不删短表这行。

### 开放支付假签枚举 appId（短表有指针）

认：开放支付 / 进件网关 body 有 `appId`+`sign`（可再加 `random`/`merchantId`）。假签时活应用回 `MERCHANT_NOT_EXIST` 或 `SUCCESS`，死应用回 `SIGN_ERROR` / `APP_NOT_FOUND`。不是「JS 写死盐自己算」那一行，也不是文档里抄出真 AppSecret。

打（不登录）：

1. `sign` 填一串假值（32 个 `a` 一类），扫 `appId`，对照回码  
2. 活的再打商户查询（`query` / `query/v2`）换 `merchantId`（文档示例号、邻号）  
3. 同一套假签再点签订/协议口，只看回包是否接受；不要批量签、不要改结算卡  

4. **ST / 演示收银台预下单**假签或 node 代签也能进生产网关时：不登录 POST `/api/precreate` 或 `/demoapi/precreate`，sign 填假值 + 活 appId + 他人 merchantId。本站查单 404 别停，打生产 `/api/pay/query`。

算成：出他人商户身份证 / 银行卡 / 手机；或生产查单 `ORDER_NEW` 且有他商户交易号。

假点：假签一律 `SIGN_ERROR`；`SUCCESS` 但证件卡号全空；文档示例 appId 已死；只回 `channelPayerNo` 且生产查单不存在；只能给演示店挂。单站没中不删短表这行。密钥实值不进库。

### 短链 302 query 带手机（短表有指针）

认：短信/运营短链解析站；猜中的短码 302 到落地页，query 明文带 `phone` / `name` / 金额。首页可能是 OpenResty 欢迎页、302 到品牌官网、或 `index.html` 只有 Hello，**不要只看 `/` 当整站证伪**。

打（不登录）：

1. `/open` `/app` `/s/` `/www` 加短字典（`aaaaa`、`1`、`1234`）  
2. 只看 302 目标 query，不要跟到落地业务域  
3. 对照：对不上的码应失效页、没有手机号  

算成：跳转地址里是**别人**的手机/姓名。

假点：失效页；公开营销无 PII；短码要真短信才解析。单站没中不删短表这行。首页 Welcome / 302 官网 / Hello 壳不是假点。

### 隐私号失败回真实号（短表有指针）

认：订单/门店/物流 H5 调隐私号或虚拟号（AXB）口；未登录只要业务 appid + 可遍历对象号。失败时回包把真实 11 位手机当下发，文案还写获取隐私号失败、将用真实号码拨打。不要只当虚拟号口丢掉。

打（不登录）：

1. 前端抄 functionId / 隐私号 path，对象号从 0、2、邻号打  
2. 对照：非数字对象号应参数错、没有手机  
3. 换号号码变才算批量  
4. 网关空 Origin 或本站 Origin 回 `cross-origin 403` **别停**，改业务域 Origin（订单 / 购物车 / H5 域，不钉某一家 host）再打  

算成：回包是**他人**真实手机。

假点：只出虚拟号/中间号；必须登录；换号号码不变；空 Origin 403 当没口。单站没中不删短表这行。

### 入驻 H5 空参出招商通讯录（短表有指针）

认：入驻 H5 有招商电话页；同套其它 settle 口 302 未登录。JS 里按品类查 BD 的 RPC（queryBd / bdInfo 一类），industry/类目空时 body 空。页面上可能只摆 400 热线。

打（不登录）：

1. 从招商电话页 JS 抄网关 `/api` 和 functionId  
2. 类目字段空着打（`body={}`）。对照：填死数字类目号常回空数组  
3. 同套其它 settle 口 302 别当整站没口  

算成：内部 BD 姓名+11 位手机+企业邮箱整表。

假点：类目填死数字出空数组就当没口；把页面公开 400 热线当这枪。单站没中不删短表这行。

### 入驻 H5 写死 token 换 pin（短表有指针）

认：入驻 H5 / 小程序打包 JS 把 OCR / 企业信息口 token 写死。JSON 口只要 pin（或同类账号）+ 非空 token，不要 Cookie。对照：token 留空或省略只回空 data。

打（不登录）：

1. 从入驻 chunk 抄写死 token 和企业信息 path  
2. 空 token 对照应空 data  
3. 带写死 token，pin 用 `admin` / `test` / `0` / 短账号再打  
4. 写死那串过了，再填任意非空串对照（过了说明不校验 token 内容）  

算成：换 pin 出对应真实手机。

假点：空 token 也出数（完全无鉴权，另一条）；token 过了仍只出自己刚入驻的。单站没中不删短表这行。密钥实值不进库。

### 公司名/抬头自动完成（短表有指针）

认：报名/入驻/发票抬头自动完成；页上下拉只出公司名；接口走工商/邓白氏 Match 或抬头 suggest。前端可能把该口标 `auth:true`，服务端仍不验登录。

打（不登录）：

1. POST 名称+国家码，或抬头口。suggest 字段可能是 `prefix`（填 `title` 会报「关键词不能为空」像没入口）  
2. suggest 报关键词空别停：同产品还有专票/工商补全口，字段就是 `title`，前端标 `auth:true` 仍打  
3. 对照页面下拉：页上只有公司名，回包多出手机/卡/住址才算  

算成：回包出现负责人或开票手机/住址/银行账号，且对得上人。

假点：下拉和接口都只有公司名；必须登录；公开企业名录没有电话；前端标 auth 就当没洞。单站没中不删短表这行。


### Mass Assignment / 隐藏可写字段（短表有指针）

认：注册 / 改资料 / 建用户的 JSON 比页面控件多；Swagger、管理员「建用户」或前端注释里多出 `role` / `isAdmin` / `verified` / `tenantId` / `balance` / `permissions`。

打：

> （续前：playbook-src-idor-test 第 3/4 部分）
