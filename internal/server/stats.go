package server

import (
	"net/http"
	"os"
	"path/filepath"
)

// handleAdminStats reports live node-side runtime stats for the dashboard — the
// disk the node itself runs on (its DataDir volume), NOT the R2/S3 blob store.
// Read-only.
func (s *Server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}

	disk := map[string]any{"available": false}
	if total, free, ok := diskUsageMB(s.dataDir); ok {
		used := total - free
		if used < 0 {
			used = 0
		}
		disk = map[string]any{
			"available": true,
			"total_mb":  total,
			"used_mb":   used,
			"free_mb":   free,
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"disk":        disk,
		"data_dir_mb": dirSizeMB(s.dataDir), // the node's own footprint (events + metadata)
	})
}

// dirSizeMB returns the total size (MB) of everything under dir, best-effort (0 on
// error). This is the node's own footprint — the event store plus the metadata
// files — as opposed to whole-volume usage.
func dirSizeMB(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, e := d.Info(); e == nil {
			total += info.Size()
		}
		return nil
	})
	return total / (1024 * 1024)
}
