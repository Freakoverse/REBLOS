package server

import (
	"context"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestDeletions_RejectResurrectionButAllowNewer(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	ctx := context.Background()

	modAt := func(ts nostr.Timestamp) *nostr.Event {
		return &nostr.Event{Kind: currentModKind, PubKey: pk, CreatedAt: ts, Tags: nostr.Tags{{"d", "my-mod"}}}
	}

	orig := modAt(1000)
	if rej, _ := srv.rejectEvent(ctx, orig); rej {
		t.Fatal("mod should be accepted before any deletion")
	}

	// Author deletes their mod at t=1000 (same instant as the event).
	del := &nostr.Event{Kind: 5, PubKey: pk, CreatedAt: 1000, Tags: nostr.Tags{{"a", moderationKey(orig)}}}
	if ok, msg := srv.onDeletionOutcome(ctx, orig, del); !ok {
		t.Fatalf("author's own deletion should be accepted: %s", msg)
	}

	// The original (and any equal-or-older re-mirror) must now be rejected.
	if rej, _ := srv.rejectEvent(ctx, orig); !rej {
		t.Fatal("re-ingest of the deleted mod must be rejected")
	}
	if rej, _ := srv.rejectEvent(ctx, modAt(999)); !rej {
		t.Fatal("an older revision of the deleted mod must be rejected")
	}

	// A genuinely NEWER re-publish at the same coordinate is allowed.
	if rej, _ := srv.rejectEvent(ctx, modAt(1001)); rej {
		t.Fatal("a newer revision at the same coordinate should be accepted")
	}
}

func TestDeletions_RejectsForeignDeletion(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	author, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	attacker, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	ctx := context.Background()

	mod := &nostr.Event{Kind: currentModKind, PubKey: author, CreatedAt: 1000, Tags: nostr.Tags{{"d", "my-mod"}}}
	del := &nostr.Event{Kind: 5, PubKey: attacker, CreatedAt: 2000, Tags: nostr.Tags{{"a", moderationKey(mod)}}}

	if ok, _ := srv.onDeletionOutcome(ctx, mod, del); ok {
		t.Fatal("a non-author must not be able to delete someone else's mod")
	}
	if rej, _ := srv.rejectEvent(ctx, mod); rej {
		t.Fatal("mod must remain servable after a rejected foreign deletion")
	}
}

func TestDeletions_AcceptsKind5(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	del := &nostr.Event{Kind: 5, PubKey: pk, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"e", "someid"}}}
	if rej, msg := srv.rejectEvent(context.Background(), del); rej {
		t.Fatalf("kind-5 deletion requests must be accepted, got: %s", msg)
	}
}
