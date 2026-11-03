package server

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Freakoverse/REBLOS/internal/config"
	"github.com/Freakoverse/REBLOS/internal/storage"
)

// backendDesc is a secret-free description of a storage backend for the dashboard.
type backendDesc struct {
	Kind     string `json:"kind"` // r2 | s3 | disk
	Endpoint string `json:"endpoint,omitempty"`
	Bucket   string `json:"bucket,omitempty"`
	Path     string `json:"path,omitempty"`
}

// describeBackend builds a backendDesc from config, omitting all credentials.
func describeBackend(kind string, r2 config.R2, s3 config.S3, disk config.Disk) backendDesc {
	switch kind {
	case "s3":
		return backendDesc{Kind: "s3", Endpoint: s3.Endpoint, Bucket: s3.Bucket}
	case "disk":
		return backendDesc{Kind: "disk", Path: disk.Path}
	default: // r2
		return backendDesc{Kind: "r2", Endpoint: r2.Endpoint, Bucket: r2.Bucket}
	}
}

// migrationState tracks a single background storage migration (fallback -> primary).
type migrationState struct {
	mu         sync.Mutex
	running    bool
	progress   storage.MigrationProgress
	startedAt  time.Time
	finishedAt time.Time
}

func newMigrationState() *migrationState { return &migrationState{} }

// start marks a migration running; returns false if one is already in progress.
func (m *migrationState) start() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return false
	}
	m.running = true
	m.progress = storage.MigrationProgress{}
	m.startedAt = time.Now()
	m.finishedAt = time.Time{}
	return true
}

func (m *migrationState) update(p storage.MigrationProgress) {
	m.mu.Lock()
	m.progress = p
	m.mu.Unlock()
}

func (m *migrationState) finish(p storage.MigrationProgress, err error) {
	m.mu.Lock()
	if err != nil && p.Err == "" {
		p.Err = err.Error()
	}
	m.progress = p
	m.running = false
	m.finishedAt = time.Now()
	m.mu.Unlock()
}

func (m *migrationState) snapshot() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]any{
		"running":  m.running,
		"progress": m.progress,
	}
	if !m.startedAt.IsZero() {
		out["started_at"] = m.startedAt.Unix()
	}
	if !m.finishedAt.IsZero() {
		out["finished_at"] = m.finishedAt.Unix()
	}
	return out
}

// handleAdminStorageGet reports the storage backend layout (secret-free), the node's
// disk usage, the stored blob count + total size, and the current migration status.
func (s *Server) handleAdminStorageGet(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}

	blobCount, blobBytes := 0, int64(0)
	if list, err := s.listBlobsCached(r.Context()); err == nil {
		blobCount = len(list)
		for _, b := range list {
			blobBytes += b.Size
		}
	}

	disk := map[string]any{"available": false}
	if total, free, ok := diskUsageMB(s.dataDir); ok {
		used := total - free
		if used < 0 {
			used = 0
		}
		disk = map[string]any{"available": true, "total_mb": total, "used_mb": used, "free_mb": free}
	}

	resp := map[string]any{
		"primary":    s.storagePrimary,
		"fallback":   s.storageFallback, // null when none
		"blob_count": blobCount,
		"blob_bytes": blobBytes,
		"disk":       disk,
		"migration":  s.migration.snapshot(),
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAdminStorageMigrate starts a background migration that copies the read-
// fallback backend's blobs into the primary (the backlog step of re-routing storage).
// 400 if no fallback is configured, 409 if a migration is already running. The copy
// is idempotent, so a repeat run is safe.
func (s *Server) handleAdminStorageMigrate(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if s.storageMulti == nil || len(s.storageMulti.Backends()) < 2 {
		httpErr(w, http.StatusBadRequest, "no read-fallback backend configured")
		return
	}
	if !s.migration.start() {
		httpErr(w, http.StatusConflict, "a migration is already running")
		return
	}
	go s.runMigration()
	writeJSON(w, http.StatusAccepted, s.migration.snapshot())
}

// runMigration copies the fallback backend into the primary, updating the tracker as
// it goes. Uses a background context so it outlives the triggering request; the copy
// is idempotent and resumable, so an interrupted run can simply be re-triggered.
func (s *Server) runMigration() {
	primary := s.storageMulti.Primary()
	source := s.storageMulti.Backends()[1] // config allows a single fallback
	slog.Info("storage migration started", "to", s.storagePrimary.Kind)
	p, err := storage.Migrate(context.Background(), source, primary, s.migration.update)
	if err != nil {
		slog.Warn("storage migration failed", "err", err, "copied", p.Copied, "failed", p.Failed)
	} else {
		slog.Info("storage migration finished", "copied", p.Copied, "skipped", p.Skipped, "failed", p.Failed)
	}
	s.invalidateBlobCache() // migrated blobs now live in the primary
	s.migration.finish(p, err)
}
