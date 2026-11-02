package hub

import (
	"context"
	"sync"
	"time"
)

// PosterCache holds each whitelisted hub's poster set, rebuilt when the hub's
// member-tree index changes. The 36942 watcher feeds it (coordinate, indexHash,
// epoch) from the hub event's `m` tag; gating reads it on every event.
//
// Rebuilds run the (I/O-bound) build function OUTSIDE the lock, then swap the
// result in under the lock with an epoch guard so an out-of-order older revision
// can never clobber a newer poster set.
type PosterCache struct {
	mu    sync.RWMutex
	build func(ctx context.Context, indexHash string) (PosterSet, error)
	byHub map[string]*cachedPosters
}

type cachedPosters struct {
	set       PosterSet
	indexHash string
	epoch     int
	builtAt   time.Time
}

// NewPosterCache builds a cache. build fetches + parses the member tree for an
// index hash (in production: BuildPosterSet over a storage-backed Fetcher).
func NewPosterCache(build func(ctx context.Context, indexHash string) (PosterSet, error)) *PosterCache {
	return &PosterCache{build: build, byHub: map[string]*cachedPosters{}}
}

// Update ensures the cached poster set for coord reflects indexHash at the given
// epoch, rebuilding only when the index hash actually changed. It returns the
// current (post-update) poster set.
//
//   - Same index hash as cached: no work, returns the cached set.
//   - Different hash, epoch >= cached: rebuild and store.
//   - Different hash, epoch < cached: a stale/out-of-order revision; the rebuilt
//     set is discarded and the newer cached set is kept.
func (c *PosterCache) Update(ctx context.Context, coord, indexHash string, epoch int) (PosterSet, error) {
	c.mu.RLock()
	cur, ok := c.byHub[coord]
	c.mu.RUnlock()
	if ok && cur.indexHash == indexHash {
		return cur.set, nil
	}

	set, err := c.build(ctx, indexHash)
	if err != nil {
		if ok {
			return cur.set, err // keep serving the last good set on a build failure
		}
		return PosterSet{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.byHub[coord]; ok && existing.epoch > epoch {
		return existing.set, nil // a newer revision won the race; keep it
	}
	c.byHub[coord] = &cachedPosters{set: set, indexHash: indexHash, epoch: epoch, builtAt: time.Now()}
	return set, nil
}

// Get returns the cached poster set for coord, if any.
func (c *PosterCache) Get(coord string) (PosterSet, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.byHub[coord]
	if !ok {
		return PosterSet{}, false
	}
	return e.set, true
}

// Has reports whether pubkey is an authorized poster in the cached set for coord.
// A hub with no cached set yet returns false (fail closed).
func (c *PosterCache) Has(coord, pubkey string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.byHub[coord]
	return ok && e.set.Has(pubkey)
}

// Epoch returns the epoch the cached set for coord was built at (-1 if none).
func (c *PosterCache) Epoch(coord string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if e, ok := c.byHub[coord]; ok {
		return e.epoch
	}
	return -1
}

// Remove drops the cached set for coord (call when a hub is un-whitelisted).
func (c *PosterCache) Remove(coord string) {
	c.mu.Lock()
	delete(c.byHub, coord)
	c.mu.Unlock()
}

// Len is the number of hubs with a cached poster set.
func (c *PosterCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byHub)
}
