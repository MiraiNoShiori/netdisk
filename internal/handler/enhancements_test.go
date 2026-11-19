package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Miraitowa-kawayi/netdisk/internal/config"
	"github.com/Miraitowa-kawayi/netdisk/internal/repository"
	"github.com/Miraitowa-kawayi/netdisk/internal/service"
	"github.com/Miraitowa-kawayi/netdisk/internal/storage"
	"github.com/Miraitowa-kawayi/netdisk/internal/testutil"
	"github.com/google/uuid"
)

func TestSafeInlineMIMEBlocksXSS(t *testing.T) {
	if _, ok := safeInlineMIME("evil.html"); ok {
		t.Fatal("HTML must not be served inline")
	}
	if _, ok := safeInlineMIME("vector.svg"); ok {
		t.Fatal("SVG must not be served inline")
	}
	if mt, ok := safeInlineMIME("photo.png"); !ok || mt != "image/png" {
		t.Fatalf("png got %q %v", mt, ok)
	}
	if mt, ok := safeInlineMIME("notes.md"); !ok || mt != "text/plain; charset=utf-8" {
		t.Fatalf("md got %q %v", mt, ok)
	}
}

func TestHTTPEnhancementsEndToEnd(t *testing.T) {
	dsn := testutil.Database(t)
	ctx := context.Background()
	st, err := repository.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	local, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{JWTSecret: "test-key", CORSOrigins: []string{"https://ui.example.com"}}
	auth := service.NewAuthWithOptions(st, service.NewTokens(cfg.JWTSecret, time.Hour), 24*time.Hour, 10<<20)
	files := service.NewFiles(st, local, "local", log)
	router := NewRouter(Deps{
		Cfg:     cfg,
		Logger:  log,
		Store:   st,
		Auth:    auth,
		Files:   files,
		Uploads: service.NewUploads(st, local, "local", log),
		Shares:  service.NewShares(st, files, log),
	})

	call := func(method, path, token string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		var data io.Reader
		if v, ok := body.(string); ok {
			data = strings.NewReader(v)
		} else if body != nil {
			b, _ := json.Marshal(body)
			data = bytes.NewReader(b)
		}
		r := httptest.NewRequest(method, path, data)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}

	username := "e2e" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	reg := call("POST", "/api/v1/auth/register", "", map[string]any{
		"username": username,
		"password": "password123",
		"email":    "dev@example.com",
	}, 201)
	var ur struct {
		User struct {
			ID uuid.UUID `json:"id"`
		} `json:"user"`
	}
	_ = json.Unmarshal(reg.Body.Bytes(), &ur)
	u, _ := st.GetUserByID(ctx, ur.User.ID)
	defer st.DeleteAccount(ctx, u.ID, u.PasswordHash)

	login := call("POST", "/api/v1/auth/login", "", map[string]string{"username": username, "password": "password123"}, 200)
	var sess struct {
		Token        string `json:"token"`
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.Unmarshal(login.Body.Bytes(), &sess)
	if sess.RefreshToken == "" {
		t.Fatal("missing refresh token")
	}

	// 刷新会话。
	refreshed := call("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": sess.RefreshToken}, 200)
	_ = json.Unmarshal(refreshed.Body.Bytes(), &sess)
	token := sess.Token

	// 上传文本文件并验证 content_hash 兼容字段。
	up := call("POST", "/api/v1/uploads/", token, map[string]any{
		"name":         "readme.md",
		"total_size":   5,
		"chunk_size":   5,
		"content_hash": "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
	}, 201)
	var upResp struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	_ = json.Unmarshal(up.Body.Bytes(), &upResp)
	call("PUT", "/api/v1/uploads/"+upResp.Session.ID+"/parts/0", token, "hello", 200)
	done := call("POST", "/api/v1/uploads/"+upResp.Session.ID+"/complete", token, nil, 201)
	var nodeResp struct {
		Node struct {
			ID string `json:"id"`
		} `json:"node"`
	}
	_ = json.Unmarshal(done.Body.Bytes(), &nodeResp)

	// 安全内联预览。
	preview := call("GET", "/api/v1/files/"+nodeResp.Node.ID+"/download?inline=1", token, nil, 200)
	if !strings.HasPrefix(preview.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("inline mime = %q", preview.Header().Get("Content-Type"))
	}

	// 配额统计、搜索分页与存储策略接口。
	call("GET", "/api/v1/auth/stats", token, nil, 200)
	call("GET", "/api/v1/files/?q=readme&limit=10&offset=0", token, nil, 200)
	call("GET", "/api/v1/storage/policy", token, nil, 200)

	// 带提取码和下载次数限制的分享 + verify Cookie + 分析接口。
	shResp := call("POST", "/api/v1/shares/", token, map[string]any{
		"node_id":       nodeResp.Node.ID,
		"password":      "code99",
		"max_downloads": 2,
	}, 201)
	var sh struct {
		Share struct {
			ID    string `json:"id"`
			Token string `json:"token"`
		} `json:"share"`
	}
	_ = json.Unmarshal(shResp.Body.Bytes(), &sh)
	call("GET", "/api/v1/share/"+sh.Share.Token, "", nil, 403)
	verify := call("POST", "/api/v1/share/"+sh.Share.Token+"/verify", "", map[string]string{"password": "code99"}, 200)
	var unlockCookie *http.Cookie
	for _, c := range verify.Result().Cookies() {
		if strings.HasPrefix(c.Name, "netdisk_share_") {
			unlockCookie = c
		}
	}
	if unlockCookie == nil {
		t.Fatal("missing share unlock cookie")
	}
	req := httptest.NewRequest("GET", "/api/v1/share/"+sh.Share.Token+"/download", nil)
	req.AddCookie(unlockCookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 || w.Body.String() != "hello" {
		t.Fatalf("cookie share download = %d %q", w.Code, w.Body.String())
	}
	call("GET", "/api/v1/shares/"+sh.Share.ID+"/analytics", token, nil, 200)

	// 回收站：软删 -> 列表 -> 恢复 -> 永久删除。
	call("DELETE", "/api/v1/files/"+nodeResp.Node.ID, token, nil, 204)
	call("GET", "/api/v1/trash/", token, nil, 200)
	call("POST", "/api/v1/trash/"+nodeResp.Node.ID+"/restore", token, nil, 200)
	call("DELETE", "/api/v1/files/"+nodeResp.Node.ID+"?permanent=true", token, nil, 204)
}
