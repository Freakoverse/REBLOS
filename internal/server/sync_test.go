package server

import (
	"context"
	"io"
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

// mirrorHubBlobs pulls the full structural set (index + leaf pages + spine +
// history + struct) from a remote Blossom into local storage.
func TestMirrorHubBlobs(t *testing.T) {
	fs := &fakeStorage{stored: map[string][]byte{}}
	srv := testServer(t, fs)
	ctx := context.Background()

	member := key(t)
	page := "leaf:n0:" + member + ":everyone:AA==?iv=BB=="
	spine := "page-root:r0:n0:n0:CC==:CC=="
	history := "history-log-contents"
	structBlob := "encrypted-struct-blob-base64"
	pageH, spineH, histH, structH := sha256hex(page), sha256hex(spine), sha256hex(history), sha256hex(structBlob)
	index := strings.Join([]string{
		"meta:page_size=10000",
		"spine:" + spineH,
		"leaf-page:0:" + member + ":" + pageH,
		"history:" + histH,
	}, "\n")
	indexH := sha256hex(index)

	blobs := map[string]string{indexH: index, pageH: page, spineH: spine, histH: history, structH: structBlob}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := blobs[strings.TrimPrefix(r.URL.Path, "/")]; ok {
			_, _ = w.Write([]byte(b))
			return
		}
		http.NotFound(w, r)
	}))
	defer remote.Close()

	srv.mirrorHubBlobs(ctx, &hub.Hub{Pubkey: key(t), DTag: "d0"}, indexH, structH, []string{remote.URL})

	for name, h := range map[string]string{"index": indexH, "leaf": pageH, "spine": spineH, "history": histH, "struct": structH} {
		if _, ok := fs.stored[h+".txt"]; !ok {
			t.Errorf("%s blob (%s) was not mirrored to local storage", name, h)
		}
	}
}

// pngBlob returns a minimal byte slice that classifyBytes detects as image/png.
func pngBlob(payload string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte(payload)...)
}

// storeHubEvent whitelists a hub and stores a signed 36942 for it carrying the
// given Blossom server hint, so hubBlossomUnion / latestHubEvent see it.
func storeHubEvent(t *testing.T, srv *Server, dTag, blossomURL string) {
	t.Helper()
	sk := nostr.GeneratePrivateKey()
	pk, _ := nostr.GetPublicKey(sk)
	coord := "36942:" + pk + ":" + dTag
	h, err := hub.ParseHubRef(coord)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.hubs.Add(h); err != nil {
		t.Fatal(err)
	}
	ev := &nostr.Event{
		Kind:      36942,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"d", dTag}, {"m", "idx", "1"}, {"o", blossomURL}},
	}
	if err := ev.Sign(sk); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.ReplaceEvent(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
}

// A GET for a blob absent locally must be lazily pulled from a whitelisted hub's
// Blossom server, verified, classified, stored, and served (media durability, §6).
func TestMirrorOnRead(t *testing.T) {
	fs := &fakeStorage{stored: map[string][]byte{}}
	srv := testServer(t, fs)
	ctx := context.Background()

	blob := pngBlob("mirror-on-read-png-payload")
	h := sha256hex(string(blob))
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.URL.Path, "/") == h {
			_, _ = w.Write(blob)
			return
		}
		http.NotFound(w, r)
	}))
	defer remote.Close()

	storeHubEvent(t, srv, "mediahub", remote.URL)

	rc, err := srv.mirrorOnRead(ctx, h, "png")
	if err != nil {
		t.Fatalf("mirrorOnRead: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != string(blob) {
		t.Fatalf("served bytes != blob")
	}
	// Stored under the sniffed extension (png), not the member-tree default (txt).
	if _, ok := fs.stored[h+".png"]; !ok {
		t.Errorf("blob not stored as .png: keys=%v", keysOf(fs))
	}
}

// With no whitelisted hubs (hence no Blossom servers), there is nothing to mirror
// from and the miss stays a miss.
func TestMirrorOnRead_NoHubs(t *testing.T) {
	fs := &fakeStorage{stored: map[string][]byte{}}
	srv := testServer(t, fs)
	if _, err := srv.mirrorOnRead(context.Background(), sha256hex(string(pngBlob("x"))), "png"); err == nil {
		t.Fatal("expected an error with no hub Blossom servers")
	}
}

// A remote that serves the wrong bytes for the requested hash must not be trusted:
// the fetcher's sha256 check fails and nothing is stored.
func TestMirrorOnRead_HashMismatch(t *testing.T) {
	fs := &fakeStorage{stored: map[string][]byte{}}
	srv := testServer(t, fs)

	want := sha256hex(string(pngBlob("the-real-one")))
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(pngBlob("a-different-blob")) // wrong bytes for `want`
	}))
	defer remote.Close()
	storeHubEvent(t, srv, "mediahub", remote.URL)

	if _, err := srv.mirrorOnRead(context.Background(), want, "png"); err == nil {
		t.Fatal("expected a hash-mismatch error")
	}
	if len(fs.stored) != 0 {
		t.Errorf("nothing should be stored on mismatch, got %v", keysOf(fs))
	}
}

func keysOf(fs *fakeStorage) []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	var k []string
	for key := range fs.stored {
		k = append(k, key)
	}
	return k
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
