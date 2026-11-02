package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultMaxBlobBytes caps a single member-tree blob. A full leaf page is roughly
// (page_size members x ~200 bytes) plus internal nodes; 32 MiB is comfortably
// above a 10k-member page while still refusing a hostile oversized blob.
const defaultMaxBlobBytes int64 = 32 << 20

// LocalStore is the subset of the blob storage the fetcher reads from. The
// production *storage.Storage satisfies it; ext "" resolves the object by hash.
type LocalStore interface {
	Load(ctx context.Context, sha256hex, ext string) (io.ReadSeekCloser, error)
}

// Fetcher resolves content-addressed member-tree blobs local-first, then from the
// hub's remote Blossom servers, verifying the sha256 of whatever it returns so a
// wrong or tampered blob is never accepted. On a successful remote hit it can
// mirror the bytes into the local store (durability + faster future reads).
type Fetcher struct {
	Local    LocalStore                                            // may be nil (remote only)
	Remotes  []string                                              // Blossom base URLs, tried in order
	HTTP     *http.Client                                          // defaults to a 15s client
	MaxBytes int64                                                 // per-blob ceiling; 0 => default
	Mirror   func(ctx context.Context, sha256hex string, b []byte) // optional post-remote store hook
}

// Bind adapts the fetcher to a BlobFetcher closed over ctx, for BuildPosterSet.
func (f *Fetcher) Bind(ctx context.Context) BlobFetcher {
	return func(h string) (string, error) { return f.Fetch(ctx, h) }
}

// Fetch returns the blob's bytes as a string, verified against sha256hex. It
// tries the local store first (ErrNotFound falls through), then each remote in
// order. The returned error names the last failure when nothing yields the blob.
func (f *Fetcher) Fetch(ctx context.Context, sha256hex string) (string, error) {
	want := strings.ToLower(strings.TrimSpace(sha256hex))
	if !isSHA256Hex(want) {
		return "", fmt.Errorf("invalid blob hash %q", sha256hex)
	}
	max := f.MaxBytes
	if max <= 0 {
		max = defaultMaxBlobBytes
	}

	// 1. Local store.
	if f.Local != nil {
		if b, ok := f.loadLocal(ctx, want, max); ok {
			if err := verifyHash(want, b); err != nil {
				return "", err
			}
			return string(b), nil
		}
	}

	// 2. Remote Blossom servers, in order.
	var lastErr error
	for _, base := range f.Remotes {
		b, err := f.loadRemote(ctx, base, want, max)
		if err != nil {
			lastErr = err
			continue
		}
		if err := verifyHash(want, b); err != nil {
			lastErr = err // a server served the wrong bytes; try the next one
			continue
		}
		if f.Mirror != nil {
			f.Mirror(ctx, want, b)
		}
		return string(b), nil
	}
	if lastErr != nil {
		return "", fmt.Errorf("blob %s not found locally and no remote served it: %w", want, lastErr)
	}
	return "", fmt.Errorf("blob %s not found (no local store hit, no remotes configured)", want)
}

// loadLocal returns (bytes, true) on a hit, (nil, false) when absent or unreadable
// so the caller falls through to remotes.
func (f *Fetcher) loadLocal(ctx context.Context, hash string, max int64) ([]byte, bool) {
	rc, err := f.Local.Load(ctx, hash, "")
	if err != nil {
		return nil, false
	}
	defer rc.Close()
	b, err := readCapped(rc, max)
	if err != nil {
		return nil, false
	}
	return b, true
}

func (f *Fetcher) loadRemote(ctx context.Context, base, hash string, max int64) ([]byte, error) {
	url := strings.TrimRight(base, "/") + "/" + hash
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := f.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: http %d", url, resp.StatusCode)
	}
	return readCapped(resp.Body, max)
}

// readCapped reads up to max bytes and errors if the source exceeds it.
func readCapped(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("blob exceeds %d-byte limit", max)
	}
	return b, nil
}

func verifyHash(want string, b []byte) error {
	sum := sha256.Sum256(b)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("blob hash mismatch: got %s, want %s", got, want)
	}
	return nil
}

// isSHA256Hex reports whether s is a 64-char lowercase-hex string.
func isSHA256Hex(s string) bool { return isXOnlyPubkey(s) } // same shape: 64 lowercase hex
