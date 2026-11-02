package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Freakoverse/REBLOS/internal/hub"
	"github.com/nbd-wtf/go-nostr"
)

func TestMirrorHubBlob(t *testing.T) {
	fs := &fakeStorage{stored: map[string][]byte{}}
	srv := testServer(t, fs)
	ctx := context.Background()

	h := sha256hex("hello")
	srv.mirrorHubBlob(ctx, h, []byte("hello"))
	if got := fs.stored[h+".txt"]; string(got) != "hello" {
		t.Fatalf("blob not mirrored: %q", got)
	}
	// Idempotent: a second call on an already-stored blob is a no-op (no panic/error).
	srv.mirrorHubBlob(ctx, h, []byte("hello"))
}

// Building a poster set from a hub whose blobs live only on a remote Blossom
// server must pull those member-tree blobs into local storage (durability, §6).
func TestPosterSetBuildMirrorsMemberTree(t *testing.T) {
	fs := &fakeStorage{stored: map[string][]byte{}}
	srv := testServer(t, fs)
	ctx := context.Background()

	member := key(t)
	leaf := "leaf:n0:" + member + ":everyone:AA==?iv=BB=="
	leafHash := sha256hex(leaf)
	index := "tree:" + leafHash
	indexHash := sha256hex(index)

	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/") {
		case indexHash:
			_, _ = w.Write([]byte(index))
		case leafHash:
			_, _ = w.Write([]byte(leaf))
		default:
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()

	owner := key(t)
	coord := "36942:" + owner + ":h"
	h, err := hub.ParseHubRef(coord)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.hubs.Add(h); err != nil {
		t.Fatal(err)
	}

	ps, err := srv.posters.Update(ctx, coord, indexHash, 1, []string{remote.URL})
	if err != nil {
		t.Fatal(err)
	}
	if !ps.Has(member) {
		t.Fatal("member missing from poster set")
	}
	if _, ok := fs.stored[indexHash+".txt"]; !ok {
		t.Error("index blob was not mirrored to local storage")
	}
	if _, ok := fs.stored[leafHash+".txt"]; !ok {
		t.Error("leaf-page blob was not mirrored to local storage")
	}
}

func TestStoreEvent_RoutesByKind(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})
	ctx := context.Background()
	sk := nostr.GeneratePrivateKey()

	// Addressable message (36943) -> ReplaceEvent.
	msg := &nostr.Event{Kind: kindMessage, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", "m1"}, {"h", "hub"}}, Content: "hi"}
	if err := msg.Sign(sk); err != nil {
		t.Fatal(err)
	}
	if !srv.storeEvent(ctx, msg) {
		t.Fatal("addressable message was not stored")
	}

	// Regular reaction (7) -> SaveEvent.
	react := &nostr.Event{Kind: kindReaction, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"h", "hub"}}, Content: "+"}
	if err := react.Sign(sk); err != nil {
		t.Fatal(err)
	}
	if !srv.storeEvent(ctx, react) {
		t.Fatal("regular reaction was not stored")
	}
	// A duplicate regular event is not newly stored.
	if srv.storeEvent(ctx, react) {
		t.Error("duplicate reaction should not count as newly stored")
	}

	// Both are queryable from the store.
	ch, err := srv.store.QueryEvents(ctx, nostr.Filter{Kinds: []int{kindMessage, kindReaction}})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for range ch {
		n++
	}
	if n != 2 {
		t.Fatalf("expected 2 stored events, got %d", n)
	}
}

func TestMergeRelays(t *testing.T) {
	got := mergeRelays([]string{"wss://a", "wss://b"}, []string{"wss://b", "wss://c", ""})
	want := []string{"wss://a", "wss://b", "wss://c"}
	if len(got) != len(want) {
		t.Fatalf("mergeRelays = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mergeRelays = %v, want %v", got, want)
		}
	}
}

func TestRelayHintsOf(t *testing.T) {
	ev := &nostr.Event{Tags: nostr.Tags{
		{"r", "wss://a.example", "general"},
		{"r", "wss://b.example"},
		{"o", "https://blossom.example"}, // not a relay hint
		{"d", "x"},
		{"r"}, // malformed, ignored
	}}
	got := relayHintsOf(ev)
	if len(got) != 2 || got[0] != "wss://a.example" || got[1] != "wss://b.example" {
		t.Fatalf("relayHintsOf = %v", got)
	}
}
