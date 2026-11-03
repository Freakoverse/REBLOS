package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
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
// and reconciles it, so the relay stays current (and, via the poster-cache's
// mirror hook, pulls the member tree into local storage) even when a revision is
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
	// Pull from the hub's own relay hints (naddr hints + the stored event's `r`
	// tags) AND the operator's enabled general sync relays, so a hub with no hints
	// is still discoverable and a general relay can fill a gap the hints missed.
	relays := mergeRelays(h.Relays, s.syncR.enabled())
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

	// 2. Pull the hub's content events (member-gated on store). On the hub's first
	//    refresh this process, do a deep historical back-fill (paginating each relay
	//    backward until exhausted); every refresh then does the cheap recent catch-up.
	if s.markBackfill(h.Coordinate()) {
		s.backfillHubContent(ctx, relays, h)
	}
	s.syncHubContent(ctx, relays, h)

	// 3. Mirror the hub's structural blobs (index, leaf pages, spine, history,
	//    struct) from its Blossom hints, so the full member tree survives here.
	if cur := s.latestHubEvent(ctx, h.Pubkey, h.DTag); cur != nil {
		if meta, ok := parseHubEventMeta(cur); ok {
			s.mirrorHubBlobs(ctx, h, meta.IndexHash, meta.StructHash, meta.BlossomServers)
		}
	}
}

// mirrorHubBlobs ensures every structural blob a hub references is in local
// storage: it fetches the index, parses it, and fetches each referenced blob
// (leaf pages / tree, spine, history) plus the struct blob through a mirroring
// fetcher, local-first, so an already-stored blob is a no-op and a missing one is
// pulled from the hub's Blossom hints (sha256-verified) and stored.
func (s *Server) mirrorHubBlobs(ctx context.Context, h *hub.Hub, indexHash, structHash string, remotes []string) {
	if indexHash == "" || len(remotes) == 0 {
		return // nothing to pull, or nowhere to pull from
	}
	f := &hub.Fetcher{Local: s.storage, Remotes: remotes, Mirror: s.mirrorHubBlob}
	idxText, err := f.Fetch(ctx, indexHash)
	if err != nil {
		return
	}
	// Attribute each structural blob to the hub: the creator (36942 author) as its
	// uploader, and the hub coordinate as its owner. This shows the owner + hub in the
	// Files view (instead of orphaned "unclaimed") and lets a hub removal optionally
	// delete only the blobs it solely owns.
	coord := h.Coordinate()
	s.owners.add(indexHash, h.Pubkey)
	s.blobHubs.add(indexHash, coord)
	hashes := hub.ParseIndex(idxText).AllBlobHashes()
	if structHash != "" {
		hashes = append(hashes, structHash)
	}
	for _, blobHash := range hashes {
		if ctx.Err() != nil {
			return
		}
		if _, err := f.Fetch(ctx, blobHash); err == nil { // local hit = no-op; remote hit mirrors via the hook
			s.owners.add(blobHash, h.Pubkey)
			s.blobHubs.add(blobHash, coord)
		}
	}
}

// mirrorOnRead lazily pulls a blob that is not stored locally from the whitelisted
// hubs' Blossom servers, verifies its sha256, classifies it, stores it, and returns
// a reader: the media-durability half of §6.
//
// A hub message's attachment hashes live inside AES-encrypted content the relay
// cannot read, so message media cannot be mirrored proactively the way the member
// tree is. Instead it is mirrored the first time a member actually fetches it: the
// sha256 in the request URL is all that is needed (no decryption), and the fetcher
// verifies the hash, so a wrong or tampered blob is never stored. Blacklisted and
// quarantined hashes are already withheld by the download gate before this runs.
// Best-effort: any failure returns an error and the GET 404s as before.
func (s *Server) mirrorOnRead(ctx context.Context, sha256hex, ext string) (io.ReadSeekCloser, error) {
	if !isSHA256Hex(sha256hex) {
		return nil, fmt.Errorf("not a blob hash")
	}
	remotes := s.hubBlossomUnion(ctx)
	if len(remotes) == 0 {
		return nil, fmt.Errorf("no hub Blossom servers to mirror from")
	}
	// Bound by the configured upload ceiling (0 => the fetcher's own default); the
	// blob is buffered in memory once, then written to storage.
	max := s.maxUploadBytes
	f := &hub.Fetcher{Remotes: remotes, MaxBytes: max}
	text, err := f.Fetch(ctx, sha256hex) // sha256-verified against the request hash
	if err != nil {
		return nil, err
	}
	b := []byte(text)
	fext, ctype, err := s.classifyBytes(b)
	if err != nil {
		return nil, err // a type this server does not host
	}
	if err := s.storage.Store(ctx, sha256hex, fext, ctype, bytes.NewReader(b), int64(len(b))); err != nil {
		slog.Warn("mirror-on-read store failed", "hash", sha256hex, "err", err)
		return nil, err
	}
	s.invalidateBlobCache()
	slog.Info("mirrored blob on read", "hash", sha256hex, "ext", fext, "bytes", len(b))
	return s.storage.Load(ctx, sha256hex, fext)
}

// hubContentFilters returns the hub-scoped filters that select a hub's member
// content: its #h-tagged kinds (messages, reactions, polls, pins, voice, hide,
// typing), its #a-tagged reports, and its d+p-tagged join requests.
func (s *Server) hubContentFilters(h *hub.Hub) []nostr.Filter {
	return []nostr.Filter{
		{Kinds: hubContentKinds, Tags: nostr.TagMap{"h": []string{h.DTag}}},
		{Kinds: []int{kindReport}, Tags: nostr.TagMap{"a": []string{h.Coordinate()}}},
		{Kinds: []int{kindJoinRequest}, Tags: nostr.TagMap{"d": []string{h.DTag}, "p": []string{h.Pubkey}}},
	}
}

// syncHubContent fetches the hub's member-authored events from its relays and
// stores those that pass the write policy (member-gated), so the node catches up
// on anything published while it was down or unreachable. A bounded, recent-catch-up
// pass (one FetchMany per filter); the deep history is handled by backfillHubContent.
func (s *Server) syncHubContent(ctx context.Context, relays []string, h *hub.Hub) {
	pool := nostr.NewSimplePool(ctx)
	stored := 0
	for _, f := range s.hubContentFilters(h) {
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

// markBackfill returns true the first time it is called for a coordinate in this
// process, and false afterward, so the deep back-fill runs once per hub per run.
func (s *Server) markBackfill(coord string) bool {
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	if s.backfilled[coord] {
		return false
	}
	s.backfilled[coord] = true
	return true
}

// backfillHubContent does a one-time deep historical pull of a hub's content,
// paginating each relay backward until exhausted (a single subscription's returns
// are capped by the relay). Hub-scoped and member-gated, so it never mirrors spam.
// This is the per-hub replacement for the old global ingest back-fill.
func (s *Server) backfillHubContent(ctx context.Context, relays []string, h *hub.Hub) {
	pool := nostr.NewSimplePool(ctx)
	total := 0
	for _, f := range s.hubContentFilters(h) {
		for _, relay := range relays {
			total += s.backfillFilterRelay(ctx, pool, relay, f)
		}
	}
	if total > 0 {
		slog.Info("hub content backfilled", "coord", h.Coordinate(), "stored", total)
	}
}

// backfillFilterRelay paginates one relay backward for one filter until it stops
// returning events, storing each member-gated event. Each relay is paginated
// INDEPENDENTLY: a shared cursor across relays holding different date ranges would
// let one relay's lone ancient event jump the cursor past another's middle range.
// Returns how many events it newly stored.
func (s *Server) backfillFilterRelay(ctx context.Context, pool *nostr.SimplePool, relay string, filter nostr.Filter) int {
	const window = 500
	one := []string{relay}
	until := nostr.Now()
	stored := 0
	for {
		if ctx.Err() != nil {
			return stored
		}
		f := filter
		f.Until = &until
		f.Limit = window

		got := 0
		oldest := until
		fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		for re := range pool.FetchMany(fctx, one, f) {
			ev := re.Event
			if ev == nil {
				continue
			}
			got++
			if ev.CreatedAt < oldest {
				oldest = ev.CreatedAt
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
		// Stop when a window returns nothing, or we cannot move further back.
		if got == 0 || oldest >= until {
			return stored
		}
		until = oldest - 1
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
