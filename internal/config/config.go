// Package config loads node configuration from a YAML file, with env overrides
// for secrets.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen    string   `yaml:"listen"`     // e.g. ":3000"
	PublicURL string   `yaml:"public_url"` // e.g. "https://brs.degmods.com"
	DataDir   string   `yaml:"data_dir"`   // where the identity key lives
	Backend   string   `yaml:"backend"`    // storage backend: "r2" (default) | "s3" | "disk"
	R2        R2       `yaml:"r2"`
	S3        S3       `yaml:"s3"`
	Disk      Disk     `yaml:"disk"`
	Fallback  Fallback `yaml:"fallback"`
	Upload    Upload   `yaml:"upload"`
	Relay     Relay    `yaml:"relay"`
	Download  Download `yaml:"download"`
	Announce  Announce `yaml:"announce"`
	Sync      Sync     `yaml:"sync"`
	Log       Log      `yaml:"log"`
}

// Sync configures the durability sync layer (REBLOS-MIGRATION §6): per whitelisted
// hub, the node pulls that hub's events + blobs from the hub's OWN declared relay
// hints PLUS these extra relays, and stores whatever passes the (member-gated)
// write policy. It replaces the old global "ingest" mirror.
type Sync struct {
	// ExtraRelays are general relays merged into every hub's sync, on top of the
	// curated DefaultSyncRelays and each hub's own hints. They seed the persistent,
	// operator-managed sync-relay list (sync_relays.json) on first run; after that
	// the list is managed from the dashboard. Leave empty to use only the defaults.
	ExtraRelays []string `yaml:"extra_relays"`
	// IntervalMinutes is the refresh cadence (default 15, floor 1).
	IntervalMinutes int `yaml:"interval_minutes"`
}

// DefaultSyncRelays is the curated set of general Nostr relays the node pulls hub
// events + content from by default, in addition to each hub's own relay hints. It
// mirrors the DEN Chat client's default list (relays that pass a write + read-back
// health test). An operator can disable any of these or add custom relays from the
// dashboard; the choice persists in sync_relays.json.
var DefaultSyncRelays = []string{
	"wss://relay.primal.net",
	"wss://relay.damus.io",
	"wss://nos.lol",
	"wss://nostr.mom",
	"wss://wheat.happytavern.co",
	"wss://relay.snort.social",
	"wss://nostr.bitcoiner.social",
	"wss://relay.layer.systems",
	"wss://nostr.oxtr.dev",
	"wss://relay.ditto.pub",
	"wss://relay.nostr.net",
	"wss://offchain.pub",
	"wss://nostr-01.yakihonne.com",
}

// Log configures structured logging.
type Log struct {
	Level  string `yaml:"level"`  // debug | info (default) | warn | error
	Format string `yaml:"format"` // text (default) | json
}

// Announce controls the node's self-discovery announcement over Nostr, so other
// nodes and clients can find it and learn what it accepts (federation).
type Announce struct {
	Enabled       bool     `yaml:"enabled"`
	Relays        []string `yaml:"relays"`         // relays to publish the announcement to
	IntervalHours int      `yaml:"interval_hours"` // republish cadence in hours (default 168 = weekly)
}

type Download struct {
	PoWDifficulty   int    `yaml:"pow_difficulty"`    // BUD-POW leading-zero bits; 0 = gate off
	ChallengeTTL    int    `yaml:"challenge_ttl"`     // seconds a challenge stays valid (default 600)
	TrustedIPHeader string `yaml:"trusted_ip_header"` // e.g. "CF-Connecting-IP" or "X-Forwarded-For"; empty = socket IP
}

type Relay struct {
	AdminNpub   string `yaml:"admin_npub"`    // npub allowed to use the NIP-86 management API
	MinEventPoW int    `yaml:"min_event_pow"` // NIP-13 bits required on accepted events (0 = off)
	// ExtraKinds are additional event kinds to accept beyond the built-in NIP-CHAT
	// hub set (hub event, member-content kinds, join requests), e.g. DM kinds or
	// a custom kind. They are NOT member-gated; they pass the kind allowlist and
	// the min_event_pow floor. Hub member/role gating applies only to the built-in
	// hub-content kinds.
	ExtraKinds []int `yaml:"extra_kinds"`
	// AcceptAllKinds drops the kind allowlist AND the hub member gate: any kind,
	// from anyone. For a general-purpose fork. The remaining protections (banned
	// pubkeys, event takedowns, honored deletions, min_event_pow) still apply, so
	// set min_event_pow when enabling this.
	AcceptAllKinds bool `yaml:"accept_all_kinds"`
}

type Upload struct {
	MaxSizeMB         int    `yaml:"max_size_mb"`          // hard cap per blob (default 500)
	MaxConcurrent     int    `yaml:"max_concurrent"`       // global simultaneous uploads (default 4)
	TempDir           string `yaml:"temp_dir"`             // temp spool dir (default: OS temp)
	MinPoW            int    `yaml:"min_pow"`              // NIP-13 bits required on the 24242 auth event (0 = off)
	MinFreeDiskMB     int64  `yaml:"min_free_disk_mb"`     // refuse uploads below this free space (default 1024)
	IdleTimeoutSec    int    `yaml:"idle_timeout_sec"`     // abort an upload after this many seconds with no data (default 60)
	MinUploadRateKBps int    `yaml:"min_upload_rate_kbps"` // abort an upload averaging below this over a 5s window (default 50; negative = off)
	// Accepted file extensions, detected by magic bytes / text sniffing (default
	// DefaultUploadTypes: text plus common image/audio/video). Use ["*"] to allow
	// any type. `txt` MUST stay allowed or hubs cannot store their member tree.
	AllowedTypes []string `yaml:"allowed_types"`
}

// DefaultUploadTypes is the out-of-the-box upload allowlist for a NIP-CHAT node:
// text (the member-tree / struct blobs, which are text/plain) plus the common web
// media a hub's messages carry. Archives/executables are NOT accepted by default;
// add them to allowed_types if a deployment needs them.
var DefaultUploadTypes = []string{
	"txt",
	"png", "jpg", "gif", "webp", "bmp", // images
	"mp3", "ogg", "wav", "flac", // audio
	"mp4", "webm", // video
	"pdf",
}

type R2 struct {
	Endpoint  string `yaml:"endpoint"`
	Region    string `yaml:"region"`
	Bucket    string `yaml:"bucket"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	UseSSL    bool   `yaml:"use_ssl"`
}

// S3 configures a self-hosted S3-compatible backend (Garage, Ceph, SeaweedFS, …).
// Like R2 it is content-addressed; most self-hosted servers want path-style URLs.
type S3 struct {
	Endpoint  string `yaml:"endpoint"` // host[:port], no scheme
	Region    string `yaml:"region"`   // default "us-east-1"
	Bucket    string `yaml:"bucket"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	UseSSL    bool   `yaml:"use_ssl"`
	PathStyle bool   `yaml:"path_style"` // most self-hosted S3 servers need this
}

// Disk configures the local-disk backend (own hardware, no S3 at all).
type Disk struct {
	Path string `yaml:"path"` // blob root; default "<data_dir>/blobs"
}

// Fallback is a second, read-only backend kept alongside the primary while
// re-routing storage (REBLOS-MIGRATION §4). When Backend is set, the node reads the
// primary first then this backend, so blobs not yet migrated off the old backend are
// still served; new writes go only to the primary. Run a migration (admin API /
// dashboard Storage tab) to copy the backlog into the primary, then clear this.
// Its secrets come from FALLBACK_R2_ACCESS_KEY / FALLBACK_R2_SECRET_KEY (or the
// FALLBACK_S3_* pair), keeping them out of the file like the primary's.
type Fallback struct {
	Backend string `yaml:"backend"` // "" (none), "r2", "s3", or "disk"
	R2      R2     `yaml:"r2"`
	S3      S3     `yaml:"s3"`
	Disk    Disk   `yaml:"disk"`
}

// Load reads and validates config. Env vars R2_ACCESS_KEY / R2_SECRET_KEY
// override the file so secrets can stay out of it.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("config: parse: %w", err)
	}

	if c.Listen == "" {
		c.Listen = ":3000"
	}
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	if c.Backend == "" {
		c.Backend = "r2"
	}
	if c.R2.Region == "" {
		c.R2.Region = "auto"
	}
	if c.S3.Region == "" {
		c.S3.Region = "us-east-1"
	}
	if c.Disk.Path == "" {
		c.Disk.Path = filepath.Join(c.DataDir, "blobs")
	}
	if c.Upload.MaxSizeMB == 0 {
		c.Upload.MaxSizeMB = 500
	}
	if c.Upload.MaxConcurrent == 0 {
		c.Upload.MaxConcurrent = 4
	}
	if c.Upload.TempDir == "" {
		c.Upload.TempDir = os.TempDir()
	}
	if c.Upload.MinFreeDiskMB == 0 {
		c.Upload.MinFreeDiskMB = 1024
	}
	if c.Upload.IdleTimeoutSec == 0 {
		c.Upload.IdleTimeoutSec = 60
	}
	if c.Upload.MinUploadRateKBps == 0 {
		c.Upload.MinUploadRateKBps = 50
	}
	if len(c.Upload.AllowedTypes) == 0 {
		c.Upload.AllowedTypes = DefaultUploadTypes
	}
	if c.Download.ChallengeTTL == 0 {
		c.Download.ChallengeTTL = 600
	}
	if c.Announce.IntervalHours == 0 {
		c.Announce.IntervalHours = 168 // weekly
	}
	if c.Sync.IntervalMinutes == 0 {
		c.Sync.IntervalMinutes = 15
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.Format == "" {
		c.Log.Format = "text"
	}
	if v := os.Getenv("R2_ACCESS_KEY"); v != "" {
		c.R2.AccessKey = v
	}
	if v := os.Getenv("R2_SECRET_KEY"); v != "" {
		c.R2.SecretKey = v
	}
	if v := os.Getenv("S3_ACCESS_KEY"); v != "" {
		c.S3.AccessKey = v
	}
	if v := os.Getenv("S3_SECRET_KEY"); v != "" {
		c.S3.SecretKey = v
	}
	if v := os.Getenv("FALLBACK_R2_ACCESS_KEY"); v != "" {
		c.Fallback.R2.AccessKey = v
	}
	if v := os.Getenv("FALLBACK_R2_SECRET_KEY"); v != "" {
		c.Fallback.R2.SecretKey = v
	}
	if v := os.Getenv("FALLBACK_S3_ACCESS_KEY"); v != "" {
		c.Fallback.S3.AccessKey = v
	}
	if v := os.Getenv("FALLBACK_S3_SECRET_KEY"); v != "" {
		c.Fallback.S3.SecretKey = v
	}

	if c.PublicURL == "" {
		return nil, fmt.Errorf("config: public_url required")
	}
	switch c.Backend {
	case "r2":
		if c.R2.Endpoint == "" || c.R2.Bucket == "" {
			return nil, fmt.Errorf("config: r2.endpoint and r2.bucket required")
		}
		if c.R2.AccessKey == "" || c.R2.SecretKey == "" {
			return nil, fmt.Errorf("config: r2 credentials required (set in config or R2_ACCESS_KEY/R2_SECRET_KEY)")
		}
	case "s3":
		if c.S3.Endpoint == "" || c.S3.Bucket == "" {
			return nil, fmt.Errorf("config: s3.endpoint and s3.bucket required")
		}
		if c.S3.AccessKey == "" || c.S3.SecretKey == "" {
			return nil, fmt.Errorf("config: s3 credentials required (set in config or S3_ACCESS_KEY/S3_SECRET_KEY)")
		}
	case "disk":
		// disk.path is defaulted above; nothing else required.
	default:
		return nil, fmt.Errorf("config: unknown backend %q (want r2, s3, or disk)", c.Backend)
	}

	// Optional read-fallback backend (used while re-routing storage).
	if c.Fallback.Backend != "" {
		if c.Fallback.R2.Region == "" {
			c.Fallback.R2.Region = "auto"
		}
		if c.Fallback.S3.Region == "" {
			c.Fallback.S3.Region = "us-east-1"
		}
		switch c.Fallback.Backend {
		case "r2":
			if c.Fallback.R2.Endpoint == "" || c.Fallback.R2.Bucket == "" {
				return nil, fmt.Errorf("config: fallback.r2.endpoint and fallback.r2.bucket required")
			}
			if c.Fallback.R2.AccessKey == "" || c.Fallback.R2.SecretKey == "" {
				return nil, fmt.Errorf("config: fallback r2 credentials required (set FALLBACK_R2_ACCESS_KEY/FALLBACK_R2_SECRET_KEY)")
			}
		case "s3":
			if c.Fallback.S3.Endpoint == "" || c.Fallback.S3.Bucket == "" {
				return nil, fmt.Errorf("config: fallback.s3.endpoint and fallback.s3.bucket required")
			}
			if c.Fallback.S3.AccessKey == "" || c.Fallback.S3.SecretKey == "" {
				return nil, fmt.Errorf("config: fallback s3 credentials required (set FALLBACK_S3_ACCESS_KEY/FALLBACK_S3_SECRET_KEY)")
			}
		case "disk":
			if c.Fallback.Disk.Path == "" {
				return nil, fmt.Errorf("config: fallback.disk.path required")
			}
		default:
			return nil, fmt.Errorf("config: unknown fallback.backend %q (want r2, s3, or disk)", c.Fallback.Backend)
		}
		if c.Fallback.Backend == c.Backend && sameBackendTarget(&c) {
			return nil, fmt.Errorf("config: fallback must point at a different location than the primary")
		}
	}
	return &c, nil
}

// sameBackendTarget reports whether the fallback resolves to the same physical
// location as the primary (which would make a migration a no-op loop). Only a rough
// guard on the obvious same-bucket / same-path mistake.
func sameBackendTarget(c *Config) bool {
	switch c.Backend {
	case "r2":
		return c.R2.Endpoint == c.Fallback.R2.Endpoint && c.R2.Bucket == c.Fallback.R2.Bucket
	case "s3":
		return c.S3.Endpoint == c.Fallback.S3.Endpoint && c.S3.Bucket == c.Fallback.S3.Bucket
	case "disk":
		return c.Disk.Path == c.Fallback.Disk.Path
	}
	return false
}
