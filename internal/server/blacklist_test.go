package server

import (
	"path/filepath"
	"testing"
)

func TestBlacklistRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blacklist.json")
	b := loadBlacklist(path)

	const h = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if b.has(h) {
		t.Fatal("empty blacklist reported a hit")
	}
	if err := b.add(h, "malware"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if !b.has(h) {
		t.Fatal("added hash not found")
	}

	// Persisted and reloadable, with the reason intact.
	reloaded := loadBlacklist(path)
	if !reloaded.has(h) {
		t.Fatal("blacklist not persisted")
	}
	entries := reloaded.list()
	if len(entries) != 1 || entries[0].Hash != h || entries[0].Reason != "malware" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	if entries[0].Added == 0 {
		t.Fatal("added timestamp not recorded")
	}

	// Removal persists.
	if err := b.remove(h); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if loadBlacklist(path).has(h) {
		t.Fatal("hash still present after remove + reload")
	}
}
