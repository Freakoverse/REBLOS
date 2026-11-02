package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Freakoverse/REBLOS/internal/config"
	"github.com/Freakoverse/REBLOS/internal/storage"
	"github.com/nbd-wtf/go-nostr"
)

func adminServer(t *testing.T, fs storage.Storage) (*Server, string) {
	t.Helper()
	adminSK := nostr.GeneratePrivateKey()
	adminPK, _ := nostr.GetPublicKey(adminSK)
	cfg := &config.Config{PublicURL: "https://test.example", DataDir: tempDataDir(t)}
	cfg.Upload.MaxSizeMB = 500
	cfg.Upload.MaxConcurrent = 4
	cfg.Relay.AdminNpub = adminPK
	srv, err := New(cfg, fs, "gate-secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv, adminSK
}

func nip98(t *testing.T, sk, method, u string, createdAt nostr.Timestamp) string {
	t.Helper()
	evt := nostr.Event{
		Kind:      27235,
		CreatedAt: createdAt,
		Tags:      nostr.Tags{{"u", u}, {"method", method}},
	}
	if err := evt.Sign(sk); err != nil {
		t.Fatal(err)
	}
	j, _ := json.Marshal(evt)
	return "Nostr " + base64.StdEncoding.EncodeToString(j)
}

func TestAdmin_NIP98Auth(t *testing.T) {
	srv, adminSK := adminServer(t, &fakeStorage{stored: map[string][]byte{}})
	const u = "https://test.example/admin/blobs"

	do := func(auth string) int {
		req := httptest.NewRequest(http.MethodGet, "/admin/blobs", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w.Code
	}

	if code := do(nip98(t, adminSK, "GET", u, nostr.Now())); code != http.StatusOK {
		t.Fatalf("valid admin: want 200, got %d", code)
	}
	if code := do(nip98(t, nostr.GeneratePrivateKey(), "GET", u, nostr.Now())); code != http.StatusUnauthorized {
		t.Fatalf("non-admin: want 401, got %d", code)
	}
	if code := do(nip98(t, adminSK, "POST", u, nostr.Now())); code != http.StatusUnauthorized {
		t.Fatalf("method mismatch: want 401, got %d", code)
	}
	if code := do(nip98(t, adminSK, "GET", u, nostr.Now()-3600)); code != http.StatusUnauthorized {
		t.Fatalf("stale timestamp: want 401, got %d", code)
	}
	if code := do(nip98(t, adminSK, "GET", "https://test.example/admin/whitelist", nostr.Now())); code != http.StatusUnauthorized {
		t.Fatalf("url path mismatch: want 401, got %d", code)
	}
	if code := do(""); code != http.StatusUnauthorized {
		t.Fatalf("no auth: want 401, got %d", code)
	}
}

func TestAdmin_BlobsPaginationAndSearch(t *testing.T) {
	stored := map[string][]byte{}
	for _, h := range []string{
		strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64),
		strings.Repeat("d", 64), strings.Repeat("e", 64),
	} {
		stored[h+".zip"] = []byte("x")
	}
	srv, adminSK := adminServer(t, &fakeStorage{stored: stored})

	get := func(query string) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/admin/blobs?"+query, nil)
		req.Header.Set("Authorization", nip98(t, adminSK, "GET", "https://test.example/admin/blobs?"+query, nostr.Now()))
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("blobs %q: want 200, got %d", query, w.Code)
		}
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}

	page1 := get("per=2&page=1")
	if page1["total"].(float64) != 5 || page1["pages"].(float64) != 3 {
		t.Fatalf("pagination totals wrong: %+v", page1)
	}
	if n := len(page1["blobs"].([]any)); n != 2 {
		t.Fatalf("page1 blobs = %d, want 2", n)
	}

	// Search narrows to a single hash.
	found := get("search=" + strings.Repeat("c", 10))
	if found["total"].(float64) != 1 {
		t.Fatalf("search total = %v, want 1", found["total"])
	}
}

func TestAdmin_BlobsFilterAndSort(t *testing.T) {
	stored := map[string][]byte{
		strings.Repeat("a", 64) + ".zip": make([]byte, 3),
		strings.Repeat("b", 64) + ".png": make([]byte, 10),
		strings.Repeat("c", 64) + ".zip": make([]byte, 1),
	}
	srv, adminSK := adminServer(t, &fakeStorage{stored: stored})

	get := func(query string) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/admin/blobs?"+query, nil)
		req.Header.Set("Authorization", nip98(t, adminSK, "GET", "https://test.example/admin/blobs?"+query, nostr.Now()))
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%q: want 200, got %d", query, w.Code)
		}
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}

	// Distinct types advertised, sorted.
	all := get("")
	types := all["types"].([]any)
	if len(types) != 2 || types[0] != "png" || types[1] != "zip" {
		t.Fatalf("types = %v, want [png zip]", types)
	}

	// Filter to zip only.
	if n := get("ext=zip")["total"].(float64); n != 2 {
		t.Fatalf("ext=zip total = %v, want 2", n)
	}

	// Sort by size descending → largest first (the 10-byte png).
	top := get("sort=size&dir=desc")["blobs"].([]any)[0].(map[string]any)
	if top["size"].(float64) != 10 {
		t.Fatalf("size desc top = %v, want 10", top["size"])
	}

	// Combine: zip only, largest first → the 3-byte zip.
	topZip := get("ext=zip&sort=size&dir=desc")["blobs"].([]any)[0].(map[string]any)
	if topZip["size"].(float64) != 3 {
		t.Fatalf("zip size desc top = %v, want 3", topZip["size"])
	}
}

func TestAdmin_Whitelist(t *testing.T) {
	srv, adminSK := adminServer(t, &fakeStorage{stored: map[string][]byte{}})
	targetPK, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	post := func(path, method, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", nip98(t, adminSK, method, "https://test.example"+path, nostr.Now()))
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w.Code
	}

	// Not whitelisted → normal cap.
	if srv.uploadLimitFor(targetPK) != srv.maxUploadBytes {
		t.Fatal("expected normal cap before whitelisting")
	}
	// Add → 5× cap.
	if code := post("/admin/whitelist", http.MethodPost, `{"pubkey":"`+targetPK+`","note":"big uploader"}`); code != http.StatusNoContent {
		t.Fatalf("whitelist add: want 204, got %d", code)
	}
	if srv.uploadLimitFor(targetPK) != srv.maxUploadBytes*5 {
		t.Fatal("expected 5× cap after whitelisting")
	}
	// Remove → back to normal.
	if code := post("/admin/whitelist", http.MethodDelete, `{"pubkey":"`+targetPK+`"}`); code != http.StatusNoContent {
		t.Fatalf("whitelist remove: want 204, got %d", code)
	}
	if srv.uploadLimitFor(targetPK) != srv.maxUploadBytes {
		t.Fatal("expected normal cap after removal")
	}
}
