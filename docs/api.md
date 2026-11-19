# API

默认 `http://localhost:8081`。除健康检查、注册/登录/刷新会话和匿名分享/信令外，均使用 `Authorization: Bearer <token>`（GET/HEAD 也接受登录下发的 HttpOnly `netdisk_download` Cookie）。

| 方法与路径 | 用途 / 请求 |
| --- | --- |
| `GET /healthz`、`/readyz` | 存活检查 / 数据库就绪检查 |
| `POST /api/v1/auth/register` | `{username, password, nickname?, email?}` |
| `POST /api/v1/auth/login` | `{username, password}` → `token` / `expires_at` / `refresh_token` / `refresh_expires_at` / `user` |
| `POST /api/v1/auth/refresh` | `{refresh_token}` → 原子轮换新 `token` 与 `refresh_token` |
| `GET /api/v1/auth/me` | 当前用户信息（含 `email`、`quota_bytes`） |
| `GET /api/v1/auth/stats` | 用户空间统计（已用、回收站、文件/目录数、Local/S3 分布、去重节省字节） |
| `PATCH /api/v1/auth/me` | `{nickname, bio, email?}` |
| `POST /api/v1/auth/logout` | 吊销当前会话 |
| `POST /api/v1/auth/password` | `{old_password, new_password}`；吊销该用户所有会话 |
| `DELETE /api/v1/auth/me` | `{password}`；删除账号及资源并异步回收物理内容 |
| `GET /api/v1/files/` | 支持 `?parent_id=`、`?q=`（全盘搜索）、`?scope=dir`、`?type=all\|file\|dir`、`?sort=name\|size\|updated_at\|created_at`、`?order=asc\|desc`、`?limit=`、`?offset=` → `files` / `total` / `limit` / `offset` / `has_more` |
| `GET /api/v1/files/folders` | 返回当前用户所有存活文件夹，供可视化目录树移动 |
| `GET /api/v1/files/{id}/ancestors` | 返回节点自顶向下的祖先链（面包屑定位） |
| `POST /api/v1/files/` | `multipart/form-data` 流式上传（校验配额） |
| `POST /api/v1/files/dirs` | `{name, parent_id?}` 新建文件夹 |
| `PATCH /api/v1/files/{id}` | `{name?, parent_id?}` 改名和/或移动（`null` 移至根目录，带环检测） |
| `DELETE /api/v1/files/{id}` | 移入回收站（整棵子树可恢复）；带 `?permanent=true` 时立即彻底删除并回收孤儿内容 |
| `GET /api/v1/files/{id}/download` | 支持 Range / 206；`?inline=1` 支持安全白名单内联预览（拦截 HTML/SVG 防 XSS） |
| `GET /api/v1/files/{id}/zip` | 单次递归 CTE 查询 + 流式打包 ZIP（无 Range） |
| `POST /api/v1/files/instant` | `{name, parent_id?, hash, size}` 秒传；未命中返回 404 |
| `POST /api/v1/files/{id}/migrate` | `{backend:"s3"\|"local"}` 双向校验迁移，204 |
| `GET /api/v1/files/{id}/direct-url` | S3 5 分钟签名直链；local 返回 400 |
| `GET /api/v1/trash/` | 列出回收站顶层删除项（文件夹含子树总大小） |
| `POST /api/v1/trash/{id}/restore` | 恢复单项及整棵子树（可选 `{parent_id}` 指定恢复目录） |
| `DELETE /api/v1/trash/{id}` | 彻底删除回收站单项并回收孤儿内容 |
| `DELETE /api/v1/trash/` | 清空回收站并回收孤儿内容 |
| `GET /api/v1/storage/policy` | 查询已装配存储后端与热点分层配置 |
| `POST /api/v1/storage/tier/run` | `{threshold?, min_size?}` 手动触发高频访问本地文件迁至 S3 |
| `POST /api/v1/uploads/` | `{name, parent_id?, total_size, chunk_size, hash?\|content_hash?}` 开分片会话 |
| `GET /api/v1/uploads/` | 列出未过期 `pending` 会话 |
| `GET /api/v1/uploads/{id}` | 查询 `session` / `received_parts` / `missing_parts` |
| `PUT /api/v1/uploads/{id}/parts/{part_no}` | 流式上传单片（片号从 0 起） |
| `POST /api/v1/uploads/{id}/complete` | 校验并按序拼接合并建节点；缺片返回 409 |
| `DELETE /api/v1/uploads/{id}` | 放弃会话并清理分片 |
| `POST /api/v1/shares/` | `{node_id, expires_in_seconds?, password?, max_downloads?}` 创建分享 |
| `GET /api/v1/shares/` | 列出我的分享（含目标名称、提取码标记、浏览与下载计数） |
| `GET /api/v1/shares/{id}/analytics` | 分享访问分析（独立 IP、错误密码次数、近 7 天趋势、最近 50 条访问日志） |
| `DELETE /api/v1/shares/{id}` | 撤销分享 |
| `POST /api/v1/share/{token}/verify` | `{password}` 验证提取码并签发 HMAC 签名免密 Cookie（带限流） |
| `GET /api/v1/share/{token}` | 匿名浏览分享元数据与目录子项（支持 `X-Share-Password`、`?password=` 或免密 Cookie） |
| `GET /api/v1/share/{token}/download` | 匿名下载或 `?inline=1` 预览（校验提取码并原子扣减下载次数上限） |
| `POST /api/v1/p2p/shares/` | `{name, size}` 创建 WebRTC P2P 信令房间 |
| `GET /api/v1/p2p/shares/` | 我的 P2P 分享 |
| `GET /api/v1/p2p/shares/{id}` | 发送方轮询 answer |
| `POST /api/v1/p2p/shares/{id}/offer` | `{sdp}` 完整 ICE Offer SDP |
| `DELETE /api/v1/p2p/shares/{id}` | 撤销 P2P 分享 |
| `GET /api/v1/p2p/{token}` | 匿名获取 Offer 与文件元信息 |
| `POST /api/v1/p2p/{token}/answer` | `{sdp}` 提交 Answer SDP（仅接受单个不同 Answer，冲突返回 409） |

## 统一错误码

统一格式 `{"error":{"code":"...","message":"..."}}`：

`invalid_request`、`unauthorized`、`forbidden`、`not_found`、`name_conflict`、`username_taken`、`move_cycle`、`conflict`、`quota_exceeded`（413）、`share_password_required`（403）、`share_password_invalid`（403）、`share_limit_reached`（410）、`rate_limited`（429）、`internal_error`。
