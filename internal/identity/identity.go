// Package identity manages the node's Nostr key. It is generated on first run and
// persisted as hex in <dataDir>/identity.key. An operator can instead pre-seed
// that file with their own key, as hex or as an `nsec1…` (the installer does this
// when you choose to paste your own nsec).
package identity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

type Identity struct {
	SecretKey string // hex
	PubKey    string // hex
	Nsec      string
	Npub      string
	Created   bool // true if generated this run
}

// Load returns the node identity, creating and persisting a key on first run.
func Load(dataDir string) (*Identity, error) {
	path := filepath.Join(dataDir, "identity.key")

	var sk string
	if b, err := os.ReadFile(path); err == nil {
		raw := strings.TrimSpace(string(b))
		if strings.HasPrefix(raw, "nsec1") {
			prefix, v, e := nip19.Decode(raw)
			hexsk, ok := v.(string)
			if e != nil || prefix != "nsec" || !ok {
				return nil, fmt.Errorf("identity: %s holds an invalid nsec", path)
			}
			sk = hexsk
		} else {
			sk = raw
		}
	}

	created := false
	if sk == "" {
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return nil, fmt.Errorf("identity: mkdir: %w", err)
		}
		sk = nostr.GeneratePrivateKey()
		if err := os.WriteFile(path, []byte(sk), 0o600); err != nil {
			return nil, fmt.Errorf("identity: write key: %w", err)
		}
		created = true
	}

	pub, err := nostr.GetPublicKey(sk)
	if err != nil {
		return nil, fmt.Errorf("identity: pubkey: %w", err)
	}
	nsec, _ := nip19.EncodePrivateKey(sk)
	npub, _ := nip19.EncodePublicKey(pub)

	return &Identity{SecretKey: sk, PubKey: pub, Nsec: nsec, Npub: npub, Created: created}, nil
}
