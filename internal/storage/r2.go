package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// R2Config configures an S3-compatible backend (Cloudflare R2 or self-hosted
// Garage/Ceph/etc. — the same minio-go S3 client serves all of them).
type R2Config struct {
	Endpoint  string // host only, e.g. "<accountid>.r2.cloudflarestorage.com"
	Region    string // "auto" for R2
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool
	PathStyle bool // force path-style URLs (most self-hosted S3); R2 uses virtual-host
}

// R2 is an S3-compatible blob store. Objects are keyed "<sha256>.<ext>" to match
// the existing deg-blossom-storage bucket. All calls go through a circuit breaker
// so an R2/S3 outage fails fast instead of hanging.
type R2 struct {
	client *minio.Client
	bucket string
	br     *breaker
}

// NewR2 constructs an S3-compatible Storage.
func NewR2(cfg R2Config) (*R2, error) {
	endpoint := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(cfg.Endpoint, "https://"), "http://"), "/")
	region := cfg.Region
	if region == "" {
		region = "auto"
	}
	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: region,
	}
	if cfg.PathStyle {
		opts.BucketLookup = minio.BucketLookupPath
	}
	client, err := minio.New(endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("s3: new client: %w", err)
	}
	return &R2{client: client, bucket: cfg.Bucket, br: newBreaker()}, nil
}

func blobKey(sha256, ext string) string {
	ext = strings.TrimPrefix(ext, ".")
	if ext == "" {
		return sha256
	}
	return sha256 + "." + ext
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	resp := minio.ToErrorResponse(err)
	return resp.StatusCode == 404 || resp.Code == "NoSuchKey"
}

// resolveKey finds the actual object key for a bare hash by listing the prefix.
func (r *R2) resolveKey(ctx context.Context, sha256 string) (string, error) {
	for obj := range r.client.ListObjects(ctx, r.bucket, minio.ListObjectsOptions{Prefix: sha256, MaxKeys: 2}) {
		if obj.Err != nil {
			return "", obj.Err
		}
		return obj.Key, nil
	}
	return "", ErrNotFound
}

// keyFor resolves the stored key for (sha256, ext), falling back to a prefix
// lookup when ext is empty or does not match the stored object.
func (r *R2) keyFor(ctx context.Context, sha256, ext string) (string, error) {
	if ext == "" {
		return r.resolveKey(ctx, sha256)
	}
	key := blobKey(sha256, ext)
	if _, err := r.client.StatObject(ctx, r.bucket, key, minio.StatObjectOptions{}); err == nil {
		return key, nil
	} else if isNotFound(err) {
		if k, rerr := r.resolveKey(ctx, sha256); rerr == nil {
			return k, nil
		}
		return "", ErrNotFound
	} else {
		return "", err
	}
}

// Load streams the blob from R2. Key resolution is breaker-guarded (a real
// round-trip); GetObject itself is lazy, so it runs on the caller's context —
// bounding it with the op timeout would cancel the stream mid-download.
func (r *R2) Load(ctx context.Context, sha256, ext string) (io.ReadSeekCloser, error) {
	var key string
	if err := r.br.guard(ctx, breakerOpTimeout, func(c context.Context) error {
		k, err := r.keyFor(c, sha256, ext)
		if err != nil {
			return err
		}
		key = k
		return nil
	}); err != nil {
		return nil, err
	}
	obj, err := r.client.GetObject(ctx, r.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	return obj, nil // *minio.Object implements io.ReadSeekCloser
}

// Store writes the blob (streamed) under "<sha256>.<ext>".
func (r *R2) Store(ctx context.Context, sha256, ext, contentType string, rd io.Reader, size int64) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return r.br.guard(ctx, breakerWriteTimeout, func(c context.Context) error {
		_, err := r.client.PutObject(c, r.bucket, blobKey(sha256, ext), rd, size, minio.PutObjectOptions{ContentType: contentType})
		return err
	})
}

// Delete removes the blob (absent is not an error).
func (r *R2) Delete(ctx context.Context, sha256, ext string) error {
	return r.br.guard(ctx, breakerOpTimeout, func(c context.Context) error {
		key, err := r.keyFor(c, sha256, ext)
		if err == ErrNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		return r.client.RemoveObject(c, r.bucket, key, minio.RemoveObjectOptions{})
	})
}

// Has reports whether the blob exists.
func (r *R2) Has(ctx context.Context, sha256, ext string) (bool, error) {
	var ok bool
	err := r.br.guard(ctx, breakerOpTimeout, func(c context.Context) error {
		_, err := r.keyFor(c, sha256, ext)
		if err == ErrNotFound {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		ok = true
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return ok, err
}

// List returns every object in the bucket (name + size).
func (r *R2) List(ctx context.Context) ([]BlobInfo, error) {
	var out []BlobInfo
	for obj := range r.client.ListObjects(ctx, r.bucket, minio.ListObjectsOptions{Recursive: true}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		hash, ext := splitKey(obj.Key)
		out = append(out, BlobInfo{Key: obj.Key, Hash: hash, Ext: ext, Size: obj.Size, Modified: obj.LastModified})
	}
	return out, nil
}

// Stat returns blob metadata.
func (r *R2) Stat(ctx context.Context, sha256, ext string) (StatInfo, error) {
	var si StatInfo
	err := r.br.guard(ctx, breakerOpTimeout, func(c context.Context) error {
		key, err := r.keyFor(c, sha256, ext)
		if err != nil {
			return err
		}
		info, err := r.client.StatObject(c, r.bucket, key, minio.StatObjectOptions{})
		if err != nil {
			if isNotFound(err) {
				return ErrNotFound
			}
			return err
		}
		si = StatInfo{Size: info.Size, ContentType: info.ContentType, Key: key}
		return nil
	})
	return si, err
}
