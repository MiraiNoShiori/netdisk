# NetDisk

用 Go 写的一个个人私有网盘服务，日常真会用到的功能也顺手补全了：文件上传下载、目录树、秒传去重、断点续传、分享与提取码、回收站、配额统计，后面又接上了 S3 混合存储、热点文件自动迁移、NFSv3 挂载和局域网 WebRTC P2P 直传。

服务端自带一套原生 HTML / CSS / JS 写的 Web 界面，打包在二进制里，不用折腾 Node 构建，起起来打开浏览器就能直接用。

## 它能做什么

- **上传、下载和预览**：multipart 流式上传，不把大文件往内存里塞；下载走 `Range / 206` 支持断点续传；图片、视频、PDF 和纯文本支持在线安全预览；整个文件夹也能流式打成 ZIP 下回来。
- **账号与会话**：注册、登录、登出吊销、改资料、绑邮箱、改密码、注销账号。登录同时发 access token 和 refresh token，access 过期后请求返回 401 会自动续期再重发，登录接口也加了按 IP 的限流。
- **目录树与检索**：建文件夹、改名、移动（带环检测，防止把目录拖进自己肚子里）、全盘按文件名搜索、按类型筛选、排序分页。
- **秒传与断点续传**：底层按内容 SHA-256 去重，传过的文件秒传直接建记录、不耗流量；大文件切 4 MiB 分片上传，断了重新选文件能接着传。
- **回收站与容量配额**：删文件默认先进回收站，整棵文件夹也能一键恢复，30 天后后台自动清掉并回收物理空间；每个账号默认 10 GiB 配额，侧边栏能看到用量和去重帮自己省了多少空间。
- **分享链接**：文件、文件夹都能分享，支持设有效期、提取码和最大下载次数；后台记了访问日志，在「我的分享」里能看独立访客数、近 7 天趋势和具体记录。
- **Local / S3 混合存储**：平时存在本地盘里，接上 MinIO 或其他 S3 后可以随时把文件双向迁移，还能出 5 分钟有效的直链；开启热点策略后，下载次数多的本地文件会自动搬去 S3 帮主服务器减压。
- **NFSv3 挂载**：可以把单个用户的网盘目录直接以 NFSv3 导出到局域网，在 Linux 上 `mount` 成普通文件夹读写。
- **局域网 P2P 直传**：偶尔只想把大文件丢给旁边的人、不想绕服务器走一圈时，可以在 `/p2p.html` 建一个临时房间，两端浏览器靠 WebRTC 直接点对点传，服务端只帮忙递一下连接信息。

## 技术选型

没什么花哨的框架，尽量贴近标准库，代码读起来心里有底：

- **Go 1.27** + `net/http` + `chi`（轻，路由清晰，中间件不黑盒）
- **PostgreSQL 16** + `pgx/v5`（直接写 SQL，不用 ORM，复杂递归查询和约束自己全掌控）
- **MinIO Go SDK**（接 S3 兼容对象存储）
- **go-nfs** + `billy`（做 NFSv3 协议适配）
- **原生前端**（零依赖、零构建，`go:embed` 直接打进可执行文件）

## 跑起来看看

本地装好 Go 1.27+ 和 Docker Compose 就行，Windows 和 GNU/Linux 都能跑：

```sh
docker compose up -d postgres
cp .env.example .env
go run ./cmd/server
```

跑起来后打开 **http://localhost:8081**，注册一个账号就会自动进到网盘首页。

数据库表结构（`migrations/0001` 到 `0006`）在启动时会自动增量执行，重复跑也不会清空旧数据；如果你想先单独跑迁移，执行 `go run ./cmd/migrate` 就行。

## 几个实用细节

### 回收站怎么回收磁盘空间？
平时点删除（`DELETE /api/v1/files/{id}`）只是把文件或文件夹打上删除标记放进回收站，底层物理文件先留着，防止手滑误删找不回来。只有你在回收站里点「彻底删除 / 清空」，或者调用删除接口时显式带上 `?permanent=true`，又或者超过 `NETDISK_TRASH_RETENTION`（默认 30 天）被后台清理后，系统才会把数据库节点删干净，再检查这份内容还有没有别人在用——确认完全没人引用了，才真正删盘或删 S3 对象。

### 接上 S3 / MinIO 玩混合存储
本地开发可以直接用自带的 MinIO profile：

```sh
docker compose --profile objectstore up -d --build
```

把 `.env` 里的 `NETDISK_S3_*` 配好并重启服务后：
- 在网页里能看到每个文件目前在 `LOCAL` 还是 `S3`，点一下就能双向迁移，在 S3 上的文件还能生成 5 分钟直链直接下载；
- 在 `.env` 里配上 `NETDISK_AUTO_TIER_THRESHOLD`（比如设成 `10`），下载满 10 次且不小于 1 MiB 的本地文件就会被后台自动搬到 S3，想立刻看效果也可以在网页的「存储与分层」里手动点一次扫描。

### 把网盘挂载成 NFSv3 目录
先在网页里注册好要导出的账号，然后在 `.env` 里填上配置并重启：

```dotenv
NETDISK_NFS_ADDR=127.0.0.1:2049
NETDISK_NFS_USER=demo
NETDISK_NFS_CLIENTS=127.0.0.1/32,::1/128
```

在 GNU/Linux 机器上装好 `nfs-common` 后直接挂载：

```sh
sudo mkdir -p /mnt/netdisk
sudo mount -t nfs -o vers=3,proto=tcp,port=2049,mountport=2049,nolock,actimeo=0 \
  127.0.0.1:/ /mnt/netdisk
```

安全起见，NFS 网关默认只绑在本机回环地址，局域网其他机器要连的话必须把允许的网段写进 `NETDISK_NFS_CLIENTS`；一个网关实例只对应一个用户目录，别直接暴露到公网。

### 局域网 P2P 点对点直传
登录后点侧边栏的「局域网 P2P 直传」（`/p2p.html`），选好本机文件就能拿到一个 30 分钟有效的一次性链接。接收方不用登录，打开链接选好保存位置就开始直传。整个过程不走中继服务器，页面会实时显示速度和预计剩余时间，传完后还会核对 SHA-256。

## Known Limitations / 安全边界

- **账号和秒传**：已有账号建好后可设 `ALLOW_REGISTER=false` 关闭公开注册。默认仅能秒传自己仍有存活引用的内容。开启 `ALLOW_GLOBAL_INSTANT_UPLOAD=true` 后，知道其他用户文件哈希与大小的账号也能创建副本并下载。
- **反向代理和 Cookie**：文件下载、同一路径的内联预览和文件夹 ZIP 可使用下载 Cookie，其他私有 API 需要 Bearer token。HTTPS 在反代终止时，可设 `COOKIE_SECURE=true`；使用 `auto` 时，仅直连 TLS 或来自 `TRUSTED_PROXY_CIDRS` 且标明 HTTPS 的请求启用 Secure。只填写实际代理的源地址，并在代理上覆盖客户端的 `X-Forwarded-Proto`。
- **NFS**：网关使用 `willscott/go-nfs v0.0.4`，是轻量实验特性。一个实例导出一个账号的目录，访问控制依赖来源网段，适合受信局域网。
- **P2P**：浏览器使用 WebRTC 直连，未配置 STUN/TURN。适用于两端可直接互通的网络；跨 NAT、防火墙或启用了客户端隔离的 Wi-Fi 可能无法建立连接。
- **并发回收**：`Upload` / `InstantUpload` 的 blob 登记或查询与创建节点分步完成。在极高并发下，对象 GC 可能在这段窗口内回收内容。后续可通过事务锁或回收宽限期加固。
- **PDF 预览**：同源内联响应已设置 `X-Content-Type-Options: nosniff`，PDF 仍由浏览器解析。公网多用户部署建议在反代配置严格的预览安全头，或使用独立预览域隔离用户内容。

## 跑全量测试

想用真实的 PostgreSQL、MinIO 和 NFS 协议完整验一遍，直接起隔离测试环境跑验收脚本即可：

```sh
docker compose -p netdisk-test -f compose.test.yml up -d --build --wait
sh scripts/verify.sh
docker compose -p netdisk-test -f compose.test.yml down
```

## 相关文档

- 接口清单：[docs/api.md](docs/api.md) · OpenAPI 定义：[docs/openapi.json](docs/openapi.json)
- 写代码时的思路与踩坑记录：[devlog.md](devlog.md)
- 数据库表结构设计：[docs/schema.md](docs/schema.md)
- 进阶能力设计与验收说明：[docs/completion-design.md](docs/completion-design.md) · [docs/acceptance.md](docs/acceptance.md)
