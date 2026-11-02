package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

// Kind is the NIP-CHAT hub event kind. A hub is addressed by the coordinate
// "36942:<creatorPubkey>:<dTag>", which the admin whitelists (see REBLOS-MIGRATION
// §2). The creator pubkey in the coordinate is the owner's real key (v1) or owner
// pseudonym O (v2).
const Kind = 36942

// Hub is one whitelisted hub: the relay stores and serves events/blobs for its
// members and nobody else. Per-hub policy overrides (size caps, kind set, role
// rules) will hang off this struct as those features land.
type Hub struct {
	Pubkey string   `json:"pubkey"`           // hex creator pubkey (owner real key or pseudonym O)
	DTag   string   `json:"d"`                // hub d tag
	Note   string   `json:"note,omitempty"`   // admin label
	Relays []string `json:"relays,omitempty"` // relay hints from the naddr (for the sync layer)
	Added  int64    `json:"added,omitempty"`  // unix seconds the admin added it
}

// Coordinate returns the hub's addressable coordinate "36942:<pubkey>:<dTag>".
func (h Hub) Coordinate() string {
	return strconv.Itoa(Kind) + ":" + h.Pubkey + ":" + h.DTag
}

// ParseHubRef parses an naddr or a raw coordinate ("36942:<pubkey>:<dTag>") into
// a Hub. It rejects anything whose kind is not 36942, whose pubkey is not a
// 32-byte x-only hex key, or whose d tag is empty.
func ParseHubRef(ref string) (*Hub, error) {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "nostr:")
	if ref == "" {
		return nil, fmt.Errorf("empty hub reference")
	}

	// naddr (bech32).
	if strings.HasPrefix(ref, "naddr1") {
		prefix, value, err := nip19.Decode(ref)
		if err != nil {
			return nil, fmt.Errorf("decode naddr: %w", err)
		}
		if prefix != "naddr" {
			return nil, fmt.Errorf("not an naddr (%s)", prefix)
		}
		ep, ok := value.(nostr.EntityPointer)
		if !ok {
			return nil, fmt.Errorf("naddr did not decode to an address")
		}
		return hubFrom(ep.Kind, ep.PublicKey, ep.Identifier, ep.Relays)
	}

	// Raw coordinate "<kind>:<pubkey>:<dTag>".
	parts := strings.SplitN(ref, ":", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("not an naddr or a kind:pubkey:dTag coordinate")
	}
	kind, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("coordinate kind is not a number: %q", parts[0])
	}
	return hubFrom(kind, parts[1], parts[2], nil)
}

func hubFrom(kind int, pubkey, dTag string, relays []string) (*Hub, error) {
	if kind != Kind {
		return nil, fmt.Errorf("not a hub: kind %d (want %d)", kind, Kind)
	}
	pubkey = strings.ToLower(strings.TrimSpace(pubkey))
	if !isXOnlyPubkey(pubkey) {
		return nil, fmt.Errorf("invalid hub pubkey")
	}
	if strings.TrimSpace(dTag) == "" {
		return nil, fmt.Errorf("hub d tag is empty")
	}
	return &Hub{Pubkey: pubkey, DTag: dTag, Relays: relays}, nil
}

// Registry is the persistent set of whitelisted hubs, keyed by coordinate.
// Safe for concurrent use.
type Registry struct {
	mu     sync.RWMutex
	saveMu sync.Mutex
	path   string
	set    map[string]*Hub // coordinate -> hub
}

// LoadRegistry reads the registry from path. A missing or unreadable file yields
// an empty registry (never an error), mirroring the other persistent stores.
func LoadRegistry(path string) *Registry {
	r := &Registry{path: path, set: map[string]*Hub{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return r
	}
	var list []*Hub
	if json.Unmarshal(data, &list) == nil {
		for _, h := range list {
			if h != nil && h.Pubkey != "" && h.DTag != "" {
				r.set[h.Coordinate()] = h
			}
		}
	}
	return r
}

// Has reports whether a hub with this coordinate is whitelisted.
func (r *Registry) Has(coordinate string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.set[coordinate]
	return ok
}

// Get returns the hub for a coordinate, or nil.
func (r *Registry) Get(coordinate string) *Hub {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.set[coordinate]
}

// ByDTag returns every whitelisted hub carrying this d tag. A message's `#h` tag
// is the d tag alone (not the full coordinate), so two different owners could
// collide; callers disambiguate by checking the author against each candidate's
// poster set.
func (r *Registry) ByDTag(dTag string) []*Hub {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Hub
	for _, h := range r.set {
		if h.DTag == dTag {
			out = append(out, h)
		}
	}
	return out
}

// List returns all whitelisted hubs, ordered by coordinate.
func (r *Registry) List() []*Hub {
	r.mu.RLock()
	out := make([]*Hub, 0, len(r.set))
	for _, h := range r.set {
		out = append(out, h)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Coordinate() < out[j].Coordinate() })
	return out
}

// Add whitelists a hub (replacing any existing entry with the same coordinate)
// and persists the registry.
func (r *Registry) Add(h *Hub) error {
	r.mu.Lock()
	r.set[h.Coordinate()] = h
	r.mu.Unlock()
	return r.save()
}

// Remove drops a hub by coordinate and persists. Returns whether it existed.
func (r *Registry) Remove(coordinate string) (bool, error) {
	r.mu.Lock()
	_, existed := r.set[coordinate]
	delete(r.set, coordinate)
	r.mu.Unlock()
	if !existed {
		return false, nil
	}
	return true, r.save()
}

func (r *Registry) save() error {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	data, err := json.MarshalIndent(r.List(), "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}
