package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 使用固定分片和单并发上传，内存占用不随对象大小增长。
type S3 struct {
	client *minio.Client
	signer *minio.Client
	bucket string
}
type S3Config struct {
	Endpoint, PublicEndpoint, AccessKey, SecretKey, Bucket, Region string
	Secure                                                         bool
}

func NewS3(c S3Config) (*S3, error) {
	if c.Endpoint == "" || c.Bucket == "" || c.AccessKey == "" || c.SecretKey == "" {
		return nil, errors.New("S3 endpoint/bucket/credentials required")
	}
	opts := &minio.Options{Creds: credentials.NewStaticV4(c.AccessKey, c.SecretKey, ""), Secure: c.Secure, Region: c.Region}
	client, err := minio.New(c.Endpoint, opts)
	if err != nil {
		return nil, err
	}
	signer := client
	if c.PublicEndpoint != "" {
		signer, err = minio.New(c.PublicEndpoint, opts)
		if err != nil {
			return nil, err
		}
	}
	return &S3{client: client, signer: signer, bucket: c.Bucket}, nil
}

func (s *S3) EnsureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	err = s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{})
	if code := minio.ToErrorResponse(err).Code; code == "BucketAlreadyOwnedByYou" {
		return nil
	}
	return err
}

func s3Error(err error) error {
	switch minio.ToErrorResponse(err).Code {
	case "NoSuchKey", "NoSuchObject", "NotFound":
		return ErrNotFound
	}
	return err
}

func (s *S3) Put(ctx context.Context, key string, r io.Reader, size int64) (string, error) {
	h := sha256.New()
	// size 是提示；一直读到 EOF，让上层能检测超长分片。SDK 失败会中止 multipart。
	_, err := s.client.PutObject(ctx, s.bucket, key, io.TeeReader(r, h), -1, minio.PutObjectOptions{PartSize: 8 << 20, NumThreads: 1, ContentType: "application/octet-stream"})
	if err != nil {
		return "", s3Error(err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (s *S3) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return ObjectInfo{}, s3Error(err)
	}
	return ObjectInfo{Key: key, Size: info.Size}, nil
}
func (s *S3) Open(ctx context.Context, key string) (io.ReadSeekCloser, ObjectInfo, error) {
	info, err := s.Stat(ctx, key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, ObjectInfo{}, s3Error(err)
	}
	return obj, info, nil
}
func (s *S3) Delete(ctx context.Context, key string) error {
	if _, err := s.Stat(ctx, key); err != nil {
		return err
	}
	return s3Error(s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}))
}
func (s *S3) Concat(ctx context.Context, dst string, keys []string) (string, int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	done := make(chan struct{})
	var total int64
	go func() {
		defer close(done)
		var err error
		for _, key := range keys {
			var rc io.ReadSeekCloser
			rc, _, err = s.Open(ctx, key)
			if err != nil {
				break
			}
			var n int64
			n, err = io.Copy(pw, rc)
			closeErr := rc.Close()
			total += n
			if err == nil {
				err = closeErr
			}
			if err != nil {
				break
			}
		}
		_ = pw.CloseWithError(err)
	}()
	hash, err := s.Put(ctx, dst, pr, -1)
	_ = pr.CloseWithError(err)
	cancel()
	<-done
	if err != nil {
		return "", 0, fmt.Errorf("concat: %w", err)
	}
	return hash, total, nil
}

// SignedURL 的有效期限制在五分钟内；吊销分享不会撤销已经签发的 URL。
func (s *S3) SignedURL(ctx context.Context, key, name string) (string, error) {
	ascii := make([]rune, 0, len(name))
	for _, r := range name {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			ascii = append(ascii, '-')
		} else {
			ascii = append(ascii, r)
		}
	}
	params := url.Values{"response-content-disposition": {fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, string(ascii), url.PathEscape(name))}}
	u, err := s.signer.PresignedGetObject(ctx, s.bucket, key, 5*time.Minute, params)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
