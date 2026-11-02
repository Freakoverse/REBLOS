package server

import (
	"context"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestBannedEvents_RejectsRepublishUntilUnban(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	modEvent := func(title string) *nostr.Event {
		tags := nostr.Tags{{"d", "my-mod"}}
		if title != "" {
			tags = append(tags, nostr.Tag{"title", title})
		}
		return &nostr.Event{Kind: currentModKind, PubKey: pk, CreatedAt: nostr.Now(), Tags: tags}
	}
	ctx := context.Background()
	key := moderationKey(modEvent("")) // "31142:<pk>:my-mod"

	if rej, _ := srv.rejectEvent(ctx, modEvent("")); rej {
		t.Fatal("accepted before ban expected")
	}

	if err := srv.bannedEv.ban(key, "takedown"); err != nil {
		t.Fatal(err)
	}
	if rej, _ := srv.rejectEvent(ctx, modEvent("")); !rej {
		t.Fatal("expected rejection after ban")
	}
	// A re-publish is a different event id but the same address → still rejected.
	if rej, _ := srv.rejectEvent(ctx, modEvent("new title, same mod")); !rej {
		t.Fatal("re-publish of a banned address must stay rejected")
	}

	if err := srv.bannedEv.unban(key); err != nil {
		t.Fatal(err)
	}
	if rej, _ := srv.rejectEvent(ctx, modEvent("")); rej {
		t.Fatal("expected acceptance after unban")
	}
}
