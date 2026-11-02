package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"github.com/Freakoverse/REBLOS/internal/hub"
	"github.com/nbd-wtf/go-nostr"
)

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// seedHub stores a monolithic member tree (one leaf per member) in fs, whitelists
// the hub, and builds its poster set. Returns the hub coordinate.
func seedHub(t *testing.T, srv *Server, fs *fakeStorage, ownerPK, dTag string, members ...string) string {
	t.Helper()
	var lines []string
	for i, pk := range members {
		lines = append(lines, "leaf:n"+strconv.Itoa(i)+":"+pk+":everyone:AA==?iv=BB==")
	}
	tree := strings.Join(lines, "\n")
	treeHash := sha256hex(tree)
	index := "tree:" + treeHash
	indexHash := sha256hex(index)

	fs.mu.Lock()
	fs.stored[treeHash] = []byte(tree)
	fs.stored[indexHash] = []byte(index)
	fs.mu.Unlock()

	h, err := hub.ParseHubRef("36942:" + ownerPK + ":" + dTag)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.hubs.Add(h); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.posters.Update(context.Background(), h.Coordinate(), indexHash, 1, nil); err != nil {
		t.Fatal(err)
	}
	return h.Coordinate()
}

func key(t *testing.T) string {
	t.Helper()
	pk, err := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	return pk
}

func TestRejectEvent_HubGating(t *testing.T) {
	fs := &fakeStorage{stored: map[string][]byte{}}
	srv := testServer(t, fs)
	srv.extraKinds = []int{4} // allow a non-hub kind to exercise the extra-kinds branch
	ctx := context.Background()

	owner := key(t)
	member := key(t)
	stranger := key(t)
	seedHub(t, srv, fs, owner, "my-hub", owner, member)

	ev := func(kind int, pubkey string, tags nostr.Tags) *nostr.Event {
		return &nostr.Event{Kind: kind, PubKey: pubkey, CreatedAt: nostr.Now(), Tags: tags}
	}
	coord := "36942:" + owner + ":my-hub"

	cases := []struct {
		name   string
		evt    *nostr.Event
		reject bool
	}{
		{"member message (#h)", ev(kindMessage, member, nostr.Tags{{"h", "my-hub"}, {"c", "general"}}), false},
		{"owner message (#h)", ev(kindMessage, owner, nostr.Tags{{"h", "my-hub"}}), false},
		{"stranger message (#h)", ev(kindMessage, stranger, nostr.Tags{{"h", "my-hub"}}), true},
		{"member message to unknown hub", ev(kindMessage, member, nostr.Tags{{"h", "other-hub"}}), true},
		{"member reaction (#h)", ev(kindReaction, member, nostr.Tags{{"h", "my-hub"}}), false},
		{"member report (a-coord)", ev(kindReport, member, nostr.Tags{{"d", "r1"}, {"a", coord}}), false},
		{"stranger report (a-coord)", ev(kindReport, stranger, nostr.Tags{{"d", "r1"}, {"a", coord}}), true},
		{"hub event, whitelisted", ev(kindHubEvent, owner, nostr.Tags{{"d", "my-hub"}}), false},
		{"hub event, not whitelisted", ev(kindHubEvent, owner, nostr.Tags{{"d", "nope"}}), true},
		{"hub event missing d", ev(kindHubEvent, owner, nostr.Tags{{"n", "x"}}), true},
		{"join request -> whitelisted hub", ev(kindJoinRequest, stranger, nostr.Tags{{"d", "my-hub"}, {"p", owner}}), false},
		{"join request -> unknown hub", ev(kindJoinRequest, stranger, nostr.Tags{{"d", "nope"}, {"p", owner}}), true},
		{"extra kind (4) passes, no member gate", ev(4, stranger, nil), false},
		{"disallowed kind", ev(1, member, nostr.Tags{{"h", "my-hub"}}), true},
		{"deletion always accepted", ev(5, stranger, nil), false},
	}
	for _, c := range cases {
		if rej, msg := srv.rejectEvent(ctx, c.evt); rej != c.reject {
			t.Errorf("%s: reject=%v want %v (msg=%q)", c.name, rej, c.reject, msg)
		}
	}

	// A blocked pubkey is rejected even as a valid member.
	srv.block.ban(member, "test")
	if rej, _ := srv.rejectEvent(ctx, ev(kindMessage, member, nostr.Tags{{"h", "my-hub"}})); !rej {
		t.Error("blocked member should be rejected")
	}
}

func TestRejectEvent_AcceptAllKinds(t *testing.T) {
	fs := &fakeStorage{stored: map[string][]byte{}}
	srv := testServer(t, fs)
	srv.acceptAllKinds = true
	ctx := context.Background()

	// With the fork switch on, an arbitrary kind from a non-member is accepted.
	e := &nostr.Event{Kind: 1, PubKey: key(t), CreatedAt: nostr.Now()}
	if rej, msg := srv.rejectEvent(ctx, e); rej {
		t.Errorf("accept_all_kinds should accept any kind (msg=%q)", msg)
	}
}

func TestRejectFilter(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	srv.extraKinds = []int{4}
	ctx := context.Background()

	allow := func(kinds ...int) bool {
		rej, _ := srv.rejectFilter(ctx, nostr.Filter{Kinds: kinds})
		return !rej
	}
	if !allow(kindMessage) {
		t.Error("message-kind filter should be allowed")
	}
	if !allow(kindMessage, kindReaction, kindPoll) {
		t.Error("multiple NIP-CHAT kinds should be allowed")
	}
	if !allow(4) {
		t.Error("extra kind should be allowed")
	}
	if allow(1) {
		t.Error("disallowed kind should be rejected")
	}
	if allow(kindMessage, 1) {
		t.Error("a filter mixing an allowed and a disallowed kind should be rejected")
	}
	if rej, _ := srv.rejectFilter(ctx, nostr.Filter{}); rej {
		t.Error("empty filter should be allowed")
	}
}
