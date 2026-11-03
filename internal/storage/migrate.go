package storage

import (
	"context"
	"fmt"
)

// MigrationProgress is a snapshot of a migration run.
type MigrationProgress struct {
	Total   int    `json:"total"`   // blobs found in the source
	Copied  int    `json:"copied"`  // newly written to the destination
	Skipped int    `json:"skipped"` // already present in the destination
	Failed  int    `json:"failed"`  // could not be copied (left in the source)
	Done    bool   `json:"done"`    // the run finished (not cancelled mid-way)
	Err     string `json:"err,omitempty"`
}

// Migrate copies every blob from src into dst that dst does not already hold,
// preserving each blob's content type. It is the safe half of re-routing storage:
// after pointing the primary at a new backend (with the old one kept as a Multi
// fallback), run Migrate(old, new) to copy the backlog, then drop the fallback.
//
// Idempotent: a blob dst already has is skipped, so an interrupted run can simply be
// re-run. A per-blob copy failure is counted and does not abort the run (the blob
// stays in src and is retried next time). progress, if non-nil, is called after each
// blob and once more when the run finishes. Honors ctx cancellation between blobs.
func Migrate(ctx context.Context, src, dst Storage, progress func(MigrationProgress)) (MigrationProgress, error) {
	items, err := src.List(ctx)
	if err != nil {
		return MigrationProgress{Err: err.Error()}, fmt.Errorf("list source: %w", err)
	}
	p := MigrationProgress{Total: len(items)}
	emit := func() {
		if progress != nil {
			progress(p)
		}
	}
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			p.Err = err.Error()
			return p, err
		}
		if ok, _ := dst.Has(ctx, it.Hash, it.Ext); ok {
			p.Skipped++
			emit()
			continue
		}
		if err := copyBlob(ctx, src, dst, it); err != nil {
			p.Failed++
			emit()
			continue
		}
		p.Copied++
		emit()
	}
	p.Done = true
	emit()
	return p, nil
}

// copyBlob streams one blob from src to dst, carrying its content type across (so an
// S3/R2 destination stores the right metadata). Size comes from Stat when available,
// falling back to the listing's size.
func copyBlob(ctx context.Context, src, dst Storage, it BlobInfo) error {
	ct := ""
	size := it.Size
	if si, err := src.Stat(ctx, it.Hash, it.Ext); err == nil {
		ct = si.ContentType
		if si.Size > 0 {
			size = si.Size
		}
	}
	rc, err := src.Load(ctx, it.Hash, it.Ext)
	if err != nil {
		return fmt.Errorf("load %s: %w", it.Key, err)
	}
	defer rc.Close()
	if err := dst.Store(ctx, it.Hash, it.Ext, ct, rc, size); err != nil {
		return fmt.Errorf("store %s: %w", it.Key, err)
	}
	return nil
}
