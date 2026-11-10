# 中转服务端（Go）

源码：https://github.com/cpxjazz-dev/botkeep-relay

收一条消息，按配置转发到钉钉自定义机器人或 QQ 官方机器人，附带一个网页配置界面。

## 为什么是 Go

- 几乎只用标准库，启动不到 1 秒，躲开了 Botkeep 约 70 秒的启动窗口。
- 唯一第三方依赖是 Postgres 驱动 `github.com/lib/pq`（纯 Go），已 `go mod vendor` 进仓库，构建不用联网下载。
- 编译后是单个约 17 MB 的静态二进制，运行时内存十几 MB，放在 512 MB 的免费机上很宽松。
- 配置存在 `config.json`，重启后还在，网页上改。

## 文件

- `main.go`：服务端全部逻辑（HTTP 接口 + 钉钉/QQ 发送）。
- `index.html`：配置页面（`//go:embed` 编进二进制）。
- `go.mod`：模块定义，`go 1.27`；`vendor/`：lib/pq 驱动（离线构建用）。
- `config.json`：运行时生成，不用手动建。
- `.env.local`：Neon 连接串与密钥（DATABASE_URL、AWS_*），**不提交**，部署时单独上传。
- `love-schema.sql`：love 站的建表 SQL（服务启动时也会自动建表）。
- `db.go` / `password.go` / `s3.go` / `love.go`：love 站的数据库、密码哈希、对象存储、账户与照片接口。

## 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/` | 配置页面 |
| GET | `/health` | `{"status":"ok","target":"dingtalk"}` |
| GET | `/api/config` | 当前配置（密钥只返回“是否已填”，不回传明文） |
| POST | `/api/config` | 保存配置；密钥字段留空 = 保持原值 |
| POST | `/api/test` | 发一条测试消息到所有启用的接收方（可用 `{"text": "..."}` 自定义内容） |
| POST | `/relay` | 转发消息，JSON：`{"text": "...", "title": "..."}`，title 可不填 |
| GET | `/api/openids` | 监听状态 + 已抓到的 openid 列表 |
| GET | `/api/history` | 最近 50 条发送记录 |

端口取环境变量 `SERVER_PORT`（Botkeep 会自动注入），没有就取 `PORT`，再没有用 8080。

## 调用密钥（防别人乱发）

`/relay` 是公开地址，谁知道了都能往你的 QQ 发消息。在配置页的“调用密钥”一节设个密钥（有“生成随机密钥”按钮）并保存，之后调 `/relay` 和 `/api/test` 就要带上它，二选一：请求头 `X-Api-Key: 你的密钥`，或地址后面加 `?key=你的密钥`。不带或带错返回 401。密钥存在服务端的 `config.json` 里，页面上只显示“已设置”，看不到明文；想取消就点“清除密钥”再保存。

## 发送记录

每次 `/relay` 和页面测试的发送都会记下来（时间、来源、内容前 200 字、每家接收方的成功/失败），最近 50 条，存在服务端的 `history.json`，配置页最下面能看到。定时心跳不记在这里（太频繁），只写进服务端日志。

## 博客（附带的小型静态站）

`blog/` 是一份纯静态技术博客，介绍学习项目、链到各 GitHub 仓库。服务在 `/blog/` 下原样提供它；以后迁到 Cloudflare Pages 时直接把 `blog/` 目录推过去，不用改内容。**博客与 love 站已彻底拆开，互相不链接。**

- 线上：https://rxs3yg.bot-keep.xyz/blog/
- 加新文章：往 `blog/` 里加 HTML 文件，重新部署即可（改 main.go 不需要，静态文件嵌在二进制里，重启后生效）。

## 给她的（独立照片站：账户 + 上传 + 四种展示效果）

`love/` 是给女友的独立小站，与博客彻底拆开、互不链接：飘心、闪星、花瓣流式特效 + 流光标题 + 音乐播放器。

- 线上：https://rxs3yg.bot-keep.xyz/love/
- **相册是每人私有的**：首页不放任何照片；登录后只显示你自己上传的照片，别人看不到（superadmin 除外，见下）。原有的 6 张生活照片已迁迱cpxjazz 账户（存进对象存储 + 照片表），不再以静态文件放在首页。

### 账户

love 站有自己的账户系统，数据存在 Neon Postgres（Love 项目，`love_users` / `love_sessions` / `love_photos` 三张表，服务启动时自动建表）。

- 注册：用户名 3–20 位（字母/数字/下划线），密码至少 6 位，昵称可空。
- **密码只存 PBKDF2-HMAC-SHA256 哈希**（`password.go`，标准库实现，60 万次迭代 + 16 字节随机盐），明文只在内存里出现一次，绝不写盘、不记日志。
- 会话：登录后发一个 30 天有效的 HttpOnly 会话 cookie（`love_session`），库里只存 token 的 SHA-256 哈希；改密后旧会话全部作废。
- **superadmin**：`LOVE_SUPERADMIN_USERNAME`（默认 `cpxjazz`）指定的用户名，注册或登录时自动升为 superadmin；该账户保留下来，不会被覆盖。superadmin 能在页面上的用户管理里点任意用户、看他/她上传的照片（`GET /love/api/photos?user_id=` 与 `GET /love/api/users` 只对 superadmin 开放）。

### 上传：照片进 Neon 对象存储

- 登录后可在页面上上传图片（JPG / PNG / WebP / GIF，最大 5 MB），文件存到 Neon Object Storage 的 `uploads` 桶（路径风格，`s3.go` 自己算 SigV4 签名），元数据（上传者、文件名、说明、大小）记在 `love_photos` 表。
- 看图用 1 小时有效的预签名链接，浏览器直接从 S3 拉图。
- 配置在 `.env.local`：`DATABASE_URL`（pooled，给应用流量）、`DATABASE_URL_UNPOOLED`（直连，给迁移）、`AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`（对象存储凭据）、`AWS_ENDPOINT_URL_S3`、`AWS_REGION`、`NEON_STORAGE_BUCKET`。

### 九种展示效果

登录后在账户卡里选一种，记在账户的 `display_effect` 字段上，下次登录还是它（未登录默认拍立得）：

- 📷 **拍立得**（默认）：歪着摆的白边卡片、胶带、爱心徽章。
- 🔲 **网格**：齐整方块，无倾斜、无胶带。
- 🎞️ **轮播**：一张大图居中，左右切换，5 秒自动换一张。
- 🧱 **瀑布流**：多列、按原比例、不裁切。
- 🌀 **封面流**（Coverflow，参考 Apple iTunes 与 Addy Osmani 的纯 CSS Cover Flow）：3D 扇形，中间那张正对着你，两边依次后退、倾斜、变淡；点一张非中间的卡先把它挪到中间，点中间那张开灯箱，带倒影。
- 🖼️ **满幅行**（Justified，Flickr / Google Photos 式）：每行等高、按比例伸满整行，不裁切；说明文字悬停时才浮现。
- 🃏 **堆叠**：一叠牌，顶上的正对着你，下面几张歪着露个边；点顶上的牌或按左右键翻下一张。
- ✨ **扩散**：像随手撒在桌上的照片，各自歪着、错落着；悬停那张会扶正、放大、浮起来。
- 🌳 **记忆树**：中间一条主干，照片左右交错长在枝头上，枝节处一颗爱心；手机上自动收成单列。

### 每人上限

每人最多上传 **50 张**照片（`loveMaxPhotosPerUser`），满了上传会直接被拒。前端在上传区显示“已上传 N / 50 张”。

### love 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/love/api/register` | 注册（用户名、密码、昵称） |
| POST | `/love/api/login` | 登录 |
| POST | `/love/api/logout` | 登出 |
| GET | `/love/api/me` | 当前登录用户 |
| POST | `/love/api/password` | 改密码（要当前密码） |
| POST | `/love/api/prefs` | 保存展示效果偏好 |
| POST | `/love/api/upload` | 上传图片（multipart，字段 photo，可带 caption） |
| GET | `/love/api/photos` | **登录后只返回自己的照片**（未登录 401）；`?user_id=` 只给 superadmin |
| GET | `/love/api/users` | 用户列表，只给 superadmin |

## 每小时运行状态推送

服务在后台每隔一段时间（默认 30 分钟）给所有启用的接收方发一条运行状态，证明服务还活着：

```
【中转服务·运行状态】
时间：2026-11-07 16:39:00
已运行：2小时15分钟
内存：21.8 MB
存储已用：108.5 MB
目标：QQ，已启用 1 个接收方
```

没收到就说明服务可能掉了。开关和间隔在配置页的“每小时运行状态推送”一节里，改了保存就生效，不用重启。内存是进程从系统拿到的总内存（接近容器里看到的占用），存储是服务所在目录下所有文件加起来的大小。

## 部署（Botkeep）

服务器已切到 Go 1.27.1，启动命令 `go build -o relay . && ./relay`（先编译出约 10 MB 的静态二进制再跑，重启快、内存只有约 43 MB；直接 `go run .` 也能跑，但编译慢、内存约 200 MB）。把 `main.go`、`index.html`、`go.mod` 传到服务器根目录，点 Start 即可。域名：`https://rxs3yg.bot-keep.xyz/`。

## QQ 说明

- 需要 QQ 开放平台（q.qq.com）的 AppID 和 AppSecret，机器人要在应用里开通相关权限。
- 接收方 openid 从机器人收到的事件里拿：私聊用 C2C 接口，群聊用群接口。
- 主动发消息的载荷形状是按官方文档写的，第一次用“发送测试消息”按钮实测一次，报错会直接显示在页面上，再按报错调。

### 找不到 openid 怎么办

QQ 不提供“QQ 号查 openid”的接口，openid 只在机器人收到消息的事件里出现。所以服务里加了一个网关监听：填好 AppID 和 AppSecret 并保存后，服务会自动连上 QQ 的 WebSocket 网关（只订阅群聊 + C2C 消息这一个 intent，`1 << 25`），然后用 QQ 给机器人发一条消息（私聊它，或把它拉进群里发一条），openid 就会出现在配置页的“找不到 openid？”一节里，点复制填到接收方 OpenID 即可。抓到的 openid 存在服务器上的 `openids.json`，重启后还在。

注意两点：QQ 约每 30 分钟会主动断开网关连接一次（4009），这是正常的，监听会自动重连；如果 Identify 被拒（一般是群聊 + C2C 的 intent 没在开放平台开通），监听状态会显示原因，先去开放平台把权限开了再试。

### QQ token 的坑

`bots.qq.com/app/getAppAccessToken` 返回的 `expires_in` 有时是字符串 `"6253"`，有时是数字 `7200`。Go 的 `encoding/json` 按 `int` 解析会在它是字符串时报错，导致明明拿到 token 却报“获取失败”。所以 `qqTokenResp` 里不解析 `expires_in`，只取 `access_token`。

## 验证记录

- 2026-11-07 在 InstaBox 免费机（Go 1.27.1）上 `go vet` 干净、`go build` 通过；跑起来后 `/health`、`/api/config`、配置页、`/relay` 未配置时优雅报错、保存配置落盘都测过；用假 webhook 调钉钉拿到了真实返回 `errcode=300005 token is not exist`，说明钉钉发送链路是通的。
