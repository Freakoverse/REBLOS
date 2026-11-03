package server

import (
	"encoding/json"
	"os"
	"sync"
)

// blobHubs maps a blob hash -> the set of hub coordinates that own it. Only the
// structural blobs a hub publishes (member-tree index, leaf pages, spine, history,
// struct) are tagged, when they are mirrored (see mirrorHubBlobs); message media
// mirrored lazily on read is not hub-attributable (its hashes live inside encrypted
// content), so it stays untagged.
//
// This is what powers the dashboard's per-hub "Hub" column and the scoped delete on
// hub removal: a blob is removed only when the hub being removed was its sole owner,
// so content shared with another whitelisted hub survives. Persisted as JSON.
type blobHubs struct {
	mu     sync.RWMutex
	saveMu sync.Mutex
	path   string
	m      map[string][]string // hash (hex) -> hub coordinates
}

func loadBlobHubs(path string) *blobHubs {
	b := &blobHubs{path: path, m: map[string][]string{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &b.m)
	}
	return b
}

// add records that a hub coordinate owns a blob (deduped). Persists on change.
func (b *blobHubs) add(hash, coord string) {
	if hash == "" || coord == "" {
		return
	}
	b.mu.Lock()
	for _, c := range b.m[hash] {
		if c == coord {
			b.mu.Unlock()
			return
		}
	}
	b.m[hash] = append(b.m[hash], coord)
	b.mu.Unlock()
	b.save()
}

// list returns a copy of the hub coordinates owning a hash (nil if none).
func (b *blobHubs) list(hash string) []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	src := b.m[hash]
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// remove drops a hash's entry entirely (after the blob itself is deleted).
func (b *blobHubs) remove(hash string) {
	b.mu.Lock()
	if _, ok := b.m[hash]; !ok {
		b.mu.Unlock()
		return
	}
	delete(b.m, hash)
	b.mu.Unlock()
	b.save()
}

// dropHubReturningOrphans removes a hub coordinate from every blob's owner set and
// returns the hashes that, as a result, are owned by NO hub anymore (safe to delete
// when a hub is removed with its blobs). Hashes still owned by another hub keep their
// entry and are not returned. Persists once.
func (b *blobHubs) dropHubReturningOrphans(coord string) []string {
	b.mu.Lock()
	var orphans []string
	for hash, coords := range b.m {
		idx := -1
		for i, c := range coords {
			if c == coord {
				idx = i
				break
			}
		}
		if idx < 0 {
			continue
		}
		coords = append(coords[:idx], coords[idx+1:]...)
		if len(coords) == 0 {
			delete(b.m, hash)
			orphans = append(orphans, hash)
		} else {
			b.m[hash] = coords
		}
	}
	b.mu.Unlock()
	b.save()
	return orphans
}

func (b *blobHubs) save() {
	b.mu.RLock()
	data, _ := json.Marshal(b.m)
	b.mu.RUnlock()
	b.saveMu.Lock()
	_ = os.WriteFile(b.path, data, 0o600)
	b.saveMu.Unlock()
}
