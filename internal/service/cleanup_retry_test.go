package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiraiNoShiori/netdisk/internal/storage"
)

type failOneDelete struct {
	storage.Storage
	failed bool
}

func (s *failOneDelete) Delete(ctx context.Context, key string) error {
	if !s.failed {
		s.failed = true
		return errors.New("injected temporary object lock")
	}
	return s.Storage.Delete(ctx, key)
}

func TestCompletedCleanupRetriesStorageFailure(t *testing.T) {
	f := newUploadFixture(t)
	sess, err := f.svc.Create(f.ctx, CreateInput{OwnerID: f.owner, Name: "retry-cleanup", TotalSize: 3, ChunkSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	part, err := f.svc.PutPart(f.ctx, f.owner, sess.ID, 0, strings.NewReader("xyz"))
	if err != nil {
		t.Fatal(err)
	}
	f.svc.storage = &failOneDelete{Storage: f.svc.storage}
	if _, err = f.svc.Complete(f.ctx, f.owner, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.storage.Stat(f.ctx, part.StorageKey); err != nil {
		t.Fatalf("failed deletion should remain tracked: %v", err)
	}
	if err = f.svc.CleanupExpired(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.storage.Stat(f.ctx, part.StorageKey); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cleanup retry failed: %v", err)
	}
}
