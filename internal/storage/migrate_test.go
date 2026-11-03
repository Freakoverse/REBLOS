package storage

import (
	"context"
	"testing"
)

func TestMigrate_CopiesAllPreservingContentType(t *testing.T) {
	ctx := context.Background()
	src, dst := newMem(), newMem()
	mustStore(t, src, hashA, "png", "image/png", "alpha")
	mustStore(t, src, hashB, "txt", "text/plain", "beta")

	p, err := Migrate(ctx, src, dst, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 2 || p.Copied != 2 || p.Skipped != 0 || p.Failed != 0 || !p.Done {
		t.Fatalf("progress = %+v", p)
	}
	// Bytes and content type carried across.
	if got, _ := loadStr(t, dst, hashA, "png"); got != "alpha" {
		t.Errorf("hashA not copied: %q", got)
	}
	if si, err := dst.Stat(ctx, hashB, "txt"); err != nil || si.ContentType != "text/plain" {
		t.Errorf("content type not preserved: %+v err %v", si, err)
	}
}

func TestMigrate_IdempotentAndSkips(t *testing.T) {
	ctx := context.Background()
	src, dst := newMem(), newMem()
	mustStore(t, src, hashA, "png", "image/png", "alpha")
	mustStore(t, src, hashB, "txt", "text/plain", "beta")
	// dst already holds one of them.
	mustStore(t, dst, hashA, "png", "image/png", "alpha")

	p, _ := Migrate(ctx, src, dst, nil)
	if p.Copied != 1 || p.Skipped != 1 {
		t.Fatalf("first run: want copied=1 skipped=1, got %+v", p)
	}
	// Re-running copies nothing (everything now present).
	p2, _ := Migrate(ctx, src, dst, nil)
	if p2.Copied != 0 || p2.Skipped != 2 {
		t.Fatalf("second run should skip all: %+v", p2)
	}
}

func TestMigrate_ProgressCallback(t *testing.T) {
	src, dst := newMem(), newMem()
	mustStore(t, src, hashA, "png", "image/png", "a")
	mustStore(t, src, hashB, "txt", "text/plain", "b")

	var calls int
	var last MigrationProgress
	_, err := Migrate(context.Background(), src, dst, func(p MigrationProgress) {
		calls++
		last = p
	})
	if err != nil {
		t.Fatal(err)
	}
	// One call per blob plus the final done-emit.
	if calls != 3 {
		t.Errorf("expected 3 progress calls, got %d", calls)
	}
	if !last.Done || last.Copied != 2 {
		t.Errorf("final progress = %+v", last)
	}
}

func TestMigrate_ListErrorIsReturned(t *testing.T) {
	src := newMem()
	src.failList = true
	if _, err := Migrate(context.Background(), src, newMem(), nil); err == nil {
		t.Fatal("expected an error when the source cannot be listed")
	}
}

// Migrate between real disk backends, the actual re-routing path, exercising
// bare-hash resolution and on-disk round-tripping.
func TestMigrate_DiskToDisk(t *testing.T) {
	ctx := context.Background()
	old, err := NewDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	neu, err := NewDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mustStore(t, old, hashA, "png", "image/png", "disk-alpha")
	mustStore(t, old, hashB, "txt", "text/plain", "disk-beta")

	p, err := Migrate(ctx, old, neu, nil)
	if err != nil || p.Copied != 2 {
		t.Fatalf("migrate: %+v err %v", p, err)
	}
	// Both readable from the new disk, including by bare hash.
	if got, _ := loadStr(t, neu, hashA, ""); got != "disk-alpha" {
		t.Errorf("bare-hash read after migrate: %q", got)
	}
	if got, _ := loadStr(t, neu, hashB, "txt"); got != "disk-beta" {
		t.Errorf("read after migrate: %q", got)
	}
}
