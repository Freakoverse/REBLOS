package server

import (
	"fmt"
	"net/http"
	"time"
)

// handleAdminConfig returns a read-only snapshot of the node's live configuration
// for the dashboard's Relay → Settings view. It exposes policy and limits only —
// never secrets (keys, bucket credentials) — and changes nothing.
func (s *Server) handleAdminConfig(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}

	kinds := make([]map[string]any, 0)
	for _, k := range AcceptedModKinds() {
		kinds = append(kinds, map[string]any{"kind": k, "label": kindLabel(k)})
	}
	normalMB, whitelistMB := s.caps.snapshotMB()

	writeJSON(w, http.StatusOK, map[string]any{
		"relay": map[string]any{
			"accept_all_kinds": s.acceptAllKinds,
			"accepted_kinds":   kinds,
			"min_event_pow":    s.minEventPoW,
			"legacy_cutoff":    int64(legacyCutoff),
			"admin_configured": s.adminPubkey != "",
		},
		"download_gate": map[string]any{
			"pow_difficulty":    s.powDifficulty,
			"challenge_ttl_sec": int(s.challengeTTL / time.Second),
			"trusted_ip_header": s.trustedIPHeader,
		},
		"upload": map[string]any{
			"max_concurrent":       s.maxConcurrent,
			"min_pow":              s.minUploadPoW,
			"min_upload_rate_kbps": s.minUploadRateBps / 1024,
			"idle_timeout_sec":     int(s.uploadIdleTimeout / time.Second),
			"min_free_disk_mb":     s.minFreeDiskMB,
			"allowed_types":        s.allowedUploadTypes,
			"size_cap_mb":          normalMB,
			"whitelisted_cap_mb":   whitelistMB,
		},
	})
}

// kindLabel maps an accepted event kind to a human label (mirrors the dashboard).
func kindLabel(k int) string {
	switch k {
	case currentModKind:
		return "Mod"
	case legacyModKind:
		return "Legacy mod"
	case jamKind:
		return "Mod jam"
	case jamBallotKind:
		return "Jam ballot"
	case jamResultKind:
		return "Jam result"
	case moderationTagKind:
		return "Moderation tag"
	default:
		return fmt.Sprintf("kind %d", k)
	}
}
