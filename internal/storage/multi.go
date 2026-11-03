package storage

import (
	"context"
	"errors"
	"io"
)

// Multi is a Storage that writes to a single primary backend and reads from the
// primary first, then each fallback in order. It is what makes re-routing storage
// safe: point the primary at the new backend and keep the old one as a fallback, so
// a blob that has not been migrated yet (or that a transient primary outage hides)
// is still served from the old backend. Deletes apply to every backend, and List
// unions them (deduped by key).
//
// New writes land only on the primary, so once a migration has copied the old
// backend's blobs into the new primary the fallback can be dropped with no loss.
type Multi struct {
	primary   Storage
	fallbacks []Storage
}

// NewMulti wraps a primary with zero or more read-fallbacks. With no fallbacks it
// behaves exactly like the primary, so it is safe to always wrap.
func NewMulti(primary Storage, fallbacks ...Storage) *Multi {
	return &Multi{primary: primary, fallbacks: fallbacks}
}

// Backends returns the primary followed by the fallbacks, in read order.
func (m *Multi) Backends() []Storage {
	out := make([]Storage, 0, 1+len(m.fallbacks))
	out = append(out, m.primary)
	out = append(out, m.fallbacks...)
	return out
}

// Primary is the write target (and first read).
func (m *Multi) Primary() Storage { return m.primary }

// Load tries each backend in read order, returning the first hit. A backend's
// ErrNotFound falls through to the next; a real error is remembered but does not
// stop the search (a fallback can still serve the blob during a primary outage). If
// nothing serves it, returns the last real error, or ErrNotFound if all missed.
func (m *Multi) Load(ctx context.Context, sha256, ext string) (io.ReadSeekCloser, error) {
	var lastErr error = ErrNotFound
	for _, s := range m.Backends() {
		rc, err := s.Load(ctx, sha256, ext)
		if err == nil {
			return rc, nil
		}
		if !errors.Is(err, ErrNotFound) {
			lastErr = err
		}
	}
	return nil, lastErr
}

// Store writes to the primary only. New blobs always land on the current backend.
func (m *Multi) Store(ctx context.Context, sha256, ext, contentType string, r io.Reader, size int64) error {
	return m.primary.Store(ctx, sha256, ext, contentType, r, size)
}

// Delete removes the blob from every backend, so a deletion cannot be resurrected
// from a fallback. Returns the first error, after attempting all.
func (m *Multi) Delete(ctx context.Context, sha256, ext string) error {
	var firstErr error
	for _, s := range m.Backends() {
		if err := s.Delete(ctx, sha256, ext); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Has reports whether any backend holds the blob (best-effort: backend errors are
// treated as "not here" and the search continues).
func (m *Multi) Has(ctx context.Context, sha256, ext string) (bool, error) {
	for _, s := range m.Backends() {
		if ok, err := s.Has(ctx, sha256, ext); err == nil && ok {
			return true, nil
		}
	}
	return false, nil
}

// Stat returns metadata from the first backend that has the blob (same order as Load).
func (m *Multi) Stat(ctx context.Context, sha256, ext string) (StatInfo, error) {
	var lastErr error = ErrNotFound
	for _, s := range m.Backends() {
		si, err := s.Stat(ctx, sha256, ext)
		if err == nil {
			return si, nil
		}
		if !errors.Is(err, ErrNotFound) {
			lastErr = err
		}
	}
	return StatInfo{}, lastErr
}

// List returns the union of every backend's blobs, deduped by key (the primary's
// entry wins on a duplicate, since it is listed first). If every backend errors,
// returns the first error.
func (m *Multi) List(ctx context.Context) ([]BlobInfo, error) {
	seen := map[string]bool{}
	var out []BlobInfo
	var firstErr error
	any := false
	for _, s := range m.Backends() {
		items, err := s.List(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		any = true
		for _, it := range items {
			if !seen[it.Key] {
				seen[it.Key] = true
				out = append(out, it)
			}
		}
	}
	if !any && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}
