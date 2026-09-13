package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var ErrObjectNotFound = errors.New("files: object not found")

// ObjectStore is the byte-storage boundary. Implementations must treat a key
// as opaque and must not derive authorization from it; the database row and RLS
// remain the source of tenant ownership.
type ObjectStore interface {
	Put(ctx context.Context, key string, content io.Reader, size int64, contentType string) (string, error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

// LocalStore stores objects beneath a private directory. It is intended for
// development and self-hosted deployments with a persistent mounted volume.
type LocalStore struct {
	root string
}

func NewLocalStore(root string) *LocalStore {
	return &LocalStore{root: root}
}

func (s *LocalStore) Put(_ context.Context, key string, content io.Reader, size int64, _ string) (string, error) {
	path, err := s.path(key)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.root, 0o750); err != nil {
		return "", fmt.Errorf("files: create storage root: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("files: create storage directory: %w", err)
	}
	tmp, err := os.CreateTemp(s.root, ".upload-*")
	if err != nil {
		return "", fmt.Errorf("files: create temporary object: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, hash), content)
	if copyErr != nil {
		cleanup()
		return "", fmt.Errorf("files: write object: %w", copyErr)
	}
	if size >= 0 && written != size {
		cleanup()
		return "", fmt.Errorf("files: upload size changed while reading: expected %d bytes, wrote %d", size, written)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("files: sync object: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("files: close object: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("files: finalize object: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *LocalStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	path, err := s.path(key)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrObjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("files: open object: %w", err)
	}
	return file, nil
}

func (s *LocalStore) Delete(_ context.Context, key string) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("files: delete object: %w", err)
	}
	return nil
}

func (s *LocalStore) path(key string) (string, error) {
	if s.root == "" {
		return "", errors.New("files: local storage root is empty")
	}
	cleanKey := filepath.Clean(filepath.FromSlash(key))
	if cleanKey == "." || filepath.IsAbs(cleanKey) || cleanKey == ".." || strings.HasPrefix(cleanKey, ".."+string(filepath.Separator)) {
		return "", errors.New("files: invalid storage key")
	}
	root, err := filepath.Abs(s.root)
	if err != nil {
		return "", fmt.Errorf("files: resolve storage root: %w", err)
	}
	path := filepath.Join(root, cleanKey)
	if path != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return "", errors.New("files: storage key escapes root")
	}
	return path, nil
}

// S3Store uses MinIO's S3-compatible client, supporting AWS S3, MinIO, and
// compatible providers such as Cloudflare R2 and DigitalOcean Spaces.
type S3Store struct {
	client *minio.Client
	bucket string
}

func NewS3Store(endpoint, region, bucket, accessKey, secretKey string, secure, pathStyle bool) (*S3Store, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = "s3.amazonaws.com"
	}
	endpoint = strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	client, err := minio.New(endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure:       secure,
		Region:       region,
		BucketLookup: bucketLookup(pathStyle),
	})
	if err != nil {
		return nil, fmt.Errorf("files: configure s3 storage: %w", err)
	}
	return &S3Store{client: client, bucket: bucket}, nil
}

func bucketLookup(pathStyle bool) minio.BucketLookupType {
	if pathStyle {
		return minio.BucketLookupPath
	}
	return minio.BucketLookupAuto
}

func (s *S3Store) Put(ctx context.Context, key string, content io.Reader, size int64, contentType string) (string, error) {
	hash := sha256.New()
	_, err := s.client.PutObject(ctx, s.bucket, key, io.TeeReader(content, hash), size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return "", fmt.Errorf("files: put s3 object: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *S3Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("files: get s3 object: %w", err)
	}
	if _, err := object.Stat(); err != nil {
		_ = object.Close()
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("files: stat s3 object: %w", err)
	}
	return object, nil
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("files: delete s3 object: %w", err)
	}
	return nil
}
