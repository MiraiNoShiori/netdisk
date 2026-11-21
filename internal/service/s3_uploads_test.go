package service

import (
	"bytes"
	"errors"
	"github.com/MiraiNoShiori/netdisk/internal/storage"
	"github.com/google/uuid"
	"io"
	"os"
	"testing"
)

func TestS3UploadResumeAfterDefaultSwitch(t *testing.T) {
	endpoint := os.Getenv("NETDISK_TEST_S3_ENDPOINT")
	if endpoint == "" {
		if os.Getenv("NETDISK_REQUIRE_INTEGRATION") == "1" {
			t.Fatal("S3 required")
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
	local := f.svc.storage
	mixed := &storage.Backends{Storage: s3, Default: "s3", Stores: map[string]storage.Storage{"local": local, "s3": s3}}
	uploads := NewUploads(f.store, mixed, "s3", f.svc.logger)
	data := []byte(uuid.NewString())
	sess, err := uploads.Create(f.ctx, CreateInput{OwnerID: f.owner, Name: "s3-resume", TotalSize: int64(len(data)), ChunkSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = uploads.PutPart(f.ctx, f.owner, sess.ID, 0, bytes.NewReader(data[:20])); err != nil {
		t.Fatal(err)
	}
	// 模拟重启后默认新上传改回 local，已有 S3 会话必须继续正确续传。
	mixed.Storage = local
	mixed.Default = "local"
	uploads = NewUploads(f.store, mixed, "local", f.svc.logger)
	if _, err = uploads.PutPart(f.ctx, f.owner, sess.ID, 1, bytes.NewReader(data[20:])); err != nil {
		t.Fatal(err)
	}
	node, err := uploads.Complete(f.ctx, f.owner, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := f.store.GetBlobByID(f.ctx, *node.BlobID)
	if err != nil || blob.Backend != "s3" {
		t.Fatalf("session backend not preserved %v %v", blob, err)
	}
	files := NewFiles(f.store, mixed, "local", f.svc.logger)
	duplicate, err := files.Upload(f.ctx, UploadInput{OwnerID: f.owner, Name: "duplicate", Body: bytes.NewReader(data), SizeHint: -1})
	if err != nil {
		t.Fatal(err)
	}
	if *duplicate.BlobID != *node.BlobID {
		t.Fatal("cross-backend dedup failed")
	}
	dl, err := files.OpenDownload(f.ctx, f.owner, duplicate.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(dl.Content)
	dl.Content.Close()
	if !bytes.Equal(got, data) {
		t.Fatal("wrong content")
	}
	if err = files.Delete(f.ctx, f.owner, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s3.Stat(f.ctx, blob.StorageKey); err != nil {
		t.Fatal("shared blob reclaimed prematurely")
	}
	if err = files.Delete(f.ctx, f.owner, duplicate.ID); err != nil {
		t.Fatal(err)
	}
	// 两份引用都还在回收站里，底层内容要保留，否则恢复出来就是空文件。
	if _, err = s3.Stat(f.ctx, blob.StorageKey); err != nil {
		t.Fatal("trashed reference must keep content")
	}
	if err = files.PurgeTrashItem(f.ctx, f.owner, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s3.Stat(f.ctx, blob.StorageKey); err != nil {
		t.Fatal("shared blob reclaimed prematurely")
	}
	if err = files.PurgeTrashItem(f.ctx, f.owner, duplicate.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s3.Stat(f.ctx, blob.StorageKey); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal("last S3 reference not reclaimed")
	}
}
