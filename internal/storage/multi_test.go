package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
)

// memStore is an in-memory Storage for testing Multi/Migrate: deterministic, with
// opt-in failure injection and a record of the content types it was asked to store.
type memStore struct {
	mu       sync.Mutex
	blobs    map[string]memBlob // keyed "<hash>.<ext>" (ext may be empty)
	failLoad bool               // Load/Stat return a non-NotFound error
	failList bool               // List returns an error
	failHas  bool               // Has returns an error
}

type memBlob struct {
	data []byte
	ct   string
}

func newMem() *memStore { return &memStore{blobs: map[string]memBlob{}} }

func memKey(hash, ext string) string {
	if ext == "" {
		return hash
	}
	return hash + "." + ext
}

// resolve finds a blob by (hash, ext); an empty ext matches any stored extension.
func (m *memStore) resolve(hash, ext string) (string, memBlob, bool) {
	if ext != "" {
		b, ok := m.blobs[memKey(hash, ext)]
		return memKey(hash, ext), b, ok
	}
	for k, b := range m.blobs {
		if k == hash || (len(k) > len(hash) && k[:len(hash)] == hash && k[len(hash)] == '.') {
			return k, b, true
		}
	}
	return "", memBlob{}, false
}

func (m *memStore) Store(_ context.Context, hash, ext, ct string, r io.Reader, _ int64) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.blobs[memKey(hash, ext)] = memBlob{data: data, ct: ct}
	m.mu.Unlock()
	return nil
}

func (m *memStore) Load(_ context.Context, hash, ext string) (io.ReadSeekCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failLoad {
		return nil, errors.New("injected load failure")
	}
	_, b, ok := m.resolve(hash, ext)
	if !ok {
		return nil, ErrNotFound
	}
	return nopRSC{bytes.NewReader(b.data)}, nil
}

func (m *memStore) Delete(_ context.Context, hash, ext string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k, _, ok := m.resolve(hash, ext); ok {
		delete(m.blobs, k)
	}
	return nil
}

func (m *memStore) Has(_ context.Context, hash, ext string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failHas {
		return false, errors.New("injected has failure")
	}
	_, _, ok := m.resolve(hash, ext)
	return ok, nil
}

func (m *memStore) Stat(_ context.Context, hash, ext string) (StatInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failLoad {
		return StatInfo{}, errors.New("injected stat failure")
	}
	k, b, ok := m.resolve(hash, ext)
	if !ok {
		return StatInfo{}, ErrNotFound
	}
	return StatInfo{Size: int64(len(b.data)), ContentType: b.ct, Key: k}, nil
}

func (m *memStore) List(_ context.Context) ([]BlobInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failList {
		return nil, errors.New("injected list failure")
	}
	var out []BlobInfo
	for k, b := range m.blobs {
		hash, ext := splitKey(k)
		out = append(out, BlobInfo{Key: k, Hash: hash, Ext: ext, Size: int64(len(b.data))})
	}
	return out, nil
}

type nopRSC struct{ *bytes.Reader }

func (nopRSC) Close() error { return nil }

const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func mustStore(t *testing.T, s Storage, hash, ext, ct string, data string) {
	t.Helper()
	if err := s.Store(context.Background(), hash, ext, ct, bytes.NewReader([]byte(data)), int64(len(data))); err != nil {
		t.Fatalf("store: %v", err)
	}
}

func loadStr(t *testing.T, s Storage, hash, ext string) (string, error) {
	rc, err := s.Load(context.Background(), hash, ext)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return string(b), nil
}

func TestMulti_WritePrimaryReadFallback(t *testing.T) {
	ctx := context.Background()
	primary, fallback := newMem(), newMem()
	m := NewMulti(primary, fallback)

	// A blob only in the fallback is readable through Multi.
	mustStore(t, fallback, hashA, "png", "image/png", "old-blob")
	if got, err := loadStr(t, m, hashA, "png"); err != nil || got != "old-blob" {
		t.Fatalf("read from fallback: got %q err %v", got, err)
	}
	if ok, _ := m.Has(ctx, hashA, "png"); !ok {
		t.Error("Has should find a fallback-only blob")
	}
	if si, err := m.Stat(ctx, hashA, "png"); err != nil || si.ContentType != "image/png" {
		t.Errorf("Stat fallback: %+v err %v", si, err)
	}

	// Writes go only to the primary, never the fallback.
	mustStore(t, m, hashB, "txt", "text/plain", "new-blob")
	if _, _, ok := primary.resolve(hashB, "txt"); !ok {
		t.Error("new write should land on the primary")
	}
	if _, _, ok := fallback.resolve(hashB, "txt"); ok {
		t.Error("new write must NOT land on the fallback")
	}
}

func TestMulti_LoadFallsBackOnPrimaryError(t *testing.T) {
	primary, fallback := newMem(), newMem()
	primary.failLoad = true // simulate a primary outage
	mustStore(t, fallback, hashA, "png", "image/png", "served-despite-outage")

	m := NewMulti(primary, fallback)
	if got, err := loadStr(t, m, hashA, "png"); err != nil || got != "served-despite-outage" {
		t.Fatalf("expected fallback to serve during primary error: got %q err %v", got, err)
	}
}

func TestMulti_LoadMissingIsNotFound(t *testing.T) {
	m := NewMulti(newMem(), newMem())
	if _, err := m.Load(context.Background(), hashA, "png"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestMulti_DeleteHitsAllBackends(t *testing.T) {
	ctx := context.Background()
	primary, fallback := newMem(), newMem()
	mustStore(t, primary, hashA, "png", "image/png", "x")
	mustStore(t, fallback, hashA, "png", "image/png", "x")

	m := NewMulti(primary, fallback)
	if err := m.Delete(ctx, hashA, "png"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.Has(ctx, hashA, "png"); ok {
		t.Error("blob should be gone from every backend after Delete")
	}
	if _, _, ok := fallback.resolve(hashA, "png"); ok {
		t.Error("Delete must remove from the fallback too (no resurrection)")
	}
}

func TestMulti_ListUnionsAndDedups(t *testing.T) {
	primary, fallback := newMem(), newMem()
	mustStore(t, primary, hashA, "png", "image/png", "a")
	mustStore(t, fallback, hashA, "png", "image/png", "a") // duplicate key
	mustStore(t, fallback, hashB, "txt", "text/plain", "b")

	m := NewMulti(primary, fallback)
	items, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]int{}
	for _, it := range items {
		keys[it.Key]++
	}
	if len(keys) != 2 || keys[hashA+".png"] != 1 || keys[hashB+".txt"] != 1 {
		t.Fatalf("expected 2 deduped entries, got %v", keys)
	}
}

func TestMulti_NoFallbacksIsPassThrough(t *testing.T) {
	primary := newMem()
	m := NewMulti(primary)
	mustStore(t, m, hashA, "txt", "text/plain", "hi")
	if got, err := loadStr(t, m, hashA, "txt"); err != nil || got != "hi" {
		t.Fatalf("passthrough: got %q err %v", got, err)
	}
}
