package hub

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Valid 64-char x-only hex pubkeys for fixtures.
const (
	pkA = "dd664d5e8d1e55a3f1b2c3d4e5f60718293a4b5c6d7e8f901122334455667788"
	pkB = "0011223344556677889900aabbccddeeff00112233445566778899aabbccddee"
	pkC = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
)

// leafLine mirrors the client's serializeLeafLine
// (leaf:nodeId:pubkey:roles:encKey[:flags]). encKey is NIP-04 base64, no colon.
func leafLine(nodeID, pubkey, roles, encKey string, flags ...string) string {
	parts := []string{"leaf", nodeID, pubkey, roles, encKey}
	parts = append(parts, flags...)
	return strings.Join(parts, ":")
}

func TestScanMembers(t *testing.T) {
	blob := strings.Join([]string{
		leafLine("n0", pkA, "everyone", "AAAA==?iv=BBBB=="),
		leafLine("n1", pkB, "mod", "CCCC==?iv=DDDD==", "w"), // whitelist flag
		"node:n2:n0:n1:EEEE==:FFFF==",                       // internal node, ignored
		"page-root:r0:n2:n2:GGGG==:GGGG==",                  // ignored
		"roster:7:HHHHencryptedAESblob",                     // ignored (encrypted P->R)
		leafLine("n3", "nothex", "everyone", "IIII=="),      // bad pubkey -> skipped
		"leaf:n4:tooShort",                                  // malformed -> skipped
		"",                                                  // blank -> skipped
	}, "\n")

	got := ScanMembers(blob)
	if len(got) != 2 {
		t.Fatalf("got %d members, want 2: %+v", len(got), got)
	}
	if got[0].Pubkey != pkA || got[0].Roles != "everyone" || got[0].Flags != "" {
		t.Errorf("member 0 = %+v", got[0])
	}
	if got[1].Pubkey != pkB || got[1].Roles != "mod" || got[1].Flags != "w" {
		t.Errorf("member 1 = %+v", got[1])
	}
}

func TestParseIndexPaginated(t *testing.T) {
	// Mirrors createPaginatedIndexFile.
	content := strings.Join([]string{
		"meta:page_size=10000",
		"spine:spinehash111",
		"leaf-page:0:" + pkB + ":page0hash", // sorted by firstPubkey (pkB < pkA)
		"leaf-page:1:" + pkA + ":page1hash",
		"bans:0:banhash@7",
		"history:histhash",
	}, "\n")

	ix := ParseIndex(content)
	if !ix.Paginated() {
		t.Fatal("expected paginated index")
	}
	if ix.PageSize != 10000 {
		t.Errorf("page_size = %d, want 10000", ix.PageSize)
	}
	if ix.SpineHash != "spinehash111" {
		t.Errorf("spine = %q", ix.SpineHash)
	}
	if len(ix.LeafPages) != 2 {
		t.Fatalf("leaf pages = %d, want 2", len(ix.LeafPages))
	}
	hashes := ix.MemberBlobHashes()
	if len(hashes) != 2 || hashes[0] != "page0hash" || hashes[1] != "page1hash" {
		t.Errorf("member blob hashes = %v", hashes)
	}
	if ix.TreeHash != "" {
		t.Errorf("monolithic tree hash should be empty, got %q", ix.TreeHash)
	}
}

func TestParseIndexMonolithic(t *testing.T) {
	// Mirrors createIndexFile.
	content := strings.Join([]string{
		"tree:treehash999",
		"history:histhash",
	}, "\n")

	ix := ParseIndex(content)
	if ix.Paginated() {
		t.Fatal("expected monolithic index")
	}
	if ix.TreeHash != "treehash999" {
		t.Errorf("tree hash = %q", ix.TreeHash)
	}
	if h := ix.MemberBlobHashes(); len(h) != 1 || h[0] != "treehash999" {
		t.Errorf("member blob hashes = %v", h)
	}
}

func TestBuildPosterSetPaginated(t *testing.T) {
	blobs := map[string]string{
		"idx": strings.Join([]string{
			"meta:page_size=10000",
			"spine:spine0",
			"leaf-page:0:" + pkB + ":page0",
			"leaf-page:1:" + pkA + ":page1",
		}, "\n"),
		"page0": leafLine("n0", pkB, "everyone", "AA==?iv=BB=="),
		"page1": strings.Join([]string{
			leafLine("n1", pkA, "owner", "CC==?iv=DD==", "w"),
			leafLine("n2", pkC, "everyone", "EE==?iv=FF=="),
		}, "\n"),
	}
	fetch := func(h string) (string, error) {
		b, ok := blobs[h]
		if !ok {
			return "", fmt.Errorf("no blob %s", h)
		}
		return b, nil
	}

	ps, err := BuildPosterSet("idx", fetch)
	if err != nil {
		t.Fatal(err)
	}
	if ps.Len() != 3 {
		t.Fatalf("poster set size = %d, want 3", ps.Len())
	}
	for _, pk := range []string{pkA, pkB, pkC} {
		if !ps.Has(pk) {
			t.Errorf("poster set missing %s", pk)
		}
	}
	if ps.Roles[pkA] != "owner" {
		t.Errorf("pkA role = %q, want owner", ps.Roles[pkA])
	}
	if ps.Flags[pkA] != "w" {
		t.Errorf("pkA flags = %q, want w", ps.Flags[pkA])
	}
	if _, ok := ps.Flags[pkB]; ok {
		t.Errorf("pkB should carry no flags")
	}
}

func TestBuildPosterSetMonolithic(t *testing.T) {
	blobs := map[string]string{
		"idx": "tree:thetree",
		"thetree": strings.Join([]string{
			leafLine("n0", pkA, "everyone", "AA==?iv=BB=="),
			leafLine("n1", pkB, "mod", "CC==?iv=DD=="),
			"root:r0:n0:n1:XX==:YY==:ZZ==", // root line with hub secret, ignored
		}, "\n"),
	}
	fetch := func(h string) (string, error) { return blobs[h], nil }

	ps, err := BuildPosterSet("idx", fetch)
	if err != nil {
		t.Fatal(err)
	}
	if ps.Len() != 2 || !ps.Has(pkA) || !ps.Has(pkB) {
		t.Fatalf("poster set = %+v", ps.Roles)
	}
}

func TestBuildPosterSetToleratesMissingPage(t *testing.T) {
	blobs := map[string]string{
		"idx": strings.Join([]string{
			"meta:page_size=10000",
			"spine:spine0",
			"leaf-page:0:" + pkA + ":page0",
			"leaf-page:1:" + pkB + ":missingpage",
		}, "\n"),
		"page0": leafLine("n0", pkA, "everyone", "AA==?iv=BB=="),
		// "missingpage" absent -> fetch error; should be skipped, not fatal.
	}
	fetch := func(h string) (string, error) {
		b, ok := blobs[h]
		if !ok {
			return "", errors.New("missing")
		}
		return b, nil
	}

	ps, err := BuildPosterSet("idx", fetch)
	if err != nil {
		t.Fatal(err)
	}
	if ps.Len() != 1 || !ps.Has(pkA) {
		t.Fatalf("expected only pkA, got %+v", ps.Roles)
	}
}

func TestBuildPosterSetIndexFetchError(t *testing.T) {
	fetch := func(string) (string, error) { return "", errors.New("down") }
	if _, err := BuildPosterSet("idx", fetch); err == nil {
		t.Fatal("expected a hard error when the index cannot be fetched")
	}
}
