package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DEGMods/Blossom-Relay-Server/internal/storage"
	"github.com/nbd-wtf/go-nostr"
)

// Admin API — moderation/management endpoints authenticated via NIP-98 (a signed
// kind-27235 event) whose pubkey must equal the configured admin. The admin signs
// in their browser (NIP-07), so no raw nsec is ever handled on the server/CLI.

const blobCacheTTL = 30 * time.Second

// verifyAdmin authenticates a NIP-98 request signed by the admin key.
func (s *Server) verifyAdmin(r *http.Request) error {
	if s.adminPubkey == "" {
		return errors.New("admin API disabled (set relay.admin_npub)")
	}
	evt, err := parseNostrAuth(r)
	if err != nil {
		return err
	}
	if evt.Kind != 27235 {
		return errors.New("auth: expected NIP-98 (kind 27235)")
	}
	if d := time.Now().Unix() - int64(evt.CreatedAt); d < -60 || d > 60 {
		return errors.New("auth: timestamp too far from now")
	}
	if !strings.EqualFold(tagValue(evt, "method"), r.Method) {
		return errors.New("auth: method mismatch")
	}
	if pu, e := url.Parse(tagValue(evt, "u")); e != nil || pu.Path != r.URL.Path {
		return errors.New("auth: url mismatch")
	}
	if ok, e := evt.CheckSignature(); e != nil || !ok {
		return errors.New("auth: bad signature")
	}
	if evt.PubKey != s.adminPubkey {
		return errors.New("auth: not the admin key")
	}
	return nil
}

func setAdminCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	h.Set("Access-Control-Expose-Headers", "X-Reason")
}

// listBlobsCached returns the full blob listing, refreshed at most every
// blobCacheTTL so paging/searching doesn't re-scan storage on every click.
func (s *Server) listBlobsCached(ctx context.Context) ([]storage.BlobInfo, error) {
	s.blobCacheMu.Lock()
	defer s.blobCacheMu.Unlock()
	if s.blobCache != nil && time.Since(s.blobCacheAt) < blobCacheTTL {
		return s.blobCache, nil
	}
	list, err := s.storage.List(ctx)
	if err != nil {
		return nil, err
	}
	s.blobCache = list
	s.blobCacheAt = time.Now()
	return list, nil
}

func (s *Server) invalidateBlobCache() {
	s.blobCacheMu.Lock()
	s.blobCache = nil
	s.blobCacheMu.Unlock()
}

// handleAdminBlobs lists stored blobs (name + size), filtered by an optional
// substring `search` on the hash, with numbered pagination.
func (s *Server) handleAdminBlobs(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	all, err := s.listBlobsCached(r.Context())
	if err != nil {
		httpErr(w, http.StatusBadGateway, "storage list failed")
		return
	}

	q := r.URL.Query()
	search := strings.ToLower(strings.TrimSpace(q.Get("search")))

	// Distinct types across the whole library (for the client's filter toggles),
	// and the requested type filter (comma-separated, e.g. "zip,png").
	typeSet := map[string]struct{}{}
	for _, b := range all {
		if b.Ext != "" {
			typeSet[strings.ToLower(b.Ext)] = struct{}{}
		}
	}
	types := make([]string, 0, len(typeSet))
	for t := range typeSet {
		types = append(types, t)
	}
	sort.Strings(types)

	wantExt := map[string]bool{}
	for _, e := range strings.Split(q.Get("ext"), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			wantExt[e] = true
		}
	}

	filtered := make([]storage.BlobInfo, 0, len(all))
	for _, b := range all {
		if search != "" && !strings.Contains(strings.ToLower(b.Hash), search) {
			continue
		}
		if len(wantExt) > 0 && !wantExt[strings.ToLower(b.Ext)] {
			continue
		}
		filtered = append(filtered, b)
	}

	// Sort by the requested field/direction (default: hash, ascending).
	desc := strings.EqualFold(q.Get("dir"), "desc")
	less := func(i, j int) bool { return filtered[i].Hash < filtered[j].Hash }
	switch strings.ToLower(q.Get("sort")) {
	case "size":
		less = func(i, j int) bool { return filtered[i].Size < filtered[j].Size }
	case "date":
		less = func(i, j int) bool { return filtered[i].Modified.Before(filtered[j].Modified) }
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if desc {
			return less(j, i)
		}
		return less(i, j)
	})

	per := clampInt(atoiOr(q.Get("per"), 50), 1, 200)
	total := len(filtered)
	pages := (total + per - 1) / per
	page := clampInt(atoiOr(q.Get("page"), 1), 1, maxInt(pages, 1))
	start := (page - 1) * per
	if start > total {
		start = total
	}
	end := minInt(start+per, total)

	type blobDTO struct {
		Hash      string   `json:"hash"`
		Ext       string   `json:"ext"`
		Size      int64    `json:"size"`
		URL       string   `json:"url"`
		Added       int64    `json:"added"`                // unix seconds; 0 if unknown
		Uploaders   []string `json:"uploaders,omitempty"`  // pubkeys, original first; empty if untracked (legacy)
		Refs        []modRef `json:"refs,omitempty"`       // mods that reference this blob; empty = unclaimed
		Quarantined bool     `json:"quarantined,omitempty"` // withheld from download (last uploader retracted it)
	}
	items := make([]blobDTO, 0, end-start)
	for _, b := range filtered[start:end] {
		added := int64(0)
		if !b.Modified.IsZero() {
			added = b.Modified.Unix()
		}
		items = append(items, blobDTO{
			Hash: b.Hash, Ext: b.Ext, Size: b.Size,
			URL: s.publicURL + "/" + b.Key, Added: added, Uploaders: s.owners.list(b.Hash),
			Refs: s.refs.refsFor(b.Hash), Quarantined: s.quarantine.has(b.Hash),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total": total, "page": page, "per": per, "pages": pages, "types": types, "blobs": items,
	})
}

// handleAdminBlobDownload streams a blob straight to the admin, bypassing the
// BUD-POW / ad download gate (which only wraps the public "/" route). Being
// admin-authed and gate-free is what makes it the dashboard's no-friction
// download — a plain public link would hit the gate.
func (s *Server) handleAdminBlobDownload(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	name := r.PathValue("hash") // "<hash>" or "<hash>.<ext>"
	hash, ext := name, ""
	if dot := strings.IndexByte(name, '.'); dot >= 0 {
		hash, ext = name[:dot], name[dot+1:]
	}
	if !isSHA256Hex(hash) {
		httpErr(w, http.StatusBadRequest, "bad hash")
		return
	}
	rc, err := s.storage.Load(r.Context(), hash, ext)
	if err != nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	defer rc.Close()

	filename := hash
	if ext != "" {
		filename += "." + ext
	}
	// octet-stream + attachment forces a download rather than an inline preview.
	// ServeContent handles Range requests via the ReadSeeker (resumable).
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	http.ServeContent(w, r, filename, time.Time{}, rc)
}

// handleAdminBlobDelete deletes a blob via NIP-98 admin auth — the delete path the
// dashboard uses. (The public DELETE /<hash> takes a BUD-compliant kind-24242
// authorization instead; the dashboard signs NIP-98 like every other admin call.)
func (s *Server) handleAdminBlobDelete(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	name := r.PathValue("hash")
	hash := name
	if dot := strings.IndexByte(name, '.'); dot >= 0 {
		hash = name[:dot]
	}
	if !isSHA256Hex(hash) {
		httpErr(w, http.StatusBadRequest, "bad hash")
		return
	}
	if err := s.storage.Delete(r.Context(), hash, ""); err != nil {
		httpErr(w, http.StatusBadGateway, "storage delete failed")
		return
	}
	s.owners.remove(hash)
	s.invalidateBlobCache()
	w.WriteHeader(http.StatusNoContent)
}

// handleBlacklistList returns the blacklisted-hash entries.
func (s *Server) handleBlacklistList(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": s.blacklist.list()})
}

// handleBlacklistAdd blacklists a hash and purges any stored copy — so it's both
// removed now and barred from re-upload. Blacklisting a not-yet-stored hash is a
// valid pre-emptive block (the purge is then a no-op).
func (s *Server) handleBlacklistAdd(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var body struct {
		Hash   string `json:"hash"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	hash := strings.ToLower(strings.TrimSpace(body.Hash))
	if !isSHA256Hex(hash) {
		httpErr(w, http.StatusBadRequest, "invalid hash")
		return
	}
	if err := s.blacklist.add(hash, strings.TrimSpace(body.Reason)); err != nil {
		httpErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	// Purge the stored object too (best-effort — the entry is what enforces the ban).
	_ = s.storage.Delete(r.Context(), hash, "")
	s.owners.remove(hash)
	s.invalidateBlobCache()
	w.WriteHeader(http.StatusNoContent)
}

// handleQuarantineRelease lifts a blob's quarantine, making it downloadable again
// (the bytes were never removed). Admin-only.
func (s *Server) handleQuarantineRelease(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var body struct {
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	s.quarantine.remove(strings.ToLower(strings.TrimSpace(body.Hash)))
	s.invalidateBlobCache()
	w.WriteHeader(http.StatusNoContent)
}

// handleBlacklistRemove lifts a blacklist entry (does not restore any bytes).
func (s *Server) handleBlacklistRemove(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var body struct {
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.blacklist.remove(strings.ToLower(strings.TrimSpace(body.Hash))); err != nil {
		httpErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAdminWhitelistList returns the upload-size whitelist + the two size caps
// (live values, so they reflect any runtime change made via /admin/upload-caps).
func (s *Server) handleAdminWhitelistList(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	normalMB, whitelistMB := s.caps.snapshotMB()
	defNormalMB, defWhitelistMB := s.caps.defaultsMB()
	writeJSON(w, http.StatusOK, map[string]any{
		"limit_mb":               normalMB,
		"whitelisted_mb":         whitelistMB,
		"default_limit_mb":       defNormalMB,
		"default_whitelisted_mb": defWhitelistMB,
		"entries":                s.white.list(),
	})
}

// handleAdminUploadCapsSet updates the per-upload size caps (normal + whitelisted),
// in MB. Validated and persisted; the upload path reads the caps fresh, so the
// change applies to the next upload.
func (s *Server) handleAdminUploadCapsSet(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var body struct {
		LimitMB       int64 `json:"limit_mb"`
		WhitelistedMB int64 `json:"whitelisted_mb"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.caps.set(body.LimitMB, body.WhitelistedMB); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAdminUploadCapsReset clears any runtime override and restores the caps
// configured in the node's config file.
func (s *Server) handleAdminUploadCapsReset(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err := s.caps.reset(); err != nil {
		httpErr(w, http.StatusInternalServerError, "reset failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminWhitelistAdd(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var body struct {
		Pubkey string `json:"pubkey"`
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	pk := resolvePubkey(strings.TrimSpace(body.Pubkey))
	if !isSHA256Hex(pk) {
		httpErr(w, http.StatusBadRequest, "invalid pubkey/npub")
		return
	}
	if err := s.white.add(pk, strings.TrimSpace(body.Note)); err != nil {
		httpErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminWhitelistRemove(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var body struct {
		Pubkey string `json:"pubkey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.white.remove(resolvePubkey(strings.TrimSpace(body.Pubkey))); err != nil {
		httpErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── event takedowns (persistent, address-based) ───────────────────────────────

func (s *Server) handleBannedEventsList(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": s.bannedEv.list()})
}

// handleBanEvent bans an event key (its "<kind>:<pubkey>:<d>" coordinate or id) so
// it is auto-rejected on re-publish, and deletes the currently-stored copy.
func (s *Server) handleBanEvent(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var body struct {
		Key    string `json:"key"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	key := strings.TrimSpace(body.Key)
	if key == "" {
		httpErr(w, http.StatusBadRequest, "missing event key")
		return
	}
	if err := s.bannedEv.ban(key, strings.TrimSpace(body.Reason)); err != nil {
		httpErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	s.deleteStoredByKey(r.Context(), key)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUnbanEvent(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.bannedEv.unban(strings.TrimSpace(body.Key)); err != nil {
		httpErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteStoredByKey removes the currently-stored event matching a ban key (an
// addressable coordinate "<kind>:<pubkey>:<d>", or a bare event id).
func (s *Server) deleteStoredByKey(ctx context.Context, key string) {
	var f nostr.Filter
	if parts := strings.SplitN(key, ":", 3); len(parts) == 3 {
		kind, err := strconv.Atoi(parts[0])
		if err != nil {
			return
		}
		f = nostr.Filter{Kinds: []int{kind}, Authors: []string{parts[1]}, Tags: nostr.TagMap{"d": []string{parts[2]}}}
	} else if isSHA256Hex(key) {
		f = nostr.Filter{IDs: []string{key}}
	} else {
		return
	}
	ch, err := s.store.QueryEvents(ctx, f)
	if err != nil {
		return
	}
	for evt := range ch {
		_ = s.store.DeleteEvent(ctx, evt)
	}
}

// ── small helpers ─────────────────────────────────────────────────────────────

func atoiOr(s string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return v
	}
	return def
}

func clampInt(v, lo, hi int) int { return maxInt(lo, minInt(v, hi)) }

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
