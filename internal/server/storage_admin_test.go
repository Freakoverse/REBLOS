package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Freakoverse/REBLOS/internal/storage"
	"github.com/nbd-wtf/go-nostr"
)

// adminGET/adminPOST issue a NIP-98 admin request against the handler.
func adminReq(t *testing.T, srv *Server, adminSK, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	const base = "https://test.example"
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("Authorization", nip98(t, adminSK, method, base+path, nostr.Now()))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	return w
}

func TestAdminStorage_StatusNoFallback(t *testing.T) {
	srv, adminSK := adminServer(t, &fakeStorage{stored: map[string][]byte{}})

	w := adminReq(t, srv, adminSK, "GET", "/admin/storage")
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	var resp struct {
		Primary   backendDesc    `json:"primary"`
		Fallback  *backendDesc   `json:"fallback"`
		BlobCount int            `json:"blob_count"`
		Migration map[string]any `json:"migration"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Primary.Kind == "" {
		t.Error("primary backend kind should be reported")
	}
	if resp.Fallback != nil {
		t.Errorf("no fallback configured, want null, got %+v", resp.Fallback)
	}
	if run, _ := resp.Migration["running"].(bool); run {
		t.Error("no migration should be running")
	}
}

func TestAdminStorage_MigrateRejectedWithoutFallback(t *testing.T) {
	srv, adminSK := adminServer(t, &fakeStorage{stored: map[string][]byte{}})
	w := adminReq(t, srv, adminSK, "POST", "/admin/storage/migrate")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("migrate without fallback: want 400, got %d", w.Code)
	}
}

func TestAdminStorage_MigrateRequiresAdmin(t *testing.T) {
	srv, _ := adminServer(t, &fakeStorage{stored: map[string][]byte{}})
	const base = "https://test.example"
	r := httptest.NewRequest("POST", "/admin/storage/migrate", nil)
	r.Header.Set("Authorization", nip98(t, nostr.GeneratePrivateKey(), "POST", base+"/admin/storage/migrate", nostr.Now()))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("non-admin migrate: want 401, got %d", w.Code)
	}
}

// With a read-fallback configured, a migration copies the fallback's blobs into the
// primary. Driven through runMigration for determinism (the HTTP route starts it in a
// goroutine).
func TestAdminStorage_MigrationCopiesFallbackIntoPrimary(t *testing.T) {
	primary := &fakeStorage{stored: map[string][]byte{}}
	fallback := &fakeStorage{stored: map[string][]byte{}}
	h := sha256hex("old-media-payload")
	fallback.stored[h+".png"] = []byte("old-media-payload")

	srv, _ := adminServer(t, storage.NewMulti(primary, fallback))
	if srv.storageMulti == nil {
		t.Fatal("server did not detect the Multi storage")
	}

	if !srv.migration.start() {
		t.Fatal("migration should start")
	}
	srv.runMigration()

	if _, ok := primary.stored[h+".png"]; !ok {
		t.Errorf("blob was not copied into the primary: %v", primary.stored)
	}
	snap := srv.migration.snapshot()
	if run, _ := snap["running"].(bool); run {
		t.Error("migration should be finished")
	}
	prog, ok := snap["progress"].(storage.MigrationProgress)
	if !ok {
		t.Fatalf("progress type = %T", snap["progress"])
	}
	if !prog.Done || prog.Copied != 1 {
		t.Errorf("progress = %+v", prog)
	}
}

// A second migration while one is running is refused by the tracker.
func TestMigrationState_SingleFlight(t *testing.T) {
	m := newMigrationState()
	if !m.start() {
		t.Fatal("first start should succeed")
	}
	if m.start() {
		t.Fatal("second start should be refused while running")
	}
	m.finish(storage.MigrationProgress{Done: true}, nil)
	if !m.start() {
		t.Fatal("start should succeed again after finish")
	}
}
