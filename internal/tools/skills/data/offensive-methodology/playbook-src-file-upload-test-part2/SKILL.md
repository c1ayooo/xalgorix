---
name: playbook-src-file-upload-test-part2
description: "写不写只认 reportvulnerability 的字段与证据要求。本篇是测法：别停在能传能下，跟可执行/路径/SSRF/跨用户业务对象。（第 2/2 部分）"
tags: [playbook, src-hunting, file, upload, test]
intent: offensive
---
### 分享鉴权 false 仍下媒体（短表有指针）

认：云录制分享；鉴权接口有 `download_enable`/`view_minutes_enable`。或会议 JSON 网关根本没有 `permission/auth`（404 别停）。

打（不登录）：

1. `download-multi-record-file` **和** `sign-multi-record-file` 都打（要 `auth_share_id`+`resource_type`）。国际 JSON 网关缺 `sharing_id` 只回 `2710500 参数非法`，`auth_share_id` 和 `sharing_id` 一起带才签发 MP4；缺了会当没洞。**POST JSON 报「录制 id 不能为空」别停，改 GET query 把 record_id / auth_share_id 放进 URL。**  
2. 没有鉴权口不要当成没入口：直接打 `public/record-detail/download-multi-record-file`。  
3. 纪要/时间线 `get-full-summary` / `query-timeline` 只填 `record_id` 也试，不要默认必须带 share。  
4. COS 无 Referer 的 403 **不是终点**：带分享页 Referer 再 GET。  
5. `permission/get-cfg` 看会不会吐同会其它 record。  
6. 只拿到 url 没 GET 到 MP4 算半条，继续跟。  

算成：鉴权写 false 仍拉到真 MP4（ftypisom+体积）；或只要 record_id 出纪要全文（不是标题）。

假点：鉴权真拦了、下不到文件；只有公开说明书；纪要只要标题没有正文。单站没中不删短表这行。

### 密码分享回包带明文提取码（短表有指针）

认：分享详情 JSON 一边 `need_pwd` / `auth_level`（或同类）表示要提取码，一边 auth 对象把明文提取码放进 `pass_word`（或同类）。或落地 HTML / `syncData` 内联同一明文码。

打（不登录）：

1. 先空着提取码打查看口。对照：文件列表应空，闸字段应是要码。  
2. 同一份回包里抄明文码，填回查看口。对照：空密时列表空、填了才出 `file_id`。  
3. **落地 HTML / `syncData` 里已经有明文 `pass_word` 就直接抄**，不必先打 View CGI。  
4. 拿 `file_id` 和这份码打下载口，跟到真文件头（PDF/ZIP 一类正文），不要停在 JSON 有码。

算成：拿到提取码并下到密码分享正文，不是空密直接出文件列表。

假点：`pass_word` 是哈希抄了登不进；空密 View 已经出文件（那是无提取码分享，不是这枪）。单站没中不删短表这行。
密钥实值、完整 JS 不进本篇。

## temp 上传无白名单 + 域名内 html 托管（短表有指针）

**形态**：业务上传口（照片/材料）挂在拦截器匿名白名单里，layui 前端 `exts: 'jpg|jpeg'` 只是前端行为，服务端不校验扩展名；落地目录在应用自己的静态前缀下（如 `/cdn/data/temp/<uuid>.<ext>`），同一域名可直接 GET。

**打法**：
1. 不登录 multipart 直传 `.txt` 探服务端过滤（受理=无白名单）。
2. 再传 `.html`，GET 落地路径看 `Content-Type`：`text/html` 内联渲染 = **官方域名钓鱼页托管**（比任意文件读更值钱的定性）。
3. 传 `.jsp/.jspx` 验证是否解析（上传成功但 GET 404/原样字节=不解析，无 RCE，如实写）。
4. 记频控/容量/过期机制有无；文件 UUID 重命名=不可覆盖他人文件。

**过门槛**：官方域名下 200 text/html 渲染自传页面（钓鱼托管成立）。
**不过**：上传口要登录；服务端拒非图片后缀；落地按 octet-stream 强制下载。

> （续前：playbook-src-file-upload-test 第 2/2 部分）
