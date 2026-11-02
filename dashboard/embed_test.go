package dashboard

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The embed must actually carry the built app, or /dashboard 404s at runtime.
func TestEmbedHasBuiltApp(t *testing.T) {
	sub, err := fs.Sub(Assets, "dist")
	if err != nil {
		t.Fatalf("fs.Sub(dist): %v", err)
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		t.Fatalf("index.html missing — did you run `npm run build`? %v", err)
	}
	entries, err := fs.ReadDir(sub, "assets")
	if err != nil || len(entries) == 0 {
		t.Fatalf("assets/ missing or empty: %v", err)
	}
}

// The same StripPrefix + FileServerFS the node registers should hand back the
// SPA shell at /dashboard/ and the hashed assets under /dashboard/assets/.
func TestServesDashboard(t *testing.T) {
	sub, _ := fs.Sub(Assets, "dist")
	h := http.StripPrefix("/dashboard/", http.FileServerFS(sub))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/dashboard/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /dashboard/ = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `id="root"`) {
		t.Fatalf("index.html did not render the app root")
	}

	// An asset path resolves too (find one from the manifest listing).
	sub2, _ := fs.Sub(sub, "assets")
	names, _ := fs.ReadDir(sub2, ".")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/dashboard/assets/"+names[0].Name(), nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET /dashboard/assets/%s = %d, want 200", names[0].Name(), rec2.Code)
	}
}
