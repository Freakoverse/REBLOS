package announce

import (
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestBuildEvent(t *testing.T) {
	sk := nostr.GeneratePrivateKey()
	pk, _ := nostr.GetPublicKey(sk)
	node := Node{
		URL:         "https://brs.degmods.com",
		Blossom:     true,
		Relay:       true,
		MaxUploadMB: 500,
		Kinds:       []int{31142, 30402},
		Gates:       Gates{Pow: 20},
		Version:     1,
	}

	ev, err := BuildEvent(node, sk, 1_700_000_000)
	if err != nil {
		t.Fatal(err)
	}

	if ev.Kind != AnnounceKind {
		t.Fatalf("kind = %d, want %d", ev.Kind, AnnounceKind)
	}
	if ev.PubKey != pk {
		t.Fatalf("pubkey = %s, want %s", ev.PubKey, pk)
	}
	if ok, err := ev.CheckSignature(); err != nil || !ok {
		t.Fatalf("signature invalid: ok=%v err=%v", ok, err)
	}
	if d := ev.Tags.GetD(); d != AnnounceDTag {
		t.Fatalf("d tag = %q, want %q", d, AnnounceDTag)
	}

	var got Node
	if err := json.Unmarshal([]byte(ev.Content), &got); err != nil {
		t.Fatalf("content is not the node JSON: %v", err)
	}
	if got.URL != node.URL || got.MaxUploadMB != 500 || got.Gates.Pow != 20 {
		t.Fatalf("content mismatch: %+v", got)
	}
	if len(got.Kinds) != 2 || got.Kinds[0] != 31142 {
		t.Fatalf("kinds mismatch: %v", got.Kinds)
	}
}

func TestNew_ClampsInterval(t *testing.T) {
	p := New(Node{}, "sk", []string{"wss://relay.example"}, 0)
	if p.interval.Hours() < 1 {
		t.Fatalf("interval not clamped to >= 1h: %v", p.interval)
	}
}
