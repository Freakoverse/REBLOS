package server

import (
	"os"
	"strings"
	"testing"
)

func spool(t *testing.T, b []byte) *os.File {
	t.Helper()
	f, err := os.CreateTemp("", "upload-type-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close(); os.Remove(f.Name()) })
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestClassifyUpload_TextAndMedia(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}}) // default allowlist

	// A hub member-tree blob is text/plain with no magic — it MUST be accepted,
	// otherwise hubs cannot store their member tree (the blossom.band failure).
	tree := "tree:" + strings.Repeat("a", 64) + "\nleaf:n0:" + strings.Repeat("b", 64) + ":everyone:AA==?iv=BB=="
	ext, ctype, err := srv.classifyUpload(spool(t, []byte(tree)))
	if err != nil || ext != "txt" || ctype != "text/plain" {
		t.Fatalf("member-tree text blob: ext=%s ctype=%s err=%v", ext, ctype, err)
	}

	// A PNG is detected by magic and accepted under the default allowlist.
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	if ext, ctype, err := srv.classifyUpload(spool(t, png)); err != nil || ext != "png" || ctype != "image/png" {
		t.Fatalf("png: ext=%s ctype=%s err=%v", ext, ctype, err)
	}

	// RIFF/WEBP uses an offset-8 magic.
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 16)...)
	if ext, _, err := srv.classifyUpload(spool(t, webp)); err != nil || ext != "webp" {
		t.Fatalf("webp: ext=%s err=%v", ext, err)
	}

	// Opaque binary (NUL bytes, no magic) resolves to "bin" and is rejected by the
	// default allowlist (which has no "bin").
	if _, _, err := srv.classifyUpload(spool(t, []byte{0x00, 0x01, 0x02, 0x00, 0xff})); err == nil {
		t.Fatal("opaque binary must be rejected under the default allowlist")
	}
}

func TestClassifyUpload_TypeGating(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}})

	// Restrict to images only: a text blob is now rejected even though it sniffs
	// as txt (txt is not in the allowlist).
	srv.allowedUploadTypes = []string{"png", "jpg"}
	if _, _, err := srv.classifyUpload(spool(t, []byte("plain text here"))); err == nil {
		t.Fatal("text must be rejected when the allowlist excludes txt")
	}

	// rar is rejected by default, accepted with the wildcard.
	rar := []byte("Rar!\x1a\x07\x00rardata")
	srv.allowedUploadTypes = []string{"png"}
	if _, _, err := srv.classifyUpload(spool(t, rar)); err == nil {
		t.Fatal("rar must be rejected when not in the allowlist")
	}
	srv.allowedUploadTypes = []string{"*"}
	if ext, ctype, err := srv.classifyUpload(spool(t, rar)); err != nil || ext != "rar" || ctype == "" {
		t.Fatalf("rar under '*': ext=%s ctype=%s err=%v", ext, ctype, err)
	}

	// Plain zip accepted when allowed; encrypted zip (flag bit 0) always rejected.
	srv.allowedUploadTypes = []string{"zip"}
	if ext, _, err := srv.classifyUpload(spool(t, []byte("PK\x03\x04\x00\x00\x00\x00hdr"))); err != nil || ext != "zip" {
		t.Fatalf("plain zip: ext=%s err=%v", ext, err)
	}
	if _, _, err := srv.classifyUpload(spool(t, []byte("PK\x03\x04\x00\x00\x01\x00hdr"))); err == nil {
		t.Fatal("encrypted zip (flag bit 0 set) must be rejected")
	}
}
