// Package storage is the S3 / MinIO adapter used by the file service to
// persist user uploads. The on-disk layout is intentionally simple — one
// bucket, one object per (user, file) pair — so that S3 keys are completely
// decoupled from the virtual path tree the user sees in `mem ls`.
//
// Key format (set by callers, not by this package):
//   users/<user_id>/<file_id>/<name>
//
// Renames and folder moves are pure DB updates — they never touch S3.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Store is the S3 client + bucket binding used by the file service.
type Store struct {
	client *minio.Client
	bucket string
}

// New constructs a Store. `endpoint` is host[:port] without scheme — TLS is
// chosen by `useSSL`. The bucket is created on first use if missing so that
// `docker compose up` works on a fresh MinIO without manual setup.
func New(ctx context.Context, endpoint, accessKey, secretKey, bucket, region string, useSSL bool) (*Store, error) {
	if endpoint == "" {
		return nil, errors.New("storage: endpoint is empty")
	}
	if bucket == "" {
		return nil, errors.New("storage: bucket is empty")
	}
	cli, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("minio client: %w", err)
	}
	exists, err := cli.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("bucket exists check: %w", err)
	}
	if !exists {
		if err := cli.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: region}); err != nil {
			return nil, fmt.Errorf("create bucket %q: %w", bucket, err)
		}
	}
	return &Store{client: cli, bucket: bucket}, nil
}

// Bucket returns the bucket name.
func (s *Store) Bucket() string { return s.bucket }

// Put streams `body` into the bucket under `key`. If `size` is unknown, pass
// -1 — the client will use chunked uploads.
func (s *Store) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, body, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}

// Get returns a reader for the object at `key`. Caller must Close.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("s3 get %s: %w", key, err)
	}
	// GetObject returns lazily; force a Stat to surface NotFound now.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		return nil, fmt.Errorf("s3 stat %s: %w", key, err)
	}
	return obj, nil
}

// Delete removes the object at `key`. Missing-object errors are swallowed so
// the caller can use this as best-effort cleanup.
func (s *Store) Delete(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}
