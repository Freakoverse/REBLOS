package server

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestParseHubEventMeta(t *testing.T) {
	ev := &nostr.Event{
		Kind: 36942,
		Tags: nostr.Tags{
			{"d", "my-hub"},
			{"n", "My Hub"},
			{"r", "wss://relay.example.com", "general"},
			{"o", "https://blossom1.example"},
			{"o", "https://blossom2.example", "dedicated"},
			{"m", "indexhash123", "7"},
			{"epoch", "7"},
		},
	}
	meta, ok := parseHubEventMeta(ev)
	if !ok {
		t.Fatal("expected ok")
	}
	if meta.DTag != "my-hub" || meta.IndexHash != "indexhash123" || meta.Epoch != 7 {
		t.Errorf("meta = %+v", meta)
	}
	if len(meta.BlossomServers) != 2 ||
		meta.BlossomServers[0] != "https://blossom1.example" ||
		meta.BlossomServers[1] != "https://blossom2.example" {
		t.Errorf("blossom servers = %v", meta.BlossomServers)
	}
}

func TestParseHubEventMetaNoEpoch(t *testing.T) {
	ev := &nostr.Event{
		Kind: 36942,
		Tags: nostr.Tags{{"d", "h"}, {"m", "idx"}},
	}
	meta, ok := parseHubEventMeta(ev)
	if !ok || meta.Epoch != 0 || meta.IndexHash != "idx" {
		t.Fatalf("meta = %+v ok = %v", meta, ok)
	}
}

func TestParseHubEventMetaRejects(t *testing.T) {
	cases := map[string]*nostr.Event{
		"nil":        nil,
		"wrong kind": {Kind: 1, Tags: nostr.Tags{{"d", "h"}, {"m", "idx"}}},
		"no m tag":   {Kind: 36942, Tags: nostr.Tags{{"d", "h"}}},
		"no d tag":   {Kind: 36942, Tags: nostr.Tags{{"m", "idx"}}},
	}
	for name, ev := range cases {
		if _, ok := parseHubEventMeta(ev); ok {
			t.Errorf("%s: expected ok=false", name)
		}
	}
}

func TestHubCoordinate(t *testing.T) {
	got := hubCoordinate("abc", "my-hub")
	if got != "36942:abc:my-hub" {
		t.Errorf("coordinate = %q", got)
	}
}
