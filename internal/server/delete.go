package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// A Blossom delete means different things by who signs it. The ADMIN hard-deletes
// (content moderation) — the byte-removal path. Any other valid signer is treated
// as an uploader RETRACTING their own upload: their pubkey is dropped from the
// blob's owner set, and only when the last owner leaves AND no stored mod
// references the blob is it quarantined (download withheld, bytes kept, reversible).
// A non-admin who never uploaded the blob is refused, and — crucially — a
// non-admin request can NEVER delete bytes; the worst it can do is a reversible
// mark. (Because our streaming /upload bypasses khatru's blossom index, khatru's
// own DELETE can't authorize here; we validate the kind-24242 auth ourselves.)

// validateDeleteAuth checks a kind-24242 "delete" authorization for the blob:
// correct kind/verb, unexpired, matching x tag, valid signature. It does NOT check
// identity — the caller decides what the signer is permitted to do.
func (s *Server) validateDeleteAuth(evt *nostr.Event, hash string) error {
	if evt.Kind != 24242 {
		return errors.New("auth: wrong kind (want 24242)")
	}
	if tagValue(evt, "t") != "delete" {
		return errors.New("auth: not a 'delete' authorization")
	}
	exp := tagValue(evt, "expiration")
	if ts, e := strconv.ParseInt(exp, 10, 64); e != nil || ts < time.Now().Unix() {
		return errors.New("auth: missing or expired 'expiration'")
	}
	if tagValue(evt, "x") != hash {
		return errors.New("auth: 'x' does not match the blob")
	}
	if ok, e := evt.CheckSignature(); e != nil || !ok {
		return errors.New("auth: bad signature")
	}
	return nil
}

// handleDelete: admin → hard delete; uploader → retract their claim; stranger → 403.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	setGateCORS(w)
	hash, ok := blobHashFromPath(r.URL.Path)
	if !ok {
		httpErr(w, http.StatusBadRequest, "invalid blob path")
		return
	}
	evt, err := parseNostrAuth(r)
	if err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err := s.validateDeleteAuth(evt, hash); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}

	// Admin: content moderation → hard delete the bytes.
	if s.adminPubkey != "" && evt.PubKey == s.adminPubkey {
		// ext "" lets the storage layer resolve the actual object by hash prefix.
		if err := s.storage.Delete(r.Context(), hash, ""); err != nil {
			httpErr(w, http.StatusBadGateway, "storage delete failed")
			return
		}
		s.owners.remove(hash)
		s.quarantine.remove(hash)
		s.invalidateBlobCache()
		slog.Info("blob deleted", "hash", hash, "admin", evt.PubKey)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Non-admin: retract this uploader's own upload. This never deletes bytes.
	removed, nowEmpty := s.owners.removeOne(hash, evt.PubKey)
	if !removed {
		httpErr(w, http.StatusForbidden, "you are not an uploader of this blob")
		return
	}
	if nowEmpty && len(s.refs.refsFor(hash)) == 0 {
		// No owner left and no stored mod references it → quarantine: withhold
		// downloads (a re-upload revives it), keep the bytes for the admin to purge
		// or release. A still-referenced blob is left available so its mod keeps working.
		s.quarantine.add(hash)
	}
	s.invalidateBlobCache()
	slog.Info("upload retracted", "hash", hash, "pubkey", evt.PubKey, "now_empty", nowEmpty)
	w.WriteHeader(http.StatusOK)
}
