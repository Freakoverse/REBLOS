package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUploadCapsDefaultsAndOverride(t *testing.T) {
	const mb = int64(1024 * 1024)
	path := filepath.Join(t.TempDir(), "upload_caps.json")

	// Fresh: no file → config defaults, whitelisted preserved at 5×.
	c := loadUploadCaps(path, 500, 2500)
	if c.normalBytes() != 500*mb || c.whitelistBytes() != 2500*mb {
		t.Fatalf("defaults wrong: normal=%d whitelist=%d", c.normalBytes(), c.whitelistBytes())
	}

	// A whitelisted default below the normal default is lifted to the normal cap.
	if lifted := loadUploadCaps(filepath.Join(t.TempDir(), "x.json"), 500, 100); lifted.whitelistBytes() != 500*mb {
		t.Fatalf("whitelisted below normal should lift to normal, got %d", lifted.whitelistBytes())
	}

	// Set + persist.
	if err := c.set(1000, 8000); err != nil {
		t.Fatalf("set: %v", err)
	}
	if n, w := c.snapshotMB(); n != 1000 || w != 8000 {
		t.Fatalf("after set: normal=%d whitelist=%d", n, w)
	}
	if reloaded := loadUploadCaps(path, 500, 2500); reloaded.normalBytes() != 1000*mb || reloaded.whitelistBytes() != 8000*mb {
		t.Fatalf("override not persisted: normal=%d whitelist=%d", reloaded.normalBytes(), reloaded.whitelistBytes())
	}

	// Reset restores the config defaults and removes the persisted override.
	if err := c.reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if n, w := c.snapshotMB(); n != 500 || w != 2500 {
		t.Fatalf("after reset: normal=%d whitelist=%d, want 500/2500", n, w)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("reset should have removed the override file, stat err=%v", err)
	}
	// A reload with no file falls back to config defaults.
	if reloaded := loadUploadCaps(path, 500, 2500); reloaded.normalBytes() != 500*mb {
		t.Fatalf("after reset+reload normal=%d, want 500 MB", reloaded.normalBytes())
	}
}

func TestUploadCapsValidation(t *testing.T) {
	c := loadUploadCaps(filepath.Join(t.TempDir(), "caps.json"), 500, 2500)
	orig, origW := c.snapshotMB()

	for _, tc := range []struct {
		name             string
		normal, whitelist int64
	}{
		{"zero normal", 0, 2500},
		{"negative normal", -1, 2500},
		{"whitelist below normal", 500, 400},
		{"normal over max", maxCapMB + 1, maxCapMB + 1},
		{"whitelist over max", 500, maxCapMB + 1},
	} {
		if err := c.set(tc.normal, tc.whitelist); err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
	}
	// A rejected set must not have mutated the live caps.
	if n, w := c.snapshotMB(); n != orig || w != origW {
		t.Fatalf("rejected set mutated caps: normal=%d whitelist=%d", n, w)
	}
}
