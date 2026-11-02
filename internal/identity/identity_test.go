package identity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

func TestLoad_GeneratesOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	id, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !id.Created || id.SecretKey == "" || id.Npub == "" {
		t.Fatalf("first run should generate a key: %+v", id)
	}
	// Second load reuses the same key and is not "created".
	id2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if id2.Created || id2.SecretKey != id.SecretKey {
		t.Fatalf("second load should reuse the key: %+v", id2)
	}
}

func TestLoad_AcceptsHexAndNsec(t *testing.T) {
	sk := nostr.GeneratePrivateKey()
	pub, _ := nostr.GetPublicKey(sk)
	nsec, _ := nip19.EncodePrivateKey(sk)

	// Pre-seeded as hex.
	hexDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(hexDir, "identity.key"), []byte(sk+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if id, err := Load(hexDir); err != nil || id.SecretKey != sk || id.PubKey != pub || id.Created {
		t.Fatalf("hex seed: id=%+v err=%v", id, err)
	}

	// Pre-seeded as nsec -> decoded to the same hex key.
	nsecDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(nsecDir, "identity.key"), []byte(nsec), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := Load(nsecDir)
	if err != nil {
		t.Fatal(err)
	}
	if id.SecretKey != sk || id.PubKey != pub || id.Created {
		t.Fatalf("nsec seed did not decode to the same key: id=%+v", id)
	}
}

func TestLoad_RejectsBadNsec(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "identity.key"), []byte("nsec1garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("an invalid nsec seed must be a hard error, not a silent regenerate")
	}
}
