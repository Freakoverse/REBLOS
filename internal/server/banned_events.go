package server

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/nbd-wtf/go-nostr"
)

// bannedEvents is a persistent set of moderated event "keys" — the addressable
// coordinate "<kind>:<pubkey>:<d>" for replaceable events (mods), or the raw event
// id otherwise. Any stored or incoming event whose key is in the set is deleted /
// rejected, so a takedown survives re-publishing (of the same address).
type bannedEvents struct {
	mu   sync.RWMutex
	path string
	set  map[string]string // key -> reason
}

// moderationKey returns the ban key for an event: its addressable coordinate when
// it has a `d` tag (mods are addressable), else its id.
func moderationKey(evt *nostr.Event) string {
	if d := evt.Tags.GetD(); d != "" {
		return fmt.Sprintf("%d:%s:%s", evt.Kind, evt.PubKey, d)
	}
	return evt.ID
}

func loadBannedEvents(path string) *bannedEvents {
	b := &bannedEvents{path: path, set: map[string]string{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &b.set)
	}
	return b
}

func (b *bannedEvents) has(key string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.set[key]
	return ok
}

func (b *bannedEvents) ban(key, reason string) error {
	b.mu.Lock()
	b.set[key] = reason
	b.mu.Unlock()
	return b.save()
}

func (b *bannedEvents) unban(key string) error {
	b.mu.Lock()
	delete(b.set, key)
	b.mu.Unlock()
	return b.save()
}

type bannedEventEntry struct {
	Key    string `json:"key"`
	Reason string `json:"reason,omitempty"`
}

func (b *bannedEvents) list() []bannedEventEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]bannedEventEntry, 0, len(b.set))
	for k, r := range b.set {
		out = append(out, bannedEventEntry{Key: k, Reason: r})
	}
	return out
}

func (b *bannedEvents) save() error {
	b.mu.RLock()
	data, _ := json.MarshalIndent(b.set, "", "  ")
	b.mu.RUnlock()
	return os.WriteFile(b.path, data, 0o600)
}
