package server

import (
	"encoding/json"
	"os"
	"sync"
)

// whitelist is a persistent set of pubkeys granted a raised upload size cap
// (5× the normal limit). Managed via the admin API.
type whitelist struct {
	mu   sync.RWMutex
	path string
	set  map[string]string // pubkey (hex) -> optional note
}

type whitelistEntry struct {
	PubKey string `json:"pubkey"`
	Note   string `json:"note,omitempty"`
}

func loadWhitelist(path string) *whitelist {
	w := &whitelist{path: path, set: map[string]string{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &w.set)
	}
	return w
}

func (w *whitelist) has(pk string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, ok := w.set[pk]
	return ok
}

func (w *whitelist) add(pk, note string) error {
	w.mu.Lock()
	w.set[pk] = note
	w.mu.Unlock()
	return w.save()
}

func (w *whitelist) remove(pk string) error {
	w.mu.Lock()
	delete(w.set, pk)
	w.mu.Unlock()
	return w.save()
}

func (w *whitelist) list() []whitelistEntry {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]whitelistEntry, 0, len(w.set))
	for pk, note := range w.set {
		out = append(out, whitelistEntry{PubKey: pk, Note: note})
	}
	return out
}

func (w *whitelist) save() error {
	w.mu.RLock()
	data, _ := json.MarshalIndent(w.set, "", "  ")
	w.mu.RUnlock()
	return os.WriteFile(w.path, data, 0o600)
}
