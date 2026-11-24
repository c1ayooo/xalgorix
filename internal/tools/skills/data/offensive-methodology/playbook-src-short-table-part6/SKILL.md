---
name: playbook-src-short-table-part6
description: "打穿短表（索引）（第 6/7 部分）"
tags: [playbook, src-hunting, short, table]
intent: offensive
---
| Spring 单体前后台同 context（视图口匿名302到登录页=有全局拦截器），但前台白名单业务写口（申领/上传/退款）也在同一拦截器白名单里 | 全端点 GET+POST 空参双探分类：302=闸、405=方法不对但已过拦截器、500=匿名直达业务层（堆栈当执行证明）。对 500 的 POST 口按前端 JS 拼全参即可写。见 `authbypass-test.md`「拦截器白名单漏前台业务写口」 | 匿名落库拿到业务单号（applyId），且带单号重放可改写 | 写口回登录码/要验证码；参数全齐仍 411 参数异常；只读不写 |
| 文件上传口挂在匿名白名单（layui 前端限 jpg，服务端无白名单），落地路径带 /cdn/ /static/ 等静态前缀 | 不登录直接 multipart 传 .txt/.html 探服务端过滤；落地后 GET 看 Content-Type——text/html 内联渲染即钓鱼页托管。见 `file-upload-test.md`「temp 上传无白名单+域名内 html 托管」 | 官方域名下 200 text/html 渲染自传页面 | 上传口要登录；服务端拒非图片后缀；落地按 octet-stream 下载 |
| 短信验证码体系的『查询验证码』口（queryYzmByPhone/queryCode 一类）或发码接口回包携带验证码 | 先发码（jzSendSms 类仅 phone），再打查询口看回包字段：expire 之外若有 yzm/code 明文即中。拿到码后打全部 `*ByPhone` 闸口（报名/成绩/证书族，参数形靠 Spring 缺参报错点名 + 前端 axios 包装判 form/JSON body）。再拿返回实体 id 打 `*ById` 无码口闭环。见 `authbypass-test.md`「验证码明文回读链」 | 回包 yzm 明文，且带码拉出他人报名实体（证件号/手机明文）或成绩 | 查询口只回 expire 秒数；带码口全被拒；ByPhone 口需登录态 |

> （续前：playbook-src-short-table 第 6/7 部分）
