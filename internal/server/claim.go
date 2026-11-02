package server

import (
	"net/http"
	"strings"
)

// handleClaim records the caller as an uploader of an ALREADY-stored blob, without
// re-transferring the bytes. The client's upload flow HEAD-checks each server and,
// on a hit, skips the upload (content is deduplicated) — so without this the owner
// set would silently miss everyone who deduped, and they'd never see the file in
// "my uploads" or be able to retract it. The client calls this in that skip branch
// with the same signed kind-24242 upload authorization it would have uploaded with.
//
// It only ever ADDS an uploader to a blob that's genuinely here; it never stores,
// deletes, or serves bytes.
func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	setUploadCORS(w)

	name := r.PathValue("hash")
	hash := name
	if dot := strings.IndexByte(name, '.'); dot >= 0 {
		hash = name[:dot]
	}
	if !isSHA256Hex(hash) {
		httpErr(w, http.StatusBadRequest, "invalid blob hash")
		return
	}

	evt, err := parseNostrAuth(r)
	if err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	claimed, err := s.verifyUploadAuth(evt)
	if err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if claimed != hash {
		httpErr(w, http.StatusBadRequest, "authorization is for a different blob")
		return
	}
	if s.blocked(evt.PubKey) {
		httpErr(w, http.StatusForbidden, "blocked")
		return
	}
	if s.blacklist.has(hash) {
		httpErr(w, http.StatusForbidden, "this file is not permitted on this server")
		return
	}
	// You can only claim a blob that's actually stored here — no phantom ownership.
	if ok, err := s.storage.Has(r.Context(), hash, ""); err != nil || !ok {
		httpErr(w, http.StatusNotFound, "blob not found")
		return
	}

	s.owners.add(hash, evt.PubKey)
	s.quarantine.remove(hash) // a fresh claim means it's wanted again (usually a no-op)
	s.invalidateBlobCache()
	w.WriteHeader(http.StatusNoContent)
}
