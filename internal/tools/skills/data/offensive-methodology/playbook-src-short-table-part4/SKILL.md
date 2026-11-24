---
name: playbook-src-short-table-part4
description: "打穿短表（索引）（第 4/7 部分）"
tags: [playbook, src-hunting, short, table]
intent: offensive
---
| 开放平台文档中心；页面文档/分类口 401；入口 script 有文档页动态 `import()` 的技术文档 chunk | 不登录。文档 API 401 别停。跟首页 script → 文档页 import → 文档 chunk，抠样例报文里的身份证/手机。见 `info-leak-test.md`「文档 chunk 里的真实证件样例」 | 过校验位的身份证 + 姓名/手机，对得上人 | 张三/110101 占位；校验位不对的编造号；空证件号模板；只有公开产品说明书没有样例报文 |
| 门户 CMS 前端写死 `accessKey`+`secretKey`；有站点 login 口发 JWT | 不登录抄钥打 login，头带站点 token 打内容/附件列表，跟测试/后台频道和附件 URL。见 `authbypass-test.md`「门户 CMS 写死站点钥」 | 内部测试报告/未对公开展示频道的稿件正文 | 只有公开运营 Banner/客户端下载 |
| 管理台前端 JS 写死 CI 的流水线 id + base64 `auth`，且有未登录 trigger 口 | 不登录抄 auth 列私仓。见 `info-leak-test.md`「管理台 JS 写死 CI 仓钥」 | 列出他团队私有仓库名/HttpsUrl/ProjectId，或钥 scope 含读仓且开放接口认钥 | 钥过期；仓是公开的；只有 trigger 回产品下线、没有证明钥还能列出私仓 |
| 开源文档/社区前端为拉 GitHub org、贡献者、star 把头，把 `ghp_` / `github_pat_` 打进打包 JS | 不登录从 JS 抄 PAT，打 `GET https://api.github.com/user`，再 `/user/repos?affiliation=owner` 看 permissions。对照无钥应 401。见 `info-leak-test.md`「文档站 GitHub PAT」 | me 是真人 login/姓名，且对该号仓 `admin=true`（能当这个 GitHub 号用） | 钥已吊销；`/user` 401；只是 GitHub App 安装令牌读公开 org |
| 落地页/viewer JS 有循环 XOR / hex 包着对象存储永久 SECRET_ID/SECRET_KEY | 不登录解开永久 AK/SK，签 STS 问身份、问账号。ListBuckets 403 别停。见 `info-leak-test.md`「viewer JS XOR 藏对象存储永久钥」 | 问出 AccountId/Uin/AppId（长期钥，不是临时票） | 解开调云 API AuthFailure；exampleValue 解成 hello_world 占位 |
| 管理台 webpack 明文 `accessKeyId`+`secretAccessKey`（S3 兼容永久钥）；或中间件登录页内联服务钥；`getUploadSign` 是前端 HMAC-SHA1，policy `starts-with $key` 为空 | 不登录抄钥自己算。List/PUT 403 别停，先问桶地域证活；页面桶名拼错试邻近。对照假 AK `InvalidAccessKeyId`。任意 key PUT/DELETE，官方已引用对象试覆盖。见 `file-upload-test.md`「webpack 明文对象存储永久钥」 | 完整永久云钥能签（真签 PUT 200 或问出地域）；能盖官方已有对象更稳 | InvalidAccessKeyId；只能传到固定前缀；getUploadSign 其实是 SSO 接口没有本地钥；钥过期 |
| 公网 HTTP `/version` 出 `"Model":"master"`（分布式文件集群） | 不登录 `GET /user/list` 拿 AK/SK；对照假 ak 打用户钥信息口；再管理口看他用户业务卷。见 `info-leak-test.md`「分布式文件 master 未授权用户钥」 | 完整 AccessKey+SecretKey 且真钥问出身份/他用户业务卷 | 只出版本没有钥；list 空；真假钥同一句；只有空测试卷 |
| 企业流水/消费/名单列表有员工名、姓名、关键字筛；回包有 `total`/`totalNum`/`totalSize`；或后端是 ES | **只打一枪**，只看回包 **total** 就停。不连打、不翻页导出、本枪不用 sqlmap dump。见 `injection-test.md`「列表筛选项 OR + total」 | total 从空/个位涨成海量，且第一条能看出**不是本企业本职**的流水（姓名/金额） | 模糊搜索碰巧命中 or；涨的全是本企业本职可读（不报）；ES 只吃 DSL 不吃这段 SQL；WAF 405 |
| 邮件订阅嵌在 iframe 里；同目录 list 的 key 当鉴权、拼进 SQL | 不登录打 iframe 同目录 `list.php?key=`：`1' OR client_id=租户 LIMIT 1#`。订阅表单皮常见。见 `injection-test.md`「邮件订阅 iframe 同目录 list」 | 该租户订户姓名/邮箱/电话 | 没有 list.php；key 走常量比较/预编译 |
| 登录后个人页、账单、`/api/me`、设置页（有没有 `X-Cache` 都行） | 原 path 后加 `.css`/`.js`/`;.css`/`%2f.css`，再未登录打同一 URL。见 `cache-poisoning-test.md`「缓存欺骗偷会话页」 | 未登录拿到**别人**个人页/账单/会话页正文 | 只 HIT 了静态壳；`Cache-Control: no-store`；要受害人先点才缓存、你这边没拿到他人数据 |
| 清单有重置 / 激活 / 邀请发信口；或未登录授权地址口用 Host 拼 OAuth `redirect_uri`（GetAuthorizationUrl 一类） | 重置/授权请求改 `Host`，拦了再只改 `X-Forwarded-Host`。见 `http-host-header-test.md`「Host 毒重置信」；认证口下限仍是 `dig-scope` §4.2.2 | 协作域收到带 token 的重置链，能换别人密；或官方身份页仍 200 且 `redirect_uri` 是外域（登完码落到外域） | 信里仍是原站；只反射 Host、邮件不跟；token 绑死本机会话点不开；身份页拒外域回调 |
| 未登录签发 SSO / 回跳；callback 只判断字符串里有没有官方 host，或不校验、任意外域也能签 | 不登录。callback 填外域（夹官方 host 和不夹都试）。见 `authbypass-test.md`「未登录签发回跳外域」 | SSO 成功且回跳仍是外域；或登完通行证出现在外域 query | callback 被改空；SSO 缺回调参数 |
| 电子合同/供应商门户登录页只填可枚举合作方数字 id；未登录 getUrl（或同类签发登录链）直接出 partnerId+generate+code | 不登录打签发口，把回包三项 POST 给 setCookies/换票口，再带会话打合同/支付列表。见 `authbypass-test.md`「未登录签发合作方登录链」 | 进了别人供应商号（身份口登录成功，或能当这个号读未公开合同） | 签发口要已登录；code 必须从邮件点开；setCookies 不下会话；只出公开招商页 |
| 管理后台前端只跳 SSO，页面没有账密框；后端仍暴露账密登录 API | 不登录 POST 该登录 API 默认 `admin`/`123456`（不要磨 SSO 验证码），拿 accessToken 打 tenant/user page。见 `authbypass-test.md`「SSO 壳后面的账密登录口」 | 进了平台管理员号，且租户/用户列表出他人手机/姓名 | 登录口 404 或默认口已改；票只能进空租户没有联系人 |
| 运营台前端把供应商直连配置查询挂成未登录；body 只有可枚举合作方数字 id | 不登录 POST 查询口，换邻号。对照未配置 id 应回查空。见 `authbypass-test.md`「未登录合作方直连配置查询」 | 回包里是完整 clientId+clientSecret（能当这把直连钥用） | 接口已下线；clientSecret 空或占位；必须登录；只出公司名没有钥 |
| 联合登录签发口 body 只有 `provider`+云账号 `uin`（或同类 uid），不验 OAuth code/ticket | 不登录 POST 该口；再拿票打 me/用户信息。见 `authbypass-test.md`「联合登录只吃 uin」 | me 是**对方** UIN，能当这个号用 | 只出游客号；uin 必须已在本活动注册且 me 仍是自己；必须真 OAuth code |
| 活动页 login 吃互联 `openid+acctype+access_token`；假 token 在 qq/qc/pt 仍发票，wx/空票对照失败 | 不登录 POST login 假 token + 可枚举 openid；拿 JWT 打 getRoles/bindRole 一类角色口。见 `authbypass-test.md`「活动实验室 login 不校验互联票」 | 票里是对方 openid，角色列表出现他名下角色，或能绑上 | 只签发空号且角色列表全空、绑不上游戏角色；必须真互联票 |
| 未登录隐私号/虚拟号（AXB）口；失败时把真实手机当下发；对象号可遍历 | 不登录换门店/对象号。网关空 Origin 或本站 Origin 回 cross-origin 403 **别停**，改业务域 Origin（订单/购物车/H5 域，不钉某一家 host）再打。见 `idor-test.md`「隐私号失败回真实号」 | 回包是他人 11 位真实手机（文案写获取隐私号失败也算） | 只出虚拟号/中间号；必须登录；换号号码不变；空 Origin 403 当没口 |
| 物流/开放网关前端写死「无需鉴权」头 + 域名路由头（或同类过网关鉴权头） | 不登录带着这组头打可枚举 siteId / 空翻页名单/运单口。见 `idor-test.md`「过网关无需鉴权头」 | 出他人手机/住址/站点联系人 | 头过了业务仍请登录；只有公开轨迹没有 PII |
| 培训/学院 H5 网关把账号绑定/查询 RPC 放进未登录 client 前缀，body 吃 C 端 uid + 商家号 | 不登录 POST bind，uid 换成别人、商家号换成自编；再 querybind。见 `authbypass-test.md`「未登录培训绑定口」 | 出对方手机，或绑定成功改绑 | 只回未登录/参数异常；uid 一律未绑手机且写不进 |
| 未登录改密/首次设密/重置最后一步；body 有旧密或验码字段 + 新密 + 身份 id | 不登录。旧密/验码置空或省略；对照填错验码应拦、不存在 id 应查空。同一密再打若报历史重复=已写入。见 `authbypass-test.md`「未登录改密口」 | 未登录 Success 且改掉别人的密（历史重复/错码对照能证明写库） | 只回 0 没写库；必须真旧密/真短信；Success 但登录另一套、密没跟过去 |
| IDaaS 忘记密码先签发非 reset 的 JWT（scope=`_` 一类）；密保题库有未绑到该号的 id 可写 | 不登录拿 sq 类 token；GET 已绑 id；`update_question` 写未占用 id；verify 换 reset scope；再 `set_password`。见 `authbypass-test.md`「IDaaS 未占用密保题」 | 密保答案写到别人号上（改绑），或用该令牌真改掉别人的密。过了改回；改不回停在回包 | sq token 直接改密报 scope is not expected；已绑 id 409；verify 错答；策略拒且密保没写上（半条，不进表） |

> （续前：playbook-src-short-table 第 4/7 部分）
