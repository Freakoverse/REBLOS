package server

import (
	"encoding/json"
	"os"
	"sync"
)

// maxUploadersPerBlob bounds how many uploaders we track per blob. Content is
// deduplicated, so the same bytes arriving from several people is normal (reposts,
// mirrors); we keep them as an ordered set (first = the original uploader). The
// cap is a backstop against pathological growth — uploading costs bytes + PoW, so
// reaching it in practice means abuse.
const maxUploadersPerBlob = 50

// blobOwners maps a blob hash → the set of pubkeys that uploaded it, in order
// (first = the original uploader). The storage layer is content-addressed and
// keeps no per-object uploader, so this is recorded alongside it here. Tracking
// every uploader (not just the first) is what lets a future "my uploads" view show
// a blob to everyone who added it, and underpins reference-counted removal — a
// person removing their upload drops only their pubkey; the blob stays while
// anyone else still claims it.
//
// Persisted as JSON. Legacy blobs (uploaded before tracking) have no entry —
// callers treat that as "unknown", never an error. The loader tolerates the
// earlier single-string shape ({"hash":"pk"}) and upgrades it to a list on the
// next save.
type blobOwners struct {
	mu     sync.RWMutex
	saveMu sync.Mutex
	path   string
	m      map[string][]string // hash (hex) -> uploader pubkeys (hex), original first
}

func loadBlobOwners(path string) *blobOwners {
	b := &blobOwners{path: path, m: map[string][]string{}}
	if data, err := os.ReadFile(path); err == nil {
		b.m = parseOwners(data)
	}
	return b
}

// parseOwners reads the owners map, accepting both the current list form
// ({"hash":["pk",...]}) and the earlier single-string form ({"hash":"pk"}), so an
// existing blob_owners.json upgrades seamlessly.
func parseOwners(data []byte) map[string][]string {
	out := map[string][]string{}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return out
	}
	for h, v := range raw {
		var list []string
		if err := json.Unmarshal(v, &list); err == nil {
			if len(list) > 0 {
				out[h] = list
			}
			continue
		}
		var one string
		if err := json.Unmarshal(v, &one); err == nil && one != "" {
			out[h] = []string{one}
		}
	}
	return out
}

// first returns the original uploader (or "" if none/legacy).
func (b *blobOwners) first(hash string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if list := b.m[hash]; len(list) > 0 {
		return list[0]
	}
	return ""
}

// hasOwnerIn reports whether any of a hash's uploaders satisfies member — e.g.
// "is any uploader whitelisted?". Allocation-free, for the download hot path.
func (b *blobOwners) hasOwnerIn(hash string, member func(pubkey string) bool) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, pk := range b.m[hash] {
		if member(pk) {
			return true
		}
	}
	return false
}

// list returns a copy of the uploaders for a hash (nil if none), original first.
func (b *blobOwners) list(hash string) []string {
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

// add records an uploader for a hash. The original (first) uploader is preserved;
// a repeat uploader is appended once (deduped), up to the cap. Persists on change.
func (b *blobOwners) add(hash, pubkey string) {
	if hash == "" || pubkey == "" {
		return
	}
	b.mu.Lock()
	list := b.m[hash]
	for _, pk := range list {
		if pk == pubkey {
			b.mu.Unlock()
			return // already recorded
		}
	}
	if len(list) >= maxUploadersPerBlob {
		b.mu.Unlock()
		return
	}
	b.m[hash] = append(list, pubkey)
	b.mu.Unlock()
	b.save()
}

// removeOne drops a single uploader from a hash's set (an uploader retracting
// their own upload). Returns whether the pubkey was present, and whether the set
// is now empty. Persists on change.
func (b *blobOwners) removeOne(hash, pubkey string) (removed, nowEmpty bool) {
	b.mu.Lock()
	list := b.m[hash]
	idx := -1
	for i, pk := range list {
		if pk == pubkey {
			idx = i
			break
		}
	}
	if idx < 0 {
		b.mu.Unlock()
		return false, len(list) == 0
	}
	list = append(list[:idx], list[idx+1:]...)
	if len(list) == 0 {
		delete(b.m, hash)
	} else {
		b.m[hash] = list
	}
	nowEmpty = len(list) == 0
	b.mu.Unlock()
	b.save()
	return true, nowEmpty
}

// remove drops a hash's entire entry (e.g. after the blob is deleted), so the map
// doesn't accumulate entries for blobs that no longer exist.
func (b *blobOwners) remove(hash string) {
	b.mu.Lock()
	if _, ok := b.m[hash]; !ok {
		b.mu.Unlock()
		return
	}
	delete(b.m, hash)
	b.mu.Unlock()
	b.save()
}

// save snapshots under the read lock, then writes under a dedicated mutex so
// concurrent uploads can't interleave a half-written file.
func (b *blobOwners) save() {
	b.mu.RLock()
	data, _ := json.Marshal(b.m)
	b.mu.RUnlock()
	b.saveMu.Lock()
	_ = os.WriteFile(b.path, data, 0o600)
	b.saveMu.Unlock()
}
