package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Miraitowa-kawayi/netdisk/internal/config"
	"github.com/Miraitowa-kawayi/netdisk/internal/repository"
	"github.com/Miraitowa-kawayi/netdisk/internal/service"
	"github.com/Miraitowa-kawayi/netdisk/internal/storage"
	"github.com/Miraitowa-kawayi/netdisk/internal/testutil"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPAccountUploadShareAndIsolation(t *testing.T) {
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
	auth := service.NewAuth(st, service.NewTokens("test-key", time.Hour))
	files := service.NewFiles(st, local, "local", log)
	router := NewRouter(Deps{Cfg: &config.Config{}, Logger: log, Store: st, Auth: auth, Files: files, Uploads: service.NewUploads(st, local, "local", log), Shares: service.NewShares(st, files, log)})
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
	name := "h" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	creds := map[string]string{"username": name, "password": "password123"}
	registered := call("POST", "/api/v1/auth/register", "", creds, 201)
	var ur struct {
		User struct {
			ID           uuid.UUID `json:"id"`
			PasswordHash string    `json:"password_hash"`
		} `json:"user"`
	}
	json.Unmarshal(registered.Body.Bytes(), &ur)
	if ur.User.PasswordHash != "" {
		t.Fatal("password leaked")
	}
	u, _ := st.GetUserByID(ctx, ur.User.ID)
	defer st.DeleteAccount(ctx, u.ID, u.PasswordHash)
	login := call("POST", "/api/v1/auth/login", "", creds, 200)
	var session struct {
		Token string `json:"token"`
	}
	json.Unmarshal(login.Body.Bytes(), &session)
	cookie := login.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("download cookie flags")
	}
	token := session.Token
	created := call("POST", "/api/v1/uploads/", token, map[string]any{"name": "demo.txt", "total_size": 6, "chunk_size": 3}, 201)
	var upload struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	json.Unmarshal(created.Body.Bytes(), &upload)
	base := "/api/v1/uploads/" + upload.Session.ID
	call("PUT", base+"/parts/0", token, "abc", 200)
	call("POST", base+"/complete", token, nil, 409)
	call("PUT", base+"/parts/1", token, "def", 200)
	done := call("POST", base+"/complete", token, nil, 201)
	var node struct {
		File struct {
			ID string `json:"id"`
		} `json:"node"`
	}
	json.Unmarshal(done.Body.Bytes(), &node)
	if node.File.ID == "" {
		t.Fatalf("unexpected complete response: %s", done.Body.String())
	}
	url := "/api/v1/files/" + node.File.ID
	r := httptest.NewRequest("GET", url+"/download", nil)
	r.AddCookie(cookie)
	r.Header.Set("Range", "bytes=1-3")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "bcd" {
		t.Fatalf("download range %d %s", w.Code, w.Body.String())
	}
	call("GET", url+"/download", "", nil, 401)
	share := call("POST", "/api/v1/shares/", token, map[string]any{"node_id": node.File.ID}, 201)
	var sh struct {
		Share struct {
			Token string `json:"token"`
		} `json:"share"`
	}
	json.Unmarshal(share.Body.Bytes(), &sh)
	call("GET", "/api/v1/share/"+sh.Share.Token+"/download", "", nil, 200)
	call("GET", "/api/v1/share/"+sh.Share.Token+"/download?node_id="+uuid.NewString(), "", nil, 404)
	call("POST", "/api/v1/auth/logout", token, nil, 204)
	call("GET", url+"/download", token, nil, 401)
}
