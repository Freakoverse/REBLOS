package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBlobOwnersSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blob_owners.json")
	o := loadBlobOwners(path)

	if got := o.first("abc"); got != "" {
		t.Fatalf("empty store returned %q", got)
	}

	o.add("hash1", "pk1")
	o.add("hash1", "pk2") // second uploader of the same content — appended
	o.add("hash1", "pk1") // duplicate — ignored
	o.add("hash2", "pk9")
	o.add("", "pkX")      // ignored — no hash
	o.add("hash3", "")    // ignored — no pubkey

	if got := o.first("hash1"); got != "pk1" {
		t.Fatalf("first(hash1) = %q, want pk1 (original preserved)", got)
	}
	if got := o.list("hash1"); len(got) != 2 || got[0] != "pk1" || got[1] != "pk2" {
		t.Fatalf("list(hash1) = %v, want [pk1 pk2]", got)
	}
	if got := o.list("hash3"); got != nil {
		t.Fatalf("list(hash3) = %v, want nil", got)
	}

	// Persisted and reloadable, keeping order.
	reloaded := loadBlobOwners(path)
	if got := reloaded.list("hash1"); len(got) != 2 || got[0] != "pk1" {
		t.Fatalf("after reload list(hash1) = %v, want [pk1 pk2]", got)
	}

	// Removal drops the whole entry.
	o.remove("hash1")
	if got := loadBlobOwners(path).list("hash1"); got != nil {
		t.Fatalf("hash1 not removed after reload, got %v", got)
	}
	if got := loadBlobOwners(path).first("hash2"); got != "pk9" {
		t.Fatalf("removal clobbered hash2, got %q", got)
	}
}

func TestBlobOwnersLegacyMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blob_owners.json")
	// The earlier on-disk shape: hash -> single pubkey string.
	if err := os.WriteFile(path, []byte(`{"legacyhash":"pkOld","other":"pkTwo"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	o := loadBlobOwners(path)
	if got := o.list("legacyhash"); len(got) != 1 || got[0] != "pkOld" {
		t.Fatalf("legacy single-string not upgraded: %v", got)
	}

	// A new uploader appends to the migrated list, and the save is now list-form.
	o.add("legacyhash", "pkNew")
	if got := loadBlobOwners(path).list("legacyhash"); len(got) != 2 || got[1] != "pkNew" {
		t.Fatalf("after append+reload = %v, want [pkOld pkNew]", got)
	}
}
