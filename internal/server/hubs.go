package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

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
	StructHash     string   // from the `struct` tag (the encrypted hub-config blob), "" if none
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
		case "struct":
			meta.StructHash = t[1]
		}
	}
	ok = meta.DTag != "" && meta.IndexHash != ""
	return meta, ok
}

// hubCoordinate is "36942:<pubkey>:<dTag>".
func hubCoordinate(pubkey, dTag string) string {
	return strconv.Itoa(hub.Kind) + ":" + pubkey + ":" + dTag
}

// mirrorHubBlob stores a member-tree blob fetched from a hub's remote Blossom into
// local storage, so the member tree survives here even if the origin servers drop
// it (REBLOS-MIGRATION §6). Best-effort: member-tree blobs are text/plain, and the
// hash is already verified by the fetcher. Skips a blob that is already stored.
func (s *Server) mirrorHubBlob(ctx context.Context, sha256hex string, b []byte) {
	if ok, err := s.storage.Has(ctx, sha256hex, ""); err == nil && ok {
		return
	}
	if err := s.storage.Store(ctx, sha256hex, "txt", "text/plain", bytes.NewReader(b), int64(len(b))); err != nil {
		slog.Warn("mirror hub blob failed", "hash", sha256hex, "err", err)
		return
	}
	slog.Info("mirrored hub blob", "hash", sha256hex, "bytes", len(b))
}

// mayUpload reports whether a pubkey may store blobs on this node. REBLOS is a
// closed Blossom server: an uploader must be the admin, on the trusted-key
// whitelist, a whitelisted hub's creator (which bootstraps the very first member-
// tree upload, before any poster set exists), or a current member of some
// whitelisted hub. The acceptAllKinds fork switch opens uploads too.
func (s *Server) mayUpload(pubkey string) bool {
	if s.acceptAllKinds || pubkey == s.adminPubkey || s.white.has(pubkey) {
		return true
	}
	for _, h := range s.hubs.List() {
		if h.Pubkey == pubkey || s.posters.Has(h.Coordinate(), pubkey) {
			return true
		}
	}
	return false
}

// relayHintsOf returns the general relay URLs advertised in a hub event's `r` tags.
func relayHintsOf(ev *nostr.Event) []string {
	var out []string
	for _, t := range ev.Tags {
		if len(t) >= 2 && t[0] == "r" && t[1] != "" {
			out = append(out, t[1])
		}
	}
	return out
}

// maxBlossomUnion caps how many distinct Blossom servers the mirror-on-read
// fallback will try for one blob, bounding the fan-out when many hubs are
// whitelisted (and the amplification if someone requests random hashes).
const maxBlossomUnion = 24

// hubBlossomUnion returns the deduped set of Blossom server URLs advertised by the
// whitelisted hubs' current 36942 events, capped at maxBlossomUnion. It is the
// pool the mirror-on-read fallback pulls a locally-absent blob from: a hub message
// keeps its media on these servers, so any of them may hold the requested blob.
func (s *Server) hubBlossomUnion(ctx context.Context) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range s.hubs.List() {
		ev := s.latestHubEvent(ctx, h.Pubkey, h.DTag)
		if ev == nil {
			continue
		}
		meta, ok := parseHubEventMeta(ev)
		if !ok {
			continue
		}
		for _, u := range meta.BlossomServers {
			if u == "" || seen[u] {
				continue
			}
			seen[u] = true
			out = append(out, u)
			if len(out) >= maxBlossomUnion {
				return out
			}
		}
	}
	return out
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

// ── admin API: hub whitelist (NIP-98 admin, same as the other /admin/* routes) ──

// hubView is a registry entry enriched with the live poster-set status.
type hubView struct {
	Coordinate string   `json:"coordinate"`
	Pubkey     string   `json:"pubkey"`
	DTag       string   `json:"d"`
	Note       string   `json:"note,omitempty"`
	Relays     []string `json:"relays,omitempty"`
	Epoch      int      `json:"epoch"`   // poster-set epoch, -1 if not built yet
	Members    int      `json:"members"` // cached poster-set size, 0 if not built
}

func (s *Server) hubViewOf(h *hub.Hub) hubView {
	coord := h.Coordinate()
	v := hubView{Coordinate: coord, Pubkey: h.Pubkey, DTag: h.DTag, Note: h.Note, Relays: h.Relays, Epoch: s.posters.Epoch(coord)}
	if ps, ok := s.posters.Get(coord); ok {
		v.Members = ps.Len()
	}
	return v
}

// handleAdminHubsList returns every whitelisted hub with its poster-set status.
func (s *Server) handleAdminHubsList(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	hubs := s.hubs.List()
	out := make([]hubView, 0, len(hubs))
	for _, h := range hubs {
		out = append(out, s.hubViewOf(h))
	}
	writeJSON(w, http.StatusOK, map[string]any{"hubs": out})
}

// handleAdminHubsAdd whitelists a hub by naddr or raw coordinate, then reconciles
// its poster set from the local store (if its 36942 is already stored).
func (s *Server) handleAdminHubsAdd(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var body struct {
		Ref  string `json:"ref"` // naddr or "36942:<pubkey>:<dTag>"
		Note string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	h, err := hub.ParseHubRef(strings.TrimSpace(body.Ref))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	h.Note = strings.TrimSpace(body.Note)
	h.Added = nostr.Now().Time().Unix()
	if err := s.hubs.Add(h); err != nil {
		httpErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	// Build the poster set now if we already hold the hub event; otherwise it
	// builds when the 36942 arrives (OnEventSaved) or on the next startup.
	if ev := s.latestHubEvent(r.Context(), h.Pubkey, h.DTag); ev != nil {
		s.reconcileHubEvent(r.Context(), ev)
	}
	slog.Info("hub whitelisted", "coord", h.Coordinate())
	writeJSON(w, http.StatusOK, s.hubViewOf(h))
}

// handleAdminHubsRemove drops a hub from the whitelist and clears its poster set.
func (s *Server) handleAdminHubsRemove(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var body struct {
		Coordinate string `json:"coordinate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	coord := strings.TrimSpace(body.Coordinate)
	if _, err := s.hubs.Remove(coord); err != nil {
		httpErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	s.posters.Remove(coord)
	slog.Info("hub removed from whitelist", "coord", coord)
	w.WriteHeader(http.StatusNoContent)
}
