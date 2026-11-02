package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/Freakoverse/REBLOS/internal/hub"
	"github.com/nbd-wtf/go-nostr"
)

// defaultHubRefreshInterval is how often the watcher re-pulls each hub's current
// 36942 from the hub's own relays. Clamped to a 1-minute floor.
const defaultHubRefreshInterval = 15 * time.Minute

// RunHubRefresh is the per-hub sync watcher (REBLOS-MIGRATION §6). On a timer it
// re-fetches each whitelisted hub's latest 36942 from the hub's own relay hints
// and reconciles it, so the relay stays current — and, via the poster-cache's
// mirror hook, pulls the member tree into local storage — even when a revision is
// never republished directly here. Pull-only; clients still publish widely via
// their own failover. Blocks until ctx is cancelled.
func (s *Server) RunHubRefresh(ctx context.Context, interval time.Duration) {
	if interval < time.Minute {
		interval = defaultHubRefreshInterval
	}
	slog.Info("hub refresh watcher started", "interval", interval.String())
	s.refreshAllHubs(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.refreshAllHubs(ctx)
		}
	}
}

func (s *Server) refreshAllHubs(ctx context.Context) {
	for _, h := range s.hubs.List() {
		if ctx.Err() != nil {
			return
		}
		s.refreshHub(ctx, h)
	}
}

// refreshHub pulls a single hub's latest 36942 and its member-authored content
// from the hub's relay hints. Relay hints come from the admin-supplied naddr plus
// the `r` tags of the stored / fetched 36942. A newer hub revision is stored and
// reconciled (rebuilding the poster set and mirroring the member tree); then the
// hub's content events are pulled and member-gated into the store.
func (s *Server) refreshHub(ctx context.Context, h *hub.Hub) {
	stored := s.latestHubEvent(ctx, h.Pubkey, h.DTag)
	relays := h.Relays
	if stored != nil {
		relays = mergeRelays(relays, relayHintsOf(stored))
	}
	if len(relays) == 0 {
		return // nothing to pull from
	}

	// 1. Pull a newer hub event, if any, and reconcile it.
	if ev := s.fetchHubEventFromRelays(ctx, relays, h.Pubkey, h.DTag); ev != nil {
		if stored == nil || ev.CreatedAt > stored.CreatedAt {
			if reject, _ := s.rejectEvent(ctx, ev); !reject {
				if err := s.store.ReplaceEvent(ctx, ev); err != nil {
					slog.Warn("hub refresh store failed", "coord", h.Coordinate(), "err", err)
				} else {
					slog.Info("hub refreshed from relays", "coord", h.Coordinate(), "created_at", int64(ev.CreatedAt))
					s.reconcileHubEvent(ctx, ev)
					relays = mergeRelays(relays, relayHintsOf(ev))
				}
			}
		}
	}

	// 2. Pull the hub's content events (member-gated on store).
	s.syncHubContent(ctx, relays, h)

	// 3. Mirror the hub's structural blobs (index, leaf pages, spine, history,
	//    struct) from its Blossom hints, so the full member tree survives here.
	if cur := s.latestHubEvent(ctx, h.Pubkey, h.DTag); cur != nil {
		if meta, ok := parseHubEventMeta(cur); ok {
			s.mirrorHubBlobs(ctx, meta.IndexHash, meta.StructHash, meta.BlossomServers)
		}
	}
}

// mirrorHubBlobs ensures every structural blob a hub references is in local
// storage: it fetches the index, parses it, and fetches each referenced blob
// (leaf pages / tree, spine, history) plus the struct blob through a mirroring
// fetcher — local-first, so an already-stored blob is a no-op and a missing one is
// pulled from the hub's Blossom hints (sha256-verified) and stored.
func (s *Server) mirrorHubBlobs(ctx context.Context, indexHash, structHash string, remotes []string) {
	if indexHash == "" || len(remotes) == 0 {
		return // nothing to pull, or nowhere to pull from
	}
	f := &hub.Fetcher{Local: s.storage, Remotes: remotes, Mirror: s.mirrorHubBlob}
	idxText, err := f.Fetch(ctx, indexHash)
	if err != nil {
		return
	}
	hashes := hub.ParseIndex(idxText).AllBlobHashes()
	if structHash != "" {
		hashes = append(hashes, structHash)
	}
	for _, h := range hashes {
		if ctx.Err() != nil {
			return
		}
		_, _ = f.Fetch(ctx, h) // local hit = no-op; remote hit mirrors via the hook
	}
}

// syncHubContent fetches the hub's member-authored events from its relays and
// stores those that pass the write policy (member-gated), so the node catches up
// on anything published while it was down or unreachable. One-shot per relay set.
func (s *Server) syncHubContent(ctx context.Context, relays []string, h *hub.Hub) {
	pool := nostr.NewSimplePool(ctx)
	filters := []nostr.Filter{
		// Messages, reactions, polls, pins, voice, hide, typing — all tagged #h.
		{Kinds: hubContentKinds, Tags: nostr.TagMap{"h": []string{h.DTag}}},
		// Reports reference the hub by its #a coordinate instead of #h.
		{Kinds: []int{kindReport}, Tags: nostr.TagMap{"a": []string{h.Coordinate()}}},
		// Join requests reference the hub by d + p (the creator).
		{Kinds: []int{kindJoinRequest}, Tags: nostr.TagMap{"d": []string{h.DTag}, "p": []string{h.Pubkey}}},
	}
	stored := 0
	for _, f := range filters {
		fctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		for re := range pool.FetchMany(fctx, relays, f) {
			ev := re.Event
			if ev == nil {
				continue
			}
			if ok, err := ev.CheckSignature(); err != nil || !ok {
				continue
			}
			if reject, _ := s.rejectEvent(ctx, ev); reject {
				continue
			}
			if s.storeEvent(ctx, ev) {
				stored++
			}
		}
		cancel()
	}
	if stored > 0 {
		slog.Info("hub content synced", "coord", h.Coordinate(), "stored", stored)
	}
}

// storeEvent persists an event, routing replaceable/addressable kinds to
// ReplaceEvent (keep newest per coordinate) and regular kinds to SaveEvent.
// Returns whether it was newly stored (a duplicate SaveEvent is not an error we
// surface, just not counted).
func (s *Server) storeEvent(ctx context.Context, ev *nostr.Event) bool {
	var err error
	if nostr.IsReplaceableKind(ev.Kind) || nostr.IsAddressableKind(ev.Kind) {
		err = s.store.ReplaceEvent(ctx, ev)
	} else {
		err = s.store.SaveEvent(ctx, ev)
	}
	return err == nil
}

// mergeRelays returns the union of two relay-URL lists, order-preserving, deduped.
func mergeRelays(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{a, b} {
		for _, u := range list {
			if u != "" && !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	}
	return out
}

// fetchHubEventFromRelays fetches the newest valid 36942 for a hub coordinate from
// the given relays, with a bounded timeout. Returns nil if none is found.
func (s *Server) fetchHubEventFromRelays(ctx context.Context, relays []string, pubkey, dTag string) *nostr.Event {
	pool := nostr.NewSimplePool(ctx)
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	filter := nostr.Filter{
		Kinds:   []int{hub.Kind},
		Authors: []string{pubkey},
		Tags:    nostr.TagMap{"d": []string{dTag}},
		Limit:   1,
	}
	var newest *nostr.Event
	for re := range pool.FetchMany(fctx, relays, filter) {
		ev := re.Event
		if ev == nil || ev.Kind != hub.Kind || ev.PubKey != pubkey {
			continue
		}
		// From untrusted relays: verify the signature ourselves.
		if ok, err := ev.CheckSignature(); err != nil || !ok {
			continue
		}
		if newest == nil || ev.CreatedAt > newest.CreatedAt {
			newest = ev
		}
	}
	return newest
}
