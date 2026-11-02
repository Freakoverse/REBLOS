package server

import (
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

const (
	refHashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	refHashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	refHashC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func TestExtractHashesFromModEvent(t *testing.T) {
	b := newBlobRefs("https://brs.degmods.com")

	evt := &nostr.Event{
		Kind:    currentModKind,
		Content: "check the screenshot https://brs.degmods.com/" + refHashC + ".png here",
		Tags: nostr.Tags{
			{"d", "some-mod"},
			{"title", "Cool Mod"},
			// download tag: a JSON blob carrying the file URL (as the client writes it)
			{"download", `{"file":"https://brs.degmods.com/` + refHashA + `.zip","hash":"` + refHashA + `"}`},
			{"image", "https://brs.degmods.com/" + refHashB + ".png"},
			// a file on a DIFFERENT host must NOT be attributed to us
			{"screenshots", "https://other.example.com/" + strings.Repeat("d", 64) + ".png"},
		},
	}

	got := map[string]bool{}
	for _, h := range b.extractHashes(evt) {
		got[h] = true
	}
	for _, want := range []string{refHashA, refHashB, refHashC} {
		if !got[want] {
			t.Errorf("expected hash %s to be extracted", want)
		}
	}
	if len(got) != 3 {
		t.Fatalf("extracted %d hashes, want 3 (off-host URL must be ignored): %v", len(got), got)
	}
}

func TestNoteAndRefsFor(t *testing.T) {
	b := newBlobRefs("https://brs.degmods.com")

	evt := &nostr.Event{
		Kind: currentModKind,
		Tags: nostr.Tags{
			{"d", "mod-1"},
			{"title", "First Mod"},
			{"download", `{"file":"https://brs.degmods.com/` + refHashA + `.zip"}`},
		},
	}
	b.note(evt)
	b.note(evt) // idempotent — same coord must not double-count

	refs := b.refsFor(refHashA)
	if len(refs) != 1 {
		t.Fatalf("refsFor(A) = %d refs, want 1", len(refs))
	}
	if refs[0].Title != "First Mod" || refs[0].Coord != currentModKindCoord("mod-1", evt.PubKey) {
		t.Fatalf("unexpected ref: %+v", refs[0])
	}

	// An unrelated, unreferenced blob is unclaimed.
	if refs := b.refsFor(refHashB); refs != nil {
		t.Fatalf("refsFor(B) = %v, want nil (unclaimed)", refs)
	}

	// A second mod referencing the same blob adds a distinct ref.
	evt2 := &nostr.Event{
		Kind: currentModKind,
		Tags: nostr.Tags{
			{"d", "mod-2"},
			{"title", "Second Mod"},
			{"download", `{"file":"https://brs.degmods.com/` + refHashA + `.zip"}`},
		},
	}
	b.note(evt2)
	if refs := b.refsFor(refHashA); len(refs) != 2 {
		t.Fatalf("refsFor(A) = %d refs after second mod, want 2", len(refs))
	}
}

func TestRefsCappedPerBlob(t *testing.T) {
	b := newBlobRefs("https://brs.degmods.com")
	// Far more distinct mods reference the same blob than the cap allows.
	for i := 0; i < maxRefsPerBlob+25; i++ {
		b.note(&nostr.Event{
			Kind: currentModKind,
			Tags: nostr.Tags{
				{"d", "mod-" + itoa(i)},
				{"download", `{"file":"https://brs.degmods.com/` + refHashA + `.zip"}`},
			},
		})
	}
	if got := len(b.refsFor(refHashA)); got != maxRefsPerBlob {
		t.Fatalf("refsFor(A) = %d, want capped at %d", got, maxRefsPerBlob)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func currentModKindCoord(d, pk string) string {
	return coordOf(&nostr.Event{Kind: currentModKind, PubKey: pk, Tags: nostr.Tags{{"d", d}}})
}
