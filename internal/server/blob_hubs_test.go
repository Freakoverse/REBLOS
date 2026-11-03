package server

import (
	"path/filepath"
	"sort"
	"testing"
)

// dropHubReturningOrphans must return only the blobs a removed hub SOLELY owned, and
// leave blobs shared with another hub in place (owned by the remaining hub). This is
// the safety invariant behind "remove hub and delete its blobs".
func TestBlobHubsDropHubReturningOrphans(t *testing.T) {
	bh := loadBlobHubs(filepath.Join(t.TempDir(), "blob_hubs.json"))

	const hub1 = "36942:aa:one"
	const hub2 = "36942:bb:two"
	bh.add("solo", hub1)   // owned only by hub1 -> orphaned on hub1 removal
	bh.add("shared", hub1) // owned by both
	bh.add("shared", hub2) //   "
	bh.add("other", hub2)  // untouched by a hub1 removal

	orphans := bh.dropHubReturningOrphans(hub1)
	sort.Strings(orphans)
	if len(orphans) != 1 || orphans[0] != "solo" {
		t.Fatalf("orphans = %v, want [solo]", orphans)
	}
	if got := bh.list("solo"); got != nil {
		t.Fatalf("solo should be gone, got %v", got)
	}
	if got := bh.list("shared"); len(got) != 1 || got[0] != hub2 {
		t.Fatalf("shared should remain owned by hub2 only, got %v", got)
	}
	if got := bh.list("other"); len(got) != 1 || got[0] != hub2 {
		t.Fatalf("other should be untouched, got %v", got)
	}
}

// Reload reads back what was persisted.
func TestBlobHubsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blob_hubs.json")
	bh := loadBlobHubs(path)
	bh.add("h1", "36942:aa:one")
	bh.add("h1", "36942:aa:one") // dedup

	reload := loadBlobHubs(path)
	if got := reload.list("h1"); len(got) != 1 || got[0] != "36942:aa:one" {
		t.Fatalf("reloaded list = %v, want one entry", got)
	}
}
