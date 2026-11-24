---
name: playbook-src-authbypass-test-part3
description: "authbypass-authentication-flaws（第 3/3 部分）"
tags: [playbook, src-hunting, authbypass, test]
intent: offensive
---
打（不登录）：有演示号抄客户编号再打领钥 / 建任务 / 联合身份。没有演示号也打领钥口：缺参报字段校验别当登录闸。空 body 报缺 `AppId`/`Uin` 也别当没口，带能解析的数字 Uin 就可能发联邦 STS；bucket+file_name 仍打。领钥口可能在独立 demo CDN 的 `/openapi/`（页面 JS 跟过去）。分享域前端 HMAC 过网关后，对照其它口「token 不合法 / Token校验失败」，`/share/token/info` 一类领 STS 口不带分享页 token 仍打。换 bucket 用 PUT/NoSuchBucket 探活桶。钥拿去 GetCallerIdentity 对主账号；有启动、检索一类 Action 再签（余量不够别停，看能不能问出账号或出账单）。Epaas 过 HMAC `SC-SIGN` 后对照名单仍 UserNotLogin，专找 GetUploadURL/FileName 不校验 token（过签≠出数）。

算成：临时钥匙能问出 AccountId/角色名；同一身份下列表出现刚建的任务；或检索出口径出账单/实例/他主体业务字段；或活桶 PUT 200。

假点：钥匙调业务 API 全 Unauthorized；领钥口请登录/401；create 200 但列表没有。没有演示号/客户编号不是假点。单站没中不删短表这行。密钥实值不进库。

### 未登录签发合作方登录链（短表有指针）

认：电子合同 / 供应商门户人打开是登录页，框里只填可枚举合作方数字 id（partnerId / supplierId 一类），没有密码。未登录 `getUrl`（或同类签发登录链）直接回 `partnerId`+`generate`+`code`。旁边有 `setCookies` / 换票口吃这三项下会话。

打（不登录）：

1. 登录页 JS 抄签发口，合作方 id 用页上提示的数字或邻号  
2. 把回包三项 POST 给 setCookies / 换票口，抄 Set-Cookie  
3. 带会话打合同列表 / 支付进件 / 身份口。对照：不带 Cookie 应未登录；不存在的合作方 id 应查空  

算成：身份口登录成功，或能当这个号读未公开合同。

假点：签发口要已登录；code 必须从邮件点开；setCookies 不下会话；只出公开招商页。单站没中不删短表这行。密钥实值、某次 Cookie 不进库。

### 未登录合作方直连配置查询（短表有指针）

认：运营台 / 供应链前端把供应商直连配置查询挂成未登录。body 只有可枚举合作方数字 id（partnerId 一类），不要 Cookie。回包 `partnerInfos`（或同类）里带 clientId+clientSecret。

打（不登录）：

1. 运营台 JS 抄查询口（getV2 / tech/partner 一类）
2. partnerId 用邻号。对照：未配置的 id 应回查空/未配置，不是登录闸
3. 算出是完整 clientSecret，不是空成功码

算成：回包里是完整 clientId+clientSecret，能当这把直连钥用。

假点：接口已下线；clientSecret 空或占位；必须登录；只出公司名没有钥。单站没中不删短表这行。密钥实值不进库。

### 未登录签发回跳外域（短表有指针）

认：未登录签发 SSO / 回跳；callback 只判断字符串里有没有官方 host，或不校验、任意外域也能签。

打（不登录）：callback/redirect 填外域：夹官方 host（query/子域/userinfo）和**不夹**都试。回包有票/签再交给自家 SSO。通行证页看会不会 `location.replace(回跳+"?"+token名+"="+session)`。对照：不含官方字应被改空或拦。必须真机登录才出票且前端不会把 token 拼进回跳 → 半条，停在签单，不算打穿。

算成：SSO 成功且回跳仍是外域；或登完通行证出现在外域 query。

假点：callback 被改空；SSO 缺回调参数。单站没中不删短表这行。

### 缺参字段改头换票（短表有指针）

认：未登录业务口 JSON 报「缺少 xxx」；query/body 带了仍报缺少。或 Spring 400 HTML/`message` 写 `Required String parameter 'os' is not present` 一类，点名的是 **Cookie 名** 不是 query。

打：把这个字段放到 **HTTP 头**再打 SSO/换票/login。头没吃别停，改打 Cookie（设备 `uuid`/`os`/`platform` 一类自己编）。未登录写口（绑店/改状态）同样试，不要只打换票。对照：只放 query 应仍报缺少。

算成：出别人的登录票/姓名；或写上他人门店/对象。

假点：query 有该字段仍报缺少（头和 Cookie 都没吃）不算过；只出自己的票。单站没中不删短表这行。

### SSO 壳后面的账密登录口（短表有指针）

认：管理后台人打开只跳 SSO / 统一认证，页面没有账密框；后端仍暴露账密登录 API。别把 SSO 壳当成整摊没登录口。常见后台骨架 `/admin-api/system/auth/login` 是常见皮，没有这名仍打。

打（不登录）：

1. POST `/admin-api/system/auth/login`，body `{"username":"admin","password":"123456"}`（不要去磨 SSO 验证码）
2. 把 `data.accessToken` 放到 `Authorization: Bearer` 打 `/admin-api/system/tenant/page` 或 `/system/user/page`
3. 对照：错密应失败；默认口出 `userId=1` 一类平台票

算成：进了平台管理员号，且租户/用户列表出他人手机/姓名。

假点：登录口 404 或默认口已改；票只能进空租户没有联系人。密钥实值、某次 Token 不进库。单站没中不删短表这行。

### 刷新票空包发管理员票（短表有指针）

认：JSON 网关有刷新票口（`/api/auth/refresh` 一类）。不登录、空包 `{}` 就发票，票里身份是 `admin`（不是游客）。管理后台和前台走同一套云开发/网关。

打（不登录）：POST 刷新口；把 `data.token` 放到 `Authorization: Bearer` 打 `/api/admin/me/permissions` 和申请/用户名单。对照：不带这张票应未登录。

算成：me 是超级管理员（权限 `*`），或名单出现**他人**手机/姓名。

假点：只出游客/过期票；refresh 必须带旧 refresh token；me 仍是自己。密钥实值不进库。单站没中不删短表这行。

### 空 openId 进已有号（短表有指针）

认：小程序 / H5 的 GET 登录口吃 `openId`（`loginByOpenId` 一类）。缺参报缺字段，假 openId 登录失败，空串仍 200 出**已有商家号**和手机，不是游客空号。

打（不登录）：

1. 缺参一枪，确认这是登录口  
2. 假值一枪，应失败  
3. `openId=` 空串再打；GET query 没吃再试 POST JSON `{"openId":""}`  
4. 对照：空串出的店名/登录手机，和假值失败、缺参报缺，不是同一套游客号  

算成：进已有商家号且出手机（能当这个号用）。

假点：空串只出游客空号。密钥实值、某次 Cookie 不进库。单站没中不删短表这行。


## PHP 后台 Proxy 层无鉴权（短表有指针）

认：自研 PHP（`action.php?c=X&a=Y` 路由）管理后台：管理页匿名直开不跳登录；数据走 `XxxProxy&a=get*List`；框架无全局会话中间件，Proxy 层裸奔。

打（不登录）：
1. 直开 `?c=User`、`?c=Score`、`?c=Role` 类管理页，能渲染出表格壳即页面层裸。
2. 页内联 JS 抽 `XxxProxy&a=get*List`（GET，`pageNumber/pageSize`，pageSize 放大翻页）→ 整表 PII。
3. 写口（add/update/delete）带**不存在的对象 id** 打对照：回业务校验文案（「学校id不能为空」类）= 写口无鉴权；回登录跳转/「请登录」= 写口有闸。**别动真实数据**；自建测试对象走「先加→删自己那条」。

算成：整表姓名+手机（或成绩/角色）直接出；写口回业务错不回登录错。
假点：列表/写口回登录跳转；页面能开但 Proxy 口要会话；只有公开运营数据。

## 空凭证直发超管会话（短表有指针）

认：Java 后台登录接口不校验凭证明细：空 body / 任意 loginName+password → `stateCode` 602「登录成功」+ **完整用户对象（含密码哈希）** + Set-Cookie；同接口另有 `vlogin` 会话验证口。

打：
1. POST 登录口（参数名以前端 login js 为准，常见 `loginName/password`），空密/假密各打一枪对照——都 602 即凭证零校验。
2. 回包 Cookie 打 `vlogin`：602 = 真会话（前端 602 就跳 main）。
3. 按前端 controller 抽 `*/list`（**GET**；405=已过鉴权只是方法错）读账号表/员工通讯录/短信日志/审计日志；审计里出现自己这次登录 = 会话实锤。
4. 写口（save/resetPwd/delete）会话下可达即写进危害，**不真改**线上数据。

算成：vlogin 602 + 至少一张 list 出数（账号哈希/手机邮箱/短信日志任一）+审计对照。
假点：vlogin 回未登录；list 回登录码；只有用户对象回包但会话不被 vlogin 认（半条链不写）。

## 拦截器白名单漏前台业务写口（短表有指针）

**形态**：Spring 单体前后台同 context，有一张全局 302→登录页的拦截器，但它是**白名单式**（放行前台页面用的口）。前台白名单里混着真正的业务写口：申领提交、退款、临时上传、支付查询。视图口全部 302 会让人以为整站都有闸。

**打法**：
1. 从前端 config/urlSet JS 抽全量端点（注意 `httpName+"xxx/yyy"` 这种不带前导斜杠的写法）。
2. 全端点 GET+POST 空参双探，按回包分类：`302`=拦截器罩住；`405`=方法不匹配但**已过拦截器**（映射期抛出，不证明有闸）；`500` 带堆栈=匿名直达业务层（堆栈即执行证明，还白送类名/包名/行号）。
3. 对 500 的 POST 口，按前端 JS 的表单字段拼全参（敏感字段可能是前端 AES，密钥/IV 就在页面隐藏域里，静态硬编码）。
4. create 口成功后带返回单号重放=改写口；同唯一键二次提交被查重拒绝的报错本身也是"可占位阻断真实用户"的影响证据。
5. 反例要记：有的写口深层校验真实外键（personId 必须真实存在），空参 NPE 递进假值仍 500 → 停，如实记"需真实外键"。

**过门槛**：匿名落库拿到业务单号，且带单号重放可改写他人可见数据结构。
**不过**：写口回登录码/短信码；参数全齐仍 411；只读不写。

## 验证码明文回读链（短表有指针）

**形态**：短信验证码体系里存在「查询验证码状态」接口，回包除了 expire 还带 yzm 明文；或发码接口直接回传验证码。配合无图形码的发码口，验证码闸整体失效。

**打法**：
1. 发码口只传 phone 试发（本人号码），看有无图形码/频控前置。
2. 打 queryYzm 类口：未发码时 expire=-2，发码后 expire>0 且 yzm=明文 → 中。
3. 用码打全部 *ByPhone 闸口（queryByPhone/newQueryByPhone/scorebyphone/certuser…），参数名靠 Spring 缺参报错点名（Required String parameter 'bmrsjhyzm'），body 形态照前端 axios 包装判（有 json header 走 JSON，否则 qs form）。
4. 拿返回实体 id 打 *ById 口（常无码无鉴权）闭环取证。
5. 负向对照必做：错码/无码应被拒——证明校验存在、缺陷在明文回读，报告定性才稳。

**过门槛**：回读明文码 + 带码拉出本人完整报名实体（证件号/手机明文）或成绩。
**不过**：查询口只回 expire；带码口全拒；ByPhone 需登录态。


> 术语：`dig-scope` 指 `playbook-src-rule-dig-scope-workflow`。

> （续前：playbook-src-authbypass-test 第 3/3 部分）
