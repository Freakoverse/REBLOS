package server

import (
	"context"
	"log/slog"

	"github.com/nbd-wtf/go-nostr"
)

// RunIngest subscribes to the given relays for mod events (current 31142 + legacy
// 30402 GameMod) and stores every one that is validly signed and passes this
// relay's own policy. This turns the node into a comprehensive mirror/DB of all
// mods on the network, so clients can query one place and reliably get everything.
// It blocks until ctx is cancelled; the pool reconnects on drops.
func (s *Server) RunIngest(ctx context.Context, relays []string) {
	if len(relays) == 0 {
		return
	}
	pool := nostr.NewSimplePool(ctx)
	filters := nostr.Filters{
		{Kinds: []int{currentModKind}},
		{Kinds: []int{legacyModKind}, Tags: nostr.TagMap{"t": []string{"GameMod"}}},
	}
	slog.Info("ingest started", "relays", len(relays))

	// Back-fill all history first (relays cap a single subscription, so paginate
	// backward until exhausted), then stay live for new events.
	for _, f := range filters {
		s.backfill(ctx, pool, relays, f)
	}

	var stored, seen int
	for re := range pool.SubMany(ctx, relays, filters) {
		evt := re.Event
		if evt == nil {
			continue
		}
		seen++
		// Verify the signature ourselves — khatru does this for direct clients, but
		// ingested events come straight from possibly-untrusted relays.
		if ok, err := evt.CheckSignature(); err != nil || !ok {
			continue
		}
		if reject, _ := s.rejectEvent(ctx, evt); reject {
			continue
		}
		// Both mod kinds are addressable → ReplaceEvent keeps the latest per coordinate.
		if err := s.store.ReplaceEvent(ctx, evt); err != nil {
			continue
		}
		stored++
		if stored%200 == 0 {
			slog.Info("ingest progress", "stored", stored, "seen", seen)
		}
	}
	slog.Info("ingest stopped", "stored", stored, "seen", seen)
}

// backfill pulls the full history for a filter, paginating each relay INDEPENDENTLY.
// (A shared cursor across relays breaks when relays hold different date ranges: one
// relay's single ancient event would jump the cursor past another relay's middle
// range, silently skipping it.)
func (s *Server) backfill(ctx context.Context, pool *nostr.SimplePool, relays []string, filter nostr.Filter) {
	for _, relay := range relays {
		s.backfillRelay(ctx, pool, relay, filter)
	}
}

// backfillRelay paginates a single relay backward in windows until exhausted.
func (s *Server) backfillRelay(ctx context.Context, pool *nostr.SimplePool, relay string, filter nostr.Filter) {
	const window = 500
	one := []string{relay}
	until := nostr.Now()
	for {
		if ctx.Err() != nil {
			return
		}
		f := filter
		f.Until = &until
		f.Limit = window

		got, stored := 0, 0
		oldest := until
		for re := range pool.FetchMany(ctx, one, f) {
			evt := re.Event
			if evt == nil {
				continue
			}
			got++
			if evt.CreatedAt < oldest {
				oldest = evt.CreatedAt
			}
			if ok, err := evt.CheckSignature(); err != nil || !ok {
				continue
			}
			if reject, _ := s.rejectEvent(ctx, evt); reject {
				continue
			}
			if err := s.store.ReplaceEvent(ctx, evt); err == nil {
				stored++
			}
		}
		slog.Info("ingest backfill window", "relay", relay, "kinds", filter.Kinds, "fetched", got, "stored", stored)
		// Stop when a window returns nothing, or we can't move further back.
		if got == 0 || oldest >= until {
			return
		}
		until = oldest - 1
	}
}
