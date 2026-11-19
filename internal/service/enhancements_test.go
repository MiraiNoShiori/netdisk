package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Miraitowa-kawayi/netdisk/internal/repository"
	"github.com/Miraitowa-kawayi/netdisk/internal/storage"
	"github.com/google/uuid"
)

func TestRefreshTokenRotationAndEmailProfile(t *testing.T) {
	f := newUploadFixture(t)
	auth := NewAuthWithOptions(f.store, NewTokens("test-secret", time.Hour), 24*time.Hour, 1024*1024)
	name := "rt" + strings.ReplaceAll(uuid.NewString(), "-", "")[:18]

	u, err := auth.RegisterWithEmail(f.ctx, name, "password123", "Tester", "Test@Example.COM")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer f.cleanDB.Exec(f.ctx, `DELETE FROM users WHERE id = $1`, u.ID)
	if u.Email != "test@example.com" {
		t.Fatalf("email = %q, want test@example.com", u.Email)
	}

	// 非法邮箱应拒绝。
	badEmail := "not-an-email"
	if _, err := auth.ProfileWithEmail(f.ctx, u.ID, "Tester", "bio", &badEmail); err == nil {
		t.Fatal("invalid email accepted")
	}

	_, tok1, err := auth.LoginWithRefresh(f.ctx, name, "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if tok1.RefreshToken == "" {
		t.Fatal("missing refresh token")
	}

	// 轮换 refresh token：旧 refresh token 只能用一次，新 access token 立即生效。
	_, tok2, err := auth.Refresh(f.ctx, tok1.RefreshToken)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if tok2.Token == tok1.Token || tok2.RefreshToken == tok1.RefreshToken {
		t.Fatal("tokens did not rotate")
	}
	if _, err := auth.Authenticate(f.ctx, tok1.Token); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old access token should be revoked after refresh, got %v", err)
	}
	if _, err := auth.Authenticate(f.ctx, tok2.Token); err != nil {
		t.Fatalf("new access token invalid: %v", err)
	}
	if _, _, err := auth.Refresh(f.ctx, tok1.RefreshToken); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("replayed refresh token should fail, got %v", err)
	}
}

func TestQuotaTrashSearchAndStats(t *testing.T) {
	f := newUploadFixture(t)
	auth := NewAuthWithOptions(f.store, NewTokens("test-secret", time.Hour), 24*time.Hour, 20)
	name := "qt" + strings.ReplaceAll(uuid.NewString(), "-", "")[:18]
	u, err := auth.RegisterWithEmail(f.ctx, name, "password123", "QuotaUser", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer f.cleanDB.Exec(f.ctx, `DELETE FROM users WHERE id = $1`, u.ID)

	files := NewFiles(f.store, f.svc.storage, "local", f.svc.logger)

	// 建目录并上传 10 字节文件（配额 20 字节）。
	dir, err := files.CreateDir(f.ctx, u.ID, nil, "Docs")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	n1, err := files.Upload(f.ctx, UploadInput{
		OwnerID:  u.ID,
		ParentID: &dir.ID,
		Name:     "report-2026.txt",
		SizeHint: 10,
		Body:     strings.NewReader("0123456789"),
	})
	if err != nil {
		t.Fatalf("upload 1: %v", err)
	}
	// 同内容秒传（或再次上传）10 字节：逻辑占用 20，物理唯一 10，去重节省 10。
	n2, err := files.Upload(f.ctx, UploadInput{
		OwnerID:  u.ID,
		ParentID: nil,
		Name:     "copy.txt",
		SizeHint: 10,
		Body:     strings.NewReader("0123456789"),
	})
	if err != nil {
		t.Fatalf("upload 2: %v", err)
	}
	// 再传 1 字节超配额，应返回 ErrQuotaExceeded。
	if _, err := files.Upload(f.ctx, UploadInput{
		OwnerID:  u.ID,
		ParentID: nil,
		Name:     "overflow.txt",
		SizeHint: 1,
		Body:     strings.NewReader("x"),
	}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded, got %v", err)
	}

	stats, err := auth.Stats(f.ctx, u.ID)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.UsedBytes != 20 || stats.DedupSavedBytes != 10 || stats.FileCount != 2 || stats.DirCount != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	// 全树搜索 + 分页。
	res, err := files.Query(f.ctx, u.ID, repository.ListFilesOptions{
		FilterByDir: false,
		Query:       "report",
		Limit:       10,
		Offset:      0,
	})
	if err != nil || res.Total != 1 || len(res.Nodes) != 1 || res.Nodes[0].ID != n1.ID {
		t.Fatalf("search got %+v err=%v", res, err)
	}

	// 软删目录进入回收站 -> 列表可见 -> 整棵恢复 -> 再删并彻底清空回收站 -> 释放配额。
	if err := files.Delete(f.ctx, u.ID, dir.ID); err != nil {
		t.Fatalf("soft delete dir: %v", err)
	}
	trash, err := files.ListTrash(f.ctx, u.ID)
	if err != nil || len(trash) != 1 || trash[0].ID != dir.ID || trash[0].Size != 10 {
		t.Fatalf("trash = %+v err=%v", trash, err)
	}
	if _, err := files.RestoreTrash(f.ctx, u.ID, dir.ID, nil, false); err != nil {
		t.Fatalf("restore trash: %v", err)
	}
	dl, err := files.OpenDownload(f.ctx, u.ID, n1.ID)
	if err != nil {
		t.Fatalf("download restored file: %v", err)
	}
	body, _ := io.ReadAll(dl.Content)
	dl.Content.Close()
	if string(body) != "0123456789" {
		t.Fatalf("restored content = %q", body)
	}

	// 删除两个文件并清空回收站，应回收物理对象并恢复可用配额。
	blob, err := f.store.GetBlobByID(f.ctx, *n1.BlobID)
	if err != nil {
		t.Fatalf("get blob: %v", err)
	}
	_ = files.Delete(f.ctx, u.ID, dir.ID)
	_ = files.Delete(f.ctx, u.ID, n2.ID)
	if err := files.EmptyTrash(f.ctx, u.ID); err != nil {
		t.Fatalf("empty trash: %v", err)
	}
	if _, err := f.svc.storage.Stat(f.ctx, blob.StorageKey); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("blob not reclaimed after empty trash: %v", err)
	}
	if _, err := files.Upload(f.ctx, UploadInput{
		OwnerID:  u.ID,
		ParentID: nil,
		Name:     "after-purge.txt",
		SizeHint: 5,
		Body:     strings.NewReader("hello"),
	}); err != nil {
		t.Fatalf("upload after freeing quota: %v", err)
	}
}

func TestSharePasswordLimitAndAnalytics(t *testing.T) {
	f := newShareFixture(t)
	file := f.upload(t, nil, "secret-plan.txt", "TOP SECRET")
	maxDL := int64(1)

	view, err := f.shares.Create(f.ctx, ShareInput{
		OwnerID:      f.owner,
		NodeID:       file.ID,
		Password:     "pass1234",
		MaxDownloads: &maxDL,
	})
	if err != nil {
		t.Fatalf("create protected share: %v", err)
	}
	if !view.Share.HasPassword || view.Share.MaxDownloads == nil || *view.Share.MaxDownloads != 1 {
		t.Fatalf("share flags mismatch: %+v", view.Share)
	}

	// 不带密码 -> ErrSharePasswordRequired；错误密码 -> ErrSharePasswordInvalid。
	if _, err := f.shares.ResolveWithAccess(f.ctx, view.Share.Token, nil, ShareAccessMeta{ClientIP: "127.0.0.1"}); !errors.Is(err, ErrSharePasswordRequired) {
		t.Fatalf("want ErrSharePasswordRequired, got %v", err)
	}
	if _, err := f.shares.ResolveWithAccess(f.ctx, view.Share.Token, nil, ShareAccessMeta{Password: "wrong", ClientIP: "127.0.0.1"}); !errors.Is(err, ErrSharePasswordInvalid) {
		t.Fatalf("want ErrSharePasswordInvalid, got %v", err)
	}

	// 正确密码可浏览并下载 1 次，第 2 次触发 ErrShareLimitReached。
	meta := ShareAccessMeta{Password: "pass1234", ClientIP: "127.0.0.1", UserAgent: "TestAgent"}
	if _, err := f.shares.ResolveWithAccess(f.ctx, view.Share.Token, nil, meta); err != nil {
		t.Fatalf("resolve with password: %v", err)
	}
	dl, err := f.shares.OpenDownloadWithAccess(f.ctx, view.Share.Token, nil, meta)
	if err != nil {
		t.Fatalf("first download: %v", err)
	}
	dl.Content.Close()

	if _, err := f.shares.OpenDownloadWithAccess(f.ctx, view.Share.Token, nil, meta); !errors.Is(err, ErrShareLimitReached) {
		t.Fatalf("second download want ErrShareLimitReached, got %v", err)
	}

	// 分析接口应记录 1 次 auth_failed、1 次 view、1 次 download 与 1 个独立 IP。
	an, err := f.shares.Analytics(f.ctx, f.owner, view.Share.ID)
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if an.FailedAuths != 1 || an.UniqueIPs != 1 || an.Share.DownloadCount != 1 || len(an.RecentLogs) < 3 {
		t.Fatalf("unexpected analytics: %+v", an)
	}
}

func TestLocalPutRespectsContextCancellation(t *testing.T) {
	loc, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := loc.Put(ctx, "blobs/cancelled", strings.NewReader("some data"), -1); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
