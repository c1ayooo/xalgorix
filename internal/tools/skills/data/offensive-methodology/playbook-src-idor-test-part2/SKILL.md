---
name: playbook-src-idor-test-part2
description: "写不写只认 reportvulnerability 的字段与证据要求。进站有会话时最低探针见 dig-scope §4.2.3（对象图、换 id、哨兵值）；单号用列表/回包里的他人 id，不为第二号磨注册。默认先用读/列表差分证明跨用户·跨租户。写越权仍要测，但不是对着别人已有数据改/删。顺序见下「写越权怎么打」。禁止批量改删、禁止真资损。（第 2/4 部分）"
tags: [playbook, src-hunting, idor, test]
intent: offensive
---
打（不登录）：

1. 记下公开列表 id；列表没有的 id 丢给详情 / hidden / tab；详情 403 再跟预览 / 导出  
2. 列表口自己加 `status=hidden` / `all` / `private`（或同类可见性参）。对照：不带参列表空或只有公开；同一 id 打详情仍「没有权限」也别停——闸可能只套在详情上  
3. **这条列表口 401 / 要登录 / 空包别停。** 换同产品另一条 guest 查询，看回包 `hidden` / `is_public` / `isHidden`。公开 contents 404 再打 hidden/contents 同一 id。详情只要数字 id + 业务键仍打。`is_secret=1` 仍出全文别停在标题。  
4. **详情 200 且 JSON 已写尚未发布 / 要登录 / `canAnswer=false`，别停。** 看同包 `savedConfigDraft` / 同类草稿字段有没有未发布正文。拦截文案和草稿可以在同一份 JSON。  
5. **对外详情 contact / mobile / email 置空别停。** 同一 id 再打审核 / approval / audit 详情；闸可能只套在对外展示口。  
6. **作品/项目有 `period=edit|publish`（或同类状态参）别停在 publish。** 不登录打内容口 `period=edit`。对照：同一 id 的 publish 说不存在/已删除，info 挡「没权读别人的」。edit 仍出正文才算。  
7. **JSON 列表筛选项 null / 缺字段报 Unknown / 参数错误，别停。** 把该字段改成空数组 `[]` 再打。后端常把空数组当成「不过滤」，匿名出下架 / 全表；缺参或 null 反而拦。对照：`null` 或删掉该键应报错或空，`[]` 才出已下架正文。
8. **文档 CMS 前端写死公开 `area` / `端` / `channel`，别停在默认值。** 未登录把参改成内部区（常见 1 vs 2、oa vs public）。对照：默认公开列表没有的接入/加固/内网算法正文，改参后列表或详情出来才算。  
9. **对外搜索口有 `materialType`（或同类素材类型）别停在前端默认公开值。** 对照公开规范/橱窗类型，改成内部市场素材、营销规范一类。path 缺 `/v2/` 报 `auth failed` 别停，按前端替换规则补上再打。算成：SSO 墙后、`isLimitAuth=1` 的内部物料名单/文件直链。  
10. **自助入驻/审核 query 只带门店号/业务 id 出空壳别停。** 加上审核状态=已通过（`baseInfoStatus` 一类）再打。回包 `uid` 立刻跟邮箱/联系人口。对照：不带状态应空壳或没手机。  

11. **证照图 JSON 没有 idNo 别停。** 详情出商标注册证 / 执照扫描件就打开看：注册人行常把身份证号拼在姓名后面。图上对得上人就算，不要只扫 JSON 字段。  
12. **导出任务列表 `pageNum=0` 空别当没数据。** 改 `pageNum=1` 再打，常能出全站别人的导出。抄 `fileName` / key 打 download。
13. **详情 telephone / 手机字段打码别停。** 未发布详情的 `requirement` / 备注 / 自由文本里常把完整 11 位手机写进去，接口只剥了结构化字段。
14. **全量列表口不带可见性参也打。** 行里直接看 unpublished / offline / rejected 详情正文。对照：公开搜索该 id total=0、人打开的橱窗详情说不存在。别只打公开搜索和详情页就当没洞。
15. **浏览页/目录写访客请登录别停。** 同站 search API（Keyword+Page+PageSize）未登录仍可能出内部/专有文档正文和详情 URL。详情页 JSON `isLogined=false` 仍出全文别停。对照：人打开的目录页写着访客请登录才能看。
16. **详情口已补 / 项目不存在别停。** 同站 visit-log / 埋点 / doc-add-visit-log 只要数字篇号仍可能出 OA/内部全文。对照：原详情口同一业务 id 应已拦。
17. **目录写访客请登录才能看/下别停。** 同站文件列表口（`getHomeCosFileList` 一类，`solution_id`+`version_id`+`folder_id=0`）未登录仍可能出培训视频/白皮书/手册名单。列表 `file_id` 丢权限口（`getFilePrivilege`）看 `PreviewUrl` / 带签下载。对照：人打开的目录页写着访客请登录才能看、才能下。
18. **内容 SPA 按 `location.host` 正则选 Color/网关域，别打错 TLD。** JS 里常见 `.*\\.jd\\.hk` → `内容网关域`；打成 `打错的内容网关域` 会当没口。打法仍是不登录楼层 queryContents + previewContentDetail。对照公开详情应不存在/1414。
19. **文档站公开 itemList / 目录只出对外产品别停。** 未登录把 `item`（或同类项目号）改成纯数字自增打详情/page。正文可能是压缩 markdown，按前端同一套解开。对照：公开 itemList 没有的「对内/未对外/不对外」库才算。
20. **收集表/问卷填报详情 JSON 的 relative / 关联表挂着答卷 sheet 别停。** 人打开的是填报页，答卷表可能另有 id。不登录打答卷表详情/opendoc。对照：填报页只是题目，答卷表才出身份证/手机单元格。
21. **内容预览口只要数字 contentId，回包 `isDelete=1` / 已删除 / 未发布别停。** 对照公开橱窗应没有这篇。出整页 H5 或编辑器 JSON 才算，不要只看标题。

算成：未公开业务正文从列表出来（不是公开橱窗标题）；或他主体手机 / 证件；或证照图注册人行印着身份证号；或未发布/已下架作品源码正文。

假点：加了可见性参仍只出公开稿；只有标题没有正文；列表 401 就当没洞；edit 和 publish 出同一份已上架正文；空数组和缺参出同一份上架稿；`materialType` 仍只出公开橱窗；只带业务 id 出空壳就当没洞、没再加审核状态；只打了公开搜索/橱窗详情就当没洞；浏览页写请登录就当没洞；数字 item 仍是那几本公开文档。打错 Color 域当没口不是假点。单站没中不删短表这行。

### 过网关无需鉴权头（短表有指针）

认：物流 / 开放网关（`lop-proxy` 一类）前端 JS 写死 `Auth-Control: no-auth` + `LOP-DN`（业务域）。其它业务口请登录，带着这组头名单/运单口仍出数。

打（不登录）：

1. 抄 JS 里的 `LOP-DN` 和 `Auth-Control: no-auth`（或同类过网关鉴权头）  
2. 空翻页打站点/名单口；再换可枚举 `siteId` 打运单/详情  
3. 对照：不带头应请登录或 Host 未注册  

算成：出他人手机 / 住址 / 站点联系人，不是公开轨迹。

假点：头过了业务仍请登录；只有公开轨迹没有 PII。单站没中不删短表这行。

### 匿名会话读报名表（短表有指针）

认：BaaS 匿名 session；列表口不验管理员。页面写死几个管理员 ID，后台跳登录。Appwrite / Weavefox / TablesDB 是常见皮，没有这名仍打。前端常见 `Client().setEndpoint`、`X-Appwrite-Project`、`POST /v1/account/sessions/anonymous`；报名/活动表走 `tablesDB.listRows`。

打（不登录、不要管理员号）：

1. 无 Cookie 先打 `GET /v1/tablesdb/{db}/tables/{table}/rows` —— 基线应是 `total=0` / 空 rows（系统其实会拦游客）  
2. `POST /v1/account/sessions/anonymous` body `{}`，头带同一 `X-Appwrite-Project`，拿 `Set-Cookie: a_session_…`  
3. 带着这条匿名会话再打同一 listRows  

算成：匿名会话后 `total` 相对空会话暴涨，rows 里是**别人**的手机 / 邮箱 / 报名正文。管理员白名单只写在浏览器里、没套到列表口。

假点：无会话已经是全表（那是完全无鉴权，另记）；匿名会话仍空或只有自己刚填的一条；DELETE 别人行 401 只说明写没开，读仍算；公开运营展示名单。单站没中不删短表这行。

和「无 Project key、连匿名会话都不用就能 CRUD」不是同一枪：本条关键是 **匿名 session 被当成已登录，且不验管理员**。

### 云开发匿名用户表（短表有指针）

认：云开发开了匿名登录；有低代码数据源函数。控制台或前端能抄到环境 id。不是短表已有的 proxy `targetUrl`，也不是 BaaS `sessions/anonymous`。

打（不登录）：

1. `POST https://{envId}.api.云开发网关/auth/v1/signin/anonymously`，头 `x-device-id`，body `{}`，拿 JWT  
2. `POST /v1/functions/lowcode-datasource`，`Authorization: Bearer` 那张票，body `dataSourceName=sys_user`、`methodName=wedaGetRecords`（`getRecords` 同源）  
3. 对照：同一网关 `GET /auth/v1/user/query` 应拦匿名；数据源名 `users` 常行权限失败，**不要停**，`sys_user` 才是系统用户表  
4. HTTP 网关（常见 `/web?env=`）回 `LOGIN_TYPE_DISABLED` **别停**：改 `POST /web?env=`，`auth.signInAnonymously` → `auth.getUserInfo` 拿 `jwt;expire`（**不要去掉分号后缀**）→ `functions.invokeFunction`，同样打 `sys_user` / `wedaGetRecords`  
5. 旧 HS256 票丢 `/v1/functions` 报 `KID_INVALID` → 这枪没成，换第 4 步网关票，不要当「数据源不存在」  
6. `sys_user` 行权限失败 **别停**：同一张 `/web` 匿名票改 `database.countDocument` / `database.queryDocument`，`collectionName=users`（互联登录落库的用户表，不是数据源名 `users`）  

算成：records 里是**他人**手机 / 邮箱 / uin / 超管标记，不是自己刚匿名建的空号。

假点：行权限拒匿名且 `/web` users 集合也空；数据源不存在；只出演示 todo/sales；旧 HS256 票 `KID_INVALID` 当没洞。HTTP 网关 `LOGIN_TYPE_DISABLED` 不是假点。数据源名 `users` 失败不算假点。单站没中不删短表这行。密钥实值、某次 JWT 不进库。

### 详情抄 openid 再打信箱（短表有指针）

认：招募/指定用户详情带 `specifyUsers` 或 openid；H5 写死 SHA256 请求签名；或邀请页地址已有 `?openid=`。

打（不登录）：

1. 调详情抄 openid  
2. 钥算 Signature 再打 `/msg/message/list/`  
3. URL 上已有 openid 则再打无签名的 `get_invite_code` / `count` / `detail`，名单 `uid` 可再填回去  

算成：换 openid 信箱或邀请码/名单变，出现对方积分过期通知或打码手机昵称。

假点：列表人人一样只是全站广播；接口要真登录/签名；假 openid 没有邀请码；公开运营名单。单站没中不删短表这行。

### 资料库 nodeId 未授权读全文（短表有指针）

认：发布页 conversation-data 的 artifactMap 有资料库 `nodeId`；主站 `/space/d/` 要登录。

打（不登录）：打发布域 `POST /space/api/page/share/query/pagechunk`（body=`pageId=nodeId`）。对照：分享页自己的 pageId 应是「不可分享」。

算成：拉到他人文档全文（标题/角色卡/大纲，`role=editor`）。

假点：只读到已发布 HTML 快照；pagechunk 对资料库也 12607；正文已在对话快照里。单站没中不删短表这行。

### 匿名 CSRF 头读详情（短表有指针）

认：匿名 CSRF/临时 token 口直接出 token；业务详情只验这个头不验登录；query 的数字 id 和回包 `data.id` 可以不是同一个号。不是助手 `GetHistoryList` 可选登录头（那枪见「助手历史未授权读他人任务」）。

打（不登录）：

1. 打 csrf/token 口拿头  
2. 详情换数字 id（1、2、13），**query id 对不上回包 id 别停**  
3. 对照：不带头应失败；不存在的 id 应「不存在」

算成：未上架/测试训练脚本或内部会话正文（cardMap/对白全文，不是标题）。

假点：只有公开广场；token 过了仍空。单站没中不删短表这行。

> （续前：playbook-src-idor-test 第 2/4 部分）
