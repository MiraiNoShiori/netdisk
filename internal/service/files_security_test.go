package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/MiraiNoShiori/netdisk/internal/repository"
	"github.com/MiraiNoShiori/netdisk/internal/storage"
	"github.com/google/uuid"
)

func TestInstantUploadOwnerAndSize(t *testing.T) {
	f := newZipFixture(t)
	file := f.upload(t, nil, "original", "abc")
	req := InstantInput{OwnerID: f.newUser(t), Name: "copy", Hash: fmt.Sprintf("%x", sha256.Sum256([]byte("abc"))), Size: 3}
	miss := func() {
		t.Helper()
		if _, err := f.files.InstantUpload(f.ctx, req); !errors.Is(err, ErrContentNotStored) {
			t.Fatalf("instant upload: got %v, want ErrContentNotStored", err)
		}
	}
	miss()
	req.OwnerID = f.owner
	req.Size = 2
	miss()
	req.Size = 3
	copyNode, err := f.files.InstantUpload(f.ctx, req)
	if err != nil || *copyNode.BlobID != *file.BlobID {
		t.Fatalf("owner copy: %v %v", copyNode, err)
	}
	for _, n := range []uuid.UUID{file.ID, copyNode.ID} {
		if err := f.files.Delete(f.ctx, f.owner, n); err != nil {
			t.Fatal(err)
		}
	}
	miss()
	f.files.AllowGlobalInstantUpload = true
	req.OwnerID = f.newUser(t)
	if _, err := f.files.InstantUpload(f.ctx, req); err != nil {
		t.Fatal(err)
	}
	req.Size = 0
	miss()
}

func TestUploadStopsAtRemainingQuota(t *testing.T) {
	f := newZipFixture(t)
	if _, err := f.cleanDB.Exec(f.ctx, "UPDATE users SET quota_bytes=6 WHERE id=$1", f.owner); err != nil {
		t.Fatal(err)
	}
	f.upload(t, nil, "used", "abc")
	body := strings.NewReader("1234567890")
	_, err := f.files.Upload(f.ctx, UploadInput{OwnerID: f.owner, Name: "too big", SizeHint: -1, Body: body})
	if !errors.Is(err, ErrQuotaExceeded) || body.Len() != 6 {
		t.Fatalf("upload error=%v unread=%d", err, body.Len())
	}
}

func TestQuotaReaderExactBoundary(t *testing.T) {
	for _, tc := range []struct {
		body    string
		wantErr error
	}{{"abc", nil}, {"abcdef", ErrQuotaExceeded}} {
		data, err := io.ReadAll(&quotaReader{r: strings.NewReader(tc.body), remaining: 3})
		if string(data) != "abc" || !errors.Is(err, tc.wantErr) {
			t.Fatalf("read %q: %q, %v", tc.body, data, err)
		}
	}
}

// 模拟对象写完后客户端正好断开。
type cancelAfterPut struct {
	storage.Storage
	cancel context.CancelFunc
	key    string
}

func (s *cancelAfterPut) Put(ctx context.Context, key string, r io.Reader, size int64) (string, error) {
	hash, err := s.Storage.Put(ctx, key, r, size)
	if err == nil {
		s.key = key
		s.cancel()
	}
	return hash, err
}
func (s *cancelAfterPut) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Storage.Delete(ctx, key)
}

func TestUploadRollbackAfterCancellation(t *testing.T) {
	f := newZipFixture(t)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	st := &cancelAfterPut{Storage: f.files.storage, cancel: cancel}
	f.files.storage = st
	_, err := f.files.Upload(ctx, UploadInput{OwnerID: f.owner, Name: "cancelled", SizeHint: -1, Body: strings.NewReader("abc")})
	if !errors.Is(err, context.Canceled) || st.key == "" {
		t.Fatalf("upload %q: %v", st.key, err)
	}
	if _, err := st.Stat(f.ctx, st.key); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("rollback stat: %v", err)
	}
}

func TestUploadRollbackQueuesFailedDelete(t *testing.T) {
	f := newZipFixture(t)
	existing := f.upload(t, nil, "same", "original")
	f.files.storage = &failOneDelete{Storage: f.files.storage}
	_, err := f.files.Upload(f.ctx, UploadInput{OwnerID: f.owner, Name: existing.Name, SizeHint: -1, Body: strings.NewReader("different")})
	if !errors.Is(err, ErrNameConflict) {
		t.Fatalf("upload conflict: %v", err)
	}
	if _, err := f.store.GetBlobByHash(f.ctx, fmt.Sprintf("%x", sha256.Sum256([]byte("different")))); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("blob rollback: %v", err)
	}
	queued, err := f.store.Garbage(f.ctx)
	if err != nil || len(queued) != 1 || queued[0].Backend != "local" {
		t.Fatalf("queued objects: %v, %v", queued, err)
	}
	uploads := NewUploads(f.store, f.files.storage, "local", f.files.logger)
	if err := uploads.CleanupExpired(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.files.storage.Stat(f.ctx, queued[0].Key); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cleanup retry: %v", err)
	}
}
