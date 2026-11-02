package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

// Admin relay browser — a read-only query over the stored events, filtered by
// kind / author / tags / time, with an optional free-text search. It never
// modifies anything; moderation actions (takedowns) stay on their own endpoints.

const (
	eventsDefaultLimit = 100
	eventsMaxLimit     = 2000   // an admin can ask for more than badger's 1000/query cap
	eventsScanCeiling  = 200000 // hard backstop so a broad search can't scan unbounded
)

// handleAdminEvents queries stored events. Structured filters (kinds, author,
// single-letter tags, since/until) are pushed into the event store; a free-text
// `search` is applied on top in-process (the store has no text index). Results
// are paged internally so the admin limit isn't bound by the store's per-query cap.
func (s *Server) handleAdminEvents(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	q := r.URL.Query()

	var f nostr.Filter
	for _, k := range strings.Split(q.Get("kinds"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			if n, err := strconv.Atoi(k); err == nil {
				f.Kinds = append(f.Kinds, n)
			}
		}
	}
	if a := resolvePubkey(strings.TrimSpace(q.Get("author"))); a != "" {
		f.Authors = []string{a}
	}
	// Repeated `tag=name:value`. Only single-letter tag names are indexed by the
	// store (per NIP-01); anything else won't match here — that's what search is for.
	for _, t := range q["tag"] {
		name, val, ok := strings.Cut(t, ":")
		if !ok || name == "" {
			continue
		}
		if f.Tags == nil {
			f.Tags = nostr.TagMap{}
		}
		f.Tags[name] = append(f.Tags[name], val)
	}
	if v := q.Get("since"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			ts := nostr.Timestamp(n)
			f.Since = &ts
		}
	}
	if v := q.Get("until"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			ts := nostr.Timestamp(n)
			f.Until = &ts
		}
	}
	limit := clampInt(atoiOr(q.Get("limit"), eventsDefaultLimit), 1, eventsMaxLimit)
	search := strings.ToLower(strings.TrimSpace(q.Get("search")))

	events, truncated, scanned := s.queryEventsPaged(r.Context(), f, search, limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"events":    events,
		"count":     len(events),
		"truncated": truncated, // true = the limit/scan-ceiling was hit, more may exist
		"scanned":   scanned,
	})
}

// queryEventsPaged walks the store newest-first in pages (working around the
// per-query cap), applies the free-text search in-process, and collects up to
// `limit` matches. Returns whether it stopped early (more may exist) and how many
// events it examined.
func (s *Server) queryEventsPaged(ctx context.Context, base nostr.Filter, search string, limit int) ([]*nostr.Event, bool, int) {
	const page = 1000 // == the store's default MaxLimit
	seen := map[string]struct{}{}
	out := []*nostr.Event{}
	scanned := 0

	until := nostr.Now()
	if base.Until != nil {
		until = *base.Until
	}
	for {
		f := base
		f.Limit = page
		u := until
		f.Until = &u
		ch, err := s.store.QueryEvents(ctx, f)
		if err != nil {
			return out, false, scanned
		}
		got := 0
		oldest := until
		for evt := range ch {
			got++
			if _, dup := seen[evt.ID]; !dup {
				seen[evt.ID] = struct{}{}
				scanned++
				if matchesSearch(evt, search) {
					out = append(out, evt)
					if len(out) >= limit {
						return out, true, scanned
					}
				}
			}
			if evt.CreatedAt < oldest {
				oldest = evt.CreatedAt
			}
		}
		if got < page || oldest >= until {
			return out, false, scanned // reached the end of the range
		}
		if scanned >= eventsScanCeiling {
			return out, true, scanned
		}
		until = oldest
	}
}

// matchesSearch reports whether an event matches a lowercased free-text term.
// The haystack is the event's raw JSON plus its addressable coordinate, naddr,
// and id — so a pasted naddr, an author, a d-tag, or any word in content/tags hits.
func matchesSearch(evt *nostr.Event, search string) bool {
	if search == "" {
		return true
	}
	if data, err := json.Marshal(evt); err == nil {
		if strings.Contains(strings.ToLower(string(data)), search) {
			return true
		}
	}
	if strings.Contains(strings.ToLower(coordOf(evt)), search) {
		return true
	}
	if d := tagValue(evt, "d"); d != "" {
		if naddr, err := nip19.EncodeEntity(evt.PubKey, evt.Kind, d, nil); err == nil {
			if strings.Contains(strings.ToLower(naddr), search) {
				return true
			}
		}
	}
	// The raw JSON carries only hex pubkeys, so an npub search needs the bech32 form
	// — of the author, and of any pubkey the event references via a p tag.
	if npubContains(evt.PubKey, search) {
		return true
	}
	for _, t := range evt.Tags {
		if len(t) >= 2 && t[0] == "p" && npubContains(t[1], search) {
			return true
		}
	}
	return false
}

// npubContains reports whether a hex pubkey's npub form contains the search term.
func npubContains(pubkeyHex, search string) bool {
	if !isSHA256Hex(pubkeyHex) {
		return false // pubkeys are 64 lowercase hex, same shape as a sha256
	}
	npub, err := nip19.EncodePublicKey(pubkeyHex)
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(npub), search)
}
