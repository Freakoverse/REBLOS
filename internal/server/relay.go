package server

import (
	"context"
	"time"

	"github.com/Freakoverse/REBLOS/internal/hub"
	"github.com/fiatjaf/eventstore/badger"
	"github.com/fiatjaf/khatru"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip13"
	"github.com/nbd-wtf/go-nostr/nip86"
)

const (
	currentModKind    = 31142 // the permanent, current mod kind
	legacyModKind     = 30402 // LEGACY: old mods (temporary exception, see below)
	jamKind           = 31143 // mod jam
	jamBallotKind     = 31243 // mod jam ballot (a vote)
	jamResultKind     = 31343 // mod jam results (paged leaderboard)
	moderationTagKind = 30985 // admin tag overlay applied to someone else's post
)

// LEGACY: legacy mods (kind 30402) are accepted only with a "GameMod" t-tag and a
// created_at before this cutoff, and are PoW-exempt. Mirrors the client's
// isLegacyModEvent(). Expected to be removed in a future version.
var legacyCutoff = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Unix()

func hasTagValue(evt *nostr.Event, name, val string) bool {
	for _, t := range evt.Tags {
		if len(t) >= 2 && t[0] == name && t[1] == val {
			return true
		}
	}
	return false
}

func isLegacyMod(evt *nostr.Event) bool {
	return evt.Kind == legacyModKind &&
		int64(evt.CreatedAt) < legacyCutoff &&
		hasTagValue(evt, "t", "GameMod")
}

func isJamKind(k int) bool { return k == jamKind || k == jamBallotKind || k == jamResultKind }

func acceptedKind(k int) bool {
	return k == currentModKind || k == legacyModKind || isJamKind(k) || k == moderationTagKind
}

// AcceptedModKinds are the relay event kinds this node stores/serves (for the
// discovery announcement): mods (current + legacy), the mod-jam family, and the
// admin's moderation tag overlays.
func AcceptedModKinds() []int {
	return []int{currentModKind, legacyModKind, jamKind, jamBallotKind, jamResultKind, moderationTagKind}
}

// rejectEvent enforces the write policy: mods (+ legacy), the mod-jam family,
// and admin-only moderation tags.
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
	// Fork switch: no kind allowlist at all. Everything above still applies
	// (bans, takedowns, honored deletions) and PoW becomes the only spam floor,
	// so a general-purpose fork should set min_event_pow rather than rely on the
	// scope. Deletions stay exempt — a takedown must never need mining.
	if s.acceptAllKinds {
		if s.minEventPoW > 0 && evt.Kind != 5 && nip13.Difficulty(evt.ID) < s.minEventPoW {
			return true, "pow: insufficient proof-of-work"
		}
		return false, ""
	}
	switch evt.Kind {
	case 5:
		// NIP-09 deletion request. khatru applies its effect before this policy
		// runs (see onDeletionOutcome); accept it so the author gets an OK and the
		// deletion is retained (briefly — pruned by RunDeletionSweep).
		return false, ""
	case currentModKind:
		if tagValue(evt, "d") == "" {
			return true, "invalid: mod event is missing its d tag"
		}
		if s.minEventPoW > 0 && nip13.Difficulty(evt.ID) < s.minEventPoW {
			return true, "pow: insufficient proof-of-work"
		}
		return false, ""
	case jamKind, jamBallotKind, jamResultKind:
		// Mod-jam family: all addressable, so a d tag is required; PoW-gated like mods.
		if tagValue(evt, "d") == "" {
			return true, "invalid: jam event is missing its d tag"
		}
		if s.minEventPoW > 0 && nip13.Difficulty(evt.ID) < s.minEventPoW {
			return true, "pow: insufficient proof-of-work"
		}
		return false, ""
	case moderationTagKind:
		// Tag overlays applied to other people's posts, so only the admin may
		// write them — otherwise anyone could mark anyone's mod NSFW here.
		if s.adminPubkey == "" {
			return true, "blocked: moderation tags require a configured relay admin"
		}
		if evt.PubKey != s.adminPubkey {
			return true, "restricted: only the relay admin may publish moderation tags"
		}
		if tagValue(evt, "d") == "" {
			return true, "invalid: moderation tag is missing its d tag"
		}
		// No PoW: it's already restricted to a single pubkey, so the spam floor
		// PoW exists to raise buys nothing, and a moderator shouldn't have to mine
		// to hide something.
		return false, ""
	case legacyModKind:
		if !isLegacyMod(evt) { // LEGACY: PoW-exempt, but gated by tag + cutoff
			return true, "legacy mods require a GameMod tag and must predate the cutoff"
		}
		return false, ""
	default:
		return true, "blocked: this relay only accepts mod, mod-jam, and moderation-tag events"
	}
}

// rejectFilter keeps the relay scoped (mods, mod-jam family, moderation tags) for reads.
func (s *Server) rejectFilter(ctx context.Context, filter nostr.Filter) (bool, string) {
	if s.acceptAllKinds {
		return false, "" // fork switch: serve whatever was stored
	}
	if len(filter.Kinds) == 0 {
		return false, "" // querying everything → only mod events exist here
	}
	for _, k := range filter.Kinds {
		if acceptedKind(k) {
			return false, ""
		}
	}
	return true, "this relay only serves mod, mod-jam, and moderation-tag events"
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
	// reject an event or change what gets persisted — pure observation.
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
