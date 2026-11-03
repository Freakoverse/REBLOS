package server

import (
	"path/filepath"
	"testing"
)

func enabledSet(s *syncRelays) map[string]bool {
	m := map[string]bool{}
	for _, u := range s.enabled() {
		m[u] = true
	}
	return m
}

func TestSyncRelaysSeedAndToggle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync_relays.json")
	defaults := []string{"wss://a.example", "wss://b.example/"} // trailing slash normalized
	extras := []string{"wss://custom.example", "not-a-relay", "wss://a.example"}

	s := loadSyncRelays(path, defaults, extras)

	// Defaults + the one valid, non-duplicate extra are seeded enabled; the bad URL
	// and the duplicate of a default are dropped.
	en := enabledSet(s)
	for _, u := range []string{"wss://a.example", "wss://b.example", "wss://custom.example"} {
		if !en[u] {
			t.Errorf("expected %s enabled after seed; enabled=%v", u, s.enabled())
		}
	}
	if len(s.enabled()) != 3 {
		t.Fatalf("expected 3 enabled, got %v", s.enabled())
	}
	// Default-ness is recorded for defaults, not for the custom extra.
	for _, e := range s.list() {
		if e.URL == "wss://custom.example" && e.Default {
			t.Error("custom extra should not be marked default")
		}
		if e.URL == "wss://a.example" && !e.Default {
			t.Error("seeded default should be marked default")
		}
	}

	// Disable a default: kept in the list, dropped from enabled().
	if err := s.setEnabled("wss://a.example", false); err != nil {
		t.Fatal(err)
	}
	if enabledSet(s)["wss://a.example"] {
		t.Error("disabled default should not be enabled")
	}
	if len(s.list()) != 3 {
		t.Errorf("disabled default should stay in the list, got %d entries", len(s.list()))
	}

	// Removing a default only disables it (seeding would re-add a deleted default).
	removed, err := s.remove("wss://b.example")
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("removing a default should disable, not delete")
	}
	if enabledSet(s)["wss://b.example"] {
		t.Error("removed default should be disabled")
	}

	// Removing a custom entry deletes it outright.
	removed, _ = s.remove("wss://custom.example")
	if !removed {
		t.Error("removing a custom relay should delete it")
	}
	for _, e := range s.list() {
		if e.URL == "wss://custom.example" {
			t.Error("custom relay should be gone after remove")
		}
	}
}

func TestSyncRelaysAddRejectsBadScheme(t *testing.T) {
	s := loadSyncRelays(filepath.Join(t.TempDir(), "sr.json"), nil, nil)
	if _, err := s.add("http://not-websocket.example"); err == nil {
		t.Fatal("expected an error for a non-ws relay URL")
	}
	if _, err := s.add("wss://ok.example/"); err != nil {
		t.Fatalf("valid wss URL should add: %v", err)
	}
	if !enabledSet(s)["wss://ok.example"] {
		t.Error("added relay should be enabled and normalized (no trailing slash)")
	}
	// Re-adding re-enables rather than erroring or duplicating.
	_ = s.setEnabled("wss://ok.example", false)
	if _, err := s.add("wss://ok.example"); err != nil {
		t.Fatal(err)
	}
	if !enabledSet(s)["wss://ok.example"] {
		t.Error("re-adding should re-enable")
	}
	if n := len(s.list()); n != 1 {
		t.Errorf("re-adding should not duplicate, got %d entries", n)
	}
}

// Changes persist, and a reload re-adds a missing default while keeping a disabled
// default disabled (the operator's toggle wins over re-seeding).
func TestSyncRelaysPersistAndMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sr.json")
	s := loadSyncRelays(path, []string{"wss://d1.example", "wss://d2.example"}, nil)
	if err := s.setEnabled("wss://d1.example", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.add("wss://custom.example"); err != nil {
		t.Fatal(err)
	}

	// Reload with an EXTRA default added in a "later version".
	s2 := loadSyncRelays(path, []string{"wss://d1.example", "wss://d2.example", "wss://d3.example"}, nil)
	en := enabledSet(s2)
	if en["wss://d1.example"] {
		t.Error("a disabled default must stay disabled across reload + merge")
	}
	if !en["wss://custom.example"] {
		t.Error("a custom relay must persist across reload")
	}
	if !en["wss://d3.example"] {
		t.Error("a newly-added default must be merged in on reload")
	}
}

func TestMarkBackfillOncePerCoord(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	coord := "36942:abc:h"
	if !srv.markBackfill(coord) {
		t.Fatal("first markBackfill should return true")
	}
	if srv.markBackfill(coord) {
		t.Fatal("second markBackfill for the same coord should return false")
	}
	if !srv.markBackfill("36942:abc:other") {
		t.Fatal("a different coord should backfill independently")
	}
}
