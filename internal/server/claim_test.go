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

func uploadAuth(t *testing.T, sk, hash string, exp int64) string {
	t.Helper()
	evt := nostr.Event{
		Kind:      24242,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"t", "upload"}, {"x", hash}, {"expiration", strconv.FormatInt(exp, 10)}},
		Content:   "upload",
	}
	if err := evt.Sign(sk); err != nil {
		t.Fatal(err)
	}
	j, _ := json.Marshal(evt)
	return "Nostr " + base64.StdEncoding.EncodeToString(j)
}

func TestClaim(t *testing.T) {
	fs := &fakeStorage{stored: map[string][]byte{}}
	cfg := &config.Config{PublicURL: "https://test.example", DataDir: tempDataDir(t)}
	cfg.Upload.MaxSizeMB = 500
	cfg.Upload.MaxConcurrent = 4
	srv, err := New(cfg, fs, "gate-secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	hash := strings.Repeat("a", 64)
	future := time.Now().Add(time.Hour).Unix()
	sk := nostr.GeneratePrivateKey()
	pk, _ := nostr.GetPublicKey(sk)

	claim := func(h, auth string) int {
		req := httptest.NewRequest(http.MethodPut, "/claim/"+h, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w.Code
	}

	// Blob not stored yet → 404, nobody recorded.
	if code := claim(hash, uploadAuth(t, sk, hash, future)); code != http.StatusNotFound {
		t.Fatalf("claim of absent blob: want 404, got %d", code)
	}
	if len(srv.owners.list(hash)) != 0 {
		t.Fatal("owner recorded for an absent blob")
	}

	// Store it, then a valid claim records the uploader.
	fs.stored[hash+".zip"] = []byte("data")
	if code := claim(hash, uploadAuth(t, sk, hash, future)); code != http.StatusNoContent {
		t.Fatalf("valid claim: want 204, got %d", code)
	}
	if got := srv.owners.list(hash); len(got) != 1 || got[0] != pk {
		t.Fatalf("owners after claim = %v, want [pk]", got)
	}

	// No auth → 401; auth for a different blob → 400.
	if code := claim(hash, ""); code != http.StatusUnauthorized {
		t.Fatalf("no auth: want 401, got %d", code)
	}
	if code := claim(hash, uploadAuth(t, sk, strings.Repeat("b", 64), future)); code != http.StatusBadRequest {
		t.Fatalf("mismatched auth: want 400, got %d", code)
	}
}
