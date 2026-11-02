package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/bits"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// BUD-POW download gate. When enabled, blob GET/HEAD requires a proof-of-work
// bound to (blob, client IP, expiry). Challenges are stateless (HMAC), so no
// per-challenge server state is kept; a proof is reusable until it expires, which
// covers ranged/resumed downloads.

// blobHashFromPath extracts the sha256 from "/<sha256>[.ext]".
func blobHashFromPath(p string) (string, bool) {
	p = strings.TrimPrefix(p, "/")
	if dot := strings.IndexByte(p, '.'); dot >= 0 {
		p = p[:dot]
	}
	if isSHA256Hex(p) {
		return p, true
	}
	return "", false
}

// clientIP resolves the caller IP, honoring a trusted forwarded header if set.
func (s *Server) clientIP(r *http.Request) string {
	if s.trustedIPHeader != "" {
		if v := r.Header.Get(s.trustedIPHeader); v != "" {
			return strings.TrimSpace(strings.SplitN(v, ",", 2)[0])
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func leadingZeroBits(b []byte) int {
	n := 0
	for _, x := range b {
		if x == 0 {
			n += 8
			continue
		}
		n += bits.LeadingZeros8(x)
		break
	}
	return n
}

// powChallenge builds a stateless challenge token c bound to (hash, ip, exp, d).
func (s *Server) powChallenge(hash, ip string) string {
	exp := time.Now().Add(s.challengeTTL).Unix()
	rnd := make([]byte, 8)
	_, _ = rand.Read(rnd)
	payload := fmt.Sprintf("%s|%s|%d|%d|%s", hash, ip, exp, s.powDifficulty, hex.EncodeToString(rnd))
	mac := hmac.New(sha256.New, s.gateKey)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyPow validates "<c>:<nonce>" for the given blob + IP.
func (s *Server) verifyPow(proof, hash, ip string) bool {
	sep := strings.LastIndexByte(proof, ':')
	if sep < 0 {
		return false
	}
	c := proof[:sep]

	dot := strings.IndexByte(c, '.')
	if dot < 0 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(c[:dot])
	if err != nil {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(c[dot+1:])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, s.gateKey)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return false
	}

	parts := strings.Split(string(payload), "|")
	if len(parts) != 5 {
		return false
	}
	if parts[0] != hash || parts[1] != ip {
		return false
	}
	if exp, e := strconv.ParseInt(parts[2], 10, 64); e != nil || exp < time.Now().Unix() {
		return false
	}
	d, e := strconv.Atoi(parts[3])
	if e != nil {
		return false
	}
	sum := sha256.Sum256([]byte(proof)) // proof == c ":" nonce
	return leadingZeroBits(sum[:]) >= d
}

// signToken builds "<base64url(payload)>.<base64url(hmac)>" over "|"-joined parts.
func (s *Server) signToken(parts ...string) string {
	payload := strings.Join(parts, "|")
	mac := hmac.New(sha256.New, s.gateKey)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// parseToken verifies the HMAC and returns the payload fields.
func (s *Server) parseToken(c string) ([]string, bool) {
	dot := strings.IndexByte(c, '.')
	if dot < 0 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(c[:dot])
	if err != nil {
		return nil, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(c[dot+1:])
	if err != nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, s.gateKey)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, false
	}
	return strings.Split(string(payload), "|"), true
}

func setGateCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, HEAD, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, X-Blossom-Gate-Pow-Proof, X-Blossom-Gate-Ad-Proof, Range")
	h.Set("Access-Control-Expose-Headers", "X-Blossom-Gate-Pow, X-Blossom-Gate-Ad, X-Reason, Accept-Ranges, Content-Length")
}

// gate wraps the blob handler with the download gates (PoW + ad). No-op when both
// are off. Both can be required together (the "two-birds" flow); a 428 carries a
// challenge header per unsatisfied gate.
func (s *Server) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hash, isBlob := blobHashFromPath(r.URL.Path)
		if !isBlob {
			next.ServeHTTP(w, r)
			return
		}
		setGateCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Blacklisted (permanent ban) or quarantined (reversible — last uploader
		// retracted it) content is withheld regardless of the gate: retrieval 404s as
		// if deleted. Blacklist re-upload is rejected; a quarantine is lifted by one.
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			(s.blacklist.has(hash) || s.quarantine.has(hash)) {
			httpErr(w, http.StatusNotFound, "not found")
			return
		}
		// A HEAD is a plain existence check (BUD-01): it transfers no bytes, so it is
		// NOT gated — gating it would 428 the client's "do you already have this?"
		// dedup check, forcing a needless full re-upload of a blob we already hold.
		// Answered straight from storage, because our streaming /upload bypasses
		// khatru's blob index, so khatru's own HEAD wouldn't know about these blobs.
		if r.Method == http.MethodHead {
			if ok, err := s.storage.Has(r.Context(), hash, ""); err == nil && ok {
				w.WriteHeader(http.StatusOK)
			} else {
				httpErr(w, http.StatusNotFound, "not found")
			}
			return
		}
		// A blob uploaded by a whitelisted (trusted) npub skips the download gate, so
		// a trusted author's files are directly downloadable — no PoW/ad — including
		// from a plain browser or a shared link, not just inside the DEG Mods client.
		if r.Method == http.MethodGet &&
			s.powDifficulty > 0 && !s.gateExempt(hash) {
			if !s.checkGates(w, r, hash) {
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// gateExempt reports whether a blob bypasses the download gate because one of its
// uploaders is on the whitelist (the same trusted-npub list as the raised upload cap).
func (s *Server) gateExempt(hash string) bool {
	return s.owners.hasOwnerIn(hash, s.white.has)
}

// checkGates enforces the enabled gates; returns false (and writes 428) if unmet.
func (s *Server) checkGates(w http.ResponseWriter, r *http.Request, hash string) bool {
	ip := s.clientIP(r)
	challenged := false

	if s.powDifficulty > 0 {
		if !s.verifyPow(r.Header.Get("X-Blossom-Gate-Pow-Proof"), hash, ip) {
			w.Header().Set("X-Blossom-Gate-Pow",
				fmt.Sprintf("v=1; d=%d; c=%s", s.powDifficulty, s.powChallenge(hash, ip)))
			challenged = true
		}
	}
	if challenged {
		httpErr(w, http.StatusPreconditionRequired, "gate requirements not satisfied")
		return false
	}
	return true
}
