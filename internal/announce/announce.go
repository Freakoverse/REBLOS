// Package announce publishes this node's capability/discovery announcement over
// Nostr so other nodes and clients can find it and learn what it accepts. This is
// the federation keystone: because blobs are content-addressed, a client that
// discovers a node can fetch any hash from it without migration.
package announce

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// AnnounceKind is a NIP-78 parameterized-replaceable event; AnnounceDTag scopes
// it so `kind:30078 #d:blossom-node-announcement` (or `#t:...`) discovers all
// nodes. Deliberately unbranded so any operator's node uses the same coordinate.
const (
	AnnounceKind = 30078
	AnnounceDTag = "blossom-node-announcement"
	AnnounceTTag = "blossom-node-announcement"
)

// Node is the advertised capability set (serialized as the event content).
type Node struct {
	URL         string `json:"url"`           // public base URL
	Blossom     bool   `json:"blossom"`       // serves blobs
	Relay       bool   `json:"relay"`         // serves the mod relay
	MaxUploadMB int    `json:"max_upload_mb"` // per-blob upload cap
	Kinds       []int  `json:"kinds"`         // accepted relay event kinds
	Gates       Gates  `json:"gates"`         // retrieval gates required
	Version     int    `json:"v"`             // announcement schema version
}

// Gates describes the download gates a client must satisfy.
type Gates struct {
	Pow int `json:"pow"` // required PoW leading-zero bits (0 = none)
}

// BuildEvent builds and signs the announcement event. createdAt is passed in so
// the result is deterministic in tests.
func BuildEvent(node Node, secretKey string, createdAt nostr.Timestamp) (nostr.Event, error) {
	content, err := json.Marshal(node)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("announce: marshal: %w", err)
	}
	ev := nostr.Event{
		Kind:      AnnounceKind,
		CreatedAt: createdAt,
		Tags: nostr.Tags{
			{"d", AnnounceDTag},
			{"t", AnnounceTTag},
			{"url", node.URL},
		},
		Content: string(content),
	}
	if err := ev.Sign(secretKey); err != nil {
		return nostr.Event{}, fmt.Errorf("announce: sign: %w", err)
	}
	return ev, nil
}

// Publisher periodically republishes the node announcement to a set of relays.
type Publisher struct {
	node     Node
	sk       string
	relays   []string
	interval time.Duration
	now      func() nostr.Timestamp // injectable for tests
}

// New builds a Publisher. interval < 1h is clamped to 1h.
func New(node Node, secretKey string, relays []string, interval time.Duration) *Publisher {
	if interval < time.Hour {
		interval = time.Hour
	}
	return &Publisher{node: node, sk: secretKey, relays: relays, interval: interval, now: nostr.Now}
}

// Run publishes immediately, then every interval, until ctx is cancelled.
func (p *Publisher) Run(ctx context.Context) {
	p.publish(ctx)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.publish(ctx)
		}
	}
}

func (p *Publisher) publish(ctx context.Context) {
	ev, err := BuildEvent(p.node, p.sk, p.now())
	if err != nil {
		slog.Error("announce build failed", "err", err)
		return
	}
	ok := 0
	for _, url := range p.relays {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		relay, err := nostr.RelayConnect(rctx, url)
		if err != nil {
			cancel()
			slog.Warn("announce connect failed", "relay", url, "err", err)
			continue
		}
		err = relay.Publish(rctx, ev)
		relay.Close()
		cancel()
		if err != nil {
			slog.Warn("announce publish failed", "relay", url, "err", err)
			continue
		}
		ok++
	}
	if ok > 0 {
		slog.Info("announce published", "relays_ok", ok, "relays_total", len(p.relays), "event", ev.ID)
	}
}
