package server

import (
	"os"
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

func TestClassifyUpload_TypeGating(t *testing.T) {
	srv := testServer(t, &fakeStorage{stored: map[string][]byte{}}) // default: zip only
	rar := spool(t, []byte("Rar!\x1a\x07\x00rardata"))

	// rar rejected when only zip is allowed
	if _, _, err := srv.classifyUpload(rar); err == nil {
		t.Fatal("rar must be rejected when allowed_types is [zip]")
	}

	// rar accepted (as .rar) with the wildcard
	srv.allowedUploadTypes = []string{"*"}
	ext, ctype, err := srv.classifyUpload(rar)
	if err != nil {
		t.Fatalf("rar should be accepted with '*': %v", err)
	}
	if ext != "rar" || ctype == "" {
		t.Fatalf("want rar/<ctype>, got %s/%s", ext, ctype)
	}

	// zip still works under the default, encrypted zips still rejected
	srv.allowedUploadTypes = []string{"zip"}
	if ext, _, err := srv.classifyUpload(spool(t, []byte("PK\x03\x04\x00\x00\x00\x00hdr"))); err != nil || ext != "zip" {
		t.Fatalf("plain zip should be accepted: ext=%s err=%v", ext, err)
	}
	if _, _, err := srv.classifyUpload(spool(t, []byte("PK\x03\x04\x00\x00\x01\x00hdr"))); err == nil {
		t.Fatal("encrypted zip (flag bit 0 set) must be rejected")
	}

	// an unrecognised type resolves to "bin" and is rejected unless '*' is set
	if _, _, err := srv.classifyUpload(spool(t, []byte("just some text"))); err == nil {
		t.Fatal("unknown type must be rejected under [zip]")
	}
}
