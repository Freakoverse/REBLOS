package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Freakoverse/REBLOS/internal/config"
	"github.com/nbd-wtf/go-nostr"
)

func deleteAuth(t *testing.T, sk, hash, verb string, exp int64) string {
	t.Helper()
	evt := nostr.Event{
		Kind:      24242,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"t", verb}, {"x", hash}, {"expiration", strconv.FormatInt(exp, 10)}},
		Content:   verb,
	}
	if err := evt.Sign(sk); err != nil {
		t.Fatal(err)
	}
	j, _ := json.Marshal(evt)
	return "Nostr " + base64.StdEncoding.EncodeToString(j)
}

func TestDelete_AdminOnly(t *testing.T) {
	adminSK := nostr.GeneratePrivateKey()
	adminPK, _ := nostr.GetPublicKey(adminSK)

	fs := &fakeStorage{stored: map[string][]byte{}}
	cfg := &config.Config{PublicURL: "https://test.example", DataDir: tempDataDir(t)}
	cfg.Upload.MaxSizeMB = 500
	cfg.Upload.MaxConcurrent = 4
	cfg.Relay.AdminNpub = adminPK // hex pubkey; resolvePubkey passes it through
	srv, err := New(cfg, fs, "gate-secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	hash := strings.Repeat("a", 64)
	future := time.Now().Add(time.Hour).Unix()

	del := func(auth string) int {
		req := httptest.NewRequest(http.MethodDelete, "/"+hash, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w.Code
	}

	if code := del(deleteAuth(t, adminSK, hash, "delete", future)); code != http.StatusOK {
		t.Fatalf("admin delete: want 200, got %d", code)
	}
	// A valid signer who never uploaded this blob is refused (403) — not a hard 401,
	// because the auth itself is valid; they're just not an owner.
	if code := del(deleteAuth(t, nostr.GeneratePrivateKey(), hash, "delete", future)); code != http.StatusForbidden {
		t.Fatalf("non-owner delete: want 403, got %d", code)
	}
	if code := del(deleteAuth(t, adminSK, hash, "upload", future)); code != http.StatusUnauthorized {
		t.Fatalf("wrong verb: want 401, got %d", code)
	}
	if code := del(deleteAuth(t, adminSK, hash, "delete", time.Now().Add(-time.Hour).Unix())); code != http.StatusUnauthorized {
		t.Fatalf("expired: want 401, got %d", code)
	}
	if code := del(deleteAuth(t, adminSK, strings.Repeat("b", 64), "delete", future)); code != http.StatusUnauthorized {
		t.Fatalf("x-mismatch (auth for a different blob): want 401, got %d", code)
	}
	if code := del(""); code != http.StatusUnauthorized {
		t.Fatalf("no auth: want 401, got %d", code)
	}
}

// An uploader's delete retracts their own upload: it drops their pubkey, never
// deletes bytes, and quarantines the blob once the last owner leaves (unclaimed).
func TestDelete_UploaderRetract(t *testing.T) {
	adminSK := nostr.GeneratePrivateKey()
	adminPK, _ := nostr.GetPublicKey(adminSK)
	aSK := nostr.GeneratePrivateKey()
	aPK, _ := nostr.GetPublicKey(aSK)
	bSK := nostr.GeneratePrivateKey()
	bPK, _ := nostr.GetPublicKey(bSK)

	fs := &fakeStorage{stored: map[string][]byte{}}
	cfg := &config.Config{PublicURL: "https://test.example", DataDir: tempDataDir(t)}
	cfg.Upload.MaxSizeMB = 500
	cfg.Upload.MaxConcurrent = 4
	cfg.Relay.AdminNpub = adminPK
	srv, err := New(cfg, fs, "gate-secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	hash := strings.Repeat("c", 64)
	future := time.Now().Add(time.Hour).Unix()
	srv.owners.add(hash, aPK)
	srv.owners.add(hash, bPK) // two uploaders of the same content

	del := func(sk string) int {
		req := httptest.NewRequest(http.MethodDelete, "/"+hash, nil)
		req.Header.Set("Authorization", deleteAuth(t, sk, hash, "delete", future))
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w.Code
	}

	// A retracts: dropped from the set, blob still owned by B → not quarantined.
	if code := del(aSK); code != http.StatusOK {
		t.Fatalf("A retract: want 200, got %d", code)
	}
	if got := srv.owners.list(hash); len(got) != 1 || got[0] != bPK {
		t.Fatalf("after A retract owners = %v, want [B]", got)
	}
	if srv.quarantine.has(hash) {
		t.Fatal("blob quarantined while B still owns it")
	}
	// B retracts: set now empty and no mod references it → quarantined (bytes kept).
	if code := del(bSK); code != http.StatusOK {
		t.Fatalf("B retract: want 200, got %d", code)
	}
	if got := srv.owners.list(hash); got != nil {
		t.Fatalf("after B retract owners = %v, want none", got)
	}
	if !srv.quarantine.has(hash) {
		t.Fatal("blob not quarantined after last owner retracted")
	}
}
