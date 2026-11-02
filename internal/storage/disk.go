package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

// Disk is a content-addressed local-filesystem backend — the "own hardware, no
// S3" option. Blobs live at "<root>/<ab>/<sha256>[.ext]" (sharded by the first
// two hex chars so no single directory holds every object). Writes are atomic
// (temp file + rename). This lets a node run with zero cloud dependency.
type Disk struct {
	root string
}

// NewDisk constructs a disk-backed Storage rooted at root (created if absent).
func NewDisk(root string) (*Disk, error) {
	if root == "" {
		return nil, errors.New("disk: empty root path")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("disk: create root: %w", err)
	}
	return &Disk{root: root}, nil
}

func shard(sha256 string) string {
	if len(sha256) >= 2 {
		return sha256[:2]
	}
	return "_"
}

func (d *Disk) path(sha256, ext string) string {
	return filepath.Join(d.root, shard(sha256), blobKey(sha256, ext))
}

// resolvePath finds the on-disk file for a bare hash by scanning its shard dir.
func (d *Disk) resolvePath(sha256 string) (string, error) {
	dir := filepath.Join(d.root, shard(sha256))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	for _, e := range entries {
		name := e.Name()
		if name == sha256 || strings.HasPrefix(name, sha256+".") {
			return filepath.Join(dir, name), nil
		}
	}
	return "", ErrNotFound
}

// pathFor resolves the stored file for (sha256, ext), falling back to a shard
// scan when ext is empty or does not match the stored object.
func (d *Disk) pathFor(sha256, ext string) (string, error) {
	if ext == "" {
		return d.resolvePath(sha256)
	}
	p := d.path(sha256, ext)
	if _, err := os.Stat(p); err == nil {
		return p, nil
	} else if os.IsNotExist(err) {
		return d.resolvePath(sha256)
	} else {
		return "", err
	}
}

// Load streams the blob from disk.
func (d *Disk) Load(_ context.Context, sha256, ext string) (io.ReadSeekCloser, error) {
	p, err := d.pathFor(sha256, ext)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil // *os.File implements io.ReadSeekCloser
}

// Store writes the blob atomically under "<ab>/<sha256>.<ext>".
func (d *Disk) Store(_ context.Context, sha256, ext, _ string, r io.Reader, _ int64) error {
	dir := filepath.Join(d.root, shard(sha256))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("disk: create shard: %w", err)
	}
	tmp, err := os.CreateTemp(dir, blobKey(sha256, ext)+".*.tmp")
	if err != nil {
		return fmt.Errorf("disk: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return fmt.Errorf("disk: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("disk: close: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, blobKey(sha256, ext))); err != nil {
		return fmt.Errorf("disk: commit: %w", err)
	}
	return nil
}

// Delete removes the blob (absent is not an error).
func (d *Disk) Delete(_ context.Context, sha256, ext string) error {
	p, err := d.pathFor(sha256, ext)
	if err == ErrNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Has reports whether the blob exists.
func (d *Disk) Has(_ context.Context, sha256, ext string) (bool, error) {
	_, err := d.pathFor(sha256, ext)
	if err == ErrNotFound {
		return false, nil
	}
	return err == nil, err
}

// List returns every stored blob (name + size) by scanning the shard dirs.
func (d *Disk) List(_ context.Context) ([]BlobInfo, error) {
	shards, err := os.ReadDir(d.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []BlobInfo
	for _, sh := range shards {
		if !sh.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(d.root, sh.Name()))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || strings.Contains(e.Name(), ".tmp") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			hash, ext := splitKey(e.Name())
			out = append(out, BlobInfo{Key: e.Name(), Hash: hash, Ext: ext, Size: info.Size(), Modified: info.ModTime()})
		}
	}
	return out, nil
}

// Stat returns blob metadata (content type inferred from the extension).
func (d *Disk) Stat(_ context.Context, sha256, ext string) (StatInfo, error) {
	p, err := d.pathFor(sha256, ext)
	if err != nil {
		return StatInfo{}, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return StatInfo{}, ErrNotFound
		}
		return StatInfo{}, err
	}
	key := filepath.Base(p)
	return StatInfo{Size: fi.Size(), ContentType: contentTypeForExt(filepath.Ext(key)), Key: key}, nil
}

// contentTypeForExt maps an extension to a stable content type. It pins the types
// the node actually serves (esp. .zip) so results don't vary with the host OS's
// mime registry (Windows reports .zip as application/x-zip-compressed), then falls
// back to the mime table, then octet-stream.
func contentTypeForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".zip":
		return "application/zip"
	case ".7z":
		return "application/x-7z-compressed"
	case ".rar":
		return "application/vnd.rar"
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
