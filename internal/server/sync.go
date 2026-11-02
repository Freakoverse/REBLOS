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

// refreshHub pulls a single hub's latest 36942 from its relay hints and reconciles
// it. Relay hints come from the admin-supplied naddr, falling back to the `r` tags
// of whatever 36942 is already stored. A newer revision than what we hold is stored
// and reconciled (which rebuilds the poster set and mirrors the member tree).
func (s *Server) refreshHub(ctx context.Context, h *hub.Hub) {
	relays := h.Relays
	stored := s.latestHubEvent(ctx, h.Pubkey, h.DTag)
	if len(relays) == 0 && stored != nil {
		relays = relayHintsOf(stored)
	}
	if len(relays) == 0 {
		return // nothing to pull from
	}

	ev := s.fetchHubEventFromRelays(ctx, relays, h.Pubkey, h.DTag)
	if ev == nil {
		return
	}
	// Skip when it is not newer than what we already have.
	if stored != nil && ev.CreatedAt <= stored.CreatedAt {
		return
	}
	if reject, _ := s.rejectEvent(ctx, ev); reject {
		return
	}
	if err := s.store.ReplaceEvent(ctx, ev); err != nil {
		slog.Warn("hub refresh store failed", "coord", h.Coordinate(), "err", err)
		return
	}
	slog.Info("hub refreshed from relays", "coord", h.Coordinate(), "created_at", int64(ev.CreatedAt))
	s.reconcileHubEvent(ctx, ev)
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
