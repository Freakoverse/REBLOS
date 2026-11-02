package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

type readSeekCloser struct{ *bytes.Reader }

func (readSeekCloser) Close() error { return nil }

type fakeLocal struct{ blobs map[string][]byte }

func (f fakeLocal) Load(_ context.Context, hash, _ string) (io.ReadSeekCloser, error) {
	b, ok := f.blobs[hash]
	if !ok {
		return nil, errors.New("not found")
	}
	return readSeekCloser{bytes.NewReader(b)}, nil
}

func TestFetcherLocalHit(t *testing.T) {
	content := "tree:abc\nleaf:n0:" + pkA + ":everyone:XX==?iv=YY=="
	h := hashOf(content)
	f := &Fetcher{Local: fakeLocal{blobs: map[string][]byte{h: []byte(content)}}}

	got, err := f.Fetch(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("got %q", got)
	}
}

func TestFetcherLocalHashMismatch(t *testing.T) {
	// Store bytes under a hash that is NOT their real sha256.
	wrongHash := hashOf("something else")
	f := &Fetcher{Local: fakeLocal{blobs: map[string][]byte{wrongHash: []byte("tampered")}}}

	if _, err := f.Fetch(context.Background(), wrongHash); err == nil {
		t.Fatal("expected hash-mismatch error on a tampered local blob")
	}
}

func TestFetcherRemoteFallbackAndMirror(t *testing.T) {
	content := "meta:page_size=10000\nspine:deadbeef"
	h := hashOf(content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/"+h {
			_, _ = w.Write([]byte(content))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	mirrored := map[string][]byte{}
	f := &Fetcher{
		Local:   fakeLocal{blobs: map[string][]byte{}}, // empty -> miss -> remote
		Remotes: []string{srv.URL},
		Mirror:  func(_ context.Context, hash string, b []byte) { mirrored[hash] = b },
	}

	got, err := f.Fetch(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if got != content {
		t.Errorf("got %q", got)
	}
	if string(mirrored[h]) != content {
		t.Errorf("mirror not called with the fetched bytes")
	}
}

func TestFetcherRemoteWrongBytesThenGood(t *testing.T) {
	content := "the real blob"
	h := hashOf(content)

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("WRONG bytes, wrong hash"))
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(content))
	}))
	defer good.Close()

	f := &Fetcher{Remotes: []string{bad.URL, good.URL}} // bad first; must skip to good
	got, err := f.Fetch(context.Background(), h)
	if err != nil {
		t.Fatalf("expected the good remote to satisfy the fetch: %v", err)
	}
	if got != content {
		t.Errorf("got %q", got)
	}
}

func TestFetcherAllRemotesWrong(t *testing.T) {
	h := hashOf("real")
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("nope"))
	}))
	defer bad.Close()

	f := &Fetcher{Remotes: []string{bad.URL}}
	if _, err := f.Fetch(context.Background(), h); err == nil {
		t.Fatal("expected error when no remote serves the correct bytes")
	}
}

func TestFetcherOverSize(t *testing.T) {
	content := strings.Repeat("x", 100)
	h := hashOf(content)
	f := &Fetcher{
		Local:    fakeLocal{blobs: map[string][]byte{h: []byte(content)}},
		MaxBytes: 10, // smaller than the blob
	}
	if _, err := f.Fetch(context.Background(), h); err == nil {
		t.Fatal("expected over-size rejection")
	}
}

func TestFetcherInvalidHashArg(t *testing.T) {
	f := &Fetcher{}
	if _, err := f.Fetch(context.Background(), "not-a-hash"); err == nil {
		t.Fatal("expected invalid-hash error")
	}
}

func TestFetcherBind(t *testing.T) {
	content := "leaf:n0:" + pkA + ":everyone:AA==?iv=BB=="
	h := hashOf(content)
	f := &Fetcher{Local: fakeLocal{blobs: map[string][]byte{h: []byte(content)}}}

	bf := f.Bind(context.Background())
	got, err := bf(h)
	if err != nil || got != content {
		t.Fatalf("bound fetcher: got %q err %v", got, err)
	}
}
