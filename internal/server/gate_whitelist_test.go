package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Freakoverse/REBLOS/internal/config"
	"github.com/nbd-wtf/go-nostr"
)

// A blob uploaded by a whitelisted npub bypasses the download gate; one from a
// non-whitelisted uploader (or with no owner recorded) still gets challenged.
func TestGate_WhitelistedOwnerBypass(t *testing.T) {
	fs := &fakeStorage{stored: map[string][]byte{}}
	cfg := &config.Config{PublicURL: "https://test.example", DataDir: tempDataDir(t)}
	cfg.Upload.MaxSizeMB = 500
	cfg.Upload.MaxConcurrent = 4
	cfg.Download.PoWDifficulty = 8 // turn the gate on
	srv, err := New(cfg, fs, "gate-secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	get := func(hash string) int {
		req := httptest.NewRequest(http.MethodGet, "/"+hash, nil)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w.Code
	}

	gated := strings.Repeat("a", 64)     // no owner → gated
	exempt := strings.Repeat("b", 64)    // owner is whitelisted → exempt
	wl, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	srv.owners.add(exempt, wl)
	if err := srv.white.add(wl, "trusted"); err != nil {
		t.Fatal(err)
	}

	// Gated blob → 428 challenge (PoW required).
	if code := get(gated); code != http.StatusPreconditionRequired {
		t.Fatalf("gated blob: want 428, got %d", code)
	}
	// Whitelisted-owner blob → gate skipped; it reaches storage (which lacks it) → 404.
	if code := get(exempt); code == http.StatusPreconditionRequired {
		t.Fatalf("whitelisted-owner blob was gated (428); expected the gate to be bypassed")
	}
}

// HEAD is a gate-free existence check, so the client's dedup "do you have this?"
// isn't 428'd (which would force a needless re-upload). GET stays gated.
func TestGate_HeadIsExistenceCheck(t *testing.T) {
	fs := &fakeStorage{stored: map[string][]byte{}}
	cfg := &config.Config{PublicURL: "https://test.example", DataDir: tempDataDir(t)}
	cfg.Upload.MaxSizeMB = 500
	cfg.Upload.MaxConcurrent = 4
	cfg.Download.PoWDifficulty = 8 // gate on
	srv, err := New(cfg, fs, "gate-secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	do := func(method, hash string) int {
		req := httptest.NewRequest(method, "/"+hash, nil)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w.Code
	}

	present := strings.Repeat("a", 64)
	absent := strings.Repeat("b", 64)
	fs.stored[present+".zip"] = []byte("data")

	if code := do(http.MethodHead, present); code != http.StatusOK {
		t.Fatalf("HEAD of a stored blob: want 200 (not gated), got %d", code)
	}
	if code := do(http.MethodHead, absent); code != http.StatusNotFound {
		t.Fatalf("HEAD of a missing blob: want 404, got %d", code)
	}
	if code := do(http.MethodGet, present); code != http.StatusPreconditionRequired {
		t.Fatalf("GET of a gated blob: want 428, got %d", code)
	}
}
