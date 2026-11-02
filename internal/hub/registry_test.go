package hub

import (
	"path/filepath"
	"testing"

	"github.com/nbd-wtf/go-nostr/nip19"
)

func TestParseHubRefNaddr(t *testing.T) {
	naddr, err := nip19.EncodeEntity(pkA, Kind, "my-hub", []string{"wss://relay.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := ParseHubRef(naddr)
	if err != nil {
		t.Fatalf("ParseHubRef(naddr): %v", err)
	}
	if h.Pubkey != pkA || h.DTag != "my-hub" {
		t.Errorf("hub = %+v", h)
	}
	if h.Coordinate() != "36942:"+pkA+":my-hub" {
		t.Errorf("coordinate = %q", h.Coordinate())
	}
	if len(h.Relays) != 1 || h.Relays[0] != "wss://relay.example.com" {
		t.Errorf("relays = %v", h.Relays)
	}
	// nostr: prefix should be tolerated.
	if _, err := ParseHubRef("nostr:" + naddr); err != nil {
		t.Errorf("nostr: prefix rejected: %v", err)
	}
}

func TestParseHubRefCoordinate(t *testing.T) {
	h, err := ParseHubRef("36942:" + pkB + ":chan-hub")
	if err != nil {
		t.Fatalf("ParseHubRef(coordinate): %v", err)
	}
	if h.Pubkey != pkB || h.DTag != "chan-hub" {
		t.Errorf("hub = %+v", h)
	}
}

func TestParseHubRefRejects(t *testing.T) {
	// Wrong-kind naddr (long-form 30023, not a hub).
	wrongKind, _ := nip19.EncodeEntity(pkA, 30023, "post", nil)
	cases := map[string]string{
		"wrong kind naddr": wrongKind,
		"wrong kind coord": "30023:" + pkA + ":x",
		"bad pubkey":       "36942:nothex:x",
		"empty dtag":       "36942:" + pkA + ":",
		"garbage":          "not-a-ref",
		"empty":            "",
		"npub not naddr":   "npub1xxx",
		"two-part coord":   "36942:" + pkA,
	}
	for name, ref := range cases {
		if _, err := ParseHubRef(ref); err == nil {
			t.Errorf("%s: expected error, got nil for %q", name, ref)
		}
	}
}

func TestRegistryPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hubs.json")

	r := LoadRegistry(path)
	h1, err := ParseHubRef("36942:" + pkA + ":alpha")
	if err != nil {
		t.Fatal(err)
	}
	h1.Note = "first hub"
	if err := r.Add(h1); err != nil {
		t.Fatal(err)
	}
	// Two hubs sharing a dTag but different owners.
	h2, _ := ParseHubRef("36942:" + pkB + ":shared")
	h3, _ := ParseHubRef("36942:" + pkC + ":shared")
	_ = r.Add(h2)
	_ = r.Add(h3)

	if !r.Has(h1.Coordinate()) {
		t.Error("h1 not present")
	}
	if got := r.Get(h1.Coordinate()); got == nil || got.Note != "first hub" {
		t.Errorf("get h1 = %+v", got)
	}
	if dup := r.ByDTag("shared"); len(dup) != 2 {
		t.Errorf("ByDTag(shared) = %d, want 2", len(dup))
	}
	if len(r.List()) != 3 {
		t.Errorf("list = %d, want 3", len(r.List()))
	}

	// Reload from disk: state must survive.
	r2 := LoadRegistry(path)
	if len(r2.List()) != 3 {
		t.Fatalf("after reload, list = %d, want 3", len(r2.List()))
	}
	if got := r2.Get(h1.Coordinate()); got == nil || got.Note != "first hub" {
		t.Errorf("after reload, get h1 = %+v", got)
	}

	// Remove and confirm persistence of the removal.
	if ok, err := r2.Remove(h1.Coordinate()); err != nil || !ok {
		t.Fatalf("remove h1: ok=%v err=%v", ok, err)
	}
	if ok, _ := r2.Remove("36942:" + pkA + ":does-not-exist"); ok {
		t.Error("removing a missing hub reported true")
	}
	r3 := LoadRegistry(path)
	if r3.Has(h1.Coordinate()) {
		t.Error("h1 still present after removal + reload")
	}
	if len(r3.List()) != 2 {
		t.Errorf("after removal, list = %d, want 2", len(r3.List()))
	}
}
