package server

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// deletionTTL is how long an accepted NIP-09 deletion event (kind 5) is kept in
// the store before being swept. The deletion's effect is captured permanently in
// the deletions record below, so the raw kind-5 event is only useful briefly (for
// NIP-09 "the relay retains the deletion" semantics) and is pruned afterwards.
const deletionTTL = 24 * time.Hour

// deletionSweepInterval is how often the kind-5 pruning sweep runs.
const deletionSweepInterval = time.Hour

// deletions is a persistent record of author-initiated NIP-09 takedowns, keyed by
// the target's moderationKey ("<kind>:<pubkey>:<d>" for mods, else its id) mapped
// to the deletion's created_at. An incoming (or re-ingested) mod whose key is in
// the set and whose created_at is <= the recorded timestamp is rejected, so a
// delete survives the node re-mirroring the original event from another relay —
// while a genuinely NEWER re-publish at the same coordinate is still allowed.
type deletions struct {
	mu   sync.RWMutex
	path string
	set  map[string]int64 // moderationKey -> deletion created_at
}

func loadDeletions(path string) *deletions {
	d := &deletions{path: path, set: map[string]int64{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &d.set)
	}
	return d
}

// deletedAt returns the created_at of the newest deletion recorded for key, or 0.
func (d *deletions) deletedAt(key string) int64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.set[key]
}

// record notes a takedown for key at timestamp ts, keeping the newest.
func (d *deletions) record(key string, ts int64) error {
	d.mu.Lock()
	if ts > d.set[key] {
		d.set[key] = ts
	}
	d.mu.Unlock()
	return d.save()
}

func (d *deletions) save() error {
	d.mu.RLock()
	data, _ := json.MarshalIndent(d.set, "", "  ")
	d.mu.RUnlock()
	return os.WriteFile(d.path, data, 0o600)
}

// onDeletionOutcome is khatru's OverwriteDeletionOutcome hook. It runs when a
// kind-5 deletion request references a stored event, BEFORE rejectEvent sees the
// kind-5 itself. We re-apply NIP-09's author check and, when the deletion is
// accepted for one of our mod kinds, persist it so re-ingest can't resurrect it.
func (s *Server) onDeletionOutcome(ctx context.Context, target, deletion *nostr.Event) (bool, string) {
	if target.PubKey != deletion.PubKey {
		return false, "you are not the author of this event"
	}
	if acceptedKind(target.Kind) {
		_ = s.deletions.record(moderationKey(target), int64(deletion.CreatedAt))
	}
	return true, ""
}

// RunDeletionSweep periodically prunes stored kind-5 events older than deletionTTL.
// Their lasting effect lives in the deletions record, so the raw events are dead
// weight once acted on. Blocks until ctx is cancelled.
func (s *Server) RunDeletionSweep(ctx context.Context) {
	t := time.NewTicker(deletionSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweepDeletionEvents(ctx)
		}
	}
}

func (s *Server) sweepDeletionEvents(ctx context.Context) {
	cutoff := nostr.Timestamp(time.Now().Add(-deletionTTL).Unix())
	ch, err := s.store.QueryEvents(ctx, nostr.Filter{Kinds: []int{5}, Until: &cutoff})
	if err != nil {
		return
	}
	for evt := range ch {
		_ = s.store.DeleteEvent(ctx, evt)
	}
}
