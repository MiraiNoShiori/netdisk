// Package handler 是 HTTP 层：路由、参数校验、状态码，不做业务规则。
package handler

import (
	"log/slog"
	"net/http"

	"github.com/MiraiNoShiori/netdisk/internal/config"
	"github.com/MiraiNoShiori/netdisk/internal/handler/web"
	"github.com/MiraiNoShiori/netdisk/internal/httpx"
	"github.com/MiraiNoShiori/netdisk/internal/middleware"
	"github.com/MiraiNoShiori/netdisk/internal/repository"
	"github.com/MiraiNoShiori/netdisk/internal/service"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// Deps 是 HTTP 层需要的依赖。
type Deps struct {
	Cfg     *config.Config
	Logger  *slog.Logger
	Store   *repository.Store
	Auth    *service.Auth
	Files   *service.Files
	Uploads *service.Uploads
	Shares  *service.Shares
	Peers   *service.Peers
}

// Server 持有依赖，各 handler 是它的方法。
type Server struct {
	deps Deps
}

// NewRouter 组装中间件与路由。
func NewRouter(deps Deps) http.Handler {
	if deps.Peers == nil {
		deps.Peers = service.NewPeers()
	}
	s := &Server{deps: deps}

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(middleware.RequestLogger(deps.Logger))
	r.Use(middleware.Recoverer(deps.Logger))
	if deps.Cfg != nil && len(deps.Cfg.CORSOrigins) > 0 {
		r.Use(middleware.CORS(deps.Cfg.CORSOrigins))
	}
	r.Use(middleware.AuthLimit(deps.Logger))
	// 不挂 chi 的 Timeout / RequestSize 中间件：上传是大体积长连接，统一限时会掐断大文件。
	// 超时策略放在单条路由上按需设置。

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, r, deps.Logger, httpx.NotFound("no such endpoint"))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		code := http.StatusMethodNotAllowed
		httpx.Fail(w, r, deps.Logger, httpx.New(code, httpx.CodeInvalidRequest, "method not allowed"))
	})

	// healthz 不碰依赖；readyz 要求数据库可达。
	r.Get("/healthz", s.healthz)
	r.Get("/readyz", s.readyz)

	// /api/v1：auth 公开，其余需 Bearer token。
	requireAuth := middleware.RequireAuth(deps.Logger, deps.Auth)

	r.Route("/api/v1", func(r chi.Router) {
		r.Route("/auth", func(r chi.Router) {
			r.Post("/register", s.authRegister)
			r.Post("/login", s.authLogin)
			r.Post("/refresh", s.authRefresh)
			r.Group(func(r chi.Router) {
				r.Use(requireAuth)
				r.Get("/me", s.authMe)
				r.Get("/stats", s.authStats)
				r.Post("/logout", s.logout)
				r.Patch("/me", s.profile)
				r.Post("/password", s.password)
				r.Delete("/me", s.deleteAccount)
			})
		})

		r.Route("/p2p/shares", func(r chi.Router) {
			r.Use(requireAuth)
			r.Post("/", s.createPeer)
			r.Get("/", s.listPeers)
			r.Get("/{id}", s.getPeer)
			r.Delete("/{id}", s.deletePeer)
			r.Post("/{id}/offer", s.offerPeer)
		})
		r.Get("/p2p/{token}", s.resolvePeer)
		r.Post("/p2p/{token}/answer", s.answerPeer)

		r.Route("/files", func(r chi.Router) {
			downloadAuth := middleware.RequireDownloadAuth(deps.Logger, deps.Auth)
			r.With(downloadAuth).Get("/{id}/download", s.downloadFile) // ServeContent 提供 Range/206，支持 ?inline=1 安全预览
			r.With(downloadAuth).Get("/{id}/zip", s.downloadZip)       // 整棵子树打包成 zip（单查询流式，无 Range）
			r.Group(func(r chi.Router) {
				r.Use(requireAuth)
				r.Post("/", s.uploadFile)           // multipart 上传，流式
				r.Post("/dirs", s.createDir)        // 新建文件夹（JSON）
				r.Post("/instant", s.instantUpload) // 秒传：报 hash，命中则不收字节（JSON）
				r.Get("/folders", s.listFolders)    // 所有目录列表，供目录树选择器
				r.Get("/", s.listFiles)             // 支持 parent_id / q / type / sort / order / limit / offset
				r.Get("/{id}/ancestors", s.fileAncestors)
				r.Post("/{id}/migrate", s.migrateFile)
				r.Get("/{id}/direct-url", s.directURL)
				r.Patch("/{id}", s.patchFile)   // 改名和/或移动
				r.Delete("/{id}", s.deleteFile) // 移入回收站；?permanent=true 立即彻底删除
			})
		})

		// 回收站：列出、恢复子树、彻底删除单项、一键清空。
		r.Route("/trash", func(r chi.Router) {
			r.Use(requireAuth)
			r.Get("/", s.listTrash)
			r.Post("/{id}/restore", s.restoreTrash)
			r.Delete("/{id}", s.purgeTrashItem)
			r.Delete("/", s.emptyTrash)
		})

		// 存储策略与热点分层。
		r.Route("/storage", func(r chi.Router) {
			r.Use(requireAuth)
			r.Get("/policy", s.storagePolicy)
			r.Post("/tier/run", s.runAutoTier)
		})

		// 断点续传：分片上传会话。
		r.Route("/uploads", func(r chi.Router) {
			r.Use(requireAuth)
			r.Post("/", s.createUpload)                     // 开会话：name / parent_id / total_size / chunk_size
			r.Get("/", s.listUploads)                       // 列 pending 会话
			r.Get("/{id}", s.uploadSessionStatus)           // 进度：已收到 / 还缺哪些片
			r.Put("/{id}/parts/{part_no}", s.putUploadPart) // 传一片（原始字节，流式）
			r.Post("/{id}/complete", s.completeUpload)      // 收尾：按序合并 + 建节点
			r.Delete("/{id}", s.abortUpload)                // 放弃：清理分片
		})

		// 分享：管理需登录；匿名访问走 /share/{token}，不挂 requireAuth，校验在 service。
		r.Route("/shares", func(r chi.Router) {
			r.Use(requireAuth)
			r.Post("/", s.createShare) // 建：{node_id, expires_in_seconds?, password?, max_downloads?}
			r.Get("/", s.listShares)   // 列自己建的分享
			r.Get("/{id}/analytics", s.shareAnalytics)
			r.Delete("/{id}", s.deleteShare) // 撤销
		})
		r.Post("/share/{token}/verify", s.verifyShare)       // 验证提取码并签发免密 Cookie
		r.Get("/share/{token}", s.resolveShare)              // 匿名打开（文件/文件夹元信息）
		r.Get("/share/{token}/download", s.downloadViaShare) // 匿名下载（?node_id= 指定子项，?inline=1 预览）
	})

	r.Get("/favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	r.Handle("/*", http.FileServer(http.FS(web.Files)))
	return r
}
