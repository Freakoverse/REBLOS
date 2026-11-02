package server

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/nbd-wtf/go-nostr"
)

// modRef identifies an event that references a stored blob — a mod, a mod-jam, or
// anything else we store that links to a blob on this host.
type modRef struct {
	Coord string `json:"coord"` // "<kind>:<pubkey>:<d>" for addressable events, else the event id
	Title string `json:"title"` // the event's title tag, "" if it has none
}

// blobRefs is an in-memory index: blob hash → the events that reference it. Its
// ONLY purpose is to LABEL blobs in the admin dashboard as claimed vs. unclaimed.
// It never deletes anything, never gates public retrieval, and is never consulted
// on the upload or download paths — it is pure observability.
//
// It is fully derived from the event store, so it is not persisted: it's rebuilt
// from the store at startup (backfillRefs) and kept fresh as events are saved
// (note, called from the relay's OnEventSaved hook). Any drift errs only toward
// over-claiming — e.g. a replaced mod's previous file stays "claimed" — which is
// the safe direction for a store that never auto-removes a blob.
// maxRefsPerBlob bounds how many referencing mods we track per blob. Adding a
// reference costs the publisher proof-of-work (the relay gates mod events), so
// this is really a backstop against a pathological/adversarial case rather than
// an expected limit — a blob legitimately shared by 50+ distinct mods is not a
// thing. Past the cap we stop appending; the blob just stays "claimed".
const maxRefsPerBlob = 50

type blobRefs struct {
	mu   sync.RWMutex
	host string              // host of the node's public URL; only URLs on it count
	m    map[string][]modRef // blob hash (hex) -> referencing events (deduped by coord)
}

func newBlobRefs(publicURL string) *blobRefs {
	host := ""
	if u, err := url.Parse(publicURL); err == nil {
		host = strings.ToLower(u.Host)
	}
	return &blobRefs{host: host, m: map[string][]modRef{}}
}

// refsFor returns a copy of the events that reference a blob (nil if unclaimed).
// A copy so the caller can't observe a later concurrent append tearing the slice.
func (b *blobRefs) refsFor(hash string) []modRef {
	b.mu.RLock()
	defer b.mu.RUnlock()
	src := b.m[hash]
	if len(src) == 0 {
		return nil
	}
	out := make([]modRef, len(src))
	copy(out, src)
	return out
}

// note indexes one event: it finds the blobs on this host that the event links to
// and records the reference. Best-effort and side-effect-free beyond the index, so
// it's safe on the ingest path — it never errors and never rejects.
func (b *blobRefs) note(evt *nostr.Event) {
	if evt == nil || b.host == "" {
		return
	}
	hashes := b.extractHashes(evt)
	if len(hashes) == 0 {
		return
	}
	ref := modRef{Coord: coordOf(evt), Title: tagValue(evt, "title")}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, h := range hashes {
		if len(b.m[h]) >= maxRefsPerBlob {
			continue // already at the cap — it's claimed, that's all we need
		}
		if !hasCoord(b.m[h], ref.Coord) {
			b.m[h] = append(b.m[h], ref)
		}
	}
}

func hasCoord(refs []modRef, coord string) bool {
	for _, r := range refs {
		if r.Coord == coord {
			return true
		}
	}
	return false
}

// extractHashes returns the sha256 hashes of blobs on this host that the event
// links to, scanning its content and every tag value for "<host>/<hash>" URLs
// (the download file, the featured image, screenshots, …). Bare hashes without
// the host prefix are ignored — matching a full URL to our host is what makes a
// hit ours rather than a blob hosted on some other server.
func (b *blobRefs) extractHashes(evt *nostr.Event) []string {
	needle := b.host + "/"
	seen := map[string]struct{}{}
	var out []string

	scan := func(text string) {
		lower := strings.ToLower(text)
		from := 0
		for {
			i := strings.Index(lower[from:], needle)
			if i < 0 {
				return
			}
			start := from + i + len(needle)
			if leadingHexLen(lower[start:]) == 64 {
				h := lower[start : start+64]
				if _, ok := seen[h]; !ok {
					seen[h] = struct{}{}
					out = append(out, h)
				}
			}
			from = start
		}
	}

	scan(evt.Content)
	for _, t := range evt.Tags {
		for _, v := range t {
			scan(v)
		}
	}
	return out
}

// leadingHexLen counts the run of leading lowercase-hex characters in s. A blob
// URL segment is exactly 64 of them (optionally followed by ".<ext>"), so callers
// require == 64: a 63- or 65-long run is not a sha256 and must not match.
func leadingHexLen(s string) int {
	n := 0
	for n < len(s) {
		c := s[n]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			n++
			continue
		}
		break
	}
	return n
}

// coordOf is the addressable coordinate "<kind>:<pubkey>:<d>" for replaceable
// events, or the bare event id when the event has no d tag.
func coordOf(evt *nostr.Event) string {
	if d := tagValue(evt, "d"); d != "" {
		return fmt.Sprintf("%d:%s:%s", evt.Kind, evt.PubKey, d)
	}
	return evt.ID
}

// backfillRefs rebuilds the reference index from every event already in the store.
// It pages backwards by created_at because a single query is capped at the
// backend's MaxLimit (1000), and there can be more mods than that. Run once at
// startup so pre-existing blobs are labelled without waiting for a re-publish.
func (s *Server) backfillRefs(ctx context.Context) {
	const page = 1000 // == badger's default MaxLimit
	seen := map[string]struct{}{}
	until := nostr.Now()
	for {
		f := nostr.Filter{Until: &until, Limit: page}
		ch, err := s.store.QueryEvents(ctx, f)
		if err != nil {
			return
		}
		got := 0
		oldest := until
		for evt := range ch {
			got++
			if _, ok := seen[evt.ID]; ok {
				continue // page boundaries overlap (Until is inclusive); skip repeats
			}
			seen[evt.ID] = struct{}{}
			s.refs.note(evt)
			if evt.CreatedAt < oldest {
				oldest = evt.CreatedAt
			}
		}
		// Short page = reached the end; no progress = a full page shares one second
		// (stop rather than loop forever). Either way we're done.
		if got < page || oldest >= until {
			return
		}
		until = oldest
	}
}
