package storage

import (
	"bytes"
	"context"
	"errors"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func testS3(t *testing.T) *S3 {
	t.Helper()
	endpoint := os.Getenv("NETDISK_TEST_S3_ENDPOINT")
	if endpoint == "" {
		if os.Getenv("NETDISK_REQUIRE_INTEGRATION") == "1" {
			t.Fatal("NETDISK_TEST_S3_ENDPOINT required")
		}
		t.Skip("S3 endpoint not configured")
	}
	s, err := NewS3(S3Config{Endpoint: endpoint, AccessKey: "minioadmin", SecretKey: "minioadmin", Bucket: "netdisk-test"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureBucket(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestS3StreamRangeConcatAndAbort(t *testing.T) {
	s := testS3(t)
	ctx := context.Background()
	prefix := uuid.NewString()
	keys := []string{prefix + "/one", prefix + "/two"}
	data := []byte(strings.Repeat("abcdef", 1500000))
	for i, key := range keys {
		body := data
		if i == 1 {
			body = []byte("tail")
		}
		hash, err := s.Put(ctx, key, bytes.NewReader(body), -1)
		if err != nil || hash != wantHash(body) {
			t.Fatalf("Put: %s %v", hash, err)
		}
		t.Cleanup(func() { s.Delete(ctx, key) })
	}
	hash, size, err := s.Concat(ctx, prefix+"/joined", keys)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Delete(ctx, prefix+"/joined")
	want := append(append([]byte{}, data...), []byte("tail")...)
	if size != int64(len(want)) || hash != wantHash(want) {
		t.Fatal("concat size/hash mismatch")
	}
	rc, info, err := s.Open(ctx, prefix+"/joined")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if info.Size != size {
		t.Fatal("stat size")
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Range", "bytes=3-8")
	res := httptest.NewRecorder()
	http.ServeContent(res, req, "file", time.Time{}, rc)
	if res.Code != 206 || res.Body.String() != "defabc" {
		t.Fatalf("Range: %d %q", res.Code, res.Body.String())
	}
	link, err := s.SignedURL(ctx, prefix+"/joined", "中文.txt")
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest("GET", link, nil)
	request.Header.Set("Range", "bytes=0-5")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 206 || string(body) != "abcdef" {
		t.Fatalf("direct Range %d %q %v", response.StatusCode, body, err)
	}
	if _, _, err = s.Concat(ctx, prefix+"/failed", []string{keys[0], prefix + "/missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing source: %v", err)
	}
	if _, err = s.Stat(ctx, prefix+"/failed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial object: %v", err)
	}
	ctxCancel, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.Put(ctxCancel, prefix+"/cancel", bytes.NewReader(data), -1); err == nil {
		t.Fatal("cancel ignored")
	}
	if _, err = s.Put(ctx, prefix+"/empty", strings.NewReader(""), 0); err != nil {
		t.Fatal(err)
	}
	defer s.Delete(ctx, prefix+"/empty")
}
