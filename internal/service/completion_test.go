package service

import (
	"bytes"
	"context"
	"errors"
	"github.com/Miraitowa-kawayi/netdisk/internal/storage"
	"github.com/google/uuid"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAccountSessionLifecycle(t *testing.T) {
	f := newUploadFixture(t)
	a := NewAuth(f.store, NewTokens("test-secret", time.Hour))
	name := "u" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	u, err := a.Register(f.ctx, name, "password123", "")
	if err != nil {
		t.Fatal(err)
	}
	defer f.cleanDB.Exec(f.ctx, `DELETE FROM users WHERE id=$1`, u.ID)
	_, one, _, err := a.Login(f.ctx, name, "password123")
	if err != nil {
		t.Fatal(err)
	}
	_, two, _, err := a.Login(f.ctx, name, "password123")
	if err != nil || one == two {
		t.Fatalf("distinct sessions %v", err)
	}
	if _, err = a.Authenticate(f.ctx, one); err != nil {
		t.Fatal(err)
	}
	if err = a.Logout(f.ctx, one); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Authenticate(f.ctx, one); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("logout did not revoke")
	}
	if _, err = a.Authenticate(f.ctx, two); err != nil {
		t.Fatal(err)
	}
	if err = a.Password(f.ctx, u.ID, "password123", "newPassword123"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Authenticate(f.ctx, two); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("password did not revoke all")
	}
	_, three, _, err := a.Login(f.ctx, name, "newPassword123")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Delete(f.ctx, u.ID, "newPassword123"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Authenticate(f.ctx, three); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("deleted account token active")
	}
}
func TestUploadInvalidRetryExpiryAndConcurrentComplete(t *testing.T) {
	f := newUploadFixture(t)
	sess, err := f.svc.Create(f.ctx, CreateInput{OwnerID: f.owner, Name: "test", TotalSize: 3, ChunkSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	part, err := f.svc.PutPart(f.ctx, f.owner, sess.ID, 0, strings.NewReader("abc"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.PutPart(f.ctx, f.owner, sess.ID, 0, strings.NewReader("wrong")); err == nil {
		t.Fatal("oversize accepted")
	}
	rc, _, err := f.svc.storage.Open(f.ctx, part.StorageKey)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(rc)
	rc.Close()
	if string(body) != "abc" {
		t.Fatal("invalid retry destroyed prior part")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.svc.Complete(f.ctx, f.owner, sess.ID); errs <- e }()
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrUploadNotPending) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("completed %d times", success)
	}
	expired, err := f.svc.Create(f.ctx, CreateInput{OwnerID: f.owner, Name: "expired", TotalSize: 1, ChunkSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.svc.PutPart(f.ctx, f.owner, expired.ID, 0, strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.cleanDB.Exec(f.ctx, `UPDATE upload_sessions SET expires_at=now()-interval '1 second' WHERE id=$1`, expired.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Complete(f.ctx, f.owner, expired.ID); !errors.Is(err, ErrUploadNotPending) {
		t.Fatal("expired session completed")
	}
	if err = f.svc.CleanupExpired(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.storage.Stat(f.ctx, p.StorageKey); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal("expired part not cleaned")
	}
}
func TestPeerIsolationExpiryAndSingleReceiver(t *testing.T) {
	p := NewPeers()
	owner := uuid.New()
	r, err := p.Create(owner, "demo", 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Get(uuid.New(), r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("room owner isolation")
	}
	if err = p.Offer(owner, r.ID, "v=0\r\no=offer"); err != nil {
		t.Fatal(err)
	}
	if err = p.Answer(r.Token, "v=0\r\no=answer"); err != nil {
		t.Fatal(err)
	}
	if err = p.Answer(r.Token, "v=0\r\no=other"); !errors.Is(err, ErrConflict) {
		t.Fatal("answer overwritten")
	}
	view, _ := p.Resolve(r.Token)
	if view.Answer != "" {
		t.Fatal("answer leaked")
	}
	p.now = func() time.Time { return r.ExpiresAt.Add(time.Second) }
	if _, err = p.Resolve(r.Token); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired room visible")
	}
}
func TestMigrateMixedBackends(t *testing.T) {
	endpoint := os.Getenv("NETDISK_TEST_S3_ENDPOINT")
	if endpoint == "" {
		if os.Getenv("NETDISK_REQUIRE_INTEGRATION") == "1" {
			t.Fatal("S3 endpoint required")
		}
		t.Skip("S3 not configured")
	}
	f := newUploadFixture(t)
	s3, err := storage.NewS3(storage.S3Config{Endpoint: endpoint, AccessKey: "minioadmin", SecretKey: "minioadmin", Bucket: "netdisk-test"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s3.EnsureBucket(f.ctx); err != nil {
		t.Fatal(err)
	}
	mixed := &storage.Backends{Storage: f.svc.storage, Default: "local", Stores: map[string]storage.Storage{"local": f.svc.storage, "s3": s3}}
	files := NewFiles(f.store, mixed, "local", f.svc.logger)
	content := []byte(uuid.NewString())
	node, err := files.Upload(f.ctx, UploadInput{OwnerID: f.owner, Name: "migrate.txt", SizeHint: -1, Body: bytes.NewReader(content)})
	if err != nil {
		t.Fatal(err)
	}
	if err = files.Migrate(f.ctx, f.owner, node.ID, "s3"); err != nil {
		t.Fatal(err)
	}
	blob, err := f.store.GetBlobByID(f.ctx, *node.BlobID)
	if err != nil || blob.Backend != "s3" {
		t.Fatalf("backend %v %v", blob, err)
	}
	dl, err := files.OpenDownload(f.ctx, f.owner, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(dl.Content)
	dl.Content.Close()
	if !bytes.Equal(got, content) {
		t.Fatal("migrated content changed")
	}
	if _, err = files.DirectURL(f.ctx, f.owner, node.ID); err != nil {
		t.Fatal(err)
	}
	if err = files.Delete(context.Background(), f.owner, node.ID); err != nil {
		t.Fatal(err)
	}
	// 移入回收站后底层内容仍保留以便恢复，彻底删除后再从 S3 回收。
	if _, err = s3.Stat(f.ctx, blob.StorageKey); err != nil {
		t.Fatalf("soft-deleted S3 content should remain in trash: %v", err)
	}
	if err = files.PurgeTrashItem(context.Background(), f.owner, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s3.Stat(f.ctx, blob.StorageKey); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("S3 content not reclaimed after purge: %v", err)
	}
}
