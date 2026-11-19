package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"

	"github.com/Miraitowa-kawayi/netdisk/internal/httpx"
	"github.com/Miraitowa-kawayi/netdisk/internal/middleware"
	"github.com/Miraitowa-kawayi/netdisk/internal/service"
	"github.com/google/uuid"
)

// maxJSONBody 限制 JSON 请求体；上传走 multipart 流式路径，不受此限制。
const maxJSONBody = 64 << 10

// decodeJSON 解请求体：MaxBytesReader 限制大小，DisallowUnknownFields 让字段拼写错误立即报错。
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("请求体必须只有一个 JSON 值")
	}
	return nil
}

// userID 取当前登录用户，由 RequireAuth 写入。
func userID(r *http.Request) (uuid.UUID, bool) {
	return middleware.UserIDFromContext(r.Context())
}

// clientIP 提取 TCP 对端 IP（忽略客户端可伪造的代理头）。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// failService 把 service 层错误映射为 HTTP 响应，状态码映射集中在此。
func (s *Server) failService(w http.ResponseWriter, r *http.Request, err error) {
	var v *service.ValidationError

	switch {
	case errors.As(err, &v):
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid(v.Msg))
	case errors.Is(err, service.ErrUsernameTaken):
		httpx.Fail(w, r, s.deps.Logger, httpx.New(http.StatusConflict, "username_taken", err.Error()))
	case errors.Is(err, service.ErrNameConflict):
		httpx.Fail(w, r, s.deps.Logger, httpx.New(http.StatusConflict, "name_conflict", err.Error()))
	case errors.Is(err, service.ErrCycle):
		// 移动成环是资源状态冲突，返回 409。
		httpx.Fail(w, r, s.deps.Logger, httpx.New(http.StatusConflict, "move_cycle", err.Error()))
	case errors.Is(err, service.ErrConflict), errors.Is(err, service.ErrUploadNotPending), errors.Is(err, service.ErrUploadIncomplete):
		// 会话状态、分片不齐、并发迁移或 P2P 信令冲突返回 409。
		httpx.Fail(w, r, s.deps.Logger, httpx.Conflict(err.Error()))
	case errors.Is(err, service.ErrQuotaExceeded):
		httpx.Fail(w, r, s.deps.Logger, httpx.New(http.StatusRequestEntityTooLarge, httpx.CodeQuotaExceeded, "存储空间配额不足，请清理回收站或删除不需要的文件"))
	case errors.Is(err, service.ErrSharePasswordRequired):
		httpx.Fail(w, r, s.deps.Logger, httpx.New(http.StatusForbidden, httpx.CodeSharePasswordRequired, "该分享需要输入提取码"))
	case errors.Is(err, service.ErrSharePasswordInvalid):
		httpx.Fail(w, r, s.deps.Logger, httpx.New(http.StatusForbidden, httpx.CodeSharePasswordInvalid, "分享提取码错误"))
	case errors.Is(err, service.ErrShareLimitReached):
		httpx.Fail(w, r, s.deps.Logger, httpx.New(http.StatusGone, httpx.CodeShareLimitReached, "该分享已达到最大下载次数限制"))
	case errors.Is(err, service.ErrInvalidCredentials):
		httpx.Fail(w, r, s.deps.Logger, httpx.Unauthorized(err.Error()))
	case errors.Is(err, service.ErrNotFound):
		httpx.Fail(w, r, s.deps.Logger, httpx.NotFound(err.Error()))
	case errors.Is(err, service.ErrContentNotStored):
		// 秒传未命中：服务端没有该内容，返回 404。
		httpx.Fail(w, r, s.deps.Logger, httpx.NotFound(err.Error()))
	case errors.Is(err, service.ErrNotAFile), errors.Is(err, service.ErrNotADirectory):
		// 目标类型不匹配（文件夹当文件、文件当文件夹）返回 400。
		httpx.Fail(w, r, s.deps.Logger, httpx.Invalid(err.Error()))
	default:
		// 未识别的错误按 500 处理，细节只写日志。
		httpx.Fail(w, r, s.deps.Logger, err)
	}
}
