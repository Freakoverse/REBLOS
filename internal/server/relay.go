package server

import (
	"context"

	"github.com/Freakoverse/REBLOS/internal/hub"
	"github.com/fiatjaf/eventstore/badger"
	"github.com/fiatjaf/khatru"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip13"
	"github.com/nbd-wtf/go-nostr/nip86"
)

// NIP-CHAT event kinds (client lib/crypto/constants.ts). rejectEvent gates each
// class; groups (36950) use a different membership model and are out of scope for
// hub gating.
const (
	kindHubEvent        = 36942 // hub definition (addressable; whitelisted-coordinate gated)
	kindMessage         = 36943
	kindMessageEditHint = 26943
	kindTypingIndicator = 26950
	kindJoinRequest     = 36944 // prospective member -> gated by target hub, NOT poster set
	kindPinList         = 36945
	kindVoiceHost       = 36946
	kindVoicePresence   = 36947
	kindReport          = 36948
	kindHideMessage     = 36949
	kindPoll            = 1067
	kindPollVote        = 1017
	kindReaction        = 7
)

// hubContentKinds are the per-hub participant event kinds gated by membership: the
// author must be in the target hub's poster set. They associate with their hub via
// an `h` tag (dTag) or an `a` tag (a 36942 coordinate).
var hubContentKinds = []int{
	kindMessage, kindMessageEditHint, kindTypingIndicator, kindPinList,
	kindVoiceHost, kindVoicePresence, kindReport, kindHideMessage,
	kindPoll, kindPollVote, kindReaction,
}

func hubContentKind(k int) bool {
	for _, x := range hubContentKinds {
		if x == k {
			return true
		}
	}
	return false
}

// nipChatCoreKind is a built-in accepted kind (hub event, member-content, or a
// join request), before per-event gating.
func nipChatCoreKind(k int) bool {
	return k == kindHubEvent || k == kindJoinRequest || hubContentKind(k)
}

// acceptsKind reports whether the relay stores/serves this kind at all: the
// built-in NIP-CHAT kinds plus any configured extra kinds.
func (s *Server) acceptsKind(k int) bool {
	if nipChatCoreKind(k) {
		return true
	}
	for _, e := range s.extraKinds {
		if e == k {
			return true
		}
	}
	return false
}

// AcceptedKinds lists the kinds this relay serves, for the discovery announcement.
func (s *Server) AcceptedKinds() []int {
	ks := []int{
		kindHubEvent, kindJoinRequest, kindMessage, kindMessageEditHint,
		kindTypingIndicator, kindPinList, kindVoiceHost, kindVoicePresence,
		kindReport, kindHideMessage, kindPoll, kindPollVote, kindReaction,
	}
	return append(ks, s.extraKinds...)
}

// rejectEvent enforces the NIP-CHAT write policy. A hub event is accepted only for
// a whitelisted coordinate; a member-content event only when its author is in the
// target hub's poster set; a join request only when it targets a whitelisted hub
// (prospective members are not yet members). Configured extra kinds pass the kind
// allowlist and the PoW floor without member gating.
func (s *Server) rejectEvent(ctx context.Context, evt *nostr.Event) (bool, string) {
	if s.blocked(evt.PubKey) {
		return true, "blocked: pubkey is banned"
	}
	if s.bannedEv.has(moderationKey(evt)) {
		return true, "blocked: this event was removed by a moderator"
	}
	// An author's NIP-09 delete is persistent: reject re-stores (incl. re-ingest)
	// of the deleted coordinate up to the deletion's timestamp. A newer revision at
	// the same coordinate is still accepted (created_at moves past the record).
	if dt := s.deletions.deletedAt(moderationKey(evt)); dt > 0 && int64(evt.CreatedAt) <= dt {
		return true, "blocked: this event was deleted by its author"
	}
	// NIP-09 deletion request. khatru applies its effect before this policy runs
	// (see onDeletionOutcome); accept it so the author gets an OK and the deletion
	// is retained (briefly, pruned by RunDeletionSweep). Always exempt from the
	// kind allowlist and PoW: a takedown must never need mining.
	if evt.Kind == 5 {
		return false, ""
	}
	// Fork switch: no kind allowlist and no member gate. Everything above still
	// applies; PoW becomes the only spam floor, so set min_event_pow with this.
	if s.acceptAllKinds {
		return s.powOK(evt)
	}

	switch {
	case evt.Kind == kindHubEvent:
		dTag := tagValue(evt, "d")
		if dTag == "" {
			return true, "invalid: hub event is missing its d tag"
		}
		if !s.hubs.Has(hubCoordinate(evt.PubKey, dTag)) {
			return true, "blocked: this hub is not whitelisted on this relay"
		}
		return s.powOK(evt)
	case evt.Kind == kindJoinRequest:
		if coord := joinRequestCoordinate(evt); coord == "" || !s.hubs.Has(coord) {
			return true, "blocked: join request does not target a whitelisted hub"
		}
		return s.powOK(evt)
	case hubContentKind(evt.Kind):
		targets := s.eventHubTargets(evt)
		if len(targets) == 0 {
			return true, "blocked: event does not target a whitelisted hub"
		}
		if !s.authorIsMember(targets, evt.PubKey) {
			return true, "blocked: author is not a member of this hub"
		}
		return s.powOK(evt)
	case s.isExtraKind(evt.Kind):
		return s.powOK(evt)
	default:
		return true, "blocked: this relay only accepts this hub's NIP-CHAT events"
	}
}

// powOK applies the min-PoW spam floor (0 = off).
func (s *Server) powOK(evt *nostr.Event) (bool, string) {
	if s.minEventPoW > 0 && nip13.Difficulty(evt.ID) < s.minEventPoW {
		return true, "pow: insufficient proof-of-work"
	}
	return false, ""
}

// eventHubTargets resolves the whitelisted hubs an event is scoped to, via an
// `a` tag (an exact 36942 coordinate) or an `h` tag (a dTag, which may match more
// than one whitelisted hub). Empty when it targets none.
func (s *Server) eventHubTargets(evt *nostr.Event) []*hub.Hub {
	var out []*hub.Hub
	seen := map[string]bool{}
	add := func(h *hub.Hub) {
		if h != nil && !seen[h.Coordinate()] {
			seen[h.Coordinate()] = true
			out = append(out, h)
		}
	}
	for _, t := range evt.Tags {
		if len(t) < 2 {
			continue
		}
		switch t[0] {
		case "a":
			add(s.hubs.Get(t[1])) // non-hub coordinates (e.g. reply refs) resolve to nil
		case "h":
			for _, h := range s.hubs.ByDTag(t[1]) {
				add(h)
			}
		}
	}
	return out
}

// authorIsMember reports whether pubkey is in the poster set of any target hub.
func (s *Server) authorIsMember(targets []*hub.Hub, pubkey string) bool {
	for _, h := range targets {
		if s.posters.Has(h.Coordinate(), pubkey) {
			return true
		}
	}
	return false
}

// joinRequestCoordinate derives the hub coordinate a join request targets from its
// `p` (hub creator) and `d` (hub dTag) tags.
func joinRequestCoordinate(evt *nostr.Event) string {
	p, d := tagValue(evt, "p"), tagValue(evt, "d")
	if p == "" || d == "" {
		return ""
	}
	return hubCoordinate(p, d)
}

func (s *Server) isExtraKind(k int) bool {
	for _, e := range s.extraKinds {
		if e == k {
			return true
		}
	}
	return false
}

// rejectFilter keeps reads scoped to the kinds this relay serves. (Serve-side
// member-author intersection is belt-and-suspenders over the store gate and is a
// later addition; store gating already ensures only member events are stored.)
func (s *Server) rejectFilter(ctx context.Context, filter nostr.Filter) (bool, string) {
	if s.acceptAllKinds {
		return false, "" // fork switch: serve whatever was stored
	}
	if len(filter.Kinds) == 0 {
		return false, "" // querying everything → only accepted kinds exist here
	}
	for _, k := range filter.Kinds {
		if !s.acceptsKind(k) {
			return true, "this relay only serves this hub's NIP-CHAT events"
		}
	}
	return false, ""
}

// setupRelay wires the event store, mod policies, and NIP-86 admin onto the relay.
func (s *Server) setupRelay(store *badger.BadgerBackend, adminPubkey string) {
	r := s.relay
	r.StoreEvent = append(r.StoreEvent, store.SaveEvent)
	r.ReplaceEvent = append(r.ReplaceEvent, store.ReplaceEvent)
	r.QueryEvents = append(r.QueryEvents, store.QueryEvents)
	r.CountEvents = append(r.CountEvents, store.CountEvents)
	r.DeleteEvent = append(r.DeleteEvent, store.DeleteEvent)

	r.RejectEvent = append(r.RejectEvent, s.rejectEvent)
	r.RejectFilter = append(r.RejectFilter, s.rejectFilter)
	// Persist author-initiated NIP-09 deletions so re-ingest can't resurrect them.
	r.OverwriteDeletionOutcome = append(r.OverwriteDeletionOutcome, s.onDeletionOutcome)
	// Label blobs against the mods that reference them (dashboard only). This runs
	// after the event is stored and its return value is ignored, so it can never
	// reject an event or change what gets persisted: pure observation.
	r.OnEventSaved = append(r.OnEventSaved, func(ctx context.Context, evt *nostr.Event) {
		s.refs.note(evt)
		// A new hub revision republished here refreshes that hub's poster set live.
		if evt.Kind == hub.Kind {
			s.reconcileHubEvent(ctx, evt)
		}
	})

	if adminPubkey == "" {
		return // NIP-86 disabled when no admin configured
	}
	m := &r.ManagementAPI
	m.RejectAPICall = append(m.RejectAPICall, func(ctx context.Context, mp nip86.MethodParams) (bool, string) {
		if khatru.GetAuthed(ctx) != adminPubkey {
			return true, "restricted to the relay admin"
		}
		return false, ""
	})
	m.BanPubKey = func(ctx context.Context, pubkey, reason string) error { return s.block.ban(pubkey, reason) }
	m.AllowPubKey = func(ctx context.Context, pubkey, reason string) error { return s.block.allow(pubkey) }
	m.ListBannedPubKeys = func(ctx context.Context) ([]nip86.PubKeyReason, error) { return s.block.list(), nil }
	// Event takedowns are handled by the admin API (persistent, address-based, with
	// auto-reject on re-publish), not one-shot NIP-86 banevent.
}
