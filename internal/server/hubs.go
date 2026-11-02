package server

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/Freakoverse/REBLOS/internal/hub"
	"github.com/nbd-wtf/go-nostr"
)

// hubEventMeta is the subset of a kind-36942 hub event the relay needs to keep a
// hub's poster set current: which member-tree index to read, at which epoch, and
// the Blossom servers to fall back to when a blob is not stored locally.
type hubEventMeta struct {
	DTag           string
	IndexHash      string   // from the `m` tag
	Epoch          int      // from the `m` tag's third element
	BlossomServers []string // from `o` tags
}

// parseHubEventMeta extracts the member-tree pointer and Blossom hints from a hub
// event. ok is false unless it is a 36942 event carrying both a `d` tag and an
// `m` (member-tree index) tag.
func parseHubEventMeta(ev *nostr.Event) (meta hubEventMeta, ok bool) {
	if ev == nil || ev.Kind != hub.Kind {
		return meta, false
	}
	for _, t := range ev.Tags {
		if len(t) < 2 {
			continue
		}
		switch t[0] {
		case "d":
			meta.DTag = t[1]
		case "m":
			meta.IndexHash = t[1]
			if len(t) >= 3 {
				meta.Epoch, _ = strconv.Atoi(t[2])
			}
		case "o":
			if t[1] != "" {
				meta.BlossomServers = append(meta.BlossomServers, t[1])
			}
		}
	}
	ok = meta.DTag != "" && meta.IndexHash != ""
	return meta, ok
}

// hubCoordinate is "36942:<pubkey>:<dTag>".
func hubCoordinate(pubkey, dTag string) string {
	return strconv.Itoa(hub.Kind) + ":" + pubkey + ":" + dTag
}

// reconcileHubEvent rebuilds a whitelisted hub's poster set from the member tree
// referenced by this hub event. Events for hubs that are not whitelisted are
// ignored. The cache itself no-ops when the index hash has not changed and guards
// against out-of-order revisions, so this is cheap to call on every 36942 save.
func (s *Server) reconcileHubEvent(ctx context.Context, ev *nostr.Event) {
	meta, ok := parseHubEventMeta(ev)
	if !ok {
		return
	}
	coord := hubCoordinate(ev.PubKey, meta.DTag)
	if !s.hubs.Has(coord) {
		return
	}
	ps, err := s.posters.Update(ctx, coord, meta.IndexHash, meta.Epoch, meta.BlossomServers)
	if err != nil {
		slog.Warn("hub poster-set build failed", "coord", coord, "epoch", meta.Epoch, "err", err)
		return
	}
	slog.Info("hub poster-set updated", "coord", coord, "epoch", meta.Epoch, "members", ps.Len())
}

// reconcileHubsFromStore builds poster sets for every whitelisted hub whose 36942
// event is already in the local store. Called at startup; hubs added later are
// reconciled when their event is saved (OnEventSaved) or on admin add.
func (s *Server) reconcileHubsFromStore(ctx context.Context) {
	for _, h := range s.hubs.List() {
		if ev := s.latestHubEvent(ctx, h.Pubkey, h.DTag); ev != nil {
			s.reconcileHubEvent(ctx, ev)
		}
	}
}

// latestHubEvent returns the current (newest) stored 36942 for a hub coordinate,
// or nil. 36942 is addressable, so the store holds at most one per (author, dTag);
// the created_at comparison is belt-and-suspenders.
func (s *Server) latestHubEvent(ctx context.Context, pubkey, dTag string) *nostr.Event {
	ch, err := s.store.QueryEvents(ctx, nostr.Filter{
		Kinds:   []int{hub.Kind},
		Authors: []string{pubkey},
		Tags:    nostr.TagMap{"d": []string{dTag}},
	})
	if err != nil {
		return nil
	}
	var latest *nostr.Event
	for ev := range ch {
		if latest == nil || ev.CreatedAt > latest.CreatedAt {
			latest = ev
		}
	}
	return latest
}
