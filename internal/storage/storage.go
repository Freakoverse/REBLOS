// Package storage is the blob backend abstraction. Blobs are content-addressed by
// their lowercase hex SHA-256. Objects are keyed "<sha256>.<ext>". The interface is
// backend-agnostic so the same node runs against Cloudflare R2, a self-hosted S3
// (Garage / Ceph / …), or local disk without touching the rest of the server.
package storage

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"
)

// ErrNotFound is returned when a blob does not exist.
var ErrNotFound = errors.New("blob not found")

// splitKey splits "<hash>.<ext>" into its hash and extension (ext "" if none).
func splitKey(key string) (hash, ext string) {
	if dot := strings.IndexByte(key, '.'); dot >= 0 {
		return key[:dot], key[dot+1:]
	}
	return key, ""
}

// StatInfo is lightweight blob metadata.
type StatInfo struct {
	Size        int64
	ContentType string
	Key         string // the resolved object key, e.g. "<sha256>.zip"
}

// BlobInfo is a listed object (name + size). Content-addressed stores hold only
// names + sizes here, so the full listing is small enough to paginate in memory.
type BlobInfo struct {
	Key      string // "<sha256>.<ext>"
	Hash     string // sha256 (the key without extension)
	Ext      string // extension without the dot, e.g. "zip"
	Size     int64
	Modified time.Time // last-modified / added time (zero if unknown)
}

// Storage is a content-addressed blob store.
type Storage interface {
	// Load returns a streaming reader for the blob. ext may be empty (a bare-hash
	// request), in which case the implementation resolves the actual object.
	// Returns ErrNotFound if absent. The caller MUST Close the reader.
	Load(ctx context.Context, sha256, ext string) (io.ReadSeekCloser, error)

	// Store writes the blob under "<sha256>.<ext>" with the given content type.
	// r is streamed (never fully buffered); size is the exact length in bytes.
	Store(ctx context.Context, sha256, ext, contentType string, r io.Reader, size int64) error

	// Delete removes the blob. Absent blobs are not an error.
	Delete(ctx context.Context, sha256, ext string) error

	// Has reports whether the blob exists.
	Has(ctx context.Context, sha256, ext string) (bool, error)

	// Stat returns blob metadata (size, content type). Returns ErrNotFound if absent.
	Stat(ctx context.Context, sha256, ext string) (StatInfo, error)

	// List returns every stored blob (name + size), for admin browsing. The caller
	// filters/paginates in memory.
	List(ctx context.Context) ([]BlobInfo, error)
}
