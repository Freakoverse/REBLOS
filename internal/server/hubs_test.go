package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
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

func TestAdminHubsAPI(t *testing.T) {
	srv, adminSK := adminServer(t, &fakeStorage{stored: map[string][]byte{}})
	const base = "https://test.example"

	// A valid 36942 hub naddr.
	ownerSK := nostr.GeneratePrivateKey()
	ownerPK, _ := nostr.GetPublicKey(ownerSK)
	naddr, err := nip19.EncodeEntity(ownerPK, 36942, "my-hub", nil)
	if err != nil {
		t.Fatal(err)
	}
	coord := "36942:" + ownerPK + ":my-hub"

	call := func(method, path, body, sk string) *httptest.ResponseRecorder {
		var r *http.Request
		if body != "" {
			r = httptest.NewRequest(method, path, strings.NewReader(body))
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		if sk != "" {
			r.Header.Set("Authorization", nip98(t, sk, method, base+path, nostr.Now()))
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w
	}

	// Non-admin add is rejected.
	if w := call("POST", "/admin/hubs", `{"ref":"`+naddr+`"}`, nostr.GeneratePrivateKey()); w.Code != http.StatusUnauthorized {
		t.Fatalf("non-admin add: want 401, got %d", w.Code)
	}

	// Admin adds by naddr.
	w := call("POST", "/admin/hubs", `{"ref":"`+naddr+`","note":"first"}`, adminSK)
	if w.Code != http.StatusOK {
		t.Fatalf("admin add: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	var added hubView
	if err := json.Unmarshal(w.Body.Bytes(), &added); err != nil {
		t.Fatal(err)
	}
	if added.Coordinate != coord || added.Note != "first" || added.Epoch != -1 {
		t.Errorf("added = %+v", added)
	}

	// List shows it.
	w = call("GET", "/admin/hubs", "", adminSK)
	if w.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d", w.Code)
	}
	var listed struct {
		Hubs []hubView `json:"hubs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Hubs) != 1 || listed.Hubs[0].Coordinate != coord {
		t.Fatalf("list = %+v", listed.Hubs)
	}

	// Bad ref is a 400.
	if w := call("POST", "/admin/hubs", `{"ref":"not-a-hub"}`, adminSK); w.Code != http.StatusBadRequest {
		t.Fatalf("bad ref: want 400, got %d", w.Code)
	}

	// Remove it.
	if w := call("DELETE", "/admin/hubs", `{"coordinate":"`+coord+`"}`, adminSK); w.Code != http.StatusNoContent {
		t.Fatalf("remove: want 204, got %d", w.Code)
	}
	w = call("GET", "/admin/hubs", "", adminSK)
	var after struct {
		Hubs []hubView `json:"hubs"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &after)
	if len(after.Hubs) != 0 {
		t.Fatalf("after remove, list = %+v", after.Hubs)
	}
}
