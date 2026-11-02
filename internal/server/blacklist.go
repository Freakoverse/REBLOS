package server

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// blacklistedHashes is a persistent set of blob sha256 hashes barred from the
// server: uploads of them are rejected and downloads return 404. It's the blob
// counterpart to bannedEvents — a takedown that survives re-upload. Deleting the
// object alone doesn't stick, because the store is content-addressed and anyone
// can PUT the identical bytes back; the blacklist entry is what makes it permanent.
// Admin-managed only; there is no auto-population.
type blacklistedHashes struct {
	mu   sync.RWMutex
	path string
	set  map[string]blacklistMeta
}

type blacklistMeta struct {
	Reason string `json:"reason,omitempty"`
	Added  int64  `json:"added,omitempty"` // unix seconds
}

func loadBlacklist(path string) *blacklistedHashes {
	b := &blacklistedHashes{path: path, set: map[string]blacklistMeta{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &b.set)
	}
	return b
}

func (b *blacklistedHashes) has(hash string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.set[hash]
	return ok
}

// add blacklists a hash (idempotent; keeps the original added-time on re-add).
func (b *blacklistedHashes) add(hash, reason string) error {
	b.mu.Lock()
	m := b.set[hash]
	m.Reason = reason
	if m.Added == 0 {
		m.Added = time.Now().Unix()
	}
	b.set[hash] = m
	b.mu.Unlock()
	return b.save()
}

func (b *blacklistedHashes) remove(hash string) error {
	b.mu.Lock()
	delete(b.set, hash)
	b.mu.Unlock()
	return b.save()
}

type blacklistEntry struct {
	Hash   string `json:"hash"`
	Reason string `json:"reason,omitempty"`
	Added  int64  `json:"added,omitempty"`
}

func (b *blacklistedHashes) list() []blacklistEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]blacklistEntry, 0, len(b.set))
	for h, m := range b.set {
		out = append(out, blacklistEntry{Hash: h, Reason: m.Reason, Added: m.Added})
	}
	return out
}

func (b *blacklistedHashes) save() error {
	b.mu.RLock()
	data, _ := json.MarshalIndent(b.set, "", "  ")
	b.mu.RUnlock()
	return os.WriteFile(b.path, data, 0o600)
}
