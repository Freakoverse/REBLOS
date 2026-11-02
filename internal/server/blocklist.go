package server

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/nbd-wtf/go-nostr/nip86"
)

// blocklist is a persistent set of banned pubkeys (hex → reason), shared by the
// relay's event policy and the upload handler, mutated via the NIP-86 admin API.
type blocklist struct {
	mu   sync.RWMutex
	path string
	set  map[string]string
}

func loadBlocklist(path string) *blocklist {
	b := &blocklist{path: path, set: map[string]string{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &b.set)
	}
	return b
}

func (b *blocklist) has(pk string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.set[pk]
	return ok
}

func (b *blocklist) ban(pk, reason string) error {
	b.mu.Lock()
	b.set[pk] = reason
	b.mu.Unlock()
	return b.save()
}

func (b *blocklist) allow(pk string) error {
	b.mu.Lock()
	delete(b.set, pk)
	b.mu.Unlock()
	return b.save()
}

func (b *blocklist) list() []nip86.PubKeyReason {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]nip86.PubKeyReason, 0, len(b.set))
	for pk, r := range b.set {
		out = append(out, nip86.PubKeyReason{PubKey: pk, Reason: r})
	}
	return out
}

func (b *blocklist) save() error {
	b.mu.RLock()
	data, _ := json.MarshalIndent(b.set, "", "  ")
	b.mu.RUnlock()
	return os.WriteFile(b.path, data, 0o600)
}
