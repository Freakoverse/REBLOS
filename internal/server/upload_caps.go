package server

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
)

// Per-upload size-cap bounds (MB). Sanity limits so a fat-fingered value can't
// disable uploads (too low) or overflow (too high); the real ceiling on any
// upload is the node's free disk, checked separately.
const (
	minCapMB int64 = 1
	maxCapMB int64 = 1 << 20 // 1 TiB, expressed in MB
)

// uploadCaps holds the per-upload size caps — the normal cap and the raised cap
// for whitelisted keys — both in MB. Seeded from the node config, overridable at
// runtime from the admin dashboard, and persisted as JSON so an edit survives a
// restart. The upload path reads these fresh on every request, so a change takes
// effect on the next upload and never disturbs one already in flight.
type uploadCaps struct {
	mu             sync.RWMutex
	saveMu         sync.Mutex
	path           string
	normalMB       int64
	whitelistMB    int64
	defNormalMB    int64 // config defaults, kept so "reset" can restore them
	defWhitelistMB int64
}

type uploadCapsFile struct {
	NormalMB    int64 `json:"normal_mb"`
	WhitelistMB int64 `json:"whitelist_mb"`
}

// loadUploadCaps starts from the config defaults, then lays any persisted override
// on top. A missing or corrupt file just leaves the defaults in place — uploads
// never end up without a cap.
func loadUploadCaps(path string, defNormalMB, defWhitelistMB int64) *uploadCaps {
	if defWhitelistMB < defNormalMB {
		defWhitelistMB = defNormalMB
	}
	c := &uploadCaps{
		path:           path,
		normalMB:       defNormalMB,
		whitelistMB:    defWhitelistMB,
		defNormalMB:    defNormalMB,
		defWhitelistMB: defWhitelistMB,
	}
	if data, err := os.ReadFile(path); err == nil {
		var f uploadCapsFile
		if json.Unmarshal(data, &f) == nil {
			if f.NormalMB >= minCapMB && f.NormalMB <= maxCapMB {
				c.normalMB = f.NormalMB
			}
			if f.WhitelistMB >= c.normalMB && f.WhitelistMB <= maxCapMB {
				c.whitelistMB = f.WhitelistMB
			}
		}
	}
	return c
}

func (c *uploadCaps) normalBytes() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.normalMB * 1024 * 1024
}

func (c *uploadCaps) whitelistBytes() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.whitelistMB * 1024 * 1024
}

// snapshotMB returns the current caps in MB, for display.
func (c *uploadCaps) snapshotMB() (normalMB, whitelistMB int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.normalMB, c.whitelistMB
}

// defaultsMB returns the config defaults in MB (the values "reset" restores to).
func (c *uploadCaps) defaultsMB() (normalMB, whitelistMB int64) {
	return c.defNormalMB, c.defWhitelistMB // set once at construction, never mutated
}

// reset restores the config defaults and removes the persisted override, so the
// node is back to its configured caps and a future restart re-seeds from config.
func (c *uploadCaps) reset() error {
	c.mu.Lock()
	c.normalMB = c.defNormalMB
	c.whitelistMB = c.defWhitelistMB
	c.mu.Unlock()
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	if err := os.Remove(c.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// set validates and applies new caps (MB), persisting them. The whitelisted cap
// must be at least the normal cap, and both must sit within [minCapMB, maxCapMB].
func (c *uploadCaps) set(normalMB, whitelistMB int64) error {
	if normalMB < minCapMB || normalMB > maxCapMB {
		return errors.New("max upload size must be between 1 and 1048576 MB")
	}
	if whitelistMB < normalMB || whitelistMB > maxCapMB {
		return errors.New("whitelisted cap must be at least the max upload size and at most 1048576 MB")
	}
	c.mu.Lock()
	c.normalMB = normalMB
	c.whitelistMB = whitelistMB
	c.mu.Unlock()
	return c.save()
}

// save snapshots under the read lock, then writes under a dedicated mutex so two
// concurrent edits can't interleave a half-written file.
func (c *uploadCaps) save() error {
	c.mu.RLock()
	data, _ := json.Marshal(uploadCapsFile{NormalMB: c.normalMB, WhitelistMB: c.whitelistMB})
	c.mu.RUnlock()
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	return os.WriteFile(c.path, data, 0o600)
}
