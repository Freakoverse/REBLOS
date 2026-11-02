package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip13"
)

// ── auth ─────────────────────────────────────────────────────────────────────

// parseNostrAuth decodes "Authorization: Nostr <base64-json-event>".
func parseNostrAuth(r *http.Request) (*nostr.Event, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return nil, errors.New("missing Authorization header")
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Nostr") {
		return nil, errors.New("expected 'Authorization: Nostr <base64>'")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[1]))
	if err != nil {
		return nil, errors.New("invalid base64 in Authorization")
	}
	var evt nostr.Event
	if err := json.Unmarshal(raw, &evt); err != nil {
		return nil, errors.New("invalid auth event JSON")
	}
	return &evt, nil
}

func tagValue(evt *nostr.Event, name string) string {
	for _, t := range evt.Tags {
		if len(t) >= 2 && t[0] == name {
			return t[1]
		}
	}
	return ""
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// verifyUploadAuth validates a kind-24242 upload authorization and returns the
// claimed blob hash (its "x" tag).
func (s *Server) verifyUploadAuth(evt *nostr.Event) (hash string, err error) {
	if evt.Kind != 24242 {
		return "", errors.New("auth: wrong kind (want 24242)")
	}
	if tagValue(evt, "t") != "upload" {
		return "", errors.New("auth: not an 'upload' authorization")
	}
	exp := tagValue(evt, "expiration")
	if ts, e := strconv.ParseInt(exp, 10, 64); e != nil || ts < time.Now().Unix() {
		return "", errors.New("auth: missing or expired 'expiration'")
	}
	hash = tagValue(evt, "x")
	if !isSHA256Hex(hash) {
		return "", errors.New("auth: missing/invalid 'x' (blob sha256)")
	}
	if s.minUploadPoW > 0 && nip13.Difficulty(evt.ID) < s.minUploadPoW {
		return "", errors.New("auth: insufficient proof-of-work")
	}
	if ok, e := evt.CheckSignature(); e != nil || !ok {
		return "", errors.New("auth: bad signature")
	}
	return hash, nil
}

// ── upload type validation ───────────────────────────────────────────────────

// uploadMagic maps a file's leading bytes (optionally at a fixed offset) to an
// extension + content-type. Order matters (longer/more-specific prefixes first).
type uploadMagic struct {
	at     int // byte offset the prefix starts at (0 for most formats)
	prefix []byte
	ext    string
	ctype  string
}

func (m uploadMagic) match(b []byte) bool {
	if len(b) < m.at+len(m.prefix) {
		return false
	}
	return bytes.HasPrefix(b[m.at:], m.prefix)
}

var uploadMagics = []uploadMagic{
	// Archives / executables (not in the default allowlist; opt-in via allowed_types).
	{prefix: []byte("PK\x03\x04"), ext: "zip", ctype: "application/zip"},
	{prefix: []byte("Rar!\x1a\x07"), ext: "rar", ctype: "application/vnd.rar"},
	{prefix: []byte("7z\xbc\xaf\x27\x1c"), ext: "7z", ctype: "application/x-7z-compressed"},
	{prefix: []byte("\x1f\x8b"), ext: "gz", ctype: "application/gzip"},
	{prefix: []byte("MZ"), ext: "exe", ctype: "application/vnd.microsoft.portable-executable"},
	// Images.
	{prefix: []byte("\x89PNG\r\n\x1a\n"), ext: "png", ctype: "image/png"},
	{prefix: []byte("\xff\xd8\xff"), ext: "jpg", ctype: "image/jpeg"},
	{prefix: []byte("GIF87a"), ext: "gif", ctype: "image/gif"},
	{prefix: []byte("GIF89a"), ext: "gif", ctype: "image/gif"},
	{at: 8, prefix: []byte("WEBP"), ext: "webp", ctype: "image/webp"}, // RIFF....WEBP
	{prefix: []byte("BM"), ext: "bmp", ctype: "image/bmp"},
	// Audio.
	{prefix: []byte("ID3"), ext: "mp3", ctype: "audio/mpeg"},
	{prefix: []byte("\xff\xfb"), ext: "mp3", ctype: "audio/mpeg"},
	{prefix: []byte("OggS"), ext: "ogg", ctype: "audio/ogg"},
	{prefix: []byte("fLaC"), ext: "flac", ctype: "audio/flac"},
	{at: 8, prefix: []byte("WAVE"), ext: "wav", ctype: "audio/wav"}, // RIFF....WAVE
	// Video.
	{prefix: []byte("\x1aE\xdf\xa3"), ext: "webm", ctype: "video/webm"}, // matroska/webm
	{at: 4, prefix: []byte("ftyp"), ext: "mp4", ctype: "video/mp4"},     // ISO-BMFF (mp4/mov/m4a)
	// Documents.
	{prefix: []byte("%PDF-"), ext: "pdf", ctype: "application/pdf"},
}

// sniffLen is how much of a blob we read to detect its type. Enough for every
// magic above and a solid UTF-8/text sample, bounded so a huge binary without a
// magic isn't read in full.
const sniffLen = 16 << 10

func (s *Server) uploadTypeAllowed(ext string) bool {
	for _, t := range s.allowedUploadTypes {
		if t == "*" || strings.EqualFold(t, ext) {
			return true
		}
	}
	return false
}

// classifyUpload detects the spooled file's type from its magic bytes — and, with
// no magic match, whether it is UTF-8 text (the hub member-tree / struct blobs are
// text/plain) — then enforces the configured allow-list. The client's declared
// Content-Type is never trusted; only the bytes decide. Returns the extension +
// content-type to store the blob under.
func (s *Server) classifyUpload(f *os.File) (ext, ctype string, err error) {
	if _, e := f.Seek(0, io.SeekStart); e != nil {
		return "", "", e
	}
	buf := make([]byte, sniffLen)
	n, _ := io.ReadFull(f, buf) // short reads (EOF/ErrUnexpectedEOF) are expected and fine
	b := buf[:n]

	ext, ctype = "", ""
	for _, m := range uploadMagics {
		if m.match(b) {
			ext, ctype = m.ext, m.ctype
			break
		}
	}
	if ext == "" { // no binary magic: text/plain if it sniffs as UTF-8 text, else opaque
		if looksTextual(b) {
			ext, ctype = "txt", "text/plain"
		} else {
			ext, ctype = "bin", "application/octet-stream"
		}
	}
	if !s.uploadTypeAllowed(ext) {
		return "", "", fmt.Errorf("this server does not accept .%s files", ext)
	}
	// General-purpose bit flag at offset 6 (little-endian); bit 0 = encrypted.
	if ext == "zip" && n >= 8 && (uint16(b[6])|uint16(b[7])<<8)&0x0001 != 0 {
		return "", "", errors.New("encrypted zip archives are not accepted")
	}
	return ext, ctype, nil
}

// looksTextual reports whether a sample is UTF-8 text with no NUL bytes. An empty
// blob counts as text. A sample cut mid-rune at the boundary is tolerated by
// trimming up to 3 trailing bytes before the final UTF-8 check.
func looksTextual(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	if bytes.IndexByte(b, 0x00) >= 0 {
		return false
	}
	if !utf8.Valid(b) {
		t := b
		for i := 0; i < 3 && len(t) > 0 && !utf8.Valid(t); i++ {
			t = t[:len(t)-1]
		}
		return utf8.Valid(t)
	}
	return true
}

// ── http helpers ─────────────────────────────────────────────────────────────

func setUploadCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "PUT, HEAD, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, X-Sha256, X-SHA-256, X-Content-Type, X-Content-Length, Content-Type")
	h.Set("Access-Control-Expose-Headers", "X-Reason")
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("X-Reason", msg)
	http.Error(w, msg, code)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

var (
	errUploadTooLarge = errors.New("upload exceeds size limit")
	errUploadStalled  = errors.New("upload stalled")
	errUploadTooSlow  = errors.New("upload too slow")
)

// rateWindow is the interval over which minimum upload throughput is measured.
const rateWindow = 5 * time.Second

// uploadLimitFor returns the max upload size for a pubkey — the raised cap for
// whitelisted keys, the normal cap otherwise. Read fresh from the (runtime-
// adjustable) caps store on every call, so a dashboard change applies to the next
// upload without disturbing any already in flight.
func (s *Server) uploadLimitFor(pubkey string) int64 {
	if s.white.has(pubkey) {
		return s.caps.whitelistBytes()
	}
	return s.caps.normalBytes()
}

// streamBody copies the request body to dst, enforcing the size cap, an idle
// timeout (reset every chunk), and a minimum average throughput measured over a
// rolling 5s window. A stalled or trickle connection is aborted so it can't hold a
// concurrency slot + temp file indefinitely; slow-but-adequate uploads are fine.
// minRateBps is the minimum-rate floor to enforce (0 = no floor), passed per
// upload so trusted (whitelisted) uploaders can go as slowly as they need. The
// idle timeout still applies regardless, so a genuinely dead connection is cut.
func (s *Server) streamBody(w http.ResponseWriter, r *http.Request, dst io.Writer, limit, minRateBps int64) (int64, error) {
	rc := http.NewResponseController(w)
	buf := make([]byte, 128*1024)
	var total int64

	// Bytes required each window to stay above the minimum rate (0 = no minimum).
	windowMin := minRateBps * int64(rateWindow/time.Second)
	windowStart := time.Now()
	var windowBytes int64

	for {
		if s.uploadIdleTimeout > 0 {
			// Best-effort: some ResponseWriters (e.g. httptest) don't support deadlines.
			_ = rc.SetReadDeadline(time.Now().Add(s.uploadIdleTimeout))
		}
		nr, er := r.Body.Read(buf)
		if nr > 0 {
			total += int64(nr)
			windowBytes += int64(nr)
			if total > limit {
				return total, errUploadTooLarge
			}
			if _, ew := dst.Write(buf[:nr]); ew != nil {
				return total, ew
			}
		}
		if windowMin > 0 && time.Since(windowStart) >= rateWindow {
			if windowBytes < windowMin {
				return total, errUploadTooSlow
			}
			windowStart = time.Now()
			windowBytes = 0
		}
		if er == io.EOF {
			return total, nil
		}
		if er != nil {
			if os.IsTimeout(er) {
				return total, errUploadStalled
			}
			return total, er
		}
	}
}

// ── handlers ─────────────────────────────────────────────────────────────────

// handleUploadHead is the BUD-06 pre-flight: validate auth + declared size so the
// client learns whether a 500 MB PUT will be accepted before sending it.
func (s *Server) handleUploadHead(w http.ResponseWriter, r *http.Request) {
	setUploadCORS(w)
	evt, err := parseNostrAuth(r)
	if err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	hash, err := s.verifyUploadAuth(evt)
	if err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if s.blocked(evt.PubKey) {
		httpErr(w, http.StatusForbidden, "blocked")
		return
	}
	if !s.mayUpload(evt.PubKey) {
		httpErr(w, http.StatusForbidden, "uploads are restricted to members of this relay's hubs")
		return
	}
	if s.blacklist.has(hash) {
		httpErr(w, http.StatusForbidden, "this file is not permitted on this server")
		return
	}
	if sz := r.Header.Get("X-Content-Length"); sz != "" {
		if v, e := strconv.ParseInt(sz, 10, 64); e == nil && v > s.uploadLimitFor(evt.PubKey) {
			httpErr(w, http.StatusRequestEntityTooLarge, "file exceeds the size limit")
			return
		}
	}
	if free, ok := freeDiskMB(s.tempDir); ok && free < s.minFreeDiskMB {
		httpErr(w, http.StatusInsufficientStorage, "server is low on disk")
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleUploadPut streams the body to a temp file, hashing on the fly, verifies
// the hash against the auth, enforces zip-only + size + concurrency, then stores
// to R2. Memory stays flat regardless of file size.
func (s *Server) handleUploadPut(w http.ResponseWriter, r *http.Request) {
	setUploadCORS(w)
	ctx := r.Context()

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
	if s.blocked(evt.PubKey) {
		httpErr(w, http.StatusForbidden, "blocked")
		return
	}
	if !s.mayUpload(evt.PubKey) {
		httpErr(w, http.StatusForbidden, "uploads are restricted to members of this relay's hubs")
		return
	}
	if s.blacklist.has(claimed) {
		httpErr(w, http.StatusForbidden, "this file is not permitted on this server")
		return
	}
	limit := s.uploadLimitFor(evt.PubKey)
	if r.ContentLength > limit {
		httpErr(w, http.StatusRequestEntityTooLarge, "file exceeds the size limit")
		return
	}
	if free, ok := freeDiskMB(s.tempDir); ok && free < s.minFreeDiskMB {
		httpErr(w, http.StatusInsufficientStorage, "server is low on disk")
		return
	}

	release, err := s.limiter.acquire(evt.PubKey)
	if err != nil {
		httpErr(w, http.StatusTooManyRequests, err.Error())
		return
	}
	defer release()

	tmp, err := os.CreateTemp(s.tempDir, "deg-up-*.tmp")
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "could not open temp file")
		return
	}
	tmpName := tmp.Name()
	defer func() { tmp.Close(); os.Remove(tmpName) }()

	// Stream → temp file + hasher, hard-capped in case Content-Length lied, with an
	// idle timeout so a stalled or trickle upload can't hold a slot/temp file forever.
	// Whitelisted (trusted) uploaders are exempt from the minimum-rate floor — they
	// can upload as slowly as their connection allows without being aborted.
	minRate := s.minUploadRateBps
	if s.white.has(evt.PubKey) {
		minRate = 0
	}
	h := sha256.New()
	n, err := s.streamBody(w, r, io.MultiWriter(tmp, h), limit, minRate)
	if err == errUploadTooLarge {
		httpErr(w, http.StatusRequestEntityTooLarge, "file exceeds the size limit")
		return
	}
	if err == errUploadStalled {
		httpErr(w, http.StatusRequestTimeout, "upload stalled (no data received) — aborted")
		return
	}
	if err == errUploadTooSlow {
		httpErr(w, http.StatusRequestTimeout, "upload too slow (below the minimum speed) — aborted")
		return
	}
	if err != nil {
		httpErr(w, http.StatusBadRequest, "error reading upload")
		return
	}

	sum := hex.EncodeToString(h.Sum(nil))
	if sum != claimed {
		httpErr(w, http.StatusBadRequest, "sha256 does not match the authorization")
		return
	}
	ext, ctype, err := s.classifyUpload(tmp)
	if err != nil {
		httpErr(w, http.StatusUnsupportedMediaType, err.Error())
		return
	}

	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		httpErr(w, http.StatusInternalServerError, "temp seek failed")
		return
	}
	if err := s.storage.Store(ctx, sum, ext, ctype, tmp, n); err != nil {
		httpErr(w, http.StatusBadGateway, "storage write failed")
		return
	}
	// Remember who uploaded this, for the admin dashboard and later blob↔mod
	// reconciliation. Best-effort: a failure here doesn't fail the upload.
	s.owners.add(sum, evt.PubKey)
	// A re-upload revives a quarantined blob (someone wants it again).
	s.quarantine.remove(sum)

	writeJSON(w, http.StatusOK, map[string]any{
		"url":      s.publicURL + "/" + sum + "." + ext,
		"sha256":   sum,
		"size":     n,
		"type":     ctype,
		"uploaded": time.Now().Unix(),
	})
}
