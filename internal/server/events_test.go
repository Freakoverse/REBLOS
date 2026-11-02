package server

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

func TestMatchesSearch(t *testing.T) {
	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	evt := &nostr.Event{
		Kind:      currentModKind,
		PubKey:    pk,
		Content:   "A great Skyrim overhaul",
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"d", "cool-mod-123"}, {"t", "skyrim"}, {"title", "Cool Mod"}},
	}
	naddr, _ := nip19.EncodeEntity(pk, currentModKind, "cool-mod-123", nil)
	npub, _ := nip19.EncodePublicKey(pk)

	for _, tc := range []struct {
		name   string
		term   string
		expect bool
	}{
		{"empty matches all", "", true},
		{"word in content", "overhaul", true},
		{"case-insensitive", "SKYRIM", true},
		{"word in a tag value", "cool mod", true},
		{"the d identifier", "cool-mod-123", true},
		{"the full naddr", naddr, true},
		{"the author pubkey", pk, true},
		{"the author npub", npub, true},
		{"a miss", "witcher", false},
	} {
		got := matchesSearch(evt, lower(tc.term))
		if got != tc.expect {
			t.Errorf("%s: matchesSearch(%q) = %v, want %v", tc.name, tc.term, got, tc.expect)
		}
	}
}

// lower mirrors the handler's lowercasing of the incoming term.
func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
