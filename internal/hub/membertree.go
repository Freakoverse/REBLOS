// Package hub parses NIP-CHAT hub member-tree files to derive a hub's poster
// set: the member pubkeys authorized to post, with their roles.
//
// The formats are produced by the client in client/src/lib/blossom/members.ts
// and client/src/lib/crypto/lkh.ts. The relay only needs the plaintext `leaf:`
// lines; each is
//
//	leaf:<nodeId>:<pubkey>:<roles>:<encryptedLeafKey>[:<flags>]
//
// where <pubkey> is the member's real key (v1 public hub) or per-hub pseudonym P
// (v2 private hub), and <roles> and <flags> are plaintext. This holds for BOTH
// hub versions: v2 does not encrypt the pseudonym or roles, only the leaf key and
// the page roster (the P->R map), both of which we ignore. So the poster set is
// derivable with no secret, while the real identity behind a v2 pseudonym stays
// hidden.
//
// NIP-04 / AES outputs are base64 (no ':'), so splitting a leaf line on ':' is
// safe, exactly as the client's parser does.
package hub

import (
	"strconv"
	"strings"
)

// Member is one entry parsed from a leaf line.
type Member struct {
	Pubkey string // hex x-only pubkey: real key (v1) or pseudonym P (v2)
	Roles  string // raw roles string as stored, e.g. "everyone"
	Flags  string // optional flags, e.g. "w" (whitelist); "" if absent
}

// LeafPageRef points at one leaf-page blob in a paginated index.
type LeafPageRef struct {
	PageIndex   int
	FirstPubkey string
	Hash        string
}

// Index is a parsed member-tree index file (the blob the hub event's `m` tag
// references). It is one of two shapes, auto-detected:
//
//   - paginated: meta:page_size=..., spine:<hash>, leaf-page:<i>:<firstPubkey>:<hash>
//   - monolithic: tree:<hash>
//
// Both may also carry bans/group/history lines, which are not needed for the
// poster set and are not parsed here.
type Index struct {
	PageSize  int           // paginated only; 0 for monolithic
	SpineHash string        // paginated only
	TreeHash  string        // monolithic only
	LeafPages []LeafPageRef // paginated only
}

// Paginated reports whether this is a paginated index (has leaf pages / a spine).
func (ix Index) Paginated() bool { return len(ix.LeafPages) > 0 || ix.SpineHash != "" }

// MemberBlobHashes returns the content-addressed blob hashes to fetch and scan
// for `leaf:` lines: the leaf-page hashes for a paginated index, or the single
// tree hash for a monolithic one. Returns nil when neither is present.
func (ix Index) MemberBlobHashes() []string {
	if len(ix.LeafPages) > 0 {
		out := make([]string, 0, len(ix.LeafPages))
		for _, lp := range ix.LeafPages {
			if lp.Hash != "" {
				out = append(out, lp.Hash)
			}
		}
		return out
	}
	if ix.TreeHash != "" {
		return []string{ix.TreeHash}
	}
	return nil
}

// ParseIndex parses an index file. It is lenient: unknown or malformed lines are
// skipped, mirroring the client's parseIndexFile.
func ParseIndex(content string) Index {
	var ix Index
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		switch {
		case strings.HasPrefix(t, "meta:"):
			kv := t[len("meta:"):]
			if v, ok := strings.CutPrefix(kv, "page_size="); ok {
				if n, err := strconv.Atoi(v); err == nil {
					ix.PageSize = n
				}
			}
		case strings.HasPrefix(t, "spine:"):
			ix.SpineHash = t[len("spine:"):]
		case strings.HasPrefix(t, "leaf-page:"):
			parts := strings.Split(t[len("leaf-page:"):], ":")
			if len(parts) >= 3 {
				idx, _ := strconv.Atoi(parts[0])
				ix.LeafPages = append(ix.LeafPages, LeafPageRef{
					PageIndex:   idx,
					FirstPubkey: parts[1],
					Hash:        parts[2],
				})
			}
		case strings.HasPrefix(t, "tree:"):
			ix.TreeHash = t[len("tree:"):]
		}
	}
	return ix
}

// ScanMembers extracts members from any member-tree blob (a monolithic tree or a
// single leaf page) by reading its `leaf:` lines. Lines that are not well-formed
// leaf lines, or whose pubkey is not a 32-byte x-only hex key, are skipped, so a
// malformed or hostile blob cannot inject a bogus authorized pubkey.
func ScanMembers(blob string) []Member {
	var out []Member
	for _, line := range strings.Split(blob, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "leaf:") {
			continue
		}
		parts := strings.Split(t[len("leaf:"):], ":")
		// nodeId:pubkey:roles:encKey[:flags]
		if len(parts) < 4 {
			continue
		}
		pubkey := parts[1]
		if !isXOnlyPubkey(pubkey) {
			continue
		}
		m := Member{Pubkey: pubkey, Roles: parts[2]}
		if len(parts) >= 5 {
			m.Flags = parts[4]
		}
		out = append(out, m)
	}
	return out
}

// PosterSet is a hub's authorized posters: a membership test plus each member's
// roles (and optional flags), keyed by pubkey.
type PosterSet struct {
	Roles map[string]string // pubkey -> roles
	Flags map[string]string // pubkey -> flags (only members that carry one)
}

// Has reports whether pubkey is an authorized poster in this set.
func (p PosterSet) Has(pubkey string) bool {
	_, ok := p.Roles[pubkey]
	return ok
}

// Len is the number of authorized posters.
func (p PosterSet) Len() int { return len(p.Roles) }

// BlobFetcher returns the UTF-8 text of a content-addressed blob by its sha256
// hex. The caller is responsible for verifying the bytes hash to the requested
// id before returning them.
type BlobFetcher func(sha256hex string) (string, error)

// BuildPosterSet fetches the index blob named by indexHash, then every member-
// tree blob it references, and returns the union of their members. A leaf page
// that fails to fetch is skipped (best effort, so one bad page does not blank the
// whole roster); an index that fails to fetch is a hard error.
func BuildPosterSet(indexHash string, fetch BlobFetcher) (PosterSet, error) {
	ps := PosterSet{Roles: map[string]string{}, Flags: map[string]string{}}
	idxText, err := fetch(indexHash)
	if err != nil {
		return ps, err
	}
	ix := ParseIndex(idxText)
	for _, h := range ix.MemberBlobHashes() {
		blob, err := fetch(h)
		if err != nil {
			continue
		}
		for _, m := range ScanMembers(blob) {
			ps.Roles[m.Pubkey] = m.Roles
			if m.Flags != "" {
				ps.Flags[m.Pubkey] = m.Flags
			}
		}
	}
	return ps, nil
}

// isXOnlyPubkey reports whether s is a 64-char lowercase-hex x-only pubkey.
func isXOnlyPubkey(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
