package server

import (
	"context"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

// Moderation tags (kind 30985) apply tags to *other people's* posts, so the
// write policy is a single-pubkey allowlist rather than the PoW spam floor the
// open kinds use.
func TestModerationTags_AdminOnly(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	adminPK, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	otherPK, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	ctx := context.Background()

	tagEvent := func(pk, d string) *nostr.Event {
		tags := nostr.Tags{}
		if d != "" {
			tags = append(tags, nostr.Tag{"d", d})
		}
		return &nostr.Event{Kind: moderationTagKind, PubKey: pk, CreatedAt: nostr.Now(), Tags: tags}
	}

	// With no admin configured there is nobody to trust, so the kind is closed.
	if rej, _ := srv.rejectEvent(ctx, tagEvent(adminPK, "31142:abc:my-mod")); !rej {
		t.Fatal("expected rejection while no relay admin is configured")
	}

	srv.adminPubkey = adminPK

	if rej, msg := srv.rejectEvent(ctx, tagEvent(adminPK, "31142:abc:my-mod")); rej {
		t.Fatalf("admin moderation tag rejected: %s", msg)
	}
	if rej, _ := srv.rejectEvent(ctx, tagEvent(otherPK, "31142:abc:my-mod")); !rej {
		t.Fatal("a non-admin must not be able to tag someone else's post")
	}
	if rej, _ := srv.rejectEvent(ctx, tagEvent(adminPK, "")); !rej {
		t.Fatal("expected rejection: addressable event with no d tag")
	}

	// PoW is deliberately not applied here — the pubkey check already does the
	// job, and a moderator shouldn't have to mine to hide something.
	srv.minEventPoW = 30
	if rej, msg := srv.rejectEvent(ctx, tagEvent(adminPK, "31142:abc:my-mod")); rej {
		t.Fatalf("moderation tag should be PoW-exempt, got: %s", msg)
	}
}

// Readers are unrestricted: anyone must be able to fetch the overlays, or the
// tags the admin applied would have no effect in clients.
func TestModerationTags_Readable(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	ctx := context.Background()

	if rej, msg := srv.rejectFilter(ctx, nostr.Filter{Kinds: []int{moderationTagKind}}); rej {
		t.Fatalf("moderation tag reads rejected: %s", msg)
	}
}

// The fork switch: relay.accept_all_kinds turns a mod-scoped node into a
// general-purpose one, without touching the protections that aren't about scope.
func TestAcceptAllKinds(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	sk := nostr.GeneratePrivateKey()
	pk, _ := nostr.GetPublicKey(sk)
	ctx := context.Background()
	// Signed for real: the PoW check reads evt.ID, which is only set by Sign.
	signed := func(kind int) *nostr.Event {
		ev := &nostr.Event{Kind: kind, PubKey: pk, CreatedAt: nostr.Now(), Tags: nostr.Tags{}}
		if err := ev.Sign(sk); err != nil {
			t.Fatal(err)
		}
		return ev
	}
	note := func() *nostr.Event { return signed(1) }

	// Default: a kind-1 note is out of scope.
	if rej, _ := srv.rejectEvent(ctx, note()); !rej {
		t.Fatal("expected a plain note to be rejected by the mod-scoped default")
	}
	if rej, _ := srv.rejectFilter(ctx, nostr.Filter{Kinds: []int{1}}); !rej {
		t.Fatal("expected kind-1 reads to be rejected by default")
	}

	srv.acceptAllKinds = true

	if rej, msg := srv.rejectEvent(ctx, note()); rej {
		t.Fatalf("accept_all_kinds should accept a plain note: %s", msg)
	}
	if rej, msg := srv.rejectFilter(ctx, nostr.Filter{Kinds: []int{1}}); rej {
		t.Fatalf("accept_all_kinds should serve any kind: %s", msg)
	}

	// Opening the scope must not open the door to banned pubkeys.
	if err := srv.block.ban(pk, "spam"); err != nil {
		t.Fatal(err)
	}
	if rej, _ := srv.rejectEvent(ctx, note()); !rej {
		t.Fatal("accept_all_kinds must still reject banned pubkeys")
	}
	if err := srv.block.allow(pk); err != nil {
		t.Fatal(err)
	}

	// PoW becomes the only spam floor, so it has to still bite.
	srv.minEventPoW = 30
	if rej, _ := srv.rejectEvent(ctx, note()); !rej {
		t.Fatal("accept_all_kinds must still enforce min_event_pow")
	}
	// ...but never on a deletion.
	if rej, msg := srv.rejectEvent(ctx, signed(5)); rej {
		t.Fatalf("deletions must stay PoW-exempt: %s", msg)
	}
}
