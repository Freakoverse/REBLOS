package hub

import (
	"context"
	"errors"
	"testing"
)

// fakeBuilder returns a poster set per index hash and counts how often it runs.
type fakeBuilder struct {
	sets  map[string]PosterSet
	calls int
	fail  bool
}

func (b *fakeBuilder) build(_ context.Context, indexHash string) (PosterSet, error) {
	b.calls++
	if b.fail {
		return PosterSet{}, errors.New("build failed")
	}
	ps, ok := b.sets[indexHash]
	if !ok {
		return PosterSet{Roles: map[string]string{}}, nil
	}
	return ps, nil
}

func set(pubkeys ...string) PosterSet {
	ps := PosterSet{Roles: map[string]string{}, Flags: map[string]string{}}
	for _, pk := range pubkeys {
		ps.Roles[pk] = "everyone"
	}
	return ps
}

const coord = "36942:" + pkA + ":the-hub"

func TestCacheBuildsAndCaches(t *testing.T) {
	b := &fakeBuilder{sets: map[string]PosterSet{"idx1": set(pkA, pkB)}}
	c := NewPosterCache(b.build)

	ps, err := c.Update(context.Background(), coord, "idx1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if ps.Len() != 2 || !ps.Has(pkA) || !ps.Has(pkB) {
		t.Fatalf("poster set = %+v", ps.Roles)
	}
	if b.calls != 1 {
		t.Fatalf("build calls = %d, want 1", b.calls)
	}

	// Same index hash -> no rebuild.
	if _, err := c.Update(context.Background(), coord, "idx1", 1); err != nil {
		t.Fatal(err)
	}
	if b.calls != 1 {
		t.Fatalf("build ran again for the same index hash (calls=%d)", b.calls)
	}
	if !c.Has(coord, pkA) {
		t.Error("Has(pkA) false")
	}
	if c.Epoch(coord) != 1 {
		t.Errorf("epoch = %d, want 1", c.Epoch(coord))
	}
}

func TestCacheRebuildsOnNewEpoch(t *testing.T) {
	b := &fakeBuilder{sets: map[string]PosterSet{
		"idx1": set(pkA, pkB),
		"idx2": set(pkA), // pkB kicked
	}}
	c := NewPosterCache(b.build)

	_, _ = c.Update(context.Background(), coord, "idx1", 1)
	ps, err := c.Update(context.Background(), coord, "idx2", 2)
	if err != nil {
		t.Fatal(err)
	}
	if ps.Len() != 1 || ps.Has(pkB) {
		t.Fatalf("after epoch 2, set = %+v (pkB should be gone)", ps.Roles)
	}
	if b.calls != 2 {
		t.Fatalf("build calls = %d, want 2", b.calls)
	}
	if c.Epoch(coord) != 2 {
		t.Errorf("epoch = %d, want 2", c.Epoch(coord))
	}
}

func TestCacheIgnoresStaleRevision(t *testing.T) {
	b := &fakeBuilder{sets: map[string]PosterSet{
		"idx2": set(pkA),      // newer (epoch 2)
		"idx1": set(pkA, pkB), // older (epoch 1), arrives late
	}}
	c := NewPosterCache(b.build)

	// Newer revision lands first.
	_, _ = c.Update(context.Background(), coord, "idx2", 2)
	// An out-of-order older revision (different hash, lower epoch) must not win.
	ps, err := c.Update(context.Background(), coord, "idx1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if ps.Len() != 1 || ps.Has(pkB) {
		t.Fatalf("stale revision clobbered the newer set: %+v", ps.Roles)
	}
	if c.Epoch(coord) != 2 {
		t.Errorf("epoch = %d, want 2 (newer kept)", c.Epoch(coord))
	}
}

func TestCacheKeepsLastGoodOnBuildError(t *testing.T) {
	b := &fakeBuilder{sets: map[string]PosterSet{"idx1": set(pkA)}}
	c := NewPosterCache(b.build)
	_, _ = c.Update(context.Background(), coord, "idx1", 1)

	b.fail = true
	ps, err := c.Update(context.Background(), coord, "idx2", 2)
	if err == nil {
		t.Fatal("expected the build error to surface")
	}
	if ps.Len() != 1 || !ps.Has(pkA) {
		t.Fatalf("last-good set not retained on build failure: %+v", ps.Roles)
	}
	if !c.Has(coord, pkA) {
		t.Error("cache dropped the last-good set after a failed rebuild")
	}
}

func TestCacheRemove(t *testing.T) {
	b := &fakeBuilder{sets: map[string]PosterSet{"idx1": set(pkA)}}
	c := NewPosterCache(b.build)
	_, _ = c.Update(context.Background(), coord, "idx1", 1)
	if c.Len() != 1 {
		t.Fatalf("len = %d, want 1", c.Len())
	}
	c.Remove(coord)
	if _, ok := c.Get(coord); ok {
		t.Error("entry still present after Remove")
	}
	if c.Has(coord, pkA) {
		t.Error("Has true after Remove (should fail closed)")
	}
	if c.Epoch(coord) != -1 {
		t.Errorf("epoch = %d after remove, want -1", c.Epoch(coord))
	}
}
