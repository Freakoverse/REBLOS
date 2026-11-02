package storage

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestDisk_RoundTrip(t *testing.T) {
	d, err := NewDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// 64-hex sha (content is irrelevant to the store; it addresses by name).
	sha := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	data := []byte("deg mods blob payload")

	if err := d.Store(ctx, sha, "zip", "application/zip", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("store: %v", err)
	}

	// Has / Stat with explicit ext.
	if ok, err := d.Has(ctx, sha, "zip"); err != nil || !ok {
		t.Fatalf("has(zip): ok=%v err=%v", ok, err)
	}
	si, err := d.Stat(ctx, sha, "zip")
	if err != nil || si.Size != int64(len(data)) || si.ContentType != "application/zip" {
		t.Fatalf("stat: %+v err=%v", si, err)
	}

	// Load with explicit ext.
	rc, err := d.Load(ctx, sha, "zip")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, data) {
		t.Fatalf("load bytes mismatch: %q", got)
	}

	// Bare-hash resolution (ext empty) must find the sharded object.
	if ok, err := d.Has(ctx, sha, ""); err != nil || !ok {
		t.Fatalf("has(bare): ok=%v err=%v", ok, err)
	}
	rc2, err := d.Load(ctx, sha, "")
	if err != nil {
		t.Fatalf("load(bare): %v", err)
	}
	got2, _ := io.ReadAll(rc2)
	rc2.Close()
	if !bytes.Equal(got2, data) {
		t.Fatalf("bare-hash load mismatch")
	}

	// Delete, then everything reports absent.
	if err := d.Delete(ctx, sha, "zip"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if ok, _ := d.Has(ctx, sha, "zip"); ok {
		t.Fatal("blob still present after delete")
	}
	if _, err := d.Load(ctx, sha, "zip"); err != ErrNotFound {
		t.Fatalf("load after delete: want ErrNotFound, got %v", err)
	}
	if _, err := d.Stat(ctx, sha, ""); err != ErrNotFound {
		t.Fatalf("stat after delete: want ErrNotFound, got %v", err)
	}
	// Delete of an absent blob is not an error.
	if err := d.Delete(ctx, sha, "zip"); err != nil {
		t.Fatalf("delete(absent): %v", err)
	}
}

func TestDisk_MissingIsNotFound(t *testing.T) {
	d, err := NewDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sha := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if ok, err := d.Has(ctx, sha, "zip"); err != nil || ok {
		t.Fatalf("has(missing): ok=%v err=%v", ok, err)
	}
	if _, err := d.Load(ctx, sha, ""); err != ErrNotFound {
		t.Fatalf("load(missing bare): want ErrNotFound, got %v", err)
	}
}
