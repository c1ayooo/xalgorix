---
name: playbook-src-short-table-part2
description: "打穿短表（索引）（第 2/7 部分）"
tags: [playbook, src-hunting, short, table]
intent: offensive
---
| 认什么 | 打哪 | 出什么算成 | 假点 |
|--------|------|------------|------|
| 二维码登录；回包有 token；登录页吃 `?token=` | 不登录拿 token；**要会话**的人打开带 token 的登录 URL（等于确认扫码）。见 `csrf-test.md` §18 | 未登录端变成对方号 | token 过期；点开只出码不绑会话；必须真机扫、URL 确认无效 |
| 未登录首页/运营配置 JSON 的跳转 URL（skipPath / jumpUrl / schema）query 里带着能当会话用的 token | 不登录抄出来当 Token / Authorization 头打 me/info。对照：不带这串应登录过期。见 `authbypass-test.md`「运营配置深链里的会话票」 | me 是别人的手机/角色，会话能当这个号用 | token 过期或只是占位串登不进去；配置只出运营文案没有票 |
| 对象存储先申 STS；key 是文件 md5；或 assumerole 的 `filename`/`Action` 进 Policy；或领钥口（temp.secret 一类）匿名可调 | 凭证字段填 `*`/`空`/`/`，Policy 里 filename/Action 通配。**匿名领钥口先原样领默认凭证**：假值对照 → 直接 PutObject（任意 key+自定 CT）→ 桶域名公网读回即成，**List/Get/Delete 全拒不算假点**，别先折腾通配。**List/Delete 403 别停**，换匿名票再打。落地页写死匿名 JWT 改打存储 REST。细节见 `file-upload-test.md`「STS / 对象 key 通配覆盖」 | 对方文档/图变成你传的；或能列出并删别人的 key；或拉到**他人**问卷/证件自拍；或写入的文件在桶域名公网可读 | `*` 只到废桶；覆盖 403；只盖自己的；策略服务端写死；CDN 不刷新看起来没盖；只能下自己刚传的；猜不到别人 key；rest 表 403 就当存储 REST 也不能写；**匿名领的凭证 PutObject 也拒** |
| 业务网关把对象存储代理成 `/api/storage/sign`（或同类 sign），query 吃 `key`；`key=/` 或 `key=.` 回 ListBucket XML | 不登录 GET `?key=/` 列表，再 GET 列表里业务前缀。见 `file-upload-test.md`「存储代理 sign key=/」 | 列出并读到他人未公开对象原文 | 只有 app-static 公开静态；`filename=` 400 当没口 |
| 站点图/指南挂在对象存储桶上；`?policy` 或等价策略接口全 Allow | 无密钥 LIST，再 PUT 盖官方对象。见 `file-upload-test.md`「桶策略对匿名全开」 | 官方指南/协议/站上图变成你传的 | 只能列不能写；只能盖自己前缀；只能下本来就公开的静态文件 |
| 对象存储带签 URL；SignedHeaders 一类没有 `host` | 域名换成同账号另一个桶，加 `?uploads` 做分片列举。见 `file-upload-test.md`「签名没绑 Host」 | 列出或读到**别的桶**里的对象 | 签名罩住 Host；换域 403；只有自己这个桶 |
| 对象内容你能控，对象 Content-Type 被卡死；有临时钥或预签名 | 再签一枪，签进 `response-content-type=text/html`。见 `file-upload-test.md`「签名覆盖 Content-Type」 | 用签过的 URL 打开，浏览器当 HTML 跑（存储 XSS） | 签名拒这个参；下载仍是附件/原 CT；只能改自己不可达的对象 |
| 商家后台能建促销/券再绑 SKU；绑接口里有 `productId` | 自己的券 ID 不动，把 `productId` 改成别人的货。见 `logic-test.md`「商家促销绑定」 | C 端打开别人那件货，实付价按你这张券掉下来 | 绑定 200 但 C 端价不变；只能绑自己店的货；结账页改 `productId` 换便宜货（那是另一条） |
| 企业 Git + 桌面客户端；README/议题能写 HTML | 当存储 XSS 打进客户端；有桥再升 RCE。见 `xss-test.md` §8「XSS → RCE」 | 客户端里执行了本机命令 | 只在浏览器弹；客户端不渲或沙箱死了（停在存储 XSS，别写成 RCE） |
| Electron / CEF 客户端注册了自定义协议；协议参数有 `url` / `urlType` / `open` / `openUrl` / `webview` | 协议里填自己的页（JSON `params` 或扁平 `openUrl?url=`）；页上先探 `process` / `require` / `window.require`，有就 `exec`，不要只认 Electron 10 的 `remote`。见 `xss-test.md` §8「自定义协议 → RCE」 | 本机弹出计算器 / 跑了指定无害命令 | 协议只开自家域；`require` 和 `remote` 都没有、预加载只露 `ipcRenderer` 却调不了命令；只弹「打开应用」但不加载外站；沙箱死了只在页面 alert |
| 改/查详情的 id 是一长串密文；JS 有 `modulus`/`exponent` 或 JSEncrypt / `security.js`。或落地页写死公钥 + 演示 `userid`，列表 query 吃加密串 | 回包里找明文序号；用公钥自己加密相邻数字再换进去。演示号则加密页面写死的 `userid` 不登录打工单列表，再换邻号/对照 `userid=0`。见 `idor-test.md`「密文 ID」 | 出别人的地址/手机/姓名；或列表出现他人工单/跟帖正文 | 会话绑死只回自己的；钥匙是验签用的、乱加密服务端拒；加密后仍是公开 FAQ |
| 列表按 `appId`/`tenantId` 过滤；附件是带 `sign` 的下载 URL | 租户字段试 `0`/`-1`/空；附件 URL 只把租户改成自己的。见 `idor-test.md`「哨兵租户」 | 列表出现他租户工单/资质，附件真下到证照 | `0` 仍只回自己的；换租户下载 403；只有文件没有业务列表 |
| 小程序云 / 云函数有 Docker Registry、`/v2/` | 自己号 login 后打开 `_catalog`，再 pull 对得上别人租户的镜像。见 `idor-test.md`「制品库 catalog」 | 拉下来的镜像是别人租户的 | catalog 只回自己的；pull 403；只能拉公开库 |
| 云开发网关；`*proxy*` 有 `targetUrl`/`url`；或云 SSRF 已打到元数据目录；开放代理**固定 POST**（直打 IMDS 405） | 不登录（匿名 token 也算不登录）。回环对照后再打元数据；405 转 GET、钥匙路径见 `ssrf-test.md`「云厂商元数据路径差」。ListBuckets 403 别停，再签日志类只读 API | 回显元数据，或临时钥匙能调云 API 问出主账号；或列出日志采集配置/主题 | 代理白名单且元数据也 403；匿名开了但代理对匿名关死；窄角色没证明能调云 API 却写成接管全账号 |
| 公开 GOPROXY（`/go/`、模块 `/@v/list`）会按模块路径做 `?go-get=1` 再跟 VCS | 不登录。模块路径写成自己的域，`go-import` 用 **hg**（git 超时别停）。见 `ssrf-test.md`「公开 GOPROXY」 | hg 报错/回显出元数据或临时钥匙，再问出云账号 | 不是 GOPROXY / 不会 go-get；元数据域名也被拦；hg 也不跟且出不了钥；钥调不通 |
| 未登录发签口给 IM/RTC/体验 userSig；identifier 跟请求走或票里没 userId；或同一后台其它口请登录、发签口只要自定义头+客户端 userId | 不登录拿签登 IM 拉他人会话/群。v2 403 改 v3。Demo 抄 JS 发签口+空密。细节见 `authbypass-test.md`「未登录发 IM/体验票」 | 拉到**他人**会话/群成员/好友，或明文手机 | 只能登游客 `null`、空会话、只有自己建的空群；换别人 UserID 失败；`none_auth` 只新开空号；演示号没绑手机不算；前端 SECRETKEY 已空且发签口要验证码 |
| H5/落地页写死产品号或业务 id；发签口不登录给共享产品号 JWT（不是游客空号） | 不登录 POST 发签，带着票打 JS 没写出来的 list（只有 create/GET by id 也打）。见 `authbypass-test.md`「写死产品号发共享 JWT」 | list total 海量且带他人原图/证件照 | 票只能建空任务；list total=0 或只有自己刚传的 |
| 企业 IM / 业务 unlogin 发签口；回包 `nonceStr`/`nonce` 以 `MIIE` 或 `-----BEGIN` 开头 | 不登录。渠道/活码未登录漏 `corpId` 再打发签。把 nonce 当 PKCS8 私钥 load，不要当短随机串丢掉。见 `authbypass-test.md`「发签 nonce 是私钥」 | 能 load 成 RSA 私钥 | nonce 只是短随机串；假 corpId 一直业务错 |
| 招募/指定用户详情带 `specifyUsers` 或 openid；H5 写死 SHA256 请求签名；或邀请页地址已有 `?openid=` | 不登录抄 openid 打信箱/邀请码。见 `idor-test.md`「详情抄 openid 再打信箱」 | 换 openid 信箱或邀请码/名单变，出现对方积分过期通知或打码手机昵称 | 列表人人一样只是全站广播；接口要真登录/签名；假 openid 没有邀请码；公开运营名单 |
| 云录制分享；鉴权接口有下载/试看开关；或会议 JSON 网关根本没有 permission/auth | 不登录打 download **和** sign 两口；没有鉴权口别停，直接打多路下载；纪要/章节摘要口只填 record_id。**POST JSON 报录制 id 不能为空别停，改 GET query**。对象存储无 Referer 403 别停。国际网关分享 id 一起带。细节见 `file-upload-test.md`「分享鉴权 false 仍下媒体」 | 鉴权写 false 仍拉到真媒体（容器头+体积）；或只要 record_id 出纪要全文/各章正文 | 鉴权真拦了、下不到文件；只有公开说明书；纪要只要标题没有正文 |
| 分享详情 JSON 一边 `need_pwd`/`auth_level` 表示要提取码，一边 auth 对象把明文提取码放进 `pass_word`（或同类）。或落地 HTML / `syncData` 内联明文码 | 不登录抄码再打下文件。落地 HTML/`syncData` 里的明文 `pass_word` 也抄，不必先打 View CGI。见 `file-upload-test.md`「密码分享回包带明文提取码」 | 拿到提取码并下到密码分享正文，不是空密直接出文件列表 | `pass_word` 是哈希抄了登不进；空密 View 已经出文件（那是无提取码分享，不是这枪） |
| 分享图 `generate`；replacements 有 `%MARKDOWN%` | 不登录 POST，markdown 写成 `![x](url)`。先对照公网图，再打 metadata。见 `ssrf-test.md` | 生成图里嵌进你指定的图；或内网/元数据在图上出 500/正文 | 只出模板空图；markdown 当纯文本；必须登录 |
| 企业客服 H5 签发链；内联有拉小程序链 / 开放链一类 | 不登录 POST 回调参=外站。打开返回的落地链。见 `authbypass-test.md`「官方客服链签发外站」 | 落地页正式名是官方客服号，解码出的 query 是你填的网址 | 只签发本站路径；落地页不带 query；必须登录 |
| 服务端代调身份供应商（小程序码/跳转码 / OAuth 换票 / 未登录业务口直接下发 `access_token`）；非法 pagepath 或 `redirect_uri` 外域时 message/报错 URL 拼进 `access_token=` 或 `client_secret=`；或 GET 领票口直接 200 出票 | 不登录。清单有领票口先 GET，200 出票不必等报错；没有再 path 填外站 / 换票口假码对照再改 `redirect_uri` 外域，从 message/报错 URL 抄。仍打账号信息口 / 签发跳转码。见 `authbypass-test.md`「身份供应商代调报错回显 token」 | token 能问出官方号主体或签发官方跳转码；或完整 AppSecret 能拿授权码换成该应用 access_token | 报错只有 ErrCode 没有 token/secret；假码本域回调不带钥；token 调身份接口无效；secret 换不了票；领票口请登录或只出占位串 |
| 发布页 conversation-data 的 artifactMap 有资料库 `nodeId`；主站文档空间要登录 | 不登录打发布域 pagechunk。见 `idor-test.md`「资料库 nodeId 未授权读全文」 | 拉到他人文档全文（标题/角色卡/大纲，`role=editor`） | 只读到已发布 HTML 快照；pagechunk 对资料库也业务错码；正文已在对话快照里 |

> （续前：playbook-src-short-table 第 2/7 部分）
